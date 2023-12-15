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
	totalSynced      atomic.Int64
	totalSyncedGauge metric.Float64ObservableGauge

	syncLoopStarted       metric.Int64Counter
	trustedPeersOutOfSync metric.Int64Counter
	laggingHeadersStart   metric.Int64Counter

	subjectiveHead atomic.Int64
	blockTime      metric.Float64Histogram

	headerReceived time.Time
	prevHeader     time.Time

	headersThreshold time.Duration
}

func newMetrics(headersThreshold time.Duration) (*metrics, error) {
	totalSynced, err := meter.Float64ObservableGauge(
		"hdr_total_synced_headers",
		metric.WithDescription("total synced headers"),
	)
	if err != nil {
		return nil, err
	}

	syncLoopStarted, err := meter.Int64Counter(
		"hdr_sync_loop_started",
		metric.WithDescription("sync loop started"),
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
		totalSyncedGauge:      totalSynced,
		syncLoopStarted:       syncLoopStarted,
		trustedPeersOutOfSync: trustedPeersOutOfSync,
		laggingHeadersStart:   laggingHeadersStart,
		blockTime:             blockTime,
		headersThreshold:      headersThreshold,
	}

	callback := func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveFloat64(totalSynced, float64(m.totalSynced.Load()))
		observer.ObserveInt64(subjectiveHead, m.subjectiveHead.Load())
		return nil
	}

	_, err = meter.RegisterCallback(callback, totalSynced, subjectiveHead)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (m *metrics) recordTotalSynced(totalSynced int) {
	if m == nil {
		return
	}
	m.totalSynced.Add(int64(totalSynced))
}

func (m *metrics) recordSyncLoopStarted() {
	if m == nil {
		return
	}
	m.syncLoopStarted.Add(context.Background(), 1)
}

func (m *metrics) recordTrustedPeersOutOfSync() {
	if m == nil {
		return
	}
	m.trustedPeersOutOfSync.Add(context.Background(), 1)
}

func (m *metrics) observeNewSubjectiveHead(height int64, timestamp time.Time) {
	if m == nil {
		return
	}
	m.subjectiveHead.Store(height)

	ctx := context.Background()
	if !m.prevHeader.IsZero() {
		m.blockTime.Record(ctx, timestamp.Sub(m.prevHeader).Seconds())
	}
	m.prevHeader = timestamp

	if time.Since(m.headerReceived) > m.headersThreshold {
		m.laggingHeadersStart.Add(ctx, 1)
	}
	m.headerReceived = time.Now()
}
