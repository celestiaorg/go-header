package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/celestiaorg/go-header"
)

// NetworkHeadRequestTimeout is the amount of time the syncer is willing to wait for
// the exchange to request the head of the chain from the network.
var NetworkHeadRequestTimeout = time.Second * 2

// Head returns the network head or an error. It will try to get the most recent network head or return the current
// non-expired subjective head as a fallback.
// If the head has changed, it will update the tail with the new head.
func (s *Syncer[H]) Head(ctx context.Context, _ ...header.HeadOption[H]) (H, error) {
	netHead, err := s.networkHead(ctx)
	if err != nil {
		return netHead, err
	}

	if _, err = s.subjectiveTail(ctx, netHead); err != nil {
		return netHead, fmt.Errorf(
			"subjective tail for head %d: %w",
			netHead.Height(),
			err,
		)
	}

	// attempt to set the (potentially) new network head
	// it doesn't matter for the caller setting succeeds or not
	_ = s.incomingNetworkHead(ctx, netHead)
	// so return whatever is the current highest head
	return s.localHead(ctx)
}

// networkHead returns subjective head ensuring its recency.
// If the subjective head is not recent, it attempts to request the most recent network head from trusted peers
// assuming that trusted peers are always fully synced.
// The request is limited with [NetworkHeadRequestTimeout], otherwise the unrecent subjective header is returned.
func (s *Syncer[H]) networkHead(ctx context.Context) (H, error) {
	sbjHead, initialized, err := s.subjectiveHead(ctx)
	if err != nil {
		return sbjHead, err
	}
	if isRecent(sbjHead, s.Params.blockTime, s.Params.recencyThreshold) || initialized {
		return sbjHead, nil
	}

	s.metrics.outdatedHead(ctx)
	log.Warnw("outdated subjective head", "outdated_height", sbjHead.Height())
	log.Warnw("attempting to request the most recent network head...")

	// cap the max blocking time for the request call
	ctx, cancel := context.WithTimeout(ctx, NetworkHeadRequestTimeout)
	defer cancel()

	newHead, err := s.head.Head(ctx, header.WithTrustedHead[H](sbjHead))
	var verErr *header.VerifyError
	if errors.As(err, &verErr) && verErr.SoftFailure {
		// if we have a soft failure, try to bifurcate
		err = s.incomingNetworkHead(ctx, newHead)
	}
	if err != nil {
		// if we have a non-expired subjective head, but failed to get a more recent network head
		// still return the current subjective head
		log.Warnw(
			"error requesting the most recent network head, using the current subjective",
			"err",
			err,
			"subjective_height",
			sbjHead.Height(),
		)

		return sbjHead, nil
	}
	// still check if even the newly requested head is outdated
	if !isRecent(newHead, s.Params.blockTime, s.Params.recencyThreshold) {
		log.Warnw("non recent head from trusted peers", "height", newHead.Height())
		log.Error("trusted peers are out of sync")
		s.metrics.trustedPeersOutOufSync(ctx)
	}

	if newHead.Height() <= sbjHead.Height() {
		// nothing new, just return what we have already
		return sbjHead, nil
	}
	// set the new head as subjective, skipping expensive verification
	// as it was already verified by the Exchange.
	s.setLocalHead(ctx, newHead)

	log.Infow(
		"successfully requested a more recent network head",
		"height",
		newHead.Height(),
	)
	return newHead, nil
}

// subjectiveHead returns the highest known non-expired subjective Head.
// If the current subjective head is expired or does not exist,
// it performs automatic subjective (re) initialization by requesting the most recent head from trusted peers.
// Reports true if initialization was performed, false otherwise.
func (s *Syncer[H]) subjectiveHead(ctx context.Context) (H, bool, error) {
	sbjHead, err := s.localHead(ctx)
	switch {
	case errors.Is(err, header.ErrEmptyStore):
		log.Info("empty store, initializing...")
	case !sbjHead.IsZero() && isExpired(sbjHead, s.Params.TrustingPeriod):
		log.Infow(
			"subjective head expired, reinitializing...",
			"expired_height",
			sbjHead.Height(),
		)
	default:
		// success or unknown error case
		return sbjHead, false, err
	}

	s.metrics.subjectiveInitialization(ctx)
	newHead, err := s.head.Head(ctx)
	if err != nil {
		return newHead, false, err
	}
	// still check if even the newly requested head is expired
	if isExpired(newHead, s.Params.TrustingPeriod) {
		// forbid initializing off an expired header
		err := fmt.Errorf("subjective initialization with an expired header(%d)", newHead.Height())
		log.Error(err)
		log.Error("trusted peers are out of sync")
		s.metrics.trustedPeersOutOufSync(ctx)
		return newHead, false, err
	}

	log.Infow("subjective initialization finished", "height", newHead.Height())
	return newHead, false, nil
}

// localHead reports the current highest locally known head.
func (s *Syncer[H]) localHead(ctx context.Context) (H, error) {
	// pending head is the latest known subjective head and a sync target
	// if it is empty, no sync is in progress
	pendHead := s.pending.Head()
	if !pendHead.IsZero() {
		return pendHead, nil
	}
	// if pending is empty - get the latest stored/synced head
	return s.store.Head(ctx)
}

// setLocalHead takes the already validated head and sets it as the new sync target.
func (s *Syncer[H]) setLocalHead(ctx context.Context, netHead H) {
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

// incomingNetworkHead processes new potential network heads.
// If the header is valid, sets as the new subjective header.
func (s *Syncer[H]) incomingNetworkHead(ctx context.Context, head H) error {
	// ensure there is no racing between network head candidates
	// additionally ensures there is only one bifurcation attempt at a time
	s.incomingMu.Lock()
	defer s.incomingMu.Unlock()

	if err := s.verify(ctx, head); err != nil {
		return err
	}

	s.setLocalHead(ctx, head)
	return nil
}

// verify verifies given network head candidate.
func (s *Syncer[H]) verify(ctx context.Context, newHead H) error {
	sbjHead, _, err := s.subjectiveHead(ctx)
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
		s.setLocalHead(ctx, subjHead)

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
