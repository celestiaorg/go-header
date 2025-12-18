package store

import (
	"bytes"
	"context"
	"errors"
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

func TestStore_Append_advanceTail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	missing := suite.GenDummyHeaders(10)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(4))

	// assert Tail is beyond missing headers
	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, tail.Height(), suite.Head().Height())

	// append the first 5 headers creating a gap, and assert Tail is still beyond missing headers
	err = store.Append(ctx, missing[0:5]...)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	tail, err = store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, tail.Height(), suite.Head().Height())

	// append the remaining 5 headers filling the gap, and assert Tail advanced over the missing headers
	// until the very first one
	err = store.Append(ctx, missing[5:10]...)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	tail, err = store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, tail.Height(), missing[0].Height())
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
			require.Equal(t, head.Hash(), wantLastHead.Hash())
		default:
			t.Fatal("store.GetByHeight on last height MUST NOT be blocked")
		}
	}
}

func TestStoreGetByHeight_intermittentWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head())

	firstChunk := suite.GenDummyHeaders(10)
	_ = suite.GenDummyHeaders(10)
	secondChunk := suite.GenDummyHeaders(10)
	err := store.Append(ctx, firstChunk...)
	require.NoError(t, err)
	err = store.Append(ctx, secondChunk...)
	require.NoError(t, err)
	// wait for batch to be written
	time.Sleep(10 * time.Millisecond)

	for _, expect := range firstChunk {
		have, err := store.GetByHeight(ctx, expect.HeightI)
		require.NoError(t, err)
		assert.Equal(t, expect.Height(), have.Height())
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

func TestStore_DeleteRange_Tail(t *testing.T) {
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
		to        uint64
		wantTail  uint64
		wantError bool
	}{
		{
			name:      "initial delete request",
			to:        14,
			wantTail:  14,
			wantError: false,
		},
		{
			name:      "no-op delete request",
			to:        5,
			wantError: true,
		},
		{
			name:      "valid delete request",
			to:        50,
			wantTail:  50,
			wantError: false,
		},
		{
			name:      "higher than head",
			to:        103,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from := (*store.tailHeader.Load()).Height()

			// manually add something to the pending for assert at the bottom
			if idx := from - 2; idx < count {
				store.pending.Append(in[idx])
				defer store.pending.Reset()
			}

			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			err := store.DeleteRange(ctx, from, tt.to)
			if tt.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// check that cache and pending doesn't contain old headers
			for h := from; h < tt.to; h++ {
				hash := hashes[h]
				assert.False(t, store.cache.Contains(hash.String()))
				assert.False(t, store.heightIndex.cache.Contains(h))
				assert.False(t, store.pending.Has(hash))
			}

			tail, err := store.Tail(ctx)
			require.NoError(t, err)
			require.EqualValues(t, tt.wantTail, tail.Height())
		})
	}
}

func TestStore_DeleteRange_EmptyStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)

	err = store.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)

	err = store.DeleteRange(ctx, tail.Height(), 101)
	require.NoError(t, err)

	// assert store is empty
	tail, err = store.Tail(ctx)
	assert.Nil(t, tail)
	assert.ErrorIs(t, err, header.ErrEmptyStore)
	head, err := store.Head(ctx)
	assert.Nil(t, head)
	assert.ErrorIs(t, err, header.ErrEmptyStore)

	// assert that it is still empty even after restart
	err = store.Stop(ctx)
	require.NoError(t, err)
	err = store.Start(ctx)
	require.NoError(t, err)

	tail, err = store.Tail(ctx)
	assert.Nil(t, tail)
	assert.ErrorIs(t, err, header.ErrEmptyStore)
	head, err = store.Head(ctx)
	assert.Nil(t, head)
	assert.ErrorIs(t, err, header.ErrEmptyStore)
}

func TestStore_DeleteRange_MoveHeadAndTail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)

	// Append 100 headers (heights 2-101, head becomes 101)
	err = store.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	// Append 10 more headers (heights 102-111, head becomes 111)
	newHeaders := suite.GenDummyHeaders(10)
	err = store.Append(ctx, newHeaders...)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)

	head, err := store.Head(ctx)
	require.NoError(t, err)

	// Delete from tail to head+1 (wipes the store, then we verify behavior)
	// Instead, let's delete a portion from tail to keep some headers
	deleteTo := uint64(102) // Delete heights 1-101, keep 102-111
	err = store.DeleteRange(ctx, tail.Height(), deleteTo)
	require.NoError(t, err)

	// assert store is not empty
	tail, err = store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, deleteTo, tail.Height())
	head, err = store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, newHeaders[len(newHeaders)-1].Height(), head.Height())

	// assert that it is still not empty after restart
	err = store.Stop(ctx)
	require.NoError(t, err)
	err = store.Start(ctx)
	require.NoError(t, err)

	tail, err = store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, deleteTo, tail.Height())
	head, err = store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, newHeaders[len(newHeaders)-1].Height(), head.Height())
}

