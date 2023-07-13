package sync

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header/sync")

type metrics struct {
	totalSynced int64
}

func (s *Syncer[H]) InitMetrics() error {
	s.metrics = &metrics{}

	totalSynced, err := meter.Float64ObservableGauge(
		"total_synced_headers",
		metric.WithDescription("total synced headers"),
	)
	if err != nil {
		return err
	}

	callback := func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveFloat64(totalSynced, float64(atomic.LoadInt64(&s.metrics.totalSynced)))
		return nil
	}
	_, err = meter.RegisterCallback(callback, totalSynced)
	return err
}

// recordTotalSynced records the total amount of synced headers.
func (m *metrics) recordTotalSynced(totalSynced int) {
	if m != nil {
		atomic.AddInt64(&m.totalSynced, int64(totalSynced))
	}
}
