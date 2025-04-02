package sync

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	otelattr "github.com/celestiaorg/go-header/internal/otelattr"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header/sync")

type metrics struct {
	syncerReg metric.Registration

	subjectiveHeadInst  metric.Int64ObservableGauge
	syncLoopRunningInst metric.Int64ObservableGauge

	syncLoopStarted       metric.Int64Counter
	trustedPeersOutOfSync metric.Int64Counter
	outdatedHeader        metric.Int64Counter
	subjectiveInit        metric.Int64Counter
	failedBifurcations    metric.Int64Counter

	subjectiveHead atomic.Uint64

	syncLoopDurationHist metric.Float64Histogram
	syncLoopActive       atomic.Int64
	syncStartedTS        time.Time

	requestRangeTimeHist metric.Float64Histogram
	requestRangeStartTS  time.Time

	blockTime  metric.Float64Histogram
	prevHeader time.Time
}

func newMetrics() (*metrics, error) {
	syncLoopStarted, err := meter.Int64Counter(
		"hdr_sync_loop_started_counter",
		metric.WithDescription("sync loop started shows that syncing is in progress"),
	)
	if err != nil {
		return nil, err
	}

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

	failedBifurcations, err := meter.Int64Counter(
		"hdr_failed_bifurcations_total",
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

	syncLoopDurationHist, err := meter.Float64Histogram(
		"hdr_sync_loop_time_hist",
		metric.WithDescription("tracks the duration of syncing"))
	if err != nil {
		return nil, err
	}

	requestRangeTimeHist, err := meter.Float64Histogram("hdr_sync_range_request_time_hist",
		metric.WithDescription("tracks the duration of GetRangeByHeight requests"))
	if err != nil {
		return nil, err
	}

	syncLoopRunningInst, err := meter.Int64ObservableGauge(
		"hdr_sync_loop_status_gauge",
		metric.WithDescription("reports whether syncing is active or not"))
	if err != nil {
		return nil, err
	}

	blockTime, err := meter.Float64Histogram(
		"hdr_sync_actual_blockTime_ts_hist",
		metric.WithDescription("duration between creation of 2 blocks"),
	)
	if err != nil {
		return nil, err
	}

	m := &metrics{
		syncLoopStarted:       syncLoopStarted,
		trustedPeersOutOfSync: trustedPeersOutOfSync,
		outdatedHeader:        outdatedHeader,
		subjectiveInit:        subjectiveInit,
		failedBifurcations:    failedBifurcations,
		syncLoopDurationHist:  syncLoopDurationHist,
		syncLoopRunningInst:   syncLoopRunningInst,
		requestRangeTimeHist:  requestRangeTimeHist,
		blockTime:             blockTime,
		subjectiveHeadInst:    subjectiveHead,
	}

	m.syncerReg, err = meter.RegisterCallback(
		m.observeMetrics,
		m.subjectiveHeadInst,
		m.syncLoopRunningInst,
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
	obs.ObserveInt64(m.syncLoopRunningInst, m.syncLoopActive.Load())
	return nil
}

func (m *metrics) syncStarted(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.syncStartedTS = time.Now()
		m.syncLoopStarted.Add(ctx, 1)
		m.syncLoopActive.Store(1)
	})
}

func (m *metrics) syncFinished(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.syncLoopActive.Store(0)
		m.syncLoopDurationHist.Record(ctx, time.Since(m.syncStartedTS).Seconds())
	})
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

func (m *metrics) updateGetRangeRequestInfo(ctx context.Context, amount uint64, failed bool) {
	m.observe(ctx, func(ctx context.Context) {
		m.requestRangeTimeHist.Record(ctx, time.Since(m.requestRangeStartTS).Seconds(),
			metric.WithAttributes(
				otelattr.Uint64("headers amount", amount),
				attribute.Bool("request failed", failed),
			))
	})
}

func (m *metrics) newSubjectiveHead(ctx context.Context, height uint64, timestamp time.Time) {
	m.observe(ctx, func(ctx context.Context) {
		m.subjectiveHead.Store(height)

		if !m.prevHeader.IsZero() {
			m.blockTime.Record(ctx, timestamp.Sub(m.prevHeader).Seconds())
		}
	})
}

func (m *metrics) failedBifurcation(ctx context.Context, height uint64, hash string) {
	m.observe(ctx, func(ctx context.Context) {
		m.failedBifurcations.Add(ctx, 1,
			metric.WithAttributes(
				attribute.Int64("height", int64(height)),
				attribute.String("hash", hash),
			),
		)
	})
}

func (m *metrics) rangeRequestStart() {
	if m == nil {
		return
	}
	m.requestRangeStartTS = time.Now()
}

func (m *metrics) rangeRequestStop() {
	if m == nil {
		return
	}
	m.requestRangeStartTS = time.Time{}
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
