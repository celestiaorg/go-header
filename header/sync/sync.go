package sync

import (
	"context"
	"errors"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	tmbytes "github.com/tendermint/tendermint/libs/bytes"

	"github.com/celestiaorg/celestia-node/header"
)

var log = logging.Logger("header/sync")

// Syncer implements efficient synchronization for headers.
//
// Subjective header - the latest known header that is not expired (within trusting period)
// Network header - the latest header received from the network
//
// There are two main processes running in Syncer:
// 1. Main syncing loop(s.syncLoop)
//   - Performs syncing from the subjective(local chain view) header up to the latest known trusted header
//   - Syncs by requesting missing headers from Exchange or
//   - By accessing cache of pending and verified headers
//
// 2. Receives new headers from PubSub subnetwork (s.processIncoming)
//   - Usually, a new header is adjacent to the trusted head and if so, it is simply appended to the local store,
//     incrementing the subjective height and making it the new latest known trusted header.
//   - Or, if it receives a header further in the future,
//     verifies against the latest known trusted header
//     adds the header to pending cache(making it the latest known trusted header)
//     and triggers syncing loop to catch up to that point.
type Syncer struct {
	sub      header.Subscriber
	exchange header.Exchange
	store    header.Store

	// blockTime provides a reference point for the Syncer to determine
	// whether its subjective head is outdated
	blockTime time.Duration

	// stateLk protects state which represents the current or latest sync
	stateLk sync.RWMutex
	state   State
	// signals to start syncing
	triggerSync chan struct{}
	// pending keeps ranges of valid new network headers awaiting to be appended to store
	pending ranges
	// netReqLk ensures only one network head is requested at any moment
	netReqLk sync.RWMutex

	// controls lifecycle for syncLoop
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSyncer creates a new instance of Syncer.
func NewSyncer(exchange header.Exchange, store header.Store, sub header.Subscriber, blockTime time.Duration) *Syncer {
	return &Syncer{
		sub:         sub,
		exchange:    exchange,
		store:       store,
		blockTime:   blockTime,
		triggerSync: make(chan struct{}, 1), // should be buffered
	}
}

// Start starts the syncing routine.
func (s *Syncer) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	// register validator for header subscriptions
	// syncer does not subscribe itself and syncs headers together with validation
	err := s.sub.AddValidator(s.incomingNetHead)
	if err != nil {
		return err
	}
	// get the latest head and set it as syncing target
	_, err = s.networkHead(ctx)
	if err != nil {
		return err
	}
	// start syncLoop only if Start is errorless
	go s.syncLoop()
	return nil
}

// Stop stops Syncer.
func (s *Syncer) Stop(ctx context.Context) error {
	s.cancel()
	return s.sub.Stop(ctx)
}

// WaitSync blocks until ongoing sync is done.
func (s *Syncer) WaitSync(ctx context.Context) error {
	state := s.State()
	if state.Finished() {
		return nil
	}

	// this store method blocks until header is available
	_, err := s.store.GetByHeight(ctx, state.ToHeight)
	return err
}

// State collects all the information about a sync.
type State struct {
	ID                   uint64 // incrementing ID of a sync
	Height               uint64 // height at the moment when State is requested for a sync
	FromHeight, ToHeight uint64 // the starting and the ending point of a sync
	FromHash, ToHash     tmbytes.HexBytes
	Start, End           time.Time
	Error                error // the error that might happen within a sync
}

// Finished returns true if sync is done, false otherwise.
func (s State) Finished() bool {
	return s.ToHeight <= s.Height
}

// Duration returns the duration of the sync.
func (s State) Duration() time.Duration {
	return s.End.Sub(s.Start)
}

// State reports state of the current (if in progress), or last sync (if finished).
// Note that throughout the whole Syncer lifetime there might an initial sync and multiple catch-ups.
// All of them are treated as different syncs with different state IDs and other information.
func (s *Syncer) State() State {
	s.stateLk.RLock()
	state := s.state
	s.stateLk.RUnlock()
	state.Height = s.store.Height()
	return state
}

