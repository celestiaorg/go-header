package sync

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/celestiaorg/go-header"
)

// syncGetter is a Getter wrapper that ensure only one Head call happens at the time
type syncGetter[H header.Header] struct {
	getterLk   sync.Mutex
	getterCond *sync.Cond
	isGetterLk atomic.Bool
	header.Getter[H]
}

func newSyncGetter[H header.Header](g header.Getter[H]) *syncGetter[H] {
	getter := syncGetter[H]{Getter: g}
	getter.getterCond = sync.NewCond(&getter.getterLk)
	return &getter
}

// Lock locks the getter for single user.
// Reports 'true' if the lock was held by the current routine.
// Does not require unlocking on 'false'.
func (se *syncGetter[H]) Lock() bool {
	// the lock construction here ensures only one routine is freed at a time
	// while others wait via Rlock
	locked := se.isGetterLk.CompareAndSwap(false, true)
	if !locked {
		se.await()
	}
	return locked
}

// Unlock unlocks the getter.
func (se *syncGetter[H]) Unlock() {
	se.checkLock("Unlock without preceding Lock on syncGetter")
	se.isGetterLk.Store(false)
	se.getterCond.Broadcast()
}

// Head must be called with held Lock.
func (se *syncGetter[H]) Head(ctx context.Context) (H, error) {
	se.checkLock("Head without preceding Lock on syncGetter")
	return se.Getter.Head(ctx)
}

// checkLock ensures api safety
func (se *syncGetter[H]) checkLock(msg string) {
	if !se.isGetterLk.Load() {
		panic(msg)
	}
}

func (se *syncGetter[H]) await() {
	se.getterLk.Lock()
	se.getterCond.Wait()
	se.getterLk.Unlock()
}
