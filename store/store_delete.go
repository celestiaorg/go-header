package store

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"slices"
	"sync"
	"time"

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
				rerr = fmt.Errorf(
					"user provided onDelete panicked on %d with: %s\n%s",
					height,
					err,
					string(debug.Stack()),
				)
			}
		}()
		return fn(ctx, height)
	})
}

// deleteRangeParallelThreshold defines the threshold for parallel deletion.
// If range is smaller than this threshold, deletion will be performed sequentially.
var (
	deleteRangeParallelThreshold uint64 = 10000
	errDeleteTimeout                    = errors.New("delete timeout")
)

// deleteSingle deletes a single header from the store,
// its caches and indexies, notifying any registered onDelete handlers.
func (s *Store[H]) deleteSingle(
	ctx context.Context,
	height uint64,
	onDelete []func(ctx context.Context, height uint64) error,
) error {
	// some of the methods may not handle context cancellation properly
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}

	hash, err := s.heightIndex.HashByHeight(ctx, height, false)
	if err != nil {
		return fmt.Errorf("hash by height %d: %w", height, err)
	}

	for _, deleteFn := range onDelete {
		if err := deleteFn(ctx, height); err != nil {
			return fmt.Errorf("on delete handler for %d: %w", height, err)
		}
	}

	if err := s.ds.Delete(ctx, hashKey(hash)); err != nil {
		return fmt.Errorf("delete hash key (%X): %w", hash, err)
	}
	if err := s.ds.Delete(ctx, heightKey(height)); err != nil {
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
) (highest uint64, missing int, err error) {
	log.Debugw("starting delete range sequential", "from_height", from, "to_height", to)

	ctx, done := s.withWriteBatch(ctx)
	defer func() {
		if derr := done(); derr != nil {
			err = errors.Join(err, fmt.Errorf("committing batch: %w", derr))
		}
	}()
	ctx, doneTx := s.withReadTransaction(ctx)
	defer doneTx()

	s.onDeleteMu.Lock()
	onDelete := slices.Clone(s.onDelete)
	s.onDeleteMu.Unlock()

	for height := from; height < to; height++ {
		err := s.deleteSingle(ctx, height, onDelete)
		if errors.Is(err, datastore.ErrNotFound) {
			missing++
			log.Debugw("attempt to delete header that's not found", "height", height)
		} else if err != nil {
			return height, missing, err
		}
	}

	return to, missing, nil
}

