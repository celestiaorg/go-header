package p2p

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerTracker_GC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer t.Cleanup(cancel)

	gcCycleDefault = time.Millisecond * 200
	maxAwaitingTime = time.Millisecond

	h := createMocknet(t, 1)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	mockPeerStore := newMockPIDStore()
	p := NewPeerTracker(h[0], connGater, mockPeerStore)

	peerlist, err := generateRandomPeerlist(4)
	require.NoError(t, err)

	pid1 := peerlist[0]
	pid2 := peerlist[1]
	pid3 := peerlist[2]
	pid4 := peerlist[3]

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

	assert.Equal(t, peers[0], p.trackedPeers[pid2].peerID)
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

type mockPIDStore struct {
	ds datastore.Datastore
}

func (m mockPIDStore) Put(ctx context.Context, peers []peer.ID) error {
	bin, err := json.Marshal(peers)
	if err != nil {
		return err
	}

	return m.ds.Put(ctx, datastore.NewKey("peers"), bin)
}

func (m mockPIDStore) Load(ctx context.Context) ([]peer.ID, error) {
	bin, err := m.ds.Get(ctx, datastore.NewKey("peers"))
	if err != nil {
		return nil, err
	}

	var peers []peer.ID
	err = json.Unmarshal(bin, &peers)
	return peers, err
}

func newMockPIDStore() PeerIDStore {
	return &mockPIDStore{ds: sync.MutexWrap(datastore.NewMapDatastore())}
}

func generateRandomPeerlist(length int) ([]peer.ID, error) {
	peerlist := make([]peer.ID, length)
	for i := range peerlist {
		key, err := rsa.GenerateKey(rand.Reader, 2096)
		if err != nil {
			return nil, err
		}

		_, pubkey, err := crypto.KeyPairFromStdKey(key)
		if err != nil {
			return nil, err
		}

		peerID, err := peer.IDFromPublicKey(pubkey)
		if err != nil {
			return nil, err
		}

		peerlist[i] = peerID
	}

	return peerlist, nil
}
