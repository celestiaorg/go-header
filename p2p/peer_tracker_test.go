package p2p

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
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
	p := NewPeerTracker(h[0], connGater, pidstore)

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
	p := NewPeerTracker(h[0], connGater, nil)
	maxAwaitingTime = time.Millisecond
	p.blockPeer(h[1].ID(), errors.New("test"))
	require.Len(t, connGater.ListBlockedPeers(), 1)
	require.True(t, connGater.ListBlockedPeers()[0] == h[1].ID())
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
		key, err := rsa.GenerateKey(rand.Reader, 2096)
		require.NoError(t, err)

		_, pubkey, err := crypto.KeyPairFromStdKey(key)
		require.NoError(t, err)

		peerID, err := peer.IDFromPublicKey(pubkey)
		require.NoError(t, err)

		peerlist[i] = peerID
	}
	return peerlist
}
