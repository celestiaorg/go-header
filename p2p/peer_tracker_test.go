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
	h := createMocknet(t, 1)
	gcCycleDefault = time.Millisecond * 200
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)
	mockPeerStore := peerstore.NewPeerStore(sync.MutexWrap(datastore.NewMapDatastore()))
	p := newPeerTracker(h[0], connGater, mockPeerStore)
	maxAwaitingTime = time.Millisecond

	peerlist, err := peerstore.GenerateRandomPeerlist(4)
	require.NoError(t, err)

	pid1 := peerlist[0].ID
	pid2 := peerlist[1].ID
	pid3 := peerlist[2].ID
	pid4 := peerlist[3].ID

	p.trackedPeers[pid1] = &peerStat{peerID: pid1, peerScore: 0.5}
	p.trackedPeers[pid2] = &peerStat{peerID: pid2, peerScore: 10}

	p.disconnectedPeers[pid3] = &peerStat{peerID: pid3, pruneDeadline: time.Now()}
	p.disconnectedPeers[pid4] = &peerStat{peerID: pid4, pruneDeadline: time.Now().Add(time.Minute * 10)}

	assert.True(t, len(p.trackedPeers) > 0)
	assert.True(t, len(p.disconnectedPeers) > 0)

	go p.track()
	go p.gc()

	<-time.After(gcCycleDefault + time.Millisecond*20)

	err = p.stop(context.Background())
	require.NoError(t, err)

	require.Nil(t, p.trackedPeers[pid1])
	require.Nil(t, p.disconnectedPeers[pid3])
	require.Equal(t, pid2, p.trackedPeers[pid2].peerID)

	peers, err := mockPeerStore.Load(context.Background())
	require.NoError(t, err)

	require.Equal(t, peers[0].ID, p.trackedPeers[pid2].peerID)
	assert.Equal(t, 1, len(p.trackedPeers))
}

func TestPeerTracker_BlockPeer(t *testing.T) {
	h := createMocknet(t, 2)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)
	p := newPeerTracker(h[0], connGater, peerstore.NewPeerStore(sync.MutexWrap(datastore.NewMapDatastore())))
	maxAwaitingTime = time.Millisecond
	p.blockPeer(h[1].ID(), errors.New("test"))
	require.Len(t, connGater.ListBlockedPeers(), 1)
	require.True(t, connGater.ListBlockedPeers()[0] == h[1].ID())
}
