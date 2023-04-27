package headertest

import (
	"bytes"
	"context"
	"testing"

	"github.com/celestiaorg/go-header"
)

type Generator[H header.Header] interface {
	NextHeader() H
}

type Store[H header.Header] struct {
	Headers    map[int64]H
	HeadHeight int64
}

// NewDummyStore creates a store for DummyHeader.
func NewDummyStore(t *testing.T) *Store[*DummyHeader] {
	return NewStore[*DummyHeader](t, NewTestSuite(t), 10)
}

// NewStore creates a generic mock store supporting different type of Headers based on Generator.
func NewStore[H header.Header](t *testing.T, gen Generator[H], numHeaders int) *Store[H] {
	store := &Store[H]{
		Headers:    make(map[int64]H),
		HeadHeight: 0,
	}

	for i := 0; i < numHeaders; i++ {
		header := gen.NextHeader()
		store.Headers[header.Height()] = header

		if header.Height() > store.HeadHeight {
			store.HeadHeight = header.Height()
		}
	}
	return store
}

func (m *Store[H]) Init(context.Context, H) error { return nil }
func (m *Store[H]) Start(context.Context) error   { return nil }
func (m *Store[H]) Stop(context.Context) error    { return nil }

func (m *Store[H]) Height() uint64 {
	return uint64(m.HeadHeight)
}

func (m *Store[H]) Head(context.Context, ...header.Option) (H, error) {
	return m.Headers[m.HeadHeight], nil
}

func (m *Store[H]) Get(ctx context.Context, hash header.Hash) (H, error) {
	for _, header := range m.Headers {
		if bytes.Equal(header.Hash(), hash) {
			return header, nil
		}
	}
	var zero H
	return zero, header.ErrNotFound
}

func (m *Store[H]) GetByHeight(ctx context.Context, height uint64) (H, error) {
	return m.Headers[int64(height)], nil
}

func (m *Store[H]) GetRangeByHeight(ctx context.Context, from, to uint64) ([]H, error) {
	headers := make([]H, to-from)
	// As the requested range is [from; to),
	// check that (to-1) height in request is less than
	// the biggest header height in store.
	if to-1 > m.Height() {
		return nil, header.ErrNotFound
	}
	for i := range headers {
		headers[i] = m.Headers[int64(from)]
		from++
	}
	return headers, nil
}

func (m *Store[H]) GetVerifiedRange(
	ctx context.Context,
	h H,
	to uint64,
) ([]H, error) {
	return m.GetRangeByHeight(ctx, uint64(h.Height())+1, to)
}

func (m *Store[H]) Has(context.Context, header.Hash) (bool, error) {
	return false, nil
}

func (m *Store[H]) HasAt(_ context.Context, height uint64) bool {
	return height != 0 && m.HeadHeight >= int64(height)
}

func (m *Store[H]) Append(ctx context.Context, headers ...H) error {
	for _, header := range headers {
		m.Headers[header.Height()] = header
		// set head
		if header.Height() > m.HeadHeight {
			m.HeadHeight = header.Height()
		}
	}
	return nil
}
