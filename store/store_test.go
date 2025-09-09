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

func TestStore_DeleteTo(t *testing.T) {
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

			err := store.DeleteTo(ctx, tt.to)
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

func TestStore_DeleteTo_EmptyStore(t *testing.T) {
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

	err = store.DeleteTo(ctx, 101)
	require.NoError(t, err)

	// assert store is empty
	tail, err := store.Tail(ctx)
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

func TestStore_DeleteTo_MoveHeadAndTail(t *testing.T) {
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

	gap := suite.GenDummyHeaders(10)

	err = store.Append(ctx, suite.GenDummyHeaders(10)...)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	err = store.DeleteTo(ctx, 111)
	require.NoError(t, err)

	// assert store is not empty
	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, int(gap[len(gap)-1].Height()+1), int(tail.Height()))
	head, err := store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, suite.Head().Height(), head.Height())

	// assert that it is still not empty after restart
	err = store.Stop(ctx)
	require.NoError(t, err)
	err = store.Start(ctx)
	require.NoError(t, err)

	tail, err = store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, gap[len(gap)-1].Height()+1, tail.Height())
	head, err = store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, suite.Head().Height(), head.Height())
}

func TestStore_DeleteTo_Synchronized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	err := store.Append(ctx, suite.GenDummyHeaders(50)...)
	require.NoError(t, err)

	err = store.Append(ctx, suite.GenDummyHeaders(50)...)
	require.NoError(t, err)

	err = store.Append(ctx, suite.GenDummyHeaders(50)...)
	require.NoError(t, err)

	err = store.DeleteTo(ctx, 100)
	require.NoError(t, err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 100, tail.Height())
}

func TestStore_OnDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)

	err = store.Append(ctx, suite.GenDummyHeaders(50)...)
	require.NoError(t, err)
	// artificial gap
	_ = suite.GenDummyHeaders(50)

	err = store.Append(ctx, suite.GenDummyHeaders(50)...)
	require.NoError(t, err)

	deleted := 0
	store.OnDelete(func(ctx context.Context, height uint64) error {
		hdr, err := store.GetByHeight(ctx, height)
		assert.NoError(t, err, "must be accessible")
		require.NotNil(t, hdr)
		deleted++
		return nil
	})

	err = store.DeleteTo(ctx, 101)
	require.NoError(t, err)
	assert.Equal(t, 50, deleted)

	hdr, err := store.GetByHeight(ctx, 50)
	assert.Error(t, err)
	assert.Nil(t, hdr)
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

	err = store.DeleteTo(ctx, 50)
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

func TestStore_DeleteFromHead(t *testing.T) {
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
		wantHead  uint64
		wantError bool
	}{
		{
			name:      "initial delete from head request",
			to:        85,
			wantHead:  85,
			wantError: false,
		},
		{
			name:      "no-op delete request - to equals current head",
			to:        85, // same as previous head
			wantError: false,
		},
		{
			name:      "valid delete from head request",
			to:        50,
			wantHead:  50,
			wantError: false,
		},
		{
			name:      "delete to height above current head",
			to:        200,
			wantError: true,
		},
		{
			name:      "delete to height below tail",
			to:        0,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeHead, err := store.Head(ctx)
			require.NoError(t, err)
			beforeTail, err := store.Tail(ctx)
			require.NoError(t, err)

			// manually add something to the pending for assert at the bottom
			if idx := beforeHead.Height() - 1; idx < count && idx > 0 {
				store.pending.Append(in[idx-1])
				defer store.pending.Reset()
			}

			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			err = store.DeleteFromHead(ctx, tt.to)
			if tt.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// check that cache and pending doesn't contain deleted headers
			for h := tt.to + 1; h <= beforeHead.Height(); h++ {
				hash, ok := hashes[h]
				if !ok {
					continue // skip heights that weren't in our original set
				}
				assert.False(
					t,
					store.cache.Contains(hash.String()),
					"height %d should be removed from cache",
					h,
				)
				assert.False(
					t,
					store.heightIndex.cache.Contains(h),
					"height %d should be removed from height index",
					h,
				)
				assert.False(
					t,
					store.pending.Has(hash),
					"height %d should be removed from pending",
					h,
				)
			}

			// verify new head is correct
			if tt.wantHead > 0 {
				head, err := store.Head(ctx)
				require.NoError(t, err)
				require.EqualValues(t, tt.wantHead, head.Height())
			}

			// verify tail hasn't changed
			tail, err := store.Tail(ctx)
			require.NoError(t, err)
			require.EqualValues(t, beforeTail.Height(), tail.Height())

			// verify headers below 'to' still exist
			for h := beforeTail.Height(); h <= tt.to; h++ {
				has := store.HasAt(ctx, h)
				assert.True(t, has, "height %d should still exist", h)
			}
		})
	}
}

func TestStore_DeleteFromHead_EmptyStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store, err := NewStore[*headertest.DummyHeader](ds)
	require.NoError(t, err)

	err = store.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Stop(ctx)
	})

	// wait for store to initialize
	time.Sleep(10 * time.Millisecond)

	// should handle empty store gracefully
	err = store.DeleteFromHead(ctx, 50)
	require.Error(t, err) // should error because store is empty
}

func TestStore_DeleteFromHead_SingleHeader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	// Add single header at height 1 (genesis is at 0)
	headers := suite.GenDummyHeaders(1)
	err := store.Append(ctx, headers...)
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)

	// Should not be able to delete the only header
	err = store.DeleteFromHead(ctx, 0)
	require.Error(t, err) // should error - would delete below tail
}

func TestStore_DeleteFromHead_Synchronized(t *testing.T) {
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

	err = store.DeleteFromHead(ctx, 25)
	require.NoError(t, err)

	// Verify head is now at height 25
	head, err := store.Head(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 25, head.Height())

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

func TestStore_DeleteFromHead_OnDeleteHandlers(t *testing.T) {
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

	err = store.DeleteFromHead(ctx, 40)
	require.NoError(t, err)

	// Verify onDelete was called for each deleted height (from 41 to head height)
	var expectedDeleted []uint64
	for h := uint64(41); h <= head.Height(); h++ {
		expectedDeleted = append(expectedDeleted, h)
	}
	assert.ElementsMatch(t, expectedDeleted, deletedHeights)
}

func TestStore_DeleteFromHead_LargeRange(t *testing.T) {
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

	// Delete a large range to test parallel deletion path
	const keepHeight = 5000
	err = store.DeleteFromHead(ctx, keepHeight)
	require.NoError(t, err)

	// Verify new head
	head, err := store.Head(ctx)
	require.NoError(t, err)
	require.EqualValues(t, keepHeight, head.Height())

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

func TestStore_DeleteFromHead_ValidationErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	store := NewTestStore(t, ctx, ds, suite.Head(), WithWriteBatchSize(10))

	err := store.Append(ctx, suite.GenDummyHeaders(20)...)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	head, err := store.Head(ctx)
	require.NoError(t, err)
	tail, err := store.Tail(ctx)
	require.NoError(t, err)

	tests := []struct {
		name   string
		to     uint64
		errMsg string
	}{
		{
			name:   "to below tail",
			to:     tail.Height() - 1,
			errMsg: "below current tail",
		},
		{
			name:   "to above head",
			to:     head.Height() + 1,
			errMsg: "above current head",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.DeleteFromHead(ctx, tt.to)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}
