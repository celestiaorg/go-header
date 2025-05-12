package store

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header/store")

type metrics struct {
	headHeight     atomic.Uint64
	tailHeight     atomic.Uint64
	headHeightInst metric.Int64ObservableGauge
	tailHeightInst metric.Int64ObservableGauge
	heightReg      metric.Registration

	flushTimeInst metric.Float64Histogram
	readTimeInst  metric.Float64Histogram

	writesQueueBlockedInst metric.Int64Counter
}

func newMetrics() (m *metrics, err error) {
	m = new(metrics)
	m.headHeightInst, err = meter.Int64ObservableGauge(
		"hdr_store_head_height_gauge",
		metric.WithDescription("current header store head height(subjective height)"),
	)
	if err != nil {
		return nil, err
	}
	m.tailHeightInst, err = meter.Int64ObservableGauge(
		"hdr_store_tail_height_gauge",
		metric.WithDescription("current header store tail height"),
	)
	if err != nil {
		return nil, err
	}
	m.heightReg, err = meter.RegisterCallback(m.observeHeight, m.headHeightInst, m.tailHeightInst)
	if err != nil {
		return nil, err
	}
	m.flushTimeInst, err = meter.Float64Histogram(
		"hdr_store_flush_time_hist",
		metric.WithDescription("header store flush time in seconds"),
	)
	if err != nil {
		return nil, err
	}
	m.readTimeInst, err = meter.Float64Histogram(
		"hdr_store_read_time_hist",
		metric.WithDescription(
			"header store single header read time from datastore in seconds and ignoring cache",
		),
	)
	if err != nil {
		return nil, err
	}
	m.writesQueueBlockedInst, err = meter.Int64Counter(
		"hdr_store_writes_blocked_counter",
		metric.WithDescription("header store writes blocked counter"),
	)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (m *metrics) newHead(height uint64) {
	m.observe(context.Background(), func(context.Context) {
		m.headHeight.Store(height)
	})
}

func (m *metrics) newTail(height uint64) {
	m.observe(context.Background(), func(context.Context) {
		m.headHeight.Store(height)
	})
}

func (m *metrics) observeHeight(_ context.Context, obs metric.Observer) error {
	headHeight := m.headHeight.Load()
	if headHeight > math.MaxInt64 {
		return fmt.Errorf("height overflows int64: %d", headHeight)
	}

	tailHeight := m.tailHeight.Load()
	if tailHeight > headHeight {
		return fmt.Errorf(
			"tail height is bigger then head height: %d vs %d",
			tailHeight,
			headHeight,
		)
	}

	obs.ObserveInt64(m.headHeightInst, int64(headHeight))
	obs.ObserveInt64(m.tailHeightInst, int64(tailHeight)) //nolint:gosec
	return nil
}

func (m *metrics) flush(ctx context.Context, duration time.Duration, amount int, failed bool) {
	m.observe(ctx, func(ctx context.Context) {
		m.flushTimeInst.Record(ctx,
			duration.Seconds(),
			metric.WithAttributes(
				attribute.Int("amount", amount/100), // divide by 100 to reduce cardinality
				attribute.Bool("failed", failed),
			),
		)
	})
}

func (m *metrics) readSingle(ctx context.Context, duration time.Duration, failed bool) {
	m.observe(ctx, func(ctx context.Context) {
		m.readTimeInst.Record(ctx,
			duration.Seconds(),
			metric.WithAttributes(attribute.Bool("failed", failed)),
		)
	})
}

func (m *metrics) writesQueueBlocked(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.writesQueueBlockedInst.Add(ctx, 1)
	})
}

func (m *metrics) observe(ctx context.Context, f func(context.Context)) {
	if m == nil {
		return
	}

	if ctx.Err() != nil {
		ctx = context.Background()
	}

	f(ctx)
}

func (m *metrics) Close() error {
	if m == nil {
		return nil
	}

	return m.heightReg.Unregister()
}
