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
