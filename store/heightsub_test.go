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

	hs := newHeightSub()

	// assert subscription returns nil for past heights
	{
		hs.Init(99)

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
				err := hs.Wait(ctx, 103)
				ch <- err
			}()
		}

		time.Sleep(time.Millisecond * 10)

		hs.SetHeight(103)

		for range cap(ch) {
			assert.NoError(t, <-ch)
		}
	}
}

func TestHeightSub_withWaitCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	hs := newHeightSub()
	hs.Init(10)

	const waiters = 5

	cancelChs := make([]chan error, waiters)
	blockedChs := make([]chan error, waiters)
	for i := range waiters {
		cancelChs[i] = make(chan error, 1)
		blockedChs[i] = make(chan error, 1)

		go func() {
			ctx, cancel := context.WithTimeout(ctx, time.Duration(i+1)*time.Millisecond)
			defer cancel()

			err := hs.Wait(ctx, 100)
			cancelChs[i] <- err
		}()

		go func() {
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			err := hs.Wait(ctx, 100)
			blockedChs[i] <- err
		}()
	}

	for i := range cancelChs {
		err := <-cancelChs[i]
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	}

	for i := range blockedChs {
		select {
		case <-blockedChs[i]:
			t.Error("channel should be blocked")
		default:
		}
	}
}

// Test heightSub can accept non-adj headers without an error.
func TestHeightSubNonAdjacency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	hs := newHeightSub()
	hs.Init(99)

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
	hs := newHeightSub()

	hs.Init(99)
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
	h.HeightI %= 1000 // make it a bit lower
	hs := newHeightSub()

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
