package store

import (
	"maps"
	"slices"
	"sync"

	"github.com/celestiaorg/go-header"
)

// batch keeps a range of headers and loosely mimics the Store
// interface. NOTE: Can fully implement Store for a use case.
//
// It keeps a mapping 'height -> header' and 'hash -> height'
// unlike the Store which keeps 'hash -> header' and 'height -> hash'.
// The approach simplifies implementation for the batch and
// makes it better optimized for the GetByHeight case which is what we need.
type batch[H header.Header[H]] struct {
	lk      sync.RWMutex
	heights map[string]uint64
	headers map[uint64]H
}

// newBatch creates the batch with the given pre-allocated size.
func newBatch[H header.Header[H]](size int) *batch[H] {
	return &batch[H]{
		heights: make(map[string]uint64, size),
		headers: make(map[uint64]H, size),
	}
}

// Len gives current length of the batch.
func (b *batch[H]) Len() int {
	b.lk.RLock()
	defer b.lk.RUnlock()
	return len(b.headers)
}

// GetAll returns a slice of all the headers in the batch.
func (b *batch[H]) GetAll() []H {
	b.lk.RLock()
	defer b.lk.RUnlock()
	return slices.Collect(maps.Values(b.headers))
}

// Get returns a header by its hash.
func (b *batch[H]) Get(hash header.Hash) H {
	b.lk.RLock()
	defer b.lk.RUnlock()
	height, ok := b.heights[hash.String()]
	if !ok {
		var zero H
		return zero
	}

	return b.headers[height]
}

// GetByHeight returns a header by its height.
func (b *batch[H]) GetByHeight(height uint64) H {
	b.lk.RLock()
	defer b.lk.RUnlock()
	return b.headers[height]
}

// Append appends new headers to the batch.
func (b *batch[H]) Append(headers ...H) {
	b.lk.Lock()
	defer b.lk.Unlock()
	for _, h := range headers {
		b.headers[h.Height()] = h
		b.heights[h.Hash().String()] = h.Height()
	}
}

// Has checks whether header by the hash is present in the batch.
func (b *batch[H]) Has(hash header.Hash) bool {
	b.lk.RLock()
	defer b.lk.RUnlock()
	_, ok := b.heights[hash.String()]
	return ok
}

// DeleteRange of headers from the batch [from:to).
func (b *batch[H]) DeleteRange(from, to uint64) {
	b.lk.Lock()
	defer b.lk.Unlock()

	maps.DeleteFunc(b.heights, func(_ string, height uint64) bool {
		return from <= height && height < to
	})
	maps.DeleteFunc(b.headers, func(height uint64, _ H) bool {
		return from <= height && height < to
	})
}

// Reset cleans references to batched headers.
func (b *batch[H]) Reset() {
	b.lk.Lock()
	defer b.lk.Unlock()
	for k := range b.heights {
		delete(b.heights, k)
	}
	for k := range b.headers {
		delete(b.headers, k)
	}
}