func TestStore_OnDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	err := store.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	deleted := 0
	store.OnDelete(func(ctx context.Context, height uint64) error {
		hdr, err := store.GetByHeight(ctx, height)
		assert.NoError(t, err, "must be accessible")
		require.NotNil(t, hdr)
		deleted++
		return nil
	})

	tail, err := store.Tail(ctx)
	require.NoError(t, err)

	// Delete a partial range from tail (not the entire store)
	// This ensures OnDelete handlers are called for each header
	deleteTo := uint64(51)
	err = store.DeleteRange(ctx, tail.Height(), deleteTo)
	require.NoError(t, err)
	// Should have deleted headers from tail to deleteTo-1 (50 headers)
	expectedDeleted := int(deleteTo - tail.Height())
	assert.Equal(t, expectedDeleted, deleted)

	// Verify deleted headers are gone
	hdr, err := store.GetByHeight(ctx, tail.Height())
	assert.Error(t, err)
	assert.Nil(t, hdr)

	// Verify headers at and above deleteTo still exist
	hdr, err = store.GetByHeight(ctx, deleteTo)
	assert.NoError(t, err)
	assert.NotNil(t, hdr)
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
		// sleep a bit before writing a header
		// so GetByHeight will wait in a given height
		time.Sleep(100 * time.Millisecond)
		_ = store.Append(ctx, suite.Head())
	}()

	_, err = store.GetByHeight(ctx, 1)
	require.ErrorIs(t, err, header.ErrNotFound)
}

func TestStoreInit(t *testing.T) {
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

	headers := suite.GenDummyHeaders(10)
	h := headers[len(headers)-1]
	err = store.Append(ctx, h) // init should work with any height, not only 1
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

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
	assert.Equal(t, suite.Head().Height(), reopenedHead.Height())
	assert.NotEqual(t, head.Height(), reopenedHead.Height())

	tail, err := reopenedStore.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, tail.Hash(), h.Hash())

	err = reopenedStore.Stop(ctx)
	require.NoError(t, err)
}

func TestStore_HasAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)

	headers := suite.GenDummyHeaders(100)

	err = store.Append(ctx, headers...)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)

	err = store.DeleteRange(ctx, tail.Height(), 50)
	require.NoError(t, err)

	has := store.HasAt(ctx, 100)
	assert.True(t, has)

	has = store.HasAt(ctx, 50)
	assert.True(t, has)

	has = store.HasAt(ctx, 49)
	assert.False(t, has)

	has = store.HasAt(ctx, 10)
	assert.False(t, has)

	has = store.HasAt(ctx, 0)
	assert.False(t, has)
}

