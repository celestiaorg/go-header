package store

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	logging "github.com/ipfs/go-log/v2"

	"github.com/celestiaorg/go-header"
)

var log = logging.Logger("header/store")

var (
	// defaultStorePrefix defines default datastore prefix
	defaultStorePrefix = datastore.NewKey("headers")
	// errStoppedStore is returned for attempted operations on a stopped store
	errStoppedStore = errors.New("stopped store")
)

// Store implements the Store interface for Headers over Datastore.
type Store[H header.Header[H]] struct {
	// header storing
	//
	// underlying KV store
	ds datastore.Batching
	// adaptive replacement cache of headers
	cache *lru.TwoQueueCache[string, H]
	// metrics collection instance
	metrics *metrics

	// header heights management
	//
	// maps heights to hashes
	heightIndex *heightIndexer[H]
	// manages current store read head height (1) and
	// allows callers to wait until header for a height is stored (2)
	heightSub *heightSub

	// writing to datastore
	//
	// queue of headers to be written
	writes chan []H
	// signals when writes are finished
	writesDn chan struct{}
	// tailHeader maintains the current tail header.
	tailHeader atomic.Pointer[H]
	// contiguousHead is the highest contiguous header observed
	contiguousHead atomic.Pointer[H]
	// pending keeps headers pending to be written in one batch
	pending *batch[H]

	Params Parameters
}

// NewStore constructs a Store over datastore.
// The datastore must have a head there otherwise Start will error.
// For first initialization of Store use NewStoreWithHead.
func NewStore[H header.Header[H]](ds datastore.Batching, opts ...Option) (*Store[H], error) {
	return newStore[H](ds, opts...)
}

func newStore[H header.Header[H]](ds datastore.Batching, opts ...Option) (*Store[H], error) {
	params := DefaultParameters()
	for _, opt := range opts {
		opt(&params)
	}

	if err := params.Validate(); err != nil {
		return nil, fmt.Errorf("header/store: store creation failed: %w", err)
	}

	cache, err := lru.New2Q[string, H](params.StoreCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create index cache: %w", err)
	}

	prefix := params.storePrefix
	if len(prefix.String()) == 0 {
		prefix = defaultStorePrefix
	}

	wrappedStore := namespace.Wrap(ds, prefix)
	index, err := newHeightIndexer[H](wrappedStore, params.IndexCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create height indexer: %w", err)
	}

	var metrics *metrics
	if params.metrics {
		metrics, err = newMetrics()
		if err != nil {
			return nil, err
		}
	}

	return &Store[H]{
		ds:          wrappedStore,
		cache:       cache,
		metrics:     metrics,
		heightIndex: index,
		heightSub:   newHeightSub(),
		writes:      make(chan []H, 16),
		writesDn:    make(chan struct{}),
		pending:     newBatch[H](params.WriteBatchSize),
		Params:      params,
	}, nil
}

func (s *Store[H]) initStore(ctx context.Context, initial H) error {
	// initialize with the initial head before first flush.
	s.contiguousHead.Store(&initial)
	s.heightSub.Init(initial.Height())
	s.tailHeader.Store(&initial)

	// trust the given header as the initial head
	err := s.flush(ctx, initial)
	if err != nil {
		return err
	}

	log.Infow("initialized head", "height", initial.Height(), "hash", initial.Hash())
	return nil
}

func (s *Store[H]) Start(ctx context.Context) error {
	// closed s.writesDn means that store was stopped before, recreate chan.
	select {
	case <-s.writesDn:
		s.writesDn = make(chan struct{})
	default:
	}

	if err := s.loadHeadAndTail(ctx); err != nil {
		return err
	}

	go s.flushLoop()
	return nil
}

func (s *Store[H]) Stop(ctx context.Context) error {
	select {
	case <-s.writesDn:
		return errStoppedStore
	default:
	}
	// signal to prevent further writes to Store
	select {
	case s.writes <- nil:
	case <-ctx.Done():
		return ctx.Err()
	}
	// wait till it is done writing
	select {
	case <-s.writesDn:
	case <-ctx.Done():
		return ctx.Err()
	}

	// cleanup caches
	s.cache.Purge()
	s.heightIndex.cache.Purge()
	s.contiguousHead.Store(nil)
	s.tailHeader.Store(nil)
	return s.metrics.Close()
}

func (s *Store[H]) Height() uint64 {
	return s.heightSub.Height()
}

func (s *Store[H]) Head(_ context.Context, _ ...header.HeadOption[H]) (H, error) {
	if head := s.contiguousHead.Load(); head != nil {
		return *head, nil
	}

	var zero H
	return zero, header.ErrEmptyStore
}

