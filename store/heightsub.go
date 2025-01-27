package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// errElapsedHeight is thrown when a requested height was already provided to heightSub.
var errElapsedHeight = errors.New("elapsed height")

// heightSub provides a minimalistic mechanism to wait till header for a height becomes available.
type heightSub struct {
	// height refers to the latest locally available header height
	// that has been fully verified and inserted into the subjective chain
	height       atomic.Uint64
	heightSubsLk sync.Mutex
	heightSubs   map[uint64]*sub
}

type sub struct {
	signal chan struct{}
	count  int
}

// newHeightSub instantiates new heightSub.
func newHeightSub() *heightSub {
	return &heightSub{
		heightSubs: make(map[uint64]*sub),
	}
}

// Init the heightSub with a given height.
// Notifies all awaiting [Wait] calls lower than height.
func (hs *heightSub) Init(height uint64) {
	hs.height.Store(height)

	hs.heightSubsLk.Lock()
	defer hs.heightSubsLk.Unlock()

	for h := range hs.heightSubs {
		if h < height {
			hs.notify(h, true)
		}
	}
}

// Height reports current height.
func (hs *heightSub) Height() uint64 {
	return hs.height.Load()
}

// SetHeight sets the new head height for heightSub.
// Notifies all awaiting [Wait] calls in range from [heightSub.Height] to height.
func (hs *heightSub) SetHeight(height uint64) {
	for {
		curr := hs.height.Load()
		if curr >= height {
			return
		}
		if !hs.height.CompareAndSwap(curr, height) {
			continue
		}

		hs.heightSubsLk.Lock()
		defer hs.heightSubsLk.Unlock() //nolint:gocritic we have a return below

		for ; curr <= height; curr++ {
			hs.notify(curr, true)
		}
		return
	}
}

// Wait for a given height to be published.
// It can return errElapsedHeight, which means a requested height was already seen
// and caller should get it elsewhere.
func (hs *heightSub) Wait(ctx context.Context, height uint64) error {
	if hs.Height() >= height {
		return errElapsedHeight
	}

	hs.heightSubsLk.Lock()
	if hs.Height() >= height {
		// This is a rare case we have to account for.
		// The lock above can park a goroutine long enough for hs.height to change for a requested height,
		// leaving the request never fulfilled and the goroutine deadlocked.
		hs.heightSubsLk.Unlock()
		return errElapsedHeight
	}

	sac, ok := hs.heightSubs[height]
	if !ok {
		sac = &sub{
			signal: make(chan struct{}, 1),
		}
		hs.heightSubs[height] = sac
	}
	sac.count++
	hs.heightSubsLk.Unlock()

	select {
	case <-sac.signal:
		return nil
	case <-ctx.Done():
		// no need to keep the request, if the op has canceled
		hs.heightSubsLk.Lock()
		hs.notify(height, false)
		hs.heightSubsLk.Unlock()
		return ctx.Err()
	}
}

// Notify and release the waiters in [Wait].
// Note: do not advance heightSub's height.
func (hs *heightSub) Notify(height uint64) {
	hs.heightSubsLk.Lock()
	defer hs.heightSubsLk.Unlock()

	hs.notify(height, true)
}

func (hs *heightSub) notify(height uint64, all bool) {
	sac, ok := hs.heightSubs[height]
	if !ok {
		return
	}

	sac.count--
	if all || sac.count == 0 {
		close(sac.signal)
		delete(hs.heightSubs, height)
	}
}
