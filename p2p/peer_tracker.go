package p2p

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	libpeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
)

type peerTracker struct {
	host       host.Host
	connGater  *conngater.BasicConnectionGater
	metrics    *exchangeMetrics
	protocolID protocol.ID
	peerLk     sync.RWMutex
	// trackedPeers contains active peers that we can request to.
	// we cache the peer once they disconnect,
	// so we can guarantee that peerQueue will only contain active peers
	trackedPeers map[libpeer.ID]struct{}

	// an optional interface used to periodically dump
	// good peers during garbage collection
	pidstore PeerIDStore

	ctx    context.Context
	cancel context.CancelFunc
	// done is used to gracefully stop the peerTracker.
	// It allows to wait until track() will be stopped.
	done chan struct{}
}

func newPeerTracker(
	h host.Host,
	connGater *conngater.BasicConnectionGater,
	networkID string,
	pidstore PeerIDStore,
	metrics *exchangeMetrics,
) *peerTracker {
	ctx, cancel := context.WithCancel(context.Background())
	return &peerTracker{
		host:         h,
		connGater:    connGater,
		protocolID:   protocolID(networkID),
		metrics:      metrics,
		trackedPeers: make(map[libpeer.ID]struct{}),
		pidstore:     pidstore,
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
}

// bootstrap will initiate connections to the given trusted peers and if
// a pidstore was given, will also attempt to bootstrap the tracker with previously
// seen peers.
//
// NOTE: bootstrap is intended to be used with an on-disk peerstore.Peerstore as
// the peerTracker needs access to the previously-seen peers' AddrInfo on start.
func (p *peerTracker) bootstrap(trusted []libpeer.ID) error {
	// store peers that have been already connected
	for _, c := range p.host.Network().Conns() {
		go p.connected(c.RemotePeer())
	}
	for _, trust := range trusted {
		go p.connectToPeer(p.ctx, trust)
	}

	// short-circuit if pidstore was not provided
	if p.pidstore == nil {
		return nil
	}

	prevSeen, err := p.pidstore.Load(p.ctx)
	if err != nil {
		return err
	}

	for _, peer := range prevSeen {
		go p.connectToPeer(p.ctx, peer)
	}
	return nil
}

// connectToPeer attempts to connect to the given peer.
func (p *peerTracker) connectToPeer(ctx context.Context, peer libpeer.ID) {
	err := p.host.Connect(ctx, p.host.Peerstore().PeerInfo(peer))
	if err != nil {
		log.Debugw("failed to connect to peer", "id", peer.String(), "err", err)
		return
	}
}

func (p *peerTracker) track() {
	defer func() {
		p.done <- struct{}{}
	}()

	connSubs, err := p.host.EventBus().Subscribe(&event.EvtPeerConnectednessChanged{})
	if err != nil {
		log.Errorw("subscribing to EvtPeerConnectednessChanged", "err", err)
		return
	}

	identifySub, err := p.host.EventBus().Subscribe(&event.EvtPeerIdentificationCompleted{})
	if err != nil {
		log.Errorw("subscribing to EvtPeerIdentificationCompleted", "err", err)
		return
	}

	protocolSub, err := p.host.EventBus().Subscribe(&event.EvtPeerProtocolsUpdated{})
	if err != nil {
		log.Errorw("subscribing to EvtPeerProtocolsUpdated", "err", err)
		return
	}

	for {
		select {
		case <-p.ctx.Done():
			err = connSubs.Close()
			errors.Join(err, identifySub.Close(), protocolSub.Close())
			if err != nil {
				log.Errorw("closing subscriptions", "err", err)
			}
			return
		case connSubscription := <-connSubs.Out():
			ev := connSubscription.(event.EvtPeerConnectednessChanged)
			if network.NotConnected == ev.Connectedness {
				p.disconnected(ev.Peer)
			}
		case subscription := <-identifySub.Out():
			ev := subscription.(event.EvtPeerIdentificationCompleted)
			p.connected(ev.Peer)
		case subscription := <-protocolSub.Out():
			ev := subscription.(event.EvtPeerProtocolsUpdated)
			if slices.Contains(ev.Removed, p.protocolID) {
				p.disconnected(ev.Peer)
				break
			}
			p.connected(ev.Peer)
		}
	}
}

// getPeers returns the tracker's currently tracked peers up to the `max`.
func (p *peerTracker) getPeers(max int) []libpeer.ID {
	p.peerLk.RLock()
	defer p.peerLk.RUnlock()

	peers := make([]libpeer.ID, 0, max)
	for peer := range p.trackedPeers {
		peers = append(peers, peer)
		if len(peers) == max {
			break
		}
	}
	return peers
}

func (p *peerTracker) connected(pID libpeer.ID) {
	if err := pID.Validate(); err != nil {
		return
	}

	if p.host.ID() == pID {
		return
	}

	// check that peer supports our protocol id.
	protocol, err := p.host.Peerstore().SupportsProtocols(pID, p.protocolID)
	if err != nil {
		return
	}
	if !slices.Contains(protocol, p.protocolID) {
		return
	}

	for _, c := range p.host.Network().ConnsToPeer(pID) {
		// check if connection is short-termed and skip this peer
		if c.Stat().Limited {
			return
		}
	}

	p.peerLk.Lock()
	defer p.peerLk.Unlock()
	if _, ok := p.trackedPeers[pID]; ok {
		return
	}

	log.Debugw("connected to peer", "id", pID.String())
	p.trackedPeers[pID] = struct{}{}

	p.metrics.peersTracked(1)
}

func (p *peerTracker) disconnected(pID libpeer.ID) {
	if err := pID.Validate(); err != nil {
		return
	}

	p.peerLk.Lock()
	defer p.peerLk.Unlock()
	if _, ok := p.trackedPeers[pID]; !ok {
		return
	}
	delete(p.trackedPeers, pID)

	p.host.ConnManager().UntagPeer(pID, string(p.protocolID))

	p.metrics.peersTracked(-1)
	p.metrics.peersDisconnected(1)
}

func (p *peerTracker) peers() []*peerStat {
	p.peerLk.RLock()
	defer p.peerLk.RUnlock()

	peers := make([]*peerStat, 0)
	for peerID := range p.trackedPeers {
		score := 0
		if info := p.host.ConnManager().GetTagInfo(peerID); info != nil {
			score = info.Tags[string(p.protocolID)]
		}
		peers = append(peers, &peerStat{peerID: peerID, peerScore: score})
	}
	return peers
}

// dumpPeers stores peers to the peerTracker's PeerIDStore if
// present.
func (p *peerTracker) dumpPeers(ctx context.Context) {
	if p.pidstore == nil {
		return
	}

	peers := make([]libpeer.ID, 0, len(p.trackedPeers))

	p.peerLk.RLock()
	for id := range p.trackedPeers {
		peers = append(peers, id)
	}
	p.peerLk.RUnlock()

	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	err := p.pidstore.Put(ctx, peers)
	if err != nil {
		log.Errorw("failed to dump tracked peers to PeerIDStore", "err", err)
		return
	}
	log.Debugw("dumped peers to PeerIDStore", "amount", len(peers))
}

// stop waits until all background routines will be finished.
func (p *peerTracker) stop(ctx context.Context) error {
	p.cancel()

	select {
	case <-p.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	// dump remaining tracked peers
	p.dumpPeers(ctx)
	return nil
}

// blockPeer blocks a peer on the networking level and removes it from the local cache.
func (p *peerTracker) blockPeer(pID libpeer.ID, reason error) {
	if err := pID.Validate(); err != nil {
		return
	}

	// add peer to the blacklist, so we can't connect to it in the future.
	err := p.connGater.BlockPeer(pID)
	if err != nil {
		log.Errorw("header/p2p: blocking peer failed", "pID", pID, "err", err)
	}
	// close connections to peer.
	err = p.host.Network().ClosePeer(pID)
	if err != nil {
		log.Errorw("header/p2p: closing connection with peer failed", "pID", pID, "err", err)
	}

	log.Warnw("header/p2p: blocked peer", "pID", pID, "reason", reason)
	p.metrics.peerBlocked()
}

func (p *peerTracker) updateScore(stats *peerStat, size uint64, duration time.Duration) {
	score := stats.updateStats(size, duration)
	p.host.ConnManager().TagPeer(stats.peerID, string(p.protocolID), score)
}
