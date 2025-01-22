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
	heightSubsLk sync.Mutex
	heightSubs   map[uint64]*signalAndCounter
}

type signalAndCounter struct {
	signal chan struct{}
	count  int
}

// newHeightSub instantiates new heightSub.
func newHeightSub[H header.Header[H]]() *heightSub[H] {
	return &heightSub[H]{
		heightSubs: make(map[uint64]*signalAndCounter),
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
		if !hs.height.CompareAndSwap(curr, height) {
			continue
		}

		hs.heightSubsLk.Lock()
		defer hs.heightSubsLk.Unlock() //nolint:gocritic we have a return below

		for ; curr <= height; curr++ {
			hs.unblockHeight(curr, true)
		}
		return
	}
}

// Wait for a given height to be published.
// It can return errElapsedHeight, which means a requested height was already seen
// and caller should get it elsewhere.
func (hs *heightSub[H]) Wait(ctx context.Context, height uint64) error {
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
		sac = &signalAndCounter{
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
		hs.unblockHeight(height, false)
		hs.heightSubsLk.Unlock()
		return ctx.Err()
	}
}

// UnblockHeight and release the waiters in [Wait].
// Note: do not advance heightSub's height.
func (hs *heightSub[H]) UnblockHeight(height uint64) {
	hs.heightSubsLk.Lock()
	defer hs.heightSubsLk.Unlock()

	hs.unblockHeight(height, true)
}

func (hs *heightSub[H]) unblockHeight(height uint64, all bool) {
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
