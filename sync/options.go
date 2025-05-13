package sync

import (
	"fmt"
	"time"

	"github.com/celestiaorg/go-header"
)

// Option is the functional option that is applied to the Syner instance
// to configure its parameters.
type Option func(*Parameters)

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
	// SyncFromHash is the hash of the header from which the syncer should start syncing.
	//
	// By default, the syncer will start syncing from Tail, height of which is identified by the
	// network head time minus TrustingPeriod. SyncFromHash overrides this default, allowing
	// user to specify a custom starting point.
	//
	// SyncFromHash has higher priority than SyncFromHeight.
	SyncFromHash header.Hash
	// SyncFromHeight is the height of the header from which the syncer should start syncing.
	//
	// By default, the syncer will start syncing from Tail, height of which is identified by the
	// network head time minus TrustingPeriod. SyncFromHeight overrides this default, allowing
	// user to specify a custom starting point.
	//
	// SyncFromHeight has lower priority than SyncFromHash.
	SyncFromHeight uint64
	// PruningWindow defines the duration within which headers will be retained before being pruned.
	PruningWindow time.Duration
	// blockTime provides a reference point for the Syncer to determine
	// whether its subjective head is outdated.
	// Keeping it private to disable serialization for it.
	// default value is set to 0 so syncer will constantly request networking head.
	blockTime time.Duration
	// recencyThreshold describes the time period for which a header is
	// considered "recent". The default is blockTime + 5 seconds.
	recencyThreshold time.Duration
	// metrics is a flag that enables metrics collection.
	metrics bool
}

// DefaultParameters returns the default params to configure the syncer.
func DefaultParameters() Parameters {
	return Parameters{
		TrustingPeriod: 336 * time.Hour, // tendermint's default trusting period
		PruningWindow:  337 * time.Hour,
	}
}

func (p *Parameters) Validate() error {
	if p.TrustingPeriod == 0 {
		return fmt.Errorf("invalid trusting period duration: %v", p.TrustingPeriod)
	}
	return nil
}

// WithMetrics enables Metrics on Syncer.
func WithMetrics() Option {
	return func(p *Parameters) {
		p.metrics = true
	}
}

// WithBlockTime is a functional option that configures the
// `blockTime` parameter.
func WithBlockTime(duration time.Duration) Option {
	return func(p *Parameters) {
		p.blockTime = duration
	}
}

// WithRecencyThreshold is a functional option that configures the
// `recencyThreshold` parameter.
func WithRecencyThreshold(threshold time.Duration) Option {
	return func(p *Parameters) {
		p.recencyThreshold = threshold
	}
}

// WithTrustingPeriod is a functional option that configures the
// `TrustingPeriod` parameter.
func WithTrustingPeriod(duration time.Duration) Option {
	return func(p *Parameters) {
		p.TrustingPeriod = duration
	}
}

// WithParams is a functional option that overrides Parameters.
func WithParams(params Parameters) Option {
	return func(old *Parameters) {
		*old = params
	}
}

// WithSyncFromHash sets given header hash a starting point for syncing.
// See [Parameters.SyncFromHash] for details.
func WithSyncFromHash(hash header.Hash) Option {
	return func(p *Parameters) {
		p.SyncFromHash = hash
	}
}

// WithSyncFromHeight sets given height a starting point for syncing.
// See [Parameters.SyncFromHeight] for details.
func WithSyncFromHeight(height uint64) Option {
	return func(p *Parameters) {
		p.SyncFromHeight = height
	}
}

// WithPruningWindow sets the duration within which headers will be retained before being pruned.
func WithPruningWindow(window time.Duration) Option {
	return func(p *Parameters) {
		p.PruningWindow = window
	}
}
