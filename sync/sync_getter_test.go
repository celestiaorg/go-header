package sync

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
)

func TestSyncGetterHead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fex := &fakeGetter[*headertest.DummyHeader]{}
	sex := &syncGetter[*headertest.DummyHeader]{Getter: fex}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !sex.Lock() {
				return
			}
			defer sex.Unlock()
			h, err := sex.Head(ctx)
			if h != nil || err != errFakeHead {
				t.Fail()
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, fex.hits.Load())
}

var errFakeHead = fmt.Errorf("head")

type fakeGetter[H header.Header[H]] struct {
	hits atomic.Uint32
}

func (f *fakeGetter[H]) Head(ctx context.Context, _ ...header.HeadOption[H]) (h H, err error) {
	f.hits.Add(1)
	select {
	case <-time.After(time.Millisecond * 100):
		err = errFakeHead
	case <-ctx.Done():
		err = ctx.Err()
	}

	return
}

func (f *fakeGetter[H]) Get(
	_ context.Context,
	_ header.Hash,
) (H, error) {
	panic("implement me")
}

func (f *fakeGetter[H]) GetByHeight(_ context.Context, _ uint64) (H, error) {
	panic("implement me")
}

func (f *fakeGetter[H]) GetRangeByHeight(
	_ context.Context,
	_ H,
	_ uint64,
) ([]H, error) {
	panic("implement me")
}
