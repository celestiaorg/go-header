package header

import (
	"errors"
	"fmt"
	"time"
)

// Verify verifies untrusted Header against trusted following general Header checks and
// custom user-specific checks defined in Header.Verify.
//
// Given headers must be non-zero
// Always returns VerifyError.
func Verify[H Header[H]](trstd, untrstd H) error {
	// general mandatory verification
	err := verify[H](trstd, untrstd)
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
func verify[H Header[H]](trstd, untrstd H) error {
	if trstd.IsZero() {
		return ErrZeroHeader
	}
	if untrstd.IsZero() {
		return ErrZeroHeader
	}

	if untrstd.ChainID() != trstd.ChainID() {
		return fmt.Errorf("%w: '%s' != '%s'", ErrWrongChainID, untrstd.ChainID(), trstd.ChainID())
	}

	known := untrstd.Height() <= trstd.Height()
	if known {
		return fmt.Errorf("%w: '%d' <= current '%d'", ErrKnownHeader, untrstd.Height(), trstd.Height())
	}

	if untrstd.Time().Before(trstd.Time()) {
		return fmt.Errorf("%w: timestamp '%s' < current '%s'", ErrUnorderedTime, formatTime(untrstd.Time()), formatTime(trstd.Time()))
	}

	now := time.Now()
	if untrstd.Time().After(now.Add(clockDrift)) {
		return fmt.Errorf("%w: timestamp '%s' > now '%s', clock_drift '%v'", ErrFromFuture, formatTime(untrstd.Time()), formatTime(now), clockDrift)
	}

	return nil
}

var (
	ErrZeroHeader    = errors.New("zero header")
	ErrWrongChainID  = errors.New("wrong chain id")
	ErrUnorderedTime = errors.New("unordered headers")
	ErrFromFuture    = errors.New("header is from the future")
	ErrKnownHeader   = errors.New("known header")
)

// VerifyError is thrown if a Header failed verification.
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
	return fmt.Sprintf("header verification failed: %s", vr.Reason)
}

func (vr *VerifyError) Unwrap() error {
	return vr.Reason
}

// clockDrift defines how much new header's time can drift into
// the future relative to the now time during verification.
var clockDrift = 10 * time.Second

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
