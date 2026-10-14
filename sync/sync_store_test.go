package sync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/store"
)

func TestSyncStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ts := headertest.NewTestSuite(t)
	s := headertest.NewStore[*headertest.DummyHeader](t, ts, 100)
	ss := newSyncStore(s)

	h, err := ss.Head(ctx)
	assert.NoError(t, err)
	assert.Equal(t, ts.Head(), h)

	err = ss.Append(ctx, ts.NextHeader())
	assert.NoError(t, err)

	h, err = ss.Head(ctx)
	assert.NoError(t, err)
	assert.Equal(t, ts.Head(), h)
}

type dummy = *headertest.DummyHeader

// fakeStore can delay one Head snapshot and writes ending at selected heights.
type fakeStore struct {
	header.Store[dummy]
	mu            sync.Mutex
	head          dummy
	appendErr     error
	appended      [][]dummy
	headGate      chan struct{}
	headEntered   chan struct{}
	appendGates   map[uint64]chan struct{}
	appendEntered chan uint64
}

func (f *fakeStore) Head(ctx context.Context, _ ...header.HeadOption[dummy]) (dummy, error) {
	f.mu.Lock()
	head, gate := f.head, f.headGate
	f.headGate = nil // Only the lazy reader is delayed; Append can read Head too.
	f.mu.Unlock()
	if gate != nil {
		close(f.headEntered)
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if head == nil {
		return nil, header.ErrEmptyStore
	}
	return head, nil
}

func (f *fakeStore) Append(ctx context.Context, headers ...dummy) error {
	last := headers[len(headers)-1]
	f.mu.Lock()
	gate := f.appendGates[last.Height()]
	f.mu.Unlock()
	if gate != nil {
		select {
		case f.appendEntered <- last.Height():
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	f.appended = append(f.appended, headers)
	if f.head == nil || last.Height() > f.head.Height() {
		f.head = last
	}
	return nil
}

func newTestSyncStore[H header.Header[H]](store header.Store[H]) *syncStore[H] {
	ss := newSyncStore(store)
	return &ss
}

func cachedHeight(ss *syncStore[dummy]) uint64 {
	if h := ss.head.Load(); h != nil {
		return (*h).Height()
	}
	return 0
}

func receive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}

func TestSyncStore_ConcurrentAppendNoRegress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	hdrs := headertest.NewTestSuite(t).GenDummyHeaders(20)
	gateA, gateB := make(chan struct{}), make(chan struct{})
	fs := &fakeStore{
		head:          hdrs[9],
		appendGates:   map[uint64]chan struct{}{20: gateA, 11: gateB},
		appendEntered: make(chan uint64, 2),
	}
	ss := newTestSyncStore(fs)
	head, err := ss.Head(ctx) // Both writers must start with a warm cache.
	require.NoError(t, err)
	require.EqualValues(t, 10, head.Height())

	errA, errB := make(chan error, 1), make(chan error, 1)
	go func() { errA <- ss.Append(ctx, hdrs[10:20]...) }()
	require.EqualValues(t, 20, receive(t, ctx, fs.appendEntered))
	go func() { errB <- ss.Append(ctx, hdrs[10]) }()

	// B must interleave with A's write before A is released. With serialized Appends,
	// B waits for the lock and doesn't reach the store. Without serialization, B reads
	// the old head and reaches the store write, so the test fails on a head regression.
	bWriting := false
	select {
	case height := <-fs.appendEntered:
		require.EqualValues(t, 11, height)
		bWriting = true
	case <-time.After(100 * time.Millisecond):
	}

	close(gateA)
	require.NoError(t, receive(t, ctx, errA))
	require.EqualValues(t, 20, cachedHeight(ss))
	if !bWriting {
		require.EqualValues(t, 11, receive(t, ctx, fs.appendEntered))
	}
	close(gateB)
	require.NoError(t, receive(t, ctx, errB))
	head, err = ss.Head(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 20, head.Height(), "cached head regressed")
	assert.EqualValues(t, 20, fs.head.Height())
}

// A writer waiting for another Append must give up when its own context is done.
func TestSyncStore_AppendWaitRespectsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	hdrs := headertest.NewTestSuite(t).GenDummyHeaders(11)
	gate := make(chan struct{})
	fs := &fakeStore{
		head:          hdrs[9],
		appendGates:   map[uint64]chan struct{}{11: gate},
		appendEntered: make(chan uint64, 1),
	}
	ss := newTestSyncStore(fs)

	errA := make(chan error, 1)
	go func() { errA <- ss.Append(ctx, hdrs[10]) }()
	receive(t, ctx, fs.appendEntered)

	waitCtx, waitCancel := context.WithCancel(ctx)
	defer waitCancel()
	errB := make(chan error, 1)
	go func() { errB <- ss.Append(waitCtx, hdrs[10]) }()
	select {
	case err := <-errB:
		t.Fatal("writer B didn't wait for writer A:", err)
	case <-time.After(50 * time.Millisecond):
	}
	waitCancel()
	select {
	case err := <-errB:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal("waiting writer ignored its canceled context")
	}

	select {
	case err := <-errA:
		t.Fatal("writer A finished before its write was released:", err)
	default:
	}
	close(gate)
	require.NoError(t, receive(t, ctx, errA))
	assert.EqualValues(t, 11, cachedHeight(ss))
	assert.Len(t, fs.appended, 1, "canceled writer must not write")
}