func TestStore_DeleteRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	t.Cleanup(cancel)

	t.Run("delete range from head down", func(t *testing.T) {
		suite := headertest.NewTestSuite(t)
		ds := sync.MutexWrap(datastore.NewMapDatastore())
		store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

		const count = 20
		in := suite.GenDummyHeaders(count)
		err := store.Append(ctx, in...)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Genesis is at height 1, GenDummyHeaders(20) creates headers 2-21
		// So head should be at height 21, tail at height 1
		head, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(21), head.Height())

		// Delete from height 16 to 22 (should delete 16, 17, 18, 19, 20, 21)
		err = store.DeleteRange(ctx, 16, 22)
		require.NoError(t, err)

		// Verify new head is at height 15
		newHead, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(15), newHead.Height())

		// Verify deleted headers are gone
		for h := uint64(16); h <= 21; h++ {
			has := store.HasAt(ctx, h)
			assert.False(t, has, "height %d should be deleted", h)
		}

		// Verify remaining headers still exist
		for h := uint64(1); h <= 15; h++ {
			has := store.HasAt(ctx, h)
			assert.True(t, has, "height %d should still exist", h)
		}
	})

	t.Run("delete range in middle should fail", func(t *testing.T) {
		suite := headertest.NewTestSuite(t)
		ds := sync.MutexWrap(datastore.NewMapDatastore())
		store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

		const count = 20
		in := suite.GenDummyHeaders(count)
		err := store.Append(ctx, in...)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Try to delete a range in the middle (heights 8-12) which would create gaps
		err = store.DeleteRange(ctx, 8, 12)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "would create gaps in the store")

		// Verify all headers still exist since the operation failed
		for h := uint64(1); h <= 21; h++ {
			has := store.HasAt(ctx, h)
			assert.True(t, has, "height %d should still exist after failed deletion", h)
		}
	})

	t.Run("delete range from tail up", func(t *testing.T) {
		suite := headertest.NewTestSuite(t)
		ds := sync.MutexWrap(datastore.NewMapDatastore())
		store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

		const count = 20
		in := suite.GenDummyHeaders(count)
		err := store.Append(ctx, in...)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		originalHead, err := store.Head(ctx)
		require.NoError(t, err)

		// Delete from tail height to height 10
		err = store.DeleteRange(ctx, 1, 10)
		require.NoError(t, err)

		// Verify head is unchanged
		head, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, originalHead.Height(), head.Height())

		// Verify tail moved to height 10
		tail, err := store.Tail(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(10), tail.Height())

		// Verify deleted headers are gone
		for h := uint64(1); h < 10; h++ {
			has := store.HasAt(ctx, h)
			assert.False(t, has, "height %d should be deleted", h)
		}

		// Verify remaining headers still exist
		for h := uint64(10); h <= 21; h++ {
			has := store.HasAt(ctx, h)
			assert.True(t, has, "height %d should still exist", h)
		}
	})

	t.Run("delete range completely out of bounds errors", func(t *testing.T) {
		suite := headertest.NewTestSuite(t)
		ds := sync.MutexWrap(datastore.NewMapDatastore())
		store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

		const count = 20
		in := suite.GenDummyHeaders(count)
		err := store.Append(ctx, in...)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		originalHead, err := store.Head(ctx)
		require.NoError(t, err)
		originalTail, err := store.Tail(ctx)
		require.NoError(t, err)

		// Delete range completely above head - should error (to > head+1)
		err = store.DeleteRange(ctx, 200, 300)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "beyond current head+1")

		// Verify head and tail are unchanged
		head, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, originalHead.Height(), head.Height())

		tail, err := store.Tail(ctx)
		require.NoError(t, err)
		assert.Equal(t, originalTail.Height(), tail.Height())

		// Verify all original headers still exist
		for h := uint64(1); h <= 21; h++ {
			has := store.HasAt(ctx, h)
			assert.True(t, has, "height %d should still exist", h)
		}
	})

	t.Run("invalid range errors", func(t *testing.T) {
		suite := headertest.NewTestSuite(t)
		ds := sync.MutexWrap(datastore.NewMapDatastore())
		store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

		const count = 20
		in := suite.GenDummyHeaders(count)
		err := store.Append(ctx, in...)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// from >= to should error
		err = store.DeleteRange(ctx, 50, 50)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "from must be less than to")

		// from > to should error
		err = store.DeleteRange(ctx, 60, 50)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "from must be less than to")

		// from below tail should error
		err = store.DeleteRange(ctx, 0, 5)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "below current tail")

		// middle deletion should error
		err = store.DeleteRange(ctx, 10, 15)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "would create gaps")
	})
}

func TestStore_DeleteRange_SingleHeader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	// Add single header at height 1 (genesis is at 0)
	headers := suite.GenDummyHeaders(1)
	err := store.Append(ctx, headers...)
	require.NoError(t, err)

	// Should not be able to delete below tail
	err = store.DeleteRange(ctx, 0, 1)
	require.Error(t, err) // should error - would delete below tail
}

func TestStore_DeleteRange_Synchronized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	err := store.Append(ctx, suite.GenDummyHeaders(50)...)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Ensure sync completes
	err = store.Sync(ctx)
	require.NoError(t, err)

	// Delete from height 26 to head+1
	head, err := store.Head(ctx)
	require.NoError(t, err)

	err = store.DeleteRange(ctx, 26, head.Height()+1)
	require.NoError(t, err)

	// Verify head is now at height 25
	newHead, err := store.Head(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 25, newHead.Height())

	// Verify headers above 25 are gone
	for h := uint64(26); h <= 50; h++ {
		has := store.HasAt(ctx, h)
		assert.False(t, has, "height %d should be deleted", h)
	}

	// Verify headers at and below 25 still exist
	for h := uint64(1); h <= 25; h++ {
		has := store.HasAt(ctx, h)
		assert.True(t, has, "height %d should still exist", h)
	}
}

