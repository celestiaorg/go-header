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

// Head returns the Network Head or an error. It will try to get the most recent header until it fails entirely.
// It may return an error with a header which caused it.
//
// Known subjective head is considered network head if it is recent enough(now-timestamp<=blocktime)
// Otherwise, we attempt to request recent network head from a trusted peer and
// set as the new subjective head, assuming that trusted peer is always fully synced.
//
// The request is limited with 2 seconds and otherwise potentially unrecent header is returned.
func (s *Syncer[H]) Head(ctx context.Context, _ ...header.HeadOption[H]) (H, error) {
	netHead, err := s.networkHead(ctx)
	if err != nil {
		return netHead, err
	}

	_, err = s.subjectiveTail(ctx, netHead)
	if err != nil {
		return netHead, fmt.Errorf(
			"subjective tail during subjective initialization for head %d: %w",
			netHead.Height(),
			err,
		)
	}

	// and set the fetched head as the new subjective head validating it against the tail
	// or, in other words, do 'automatic subjective initialization'
	_ = s.incomingNetworkHead(ctx, netHead)
	return s.subjectiveHead(ctx)
}

// subjectiveHead returns the most recent non-expired header.
// If the header is expired, it is retrieved from a trusted peer;
// in other words, an automatic subjective initialization is performed.
func (s *Syncer[H]) networkHead(ctx context.Context) (H, error) {
	var (
		reqCtx    = ctx
		reqCancel context.CancelFunc
		reqOpts   []header.HeadOption[H]
	)

	lclHead, err := s.subjectiveHead(ctx)
	switch {
	case errors.Is(err, header.ErrEmptyStore):
		log.Info("empty store, initializing...")
		s.metrics.subjectiveInitialization(ctx)
	case !lclHead.IsZero() && isExpired(lclHead, s.Params.TrustingPeriod):
		log.Infow(
			"local head header expired, reinitializing...",
			"expired_height",
			lclHead.Height(),
		)
		s.metrics.subjectiveInitialization(ctx)
	case !lclHead.IsZero() && !isRecent(lclHead, s.Params.blockTime, s.Params.recencyThreshold):
		reqCtx, reqCancel = context.WithTimeout(ctx, headRequestTimeout)
		defer reqCancel()
		reqOpts = append(reqOpts, header.WithTrustedHead[H](lclHead))

		log.Debugw("outdated local head header", "outdated_height", lclHead.Height())
		s.metrics.outdatedHead(ctx)
	default:
		// success and unknown error case
		return lclHead, err
	}

	newHead, err := s.head.Head(reqCtx, reqOpts...)
	if err != nil {
		if !lclHead.IsZero() && !isExpired(lclHead, s.Params.TrustingPeriod) {
			// if we have a local head, but failed to get a more recent network head
			// we can still use the local head
			return lclHead, nil
		}
		return newHead, err
	}
	switch {
	case isExpired(newHead, s.Params.TrustingPeriod):
		// forbid initializing off an expired header
		err := fmt.Errorf("subjective initialization with an expired header(%d)", newHead.Height())
		log.Error(err, "\n trusted peers are out of sync")
		s.metrics.trustedPeersOutOufSync(ctx)
		return newHead, err
	case !isRecent(newHead, s.Params.blockTime, s.Params.recencyThreshold):
		// it's not the most recent, but its good enough - allow initialization
		log.Warnw("subjective initialization with not recent header", "height", newHead.Height())
		s.metrics.trustedPeersOutOufSync(ctx)
	}

	log.Infow("subjective initialization finished", "head", newHead.Height())
	return newHead, nil
}

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
	return s.store.Head(ctx)
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

	if err := s.verify(ctx, head); err != nil {
		return err
	}

	s.setSubjectiveHead(ctx, head)
	return nil
}

// verify verifies given network head candidate.
func (s *Syncer[H]) verify(ctx context.Context, newHead H) error {
	sbjHead, err := s.subjectiveHead(ctx)
	if err != nil {
		log.Errorw("getting subjective head during new network head verification", "err", err)
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
