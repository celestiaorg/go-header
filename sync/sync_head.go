package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/celestiaorg/go-header"
)

// headRequestTimeout is the amount of time the syncer is willing to wait for
// the exchange to request the head of the chain from the network.
var headRequestTimeout = time.Second * 2

// Head returns the Network Head.
//
// Known subjective head is considered network head if it is recent enough(now-timestamp<=blocktime)
// Otherwise, we attempt to request recent network head from a trusted peer and
// set as the new subjective head, assuming that trusted peer is always fully synced.
//
// The request is limited with 2 seconds and otherwise potentially unrecent header is returned.
func (s *Syncer[H]) Head(ctx context.Context, _ ...header.HeadOption[H]) (H, error) {
	sbjHead, err := s.subjectiveHead(ctx)
	if err != nil {
		return sbjHead, err
	}
	// if subjective header is recent enough (relative to the network's block time) - just use it
	if isRecent(sbjHead, s.Params.blockTime, s.Params.recencyThreshold) {
		return sbjHead, nil
	}

	s.metrics.outdatedHead(s.ctx)

	reqCtx, cancel := context.WithTimeout(ctx, headRequestTimeout)
	defer cancel()
	netHead, err := s.head.Head(reqCtx, header.WithTrustedHead[H](sbjHead))
	if err != nil {
		log.Warnw(
			"failed to get recent head, returning current subjective",
			"sbjHead",
			sbjHead.Height(),
			"err",
			err,
		)
		return s.subjectiveHead(ctx)
	}

	// process and validate netHead fetched from trusted peers
	// NOTE: We could trust the netHead like we do during 'automatic subjective initialization'
	// but in this case our subjective head is not expired, so we should verify netHead
	// and only if it is valid, set it as new head
	_ = s.incomingNetworkHead(ctx, netHead)
	// netHead was either accepted or rejected as the new subjective
	// anyway return most current known subjective head
	return s.subjectiveHead(ctx)
}

// subjectiveHead returns the latest known local header that is not expired(within trusting period).
// If the header is expired, it is retrieved from a trusted peer without validation;
// in other words, an automatic subjective initialization is performed.
func (s *Syncer[H]) subjectiveHead(ctx context.Context) (H, error) {
	// pending head is the latest known subjective head and sync target, so try to get it
	// NOTES:
	// * Empty when no sync is in progress
	// * Pending cannot be expired, guaranteed
	pendHead := s.pending.Head()
	if !pendHead.IsZero() {
		return pendHead, nil
	}
	// if pending is empty - get the latest stored/synced head
	storeHead, err := s.store.Head(ctx)
	switch {
	case errors.Is(err, header.ErrEmptyStore):
		log.Info("empty store, initializing...")
		s.metrics.subjectiveInitialization(s.ctx)
	case !storeHead.IsZero() && isExpired(storeHead, s.Params.TrustingPeriod):
		log.Infow("stored head header expired", "height", storeHead.Height())
	default:
		return storeHead, err
	}
	// fetch a new head from trusted peers if not available locally
	newHead, err := s.head.Head(ctx)
	if err != nil {
		return newHead, err
	}
	switch {
	case isExpired(newHead, s.Params.TrustingPeriod):
		// forbid initializing off an expired header
		err := fmt.Errorf("subjective initialization with an expired header(%d)", newHead.Height())
		log.Error(err, "\n trusted peers are out of sync")
		s.metrics.trustedPeersOutOufSync(s.ctx)
		return newHead, err
	case !isRecent(newHead, s.Params.blockTime, s.Params.recencyThreshold):
		log.Warnw("subjective initialization with not recent header", "height", newHead.Height())
		s.metrics.trustedPeersOutOufSync(s.ctx)
	}

	// and set the fetched head as the new subjective head validating it against the tail
	// or, in other words, do 'automatic subjective initialization'
	err = s.incomingNetworkHead(ctx, newHead)
	if err != nil {
		err = fmt.Errorf("subjective initialization failed for head(%d): %w", newHead.Height(), err)
		log.Error(err)
		return newHead, err
	}

	log.Infow("subjective initialization finished", "head", newHead.Height())
	return newHead, nil
}

// setSubjectiveHead takes already validated head and sets it as the new sync target.
func (s *Syncer[H]) setSubjectiveHead(ctx context.Context, netHead H) {
	// TODO(@Wondertan): Right now, we can only store adjacent headers, instead we should:
	//  * Allow storing any valid header here in Store
	//  * Remove ErrNonAdjacent
	//  * Remove writeHead from the canonical store implementation
	err := s.store.Append(ctx, netHead)
	var nonAdj *errNonAdjacent
	if err != nil && !errors.As(err, &nonAdj) {
		// might be a storage error or something else, but we can still try to continue processing netHead
		log.Errorw("storing new network header",
			"height", netHead.Height(),
			"hash", netHead.Hash().String(),
			"err", err)
	}
	s.metrics.newSubjectiveHead(s.ctx, netHead.Height(), netHead.Time())

	storeHead, err := s.store.Head(ctx)
	if err == nil && storeHead.Height() >= netHead.Height() {
		// we already synced it up - do nothing
		return
	}
	// and if valid, set it as new subjective head
	s.pending.Add(netHead)
	s.wantSync()
	log.Infow("new network head", "height", netHead.Height(), "hash", netHead.Hash())
}

