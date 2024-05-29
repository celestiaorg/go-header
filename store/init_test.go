package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/local"
)

func TestInitStore_NoReinit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	exchange := local.NewExchange(NewTestStore(ctx, t, head))

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = initStore(ctx, store, exchange, head.Hash())
	assert.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)

	err = store.Append(ctx, suite.GenDummyHeaders(10)...)
	require.NoError(t, err)

	err = store.Stop(ctx)
	require.NoError(t, err)

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

// initStore ensures a Store is initialized.
// If it is not already initialized, it initializes the Store by requesting the header with the given hash.
func initStore[H header.Header[H]](ctx context.Context, store header.Store[H], ex header.Exchange[H], hash header.Hash) error {
	_, err := store.Head(ctx)
	if err == nil {
		return nil
	}

	if errors.Is(err, header.ErrNoHead) {
		initial, err := ex.Get(ctx, hash)
		if err != nil {
			return err
		}
		return store.Init(ctx, initial)
	}
	return err
}
