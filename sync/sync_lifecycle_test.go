package sync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/local"
)

func TestSyncer_StopWaitsForSyncLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	localStore := newTestStore(t, ctx, head)
	remoteStore := headertest.NewStore(t, suite, 100)
	getter := &blockingGetter[*headertest.DummyHeader]{
		Getter:   local.NewExchange(remoteStore),
		entered:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	t.Cleanup(func() { closeOnce(getter.release) })

	syncer, err := NewSyncer(
		getter,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)
	require.NoError(t, syncer.Start(ctx))

	syncer.wantSync()
	waitForSignal(t, ctx, getter.entered, "sync loop did not request headers")

	stopReturned := make(chan error, 1)
	go func() { stopReturned <- syncer.Stop(ctx) }()

	waitForSignal(t, ctx, getter.canceled, "Stop did not cancel the active sync request")
	select {
	case err := <-stopReturned:
		t.Fatalf("Stop returned before the sync loop exited: %v", err)
	default:
	}

	close(getter.release)
	require.NoError(t, waitForResult(t, ctx, stopReturned, "Stop did not return after the sync loop exited"))
}

func TestSyncer_LifecycleOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	syncer := newLifecycleTestSyncer(t, ctx)

	// Stop is idempotent before the first Start and after a completed Stop.
	require.NoError(t, syncer.Stop(ctx))
	require.NoError(t, syncer.Start(ctx))
	require.ErrorIs(t, syncer.Start(ctx), ErrSyncerRunning)
	require.NoError(t, syncer.Stop(ctx))
	require.NoError(t, syncer.Stop(ctx))

	// Every restart creates and completes a distinct run.
	require.NoError(t, syncer.Start(ctx))
	require.NoError(t, syncer.Stop(ctx))
}

func TestSyncer_ConcurrentStarts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	syncer := newLifecycleTestSyncer(t, ctx)
	const callers = 16
	results := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			results <- syncer.Start(ctx)
		}()
	}
	wg.Wait()
	close(results)

	var started int
	for err := range results {
		switch {
		case err == nil:
			started++
		case errors.Is(err, ErrSyncerStarting), errors.Is(err, ErrSyncerRunning):
		default:
			t.Fatalf("unexpected Start result: %v", err)
		}
	}
	require.Equal(t, 1, started)
	require.NoError(t, syncer.Stop(ctx))
}

func TestSyncer_ConcurrentStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	syncer := newLifecycleTestSyncer(t, ctx)
	require.NoError(t, syncer.Start(ctx))

	const callers = 16
	results := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			results <- syncer.Stop(ctx)
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
}

func TestSyncer_StopDuringStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	subscriber := &blockingSubscriber[*headertest.DummyHeader]{
		Subscriber: headertest.NewDummySubscriber(),
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	t.Cleanup(func() { closeOnce(subscriber.release) })
	syncer, err := NewSyncer(
		local.NewExchange(headertest.NewStore(t, suite, 10)),
		newTestStore(t, ctx, head),
		subscriber,
		WithBlockTime(time.Hour),
	)
	require.NoError(t, err)

	startReturned := make(chan error, 1)
	go func() { startReturned <- syncer.Start(ctx) }()
	waitForSignal(t, ctx, subscriber.entered, "Start did not begin verifier installation")
	require.ErrorIs(t, syncer.Start(ctx), ErrSyncerStarting)

	stopReturned := make(chan error, 1)
	go func() { stopReturned <- syncer.Stop(ctx) }()
	require.Eventually(t, func() bool {
		syncer.lifecycleMu.Lock()
		defer syncer.lifecycleMu.Unlock()
		return syncer.lifecycleState == stateStopping
	}, time.Second, time.Millisecond)
	require.ErrorIs(t, syncer.Start(ctx), ErrSyncerStopping)

	close(subscriber.release)
	require.ErrorIs(t, waitForResult(t, ctx, startReturned, "Start did not return"), context.Canceled)
	require.NoError(t, waitForResult(t, ctx, stopReturned, "Stop did not return"))
}

