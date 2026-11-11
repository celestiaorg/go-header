package sync

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	libhead "github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
)

func Test_syncHead(t *testing.T) {
	counter := &headCounter{}
	sh := &syncHead[*headertest.DummyHeader]{head: counter}

	callsN := 1000
	wg := sync.WaitGroup{}
	wg.Add(callsN)
	for i := 0; i < callsN; i++ {
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(rand.IntN(1000)) * time.Microsecond)
			_, _ = sh.Head(context.Background())
		}()
	}

	wg.Wait()

	assert.Less(t, int(counter.cntr.Load()), callsN/100) // <1% of calls should go through
}

func TestSyncHeadTracing(t *testing.T) {
	recorder := recordSyncSpans(t)
	source := &blockingHead{
		started: make(chan trace.SpanContext, 1),
		release: make(chan struct{}),
		result:  &headertest.DummyHeader{},
	}
	sh := &syncHead[*headertest.DummyHeader]{head: source}

	sourceResult := make(chan error, 1)
	go func() {
		_, err := sh.Head(context.Background())
		sourceResult <- err
	}()

	sourceSpanContext := <-source.started
	require.True(t, sourceSpanContext.IsValid(), "source must receive the tracing context")

	waitCtx, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	_, err := sh.Head(waitCtx)
	require.ErrorIs(t, err, context.Canceled)

	close(source.release)
	require.NoError(t, <-sourceResult)

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	sourceSpan := spanWithEvent(t, spans, "requesting-head-from-source")
	waiterSpan := spanWithEvent(t, spans, "waiting-for-ongoing-head-request")
	assert.Equal(t, codes.Ok, sourceSpan.Status().Code)
	assert.Equal(t, codes.Error, waiterSpan.Status().Code)
	assert.Equal(t, context.Canceled.Error(), waiterSpan.Status().Description)
}

func TestSyncHeadTracingSourceError(t *testing.T) {
	recorder := recordSyncSpans(t)
	expectedErr := errors.New("request failed")
	sh := &syncHead[*headertest.DummyHeader]{
		head: headFunc(func(context.Context) (*headertest.DummyHeader, error) {
			return nil, expectedErr
		}),
	}

	_, err := sh.Head(context.Background())
	require.ErrorIs(t, err, expectedErr)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Equal(t, expectedErr.Error(), spans[0].Status().Description)
}

func recordSyncSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousTracer := tracerSync
	tracerSync = provider.Tracer("header/sync")
	t.Cleanup(func() {
		tracerSync = previousTracer
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return recorder
}

func spanWithEvent(
	t *testing.T,
	spans []sdktrace.ReadOnlySpan,
	eventName string,
) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		for _, event := range span.Events() {
			if event.Name == eventName {
				return span
			}
		}
	}
	t.Fatalf("no span contained event %q", eventName)
	return nil
}

func spanByName(
	t *testing.T,
	spans []sdktrace.ReadOnlySpan,
	name string,
) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("no span named %q", name)
	return nil
}

func spanByID(t *testing.T, spans []sdktrace.ReadOnlySpan, id trace.SpanID) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.SpanContext().SpanID() == id {
			return span
		}
	}
	t.Fatalf("no span with ID %s", id)
	return nil
}

type headFunc func(context.Context) (*headertest.DummyHeader, error)

func (f headFunc) Head(
	ctx context.Context,
	_ ...libhead.HeadOption[*headertest.DummyHeader],
) (*headertest.DummyHeader, error) {
	return f(ctx)
}

type blockingHead struct {
	started chan trace.SpanContext
	release chan struct{}
	result  *headertest.DummyHeader
}

func (h *blockingHead) Head(
	ctx context.Context,
	_ ...libhead.HeadOption[*headertest.DummyHeader],
) (*headertest.DummyHeader, error) {
	h.started <- trace.SpanContextFromContext(ctx)
	select {
	case <-h.release:
		return h.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type headCounter struct {
	cntr atomic.Int64
}

func (h *headCounter) Head(
	ctx context.Context,
	h2 ...libhead.HeadOption[*headertest.DummyHeader],
) (*headertest.DummyHeader, error) {
	time.Sleep(time.Millisecond * 1)
	h.cntr.Add(1)
	return &headertest.DummyHeader{}, nil
}
