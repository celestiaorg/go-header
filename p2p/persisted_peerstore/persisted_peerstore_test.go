package persisted_peerstore

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoreds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer t.Cleanup(cancel)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	// using an on-disk addrbook (with underlying in-mem ds) here as persisted
	// peerstore is intended to be used in tandem with an on-disk AddrBook // TODO does this really have to be the case?
	addrbook, err := pstoreds.NewPeerstore(ctx, ds, pstoreds.DefaultOpts())
	require.NoError(t, err)

	peerstore := NewPersistedPeerstore(ds, addrbook)

	peerlist, err := GenerateRandomPeerlist(10)
	require.NoError(t, err)

	// add the generated multiaddrs to the addrbook
	ids := make([]peer.ID, len(peerlist))
	for i, peer := range peerlist {
		addrbook.AddAddrs(peer.ID, peer.Addrs, time.Hour)
		ids[i] = peer.ID
	}

	err = peerstore.Put(ctx, ids)
	require.NoError(t, err)

	retrievedPeerlist, err := peerstore.Load(ctx)
	require.NoError(t, err)

	assert.Equal(t, len(peerlist), len(retrievedPeerlist))
	assert.Equal(t, peerlist, retrievedPeerlist)
}
