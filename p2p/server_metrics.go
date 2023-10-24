package p2p

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const headersServedKey = "num_headers_served"

type serverMetrics struct {
	headersServed    metric.Int64Counter
	headServeTimeInst metric.Int64Histogram
	rangeServeTimeInst metric.Int64Histogram
	getServeTimeInst metric.Int64Histogram
}

func newServerMetrics() (m *serverMetrics, err error) {
	m = new(serverMetrics)
	m.headersServed, err = meter.Int64Counter(
		"hdr_p2p_exch_srvr_headers_served",
		metric.WithDescription("number of headers served"),
	)
	if err != nil {
		return nil, err
	}
	m.headServeTimeInst, err = meter.Int64Histogram(
		"hdr_p2p_exch_srvr_head_serve_time_hist",
		metric.WithDescription("exchange server head serve time in milliseconds"),
	)
	if err != nil {
		return nil, err
	}
	m.rangeServeTimeInst, err = meter.Int64Histogram(
		"hdr_p2p_exch_srvr_range_serve_time_hist",
		metric.WithDescription("exchange server range serve time in milliseconds"),
	)
	if err != nil {
		return nil, err
	}
	m.getServeTimeInst, err = meter.Int64Histogram(
		"hdr_p2p_exch_srvr_get_serve_time_hist",
		metric.WithDescription("exchange server get serve time in milliseconds"),
	)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (m *serverMetrics) headServed(ctx context.Context, duration time.Duration) {
	m.observe(ctx, func(ctx context.Context) {
		m.headersServed.Add(ctx, 1)
		m.headServeTimeInst.Record(ctx, duration.Milliseconds())
	})
}

func (m *serverMetrics) rangeServed(ctx context.Context, duration time.Duration, headersServed int) {
	m.observe(ctx, func(ctx context.Context) {
		m.headersServed.Add(ctx, int64(headersServed))
		m.rangeServeTimeInst.Record(ctx,
			duration.Milliseconds(),
			metric.WithAttributes(attribute.Int(headersServedKey, headersServed)),
		)
	})
}

func (m *serverMetrics) getServed(ctx context.Context, duration time.Duration) {
	m.observe(ctx, func(ctx context.Context) {
		m.headersServed.Add(ctx, 1)
		m.getServeTimeInst.Record(ctx, duration.Milliseconds())
	})
}

func (m *serverMetrics) observe(ctx context.Context, f func(context.Context)) {
	if m == nil {
		return
	}

	if ctx.Err() != nil {
		ctx = context.Background()
	}

	f(ctx)
}