package header

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header")

// WithMetrics enables Otel metrics to monitor head and total amount of synced headers.
func WithMetrics[H Header[H]](store Store[H]) error {
	headC, _ := meter.Int64ObservableCounter(
		"head",
		metric.WithDescription("Subjective head of the node"),
	)

	callback := func(ctx context.Context, observer metric.Observer) error {
		// add timeout to limit the time it takes to get the head
		// in case there is a deadlock
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()

		head, err := store.Head(ctx)
		if err != nil {
			observer.ObserveInt64(headC, 0,
				metric.WithAttributes(
					attribute.String("err", err.Error())))
		} else {
			observer.ObserveInt64(headC, int64(head.Height()))
		}
		return nil
	}
	_, err := meter.RegisterCallback(callback, headC)
	return err
}
