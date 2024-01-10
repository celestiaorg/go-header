package sync

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header/sync")

type metrics struct {
	syncReg metric.Registration

	totalSynced     atomic.Int64
	totalSyncedInst metric.Int64ObservableGauge

	syncLoopStarted       metric.Int64Counter
	trustedPeersOutOfSync metric.Int64Counter
	laggingHeadersStart   metric.Int64Counter

	subjectiveHead     atomic.Int64
	subjectiveHeadInst metric.Int64ObservableGauge
	blockTime          metric.Float64Histogram
	headerReceived     time.Time
	prevHeader         time.Time

	headersThreshold time.Duration
}

func newMetrics(headersThreshold time.Duration) (*metrics, error) {
	totalSynced, err := meter.Int64ObservableGauge(
		"hdr_total_synced_headers",
metric.WithDescription("total synced headers shows how many headers have been synced since runtime"),
	)
	if err != nil {
		return nil, err
	}

	syncLoopStarted, err := meter.Int64Counter(
		"hdr_sync_loop_started",
metric.WithDescription("sync loop started records timestamp of a new sync job"),
	)
	if err != nil {
		return nil, err
	}

	trustedPeersOutOfSync, err := meter.Int64Counter(
		"hdr_tr_peers_out_of_sync",
		metric.WithDescription("trusted peers out of sync"),
	)
	if err != nil {
		return nil, err
	}

	laggingHeadersStart, err := meter.Int64Counter(
		"hdr_sync_lagging_hdr_start",
		metric.WithDescription("lagging header start"),
	)
	if err != nil {
		return nil, err
	}

	subjectiveHead, err := meter.Int64ObservableGauge(
		"hdr_sync_subjective_head",
		metric.WithDescription("subjective head height"),
	)
	if err != nil {
		return nil, err
	}

	blockTime, err := meter.Float64Histogram(
		"hdr_sync_actual_blockTime_ts",
		metric.WithDescription("duration between creation of 2 blocks"),
	)
	if err != nil {
		return nil, err
	}

	m := &metrics{
		totalSyncedInst:       totalSynced,
		syncLoopStarted:       syncLoopStarted,
		trustedPeersOutOfSync: trustedPeersOutOfSync,
		laggingHeadersStart:   laggingHeadersStart,
		blockTime:             blockTime,
		subjectiveHeadInst:    subjectiveHead,
		headersThreshold:      headersThreshold,
	}

	m.syncReg, err = meter.RegisterCallback(m.observeMetrics, m.totalSyncedInst, m.subjectiveHeadInst)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (m *metrics) observeMetrics(ctx context.Context, obs metric.Observer) error {
	err := m.observeTotalSynced(ctx, obs)
	if err != nil {
		return err
	}

	return m.observeNewHead(ctx, obs)
}

func (m *metrics) observeTotalSynced(_ context.Context, obs metric.Observer) error {
	obs.ObserveInt64(m.totalSyncedInst, m.totalSynced.Load())
	return nil
}

func (m *metrics) observeNewHead(_ context.Context, obs metric.Observer) error {
	obs.ObserveInt64(m.subjectiveHeadInst, m.subjectiveHead.Load())
	return nil
}

func (m *metrics) recordTotalSynced(totalSynced int) {
	m.observe(context.Background(), func(_ context.Context) {
		m.totalSynced.Add(int64(totalSynced))
	})
}

func (m *metrics) syncingStarted(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.syncLoopStarted.Add(ctx, 1)
	})
}

func (m *metrics) peersOutOufSync(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.trustedPeersOutOfSync.Add(ctx, 1)
	})
}

func (m *metrics) observeNewSubjectiveHead(ctx context.Context, height int64, timestamp time.Time) {
	m.observe(ctx, func(ctx context.Context) {
		m.subjectiveHead.Store(height)

		if !m.prevHeader.IsZero() {
			m.blockTime.Record(ctx, timestamp.Sub(m.prevHeader).Seconds())
		}

		if time.Since(m.headerReceived) > m.headersThreshold {
			m.laggingHeadersStart.Add(ctx, 1)
		}
		m.prevHeader = timestamp
		m.headerReceived = time.Now()
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
	return m.syncReg.Unregister()
}
