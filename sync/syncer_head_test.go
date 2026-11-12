package sync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/local"
)

func TestSyncer_HeadConcurrencyError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	store := headertest.NewStore[*headertest.DummyHeader](t, suite, 1)

	syncer, err := NewSyncer[*headertest.DummyHeader](
		errorGetter{},
		store,
		headertest.NewDummySubscriber(),
		WithRecencyThreshold(time.Nanosecond), // force recent requests
	)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_, err := syncer.Head(ctx)
			require.NoError(t, err)
		}()
	}

	wg.Wait()
}

func TestSyncer_HeadTracing(t *testing.T) {
	recorder := recordSyncSpans(t)
	ctx := context.Background()
	suite := headertest.NewTestSuite(t)
	store := headertest.NewStore[*headertest.DummyHeader](t, suite, 1)
	remoteStore := headertest.NewStore[*headertest.DummyHeader](t, suite, 0)
	require.NoError(t, remoteStore.Append(ctx, suite.Head(), suite.NextHeader()))
	syncer, err := NewSyncer[*headertest.DummyHeader](
		remoteStore,
		store,
		headertest.NewDummySubscriber(),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)

	head, err := syncer.Head(ctx)
	require.NoError(t, err)
	require.False(t, head.IsZero())

	spans := recorder.Ended()
	headSpan := spanWithEvent(t, spans, "head-found")
	assert.Equal(t, "head", headSpan.Name())
	assert.Equal(t, codes.Ok, headSpan.Status().Code)

	networkSpan := spanByName(t, spans, "network-head")
	requestSpan := spanByName(t, spans, "request-head")
	assert.Equal(t, networkSpan.SpanContext().SpanID(), requestSpan.Parent().SpanID())
	for _, name := range []string{"network-head", "subjective-tail", "incoming-network-head", "local-head"} {
		phaseSpan := spanByName(t, spans, name)
		assert.Equal(t, headSpan.SpanContext().SpanID(), phaseSpan.Parent().SpanID(), name)
	}
	assert.Equal(t, codes.Ok, networkSpan.Status().Code)
	assert.Equal(t, codes.Ok, spanByName(t, spans, "subjective-tail").Status().Code)
	assert.Equal(t, codes.Error, spanByName(t, spans, "incoming-network-head").Status().Code)
	assert.Equal(t, codes.Ok, spanByName(t, spans, "local-head").Status().Code)
}

func TestSyncer_HeadTracingTailError(t *testing.T) {
	recorder := recordSyncSpans(t)
	ctx := context.Background()
	suite := headertest.NewTestSuite(t)
	store := headertest.NewStore[*headertest.DummyHeader](t, suite, 1)
	remoteStore := headertest.NewStore[*headertest.DummyHeader](t, suite, 0)
	require.NoError(t, remoteStore.Append(ctx, suite.Head(), suite.NextHeader()))
	expectedErr := errors.New("tail unavailable")
	syncer, err := NewSyncer[*headertest.DummyHeader](
		remoteStore,
		&tailErrorStore{Store: store, err: expectedErr},
		headertest.NewDummySubscriber(),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)

	_, err = syncer.Head(ctx)
	require.ErrorIs(t, err, expectedErr)

	spans := recorder.Ended()
	headSpan := spanByName(t, spans, "head")
	tailSpan := spanByName(t, spans, "subjective-tail")
	assert.Equal(t, headSpan.SpanContext().SpanID(), tailSpan.Parent().SpanID())
	assert.Equal(t, codes.Error, tailSpan.Status().Code)
	assert.Equal(t, expectedErr.Error(), tailSpan.Status().Description)
	assert.Equal(t, codes.Error, headSpan.Status().Code)
	assert.NotEqual(
		t, expectedErr.Error(), headSpan.Status().Description, "root error keeps stage context",
	)
	assert.Contains(t, headSpan.Status().Description, "subjective tail")
	assert.Nil(t, findSpanByName(spans, "incoming-network-head"))
	assert.Nil(t, findSpanByName(spans, "local-head"))
}

