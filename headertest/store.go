package headertest

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/celestiaorg/go-header"
)

type Generator[H header.Header[H]] interface {
	NextHeader() H
}

type Store[H header.Header[H]] struct {
	Headers    map[uint64]H
	HeadHeight uint64
	TailHeight uint64
}

// NewDummyStore creates a store for DummyHeader.
func NewDummyStore(t *testing.T) *Store[*DummyHeader] {
	return NewStore[*DummyHeader](t, NewTestSuite(t), 10)
}

// NewStore creates a generic mock store supporting different type of Headers based on Generator.
func NewStore[H header.Header[H]](_ *testing.T, gen Generator[H], numHeaders int) *Store[H] {
	store := &Store[H]{
		Headers:    make(map[uint64]H),
		HeadHeight: 1,
		TailHeight: 1,
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

func (m *Store[H]) Height() uint64 {
	return m.HeadHeight
}

func (m *Store[H]) Head(context.Context, ...header.HeadOption[H]) (H, error) {
	return m.Headers[m.HeadHeight], nil
}

func (m *Store[H]) Tail(context.Context) (H, error) {
	tail, ok := m.Headers[m.TailHeight]
	if !ok {
		var zero H
		return zero, header.ErrNotFound
	}
	return tail, nil
}

func (m *Store[H]) Get(_ context.Context, hash header.Hash) (H, error) {
	for _, header := range m.Headers {
		if bytes.Equal(header.Hash(), hash) {
			return header, nil
		}
	}
	var zero H
	return zero, header.ErrNotFound
}

func (m *Store[H]) GetByHeight(_ context.Context, height uint64) (H, error) {
	if header, exists := m.Headers[height]; exists {
		return header, nil
	}
	var zero H
	return zero, header.ErrNotFound
}

func (m *Store[H]) DeleteTo(_ context.Context, to uint64) error {
	for h := m.TailHeight; h < to; h++ {
		delete(m.Headers, h)
	}

	m.TailHeight = to
	return nil
}

func (m *Store[H]) GetRange(ctx context.Context, from, to uint64) ([]H, error) {
	return m.getRangeByHeight(ctx, from, to)
}

// GetRangeByHeight returns headers in range [from; to).
func (m *Store[H]) GetRangeByHeight(ctx context.Context, fromHead H, to uint64) ([]H, error) {
	from := fromHead.Height() + 1
	return m.getRangeByHeight(ctx, from, to)
}

func (m *Store[H]) getRangeByHeight(_ context.Context, from, to uint64) ([]H, error) {
	if to <= from {
		return nil, fmt.Errorf("malformed range, from: %d, to: %d", from, to)
	}

	headers := make([]H, to-from)

	// As the requested range is [from; to),
	// check that (to-1) height in request is less than
	// the biggest header height in store.
	if to-1 > m.Height() {
		return nil, header.ErrNotFound
	}
	for i := range headers {
		headers[i] = m.Headers[from]
		from++
	}
	return headers, nil
}

func (m *Store[H]) Has(context.Context, header.Hash) (bool, error) {
	return false, nil
}

func (m *Store[H]) HasAt(_ context.Context, height uint64) bool {
	return height != 0 && m.HeadHeight >= height
}

func (m *Store[H]) Append(_ context.Context, headers ...H) error {
	for _, header := range headers {
		height := header.Height()
		m.Headers[height] = header
		if height > m.HeadHeight {
			m.HeadHeight = height
		}
		if height < m.TailHeight {
			m.TailHeight = height
		}
	}
	return nil
}
