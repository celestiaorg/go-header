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

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
)

func TestStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	genesis := suite.Head()
	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, genesis)

	assert.Equal(t, *store.tailHeader.Load(), suite.Head())

	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, suite.Head().Hash(), head.Hash())

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, tail.Hash(), genesis.Hash())

	in := suite.GenDummyHeaders(10)
	err = store.Append(ctx, in...)
	require.NoError(t, err)

	out, err := store.GetRange(ctx, 2, 12)
	require.NoError(t, err)
	for i, h := range in {
		assert.Equal(t, h.Hash(), out[i].Hash())
	}

	head, err = store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, out[len(out)-1].Hash(), head.Hash())

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

	assert.Eventually(t, func() bool {
		head, err = store.Head(ctx)
		assert.NoError(t, err)
		assert.Equal(t, int(head.Height()), int(headers[len(headers)-1].Height()))

		switch {
		case int(head.Height()) != int(headers[len(headers)-1].Height()):
			return false
		case !bytes.Equal(head.Hash(), headers[len(headers)-1].Hash()):
			return false
		default:
			return true
		}
	}, time.Second, time.Millisecond)
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

func TestStoreGetByHeight_whenGaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, head.Hash(), suite.Head().Hash())

	{
		firstChunk := suite.GenDummyHeaders(5)
		latestHead := firstChunk[len(firstChunk)-1]

		err := store.Append(ctx, firstChunk...)
		require.NoError(t, err)
		// wait for batch to be written.
		time.Sleep(100 * time.Millisecond)

		head, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, head.Height(), latestHead.Height())
		assert.Equal(t, head.Hash(), latestHead.Hash())
	}

	missedChunk := suite.GenDummyHeaders(5)
	wantMissHead := missedChunk[len(missedChunk)-2]

	errChMiss := make(chan error, 1)
	go func() {
		shortCtx, shortCancel := context.WithTimeout(ctx, 3*time.Second)
		defer shortCancel()

		_, err := store.GetByHeight(shortCtx, wantMissHead.Height())
		errChMiss <- err
	}()

	lastChunk := suite.GenDummyHeaders(5)
	wantLastHead := lastChunk[len(lastChunk)-1]

	errChLast := make(chan error, 1)
	go func() {
		shortCtx, shortCancel := context.WithTimeout(ctx, 3*time.Second)
		defer shortCancel()

		_, err := store.GetByHeight(shortCtx, wantLastHead.Height())
		errChLast <- err
	}()

	// wait for goroutines start
	time.Sleep(100 * time.Millisecond)

	select {
	case err := <-errChMiss:
		t.Fatalf("store.GetByHeight on prelast height MUST be blocked, have error: %v", err)
	case err := <-errChLast:
		t.Fatalf("store.GetByHeight on last height MUST be blocked, have error: %v", err)
	default:
		// ok
	}

	{
		err := store.Append(ctx, lastChunk...)
		require.NoError(t, err)
		// wait for batch to be written.
		time.Sleep(100 * time.Millisecond)

		select {
		case err := <-errChMiss:
			t.Fatalf("store.GetByHeight on prelast height MUST be blocked, have error: %v", err)
		case err := <-errChLast:
			require.NoError(t, err)
		default:
			t.Fatalf("store.GetByHeight on last height MUST NOT be blocked, have error: %v", err)
		}
	}

	{
		err := store.Append(ctx, missedChunk...)
		require.NoError(t, err)
		// wait for batch to be written.
		time.Sleep(100 * time.Millisecond)

		select {
		case err := <-errChMiss:
			require.NoError(t, err)

			head, err := store.GetByHeight(ctx, wantLastHead.Height())
			require.NoError(t, err)
			require.Equal(t, head, wantLastHead)
		default:
			t.Fatal("store.GetByHeight on last height MUST NOT be blocked")
		}
	}
}

func TestStoreGetByHeight_earlyAvailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	const skippedHeaders = 15
	suite.GenDummyHeaders(skippedHeaders)
	lastChunk := suite.GenDummyHeaders(1)

	{
		err := store.Append(ctx, lastChunk...)
		require.NoError(t, err)

		// wait for batch to be written.
		time.Sleep(100 * time.Millisecond)
	}

	{
		h, err := store.GetByHeight(ctx, lastChunk[0].Height())
		require.NoError(t, err)
		require.Equal(t, h, lastChunk[0])
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

func TestStore_DeleteRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	const count = 100
	in := suite.GenDummyHeaders(count)
	err := store.Append(ctx, in...)
	require.NoError(t, err)

	hashes := make(map[uint64]header.Hash, count)
	for _, h := range in {
		hashes[h.Height()] = h.Hash()
	}

	// wait until headers are written
	time.Sleep(100 * time.Millisecond)

	tests := []struct {
		name      string
		from      uint64
		to        uint64
		wantTail  uint64
		wantError bool
	}{
		{
			name:      "valid delete request",
			from:      9,
			to:        14,
			wantTail:  1,
			wantError: false,
		},
		{
			name:      "valid delete request",
			from:      1,
			to:        5,
			wantTail:  5,
			wantError: false,
		},
		{
			name:      "valid delete request",
			from:      40,
			to:        50,
			wantTail:  5,
			wantError: false,
		},
		{
			name:      "valid delete request (overvlaps with deleted)",
			from:      45,
			to:        55,
			wantTail:  5,
			wantError: false,
		},
		{
			name:      "valid delete request",
			from:      1,
			to:        50,
			wantTail:  55,
			wantError: false,
		},
		{
			name:      "invalid range",
			from:      50,
			to:        30,
			wantTail:  55,
			wantError: true,
		},
		{
			name:      "valid range but is partially does not exist",
			from:      87,
			to:        109,
			wantTail:  55,
			wantError: false,
		},
		{
			name:      "valid range out of written headers",
			from:      177,
			to:        200,
			wantTail:  55,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			err := store.DeleteRange(ctx, tt.from, tt.to)
			if tt.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// check that cache and pending doesn't contain old headers
			for h := tt.from; h < tt.to; h++ {
				hash := hashes[h]
				assert.False(t, store.cache.Contains(hash.String()))
				assert.False(t, store.pending.Has(hash))
			}

			tail, err := store.Tail(ctx)
			require.NoError(t, err)
			require.EqualValues(t, tail.Height(), tt.wantTail)
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
	suite.Head().HeightI = 1_000_000

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)

	go func() {
		_ = store.Init(ctx, suite.Head())
	}()

	_, err = store.GetByHeight(ctx, 1)
	require.ErrorIs(t, err, header.ErrNotFound)
}

func TestStoreInit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	headers := suite.GenDummyHeaders(10)
	h := headers[len(headers)-1]
	err = store.Init(ctx, h) // init should work with any height, not only 1
	require.NoError(t, err)

	tail, err := store.Tail(ctx)
	assert.Equal(t, tail.Hash(), h.Hash())
	require.NoError(t, err)
}
