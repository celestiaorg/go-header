package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/celestiaorg/go-header/headertest"
)

func TestHeightSub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	hs := newHeightSub[*headertest.DummyHeader]()

	// assert subscription returns nil for past heights
	{
		hs.SetHeight(99)

		err := hs.Wait(ctx, 10)
		assert.ErrorIs(t, err, errElapsedHeight)
	}

	// assert actual subscription works
	{
		go func() {
			// fixes flakiness on CI
			time.Sleep(time.Millisecond)

			hs.SetHeight(102)
		}()

		err := hs.Wait(ctx, 101)
		assert.NoError(t, err)
	}

	// assert multiple subscriptions work
	{
		ch := make(chan error, 10)
		for range cap(ch) {
			go func() {
				_, err := hs.Sub(ctx, 103)
				ch <- err
			}()
		}

		time.Sleep(time.Millisecond * 10)

		h3 := headertest.RandDummyHeader(t)
		h3.HeightI = 103
		hs.Pub(h3)

		for range cap(ch) {
			assert.NoError(t, <-ch)
		}
	}
}

// Test heightSub can accept non-adj headers without an error.
func TestHeightSubNonAdjacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	hs := newHeightSub[*headertest.DummyHeader]()

	hs.SetHeight(99)

	go func() {
		// fixes flakiness on CI
		time.Sleep(time.Millisecond)

		hs.SetHeight(300)
	}()

	err := hs.Wait(ctx, 200)
	assert.NoError(t, err)
}

// Test heightSub's height cannot go down but only up.
func TestHeightSub_monotonicHeight(t *testing.T) {
	hs := newHeightSub[*headertest.DummyHeader]()

	hs.SetHeight(99)
	assert.Equal(t, int64(hs.height.Load()), int64(99))

	hs.SetHeight(300)
	assert.Equal(t, int64(hs.height.Load()), int64(300))

	hs.SetHeight(120)
	assert.Equal(t, int64(hs.height.Load()), int64(300))
}

func TestHeightSubCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	h := headertest.RandDummyHeader(t)
	h.HeightI %= 100 // make it a bit lower
	hs := newHeightSub[*headertest.DummyHeader]()

	sub := make(chan struct{})
	go func() {
		// subscribe first time
		hs.Wait(ctx, h.Height())
		sub <- struct{}{}
	}()

	// give a bit time for subscription to settle
	time.Sleep(time.Millisecond * 10)

	// subscribe again but with failed canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	err := hs.Wait(canceledCtx, h.Height())
	assert.ErrorIs(t, err, context.Canceled)

	// update height
	hs.SetHeight(h.Height())

	// ensure we still get our header
	select {
	case <-sub:
	case <-ctx.Done():
		t.Error(ctx.Err())
	}
	// ensure we don't have any active subscriptions
	assert.Len(t, hs.heightSubs, 0)
}
