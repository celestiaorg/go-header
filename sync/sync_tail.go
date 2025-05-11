package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/celestiaorg/go-header"
)

// TODO:
// * Flush

// subjectiveTail returns the current Tail header.
// Lazily fetching it if it doesn't exist locally or moving it to a different height.
// Moving is done if either parameters are changed or tail moved outside a pruning window.
func (s *Syncer[H]) subjectiveTail(ctx context.Context, head H) (H, error) {
	tail, err := s.store.Tail(ctx)
	if err != nil && !errors.Is(err, header.ErrEmptyStore) {
		return tail, err
	}

	var fetched bool
	if tailHash, outdated := s.isTailHashOutdated(tail); outdated {
		tail, err = s.store.Get(ctx, tailHash)
		if err != nil {
			tail, err = s.getter.Get(ctx, tailHash)
			if err != nil {
				return tail, fmt.Errorf("getting SyncFromHash tail(%x): %w", tailHash, err)
			}
			fetched = true
		}
	} else if tailHeight, outdated := s.isTailHeightOutdated(tail); outdated {
		// hack for the case with tailHeight > store.Height avoiding heightSub
		storeCtx, cancel := context.WithTimeout(ctx, time.Second)
		tail, err = s.store.GetByHeight(storeCtx, tailHeight)
		cancel()
		if err != nil {
			tail, err = s.getter.GetByHeight(ctx, tailHeight)
			if err != nil {
				return tail, fmt.Errorf("getting SyncFromHeight tail(%d): %w", tailHeight, err)
			}
			fetched = true
		}
	} else if tailHash == nil && tailHeight == 0 {
		if tail.IsZero() {
			// no previously known Tail available - estimate solely on Head
			estimatedHeight := estimateTail(head, s.Params.blockTime, s.Params.TrustingPeriod)
			tail, err = s.getter.GetByHeight(ctx, estimatedHeight)
			if err != nil {
				return tail, fmt.Errorf("getting estimated tail(%d): %w", tailHeight, err)
			}
			fetched = true
		} else {
			// have a known Tail - estimate basing on it.
			cutoffTime := head.Time().UTC().Add(-s.Params.TrustingPeriod)
			diff := cutoffTime.Sub(tail.Time().UTC())
			if diff <= 0 {
				// current tail is relevant as is
				return tail, nil
			}

			toDeleteEstimate := uint64(diff / s.Params.blockTime)
			estimatedNewTail := tail.Height() + toDeleteEstimate

			for {
				tail, err = s.store.GetByHeight(ctx, estimatedNewTail)
				if err != nil {
					log.Errorw("getting estimated tail from store ", "error", err)
					return tail, err
				}
				if tail.Time().UTC().Before(cutoffTime) {
					break
				}

				estimatedNewTail++
			}
		}
	}

	if fetched {
		if err := s.store.Append(ctx, tail); err != nil {
			return tail, fmt.Errorf("appending tail header: %w", err)
		}

		time.Sleep(time.Millisecond * 1000)
		// TODO: Flush
	}

	if err := s.moveTail(ctx, tail); err != nil {
		return tail, fmt.Errorf("moving tail: %w", err)
	}

	return tail, nil
}

// moveTail moves the Tail to be the given header.
// It will prune the store if the new Tail is higher than the old one or
// sync up if the new Tail is lower than the old one.
func (s *Syncer[H]) moveTail(ctx context.Context, new H) error {
	old, err := s.store.Tail(ctx)
	if errors.Is(err, header.ErrEmptyStore) {
		return nil
	}
	if err != nil {
		return err
	}

	switch {
	case old.Height() < new.Height():
		log.Infof("move tail up from %d to %d, pruning the diff...", old, new)
		err := s.store.DeleteTo(ctx, new.Height())
		if err != nil {
			return fmt.Errorf(
				"deleting headers up to newly configured Tail(%d): %w",
				new.Height(),
				err,
			)
		}
	case old.Height() > new.Height():
		log.Infof("move tail down from %d to %d, syncing the diff...", old, new)

		// TODO(@Wondertan): This works but it assumes this code is only run before syncing routine starts.
		//  If run after, it may race with other in prog syncs.
		//  To be reworked by bsync.
		err := s.doSync(ctx, new, old)
		if err != nil {
			return fmt.Errorf("syncing the diff between old and new Tail: %w", err)
		}
	}

	return nil
}

func estimateTail[H header.Header[H]](
	head H,
	blockTime, trustingPeriod time.Duration,
) (height uint64) {
	headersToRetain := uint64(trustingPeriod / blockTime) //nolint:gosec

	if headersToRetain >= head.Height() {
		return 1
	}
	tail := head.Height() - headersToRetain
	return tail
}

func (s *Syncer[H]) isTailHashOutdated(h H) (header.Hash, bool) {
	wantHash := s.Params.SyncFromHash
	outdated := wantHash != nil && (h.IsZero() || !bytes.Equal(wantHash, h.Hash()))
	return wantHash, outdated
}

func (s *Syncer[H]) isTailHeightOutdated(h H) (uint64, bool) {
	wantHeight := s.Params.SyncFromHeight
	outdated := wantHeight > 0 && (h.IsZero() || h.Height() != wantHeight)
	return wantHeight, outdated
}
