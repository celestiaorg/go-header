package sync

import (
	"context"

	"go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/metric/instrument"
	"go.opentelemetry.io/otel/metric/instrument/syncfloat64"
)

var meter = global.MeterProvider().Meter("header/sync")

type metrics struct {
	totalSynced syncfloat64.Counter
}

// InitMetrics initializes internal Syncer's metrics.
func (s *Syncer[H]) InitMetrics() error {
	totalSynced, err := meter.SyncFloat64().Counter(
		"total_synced_headers",
		instrument.WithDescription("Total synced headers with a node run(not preserved after restart))"),
	)
	if err != nil {
		return err
	}

	s.metrics = &metrics{
		totalSynced: totalSynced,
	}
	return nil
}

// incrementSynced increments number of synced headers
func (m *metrics) incrementSynced(ctx context.Context, synced int) {
	if m != nil {
		m.totalSynced.Add(ctx, float64(synced))
	}
}
