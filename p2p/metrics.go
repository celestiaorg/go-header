package p2p

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type metrics struct {
	responseSize     metric.Float64Histogram
	responseDuration metric.Float64Histogram
}

var (
	meter = otel.Meter("header/p2p")
)

func (ex *Exchange[H]) InitMetrics() error {
	responseSize, err := meter.Float64Histogram(
		"header_p2p_headers_response_size",
		metric.WithDescription("Size of get headers response in bytes"),
	)
	if err != nil {
		return err
	}

	responseDuration, err := meter.Float64Histogram(
		"header_p2p_headers_request_duration",
		metric.WithDescription("Duration of get headers request in seconds"),
	)
	if err != nil {
		return err
	}

	ex.metrics = &metrics{
		responseSize:     responseSize,
		responseDuration: responseDuration,
	}
	return nil
}

func (m *metrics) observeResponse(ctx context.Context, size uint64, duration uint64, err error) {
	if m == nil {
		return
	}
	m.responseSize.Record(ctx, float64(size),
		metric.WithAttributes(attribute.Bool("failed", err != nil)),
	)
	m.responseDuration.Record(ctx, float64(duration),
		metric.WithAttributes(attribute.Bool("failed", err != nil)),
	)
}
