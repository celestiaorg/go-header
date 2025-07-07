package sync

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/celestiaorg/go-header"
)

// subjectiveTail returns the current actual Tail header.
// Lazily fetching it if it doesn't exist locally or moving it to a different height.
// Moving is done if either parameters are changed or tail moved outside a pruning window.
func (s *Syncer[H]) subjectiveTail(ctx context.Context, head H) (H, error) {
	oldTail, err := s.store.Tail(ctx)
	if err != nil && !errors.Is(err, header.ErrEmptyStore) {
		return oldTail, err
	}

	if !s.tailMu.TryLock() {
		// prevents concurrent tail estimation and moving
		//
		// If a new head arrives, while tail for the previous head is still in progress
		// it is valid to skip tail renewal for the new one. It will be resolved with a more recent head
		// once in progress tail finishes.
		return oldTail, nil
	}
	defer s.tailMu.Unlock()

	newTail, err := s.renewTail(ctx, oldTail, head)
	if err != nil {
		return oldTail, fmt.Errorf("updating tail: %w", err)
	}

	if err := s.moveTail(ctx, oldTail, newTail); err != nil {
		return oldTail, fmt.Errorf(
			"moving tail from %d to %d: %w",
			oldTail.Height(),
			newTail.Height(),
			err,
		)
	}

	return newTail, nil
}

// renewTail resolves the new actual tail header respecting Syncer parameters.
func (s *Syncer[H]) renewTail(ctx context.Context, oldTail, head H) (newTail H, err error) {
	useHash, tailHash := s.tailHash(oldTail)
	switch {
	case useHash:
		if tailHash == nil {
			// nothing to renew, stick to the existing old tail hash
			return oldTail, nil
		}

		newTail, err = s.store.Get(ctx, tailHash)
		if err == nil {
			return newTail, nil
		}
		if !errors.Is(err, header.ErrNotFound) {
			return newTail, fmt.Errorf(
				"loading SyncFromHash tail from store(%x): %w",
				tailHash,
				err,
			)
		}

		newTail, err = s.getter.Get(ctx, tailHash)
		if err != nil {
			return newTail, fmt.Errorf("fetching SyncFromHash tail(%x): %w", tailHash, err)
		}
		log.Debugw("fetched tail header by hash", "hash", tailHash)
	case !useHash:
		tailHeight, err := s.tailHeight(ctx, oldTail, head)
		if err != nil {
			return oldTail, err
		}

		if tailHeight <= s.store.Height() {
			// check if the new tail is below the current head to avoid heightSub blocking
			newTail, err = s.store.GetByHeight(ctx, tailHeight)
			if err == nil {
				return newTail, nil
			}
			if !errors.Is(err, header.ErrNotFound) {
				return newTail, fmt.Errorf(
					"loading SyncFromHeight tail from store(%d): %w",
					tailHeight,
					err,
				)
			}
		}

		newTail, err = s.getter.GetByHeight(ctx, tailHeight)
		if err != nil {
			return newTail, fmt.Errorf("fetching SyncFromHeight tail(%d): %w", tailHeight, err)
		}
		log.Debugw("fetched tail header by height", "height", tailHeight)
	}

	if err := s.store.Append(ctx, newTail); err != nil {
		return newTail, fmt.Errorf("appending tail header: %w", err)
	}

	return newTail, nil
}

// moveTail moves the Tail to be the 'to' header.
// It will prune the store if the new Tail is higher than the old one or
// sync up the difference if the new Tail is lower than the old one.
func (s *Syncer[H]) moveTail(ctx context.Context, from, to H) error {
	if from.IsZero() {
		// no need to move the tail if it was not set previously
		return nil
	}

	switch {
	case from.Height() < to.Height():
		log.Infof("move tail up from %d to %d, pruning the diff...", from.Height(), to.Height())
		err := s.store.DeleteTo(ctx, to.Height())
		if err != nil {
			return fmt.Errorf(
				"deleting headers up to newly configured tail(%d): %w",
				to.Height(),
				err,
			)
		}
	case from.Height() > to.Height():
		log.Infof("move tail down from %d to %d, syncing the diff...", from.Height(), to.Height())

		// TODO(@Wondertan): This works but it assumes this code is only run before syncing routine starts.
		//  If run after, it may race with other in prog syncs.
		//  To be reworked by bsync.
		err := s.doSync(ctx, to, from)
		if err != nil {
			return fmt.Errorf(
				"syncing the diff between from(%d) and to tail(%d): %w",
				from.Height(),
				to.Height(),
				err,
			)
		}
	}

	return nil
}

