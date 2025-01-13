package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	heightSub *heightSub[H]

	// writing to datastore
	//
	writesMu sync.Mutex
	// writesPending keeps headers pending to be written in one batch
	writesPending *batch[H]
	// queue of batches to be written
	writesCh chan *batch[H]
	// signals when writes are finished
	writesDn chan struct{}

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
		ds:            wrappedStore,
		cache:         cache,
		metrics:       metrics,
		heightIndex:   index,
		heightSub:     newHeightSub[H](),
		writesCh:      make(chan *batch[H], 4),
		writesDn:      make(chan struct{}),
		writesPending: newBatch[H](params.WriteBatchSize),
		Params:        params,
	}, nil
}

func (s *Store[H]) Init(ctx context.Context, initial H) error {
	if s.heightSub.Height() != 0 {
		return errors.New("store already initialized")
	}
	// trust the given header as the initial head
	err := s.flush(ctx, initial)
	if err != nil {
		return err
	}

	log.Infow("initialized head", "height", initial.Height(), "hash", initial.Hash())
	s.heightSub.Pub(initial)
	return nil
}

// Start starts or restarts the Store.
func (s *Store[H]) Start(context.Context) error {
	// closed s.writesDn means that store was stopped before, recreate chan.
	select {
	case <-s.writesDn:
		s.writesCh = make(chan *batch[H], 4)
		s.writesDn = make(chan struct{})
		s.writesPending = newBatch[H](s.Params.WriteBatchSize)
	default:
	}

	go s.flushLoop()
	return nil
}

// Stop stops the store and cleans up resources.
// Canceling context while stopping may leave the store in an inconsistent state.
func (s *Store[H]) Stop(ctx context.Context) error {
	s.writesMu.Lock()
	defer s.writesMu.Unlock()
	// check if store was already stopped
	select {
	case <-s.writesDn:
		return errStoppedStore
	default:
	}
	// write the pending leftover
	select {
	case s.writesCh <- s.writesPending:
		// signal closing to flushLoop
		close(s.writesCh)
	case <-ctx.Done():
		return ctx.Err()
	}
	// wait till flushLoop is done writing
	select {
	case <-s.writesDn:
	case <-ctx.Done():
		return ctx.Err()
	}

	// cleanup caches
	s.cache.Purge()
	s.heightIndex.cache.Purge()
	return s.metrics.Close()
}

func (s *Store[H]) Height() uint64 {
	return s.heightSub.Height()
}

func (s *Store[H]) Head(ctx context.Context, _ ...header.HeadOption[H]) (H, error) {
	head, err := s.GetByHeight(ctx, s.heightSub.Height())
	if err == nil {
		return head, nil
	}

	var zero H
	head, err = s.readHead(ctx)
	switch {
	default:
		return zero, err
	case errors.Is(err, datastore.ErrNotFound), errors.Is(err, header.ErrNotFound):
		return zero, header.ErrNoHead
	case err == nil:
		s.heightSub.SetHeight(head.Height())
		log.Infow("loaded head", "height", head.Height(), "hash", head.Hash())
		return head, nil
	}
}