func findSpanByName(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	return nil
}

type tailErrorStore struct {
	header.Store[*headertest.DummyHeader]
	err error
}

func (s *tailErrorStore) Tail(context.Context) (*headertest.DummyHeader, error) {
	return nil, s.err
}

func TestSyncer_incomingNetworkHeadRaces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	store := headertest.NewStore[*headertest.DummyHeader](t, suite, 1)
	syncer, err := NewSyncer[*headertest.DummyHeader](
		store,
		store,
		headertest.NewDummySubscriber(),
	)
	require.NoError(t, err)

	incoming := suite.NextHeader()

	var hits atomic.Uint32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if syncer.incomingNetworkHead(ctx, incoming) == nil {
				hits.Add(1)
			}
		}()
	}

	wg.Wait()
	assert.EqualValues(t, 1, hits.Load())
}

func TestSyncer_NetworkHeadsDuringBlockedAppend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	hdrs := headertest.NewTestSuite(t).GenDummyHeaders(13)
	gate := make(chan struct{})
	fs := &fakeStore{
		head:          hdrs[9],
		appendGates:   map[uint64]chan struct{}{11: gate},
		appendEntered: make(chan uint64, 1),
	}
	syncer, err := NewSyncer(fs, fs, headertest.NewDummySubscriber())
	require.NoError(t, err)
	written := make(chan error, 1)
	go func() { written <- syncer.store.Append(ctx, hdrs[10]) }()
	receive(t, ctx, fs.appendEntered)

	headCtx, headCancel := context.WithTimeout(ctx, time.Second)
	defer headCancel()
	for _, h := range hdrs[11:] {
		require.NoError(t, syncer.incomingNetworkHead(headCtx, h))
		require.NoError(t, headCtx.Err(), "validation waited for a blocked store write")
		require.Equal(t, h, syncer.pending.Head(), "candidate must remain available for syncing")
	}
	select {
	case err := <-written:
		t.Fatal("store write completed before it was released:", err)
	default:
	}

	close(gate)
	require.NoError(t, receive(t, ctx, written))
	assert.EqualValues(t, 11, cachedHeight(&syncer.store))
	assert.Equal(t, hdrs[12], syncer.pending.Head())
}

// TestSyncer_HeadWithTrustedHead tests whether the syncer
// requests Head (new sync target) from tracked peers when
// it already has a subjective head within the unbonding period.
func TestSyncer_HeadWithTrustedHead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	localStore := newTestStore(t, ctx, head)
	remoteStore := newTestStore(t, ctx, head)

	err := remoteStore.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)

	// create a wrappedGetter to track exchange interactions
	wrappedGetter := newWrappedGetter(local.NewExchange[*headertest.DummyHeader](remoteStore))

	syncer, err := NewSyncer[*headertest.DummyHeader](
		wrappedGetter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond), // forces a request for a new sync target
		// ensures that syncer's store contains a subjective head that is within
		// the unbonding period so that the syncer can use a header from the network
		// as a sync target
		WithTrustingPeriod(time.Hour),
	)
	require.NoError(t, err)

	// start the syncer which triggers a Head request that will
	// load the syncer's subjective head from the store, and request
	// a new sync target from the network rather than from trusted peers
	err = syncer.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err = syncer.Stop(ctx)
		require.NoError(t, err)
	})

	// ensure the syncer really requested Head from the network
	// rather than from trusted peers
	require.True(t, wrappedGetter.withTrustedHead)
}

