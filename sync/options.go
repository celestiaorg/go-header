package sync

import (
	"fmt"
	"time"
)

// Options is the functional option that is applied to the Syner instance
// to configure its parameters.
// TODO(@Wondertan): rename to single Option in some breaking release
type Options func(*Parameters)

// Parameters is the set of parameters that must be configured for the syncer.
type Parameters struct {
	// TrustingPeriod is period through which we can trust a header's validators set.
	//
	// Should be significantly less than the unbonding period (e.g. unbonding
	// period = 3 weeks, trusting period = 2 weeks).
	//
	// More specifically, trusting period + time needed to check headers + time
	// needed to report and punish misbehavior should be less than the unbonding
	// period.
	TrustingPeriod time.Duration
	// blockTime provides a reference point for the Syncer to determine
	// whether its subjective head is outdated.
	// Keeping it private to disable serialization for it.
	// default value is set to 0 so syncer will constantly request networking head.
	blockTime time.Duration
}

// DefaultParameters returns the default params to configure the syncer.
func DefaultParameters() Parameters {
	return Parameters{
		TrustingPeriod: 168 * time.Hour,
	}
}

func (p *Parameters) Validate() error {
	if p.TrustingPeriod == 0 {
		return fmt.Errorf("invalid trusting period duration: %v", p.TrustingPeriod)
	}
	return nil
}

// WithBlockTime is a functional option that configures the
// `blockTime` parameter.
func WithBlockTime(duration time.Duration) Options {
	return func(p *Parameters) {
		p.blockTime = duration
	}
}

// WithTrustingPeriod is a functional option that configures the
// `TrustingPeriod` parameter.
func WithTrustingPeriod(duration time.Duration) Options {
	return func(p *Parameters) {
		p.TrustingPeriod = duration
	}
}

// WithParams is a functional option that overrides Parameters.
func WithParams(new Parameters) Options {
	return func(old *Parameters) {
		*old = new
	}
}
