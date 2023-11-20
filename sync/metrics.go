package sync

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header/sync")

type metrics struct {
	networkHead      atomic.Int64
	networkHeadGauge metric.Int64ObservableGauge

	totalSynced      atomic.Int64
	totalSyncedGauge metric.Float64ObservableGauge
}

func newMetrics() (*metrics, error) {
	totalSynced, err := meter.Float64ObservableGauge(
		"total_synced_headers",
		metric.WithDescription("total synced headers"),
	)
	if err != nil {
		return nil, err
	}
	netHead, err := meter.Int64ObservableGauge(
		"network_head_gauge",
		metric.WithDescription("network head"),
	)
	if err != nil {
		return nil, err
	}

	m := &metrics{
		totalSyncedGauge: totalSynced,
		networkHeadGauge: netHead,
	}

	totalSyncedCallback := func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveFloat64(totalSynced, float64(m.totalSynced.Load()))
		return nil
	}
	_, err = meter.RegisterCallback(totalSyncedCallback, totalSynced)
	if err != nil {
		return nil, err
	}

	netHeadCallback := func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveInt64(netHead, m.networkHead.Load())
		return nil
	}
	_, err = meter.RegisterCallback(netHeadCallback, netHead)
	if err != nil {
		return nil, err
	}

	return m, nil
}

// recordTotalSynced records the total amount of synced headers.
func (m *metrics) recordTotalSynced(totalSynced int) {
	if m == nil {
		return
	}

	m.totalSynced.Add(int64(totalSynced))
}

func (m *metrics) recordNetworkHead(netHead uint64) {
	if m == nil {
		return
	}

	m.networkHead.Store(int64(netHead))
}
