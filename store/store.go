package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	logging "github.com/ipfs/go-log/v2"
	"go.uber.org/zap/zapcore"

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
	// syncCh is a channel used to synchronize writes
	syncCh chan chan struct{}
	cancel context.CancelFunc

	onDeleteMu sync.Mutex
	onDelete   []func(context.Context, uint64) error

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
		syncCh:      make(chan chan struct{}),
		Params:      params,
	}, nil
}

func (s *Store[H]) Start(ctx context.Context) error {
	// closed s.writesDn means that store was stopped before, recreate chan.
	select {
	case <-s.writesDn:
		s.writesDn = make(chan struct{})
	default:
	}

	if err := s.init(ctx); err != nil {
		return fmt.Errorf("header/store: initializing: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.flushLoop(ctx)
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
		s.cancel()
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
	s.deinit()
	return s.metrics.Close()
}

// Sync ensures all pending writes are synchronized. It blocks until the operation completes or fails.
func (s *Store[H]) Sync(ctx context.Context) error {
	waitCh := make(chan struct{})
	select {
	case s.syncCh <- waitCh:
	case <-s.writesDn:
		return errStoppedStore
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-waitCh:
	case <-s.writesDn:
		return errStoppedStore
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

func (s *Store[H]) Height() uint64 {
	return s.heightSub.Height()
}

func (s *Store[H]) Head(_ context.Context, _ ...header.HeadOption[H]) (H, error) {
	head := s.contiguousHead.Load()
	if head == nil {
		var zero H
		return zero, header.ErrEmptyStore
	}

	return *head, nil
}

// Tail implements [header.Store] interface.
func (s *Store[H]) Tail(_ context.Context) (H, error) {
	tail := s.tailHeader.Load()
	if tail == nil {
		var zero H
		return zero, header.ErrEmptyStore
	}

	return *tail, nil
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
	if err := h.UnmarshalBinary(b); err != nil {
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
		return zero, fmt.Errorf("awaiting header %d with head %d: %w", height, s.Height(), err)
	}
	// otherwise, the errElapsedHeight is thrown,
	// which means the requested 'height' should be present
	//
	// check if the requested header is not yet written on disk

	return s.getByHeight(ctx, height)
}

func (s *Store[H]) getByHeight(ctx context.Context, height uint64) (H, error) {
	head, _ := s.Head(ctx)
	if !head.IsZero() && head.Height() == height {
		return head, nil
	}

	tail, _ := s.Tail(ctx)
	if !tail.IsZero() && tail.Height() == height {
		return tail, nil
	}

	if h := s.pending.GetByHeight(height); !h.IsZero() {
		return h, nil
	}

	hash, err := s.heightIndex.HashByHeight(ctx, height, true)
	if err != nil {
		var zero H
		if errors.Is(err, datastore.ErrNotFound) {
			return zero, fmt.Errorf("height %d: %w", height, header.ErrNotFound)
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

func (s *Store[H]) HasAt(ctx context.Context, height uint64) bool {
	if height == uint64(0) {
		return false
	}

	head, err := s.Head(ctx)
	if err != nil {
		return false
	}

	tail, err := s.Tail(ctx)
	if err != nil {
		return false
	}

	return head.Height() >= height && height >= tail.Height()
}

func (s *Store[H]) setTail(ctx context.Context, write datastore.Write, to uint64) error {
	newTail, err := s.getByHeight(ctx, to)
	if err != nil {
		return fmt.Errorf("getting tail: %w", err)
	}

	// set directly to `to`, avoiding iteration in recedeTail
	s.tailHeader.Store(&newTail)
	if err := writeHeaderHashTo(ctx, write, newTail, tailKey); err != nil {
		return fmt.Errorf("writing tailKey in batch: %w", err)
	}
	log.Infow("new tail", "height", newTail.Height(), "hash", newTail.Hash())
	s.metrics.newTail(newTail.Height())

	// update head as well, if delete went over it
	head, _ := s.Head(ctx)
	if head.IsZero() || to > head.Height() {
		if err := writeHeaderHashTo(ctx, write, newTail, headKey); err != nil {
			return fmt.Errorf("writing headKey in batch: %w", err)
		}
		s.contiguousHead.Store(&newTail)
		s.advanceHead(ctx)
	}
	return nil
}

func (s *Store[H]) wipe(ctx context.Context) (rerr error) {
	// TODO(@Wondertan): calling deinit here is racy, but not critical
	//  Will be eventually fixed by
	//  https://github.com/celestiaorg/go-header/issues/263
	s.deinit()

	if err := s.ds.Delete(ctx, headKey); err != nil {
		rerr = errors.Join(rerr, fmt.Errorf("deleting headKey DB pointer: %w", err))
	}

	if err := s.ds.Delete(ctx, tailKey); err != nil {
		rerr = errors.Join(rerr, fmt.Errorf("deleting tailKey DB pointer: %w", err))
	}

	return rerr
}

func (s *Store[H]) Append(ctx context.Context, headers ...H) error {
	lh := len(headers)
	if lh == 0 {
		return nil
	}

	// queue headers to be written on disk
	select {
	case s.writes <- headers:
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
func (s *Store[H]) flushLoop(ctx context.Context) {
	defer close(s.writesDn)

	flush := func(headers []H) {
		s.ensureInit(headers)
		// add headers to the pending and ensure they are accessible
		s.pending.Append(headers...)
		// always inform heightSub about new headers seen.
		s.heightSub.Notify(getHeights(headers...)...)
		// advance head and tail if we don't have gaps.
		// TODO(@Wondertan): Beware of the performance penalty of this approach, which always makes a at least one
		// datastore lookup for both Tail and Head.
		s.advanceHead(ctx)
		s.recedeTail(ctx)
		// don't flush and continue if pending batch is not grown enough,
		// and Store is not stopping(headers == nil)
		if s.pending.Len() < s.Params.WriteBatchSize && headers != nil {
			return
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
	}

	for {
		select {
		case dn := <-s.syncCh:
			for {
				select {
				case headers := <-s.writes:
					flush(headers)
					if headers == nil {
						// a signal to stop
						return
					}
					continue
				default:
				}

				close(dn)
				break
			}
		case headers := <-s.writes:
			flush(headers)
			if headers == nil {
				// a signal to stop
				return
			}
		}
	}
}

// flush writes given headers to datastore
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

	// marshal and add to batch reference to the new head and tail
	head := *s.contiguousHead.Load()
	if err := writeHeaderHashTo(ctx, batch, head, headKey); err != nil {
		return err
	}
	tail := *s.tailHeader.Load()
	if err := writeHeaderHashTo(ctx, batch, tail, tailKey); err != nil {
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
		return zero, fmt.Errorf("unmarshaling header hash at %s key: %w", key, err)
	}

	hdr, err := s.Get(ctx, h)
	if err != nil {
		if errors.Is(err, header.ErrNotFound) {
			derr := s.ds.Delete(ctx, key)
			if derr != nil {
				err = errors.Join(
					err,
					fmt.Errorf("deleting key %s, header for which was not found: %w", key, derr),
				)
			}
		}
		return zero, err
	}

	return hdr, nil
}

func (s *Store[H]) get(ctx context.Context, hash header.Hash) ([]byte, error) {
	startTime := time.Now()
	data, err := s.ds.Get(ctx, hashKey(hash))
	if err != nil {
		s.metrics.readSingle(ctx, time.Since(startTime), true)
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, fmt.Errorf("hash %X: %w", hash, header.ErrNotFound)
		}
		return nil, err
	}

	s.metrics.readSingle(ctx, time.Since(startTime), false)
	return data, nil
}

// advanceHead moves contiguous Head forward if a newer one exists.
// It looks throw caches, pending headers and datastore
func (s *Store[H]) advanceHead(ctx context.Context) {
	newHead, changed := s.nextHead(ctx)
	if changed {
		s.contiguousHead.Store(&newHead)
		s.heightSub.SetHeight(newHead.Height())
		log.Infow("new head", "height", newHead.Height(), "hash", newHead.Hash())
		s.metrics.newHead(newHead.Height())
	}
}

// recedeTail moves contiguous Tail back if an older one exists.
// It looks throw caches, pending headers and datastore.
func (s *Store[H]) recedeTail(ctx context.Context) {
	newTail, changed := s.nextTail(ctx)
	if changed {
		s.tailHeader.Store(&newTail)
		log.Infow("new tail", "height", newTail.Height(), "hash", newTail.Hash())
		s.metrics.newTail(newTail.Height())
	}
}

// nextHead finds the new contiguous Head by iterating the current Head up until the newer height Head is found.
// Returns true if the newer one was found.
func (s *Store[H]) nextHead(ctx context.Context) (head H, changed bool) {
	head, err := s.Head(ctx)
	if err != nil {
		log.Errorw("cannot load head while advancing", "err", err)
		return head, false
	}

	for ctx.Err() == nil {
		h, err := s.getByHeight(ctx, head.Height()+1)
		if err != nil {
			log.Debugw("next head error", "current", head.Height(), "err", err)
			return head, changed
		}

		if !changed && log.Level() == zapcore.DebugLevel {
			now := time.Now()
			log.Debugw("advancing head", "start_height", head.Height())
			defer func() {
				log.Debugw(
					"finished advancing head",
					"end_height",
					head.Height(),
					"took(s)",
					time.Since(now),
				)
			}()
		}

		head = h
		changed = true
	}

	return head, changed
}

// nextTail finds the new contiguous Tail by iterating the current Tail down until the older height Tail is found.
// Returns true if the older one was found.
func (s *Store[H]) nextTail(ctx context.Context) (tail H, changed bool) {
	tail, err := s.Tail(ctx)
	if err != nil {
		log.Errorw("cannot load tail while receding", "err", err)
		return tail, false
	}

	for ctx.Err() == nil {
		h, err := s.getByHeight(ctx, tail.Height()-1)
		if err != nil {
			return tail, changed
		}

		if !changed && log.Level() == zapcore.DebugLevel {
			now := time.Now()
			log.Debugw("receding tail", "start_height", tail.Height())
			defer func() {
				log.Debugw(
					"finished receding tail",
					"end_height",
					tail.Height(),
					"took(s)",
					time.Since(now),
				)
			}()
		}

		tail = h
		changed = true
	}

	return tail, changed
}

// init loads the head and tail headers and sets them on the store.
// allows partial initialization of either tail or head if one of the is not found.
func (s *Store[H]) init(ctx context.Context) error {
	head, err := s.readByKey(ctx, headKey)
	if err != nil && !errors.Is(err, header.ErrNotFound) {
		return fmt.Errorf("reading headKey: %w", err)
	}
	if !head.IsZero() {
		s.contiguousHead.Store(&head)
		s.heightSub.Init(head.Height())
		log.Debugw("initialized head", "height", head.Height())
	}

	tail, err := s.readByKey(ctx, tailKey)
	if err != nil && !errors.Is(err, header.ErrNotFound) {
		return fmt.Errorf("reading tailKey: %w", err)
	}
	if !tail.IsZero() {
		s.tailHeader.Store(&tail)
		log.Debugw("initialized tail", "height", tail.Height())
	}

	return nil
}

// ensureInit initializes the store with the given headers if it is not already initialized.
func (s *Store[H]) ensureInit(headers []H) {
	if len(headers) == 0 {
		return
	}

	if headPtr := s.contiguousHead.Load(); headPtr == nil {
		head := headers[len(headers)-1]
		if s.contiguousHead.CompareAndSwap(headPtr, &head) {
			s.heightSub.Init(head.Height())
			log.Debugw("initialized head", "height", head.Height())
		}
	}

	if tailPtr := s.tailHeader.Load(); tailPtr == nil {
		tail := headers[0]
		s.tailHeader.CompareAndSwap(tailPtr, &tail)
		log.Debugw("initialized tail", "height", tail.Height())
	}
}

// deinit deinitializes the store.
func (s *Store[H]) deinit() {
	s.cache.Purge()
	s.heightIndex.cache.Purge()
	s.contiguousHead.Store(nil)
	s.tailHeader.Store(nil)
	s.heightSub.SetHeight(0)
}

func writeHeaderHashTo[H header.Header[H]](
	ctx context.Context,
	write datastore.Write,
	h H,
	key datastore.Key,
) error {
	hashBytes, err := h.Hash().MarshalJSON()
	if err != nil {
		return err
	}

	if err := write.Put(ctx, key, hashBytes); err != nil {
		return err
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
