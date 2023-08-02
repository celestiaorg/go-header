package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/libp2p/go-libp2p/core/host"
	syncpkg "sync"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/core/peer"
	testpeer "github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerTracker_GC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := createMocknet(t, 1)

	gcCycle = time.Millisecond * 200

	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	pidstore := newDummyPIDStore()
	p := newPeerTracker(h[0], connGater, pidstore)

	maxAwaitingTime = time.Millisecond

	peerlist := generateRandomPeerlist(t, 4)
	pid1 := peerlist[0]
	pid2 := peerlist[1]
	pid3 := peerlist[2]
	pid4 := peerlist[3]

	p.trackedPeers[pid1] = &peerStat{peerID: pid1, peerScore: 0.5}
	p.trackedPeers[pid2] = &peerStat{peerID: pid2, peerScore: 10}
	p.disconnectedPeers[pid3] = &peerStat{peerID: pid3, pruneDeadline: time.Now()}
	p.disconnectedPeers[pid4] = &peerStat{peerID: pid4, pruneDeadline: time.Now().Add(time.Minute * 10)}
	assert.True(t, len(p.trackedPeers) > 0)
	assert.True(t, len(p.disconnectedPeers) > 0)

	go p.track()
	go p.gc()

	time.Sleep(time.Millisecond * 500)

	err = p.stop(ctx)
	require.NoError(t, err)

	require.Nil(t, p.trackedPeers[pid1])
	require.Nil(t, p.disconnectedPeers[pid3])

	// ensure good peers were dumped to store
	peers, err := pidstore.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, len(peers))
}

func TestPeerTracker_BlockPeer(t *testing.T) {
	h := createMocknet(t, 2)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)
	p := newPeerTracker(h[0], connGater, nil)
	maxAwaitingTime = time.Millisecond
	p.blockPeer(h[1].ID(), errors.New("test"))
	require.Len(t, connGater.ListBlockedPeers(), 1)
	require.True(t, connGater.ListBlockedPeers()[0] == h[1].ID())
}

func TestPeerTracker_Bootstrap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	// mn := createMocknet(t, 10)
	mn, err := mocknet.FullMeshConnected(10)
	require.NoError(t, err)

	// store peers to peerstore
	prevSeen := make([]peer.ID, 9)
	for i, peer := range mn.Hosts()[1:] {
		prevSeen[i] = peer.ID()

		// disconnect so they're not already connected on attempt to
		// connect
		err = mn.DisconnectPeers(mn.Hosts()[i].ID(), peer.ID())
		require.NoError(t, err)
	}
	pidstore := newDummyPIDStore()
	// only store 7 peers to pidstore, and use 2 as trusted
	err = pidstore.Put(ctx, prevSeen[2:])
	require.NoError(t, err)

	tracker := newPeerTracker(mn.Hosts()[0], connGater, pidstore)

	go tracker.track()

	err = tracker.bootstrap(ctx, prevSeen[:2])
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return len(tracker.peers()) > 0
	}, time.Millisecond*500, time.Millisecond*100)
}

func Test_getPeers_withWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	t.Cleanup(cancel)

	mn, err := mocknet.FullMeshConnected(5)
	require.NoError(t, err)

	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)
	tracker := newPeerTracker(mn.Hosts()[0], connGater, nil)

	go tracker.track()
	go tracker.gc()

	wg := syncpkg.WaitGroup{}
	wg.Add(1)
	go func() {
		peers, err := tracker.getPeers(ctx, 9)
		require.NoError(t, err)
		require.Len(t, peers, 9)
		wg.Done()
	}()

	num := 5
	for i := 0; i < num; i++ {
		h, err := mn.GenPeer()
		require.NoError(t, err)
		if h.ID() == mn.Hosts()[0].ID() {
			// TODO @renaynay: figure out mocknet weirdness allows GenPeer to
			//  return a peer that has already been generated in mocknet
			num++
			continue
		}
		_, err = mn.LinkPeers(h.ID(), mn.Hosts()[0].ID())
		require.NoError(t, err)
		err = h.Connect(ctx, *host.InfoFromHost(mn.Hosts()[0]))
		require.NoError(t, err)
		tracker.connected(h.ID())
	}

	wg.Wait()
}

type dummyPIDStore struct {
	ds  datastore.Datastore
	key datastore.Key
}

func newDummyPIDStore() PeerIDStore {
	return &dummyPIDStore{
		key: datastore.NewKey("peers"),
		ds:  sync.MutexWrap(datastore.NewMapDatastore()),
	}
}

func (d *dummyPIDStore) Put(ctx context.Context, peers []peer.ID) error {
	bin, err := json.Marshal(peers)
	if err != nil {
		return err
	}
	return d.ds.Put(ctx, d.key, bin)
}

func (d *dummyPIDStore) Load(ctx context.Context) ([]peer.ID, error) {
	bin, err := d.ds.Get(ctx, d.key)
	if err != nil {
		return nil, err
	}

	var peers []peer.ID
	err = json.Unmarshal(bin, &peers)
	return peers, err
}

func generateRandomPeerlist(t *testing.T, length int) []peer.ID {
	peerlist := make([]peer.ID, length)
	for i := range peerlist {
		peerlist[i] = testpeer.RandPeerIDFatal(t)
	}
	return peerlist
}
