package sync

import (
	"context"
	"errors"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/celestiaorg/celestia-node/header"
)

// subjectiveHead returns the latest known local header that is not expired(within trusting period).
// If the header is expired, it is retrieved from a trusted peer without validation;
// in other words, an automatic subjective initialization is performed.
func (s *Syncer) subjectiveHead(ctx context.Context) (*header.ExtendedHeader, error) {
	// pending head is the latest known subjective head Syncer syncs to, so try to get it
	// NOTES:
	// * Empty when no sync is in progress
	// * Pending cannot be expired, guaranteed
	pendHead := s.pending.Head()
	if pendHead != nil {
		return pendHead, nil
	}
	// if empty, get subjective head out of the store
	netHead, err := s.store.Head(ctx)
	if err != nil {
		return nil, err
	}
	// check if our subjective header is not expired and use it
	if !netHead.IsExpired() {
		return netHead, nil
	}
	log.Infow("subjective header expired", "height", netHead.Height)
	// otherwise, request network head from a trusted peer
	netHead, err = s.exchange.Head(ctx)
	if err != nil {
		return nil, err
	}
	// and set as the new subjective head without validation,
	// or, in other words, do 'automatic subjective initialization'
	s.newNetHead(ctx, netHead, true)
	switch {
	default:
		log.Infow("subjective initialization finished", "height", netHead.Height)
		return netHead, nil
	case netHead.IsExpired():
		log.Warnw("subjective initialization with an expired header", "height", netHead.Height)
	case !netHead.IsRecent(s.blockTime):
		log.Warnw("subjective initialization with an old header", "height", netHead.Height)
	}
	log.Warn("trusted peer is out of sync")
	return netHead, nil
}

// networkHead returns the latest network header.
// Known subjective head is considered network head if it is recent enough(now-timestamp<=blocktime).
// Otherwise, network header is requested from a trusted peer and set as the new subjective head,
// assuming that trusted peer is always synced.
func (s *Syncer) networkHead(ctx context.Context) (*header.ExtendedHeader, error) {
	sbjHead, err := s.subjectiveHead(ctx)
	if err != nil {
		return nil, err
	}
	// if subjective header is recent enough (relative to the network's block time) - just use it
	if sbjHead.IsRecent(s.blockTime) {
		return sbjHead, nil
	}
	// otherwise, request head from a trusted peer, as we assume it is fully synced
	//
	// the lock construction here ensures only one routine requests at a time
	// while others wait via Rlock
	if !s.netReqLk.TryLock() {
		s.netReqLk.RLock()
		defer s.netReqLk.RUnlock()
		return s.subjectiveHead(ctx)
	}
	defer s.netReqLk.Unlock()
	// TODO(@Wondertan): Here is another potential networking optimization:
	//  * From sbjHead's timestamp and current time predict the time to the next header(TNH)
	//  * If now >= TNH && now <= TNH + (THP) header propagation time
	//    * Wait for header to arrive instead of requesting it
	//  * This way we don't request as we know the new network header arrives exactly
	netHead, err := s.exchange.Head(ctx)
	if err != nil {
		return nil, err
	}
	// process netHead returned from the trusted peer and validate against the subjective head
	// NOTE: We could trust the netHead like we do during 'automatic subjective initialization'
	// but in this case our subjective head is not expired, so we should verify maybeHead
	// and only if it is valid, set it as new head
	s.newNetHead(ctx, netHead, false)
	// maybeHead was either accepted or rejected as the new subjective
	// anyway return most current known subjective head
	return s.subjectiveHead(ctx)
}

// incomingNetHead processes new gossiped network headers.
func (s *Syncer) incomingNetHead(ctx context.Context, netHead *header.ExtendedHeader) pubsub.ValidationResult {
	// Try to short-circuit netHead with append. If not adjacent/from future - try it as new network header
	_, err := s.store.Append(ctx, netHead)
	switch err {
	case nil:
		// a happy case where we appended maybe head directly, so accept
		return pubsub.ValidationAccept
	case header.ErrNonAdjacent:
		// not adjacent, maybe we've missed a few headers or its from the past
	default:
		var verErr *header.VerifyError
		if errors.As(err, &verErr) {
			return pubsub.ValidationReject
		}
		// might be a storage error or something else, but we can still try to continue processing netHead
		log.Errorw("appending network header",
			"height", netHead.Height,
			"hash", netHead.Hash().String(),
			"err", err)
	}
	// try as new head
	return s.newNetHead(ctx, netHead, false)
}

// newNetHead sets the network header as the new subjective head with preceding validation(per request).
func (s *Syncer) newNetHead(ctx context.Context, netHead *header.ExtendedHeader, trust bool) pubsub.ValidationResult {
	// validate netHead against subjective head
	if !trust {
		if res := s.validate(ctx, netHead); res != pubsub.ValidationAccept {
			// netHead was either ignored or rejected
			return res
		}
	}
	// and if valid, set it as new subjective head
	s.pending.Add(netHead)
	s.wantSync()
	log.Infow("new network head", "height", netHead.Height, "hash", netHead.Hash())
	return pubsub.ValidationAccept
}

// validate checks validity of the given header against the subjective head.
func (s *Syncer) validate(ctx context.Context, new *header.ExtendedHeader) pubsub.ValidationResult {
	sbjHead, err := s.subjectiveHead(ctx)
	if err != nil {
		log.Errorw("getting subjective head during validation", "err", err)
		return pubsub.ValidationIgnore // local error, so ignore
	}
	// ignore header if it's from the past
	if !sbjHead.IsBefore(new) {
		log.Warnw("received known network header",
			"current_height", sbjHead.Height,
			"header_height", new.Height,
			"header_hash", new.Hash())
		return pubsub.ValidationIgnore
	}
	// perform verification
	err = sbjHead.VerifyNonAdjacent(new)
	var verErr *header.VerifyError
	if errors.As(err, &verErr) {
		log.Errorw("invalid network header",
			"height_of_invalid", new.Height,
			"hash_of_invalid", new.Hash(),
			"height_of_subjective", sbjHead.Height,
			"hash_of_subjective", sbjHead.Hash(),
			"reason", verErr.Reason)
		return pubsub.ValidationReject
	}
	// and accept if the header is good
	return pubsub.ValidationAccept
}
