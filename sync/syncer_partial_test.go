package sync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/local"
)

// partialGetter wraps an underlying Getter and truncates GetRangeByHeight
// responses to simulate a peer that returns a contiguous prefix of the
// requested range with a nil error.
type partialGetter[H header.Header[H]] struct {
	header.Getter[H]
	// truncateTo limits the number of headers returned per call.
	// If 0, returns the full range. If headers returned exceed truncateTo,
	// only the first truncateTo are returned.
	truncateTo int
	// emptyOnce, if true, makes the first call return an empty slice with
	// nil error (a contract violation we want to test the syncer rejects).
	emptyOnce atomic.Bool
	calls     atomic.Int32
}

func (p *partialGetter[H]) GetRangeByHeight(ctx context.Context, from H, to uint64) ([]H, error) {
	p.calls.Add(1)
	if p.emptyOnce.Load() {
		p.emptyOnce.Store(false)
		var empty []H
		return empty, nil
	}
	headers, err := p.Getter.GetRangeByHeight(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if p.truncateTo > 0 && len(headers) > p.truncateTo {
		return headers[:p.truncateTo], nil
	}
	return headers, nil
}

// TestSyncer_PartialRangeTail simulates a peer that consistently returns
// fewer headers than requested (e.g. peer is itself catching up).
// The syncer must keep requesting the remainder until it reaches the target,
// without skipping any headers.
func TestSyncer_PartialRangeTail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	const total = 50
	require.NoError(t, remoteStore.Append(ctx, suite.GenDummyHeaders(total)...))

	const truncate = 7
	getter := &partialGetter[*headertest.DummyHeader]{
		Getter:     local.NewExchange(remoteStore),
		truncateTo: truncate,
	}

	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		getter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)

	// drive requestHeaders directly to keep the test deterministic
	err = syncer.requestHeaders(ctx, head, head.Height()+total)
	require.NoError(t, err)

	// GetByHeight blocks via heightSub until the height is observed.
	gotHead, err := localStore.GetByHeight(ctx, head.Height()+total)
	require.NoError(t, err)
	assert.Equal(t, head.Height()+total, gotHead.Height())

	// at least ceil(total/truncate) calls must have been made
	minCalls := (total + truncate - 1) / truncate
	assert.GreaterOrEqual(t, int(getter.calls.Load()), minCalls)
}

// TestSyncer_PartialRangeMidRequest checks that when the partial happens
// mid-way (after several full-size responses), the syncer still advances
// correctly and reaches the target without skipping headers.
func TestSyncer_PartialRangeMidRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	// use a size > MaxRangeRequestSize to ensure the syncer paginates
	total := int(header.MaxRangeRequestSize) + 30
	require.NoError(t, remoteStore.Append(ctx, suite.GenDummyHeaders(total)...))

	// truncate only the second batch to a small prefix
	var calls atomic.Int32
	getter := wrapGetterFunc(local.NewExchange(remoteStore),
		func(ctx context.Context, from *headertest.DummyHeader, to uint64) ([]*headertest.DummyHeader, error) {
			n := calls.Add(1)
			headers, err := local.NewExchange(remoteStore).GetRangeByHeight(ctx, from, to)
			if err != nil {
				return nil, err
			}
			if n == 2 && len(headers) > 5 {
				return headers[:5], nil
			}
			return headers, nil
		},
	)

	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		getter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)

	err = syncer.requestHeaders(ctx, head, head.Height()+uint64(total))
	require.NoError(t, err)

	gotHead, err := localStore.GetByHeight(ctx, head.Height()+uint64(total))
	require.NoError(t, err)
	assert.Equal(t, head.Height()+uint64(total), gotHead.Height())
}

// TestSyncer_PartialRangeEmptyReturnsError checks that a getter which
// violates the contract by returning an empty slice with nil error is
// rejected with an error, rather than panicking.
func TestSyncer_PartialRangeEmptyReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	require.NoError(t, remoteStore.Append(ctx, suite.GenDummyHeaders(10)...))

	getter := &partialGetter[*headertest.DummyHeader]{
		Getter: local.NewExchange(remoteStore),
	}
	getter.emptyOnce.Store(true)

	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		getter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)

	err = syncer.requestHeaders(ctx, head, head.Height()+10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty range")
}

// TestSyncer_PartialRangeNonAdjacentReturnsError checks that a getter which
// violates the contract by returning headers that don't start at
// fromHead.Height()+1 is rejected with an error.
func TestSyncer_PartialRangeNonAdjacentReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	require.NoError(t, remoteStore.Append(ctx, suite.GenDummyHeaders(10)...))

	getter := wrapGetterFunc(local.NewExchange(remoteStore),
		func(ctx context.Context, from *headertest.DummyHeader, to uint64) ([]*headertest.DummyHeader, error) {
			headers, err := local.NewExchange(remoteStore).GetRangeByHeight(ctx, from, to)
			if err != nil {
				return nil, err
			}
			if len(headers) > 1 {
				// drop the first header to create a gap from fromHead
				return headers[1:], nil
			}
			return headers, nil
		},
	)

	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		getter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)

	err = syncer.requestHeaders(ctx, head, head.Height()+10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-adjacent")
}

// getterFunc adapts a function into a header.Getter by delegating Head/Get/GetByHeight
// to an underlying getter while overriding GetRangeByHeight.
type getterFunc[H header.Header[H]] struct {
	header.Getter[H]
	fn func(ctx context.Context, from H, to uint64) ([]H, error)
}

func wrapGetterFunc[H header.Header[H]](
	inner header.Getter[H],
	fn func(ctx context.Context, from H, to uint64) ([]H, error),
) header.Getter[H] {
	return &getterFunc[H]{Getter: inner, fn: fn}
}

func (g *getterFunc[H]) GetRangeByHeight(ctx context.Context, from H, to uint64) ([]H, error) {
	return g.fn(ctx, from, to)
}