func TestStore_DeleteRange_OnDeleteHandlers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	err := store.Append(ctx, suite.GenDummyHeaders(50)...)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Get the actual head height to calculate expected deletions
	head, err := store.Head(ctx)
	require.NoError(t, err)

	var deletedHeights []uint64
	store.OnDelete(func(ctx context.Context, height uint64) error {
		deletedHeights = append(deletedHeights, height)
		return nil
	})

	// Delete from height 41 to head+1
	err = store.DeleteRange(ctx, 41, head.Height()+1)
	require.NoError(t, err)

	// Verify onDelete was called for each deleted height (from 41 to head height)
	var expectedDeleted []uint64
	for h := uint64(41); h <= head.Height(); h++ {
		expectedDeleted = append(expectedDeleted, h)
	}
	assert.ElementsMatch(t, expectedDeleted, deletedHeights)
}

func TestStore_DeleteRange_LargeRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(100))

	// Create a large number of headers to trigger parallel deletion
	const count = 15000
	headers := suite.GenDummyHeaders(count)
	err := store.Append(ctx, headers...)
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond) // allow time for large batch to write

	// Get head height for deletion range
	head, err := store.Head(ctx)
	require.NoError(t, err)

	// Delete a large range to test parallel deletion path (from 5001 to head+1)
	const keepHeight = 5000
	err = store.DeleteRange(ctx, keepHeight+1, head.Height()+1)
	require.NoError(t, err)

	// Verify new head
	newHead, err := store.Head(ctx)
	require.NoError(t, err)
	require.EqualValues(t, keepHeight, newHead.Height())

	// Spot check that high numbered headers are gone
	for h := uint64(keepHeight + 1000); h <= count; h += 1000 {
		has := store.HasAt(ctx, h)
		assert.False(t, has, "height %d should be deleted", h)
	}

	// Spot check that low numbered headers still exist
	for h := uint64(1000); h <= keepHeight; h += 1000 {
		has := store.HasAt(ctx, h)
		assert.True(t, has, "height %d should still exist", h)
	}
}

func TestStore_DeleteRange_Wipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(100))

	// Create a large number of headers
	const count = 15000
	headers := suite.GenDummyHeaders(count)
	err := store.Append(ctx, headers...)
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond) // allow time for large batch to write

	// Get head height for deletion range
	head, err := store.Head(ctx)
	require.NoError(t, err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)

	// Delete a large range to test parallel deletion path (from 5001 to head+1)
	err = store.DeleteRange(ctx, tail.Height(), head.Height()+1)
	require.NoError(t, err)

	// Verify new head
	_, err = store.Head(ctx)
	require.Error(t, err)
	_, err = store.Tail(ctx)
	require.Error(t, err)
}