// Tail implements [header.Store] interface.
func (s *Store[H]) Tail(_ context.Context) (H, error) {
	tailPtr := s.tailHeader.Load()
	if tailPtr != nil {
		return *tailPtr, nil
	}

	var zero H
	return zero, header.ErrEmptyStore
}

func (s *Store[H]) Get(ctx context.Context, hash header.Hash) (H, error) {
	var zero H
	if v, ok := s.cache.Get(hash.String()); ok {
		return v, nil
	}
	// check if the requested header is not yet written on disk
	if h := s.pending.Get(hash); !h.IsZero() {
		return h, nil
	}

	b, err := s.get(ctx, hash)
	if err != nil {
		return zero, err
	}

	h := header.New[H]()
	err = h.UnmarshalBinary(b)
	if err != nil {
		return zero, err
	}

	s.cache.Add(h.Hash().String(), h)
	return h, nil
}

func (s *Store[H]) GetByHeight(ctx context.Context, height uint64) (H, error) {
	var zero H
	if height == 0 {
		return zero, errors.New("header/store: height must be bigger than zero")
	}

	if h, err := s.getByHeight(ctx, height); err == nil {
		return h, nil
	}

	// if the requested 'height' was not yet published
	// we subscribe to it
	err := s.heightSub.Wait(ctx, height)
	if err != nil && !errors.Is(err, errElapsedHeight) {
		return zero, err
	}
	// otherwise, the errElapsedHeight is thrown,
	// which means the requested 'height' should be present
	//
	// check if the requested header is not yet written on disk

	return s.getByHeight(ctx, height)
}

func (s *Store[H]) getByHeight(ctx context.Context, height uint64) (H, error) {
	if h := s.pending.GetByHeight(height); !h.IsZero() {
		return h, nil
	}

	hash, err := s.heightIndex.HashByHeight(ctx, height)
	if err != nil {
		var zero H
		if errors.Is(err, datastore.ErrNotFound) {
			return zero, header.ErrNotFound
		}

		return zero, err
	}

	return s.Get(ctx, hash)
}

func (s *Store[H]) GetRangeByHeight(
	ctx context.Context,
	from H,
	to uint64,
) ([]H, error) {
	headers, err := s.getRangeByHeight(ctx, from.Height()+1, to)
	if err != nil {
		return nil, err
	}
	return headers, nil
}

func (s *Store[H]) GetRange(ctx context.Context, from, to uint64) ([]H, error) {
	return s.getRangeByHeight(ctx, from, to)
}

func (s *Store[H]) getRangeByHeight(ctx context.Context, from, to uint64) ([]H, error) {
	if from >= to {
		return nil, fmt.Errorf("header/store: invalid range(%d,%d)", from, to)
	}
	h, err := s.GetByHeight(ctx, to-1)
	if err != nil {
		return nil, err
	}

	ln := to - from
	headers := make([]H, ln)
	for i := ln - 1; i > 0; i-- {
		headers[i] = h
		h, err = s.Get(ctx, h.LastHeader())
		if err != nil {
			return nil, err
		}
	}
	headers[0] = h

	return headers, nil
}

func (s *Store[H]) Has(ctx context.Context, hash header.Hash) (bool, error) {
	if ok := s.cache.Contains(hash.String()); ok {
		return ok, nil
	}
	// check if the requested header is not yet written on disk
	if ok := s.pending.Has(hash); ok {
		return ok, nil
	}

	ok, err := s.ds.Has(ctx, hashKey(hash))
	if errors.Is(err, datastore.ErrNotFound) {
		return false, header.ErrNotFound
	}
	return ok, err
}

func (s *Store[H]) HasAt(_ context.Context, height uint64) bool {
	return height != uint64(0) && s.Height() >= height
}

// DeleteTo implements [header.Store] interface.
func (s *Store[H]) DeleteTo(ctx context.Context, to uint64) error {
	var from uint64

	if tailPtr := s.tailHeader.Load(); tailPtr != nil {
		from = (*tailPtr).Height()
	}
	if from >= to {
		return nil
	}
	if headPtr := s.contiguousHead.Load(); headPtr != nil {
		if height := (*headPtr).Height(); to > height {
			return fmt.Errorf("header/store: higher then head (%d vs %d)", to, height)
		}
	}

	if from >= to {
		log.Debugw("header/store: attempt to delete empty range(%d, %d)", from, to)
		return nil
	}

	if err := s.deleteRange(ctx, from, to); err != nil {
		return fmt.Errorf("header/store: delete to height %d: %w", to, err)
	}
	return nil
}