// Test will simulate a case with upto `iters` failures before we will get to
// the header that can be verified against localHead.
func TestSyncer_verifyBifurcatingSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	localStore := newTestStore(t, ctx, head)
	remoteStore := newTestStore(t, ctx, head)

	// create a wrappedGetter to track exchange interactions
	wrappedGetter := newWrappedGetter(local.NewExchange(remoteStore))

	syncer, err := NewSyncer(
		wrappedGetter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond), // forces a request for a new sync target
		// ensures that syncer's store contains a subjective head that is within
		// the unbonding period so that the syncer can use a header from the network
		// as a sync target
		WithTrustingPeriod(time.Hour),
	)
	require.NoError(t, err)

	// start the syncer which triggers a Head request that will
	// load the syncer's subjective head from the store, and request
	// a new sync target from the network rather than from trusted peers
	err = syncer.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err = syncer.Stop(ctx)
		require.NoError(t, err)
	})

	// when
	const total = 1000
	const badHeaderHeight = total + 1 // make the last header bad
	const iters = 4

	headers := suite.GenDummyHeaders(total)
	err = remoteStore.Append(ctx, headers...)
	require.NoError(t, err)

	// configure header verification method is such way
	// that the first [iters] verification will fail
	// but all other will be ok.
	var verifyCounter atomic.Int32
	for i := range total {
		headers[i].VerifyFn = func(hdr *headertest.DummyHeader) error {
			if hdr.Height() != badHeaderHeight {
				return nil
			}

			verifyCounter.Add(1)
			if verifyCounter.Load() >= iters {
				return nil
			}

			return &header.VerifyError{
				Reason:      headertest.ErrDummyVerify,
				SoftFailure: hdr.SoftFailure,
			}
		}
	}

	headers[total-1].VerifyFailure = true
	headers[total-1].SoftFailure = true

	subjHead, err := syncer.localHead(ctx)
	require.NoError(t, err)

	err = syncer.verifyBifurcating(ctx, subjHead, headers[total-1])
	require.NoError(t, err)
}

// Test will simulate a case with upto `iters` failures before we will get to
// the header that can be verified against localHead.
func TestSyncer_verifyBifurcatingSuccessWithBadCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	localStore := newTestStore(t, ctx, head)
	remoteStore := newTestStore(t, ctx, head)

	// create a wrappedGetter to track exchange interactions
	wrappedGetter := newWrappedGetter(local.NewExchange(remoteStore))

	syncer, err := NewSyncer(
		wrappedGetter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond), // forces a request for a new sync target
		// ensures that syncer's store contains a subjective head that is within
		// the unbonding period so that the syncer can use a header from the network
		// as a sync target
		WithTrustingPeriod(time.Hour),
	)
	require.NoError(t, err)

	// start the syncer which triggers a Head request that will
	// load the syncer's subjective head from the store, and request
	// a new sync target from the network rather than from trusted peers
	err = syncer.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err = syncer.Stop(ctx)
		require.NoError(t, err)
	})

	const total = 1000
	const iters = 4

	headers := suite.GenDummyHeaders(total)
	err = remoteStore.Append(ctx, headers...)
	require.NoError(t, err)

	// configure header verification method is such way
	// that the first [iters] verification will fail
	// but all other will be ok.
	var verifyCounter atomic.Int32
	for i := range total {
		headers[i].VerifyFn = func(hdr *headertest.DummyHeader) error {
			if i >= 501 {
				return nil
			}

			verifyCounter.Add(1)
			if verifyCounter.Load() > iters {
				return nil
			}
			return &header.VerifyError{
				Reason:      headertest.ErrDummyVerify,
				SoftFailure: hdr.SoftFailure,
			}
		}
	}

	headers[total-1].VerifyFailure = true
	headers[total-1].SoftFailure = true

	subjHead, err := syncer.localHead(ctx)
	require.NoError(t, err)

	err = syncer.verifyBifurcating(ctx, subjHead, headers[total-1])
	require.NoError(t, err)
}