func TestStore_DeleteRange_ValidationErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	err := store.Append(ctx, suite.GenDummyHeaders(20)...)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)

	head, err := store.Head(ctx)
	require.NoError(t, err)

	tests := []struct {
		name   string
		from   uint64
		to     uint64
		errMsg string
	}{
		{
			name:   "delete from below tail boundary",
			from:   tail.Height() - 1,
			to:     tail.Height() + 5,
			errMsg: "below current tail",
		},
		{
			name:   "invalid range - from equals to",
			from:   10,
			to:     10,
			errMsg: "from must be less than to",
		},
		{
			name:   "invalid range - from greater than to",
			from:   15,
			to:     10,
			errMsg: "from must be less than to",
		},
		{
			name:   "delete to beyond head+1",
			from:   tail.Height(),
			to:     head.Height() + 10,
			errMsg: "beyond current head+1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.DeleteRange(ctx, tt.from, tt.to)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestStore_DeleteRange_PartialDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	t.Cleanup(cancel)

	t.Run("partial delete from head with timeout and recovery", func(t *testing.T) {
		suite := headertest.NewTestSuite(t)
		ds := sync.MutexWrap(datastore.NewMapDatastore())
		store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

		// Add headers
		const count = 1000
		in := suite.GenDummyHeaders(count)
		err := store.Append(ctx, in...)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		originalHead, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(1001), originalHead.Height())

		originalTail, err := store.Tail(ctx)
		require.NoError(t, err)

		// Create a context with very short timeout to trigger partial delete
		shortCtx, shortCancel := context.WithTimeout(ctx, 1*time.Millisecond)
		defer shortCancel()

		// Try to delete from height 500 to head+1 (may partially complete or fully complete)
		deleteErr := store.DeleteRange(shortCtx, 500, 1002)

		// Get current state - head should be updated to reflect progress (set to 499 since from=500)
		head, err := store.Head(ctx)
		require.NoError(t, err)

		// Tail should not have changed (we're deleting from head side)
		tail, err := store.Tail(ctx)
		require.NoError(t, err)
		assert.Equal(t, originalTail.Height(), tail.Height())

		if deleteErr != nil {
			// Partial delete occurred - head should be updated to from-1
			assert.True(t, errors.Is(deleteErr, context.DeadlineExceeded) ||
				errors.Is(deleteErr, errDeleteTimeout),
				"expected timeout error, got: %v", deleteErr)

			// Head should be updated to reflect that deletion started at 500
			assert.Equal(t, uint64(499), head.Height())

			// Verify we can still read the new head
			_, err = store.Get(ctx, head.Hash())
			require.NoError(t, err)

			// Since head is already at 499, there's nothing more to delete from the head side
			// The partial delete already completed the deletion by setting head to from-1
		}

		// After completion, verify final state
		newHead, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(499), newHead.Height())

		// Verify deleted headers are gone
		for h := uint64(500); h <= 1001; h++ {
			has := store.HasAt(ctx, h)
			assert.False(t, has, "height %d should be deleted", h)
		}

		// Verify remaining headers exist
		for h := uint64(1); h <= 499; h++ {
			has := store.HasAt(ctx, h)
			assert.True(t, has, "height %d should still exist", h)
		}
	})

	t.Run("partial delete from tail with timeout and recovery", func(t *testing.T) {
		suite := headertest.NewTestSuite(t)
		ds := sync.MutexWrap(datastore.NewMapDatastore())
		store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

		// Add headers
		const count = 1000
		in := suite.GenDummyHeaders(count)
		err := store.Append(ctx, in...)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		originalHead, err := store.Head(ctx)
		require.NoError(t, err)
		originalTail, err := store.Tail(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), originalTail.Height())

		// Create a context with very short timeout
		shortCtx, shortCancel := context.WithTimeout(ctx, 1*time.Millisecond)
		defer shortCancel()

		// Try to delete from tail to height 500 (may partially complete or fully complete)
		deleteErr := store.DeleteRange(shortCtx, 1, 500)

		// Head should not have changed (we're deleting from tail side)
		head, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, originalHead.Height(), head.Height())

		// Get current tail - it should be updated to reflect progress
		tail, err := store.Tail(ctx)
		require.NoError(t, err)

		if deleteErr != nil {
			// Partial delete occurred
			assert.True(t, errors.Is(deleteErr, context.DeadlineExceeded) ||
				errors.Is(deleteErr, errDeleteTimeout),
				"expected timeout error, got: %v", deleteErr)

			// Tail should be updated to reflect actual progress
			assert.Greater(t, tail.Height(), originalTail.Height())

			// Now complete the deletion with proper timeout - use current tail
			err = store.DeleteRange(ctx, tail.Height(), 500)
			require.NoError(t, err)
		}

		// After completion, verify final state
		newTail, err := store.Tail(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(500), newTail.Height())

		// Head should be unchanged
		head, err = store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, originalHead.Height(), head.Height())

		// Verify deleted headers are gone
		for h := uint64(1); h < 500; h++ {
			has := store.HasAt(ctx, h)
			assert.False(t, has, "height %d should be deleted", h)
		}

		// Verify remaining headers exist
		for h := uint64(500); h <= 1001; h++ {
			has := store.HasAt(ctx, h)
			assert.True(t, has, "height %d should still exist", h)
		}
	})

	t.Run("multiple partial deletes eventually succeed", func(t *testing.T) {
		suite := headertest.NewTestSuite(t)
		ds := sync.MutexWrap(datastore.NewMapDatastore())
		store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

		// Add headers
		const count = 800
		in := suite.GenDummyHeaders(count)
		err := store.Append(ctx, in...)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Attempt delete with progressively longer timeouts
		from, to := uint64(300), uint64(802)
		maxAttempts := 5

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			attemptCtx, attemptCancel := context.WithTimeout(ctx,
				time.Duration(attempt*20)*time.Millisecond)

			err = store.DeleteRange(attemptCtx, from, to)
			attemptCancel()

			if err == nil {
				// Success!
				break
			}

			// Verify store remains consistent after each failed attempt
			head, err := store.Head(ctx)
			require.NoError(t, err)
			_, err = store.Get(ctx, head.Hash())
			require.NoError(t, err)

			if attempt == maxAttempts {
				// Last attempt with full context
				err = store.DeleteRange(ctx, from, to)
				require.NoError(t, err)
			}
		}

		// Verify final state
		newHead, err := store.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(299), newHead.Height())

		// Verify deleted range is gone
		for h := from; h <= 801; h++ {
			has := store.HasAt(ctx, h)
			assert.False(t, has, "height %d should be deleted", h)
		}
	})
}
