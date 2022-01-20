package header

import (
	"context"
	"sync/atomic"

	"github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"

	tmbytes "github.com/tendermint/tendermint/libs/bytes"
)

// TODO:
//  1. Sync protocol for peers to exchange their local heads on connect
//  2. If we are far from peers but within an unbonding period - trigger sync automatically
//  3. If we are beyond the unbonding period - request Local Head + 1 header from trusted and hardcoded peer
//  automatically and continue sync until know head.
//  4. Limit amount of requests on server side
//  5. Sync status and till sync is done.
//  7. Retry requesting headers

// Syncer implements simplest possible synchronization for headers.
type Syncer struct {
	sub      *P2PSubscriber
	exchange Exchange
	store    Store
	trusted  tmbytes.HexBytes

	// inProgress is set to 1 once syncing commences and
	// is set to 0 once syncing is either finished or
	// not currently in progress
	inProgress  uint64
	triggerSync chan struct{}

	// pending keeps ranges of valid headers rcvd from the network awaiting to be appended to store
	pending ranges

	ctx    context.Context
	cancel context.CancelFunc
}

// NewSyncer creates a new instance of Syncer.
func NewSyncer(exchange Exchange, store Store, sub *P2PSubscriber, trusted tmbytes.HexBytes) *Syncer {
	return &Syncer{
		sub:         sub,
		exchange:    exchange,
		store:       store,
		trusted:     trusted,
		triggerSync: make(chan struct{}),
	}
}

// Start starts the syncing routine.
func (s *Syncer) Start(context.Context) error {
	if s.sub != nil {
		err := s.sub.AddValidator(s.validateMsg)
		if err != nil {
			return err
		}
	}

	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.syncLoop()
	s.mustSync()
	return nil
}

// Stop stops Syncer.
func (s *Syncer) Stop(context.Context) error {
	s.cancel()
	return nil
}

// wantSync will trigger the syncing loop (non-blocking).
func (s *Syncer) wantSync() {
	select {
	case s.triggerSync <- struct{}{}:
	default:
	}
}

// mustSync will trigger the syncing loop (blocking).
func (s *Syncer) mustSync() {
	select {
	case s.triggerSync <- struct{}{}:
	case <-s.ctx.Done():
	}
}

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

// Sync syncs all headers up to the latest known header in the network.
func (s *Syncer) sync(ctx context.Context) {
	log.Info("syncing headers")
	// indicate syncing
	s.syncInProgress()
	// when method returns, toggle inProgress off
	defer s.finishSync()
	for {
		// TODO: We need this until https://github.com/celestiaorg/celestia-node/issues/366 is implemented
		if ctx.Err() != nil {
			return
		}

		sbjHead, err := s.subjectiveHead(ctx)
		if err != nil {
			log.Errorw("getting subjective head", "err", err)
			return
		}

		objHead, err := s.objectiveHead(ctx, sbjHead)
		if err != nil {
			log.Errorw("getting objective head", "err", err)
			return
		}

		if sbjHead.Height >= objHead.Height {
			// we are now synced
			log.Info("synced headers")
			return
		}

		err = s.syncDiff(ctx, sbjHead, objHead)
		if err != nil {
			log.Errorw("syncing headers", "err", err)
			return
		}
	}
}

// IsSyncing returns the current sync status of the Syncer.
func (s *Syncer) IsSyncing() bool {
	return atomic.LoadUint64(&s.inProgress) == 1
}

// syncInProgress indicates Syncer's sync status is in progress.
func (s *Syncer) syncInProgress() {
	atomic.StoreUint64(&s.inProgress, 1)
}

// finishSync indicates Syncer's sync status as no longer in progress.
func (s *Syncer) finishSync() {
	atomic.StoreUint64(&s.inProgress, 0)
}

// validateMsg implements validation of incoming Headers as PubSub msg validator.
func (s *Syncer) validateMsg(ctx context.Context, p peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
	maybeHead, err := UnmarshalExtendedHeader(msg.Data)
	if err != nil {
		log.Errorw("unmarshalling header received from the PubSub",
			"err", err, "peer", p.ShortString())
		return pubsub.ValidationReject
	}

	return s.incoming(ctx, p, maybeHead)
}

