package p2p

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/p2p/peerstore"
)

func TestPeerTracker_GC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer t.Cleanup(cancel)

	gcCycleDefault = time.Millisecond * 200
	maxAwaitingTime = time.Millisecond

	h := createMocknet(t, 1)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	mockPeerStore := peerstore.NewPeerStore(sync.MutexWrap(datastore.NewMapDatastore()))
	p := newPeerTracker(h[0], connGater, mockPeerStore)

	peerlist, err := peerstore.GenerateRandomPeerlist(4)
	require.NoError(t, err)

	pid1 := peerlist[0].ID
	pid2 := peerlist[1].ID
	pid3 := peerlist[2].ID
	pid4 := peerlist[3].ID

	// Add peer with low score to test if it will be GC'ed (it should)
	p.trackedPeers[pid1] = &peerStat{peerID: pid1, peerScore: 0.5}
	// Add peer with high score to test if it won't be GCed (it shouldn't)
	p.trackedPeers[pid2] = &peerStat{peerID: pid2, peerScore: 10}

	// Add peer such that their prune deadlnie is in the past (after GC cycle time has passed)
	// to test if they will be prned (they should)
	p.disconnectedPeers[pid3] = &peerStat{peerID: pid3, pruneDeadline: time.Now()}
	// Add peer such that their prune deadline is not the past (after GC cycle time has passed)
	// to test if they won't be pruned (they shouldn't)
	p.disconnectedPeers[pid4] = &peerStat{peerID: pid4, pruneDeadline: time.Now().Add(time.Millisecond * 300)}

	go p.track()
	go p.gc()

	<-time.After(gcCycleDefault + time.Millisecond*20)

	p.peerLk.Lock()
	assert.True(t, len(p.trackedPeers) > 0)
	assert.True(t, len(p.disconnectedPeers) > 0)
	p.peerLk.Unlock()

	err = p.stop(context.Background())
	require.NoError(t, err)

	require.Nil(t, p.trackedPeers[pid1])
	require.Nil(t, p.disconnectedPeers[pid3])

	assert.Equal(t, pid2, p.trackedPeers[pid2].peerID)

	peers, err := mockPeerStore.Load(ctx)
	require.NoError(t, err)

	assert.Equal(t, peers[0].ID, p.trackedPeers[pid2].peerID)
	assert.Equal(t, 1, len(p.trackedPeers))
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

func TestPeerTracker_Track(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer t.Cleanup(cancel)

	gcCycleDefault = time.Millisecond * 200
	maxAwaitingTime = time.Millisecond

	totalPeers := 5
	hosts := createMocknet(t, totalPeers)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	mockPeerStore := peerstore.NewPeerStore(sync.MutexWrap(datastore.NewMapDatastore()))
	peerstoreListSize := 4
	peerlist, err := peerstore.GenerateRandomPeerlist(peerstoreListSize)
	require.NoError(t, err)
	err = mockPeerStore.Put(ctx, peerlist)
	require.NoError(t, err)
	p := newPeerTracker(hosts[0], connGater, mockPeerStore)

	go p.track()

	<-time.After(time.Millisecond * 100) // to avoid data races

	p.peerLk.Lock()
	assert.Equal(t, totalPeers-1+peerstoreListSize, len(p.trackedPeers))
	p.peerLk.Unlock()
}
