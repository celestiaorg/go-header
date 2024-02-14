package p2p

import (
	"context"
	"encoding/json"
	"errors"
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
	p := newPeerTracker(h[0], connGater, pidstore, nil)

	maxAwaitingTime = time.Millisecond

	peerlist := generateRandomPeerlist(t, 10)
	for i := 0; i < 10; i++ {
		p.trackedPeers[peerlist[i]] = &peerStat{peerID: peerlist[i], peerScore: 0.5}
	}

	peerlist = generateRandomPeerlist(t, 4)
	pid1 := peerlist[2]
	pid2 := peerlist[3]

	p.disconnectedPeers[pid1] = &peerStat{peerID: pid1, pruneDeadline: time.Now()}
	p.disconnectedPeers[pid2] = &peerStat{peerID: pid2, pruneDeadline: time.Now().Add(time.Minute * 10)}
	assert.True(t, len(p.trackedPeers) > 0)
	assert.True(t, len(p.disconnectedPeers) > 0)

	go p.track()
	go p.gc()

	time.Sleep(time.Millisecond * 500)

	err = p.stop(ctx)
	require.NoError(t, err)

	require.Len(t, p.trackedPeers, 10)
	require.Nil(t, p.disconnectedPeers[pid1])

	// ensure good peers were dumped to store
	peers, err := pidstore.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, 10, len(peers))
}

func TestPeerTracker_BlockPeer(t *testing.T) {
	h := createMocknet(t, 2)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)
	p := newPeerTracker(h[0], connGater, nil, nil)
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

	tracker := newPeerTracker(mn.Hosts()[0], connGater, pidstore, nil)

	go tracker.track()

	err = tracker.bootstrap(ctx, prevSeen[:2])
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return len(tracker.getPeers(7)) > 0
	}, time.Millisecond*500, time.Millisecond*100)
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
