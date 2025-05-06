package sync

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/celestiaorg/go-header"
)

// errNonAdjacent is returned when syncer is appended with a header not adjacent to the stored head.
type errNonAdjacent struct {
	Head      uint64
	Attempted uint64
}

func (ena *errNonAdjacent) Error() string {
	return fmt.Sprintf("sync: non-adjacent: head %d, attempted %d", ena.Head, ena.Attempted)
}

// syncStore is a Store wrapper that provides synchronization over writes and reads
// for Head of underlying Store. Useful for Stores that do not guarantee synchrony between Append
// and Head method.
type syncStore[H header.Header[H]] struct {
	header.Store[H]

	head atomic.Pointer[H]
}

func (s *syncStore[H]) Head(ctx context.Context) (H, error) {
	if headPtr := s.head.Load(); headPtr != nil {
		return *headPtr, nil
	}

	storeHead, err := s.Store.Head(ctx)
	if err != nil {
		return storeHead, err
	}

	s.head.Store(&storeHead)
	return storeHead, nil
}

func (s *syncStore[H]) Append(ctx context.Context, headers ...H) error {
	if len(headers) == 0 {
		return nil
	}

	head, err := s.Head(ctx)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, header.ErrEmptyStore) {
		return err
	}

	if !errors.Is(err, header.ErrEmptyStore) {
		for _, h := range headers {
			if h.Height() != head.Height()+1 {
				return &errNonAdjacent{
					Head:      head.Height(),
					Attempted: h.Height(),
				}
			}
			head = h
		}
	}

	if err := s.Store.Append(ctx, headers...); err != nil {
		return err
	}

	s.head.Store(&headers[len(headers)-1])
	return nil
}
