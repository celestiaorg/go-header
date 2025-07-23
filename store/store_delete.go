package store

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/celestiaorg/go-header"
	"github.com/ipfs/go-datastore"
)

// OnDelete implements [header.Store] interface.
func (s *Store[H]) OnDelete(fn func(context.Context, uint64) error) {
	s.onDeleteMu.Lock()
	defer s.onDeleteMu.Unlock()

	s.onDelete = append(s.onDelete, func(ctx context.Context, height uint64) (rerr error) {
		defer func() {
			err := recover()
			if err != nil {
				rerr = fmt.Errorf("header/store: user provided onDelete panicked on %d with: %s", height, err)
			}
		}()
		return fn(ctx, height)
	})
}

// DeleteTo implements [header.Store] interface.
func (s *Store[H]) DeleteTo(ctx context.Context, to uint64) error {
	// ensure all the pending headers are synchronized
	err := s.Sync(ctx)
	if err != nil {
		return err
	}

	head, err := s.Head(ctx)
	if err != nil {
		return fmt.Errorf("header/store: reading head: %w", err)
	}
	if head.Height()+1 < to {
		_, err := s.getByHeight(ctx, to)
		if errors.Is(err, header.ErrNotFound) {
			return fmt.Errorf(
				"header/store: delete to %d beyond current head(%d)",
				to,
				head.Height(),
			)
		}
		if err != nil {
			return fmt.Errorf("delete to potential new head: %w", err)
		}

		//  if `to` is bigger than the current head and is stored - allow delete, making `to` a new head
	}

	tail, err := s.Tail(ctx)
	if err != nil {
		return fmt.Errorf("header/store: reading tail: %w", err)
	}
	if tail.Height() >= to {
		return fmt.Errorf("header/store: delete to %d below current tail(%d)", to, tail.Height())
	}

	if err := s.deleteRange(ctx, tail.Height(), to); err != nil {
		return fmt.Errorf("header/store: delete to height %d: %w", to, err)
	}

	if head.Height()+1 == to {
		// this is the case where we have deleted all the headers
		// wipe the store
		if err := s.wipe(ctx); err != nil {
			return fmt.Errorf("header/store: wipe: %w", err)
		}
	}

	return nil
}

// deleteRangeParallelThreshold defines the threshold for parallel deletion.
// If range is smaller than this threshold, deletion will be performed sequentially.
var deleteRangeParallelThreshold uint64 = 10000

// deleteRange deletes [from:to) header range from the store.
func (s *Store[H]) deleteRange(ctx context.Context, from, to uint64) error {
	if to-from < deleteRangeParallelThreshold {
		return s.deleteSequential(ctx, from, to)
	}

	return s.deleteParallel(ctx, from, to)
}

// deleteSequential deletes [from:to) header range from the store sequentially.
func (s *Store[H]) deleteSequential(ctx context.Context, from, to uint64) (err error) {
	log.Debugw("starting delete range sequential", "from_height", from, "to_height", to)

	batch, err := s.ds.Batch(ctx)
	if err != nil {
		return fmt.Errorf("new batch: %w", err)
	}
	// ctx = badger4.WithBatch(ctx, batch)

	height := from
	defer func() {
		if derr := s.setTail(ctx, s.ds, height); derr != nil {
			err = errors.Join(err, fmt.Errorf("setting tail to %d: %w", height, derr))
		}

		if derr := batch.Commit(ctx); derr != nil {
			err = errors.Join(err, fmt.Errorf("committing batch: %w", derr))
		}
	}()

	s.onDeleteMu.Lock()
	onDelete := slices.Clone(s.onDelete)
	s.onDeleteMu.Unlock()

	for ; height < to; height++ {
		if err := s.delete(ctx, height, batch, onDelete); err != nil {
			return err
		}
	}

	return nil
}