func (s *Store[H]) Get(ctx context.Context, hash header.Hash) (H, error) {
	var zero H
	if v, ok := s.cache.Get(hash.String()); ok {
		return v, nil
	}
	// check if the requested header is not yet written on disk
	if h := s.writesPending.Get(hash); !h.IsZero() {
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
	// if the requested 'height' was not yet published
	// we subscribe to it
	h, err := s.heightSub.Sub(ctx, height)
	if !errors.Is(err, errElapsedHeight) {
		return h, err
	}
	// otherwise, the errElapsedHeight is thrown,
	// which means the requested 'height' should be present
	//
	// check if the requested header is not yet written on disk
	// TODO: Synchronize with prepareWrite?
	if h := s.writesPending.GetByHeight(height); !h.IsZero() {
		return h, nil
	}

	hash, err := s.heightIndex.HashByHeight(ctx, height)
	if err != nil {
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
	if ok := s.writesPending.Has(hash); ok {
		return ok, nil
	}

	return s.ds.Has(ctx, datastore.NewKey(hash.String()))
}

func (s *Store[H]) HasAt(_ context.Context, height uint64) bool {
	return height != uint64(0) && s.Height() >= height
}

func (s *Store[H]) Append(ctx context.Context, headers ...H) error {
	lh := len(headers)
	if lh == 0 {
		return nil
	}

	// take current write head to verify headers against
	head, err := s.Head(ctx)
	if err != nil {
		return err
	}

	// collect valid headers
	verified := make([]H, 0, lh)
	for i, h := range headers {
		err = head.Verify(h)
		if err != nil {
			var verErr *header.VerifyError
			if errors.As(err, &verErr) {
				log.Errorw("invalid header",
					"height_of_head", head.Height(),
					"hash_of_head", head.Hash(),
					"height_of_invalid", h.Height(),
					"hash_of_invalid", h.Hash(),
					"reason", verErr.Reason)
			}
			// if the first header is invalid, no need to go further
			if i == 0 {
				// and simply return
				return err
			}
			// otherwise, stop the loop and apply headers appeared to be valid
			break
		}
		verified = append(verified, h)
		head = h
	}

	// prepare headers to be written
	toWrite, err := s.prepareWrite(verified)
	switch {
	case err != nil:
		return err
	case toWrite == nil:
		return nil
	}

	// queue headers to be written on disk
	select {
	case s.writesCh <- toWrite:
		// we return an error here after writing,
		// as there might be an invalid header in between of a given range
		return err
	default:
		s.metrics.writesQueueBlocked(ctx)
	}
	// if the writesCh queue is full - we block anyway
	select {
	case s.writesCh <- toWrite:
		return err
	case <-s.writesDn:
		return errStoppedStore
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store[H]) prepareWrite(headers []H) (*batch[H], error) {
	s.writesMu.Lock()
	defer s.writesMu.Unlock()
	// check if store was stopped
	select {
	case <-s.writesDn:
		return nil, errStoppedStore
	default:
	}

	// keep verified headers as pending writes and ensure they are accessible for reads
	s.writesPending.Append(headers...)
	// notify waiters if any
	// it is important to do Pub after updating pending
	// so pending is consistent with atomic Height counter on the heightSub
	s.heightSub.Pub(headers...)

	// TODO: Head advancing
	// announce our new head
	newHead := headers[len(headers)-1]
	s.metrics.newHead(newHead.Height())
	log.Infow("new head", "height", newHead.Height(), "hash", newHead.Hash())

	// don't flush and continue if pending write batch is not grown enough,
	if s.writesPending.Len() < s.Params.WriteBatchSize {
		return nil, nil
	}

	toWrite := s.writesPending
	s.writesPending = newBatch[H](s.Params.WriteBatchSize)
	return toWrite, nil
}

// flushLoop performs writing task to the underlying datastore in a separate routine
// This way writesCh are controlled and manageable from one place allowing
// (1) Appends not to be blocked on long disk IO writesCh and underlying DB compactions
// (2) Batching header writesCh
func (s *Store[H]) flushLoop() {
	defer close(s.writesDn)
	ctx := context.Background()

	for headers := range s.writesCh {
		startTime := time.Now()
		toFlush := headers.GetAll()

		for i := 0; ; i++ {
			err := s.flush(ctx, toFlush...)
			if err == nil {
				break
			}

			from, to := toFlush[0].Height(), toFlush[len(toFlush)-1].Height()
			log.Errorw("writing header batch", "try", i+1, "from", from, "to", to, "err", err)
			s.metrics.flush(ctx, time.Since(startTime), s.writesPending.Len(), true)

			const maxRetrySleep = time.Second
			sleep := min(10*time.Duration(i+1)*time.Millisecond, maxRetrySleep)
			time.Sleep(sleep)
		}

		s.metrics.flush(ctx, time.Since(startTime), s.writesPending.Len(), false)
		headers.Reset()
	}
}

// flush writesCh the given batch to datastore.
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
	b, err := headers[ln-1].Hash().MarshalJSON()
	if err != nil {
		return err
	}

	err = batch.Put(ctx, headKey, b)
	if err != nil {
		return err
	}

	// write height indexes for headers as well
	if err := indexTo(ctx, batch, headers...); err != nil {
		return err
	}

	// finally, commit the batch on disk
	return batch.Commit(ctx)
}

// readHead loads the head from the datastore.
func (s *Store[H]) readHead(ctx context.Context) (H, error) {
	var zero H
	b, err := s.ds.Get(ctx, headKey)
	if err != nil {
		return zero, err
	}

	var head header.Hash
	err = head.UnmarshalJSON(b)
	if err != nil {
		return zero, err
	}

	return s.Get(ctx, head)
}

func (s *Store[H]) get(ctx context.Context, hash header.Hash) ([]byte, error) {
	startTime := time.Now()
	data, err := s.ds.Get(ctx, datastore.NewKey(hash.String()))
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
