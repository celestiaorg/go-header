package store

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
)

func TestInitStore_NoReinit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)

	_, err = store.Head(ctx)
	require.Error(t, err, header.ErrEmptyStore)
	_, err = store.Tail(ctx)
	require.Error(t, err, header.ErrEmptyStore)

	err = store.Append(ctx, suite.GenDummyHeaders(10)...)
	// require.NoError(t, err) ?????

	err = store.Stop(ctx)
	require.NoError(t, err)

	_, err = store.Head(ctx)
	require.Error(t, err, header.ErrEmptyStore)
	_, err = store.Tail(ctx)
	require.Error(t, err, header.ErrEmptyStore)

	reopenedStore, err := NewStore[*headertest.DummyHeader](ds)
	assert.NoError(t, err)

	err = reopenedStore.Start(ctx)
	require.NoError(t, err)

	reopenedHead, err := reopenedStore.Head(ctx)
	require.NoError(t, err)

	// check that reopened head changed and the store wasn't reinitialized
	assert.Equal(t, suite.Head().Height(), reopenedHead.Height())
	assert.NotEqual(t, head.Height(), reopenedHead.Height())

	err = reopenedStore.Stop(ctx)
	require.NoError(t, err)
}