func TestSyncer_StartContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	subscriber := &blockingSubscriber[*headertest.DummyHeader]{
		Subscriber: headertest.NewDummySubscriber(),
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	t.Cleanup(func() { closeOnce(subscriber.release) })
	syncer, err := NewSyncer(
		local.NewExchange(headertest.NewStore(t, suite, 10)),
		newTestStore(t, ctx, head),
		subscriber,
		WithBlockTime(time.Hour),
	)
	require.NoError(t, err)

	startCtx, startCancel := context.WithCancel(ctx)
	startReturned := make(chan error, 1)
	go func() { startReturned <- syncer.Start(startCtx) }()
	waitForSignal(t, ctx, subscriber.entered, "Start did not begin verifier installation")

	startCancel()
	close(subscriber.release)
	require.ErrorIs(t, waitForResult(t, ctx, startReturned, "canceled Start did not return"), context.Canceled)

	syncer.lifecycleMu.Lock()
	require.Equal(t, stateStopped, syncer.lifecycleState)
	require.Nil(t, syncer.run)
	syncer.lifecycleMu.Unlock()

	require.NoError(t, syncer.Start(ctx))
	require.NoError(t, syncer.Stop(ctx))
}

func TestSyncer_MetricsRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	syncer, err := NewSyncer(
		local.NewExchange(headertest.NewStore(t, suite, 10)),
		newTestStore(t, ctx, head),
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Hour),
		WithMetrics(),
	)
	require.NoError(t, err)

	for range 2 {
		require.NoError(t, syncer.Start(ctx))
		require.NoError(t, syncer.Stop(ctx))
	}
}

func TestSyncer_StopTimeoutCanBeRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	getter := &blockingGetter[*headertest.DummyHeader]{
		Getter:   local.NewExchange(headertest.NewStore(t, suite, 100)),
		entered:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	t.Cleanup(func() { closeOnce(getter.release) })
	syncer, err := NewSyncer(
		getter,
		newTestStore(t, ctx, head),
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)
	require.NoError(t, syncer.Start(ctx))
	syncer.wantSync()
	waitForSignal(t, ctx, getter.entered, "sync loop did not request headers")

	stopCtx, stopCancel := context.WithCancel(context.Background())
	stopCancel()
	require.ErrorIs(t, syncer.Stop(stopCtx), context.Canceled)
	waitForSignal(t, ctx, getter.canceled, "Stop did not cancel the active sync request")
	require.ErrorIs(t, syncer.Start(ctx), ErrSyncerStopping)

	close(getter.release)
	require.NoError(t, syncer.Stop(ctx))
	require.NoError(t, syncer.Start(ctx))
	require.NoError(t, syncer.Stop(ctx))
}

func TestSyncer_StartWhileStopping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	getter := &blockingGetter[*headertest.DummyHeader]{
		Getter:   local.NewExchange(headertest.NewStore(t, suite, 100)),
		entered:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	t.Cleanup(func() { closeOnce(getter.release) })
	syncer, err := NewSyncer(
		getter,
		newTestStore(t, ctx, head),
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)
	require.NoError(t, syncer.Start(ctx))
	syncer.wantSync()
	waitForSignal(t, ctx, getter.entered, "sync loop did not request headers")

	stopReturned := make(chan error, 1)
	go func() { stopReturned <- syncer.Stop(ctx) }()
	waitForSignal(t, ctx, getter.canceled, "Stop did not cancel the active sync request")
	require.ErrorIs(t, syncer.Start(ctx), ErrSyncerStopping)

	close(getter.release)
	require.NoError(t, waitForResult(t, ctx, stopReturned, "Stop did not finish"))
	require.NoError(t, syncer.Start(ctx))
	require.NoError(t, syncer.Stop(ctx))
}

func newLifecycleTestSyncer(
	t *testing.T,
	ctx context.Context,
) *Syncer[*headertest.DummyHeader] {
	t.Helper()
	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	remoteStore := headertest.NewStore(t, suite, 10)
	syncer, err := NewSyncer(
		local.NewExchange(remoteStore),
		newTestStore(t, ctx, head),
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Hour),
	)
	require.NoError(t, err)
	return syncer
}

type blockingGetter[H header.Header[H]] struct {
	header.Getter[H]
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

type blockingSubscriber[H header.Header[H]] struct {
	header.Subscriber[H]
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSubscriber[H]) SetVerifier(verifier func(context.Context, H) error) error {
	close(s.entered)
	<-s.release
	return s.Subscriber.SetVerifier(verifier)
}

func (g *blockingGetter[H]) GetRangeByHeight(
	ctx context.Context,
	from H,
	to uint64,
) ([]H, error) {
	close(g.entered)
	<-ctx.Done()
	close(g.canceled)
	<-g.release
	return nil, ctx.Err()
}

func waitForSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal(message)
	}
}

func waitForResult(t *testing.T, ctx context.Context, result <-chan error, message string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatal(message)
		return nil
	}
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}
