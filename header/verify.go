package header

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/tendermint/tendermint/light"

	libhead "github.com/celestiaorg/celestia-node/libs/header"
)

// TODO(@Wondertan): We should request TrustingPeriod from the network's state params or
//  listen for network params changes to always have a topical value.

// TrustingPeriod is period through which we can trust a header's validators set.
//
// Should be significantly less than the unbonding period (e.g. unbonding
// period = 3 weeks, trusting period = 2 weeks).
//
// More specifically, trusting period + time needed to check headers + time
// needed to report and punish misbehavior should be less than the unbonding
// period.
var TrustingPeriod = 168 * time.Hour

// IsExpired checks if header is expired against trusting period.
func (eh *ExtendedHeader) IsExpired() bool {
	expirationTime := eh.Time().Add(TrustingPeriod)
	return !expirationTime.After(time.Now())
}

// IsRecent checks if header is recent against the given blockTime.
func (eh *ExtendedHeader) IsRecent(blockTime time.Duration) bool {
	return time.Since(eh.Time()) <= blockTime // TODO @renaynay: should we allow for a 5-10 block drift here?
}

// VerifyNonAdjacent validates non-adjacent untrusted header against trusted 'eh'.
func (eh *ExtendedHeader) VerifyNonAdjacent(untrusted libhead.Header) error {
	untrst, ok := untrusted.(*ExtendedHeader)
	if !ok {
		return &libhead.VerifyError{Reason: errors.New("invalid header type: expected *ExtendedHeader")}
	}
	if err := eh.verify(untrst); err != nil {
		return &libhead.VerifyError{Reason: err}
	}

	// Ensure that untrusted commit has enough of trusted commit's power.
	err := eh.ValidatorSet.VerifyCommitLightTrusting(eh.ChainID(), untrst.Commit, light.DefaultTrustLevel)
	if err != nil {
		return &libhead.VerifyError{Reason: err}
	}

	return nil
}

// VerifyAdjacent validates adjacent untrusted header against trusted 'eh'.
func (eh *ExtendedHeader) VerifyAdjacent(untrusted libhead.Header) error {
	untrst, ok := untrusted.(*ExtendedHeader)
	if !ok {
		return &libhead.VerifyError{Reason: errors.New("invalid header type: expected *ExtendedHeader")}
	}
	if untrst.Height() != eh.Height()+1 {
		return &libhead.VerifyError{Reason: &libhead.ErrNonAdjacent{
			Head:      eh.Height(),
			Attempted: untrst.Height(),
		}}
	}

	if err := eh.verify(untrst); err != nil {
		return &libhead.VerifyError{Reason: err}
	}

	// Check the validator hashes are the same
	if !bytes.Equal(untrst.ValidatorsHash, eh.NextValidatorsHash) {
		return &libhead.VerifyError{
			Reason: fmt.Errorf("expected old header next validators (%X) to match those from new header (%X)",
				eh.NextValidatorsHash,
				untrst.ValidatorsHash,
			),
		}
	}

	return nil
}

// clockDrift defines how much new header's time can drift into
// the future relative to the now time during verification.
var clockDrift = 10 * time.Second

// verify performs basic verification of untrusted header.
func (eh *ExtendedHeader) verify(untrst *ExtendedHeader) error {
	if untrst.ChainID() != eh.ChainID() {
		return fmt.Errorf("new untrusted header has different chain %s, not %s", untrst.ChainID(), eh.ChainID())
	}

	if !untrst.Time().After(eh.Time()) {
		return fmt.Errorf("expected new untrusted header time %v to be after old header time %v", untrst.Time(), eh.Time())
	}

	now := time.Now()
	if !untrst.Time().Before(now.Add(clockDrift)) {
		return fmt.Errorf(
			"new untrusted header has a time from the future %v (now: %v, clockDrift: %v)", untrst.Time(), now, clockDrift)
	}

	return nil
}
