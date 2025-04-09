package store

import (
	"context"
	"testing"

	"github.com/ipfs/go-datastore"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/headertest"
)

// NewTestStore creates initialized and started in memory header Store which is useful for testing.
func NewTestStore(tb testing.TB, ctx context.Context, //nolint:revive
	ds datastore.Batching, head *headertest.DummyHeader, opts ...Option,
) *Store[*headertest.DummyHeader] {
	store, err := NewStore[*headertest.DummyHeader](ds, opts...)
	require.NoError(tb, err)

	err = store.Start(ctx)
	require.NoError(tb, err)

	err = store.Append(ctx, head)
	require.NoError(tb, err)

	tb.Cleanup(func() {
		err := store.Stop(ctx)
		require.NoError(tb, err)
	})
	return store
}
