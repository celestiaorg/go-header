package p2p

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	libpeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
)

const (
	// defaultScore specifies the score for newly connected peers.
	defaultScore float32 = 1
	// maxTrackerSize specifies the max amount of peers that can be added to the peerTracker.
	maxPeerTrackerSize = 100
)

var (
	// maxAwaitingTime specifies the duration that gives to the disconnected peer to be back online,
	// otherwise it will be removed on the next GC cycle.
	maxAwaitingTime = time.Hour
	// gcCycle defines the duration after which the peerTracker starts removing peers.
	gcCycle = time.Minute * 5
)

type peerTracker struct {
	host      host.Host
	connGater *conngater.BasicConnectionGater

	peerLk sync.RWMutex
	// trackedPeers contains active peers that we can request to.
	// we cache the peer once they disconnect,
	// so we can guarantee that peerQueue will only contain active peers
	trackedPeers map[libpeer.ID]*peerStat
	// disconnectedPeers contains disconnected peers. In case if peer does not return
	// online until pruneDeadline, it will be removed and its score will be lost
	disconnectedPeers map[libpeer.ID]*peerStat

	// an optional interface used to periodically dump
	// good peers during garbage collection
	pidstore PeerIDStore

	ctx    context.Context
	cancel context.CancelFunc
	// done is used to gracefully stop the peerTracker.
	// It allows to wait until track() and gc() will be stopped.
	done chan struct{}
}

func newPeerTracker(
	h host.Host,
	connGater *conngater.BasicConnectionGater,
	pidstore PeerIDStore,
) *peerTracker {
	ctx, cancel := context.WithCancel(context.Background())
	return &peerTracker{
		host:              h,
		connGater:         connGater,
		trackedPeers:      make(map[libpeer.ID]*peerStat),
		disconnectedPeers: make(map[libpeer.ID]*peerStat),
		pidstore:          pidstore,
		ctx:               ctx,
		cancel:            cancel,
		done:              make(chan struct{}, 2),
	}
}

// bootstrap will bootstrap the peerTracker with the given trusted peers and if
// a pidstore was given, will also attempt to bootstrap the tracker with previously
// seen peers.
//
// NOTE: bootstrap is intended to be used with an on-disk peerstore.Peerstore as
// the peerTracker needs access to the previously-seen peers' AddrInfo on start.
func (p *peerTracker) bootstrap(ctx context.Context, trusted []libpeer.ID) error {
	// bootstrap connections to trusted
	for _, trust := range trusted {
		go p.connectToPeer(ctx, trust)
	}

	// short-circuit if pidstore was not provided
	if p.pidstore == nil {
		return nil
	}

	prevSeen, err := p.pidstore.Load(ctx)
	if err != nil {
		return err
	}

	for _, peer := range prevSeen {
		go p.connectToPeer(ctx, peer)
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
	log.Debugw("connected to peer", "id", peer.String())
}

func (p *peerTracker) track() {
	defer func() {
		p.done <- struct{}{}
	}()

	// store peers that have been already connected
	for _, c := range p.host.Network().Conns() {
		p.connected(c.RemotePeer())
	}

	subs, err := p.host.EventBus().Subscribe(&event.EvtPeerConnectednessChanged{})
	if err != nil {
		log.Errorw("subscribing to EvtPeerConnectednessChanged", "err", err)
		return
	}

	for {
		select {
		case <-p.ctx.Done():
			err = subs.Close()
			if err != nil {
				log.Errorw("closing subscription", "err", err)
			}
			return
		case subscription := <-subs.Out():
			ev := subscription.(event.EvtPeerConnectednessChanged)
			switch ev.Connectedness {
			case network.Connected:
				p.connected(ev.Peer)
			case network.NotConnected:
				p.disconnected(ev.Peer)
			}
		}
	}
}

// getPeers returns the tracker's currently tracked peers.
func (p *peerTracker) getPeers(ctx context.Context, num int) ([]libpeer.ID, error) {
	p.peerLk.RLock()
	tracked := p.trackedPeers
	p.peerLk.RUnlock()

	if len(tracked) >= num {
		peers := make([]libpeer.ID, 0, num)
		for peer := range tracked {
			peers = append(peers, peer)
			if len(peers) == num {
				return peers, nil
			}
		}
	}

	return p.waitForPeers(ctx, num)
}

// waitForPeers blocks while waiting the given number of peers to be
// populated in the tracker.
func (p *peerTracker) waitForPeers(ctx context.Context, num int) ([]libpeer.ID, error) {
	ticker := time.NewTicker(time.Millisecond * 250)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			p.peerLk.RLock()
			enoughInTracker := len(p.trackedPeers) >= num
			p.peerLk.RUnlock()

			if enoughInTracker {
				return p.getPeers(ctx, num)
			}
		}
	}
}

