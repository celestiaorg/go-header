package store

import (
	"context"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ipfs/go-datastore"

	"github.com/celestiaorg/go-header"
)

// TODO(@Wondertan): There should be a more clever way to index heights, than just storing
// HeightToHash pair... heightIndexer simply stores and cashes mappings between header Height and
// Hash.
type heightIndexer[H header.Header[H]] struct {
	ds    datastore.Batching
	cache *lru.TwoQueueCache[uint64, header.Hash]
}

// newHeightIndexer creates new heightIndexer.
func newHeightIndexer[H header.Header[H]](
	ds datastore.Batching,
	indexCacheSize int,
) (*heightIndexer[H], error) {
	cache, err := lru.New2Q[uint64, header.Hash](indexCacheSize)
	if err != nil {
		return nil, err
	}

	return &heightIndexer[H]{
		ds:    ds,
		cache: cache,
	}, nil
}

// HashByHeight loads a header hash corresponding to the given height.
func (hi *heightIndexer[H]) HashByHeight(ctx context.Context, h uint64) (header.Hash, error) {
	if v, ok := hi.cache.Get(h); ok {
		return v, nil
	}

	val, err := hi.ds.Get(ctx, heightKey(h))
	if err != nil {
		return nil, err
	}

	hi.cache.Add(h, header.Hash(val))
	return val, nil
}

// DeleteRange of height from index and store.
func (hi *heightIndexer[H]) deleteRange(
	ctx context.Context, batch datastore.Batch, from, to uint64,
) error {
	for h := from; h < to; h++ {
		if err := batch.Delete(ctx, heightKey(h)); err != nil {
			return err
		}
		hi.cache.Remove(h)
	}
	return nil
}