// tailHash reports whether tail hash should be used and returns it.
// Returns empty hash if it hasn't changed from the old tail hash.
func (s *Syncer[H]) tailHash(oldTail H) (bool, header.Hash) {
	hashStr := s.Params.SyncFromHash
	if len(hashStr) == 0 {
		return false, nil
	}
	hash, err := hex.DecodeString(hashStr)
	if err != nil {
		return false, nil
	}

	updated := oldTail.IsZero() || !bytes.Equal(hash, oldTail.Hash())
	if !updated {
		return true, nil
	}

	log.Debugw("tail hash updated", "hash", hash)
	return true, hash
}

// tailHeight figures the actual tail height based on the Syncer parameters.
func (s *Syncer[H]) tailHeight(ctx context.Context, oldTail, head H) (uint64, error) {
	height := s.Params.SyncFromHeight
	if height > 0 {
		return height, nil
	}

	if oldTail.IsZero() {
		return s.estimateTailHeight(head), nil
	}

	height, err := s.findTailHeight(ctx, oldTail, head)
	if err != nil {
		return 0, fmt.Errorf("finding tail height: %w", err)
	}

	return height, nil
}

// estimateTailHeight estimates the tail header based on the current head.
// It respects the trusting period, ensuring Syncer never initializes off an expired header.
func (s *Syncer[H]) estimateTailHeight(head H) uint64 {
	headersToRetain := uint64(s.Params.TrustingPeriod / s.Params.blockTime) //nolint:gosec
	if headersToRetain >= head.Height() {
		// means chain is very young so we can keep all headers starting from genesis
		return 1
	}

	return head.Height() - headersToRetain
}

// findTailHeight find the tail height based on the current head and tail.
// It respects the pruning window, ensuring Syncer maintains the tail within the window.
func (s *Syncer[H]) findTailHeight(ctx context.Context, oldTail, head H) (uint64, error) {
	window := s.Params.PruningWindow
	expectedTailTime := head.Time().UTC().Add(-window)
	currentTailTime := oldTail.Time().UTC()
	tailTimeDiff := expectedTailTime.Sub(currentTailTime)

	var estimatedTailHeight uint64
	switch {
	case tailTimeDiff <= 0:
		// current tail is relevant as is
		log.Debugw("relevant old tail", "expected_tail_time", expectedTailTime.Format(time.DateTime), "current_tail_time", currentTailTime.Format(time.DateTime))
		return oldTail.Height(), nil
	case tailTimeDiff >= window:
		// current and expected tails are far from each other
		// estimate with head for higher accuracy
		headersToStore := uint64(window / s.Params.blockTime) //nolint:gosec
		estimatedTailHeight = head.Height() - headersToStore
	case tailTimeDiff < window:
		// tails are close
		// estimate with tail for higher accuracy
		headersToStore := uint64(tailTimeDiff / s.Params.blockTime) //nolint:gosec
		estimatedTailHeight = oldTail.Height() + headersToStore
	}

	log.Debugw(
		"current tail is beyond pruning window",
		"time_diff", tailTimeDiff.String(),
		"window", s.Params.PruningWindow.String(),
		"curr_tail", oldTail.Height(),
		"new_estimated_tail", estimatedTailHeight,
	)

	newTailHeight := estimatedTailHeight
	for {
		// store keeps all the headers up to the current head
		// to iterate over the headers and find the most accurate tail
		newTail, err := s.store.GetByHeight(ctx, newTailHeight)
		if err != nil {
			return 0, fmt.Errorf(
				"getting estimated new tail(%d) from store: %w",
				estimatedTailHeight,
				err,
			)
		}

		if expectedTailTime.Compare(newTail.Time().UTC()) <= 0 {
			break
		}

		newTailHeight++
	}

	log.Debugw(
		"new tail height",
		"new_confirmed_tail",
		newTailHeight,
		"estimation_error",
		newTailHeight-estimatedTailHeight,
	)
	return newTailHeight, nil
}