// wantSync will trigger the syncing loop (non-blocking).
func (s *Syncer) wantSync() {
	select {
	case s.triggerSync <- struct{}{}:
	default:
	}
}

// syncLoop controls syncing process.
func (s *Syncer) syncLoop() {
	for {
		select {
		case <-s.triggerSync:
			s.sync(s.ctx)
		case <-s.ctx.Done():
			return
		}
	}
}

// sync ensures we are synced from the Store's head up to the new subjective head
func (s *Syncer) sync(ctx context.Context) {
	newHead := s.pending.Head()
	if newHead == nil {
		return
	}

	head, err := s.store.Head(ctx)
	if err != nil {
		log.Errorw("getting head during sync", "err", err)
		return
	}

	if head.Height >= newHead.Height {
		log.Warnw("sync attempt to an already synced header",
			"synced_height", head.Height,
			"attempted_height", newHead.Height,
		)
		log.Warn("PLEASE REPORT THIS AS A BUG")
		return // should never happen, but just in case
	}

	log.Infow("syncing headers",
		"from", head.Height,
		"to", newHead.Height)
	err = s.doSync(ctx, head, newHead)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// don't log this error as it is normal case of Syncer being stopped
			return
		}

		log.Errorw("syncing headers",
			"from", head.Height,
			"to", newHead.Height,
			"err", err)
		return
	}

	log.Infow("finished syncing",
		"from", head.Height,
		"to", newHead.Height,
		"elapsed time", s.state.End.Sub(s.state.Start))
}

// doSync performs actual syncing updating the internal State
func (s *Syncer) doSync(ctx context.Context, fromHead, toHead *header.ExtendedHeader) (err error) {
	from, to := uint64(fromHead.Height)+1, uint64(toHead.Height)

	s.stateLk.Lock()
	s.state.ID++
	s.state.FromHeight = from
	s.state.ToHeight = to
	s.state.FromHash = fromHead.Hash()
	s.state.ToHash = toHead.Hash()
	s.state.Start = time.Now()
	s.stateLk.Unlock()

	for processed := 0; from < to; from += uint64(processed) {
		processed, err = s.processHeaders(ctx, from, to)
		if err != nil && processed == 0 {
			break
		}
	}

	s.stateLk.Lock()
	s.state.End = time.Now()
	s.state.Error = err
	s.stateLk.Unlock()
	return err
}

// processHeaders gets and stores headers starting at the given 'from' height up to 'to' height - [from:to]
func (s *Syncer) processHeaders(ctx context.Context, from, to uint64) (int, error) {
	headers, err := s.findHeaders(ctx, from, to)
	if err != nil {
		return 0, err
	}

	return s.store.Append(ctx, headers...)
}

// TODO(@Wondertan): Number of headers that can be requested at once. Either make this configurable or,
//
//	find a proper rationale for constant.
//
// TODO(@Wondertan): Make configurable
var requestSize uint64 = 512

// findHeaders gets headers from either remote peers or from local cache of headers received by PubSub - [from:to]
func (s *Syncer) findHeaders(ctx context.Context, from, to uint64) ([]*header.ExtendedHeader, error) {
	amount := to - from + 1 // + 1 to include 'to' height as well
	if amount > requestSize {
		to, amount = from+requestSize, requestSize
	}

	out := make([]*header.ExtendedHeader, 0, amount)
	for from < to {
		// if we have some range cached - use it
		r, ok := s.pending.FirstRangeWithin(from, to)
		if !ok {
			hs, err := s.exchange.GetRangeByHeight(ctx, from, amount)
			return append(out, hs...), err
		}

		// first, request everything between from and start of the found range
		hs, err := s.exchange.GetRangeByHeight(ctx, from, r.start-from)
		if err != nil {
			return nil, err
		}
		out = append(out, hs...)
		from += uint64(len(hs))

		// then, apply cached range if any
		cached, ln := r.Before(to)
		out = append(out, cached...)
		from += ln
	}

	return out, nil
}
