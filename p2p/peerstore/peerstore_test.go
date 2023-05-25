package peerstore

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer t.Cleanup(cancel)

	peerstore := NewPeerstore(sync.MutexWrap(datastore.NewMapDatastore()))

	peerlist, err := GenerateRandomPeerlist(10)
	require.NoError(t, err)

	err = peerstore.Put(ctx, peerlist)
	require.NoError(t, err)

	retrievedPeerlist, err := peerstore.Load(ctx)
	require.NoError(t, err)

	assert.Equal(t, len(peerlist), len(retrievedPeerlist))
	assert.Equal(t, peerlist, retrievedPeerlist)
}
