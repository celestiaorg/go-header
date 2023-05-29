package p2p

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/p2p/persisted_peerstore"
)

func TestPeerTracker_GC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer t.Cleanup(cancel)

	gcCycleDefault = time.Millisecond * 200
	maxAwaitingTime = time.Millisecond

	h := createMocknet(t, 1)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	addrBook, err := pstoremem.NewPeerstore()
	require.NoError(t, err)

	mockPeerStore := persisted_peerstore.NewPersistedPeerstore(ds, addrBook)
	p := NewPeerTracker(h[0], connGater, mockPeerStore)

	peerlist, err := persisted_peerstore.GenerateRandomPeerlist(4)
	require.NoError(t, err)

	pid1 := peerlist[0].ID
	pid2 := peerlist[1].ID
	pid3 := peerlist[2].ID
	pid4 := peerlist[3].ID

	// Add peer with low score to test if it will be GC'ed (it should)
	p.trackedPeers[pid1] = &peerStat{peerID: pid1, peerScore: 0.5}
	// Add peer with high score to test if it won't be GCed (it shouldn't)
	p.trackedPeers[pid2] = &peerStat{peerID: pid2, peerScore: 10}

	// Add peer such that their prune deadline is in the past (after GC cycle time has passed)
	// to test if they will be pruned (they should)
	p.disconnectedPeers[pid3] = &peerStat{peerID: pid3, pruneDeadline: time.Now()}
	// Add peer such that their prune deadline is not the past (after GC cycle time has passed)
	// to test if they won't be pruned (they shouldn't)
	p.disconnectedPeers[pid4] = &peerStat{peerID: pid4, pruneDeadline: time.Now().Add(time.Millisecond * 300)}

	go p.track()
	go p.gc()

	<-time.After(gcCycleDefault + time.Millisecond*20)

	assert.True(t, len(p.getTrackedPeers()) > 0)
	assert.True(t, len(p.getDisconnectedPeers()) > 0)

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
	p := NewPeerTracker(h[0], connGater, nil)
	maxAwaitingTime = time.Millisecond
	p.blockPeer(h[1].ID(), errors.New("test"))
	require.Len(t, connGater.ListBlockedPeers(), 1)
	require.True(t, connGater.ListBlockedPeers()[0] == h[1].ID())
}
