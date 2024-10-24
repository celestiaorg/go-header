package store

import (
	"bytes"
	"context"
	"math/rand"
	stdsync "sync"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/headertest"
)

func TestStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(5))

	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, suite.Head().Hash(), head.Hash())

	in := suite.GenDummyHeaders(10)
	err = store.Append(ctx, in...)
	require.NoError(t, err)

	out, err := store.GetRange(ctx, 2, 12)
	require.NoError(t, err)
	for i, h := range in {
		assert.Equal(t, h.Hash(), out[i].Hash())
	}

	// we need to wait for a flush
	assert.Eventually(t, func() bool {
		head, err = store.Head(ctx)
		require.NoError(t, err)
		return bytes.Equal(out[len(out)-1].Hash(), head.Hash())
	}, time.Second, 100*time.Millisecond)

	ok, err := store.Has(ctx, in[5].Hash())
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = store.Has(ctx, headertest.RandBytes(32))
	require.NoError(t, err)
	assert.False(t, ok)

	go func() {
		err := store.Append(ctx, suite.GenDummyHeaders(1)...)
		require.NoError(t, err)
	}()

	h, err := store.GetByHeight(ctx, 12)
	require.NoError(t, err)
	assert.NotNil(t, h)

	err = store.Stop(ctx)
	require.NoError(t, err)

	h, err = store.GetByHeight(ctx, 2)
	require.NoError(t, err)
	out, err = store.GetRangeByHeight(ctx, h, 3)
	require.Error(t, err)
	assert.Nil(t, out)

	out, err = store.GetRangeByHeight(ctx, h, 4)
	require.NoError(t, err)
	assert.NotNil(t, out)
	assert.Len(t, out, 1)

	out, err = store.GetRange(ctx, 2, 2)
	require.Error(t, err)
	assert.Nil(t, out)

	// check that the store can be successfully started after previous stop
	// with all data being flushed.
	err = store.Start(ctx)
	require.NoError(t, err)

	head, err = store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, suite.Head().Hash(), head.Hash())

	out, err = store.getRangeByHeight(ctx, 1, 13)
	require.NoError(t, err)
	assert.Len(t, out, 12)

	out, err = store.getRangeByHeight(ctx, 10, 11)
	require.NoError(t, err)
	assert.Len(t, out, 1)
}

// TestStore_GetRangeByHeight_ExpectedRange
func TestStore_GetRangeByHeight_ExpectedRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head())

	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, suite.Head().Hash(), head.Hash())

	in := suite.GenDummyHeaders(100)
	err = store.Append(ctx, in...)
	require.NoError(t, err)

	// request the range [4:98] || (from:to)
	from := in[2]
	firstHeaderInRangeHeight := from.Height() + 1
	lastHeaderInRangeHeight := uint64(98)
	to := lastHeaderInRangeHeight + 1
	expectedLenHeaders := to - firstHeaderInRangeHeight // expected amount

	out, err := store.GetRangeByHeight(ctx, from, to)
	require.NoError(t, err)

	assert.Len(t, out, int(expectedLenHeaders))
	assert.Equal(t, firstHeaderInRangeHeight, out[0].Height())
	assert.Equal(t, lastHeaderInRangeHeight, out[len(out)-1].Height())
}

func TestStore_Append_BadHeader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head())

	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, suite.Head().Hash(), head.Hash())

	in := suite.GenDummyHeaders(10)
	in[0].VerifyFailure = true
	err = store.Append(ctx, in...)
	require.Error(t, err)
}

func TestStore_Append(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(4))

	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, head.Hash(), suite.Head().Hash())

	const workers = 10
	const chunk = 5
	headers := suite.GenDummyHeaders(workers * chunk)

	errCh := make(chan error, workers)
	var wg stdsync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func() {
			defer wg.Done()
			// make every append happened in random order.
			time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)

			err := store.Append(ctx, headers[i*chunk:(i+1)*chunk]...)
			errCh <- err
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		assert.NoError(t, err)
	}

	// wait for batch to be written.
	time.Sleep(100 * time.Millisecond)

	head, err = store.Head(ctx)
	assert.NoError(t, err)
	assert.Equal(t, head.Height(), headers[len(headers)-1].Height())
	assert.Equal(t, head.Hash(), headers[len(headers)-1].Hash())
}

