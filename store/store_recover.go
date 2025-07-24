package store

import (
	"context"

	"github.com/celestiaorg/go-header"
)

// ResetTail resets the tail of the store to be at the given height.
// The new tail must be present in the store.
// WARNING: Only use this function if you know what you are doing.
func ResetTail[H header.Header[H]](ctx context.Context, store *Store[H], height uint64) error {
	if err := store.setTail(ctx, store.ds, height); err != nil {
		return err
	}

	return nil
}

// ResetHead resets the head of the store to be at the given height.
// The new head must be present in the store.
// WARNING: Only use this function if you know what you are doing.
func ResetHead[H header.Header[H]](ctx context.Context, store *Store[H], height uint64) error {
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
	for height := startFrom; ctx.Err() == nil; height++ {
		header, err := store.getByHeight(ctx, height)
		if err == nil {
			return header, nil
		}
	}

	var zero H
	return zero, ctx.Err()
}