func (p *peerTracker) connected(pID libpeer.ID) {
	if p.host.ID() == pID {
		return
	}

	for _, c := range p.host.Network().ConnsToPeer(pID) {
		// check if connection is short-termed and skip this peer
		if c.Stat().Transient {
			return
		}
	}

	p.peerLk.Lock()
	defer p.peerLk.Unlock()
	// skip adding the peer to avoid overfilling of the peerTracker with unused peers if:
	// peerTracker reaches the maxTrackerSize and there are more connected peers
	// than disconnected peers.
	if len(p.trackedPeers)+len(p.disconnectedPeers) > maxPeerTrackerSize &&
		len(p.trackedPeers) > len(p.disconnectedPeers) {
		return
	}

	// additional check in p.trackedPeers should be done,
	// because libp2p does not emit multiple Connected events per 1 peer
	stats, ok := p.disconnectedPeers[pID]
	if !ok {
		stats = &peerStat{peerID: pID, peerScore: defaultScore}
	} else {
		delete(p.disconnectedPeers, pID)
	}
	p.trackedPeers[pID] = stats
}

func (p *peerTracker) disconnected(pID libpeer.ID) {
	p.peerLk.Lock()
	defer p.peerLk.Unlock()
	stats, ok := p.trackedPeers[pID]
	if !ok {
		return
	}
	stats.pruneDeadline = time.Now().Add(maxAwaitingTime)
	p.disconnectedPeers[pID] = stats
	delete(p.trackedPeers, pID)
}

func (p *peerTracker) peers() []*peerStat {
	p.peerLk.RLock()
	defer p.peerLk.RUnlock()
	peers := make([]*peerStat, 0, len(p.trackedPeers))
	for _, stat := range p.trackedPeers {
		peers = append(peers, stat)
	}
	return peers
}

// gc goes through connected and disconnected peers once every gcPeriod
// and removes:
// * disconnected peers which have been disconnected for more than maxAwaitingTime;
// * connected peers whose scores are less than or equal than defaultScore;
func (p *peerTracker) gc() {
	ticker := time.NewTicker(gcCycle)
	for {
		select {
		case <-p.ctx.Done():
			p.done <- struct{}{}
			return
		case <-ticker.C:
			p.peerLk.Lock()

			now := time.Now()

			for id, peer := range p.disconnectedPeers {
				if peer.pruneDeadline.Before(now) {
					delete(p.disconnectedPeers, id)
				}
			}

			for id, peer := range p.trackedPeers {
				if peer.peerScore <= defaultScore {
					delete(p.trackedPeers, id)
				}
			}
			p.peerLk.Unlock()

			p.dumpPeers(p.ctx)
		}
	}
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

	for i := 0; i < cap(p.done); i++ {
		select {
		case <-p.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// dump remaining tracked peers
	p.dumpPeers(ctx)

	return nil
}

// blockPeer blocks a peer on the networking level and removes it from the local cache.
func (p *peerTracker) blockPeer(pID libpeer.ID, reason error) {
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

	p.peerLk.Lock()
	defer p.peerLk.Unlock()
	// remove peer from cache.
	delete(p.trackedPeers, pID)
	delete(p.disconnectedPeers, pID)
}
