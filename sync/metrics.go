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
	ctx context.Context

	totalSynced      atomic.Int64
	totalSyncedGauge metric.Float64ObservableGauge

	syncLoopStarted       metric.Int64Counter
	trustedPeersOutOfSync metric.Int64Counter
	laggingHeadersStart   metric.Int64Counter

	subjectiveHead  atomic.Int64
	headerTimestamp metric.Float64Histogram

	headerReceived time.Time
	prevHeader     time.Time

	headersThreshHold time.Duration
}

func newMetrics(headersThreshHold time.Duration) (*metrics, error) {
	totalSynced, err := meter.Float64ObservableGauge(
		"total_synced_headers",
		metric.WithDescription("total synced headers"),
	)
	if err != nil {
		return nil, err
	}

	syncLoopStarted, err := meter.Int64Counter("sync_loop_started", metric.WithDescription("sync loop started"))
	if err != nil {
		return nil, err
	}

	trustedPeersOutOfSync, err := meter.Int64Counter("tr_peers_out_of_sync", metric.WithDescription("trusted peers out of sync"))
	if err != nil {
		return nil, err
	}

	laggingHeadersStart, err := meter.Int64Counter("sync_lagging_hdr_start", metric.WithDescription("lagging header start"))
	if err != nil {
		return nil, err
	}

	subjectiveHead, err := meter.Int64ObservableGauge("sync_subjective_head", metric.WithDescription("subjective head height"))
	if err != nil {
		return nil, err
	}

	headerTimestamp, err := meter.Float64Histogram("sync_subjective_head_ts",
		metric.WithDescription("subjective_head_timestamp"))
	if err != nil {
		return nil, err
	}

	m := &metrics{
		ctx:                   context.Background(),
		totalSyncedGauge:      totalSynced,
		syncLoopStarted:       syncLoopStarted,
		trustedPeersOutOfSync: trustedPeersOutOfSync,
		laggingHeadersStart:   laggingHeadersStart,
		headerTimestamp:       headerTimestamp,
		headersThreshHold:     headersThreshHold,
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
	m.syncLoopStarted.Add(m.ctx, 1)
}

func (m *metrics) recordTrustedPeersOutOfSync() {
	if m == nil {
		return
	}
	m.trustedPeersOutOfSync.Add(m.ctx, 1)
}

func (m *metrics) observeNewHead(height int64) {
	if m == nil {
		return
	}
	m.subjectiveHead.Store(height)
}

func (m *metrics) observeLaggingHeader() {
	if m == nil {
		return
	}
	if time.Since(m.headerReceived) > m.headersThreshHold {
		m.laggingHeadersStart.Add(m.ctx, 1)
	}
	m.headerReceived = time.Now()
}

func (m *metrics) observeHeaderTimestamp(timestamp time.Time) {
	if m == nil {
		return
	}
	if !m.prevHeader.IsZero() {
		m.headerTimestamp.Record(m.ctx, timestamp.Sub(m.prevHeader).Seconds())
	}
	m.prevHeader = timestamp
}
