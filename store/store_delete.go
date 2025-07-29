package store

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/ipfs/go-datastore"

	"github.com/celestiaorg/go-header"
)

// OnDelete implements [header.Store] interface.
func (s *Store[H]) OnDelete(fn func(context.Context, uint64) error) {
	s.onDeleteMu.Lock()
	defer s.onDeleteMu.Unlock()

	s.onDelete = append(s.onDelete, func(ctx context.Context, height uint64) (rerr error) {
		defer func() {
			err := recover()
			if err != nil {
				rerr = fmt.Errorf(
					"header/store: user provided onDelete panicked on %d with: %s",
					height,
					err,
				)
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
var (
	deleteRangeParallelThreshold uint64 = 10000
	errDeleteTimeout                    = errors.New("delete timeout")
)

// deleteRange deletes [from:to) header range from the store.
// It gracefully handles context and errors attempting to save interrupted progress.
func (s *Store[H]) deleteRange(ctx context.Context, from, to uint64) (err error) {
	startTime := time.Now()

	var height uint64
	defer func() {
		if err != nil {
			if errors.Is(err, errDeleteTimeout) {
				log.Warnw("partial delete",
					"from_height", from,
					"expected_to_height", to,
					"actual_to_height", height,
					"took(s)", time.Since(startTime),
				)
			} else {
				log.Errorw("partial delete with error",
					"from_height", from,
					"expected_to_height", to,
					"actual_to_height", height,
					"took(s)", time.Since(startTime),
					"err", err,
				)
			}
		} else if to-from > 1 {
			log.Debugw("deleted headers", "from_height", from, "to_height", to, "took", time.Since(startTime))
		}

		if derr := s.setTail(ctx, s.ds, height); derr != nil {
			err = errors.Join(err, fmt.Errorf("setting tail to %d: %w", height, derr))
		}
	}()

	if deadline, ok := ctx.Deadline(); ok {
		// allocate 95% of caller's set deadline for deletion
		// and give leftover to save progress
		// this prevents store's state corruption from partial deletion
		sub := deadline.Sub(startTime) / 100 * 95
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadlineCause(ctx, startTime.Add(sub), errDeleteTimeout)
		defer cancel()
	}

	if to-from < deleteRangeParallelThreshold {
		height, err = s.deleteSequential(ctx, from, to)
	} else {
		height, err = s.deleteParallel(ctx, from, to)
	}

	return err
}

// deleteSingle deletes a single header from the store,
// its caches and indexies, notifying any registered onDelete handlers.
func (s *Store[H]) deleteSingle(
	ctx context.Context,
	height uint64,
	batch datastore.Batch,
	onDelete []func(ctx context.Context, height uint64) error,
) error {
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

// deleteSequential deletes [from:to) header range from the store sequentially
// and returns the highest unprocessed height: 'to' in success case or the failed height in error case.
func (s *Store[H]) deleteSequential(
	ctx context.Context,
	from, to uint64,
) (highest uint64, err error) {
	log.Debugw("starting delete range sequential", "from_height", from, "to_height", to)

	batch, err := s.ds.Batch(ctx)
	if err != nil {
		return 0, fmt.Errorf("new batch: %w", err)
	}
	defer func() {
		if derr := batch.Commit(ctx); derr != nil {
			err = errors.Join(err, fmt.Errorf("committing batch: %w", derr))
		}
	}()

	s.onDeleteMu.Lock()
	onDelete := slices.Clone(s.onDelete)
	s.onDeleteMu.Unlock()

	for height := from; height < to; height++ {
		if err := s.deleteSingle(ctx, height, batch, onDelete); err != nil {
			return height, err
		}
	}

	return to, nil
}

// deleteParallel deletes [from:to) header range from the store in parallel
// and returns the highest unprocessed height: 'to' in success case or the failed height in error case.
func (s *Store[H]) deleteParallel(ctx context.Context, from, to uint64) (uint64, error) {
	log.Debugw("starting delete range parallel", "from_height", from, "to_height", to)

	s.onDeleteMu.Lock()
	onDelete := slices.Clone(s.onDelete)
	s.onDeleteMu.Unlock()

	// workerNum defines how many parallel delete workers to run
	// Scales of number of CPUs configured for the process.
	// Usually, it's recommended to have 2-4 multiplier for the number of CPUs for
	// IO operations. Three was picked empirically to be a sweet spot that doesn't
	// require too much RAM, yet shows good performance.
	workerNum := runtime.GOMAXPROCS(-1) * 3

	type result struct {
		height uint64
		err    error
	}
	results := make([]result, workerNum)
	jobCh := make(chan uint64, workerNum)
	errCh := make(chan error)

	worker := func(worker int) {
		var last result
		defer func() {
			results[worker] = last
			if last.err != nil {
				close(errCh)
			}
		}()

		batch, err := s.ds.Batch(ctx)
		if err != nil {
			last.err = fmt.Errorf("new batch: %w", err)
			return
		}

		for height := range jobCh {
			last.height = height
			last.err = s.deleteSingle(ctx, height, batch, onDelete)
			if last.err != nil {
				break
			}
		}

		if err := batch.Commit(ctx); err != nil {
			last.err = errors.Join(last.err, fmt.Errorf("committing delete batch: %w", err))
		}
	}

	var wg sync.WaitGroup
	wg.Add(workerNum)
	for i := range workerNum {
		go func(i int) {
			defer wg.Done()
			worker(i)
		}(i)
	}

	for i, height := 0, from; height < to; height++ {
		select {
		case jobCh <- height:
			i++
			if uint64(1)%deleteRangeParallelThreshold == 0 {
				log.Debugf("deleting %dth header height %d", deleteRangeParallelThreshold, height)
			}
		case <-errCh:
		}
	}

	// tell workers to stop
	close(jobCh)
	// await all workers to finish
	wg.Wait()
	// ensure results are sorted in ascending order of heights
	slices.SortFunc(results, func(a, b result) int {
		return int(a.height - b.height) //nolint:gosec
	})
	// find the highest deleted height
	var highest uint64
	for _, result := range results {
		if result.err != nil {
			// return the error immediately even if some higher headers may have been deleted
			// this ensures we set tail to the lowest errored height, s.t. retries do not shadowly miss any headers
			return result.height, result.err
		}

		if result.height > highest {
			highest = result.height
		}
	}

	// ensures the height after the highest deleted becomes the new tail
	highest++
	return highest, nil
}
