package sync

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/headertest"
)

func TestSyncer_StalePendingHeadConcurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	headers := make([]*headReadBarrierHeader, 1, 6)
	headers[0] = &headReadBarrierHeader{DummyHeader: suite.Head()}
	for _, h := range suite.GenDummyHeaders(5) {
		headers = append(headers, &headReadBarrierHeader{DummyHeader: h})
	}
	localStore := &headertest.Store[*headReadBarrierHeader]{
		Headers: make(map[uint64]*headReadBarrierHeader),
	}
	remoteStore := &headertest.Store[*headReadBarrierHeader]{
		Headers: make(map[uint64]*headReadBarrierHeader),
	}
	require.NoError(t, localStore.Append(ctx, headers[0]))
	require.NoError(t, remoteStore.Append(ctx, headers...))
	syncer, err := NewSyncer[*headReadBarrierHeader](remoteStore, localStore, nil)
	require.NoError(t, err)

	syncer.setLocalHead(ctx, headers[2])
	select {
	case <-syncer.triggerSync:
	default:
		t.Fatal("initial target did not trigger a sync")
	}

	observed := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("racing setLocalHead did not finish")
		}
	})
	racingHead := &headReadBarrierHeader{
		DummyHeader: headers[2].DummyHeader,
		observed:    observed,
		resume:      resume,
	}
	go func() {
		defer close(done)
		syncer.setLocalHead(ctx, racingHead)
	}()

	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("setLocalHead did not reach the store-head comparison")
	}
	// The racing call has read store head 1, but has not added its target yet.
	syncer.sync(ctx)
	require.EqualValues(t, 3, localStore.HeadHeight)
	require.True(t, syncer.pending.Head().IsZero())
	syncer.setLocalHead(ctx, headers[3])
	require.EqualValues(t, 4, localStore.HeadHeight)

	release()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("racing setLocalHead did not finish")
	}
	// Its stale comparison now adds height 3 even though the store is at 4.
	pending := syncer.pending.Head()
	require.False(t, pending.IsZero())
	require.EqualValues(t, 3, pending.Height())
	head, err := syncer.localHead(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 4, head.Height())
	select {
	case <-syncer.triggerSync:
	default:
		t.Fatal("late pending target did not trigger another sync")
	}
	syncer.sync(ctx)
	require.True(t, syncer.pending.Head().IsZero())

	// Catching up to a later target still works after the race.
	syncer.setLocalHead(ctx, headers[5])
	syncer.sync(ctx)
	require.EqualValues(t, 6, localStore.HeadHeight)
	require.True(t, syncer.pending.Head().IsZero())
}

// Time arms the barrier during setLocalHead's metrics call, after Append.
// The next Height call pauses the comparison against the already-read store head.
type headReadBarrierHeader struct {
	*headertest.DummyHeader
	observed chan struct{}
	resume   <-chan struct{}
	armed    atomic.Bool
	once     sync.Once
}

func (h *headReadBarrierHeader) New() *headReadBarrierHeader {
	return &headReadBarrierHeader{DummyHeader: new(headertest.DummyHeader)}
}

func (h *headReadBarrierHeader) IsZero() bool { return h == nil || h.DummyHeader.IsZero() }

func (h *headReadBarrierHeader) Verify(other *headReadBarrierHeader) error {
	return h.DummyHeader.Verify(other.DummyHeader)
}

func (h *headReadBarrierHeader) Time() time.Time {
	if h.observed != nil {
		h.armed.Store(true)
	}
	return h.DummyHeader.Time()
}

func (h *headReadBarrierHeader) Height() uint64 {
	if h.armed.CompareAndSwap(true, false) {
		h.once.Do(func() {
			close(h.observed)
			<-h.resume
		})
	}
	return h.DummyHeader.Height()
}
