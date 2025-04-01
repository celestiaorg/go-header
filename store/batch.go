package store

import (
	"slices"
	"sync"

	"github.com/celestiaorg/go-header"
)

type batches[H header.Header[H]] struct {
	batchesMu sync.RWMutex
	batches   []*batch[H]

	batchLenLimit int
}

func newEmptyBatches[H header.Header[H]]() *batches[H] {
	return &batches[H]{batches: make([]*batch[H], 0, 8)}
}

// Append must take adjacent range of Headers.
// Returns one of the internal batches once it reaches the length limit
// with true.
func (bs *batches[H]) Append(headers ...H) (*batch[H], bool) {
	// TODO: Check if headers are adjacent?
	if len(headers) == 0 {
		return nil, false
	}
	bs.batchesMu.Lock()
	defer bs.batchesMu.Unlock()

	// 1. Add headers as a new batch
	newBatch := newBatch[H](len(headers))
	newBatch.Append(headers...)
	bs.batches = append(bs.batches, newBatch)

	// 2. Ensure all the batches are sorted in descending order
	slices.SortFunc(bs.batches, func(a, b *batch[H]) int {
		return int(b.Head() - a.Head())
	})

	// 3. Merge adjacent and overlapping batches
	mergeIdx := 0
	for idx := 1; idx < len(bs.batches); idx++ {
		curr := bs.batches[mergeIdx]
		next := bs.batches[idx]

		if !next.IsReadOnly() && curr.Tail()-1 <= next.Head() {
			curr.Append(next.GetAll()...)
		} else {
			mergeIdx++
			bs.batches[mergeIdx] = next
		}
	}
	clear(bs.batches[mergeIdx+1:])
	bs.batches = bs.batches[:mergeIdx+1]

	// 4. Mark filled batches as read only and return if any
	for i := len(bs.batches) - 1; i >= 0; i-- {
		// Why in reverse? There might be several batches
		// but only one is processed, so there needs to be prioritization
		// which in this case is for lower heights.
		b := bs.batches[i]
		if b.Len() >= bs.batchLenLimit {
			b.MarkReadOnly()
			return b, true
		}
	}

	return nil, false
}

func (bs *batches[H]) GetByHeight(height uint64) (H, error) {
	bs.batchesMu.RLock()
	defer bs.batchesMu.RUnlock()

	for _, b := range bs.batches {
		if height >= b.Tail() && height <= b.Head() {
			return b.GetByHeight(height)
		}
	}

	var zero H
	return zero, header.ErrNotFound
}

func (bs *batches[H]) Get(hash header.Hash) (H, error) {
	bs.batchesMu.RLock()
	defer bs.batchesMu.RUnlock()

	for _, b := range bs.batches {
		h, err := b.Get(hash)
		if err == nil {
			return h, nil
		}
	}

	var zero H
	return zero, header.ErrNotFound
}

func (bs *batches[H]) Has(hash header.Hash) bool {
	bs.batchesMu.RLock()
	defer bs.batchesMu.RUnlock()

	for _, b := range bs.batches {
		if b.Has(hash) {
			return true
		}
	}

	return false
}

// batch keeps a range of adjacent headers and loosely mimics the Store
// interface.
//
// It keeps a mapping 'height -> header' and 'hash -> height'
// unlike the Store which keeps 'hash -> header' and 'height -> hash'.
// The approach simplifies implementation for the batch and
// makes it better optimized for the GetByHeight case which is what we need.
type batch[H header.Header[H]] struct {
	heights map[string]uint64
	headers []H // in descending order

	readOnly bool
}

// newBatch creates the batch with the given pre-allocated size.
func newBatch[H header.Header[H]](size int) *batch[H] {
	return &batch[H]{
		heights: make(map[string]uint64, size),
		headers: make([]H, 0, size),
	}
}

func (b *batch[H]) MarkReadOnly() {
	b.readOnly = true
}

func (b *batch[H]) IsReadOnly() bool {
	return b.readOnly
}

func (b *batch[H]) Head() uint64 {
	if len(b.headers) == 0 {
		return 0
	}
	return b.headers[0].Height()
}

func (b *batch[H]) Tail() uint64 {
	if len(b.headers) == 0 {
		return 0
	}
	return b.headers[len(b.headers)-1].Height()
}

// Len gives current length of the batch.
func (b *batch[H]) Len() int {
	return len(b.headers)
}

// GetAll returns a slice of all the headers in the batch.
func (b *batch[H]) GetAll() []H {
	return b.headers
}

// Get returns a header by its hash.
func (b *batch[H]) Get(hash header.Hash) (H, error) {
	height, ok := b.heights[hash.String()]
	if !ok {
		var zero H
		return zero, header.ErrNotFound
	}

	return b.GetByHeight(height)
}

// GetByHeight returns a header by its height.
func (b *batch[H]) GetByHeight(height uint64) (H, error) {
	h := b.headers[b.Head()-height]
	if h.Height() != height {
		var zero H
		return zero, header.ErrNotFound
	}

	return h, nil
}

// Append appends new headers to the batch.
func (b *batch[H]) Append(headers ...H) {
	head, tail := b.Head(), b.Tail()
	for _, h := range headers {
		if h.Height() >= tail && h.Height() <= head {
			// overwrite if exists already
			b.headers[head-h.Height()] = h
		} else {
			// add new
			b.headers = append(b.headers, h)
			b.heights[h.Hash().String()] = h.Height()
		}
	}
}

// Has checks whether header by the hash is present in the batch.
func (b *batch[H]) Has(hash header.Hash) bool {
	_, ok := b.heights[hash.String()]
	return ok
}

// Reset cleans references to batched headers.
func (b *batch[H]) Reset() {
	b.headers = b.headers[:0]
	for k := range b.heights {
		delete(b.heights, k)
	}
}