// incomingNetworkHead processes new potential network headers.
// If the header valid, sets as new subjective header.
func (s *Syncer[H]) incomingNetworkHead(ctx context.Context, head H) error {
	// ensure there is no racing between network head candidates
	s.incomingMu.Lock()
	defer s.incomingMu.Unlock()

	// TODO(@Wondertan): We need to ensure Tail is available before verification for subj init case and this is fine here
	//  however, check if that's ok to trigger Tail moves on network header that hasn't been verified yet
	//  To be reworked by bsync.
	_, err := s.subjectiveTail(ctx, head)
	if err != nil {
		log.Errorw("subjective tail failed",
			"new_head", head.Height(),
			"hash", head.Hash().String(),
			"err", err,
		)
		return err
	}

	err = s.verify(ctx, head)
	if err != nil {
		return err
	}

	s.setSubjectiveHead(ctx, head)
	return err
}

// verify verifies given network head candidate.
func (s *Syncer[H]) verify(ctx context.Context, newHead H) error {
	sbjHead, err := s.subjectiveHead(ctx)
	if err != nil {
		log.Errorw("getting subjective head during validation", "err", err)
		return err
	}

	err = header.Verify(sbjHead, newHead)
	if err == nil {
		return nil
	}

	var verErr *header.VerifyError
	if errors.As(err, &verErr) && verErr.SoftFailure {
		// bifurcate for soft failures only
		return s.verifyBifurcating(ctx, sbjHead, newHead)
	}

	logF := log.Warnw
	if errors.Is(err, header.ErrKnownHeader) {
		logF = log.Debugw
	}
	logF("invalid network header",
		"height_of_invalid", newHead.Height(),
		"hash_of_invalid", newHead.Hash(),
		"height_of_subjective", sbjHead.Height(),
		"hash_of_subjective", sbjHead.Hash(),
		"reason", verErr.Reason)

	return err
}

// verifyBifurcating verifies networkHead against subjHead via the interim headers when direct
// verification is impossible.
// It tries to find a header (or several headers if necessary) between the networkHead and
// the subjectiveHead such that non-adjacent (or in the worst case adjacent) verification
// passes and the networkHead can be verified as a valid sync target against the syncer's
// subjectiveHead.
// A non-nil error is returned when networkHead can't be verified.
func (s *Syncer[H]) verifyBifurcating(ctx context.Context, subjHead, networkHead H) error {
	log.Warnw("header bifurcation started",
		"height", networkHead.Height(),
		"hash", networkHead.Hash().String(),
	)

	subjHeight := subjHead.Height()

	diff := networkHead.Height() - subjHeight

	for diff > 1 {
		candidateHeight := subjHeight + diff/2

		candidateHeader, err := s.getter.GetByHeight(ctx, candidateHeight)
		if err != nil {
			return err
		}

		if err := header.Verify(subjHead, candidateHeader); err != nil {
			var verErr *header.VerifyError
			if errors.As(err, &verErr) && !verErr.SoftFailure {
				return err
			}

			// candidate failed, go deeper in 1st half.
			diff /= 2
			continue
		}

		// candidate was validated properly, update subjHead.
		subjHead = candidateHeader
		s.setSubjectiveHead(ctx, subjHead)

		if err := header.Verify(subjHead, networkHead); err == nil {
			// network head validate properly, return success.
			return nil
		}

		// new subjHead failed, go deeper in 2nd half.
		subjHeight = subjHead.Height()
		diff = networkHead.Height() - subjHeight
	}

	s.metrics.failedBifurcation(ctx, networkHead.Height(), networkHead.Hash().String())
	log.Errorw("header bifurcation failed",
		"height", networkHead.Height(),
		"hash", networkHead.Hash().String(),
	)

	return &header.VerifyError{
		Reason: fmt.Errorf("sync: header validation against subjHead height:%d hash:%s",
			networkHead.Height(), networkHead.Hash().String(),
		),
		SoftFailure: false,
	}
}

// isExpired checks if header is expired against trusting period.
func isExpired[H header.Header[H]](header H, period time.Duration) bool {
	expirationTime := header.Time().Add(period)
	return expirationTime.Before(time.Now())
}

// isRecent checks if header is recent against the given recency threshold.
func isRecent[H header.Header[H]](header H, blockTime, recencyThreshold time.Duration) bool {
	if recencyThreshold == 0 {
		recencyThreshold = blockTime * 2 // allow some drift by adding additional buffer of 2 blocks
	}
	return time.Since(header.Time()) <= recencyThreshold
}
