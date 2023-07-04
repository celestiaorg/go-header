package sync

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/celestiaorg/go-header"
)

// syncGetter is a Getter wrapper that ensure only one Head call happens at the time
type syncGetter[H header.Header] struct {
	getterLk   sync.RWMutex
	isGetterLk atomic.Bool
	header.Getter[H]
}

// Lock locks the getter for single user.
// Reports 'true' if the lock was held by the current routine.
// Does not require unlocking on 'false'.
func (se *syncGetter[H]) Lock() bool {
	// the lock construction here ensures only one routine is freed at a time
	// while others wait via Rlock
	locked := se.getterLk.TryLock()
	if !locked {
		se.getterLk.RLock()
		defer se.getterLk.RUnlock()
		return false
	}
	se.isGetterLk.Store(locked)
	return locked
}

// Unlock unlocks the getter.
func (se *syncGetter[H]) Unlock() {
	se.checkLock("Unlock without preceding Lock on syncGetter")
	se.getterLk.Unlock()
	se.isGetterLk.Store(false)
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
