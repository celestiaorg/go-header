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
		h := headertest.RandDummyHeader(t)
		h.HeightI = 100
		hs.SetHeight(99)
		hs.Pub(h)

		h, err := hs.Sub(ctx, 10)
		assert.ErrorIs(t, err, errElapsedHeight)
		assert.Nil(t, h)
	}

	// assert actual subscription works
	{
		go func() {
			// fixes flakiness on CI
			time.Sleep(time.Millisecond)

			h1 := headertest.RandDummyHeader(t)
			h1.HeightI = 101
			h2 := headertest.RandDummyHeader(t)
			h2.HeightI = 102
			hs.Pub(h1, h2)
		}()

		h, err := hs.Sub(ctx, 101)
		assert.NoError(t, err)
		assert.NotNil(t, h)
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

	{
		h := headertest.RandDummyHeader(t)
		h.HeightI = 100
		hs.SetHeight(99)
		hs.Pub(h)
	}

	{
		go func() {
			// fixes flakiness on CI
			time.Sleep(time.Millisecond)

			h1 := headertest.RandDummyHeader(t)
			h1.HeightI = 200
			h2 := headertest.RandDummyHeader(t)
			h2.HeightI = 300
			hs.Pub(h1, h2)
		}()

		h, err := hs.Sub(ctx, 200)
		assert.NoError(t, err)
		assert.NotNil(t, h)
	}
}

// Test heightSub's height cannot go down but only up.
func TestHeightSub_monotonicHeight(t *testing.T) {
	hs := newHeightSub[*headertest.DummyHeader]()

	{
		h := headertest.RandDummyHeader(t)
		h.HeightI = 100
		hs.SetHeight(99)
		hs.Pub(h)
	}

	{
		h1 := headertest.RandDummyHeader(t)
		h1.HeightI = 200
		h2 := headertest.RandDummyHeader(t)
		h2.HeightI = 300
		hs.Pub(h1, h2)
	}

	{
		h1 := headertest.RandDummyHeader(t)
		h1.HeightI = 120
		h2 := headertest.RandDummyHeader(t)
		h2.HeightI = 130
		hs.Pub(h1, h2)
	}

	assert.Equal(t, hs.height.Load(), uint64(300))
}

func TestHeightSubCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	h := headertest.RandDummyHeader(t)
	hs := newHeightSub[*headertest.DummyHeader]()

	sub := make(chan *headertest.DummyHeader)
	go func() {
		// subscribe first time
		h, _ := hs.Sub(ctx, h.HeightI)
		sub <- h
	}()

	// give a bit time for subscription to settle
	time.Sleep(time.Millisecond * 10)

	// subscribe again but with failed canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := hs.Sub(canceledCtx, h.HeightI)
	assert.Error(t, err)

	// publish header
	hs.Pub(h)

	// ensure we still get our header
	select {
	case subH := <-sub:
		assert.Equal(t, h.HeightI, subH.HeightI)
	case <-ctx.Done():
		t.Error(ctx.Err())
	}
	// ensure we don't have any active subscriptions
	assert.Len(t, hs.heightReqs, 0)
}
