package sync

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/celestiaorg/go-header"
)

var meter = otel.Meter("header/sync")

type metrics struct {
	syncerReg metric.Registration

	trustedPeersOutOfSync metric.Int64Counter
	outdatedHeader        metric.Int64Counter
	subjectiveInit        metric.Int64Counter

	bifurcations       metric.Int64Counter
	failedBifurcations metric.Int64Counter

	subjectiveHeadInst metric.Int64ObservableGauge
	subjectiveHead     atomic.Uint64

	requestRangeTimeHist metric.Float64Histogram
}

func newMetrics() (*metrics, error) {
	trustedPeersOutOfSync, err := meter.Int64Counter(
		"hdr_sync_trust_peers_out_of_sync_counter",
		metric.WithDescription("trusted peers out of sync and gave outdated header"),
	)
	if err != nil {
		return nil, err
	}

	outdatedHeader, err := meter.Int64Counter(
		"hdr_sync_outdated_head_counter",
		metric.WithDescription("tracks every time Syncer returns an outdated head"),
	)
	if err != nil {
		return nil, err
	}

	subjectiveInit, err := meter.Int64Counter(
		"hdr_sync_subjective_init_counter",
		metric.WithDescription(
			"tracks how many times is the node initialized ",
		),
	)
	if err != nil {
		return nil, err
	}

	bifurcations, err := meter.Int64Counter(
		"hdr_bifurcations_counter",
		metric.WithDescription(
			"tracks total number of bifurcations",
		),
	)
	if err != nil {
		return nil, err
	}

	failedBifurcations, err := meter.Int64Counter(
		"hdr_failed_bifurcations_counter",
		metric.WithDescription(
			"tracks how many times bifurcation failed against subjective head",
		),
	)
	if err != nil {
		return nil, err
	}

	subjectiveHead, err := meter.Int64ObservableGauge(
		"hdr_sync_subjective_head_gauge",
		metric.WithDescription("subjective head height"),
	)
	if err != nil {
		return nil, err
	}

	requestRangeTimeHist, err := meter.Float64Histogram("hdr_sync_range_request_time_hist",
		metric.WithDescription("tracks the duration of GetRangeByHeight requests"))
	if err != nil {
		return nil, err
	}

	m := &metrics{
		trustedPeersOutOfSync: trustedPeersOutOfSync,
		outdatedHeader:        outdatedHeader,
		subjectiveInit:        subjectiveInit,
		bifurcations:          bifurcations,
		failedBifurcations:    failedBifurcations,
		requestRangeTimeHist:  requestRangeTimeHist,
		subjectiveHeadInst:    subjectiveHead,
	}

	m.syncerReg, err = meter.RegisterCallback(
		m.observeMetrics,
		m.subjectiveHeadInst,
	)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (m *metrics) observeMetrics(_ context.Context, obs metric.Observer) error {
	headHeight := m.subjectiveHead.Load()
	if headHeight > math.MaxInt64 {
		return fmt.Errorf("height overflows int64: %d", headHeight)
	}
	obs.ObserveInt64(m.subjectiveHeadInst, int64(headHeight))
	return nil
}

func (m *metrics) outdatedHead(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.outdatedHeader.Add(ctx, 1)
	})
}

func (m *metrics) trustedPeersOutOufSync(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.trustedPeersOutOfSync.Add(ctx, 1)
	})
}

func (m *metrics) subjectiveInitialization(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.subjectiveInit.Add(ctx, 1)
	})
}

func (m *metrics) newSubjectiveHead(ctx context.Context, height uint64) {
	m.observe(ctx, func(_ context.Context) {
		m.subjectiveHead.Store(height)
	})
}

func (m *metrics) bifurcation(ctx context.Context, height uint64, hash string) {
	m.observe(ctx, func(ctx context.Context) {
		m.bifurcations.Add(ctx, 1,
			metric.WithAttributes(
				attribute.Int64("height", int64(height)), //nolint:gosec
				attribute.String("hash", hash),
			),
		)
	})
}

func (m *metrics) failedBifurcation(ctx context.Context, height uint64, hash string) {
	m.observe(ctx, func(ctx context.Context) {
		m.failedBifurcations.Add(ctx, 1,
			metric.WithAttributes(
				attribute.Int64("height", int64(height)), //nolint:gosec
				attribute.String("hash", hash),
			),
		)
	})
}

// recordRangeRequest will record the duration it takes to request a batch of
// headers. It will also record whether the range request was the full
// MaxRangeRequestSize (64) or not, giving us a lower-cardinality way to derive
// sync speed.
func (m *metrics) recordRangeRequest(ctx context.Context, duration time.Duration, size uint64) {
	if m == nil {
		return
	}
	m.observe(ctx, func(ctx context.Context) {
		m.requestRangeTimeHist.Record(ctx, duration.Seconds(),
			metric.WithAttributes(
				attribute.Bool("is_max_range_req_size", size == header.MaxRangeRequestSize),
			),
		)
	})
}

func (m *metrics) observe(ctx context.Context, observeFn func(context.Context)) {
	if m == nil {
		return
	}
	if ctx.Err() != nil {
		ctx = context.Background()
	}
	observeFn(ctx)
}

func (m *metrics) Close() error {
	if m == nil {
		return nil
	}
	return m.syncerReg.Unregister()
}