func (s *Store[H]) deleteRange(ctx context.Context, from, to uint64) error {
	batch, err := s.ds.Batch(ctx)
	if err != nil {
		return fmt.Errorf("delete batch: %w", err)
	}

	if err := s.prepareDeleteRangeBatch(ctx, batch, from, to); err != nil {
		return fmt.Errorf("prepare: %w", err)
	}

	if err := s.heightIndex.deleteRange(ctx, batch, from, to); err != nil {
		return fmt.Errorf("height index: %w", err)
	}

	newTail, err := s.updateTail(ctx, batch, to)
	if err != nil {
		return fmt.Errorf("update tail: %w", err)
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("delete commit: %w", err)
	}

	s.tailHeader.Store(&newTail)
	return nil
}

func (s *Store[H]) prepareDeleteRangeBatch(
	ctx context.Context, batch datastore.Batch, from, to uint64,
) error {
	for h := from; h < to; h++ {
		hash, err := s.heightIndex.HashByHeight(ctx, h)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				log.Errorw("removing non-existent header", "height", h)
				continue
			}
			return fmt.Errorf("hash by height(%d): %w", h, err)
		}
		s.cache.Remove(hash.String())

		if err := batch.Delete(ctx, hashKey(hash)); err != nil {
			return fmt.Errorf("delete hash key: %w", err)
		}
	}

	s.pending.DeleteRange(from, to)
	return nil
}

func (s *Store[H]) updateTail(
	ctx context.Context, batch datastore.Batch, to uint64,
) (H, error) {
	var zero H

	newTail, err := s.getByHeight(ctx, to)
	if err != nil {
		if !errors.Is(err, header.ErrNotFound) {
			return zero, fmt.Errorf("cannot fetch next tail: %w", err)
		}
		return zero, err
	}

	b, err := newTail.Hash().MarshalJSON()
	if err != nil {
		return zero, err
	}
	if err := batch.Put(ctx, tailKey, b); err != nil {
		return zero, err
	}
	return newTail, nil
}

func (s *Store[H]) Append(ctx context.Context, headers ...H) error {
	lh := len(headers)
	if lh == 0 {
		return nil
	}

	// TODO(cristaloleg): remove before merge
	if s.contiguousHead.Load() == nil || s.tailHeader.Load() == nil {
		if err := s.initStore(ctx, headers[0]); err != nil {
			return fmt.Errorf("header/store: init store: %w", err)
		}
	}

	// queue headers to be written on disk
	select {
	case s.writes <- headers:
		// we return an error here after writing,
		// as there might be an invalid header in between of a given range
		return nil
	default:
		s.metrics.writesQueueBlocked(ctx)
	}
	// if the writes queue is full, we block until it is not
	select {
	case s.writes <- headers:
		return nil
	case <-s.writesDn:
		return errStoppedStore
	case <-ctx.Done():
		return ctx.Err()
	}
}

// flushLoop performs writing task to the underlying datastore in a separate routine
// This way writes are controlled and manageable from one place allowing
// (1) Appends not to be blocked on long disk IO writes and underlying DB compactions
// (2) Batching header writes
func (s *Store[H]) flushLoop() {
	defer close(s.writesDn)
	ctx := context.Background()
	for headers := range s.writes {
		// add headers to the pending and ensure they are accessible
		s.pending.Append(headers...)

		if len(headers) > 0 && s.tailHeader.Load() == nil {
			firstHeader := headers[0]
			if err := s.setTailHeader(firstHeader); err != nil {
				log.Errorw("set tail header",
					"height_of_header", firstHeader.Height(),
					"hash_of_header", firstHeader.Hash(),
					"error", err,
				)
			}
		}

		// always inform heightSub about new headers seen.
		s.heightSub.Notify(getHeights(headers...)...)
		// advance contiguousHead if we don't have gaps.
		s.advanceContiguousHead(ctx, s.heightSub.Height())
		// don't flush and continue if pending batch is not grown enough,
		// and Store is not stopping(headers == nil)
		if s.pending.Len() < s.Params.WriteBatchSize && headers != nil {
			continue
		}

		startTime := time.Now()
		toFlush := s.pending.GetAll()

		for i := 0; ; i++ {
			err := s.flush(ctx, toFlush...)
			if err == nil {
				break
			}

			from, to := toFlush[0].Height(), toFlush[len(toFlush)-1].Height()
			log.Errorw("writing header batch", "try", i+1, "from", from, "to", to, "err", err)
			s.metrics.flush(ctx, time.Since(startTime), s.pending.Len(), true)

			const maxRetrySleep = time.Second
			sleep := min(10*time.Duration(i+1)*time.Millisecond, maxRetrySleep)
			time.Sleep(sleep)
		}

		s.metrics.flush(ctx, time.Since(startTime), s.pending.Len(), false)
		// reset pending
		s.pending.Reset()

		if headers == nil {
			// a signal to stop
			return
		}
	}
}

