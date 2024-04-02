package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	testpeer "github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerTracker_BlockPeer(t *testing.T) {
	h := createMocknet(t, 2)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)
	p := newPeerTracker(h[0], connGater, "private", nil, nil)
	p.blockPeer(h[1].ID(), errors.New("test"))
	require.Len(t, connGater.ListBlockedPeers(), 1)
	require.True(t, connGater.ListBlockedPeers()[0] == h[1].ID())
}

func TestPeerTracker_Bootstrap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	hosts := make([]host.Host, 10)

	for i := range hosts {
		hosts[i], err = libp2p.New()
		require.NoError(t, err)
		hosts[i].SetStreamHandler(protocolID("private"), nil)
	}

	// store peers to peerstore
	prevSeen := make([]peer.ID, 9)
	for i, peer := range hosts[1:] {
		hosts[0].Peerstore().AddAddrs(hosts[i].ID(), hosts[i].Addrs(), peerstore.PermanentAddrTTL)
		prevSeen[i] = peer.ID()
	}
	pidstore := newDummyPIDStore()
	// only store 7 peers to pidstore, and use 2 as trusted
	err = pidstore.Put(ctx, prevSeen[2:])
	require.NoError(t, err)
	tracker := newPeerTracker(hosts[0], connGater, "private", pidstore, nil)

	go tracker.track()

	err = tracker.bootstrap(prevSeen[:2])
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
