package store

import (
	"context"
	"errors"

	"github.com/celestiaorg/go-header"
	"github.com/ipfs/go-datastore"
)

// ResetTail resets the tail of the store to be at the given height.
// The new tail must be present in the store.
// WARNING: Only use this function if you know what you are doing.
func UnsafeResetTail[H header.Header[H]](ctx context.Context, store *Store[H], height uint64) error {
	if err := store.setTail(ctx, store.ds, height); err != nil {
		return err
	}

	return nil
}

// ResetHead resets the head of the store to be at the given height.
// The new head must be present in the store.
// WARNING: Only use this function if you know what you are doing.
func UnsafeResetHead[H header.Header[H]](ctx context.Context, store *Store[H], height uint64) error {
	newHead, err := store.getByHeight(ctx, height)
	if err != nil {
		return err
	}

	if err := writeHeaderHashTo(ctx, store.ds, newHead, headKey); err != nil {
		return err
	}

	store.contiguousHead.Store(&newHead)
	return nil
}

// FindHeader forward iterates over the store starting from the given height until it finds any stored header
// or the context is canceled.
func FindHeader[H header.Header[H]](
	ctx context.Context,
	store *Store[H],
	startFrom uint64,
) (H, error) {
	ctx, done := store.withReadTransaction(ctx)
	defer done()

	for height := startFrom; ctx.Err() == nil; height++ {
		hash, err := store.heightIndex.HashByHeight(ctx, height, false)
		if errors.Is(err, datastore.ErrNotFound) {
			continue
		}
		if err != nil {
			var zero H
			return zero, err
		}

		ok, err := store.Has(ctx, hash)
		if ok {
			return store.Get(ctx, hash)
		}
	}

	var zero H
	return zero, ctx.Err()
}