// deleteParallel deletes [from:to) header range from the store in parallel
// and returns the highest unprocessed height: 'to' in success case or the failed height in error case.
func (s *Store[H]) deleteParallel(ctx context.Context, from, to uint64) (uint64, int, error) {
	now := time.Now()
	s.onDeleteMu.Lock()
	onDelete := slices.Clone(s.onDelete)
	s.onDeleteMu.Unlock()
	// workerNum defines how many parallel delete workers to run
	// Scales of number of CPUs configured for the process.
	// Usually, it's recommended to have 2-4 multiplier for the number of CPUs for
	// IO operations. Three was picked empirically to be a sweet spot that doesn't
	// require too much RAM, yet shows good performance.
	workerNum := runtime.GOMAXPROCS(-1) * 3

	log.Infow("deleting range parallel",
		"from_height", from,
		"to_height", to,
		"worker_num", workerNum,
	)

	type result struct {
		missing int
		height  uint64
		err     error
	}
	results := make([]result, workerNum)
	jobCh := make(chan uint64, workerNum)
	errCh := make(chan error)

	worker := func(worker int) {
		var last result
		defer func() {
			results[worker] = last
			if last.err != nil {
				select {
				case <-errCh:
				default:
					close(errCh)
				}
			}
		}()

		workerCtx, done := s.withWriteBatch(ctx)
		defer func() {
			if err := done(); err != nil {
				last.err = errors.Join(last.err, fmt.Errorf("committing delete batch: %w", err))
			}
		}()
		workerCtx, doneTx := s.withReadTransaction(workerCtx)
		defer doneTx()

		for height := range jobCh {
			last.height = height
			last.err = s.deleteSingle(workerCtx, height, onDelete)
			if errors.Is(last.err, datastore.ErrNotFound) {
				last.missing++
				log.Debugw("attempt to delete header that's not found", "height", height)
			} else if last.err != nil {
				break
			}
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

	for i, height := uint64(0), from; height < to; height++ {
		select {
		case jobCh <- height:
			i++
			if i%deleteRangeParallelThreshold == 0 {
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
	var (
		highest uint64
		missing int
	)
	for _, result := range results {
		if result.err != nil {
			// return the error immediately even if some higher headers may have been deleted
			// this ensures we set tail to the lowest errored height, s.t. retries do not shadowly miss any headers
			return result.height, missing, result.err
		}

		if result.height > highest {
			highest = result.height
		}
		missing += result.missing
	}

	// ensures the height after the highest deleted becomes the new tail
	highest++
	log.Infow("deleted range parallel",
		"from_height", from,
		"to_height", to,
		"hdrs_not_found", missing,
		"took(s)", time.Since(now).Seconds(),
	)
	return highest, missing, nil
}

// DeleteRange deletes headers in the range [from:to) from the store.
// It intelligently updates head and/or tail pointers based on what range is being deleted.
func (s *Store[H]) DeleteRange(ctx context.Context, from, to uint64) error {
	// ensure all the pending headers are synchronized
	err := s.Sync(ctx)
	if err != nil {
		return err
	}

	head, err := s.Head(ctx)
	if err != nil {
		return fmt.Errorf("header/store: reading head: %w", err)
	}

	tail, err := s.Tail(ctx)
	if err != nil {
		return fmt.Errorf("header/store: reading tail: %w", err)
	}

	// validate range parameters
	if from >= to {
		return fmt.Errorf(
			"header/store: invalid range [%d:%d) - from must be less than to",
			from,
			to,
		)
	}

	if from < tail.Height() {
		return fmt.Errorf(
			"header/store: delete range from %d below current tail(%d)",
			from,
			tail.Height(),
		)
	}

	// Note: Allow deletion beyond head to match original DeleteTo behavior
	// Missing headers in the range will be handled gracefully by the deletion logic

	// if range is empty within the current store bounds, it's a no-op
	if from > head.Height() || to <= tail.Height() {
		log.Warn("header/store range is empty, nothing needs to be deleted")
		return nil
	}

	// Validate that deletion won't create gaps in the store
	// Only allow deletions that:
	// 1. Start from tail (advancing tail forward)
	// 2. End at head+1 (moving head backward)
	// 3. Delete the entire store
	if from > tail.Height() && to <= head.Height() {
		return fmt.Errorf(
			"header/store: deletion range [%d:%d) would create gaps in the store. "+
				"Only deletion from tail (%d) or to head+1 (%d) is supported",
			from, to, tail.Height(), head.Height()+1,
		)
	}

	// Check if we're deleting all existing headers (making store empty)
	// Only wipe if 'to' is exactly at head+1 (normal case) to avoid accidental wipes
	if from == tail.Height() && to == head.Height()+1 {
		// Check if any headers exist at or beyond 'to' in the pending buffer
		hasHeadersAtOrBeyond := false
		pendingHeaders := s.pending.GetAll()
		for _, h := range pendingHeaders {
			if h.Height() >= to {
				hasHeadersAtOrBeyond = true
				break
			}
		}

		if !hasHeadersAtOrBeyond {
			// wipe the entire store
			if err := s.wipe(ctx); err != nil {
				return fmt.Errorf("header/store: wipe: %w", err)
			}
			return nil
		}
	}

	// Determine which pointers need updating
	updateTail := from <= tail.Height()
	updateHead := to > head.Height()

	// Delete the headers without automatic tail updates
	actualTo, _, err := s.deleteRangeRaw(ctx, from, to)
	if err != nil {
		return fmt.Errorf(
			"header/store: delete range [%d:%d) (actual: %d): %w",
			from,
			to,
			actualTo,
			err,
		)
	}

	// Update tail if we deleted from the beginning
	if updateTail {
		err = s.setTail(ctx, s.ds, to)
		if err != nil {
			return fmt.Errorf("header/store: setting tail to %d: %w", to, err)
		}
	}

	// Update head if we deleted from the end
	if updateHead && from > tail.Height() {
		newHeadHeight := from - 1
		if newHeadHeight >= tail.Height() {
			err = s.setHead(ctx, s.ds, newHeadHeight)
			if err != nil {
				return fmt.Errorf("header/store: setting head to %d: %w", newHeadHeight, err)
			}
		}
	}

	return nil
}

// deleteRangeRaw deletes [from:to) header range without updating head or tail pointers.
// Returns the actual highest height processed (actualTo) and the number of missing headers.
func (s *Store[H]) deleteRangeRaw(
	ctx context.Context,
	from, to uint64,
) (actualTo uint64, missing int, err error) {
	startTime := time.Now()

	var height uint64
	defer func() {
		actualTo = height
		if err != nil {
			if errors.Is(err, errDeleteTimeout) {
				log.Warnw("partial delete range",
					"from_height", from,
					"expected_to_height", to,
					"actual_to_height", height,
					"hdrs_not_found", missing,
					"took(s)", time.Since(startTime).Seconds(),
				)
			} else {
				log.Errorw("partial delete range with error",
					"from_height", from,
					"expected_to_height", to,
					"actual_to_height", height,
					"hdrs_not_found", missing,
					"took(s)", time.Since(startTime).Seconds(),
					"err", err,
				)
			}
		} else if to-from > 1 {
			log.Debugw("deleted range",
				"from_height", from,
				"to_height", to,
				"hdrs_not_found", missing,
				"took(s)", time.Since(startTime).Seconds(),
			)
		}
	}()

	deleteCtx := ctx
	if deadline, ok := ctx.Deadline(); ok {
		// allocate 95% of caller's set deadline for deletion
		// and give leftover to save progress
		sub := deadline.Sub(startTime) / 100 * 95
		var cancel context.CancelFunc
		deleteCtx, cancel = context.WithDeadlineCause(ctx, startTime.Add(sub), errDeleteTimeout)
		defer cancel()
	}

	if to-from < deleteRangeParallelThreshold {
		height, missing, err = s.deleteSequential(deleteCtx, from, to)
	} else {
		height, missing, err = s.deleteParallel(deleteCtx, from, to)
	}

	return height, missing, err
}

// setHead sets the head of the store to the specified height.
func (s *Store[H]) setHead(ctx context.Context, write datastore.Write, to uint64) error {
	newHead, err := s.getByHeight(ctx, to)
	if err != nil {
		return fmt.Errorf("getting head: %w", err)
	}

	// update the contiguous head
	s.contiguousHead.Store(&newHead)
	if err := writeHeaderHashTo(ctx, write, newHead, headKey); err != nil {
		return fmt.Errorf("writing headKey in batch: %w", err)
	}

	return nil
}
