package header

import "fmt"

// VerifyError is thrown during for Headers failed verification.
type VerifyError struct {
	// Reason why verification failed as inner error.
	Reason error
	// Uncertain signals that the was not enough information to conclude a Header is correct or not.
	// May happen with recent Headers during unfinished historical sync or because of local errors.
	Uncertain bool
}

func (vr *VerifyError) Error() string {
	return fmt.Sprintf("header: verify: %s", vr.Reason.Error())
}

func (vr *VerifyError) Unwrap() error {
	return vr.Reason
}