// delete deletes a single header from the store, its caches and indexies, notifying any registered onDelete handlers.
func (s *Store[H]) delete(ctx context.Context, height uint64, batch datastore.Batch, onDelete []func(ctx context.Context, height uint64) error) error {
	// some of the methods may not handle context cancellation properly
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}

	hash, err := s.heightIndex.HashByHeight(ctx, height, false)
	if errors.Is(err, datastore.ErrNotFound) {
		log.Warnw("attempt to delete header that's not found", "height", height)
		return nil
	}
	if err != nil {
		return fmt.Errorf("hash by height %d: %w", height, err)
	}

	for _, deleteFn := range onDelete {
		if err := deleteFn(ctx, height); err != nil {
			return fmt.Errorf("on delete handler for %d: %w", height, err)
		}
	}

	if err := batch.Delete(ctx, hashKey(hash)); err != nil {
		return fmt.Errorf("delete hash key (%X): %w", hash, err)
	}
	if err := batch.Delete(ctx, heightKey(height)); err != nil {
		return fmt.Errorf("delete height key (%d): %w", height, err)
	}

	s.cache.Remove(hash.String())
	s.heightIndex.cache.Remove(height)
	s.pending.DeleteRange(height, height+1)
	return nil
}

// workerNum defines how many parallel delete workers to run
// Scales of number of CPUs configred for the process.
var workerNum = runtime.GOMAXPROCS(-1) * 3

// deleteParallel deletes [from:to) header range from the store in parallel.
// It gracefully handles context and errors attempting to save interrupted progress.
func (s *Store[H]) deleteParallel(ctx context.Context, from, to uint64) (err error) {
	log.Debugw("starting delete range parallel", "from_height", from, "to_height", to)

	deleteCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	startTime := time.Now()
	if deadline, ok := ctx.Deadline(); ok {
		// allocate 95% of caller's set deadline for deletion
		// and give leftover to save progress
		// this prevents store's state corruption from partial deletion
		sub := deadline.Sub(startTime) / 100 * 95
		var cancel context.CancelFunc
		deleteCtx, cancel = context.WithDeadlineCause(ctx, startTime.Add(sub), deleteTimeoutError)
		defer cancel()
	}

	var highestDeleted atomic.Uint64
	defer func() {
		newTailHeight := highestDeleted.Load() + 1
		if err != nil {
			if errors.Is(err, deleteTimeoutError) {
				log.Warnw("partial delete",
					"from_height", from,
					"expected_to_height", to,
					"actual_to_height", newTailHeight,
					"took(s)", time.Since(startTime),
				)
			} else {
				log.Errorw("partial delete with error",
					"from_height", from,
					"expected_to_height", to,
					"actual_to_height", newTailHeight,
					"took(s)", time.Since(startTime),
					"err", err,
				)
			}
		} else if to-from > 1 {
			log.Infow("deleted headers", "from_height", from, "to_height", to, "took", time.Since(startTime))
		}

		if derr := s.setTail(ctx, s.ds, newTailHeight); derr != nil {
			err = errors.Join(err, fmt.Errorf("setting tail to %d: %w", newTailHeight, derr))
		}
	}()

	s.onDeleteMu.Lock()
	onDelete := slices.Clone(s.onDelete)
	s.onDeleteMu.Unlock()

	jobCh := make(chan uint64, workerNum)
	errCh := make(chan error, 1)

	worker := func() {
		batch, err := s.ds.Batch(ctx)
		if err != nil {
			errCh <- fmt.Errorf("new batch: %w", err)
			return
		}
		// deleteCtx := badger4.WithBatch(deleteCtx, batch)

		defer func() {
			if err := batch.Commit(ctx); err != nil {
				errCh <- fmt.Errorf("committing delete batch: %w", err)
			}
		}()

		var lastHeight uint64
		defer func() {
			highest := highestDeleted.Load()
			for lastHeight > highest && !highestDeleted.CompareAndSwap(highest, lastHeight) {
				highest = highestDeleted.Load()
			}
		}()

		for {
			select {
			case height, ok := <-jobCh:
				if !ok {
					return
				}

				if err := s.delete(deleteCtx, height, batch, onDelete); err != nil {
					select {
					case errCh <- fmt.Errorf("delete header %d: %w", height, err):
					default:
					}
					return
				}

				lastHeight = height
			}
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < workerNum; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker()
		}()
	}
	defer wg.Wait()

	for i, height := 0, from; height < to; height++ {
		select {
		case jobCh <- height:
			i++
			if i%100000 == 0 {
				log.Debugf("deleting %d header", i)
			}
		case err = <-errCh:
			close(jobCh)
			return err
		}
	}

	close(jobCh)
	return err
}

var deleteTimeoutError = errors.New("delete timeout")