// Over the real asynchronous store, the published head and the written headers agree
// once the store has processed its write queue.
func TestSyncStore_AppendRealStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	hdrs := headertest.NewTestSuite(t).GenDummyHeaders(30)
	st := store.NewTestStore(t, ctx, dssync.MutexWrap(datastore.NewMapDatastore()), hdrs[0])
	ss := newTestSyncStore(st)

	require.NoError(t, ss.Append(ctx, hdrs[1:10]...))
	require.NoError(t, ss.Append(ctx, hdrs[10:]...))
	require.NoError(t, ss.Append(ctx, hdrs[5:8]...)) // below head: written, head stays
	require.EqualValues(t, 30, cachedHeight(ss))

	require.NoError(t, st.Sync(ctx))
	head, err := st.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, hdrs[29].Hash(), head.Hash())
	for _, h := range hdrs {
		stored, err := st.GetByHeight(ctx, h.Height())
		require.NoError(t, err)
		assert.Equal(t, h.Hash(), stored.Hash())
	}
}

func TestSyncStore_AppendPublishesHeadAfterSuccess(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		cancelWrite bool
	}{
		{name: "successful write"},
		{name: "failed write", err: errors.New("stopped store")},
		{name: "canceled write", cancelWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			hdrs := headertest.NewTestSuite(t).GenDummyHeaders(11)
			gate := make(chan struct{})
			fs := &fakeStore{
				head: hdrs[9], appendErr: tc.err,
				appendGates:   map[uint64]chan struct{}{11: gate},
				appendEntered: make(chan uint64, 1),
			}
			ss := newTestSyncStore(fs)
			reqCtx, reqCancel := context.WithCancel(ctx)
			defer reqCancel()
			result := make(chan error, 1)
			go func() { result <- ss.Append(reqCtx, hdrs[10]) }()
			receive(t, ctx, fs.appendEntered)
			head, err := ss.Head(ctx)
			require.NoError(t, err)
			assert.EqualValues(t, 10, head.Height(), "head published before write completed")
			if tc.cancelWrite {
				reqCancel()
			} else {
				close(gate)
			}
			err = receive(t, ctx, result)
			switch {
			case tc.cancelWrite:
				require.ErrorIs(t, err, context.Canceled)
				close(gate)
			case tc.err != nil:
				require.ErrorIs(t, err, tc.err)
			default:
				require.NoError(t, err)
				assert.EqualValues(t, 11, cachedHeight(ss))
				return
			}
			assert.EqualValues(t, 10, cachedHeight(ss), "head published for a failed write")
			fs.mu.Lock()
			fs.appendErr = nil
			fs.appendGates = nil
			fs.mu.Unlock()
			require.NoError(t, ss.Append(ctx, hdrs[10]), "retry of failed heights must succeed")
			assert.EqualValues(t, 11, cachedHeight(ss))
		})
	}
}

func TestSyncStore_AppendInitAndBelowHead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	hdrs := headertest.NewTestSuite(t).GenDummyHeaders(12)

	t.Run("empty store init", func(t *testing.T) {
		fs := &fakeStore{appendErr: errors.New("boom")}
		ss := newTestSyncStore(fs)

		require.Error(t, ss.Append(ctx, hdrs[4:8]...))
		_, err := ss.Head(ctx)
		require.ErrorIs(t, err, header.ErrEmptyStore)

		fs.appendErr = nil
		// init path accepts any range, adjacency isn't checked
		require.NoError(t, ss.Append(ctx, hdrs[4:8]...))
		assert.EqualValues(t, 8, cachedHeight(ss))
	})

	t.Run("below head is written but doesn't move head", func(t *testing.T) {
		fs := &fakeStore{head: hdrs[9]}
		ss := newTestSyncStore(fs)

		require.NoError(t, ss.Append(ctx, hdrs[2]))
		assert.EqualValues(t, 10, cachedHeight(ss))
		require.Len(t, fs.appended, 1)
		assert.EqualValues(t, 3, fs.appended[0][0].Height())
	})

	t.Run("non-adjacent", func(t *testing.T) {
		fs := &fakeStore{head: hdrs[9]}
		ss := newTestSyncStore(fs)

		var nonAdj *errNonAdjacent
		require.ErrorAs(t, ss.Append(ctx, hdrs[11]), &nonAdj)
		require.ErrorAs(t, ss.Append(ctx, hdrs[9]), &nonAdj)
		require.ErrorAs(t, ss.Append(ctx, hdrs[10], hdrs[11], hdrs[10]), &nonAdj)
		assert.Empty(t, fs.appended)
		assert.EqualValues(t, 10, cachedHeight(ss))
	})
}

// Lazy init in Head must not overwrite a newer head published by Append.
func TestSyncStore_HeadLazyInitNoRegress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	hdrs := headertest.NewTestSuite(t).GenDummyHeaders(11)
	gate := make(chan struct{})
	fs := &fakeStore{head: hdrs[9], headGate: gate, headEntered: make(chan struct{})}
	ss := newTestSyncStore(fs)
	type result struct {
		head dummy
		err  error
	}
	lazy := make(chan result, 1)
	go func() {
		head, err := ss.Head(ctx)
		lazy <- result{head, err}
	}()
	receive(t, ctx, fs.headEntered)
	require.NoError(t, ss.Append(ctx, hdrs[10]))
	require.EqualValues(t, 11, cachedHeight(ss))
	close(gate)
	res := receive(t, ctx, lazy)
	require.NoError(t, res.err)
	assert.EqualValues(t, 11, res.head.Height())
	assert.EqualValues(t, 11, cachedHeight(ss), "lazy init overwrote a newer head")
}