func TestStore_Append_stableHeadWhenGaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(4))

	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, head.Hash(), suite.Head().Hash())

	firstChunk := suite.GenDummyHeaders(5)
	missedChunk := suite.GenDummyHeaders(5)
	lastChunk := suite.GenDummyHeaders(5)

	wantHead := firstChunk[len(firstChunk)-1]
	latestHead := lastChunk[len(lastChunk)-1]

	{
		err := store.Append(ctx, firstChunk...)
		require.NoError(t, err)
		// wait for batch to be written.
		time.Sleep(100 * time.Millisecond)

		// head is advanced to the last known header.
		head, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, head.Height(), wantHead.Height())
		assert.Equal(t, head.Hash(), wantHead.Hash())

		// check that store height is aligned with the head.
		height := store.Height()
		assert.Equal(t, height, head.Height())
	}
	{
		err := store.Append(ctx, lastChunk...)
		require.NoError(t, err)
		// wait for batch to be written.
		time.Sleep(100 * time.Millisecond)

		// head is not advanced due to a gap.
		head, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, head.Height(), wantHead.Height())
		assert.Equal(t, head.Hash(), wantHead.Hash())

		// check that store height is aligned with the head.
		height := store.Height()
		assert.Equal(t, height, head.Height())
	}
	{
		err := store.Append(ctx, missedChunk...)
		require.NoError(t, err)
		// wait for batch to be written.
		time.Sleep(time.Second)

		// after appending missing headers we're on the latest header.
		head, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, head.Height(), latestHead.Height())
		assert.Equal(t, head.Hash(), latestHead.Hash())

		// check that store height is aligned with the head.
		height := store.Height()
		assert.Equal(t, height, head.Height())
	}
}

// TestStore_GetRange tests possible combinations of requests and ensures that
// the store can handle them adequately (even malformed requests)
func TestStore_GetRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head())

	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, suite.Head().Hash(), head.Hash())

	in := suite.GenDummyHeaders(100)
	err = store.Append(ctx, in...)
	require.NoError(t, err)

	tests := []struct {
		name          string
		from          uint64
		to            uint64
		expectedError bool
	}{
		{
			name:          "valid request for headers all contained in store",
			from:          4,
			to:            99,
			expectedError: false,
		},
		{
			name:          "malformed request for headers contained in store",
			from:          99,
			to:            4,
			expectedError: true,
		},
		{
			name:          "valid request for headers not contained in store",
			from:          100,
			to:            500,
			expectedError: true,
		},
		{
			name:          "valid request for headers partially contained in store (`to` is greater than chain head)",
			from:          77,
			to:            200,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			firstHeaderInRangeHeight := tt.from
			lastHeaderInRangeHeight := tt.to - 1
			to := lastHeaderInRangeHeight + 1
			expectedLenHeaders := to - firstHeaderInRangeHeight // expected amount

			// request the range [tt.to:tt.from)
			out, err := store.GetRange(ctx, firstHeaderInRangeHeight, to)
			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, out)
				return
			}
			require.NoError(t, err)

			assert.Len(t, out, int(expectedLenHeaders))
			assert.Equal(t, firstHeaderInRangeHeight, out[0].Height())
			assert.Equal(t, lastHeaderInRangeHeight, out[len(out)-1].Height())
		})
	}
}

func TestStorePendingCacheMiss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())

	store := NewTestStore(t, ctx, ds, suite.Head(),
		WithWriteBatchSize(100),
		WithStoreCacheSize(100),
	)

	err := store.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)

	err = store.Append(ctx, suite.GenDummyHeaders(50)...)
	require.NoError(t, err)

	_, err = store.GetRange(ctx, 1, 101)
	require.NoError(t, err)

	_, err = store.GetRange(ctx, 101, 151)
	require.NoError(t, err)
}

func TestBatch_GetByHeightBeforeInit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)

	go func() {
		_ = store.Init(ctx, suite.Head())
	}()

	h, err := store.GetByHeight(ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, h)
}

func TestStoreInit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	headers := suite.GenDummyHeaders(10)
	err = store.Init(ctx, headers[len(headers)-1]) // init should work with any height, not only 1
	require.NoError(t, err)
}