// incoming process new incoming Headers, validates them and applies/caches if applicable.
func (s *Syncer) incoming(ctx context.Context, p peer.ID, maybeHead *ExtendedHeader) pubsub.ValidationResult {
	localHead, err := s.store.Head(ctx)
	if err != nil {
		log.Errorw("getting local head", "err", err)
		return pubsub.ValidationIgnore // we don't know if header is invalid so ignore
	}

	if maybeHead.Height <= localHead.Height {
		log.Warnw("known header", "height", maybeHead.Height, "from", p.ShortString())
		return pubsub.ValidationIgnore // we don't know if header is invalid so ignore
	}

	// incoming that +2/3 of subjective validator set signed the commit and if not - reject
	err = Verify(localHead, maybeHead)
	if err != nil {
		// our subjective view can be far from objective network head(during sync), so we cannot be sure if header is
		// 100% invalid due to outdated validator set, thus ValidationIgnore
		// Potentially, we miss
		log.Warnw("ignoring header",
			"err", err, "hash", maybeHead.Hash(), "height", maybeHead.Height, "from", p.ShortString())
		return pubsub.ValidationIgnore
	}

	if maybeHead.Height > localHead.Height+1 {
		// we might be missing some headers, so save verified header to pending cache
		s.pending.Add(maybeHead)
		// and trigger sync to catch-up
		s.wantSync()
		// accepted
		log.Info("new pending header")
		return pubsub.ValidationAccept
	}

	// at this point we reliably know 'maybeHead' is adjacent to 'localHead' so append
	err = s.store.Append(ctx, maybeHead)
	if err != nil {
		log.Errorw("appending store with header from PubSub",
			"hash", maybeHead.Hash().String(), "height", maybeHead.Height, "peer", p.ShortString())

		// TODO(@Wondertan): We need to be sure that the error is actually validation error.
		//  Rejecting msgs due to storage error is not good, but for now that's fine.
		return pubsub.ValidationReject
	}

	// we are good to go
	return pubsub.ValidationAccept
}

// subjectiveHead tries to get head locally and if it does not exist, requests it by trusted hash.
func (s *Syncer) subjectiveHead(ctx context.Context) (*ExtendedHeader, error) {
	head, err := s.store.Head(ctx)
	switch err {
	case nil:
		return head, nil
	case ErrNoHead:
		// if there is no head - request header at trusted hash.
		trusted, err := s.exchange.RequestByHash(ctx, s.trusted)
		if err != nil {
			log.Errorw("requesting header at trusted hash", "err", err)
			return nil, err
		}

		err = s.store.Append(ctx, trusted)
		if err != nil {
			log.Errorw("appending header at trusted hash to store", "err", err)
			return nil, err
		}

		return trusted, nil
	}

	return nil, err
}

// objectiveHead gets the objective network head.
func (s *Syncer) objectiveHead(ctx context.Context, sbj *ExtendedHeader) (*ExtendedHeader, error) {
	phead := s.pending.PopHead()
	if phead != nil && phead.Height >= sbj.Height {
		return phead, nil
	}

	return s.exchange.RequestHead(ctx)
}

// TODO(@Wondertan): Number of headers that can be requested at once. Either make this configurable or,
//  find a proper rationale for constant.
var requestSize uint64 = 256

// syncDiff requests headers from knownHead up to new head.
func (s *Syncer) syncDiff(ctx context.Context, knownHead, newHead *ExtendedHeader) error {
	start, end := uint64(knownHead.Height+1), uint64(newHead.Height)
	for start < end {
		amount := end - start
		if amount > requestSize {
			amount = requestSize
		}

		headers, err := s.getHeaders(ctx, start, amount)
		if err != nil && len(headers) == 0 {
			return err
		}

		err = s.store.Append(ctx, headers...)
		if err != nil {
			return err
		}

		start += uint64(len(headers))
	}

	return s.store.Append(ctx, newHead)
}

// getHeaders gets headers from either remote peers or from local cached of headers rcvd by PubSub
func (s *Syncer) getHeaders(ctx context.Context, start, amount uint64) ([]*ExtendedHeader, error) {
	// short-circuit if nothing in pending cache to avoid unnecessary allocation below
	if _, ok := s.pending.BackWithin(start, start+amount); !ok {
		return s.exchange.RequestHeaders(ctx, start, amount)
	}

	end, out := start+amount, make([]*ExtendedHeader, 0, amount)
	for start < end {
		// if we have some range cached - use it
		if r, ok := s.pending.BackWithin(start, end); ok {
			// first, request everything between start and found range
			hs, err := s.exchange.RequestHeaders(ctx, start, r.Start-start)
			if err != nil {
				return nil, err
			}
			out = append(out, hs...)
			start += uint64(len(hs))

			// than, apply cached range
			cached := r.Before(end)
			out = append(out, cached...)
			start += uint64(len(cached))

			// repeat, as there might be multiple cache ranges
			continue
		}

		// fetch the leftovers
		hs, err := s.exchange.RequestHeaders(ctx, start, end-start)
		if err != nil {
			// still return what was successfully gotten
			return out, err
		}

		return append(out, hs...), nil
	}

	return out, nil
}