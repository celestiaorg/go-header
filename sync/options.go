package sync

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/celestiaorg/go-header"
)

// Option is the functional option that is applied to the Syner instance
// to configure its parameters.
type Option func(*Parameters)

// Parameters is the set of parameters that must be configured for the syncer.
type Parameters struct {
	// PruningWindow defines the duration within which headers are retained before being pruned.
	PruningWindow time.Duration
	// SyncFromHash is the hash of the header from which Syncer should start syncing.
	// Zero value to disable. Value updates up and down the chain are gracefully handled by Syncer.
	//
	// By default, Syncer maintains PruningWindow number of headers. SyncFromHash overrides this default,
	// allowing any user to specify a custom starting point.
	//
	// SyncFromHash has higher priority than SyncFromHeight.
	SyncFromHash string
	// SyncFromHeight is the height of the header from which Syncer should start syncing.
	// Zero value to disable. Value updates up and down the chain are gracefully handled by Syncer.
	//
	// By default, Syncer maintains PruningWindow number of headers. SyncFromHeight overrides this default,
	// allowing any user to specify a custom starting point.
	//
	// SyncFromHeight has lower priority than SyncFromHash.
	SyncFromHeight uint64
	// trustingPeriod is period through which we can trust a header's validators set.
	//
	// Should be significantly less than the unbonding period (e.g. unbonding
	// period = 3 weeks, trusting period = 2 weeks).
	//
	// More specifically, trusting period + time needed to check headers + time
	// needed to report and punish misbehavior should be less than the unbonding
	// period.
	trustingPeriod time.Duration
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
	// hash cache
	hash header.Hash
}

// DefaultParameters returns the default params to configure the syncer.
func DefaultParameters() Parameters {
	return Parameters{
		trustingPeriod: 336 * time.Hour, // tendermint's default trusting period
		PruningWindow:  337 * time.Hour,
	}
}

func (p *Parameters) Validate() error {
	if p.trustingPeriod == 0 {
		return fmt.Errorf("invalid trustingPeriod duration: %v", p.trustingPeriod)
	}
	if p.SyncFromHash == "" && p.PruningWindow == 0 && p.SyncFromHeight == 0 {
		return fmt.Errorf(
			"at least one of SyncFromHeight, SyncFromHash, or PruningWindow must be set",
		)
	}
	_, err := p.Hash()
	if err != nil {
		return err
	}
	return nil
}

func (p *Parameters) Hash() (header.Hash, error) {
	if p.hash == nil && len(p.SyncFromHash) > 0 {
		hash, err := hex.DecodeString(p.SyncFromHash)
		if err != nil {
			return nil, fmt.Errorf("invalid SyncFromHash: %w", err)
		}

		p.hash = hash
	}
	return p.hash, nil
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
// `trustingPeriod` parameter.
func WithTrustingPeriod(duration time.Duration) Option {
	return func(p *Parameters) {
		p.trustingPeriod = duration
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
func WithSyncFromHash(hash string) Option {
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
