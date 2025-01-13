package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/celestiaorg/go-header"
)

// errElapsedHeight is thrown when a requested height was already provided to heightSub.
var errElapsedHeight = errors.New("elapsed height")

// heightSub provides a minimalistic mechanism to wait till header for a height becomes available.
type heightSub[H header.Header[H]] struct {
	// height refers to the latest locally available header height
	// that has been fully verified and inserted into the subjective chain
	height       atomic.Uint64
	heightReqsLk sync.Mutex
	heightSubs   map[uint64]chan struct{}
}

// newHeightSub instantiates new heightSub.
func newHeightSub[H header.Header[H]]() *heightSub[H] {
	return &heightSub[H]{
		heightSubs: make(map[uint64]chan struct{}),
	}
}

// Height reports current height.
func (hs *heightSub[H]) Height() uint64 {
	return hs.height.Load()
}

// SetHeight sets the new head height for heightSub.
// Unblocks all awaiting [Wait] calls in range from [heightSub.Height] to height.
func (hs *heightSub[H]) SetHeight(height uint64) {
	for {
		curr := hs.height.Load()
		if curr >= height {
			return
		}
		if hs.height.CompareAndSwap(curr, height) {
			hs.heightReqsLk.Lock()
			for ; curr <= height; curr++ {
				sub, ok := hs.heightSubs[curr]
				if ok {
					close(sub)
					delete(hs.heightSubs, curr)
				}
			}
			hs.heightReqsLk.Unlock()
			return
		}
	}
}

func (hs *heightSub[H]) Wait(ctx context.Context, height uint64) error {
	if hs.Height() >= height {
		return errElapsedHeight
	}

	hs.heightReqsLk.Lock()
	if hs.Height() >= height {
		// This is a rare case we have to account for.
		// The lock above can park a goroutine long enough for hs.height to change for a requested height,
		// leaving the request never fulfilled and the goroutine deadlocked.
		hs.heightReqsLk.Unlock()
		return errElapsedHeight
	}

	sub, ok := hs.heightSubs[height]
	if !ok {
		sub = make(chan struct{}, 1)
		hs.heightSubs[height] = sub
	}
	hs.heightReqsLk.Unlock()

	select {
	case <-sub:
		return nil
	case <-ctx.Done():
		// no need to keep the request, if the op has canceled
		hs.heightReqsLk.Lock()
		close(sub)
		delete(hs.heightSubs, height)
		hs.heightReqsLk.Unlock()
		return ctx.Err()
	}
}
