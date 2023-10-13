package p2p

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header/p2p")

type metrics struct {
	responseSize     metric.Float64Histogram
	responseDuration metric.Float64Histogram
}

func newExchangeMetrics() (*metrics, error) {
	responseSize, err := meter.Float64Histogram(
		"header_p2p_exchange_response_size",
		metric.WithDescription("Size of get headers response in bytes"),
	)
	if err != nil {
		return nil, err
	}
	responseDuration, err := meter.Float64Histogram(
		"header_p2p_exchange_request_duration",
		metric.WithDescription("Duration of get headers request in seconds"),
	)
	if err != nil {
		return nil, err
	}
	return &metrics{
		responseSize:     responseSize,
		responseDuration: responseDuration,
	}, nil
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
