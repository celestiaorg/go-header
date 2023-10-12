package sync

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header/sync")

type metrics struct {
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

	m := &metrics{
		totalSyncedGauge: totalSynced,
	}

	callback := func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveFloat64(totalSynced, float64(m.totalSynced.Load()))
		return nil
	}
	_, err = meter.RegisterCallback(callback, totalSynced)
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

	m.totalSynced.Store(int64(totalSynced))
}
