// TODO(@Wondertan): Should be just part of sync pkg and not subpkg
//
//	Fix after adjacency requirement is removed from the Store.
package verify

import (
	"errors"
	"fmt"
	"time"

	"github.com/celestiaorg/go-header"
)

// DefaultHeightThreshold defines default height threshold beyond which headers are rejected
// NOTE: Compared against subjective head which is guaranteed to be non-expired
const DefaultHeightThreshold int64 = 40000 // ~ 7 days of 15 second headers

// VerifyError is thrown during for Headers failed verification.
type VerifyError struct {
	// Reason why verification failed as inner error.
	Reason error
	// SoftFailure means verification did not have enough information to definitively conclude a
	// Header was correct or not.
	// May happen with recent Headers during unfinished historical sync or because of local errors.
	// TODO(@Wondertan): Better be part of signature Header.Verify() (bool, error), but kept here
	//  not to break
	SoftFailure bool
}

func (vr *VerifyError) Error() string {
	return fmt.Sprintf("header: verify: %s", vr.Reason.Error())
}

func (vr *VerifyError) Unwrap() error {
	return vr.Reason
}

// Verify verifies untrusted Header against trusted following general Header checks and
// custom user-specific checks defined in Header.Verify
//
// If heightThreshold is zero, uses DefaultHeightThreshold.
// Always returns VerifyError.
func Verify[H header.Header[H]](trstd, untrstd H, heightThreshold int64) error {
	// general mandatory verification
	err := verify[H](trstd, untrstd, heightThreshold)
	if err != nil {
		return &VerifyError{Reason: err}
	}
	// user defined verification
	err = trstd.Verify(untrstd)
	if err == nil {
		return nil
	}
	// if that's an error, ensure we always return VerifyError
	var verErr *VerifyError
	if !errors.As(err, &verErr) {
		verErr = &VerifyError{Reason: err}
	}
	// check adjacency of failed verification
	adjacent := untrstd.Height() == trstd.Height()+1
	if !adjacent {
		// if non-adjacent, we don't know if the header is *really* wrong
		// so set as soft
		verErr.SoftFailure = true
	}
	// we trust adjacent verification to it's fullest
	// if verification fails - the header is *really* wrong
	return verErr
}

// verify is a little bro of Verify yet performs mandatory Header checks
// for any Header implementation.
func verify[H header.Header[H]](trstd, untrstd H, heightThreshold int64) error {
	if heightThreshold == 0 {
		heightThreshold = DefaultHeightThreshold
	}

	if untrstd.IsZero() {
		return fmt.Errorf("zero header")
	}

	if untrstd.ChainID() != trstd.ChainID() {
		return fmt.Errorf("wrong header chain id %s, not %s", untrstd.ChainID(), trstd.ChainID())
	}

	if !untrstd.Time().After(trstd.Time()) {
		return fmt.Errorf("unordered header timestamp %v is before %v", untrstd.Time(), trstd.Time())
	}

	now := time.Now()
	if !untrstd.Time().Before(now.Add(clockDrift)) {
		return fmt.Errorf("header timestamp %v is from future (now: %v, clock_drift: %v)", untrstd.Time(), now, clockDrift)
	}

	known := untrstd.Height() <= trstd.Height()
	if known {
		return fmt.Errorf("known header height %d, current %d", untrstd.Height(), trstd.Height())
	}
	// reject headers with height too far from the future
	// this is essential for headers failed non-adjacent verification
	// yet taken as sync target
	adequateHeight := untrstd.Height()-trstd.Height() < heightThreshold
	if !adequateHeight {
		return fmt.Errorf("header height %d is far from future (current: %d, threshold: %d)", untrstd.Height(), trstd.Height(), heightThreshold)
	}

	return nil
}

// clockDrift defines how much new header's time can drift into
// the future relative to the now time during verification.
var clockDrift = 10 * time.Second