// flush writes the given batch to datastore.
func (s *Store[H]) flush(ctx context.Context, headers ...H) error {
	ln := len(headers)
	if ln == 0 {
		return nil
	}

	batch, err := s.ds.Batch(ctx)
	if err != nil {
		return err
	}

	// collect all the headers in the batch to be written
	for _, h := range headers {
		b, err := h.MarshalBinary()
		if err != nil {
			return err
		}

		err = batch.Put(ctx, headerKey(h), b)
		if err != nil {
			return err
		}
	}

	// marshal and add to batch reference to the new head
	head := *s.contiguousHead.Load()
	b, err := head.Hash().MarshalJSON()
	if err != nil {
		return err
	}

	if err := batch.Put(ctx, headKey, b); err != nil {
		return err
	}

	// write height indexes for headers as well
	if err := indexTo(ctx, batch, headers...); err != nil {
		return err
	}

	// finally, commit the batch on disk
	return batch.Commit(ctx)
}

// readByKey the hash under the given key from datastore and fetch the header by hash.
func (s *Store[H]) readByKey(ctx context.Context, key datastore.Key) (H, error) {
	var zero H
	b, err := s.ds.Get(ctx, key)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return zero, header.ErrNotFound
		}
		return zero, err
	}

	var h header.Hash
	if err := h.UnmarshalJSON(b); err != nil {
		return zero, err
	}

	return s.Get(ctx, h)
}

// readTail loads the tail from the datastore.
func (s *Store[H]) readTail(ctx context.Context) (H, error) {
	var zero H
	b, err := s.ds.Get(ctx, tailKey)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return zero, header.ErrNotFound
		}
		return zero, err
	}

	var tail header.Hash
	if err := tail.UnmarshalJSON(b); err != nil {
		return zero, err
	}

	return s.Get(ctx, tail)
}

func (s *Store[H]) get(ctx context.Context, hash header.Hash) ([]byte, error) {
	startTime := time.Now()
	data, err := s.ds.Get(ctx, hashKey(hash))
	if err != nil {
		s.metrics.readSingle(ctx, time.Since(startTime), true)
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, header.ErrNotFound
		}
		return nil, err
	}

	s.metrics.readSingle(ctx, time.Since(startTime), false)
	return data, nil
}

// advanceContiguousHead updates contiguousHead and heightSub if a higher
// contiguous header exists on a disk.
func (s *Store[H]) advanceContiguousHead(ctx context.Context, height uint64) {
	newHead := s.nextContiguousHead(ctx, height)
	if newHead.IsZero() || newHead.Height() <= height {
		return
	}

	s.contiguousHead.Store(&newHead)
	s.heightSub.SetHeight(newHead.Height())
	log.Infow("new head", "height", newHead.Height(), "hash", newHead.Hash())
	s.metrics.newHead(newHead.Height())
}

// nextContiguousHead iterates up header by header until it finds a gap.
// if height+1 header not found returns a default header.
func (s *Store[H]) nextContiguousHead(ctx context.Context, height uint64) H {
	var newHead H
	for {
		height++
		h, err := s.getByHeight(ctx, height)
		if err != nil {
			break
		}
		newHead = h
	}
	return newHead
}

func (s *Store[H]) loadHeadAndTail(ctx context.Context) error {
	{
		head, err := s.readByKey(ctx, headKey)
		if err != nil {
			if !errors.Is(err, datastore.ErrNotFound) {
				return fmt.Errorf("header/store: cannot load headKey: %w", err)
			}
		} else {
			s.contiguousHead.Store(&head)
			s.heightSub.Init(head.Height())
		}
	}

	{
		tail, err := s.readByKey(ctx, tailKey)
		if err != nil {
			if !errors.Is(err, datastore.ErrNotFound) {
				return fmt.Errorf("header/store: cannot load tailKey: %w", err)
			}
		} else {
			s.tailHeader.Store(&tail)
		}
	}

	return nil
}

// indexTo saves mapping between header Height and Hash to the given batch.
func indexTo[H header.Header[H]](ctx context.Context, batch datastore.Batch, headers ...H) error {
	for _, h := range headers {
		err := batch.Put(ctx, heightKey(h.Height()), h.Hash())
		if err != nil {
			return err
		}
	}
	return nil
}

func getHeights[H header.Header[H]](headers ...H) []uint64 {
	heights := make([]uint64, len(headers))
	for i := range headers {
		heights[i] = headers[i].Height()
	}
	return heights
}