// Test will simulate a case when no headers can be verified against localHead.
// As a result the [NewValidatorSetCantBeTrustedError] error will be returned.
func TestSyncer_verifyBifurcatingCannotVerify(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	localStore := newTestStore(t, ctx, head)
	remoteStore := newTestStore(t, ctx, head)

	// create a wrappedGetter to track exchange interactions
	wrappedGetter := newWrappedGetter(local.NewExchange(remoteStore))

	syncer, err := NewSyncer(
		wrappedGetter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond), // forces a request for a new sync target
		// ensures that syncer's store contains a subjective head that is within
		// the unbonding period so that the syncer can use a header from the network
		// as a sync target
		WithTrustingPeriod(time.Hour),
	)
	require.NoError(t, err)

	// start the syncer which triggers a Head request that will
	// load the syncer's subjective head from the store, and request
	// a new sync target from the network rather than from trusted peers
	err = syncer.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err = syncer.Stop(ctx)
		require.NoError(t, err)
	})

	const total = 1000
	const badHeaderHeight = total + 1

	headers := suite.GenDummyHeaders(total)
	err = remoteStore.Append(ctx, headers...)
	require.NoError(t, err)

	for i := range total {
		headers[i].VerifyFn = func(hdr *headertest.DummyHeader) error {
			if hdr.Height() != badHeaderHeight {
				return nil
			}

			return &header.VerifyError{
				Reason:      headertest.ErrDummyVerify,
				SoftFailure: hdr.SoftFailure,
			}
		}
	}

	headers[total-1].VerifyFailure = true
	headers[total-1].SoftFailure = true

	subjHead, err := syncer.localHead(ctx)
	require.NoError(t, err)

	err = syncer.verifyBifurcating(ctx, subjHead, headers[total-1])
	assert.Error(t, err)
}

type wrappedGetter struct {
	ex header.Exchange[*headertest.DummyHeader]

	// withTrustedHead indicates whether TrustedHead was set by the request
	// via the WithTrustedHead opt.
	withTrustedHead bool
}

func newWrappedGetter(ex header.Exchange[*headertest.DummyHeader]) *wrappedGetter {
	return &wrappedGetter{
		ex:              ex,
		withTrustedHead: false,
	}
}

func (t *wrappedGetter) Head(
	ctx context.Context,
	options ...header.HeadOption[*headertest.DummyHeader],
) (*headertest.DummyHeader, error) {
	params := header.HeadParams[*headertest.DummyHeader]{}
	for _, opt := range options {
		opt(&params)
	}
	if params.TrustedHead != nil {
		t.withTrustedHead = true
	}
	return t.ex.Head(ctx, options...)
}

func (t *wrappedGetter) Get(
	ctx context.Context,
	hash header.Hash,
) (*headertest.DummyHeader, error) {
	panic("implement me")
}

func (t *wrappedGetter) GetByHeight(
	ctx context.Context,
	u uint64,
) (*headertest.DummyHeader, error) {
	return t.ex.GetByHeight(ctx, u)
}

func (t *wrappedGetter) GetRangeByHeight(
	ctx context.Context,
	from *headertest.DummyHeader,
	to uint64,
) ([]*headertest.DummyHeader, error) {
	return t.ex.GetRangeByHeight(ctx, from, to)
}

type errorGetter struct{}

func (e errorGetter) Head(
	context.Context,
	...header.HeadOption[*headertest.DummyHeader],
) (*headertest.DummyHeader, error) {
	time.Sleep(time.Millisecond * 1)
	return nil, fmt.Errorf("error")
}

func (e errorGetter) Get(ctx context.Context, hash header.Hash) (*headertest.DummyHeader, error) {
	panic("implement me")
}

func (e errorGetter) GetByHeight(ctx context.Context, u uint64) (*headertest.DummyHeader, error) {
	panic("implement me")
}

func (e errorGetter) GetRangeByHeight(
	context.Context,
	*headertest.DummyHeader,
	uint64,
) ([]*headertest.DummyHeader, error) {
	panic("implement me")
}

func TestSyncer_SetLocalHeadPrunesStaleTarget(t *testing.T) {
	ctx := context.Background()
	suite := headertest.NewTestSuite(t)
	store := headertest.NewStore[*headertest.DummyHeader](t, suite, 3)
	syncer, err := NewSyncer[*headertest.DummyHeader](store, store, nil)
	require.NoError(t, err)

	syncer.pending.Add(suite.Head())
	next := suite.NextHeader()
	syncer.setLocalHead(ctx, next)

	assert.True(t, syncer.pending.Head().IsZero())
	head, err := syncer.localHead(ctx)
	require.NoError(t, err)
	assert.Equal(t, next, head)
}
