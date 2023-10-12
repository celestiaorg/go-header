package p2p

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	statusKey = "status"
	sizeKey   = "size"
	reasonKey = "reason"
)

type subscriberMetrics struct {
	headerPropagationTime metric.Float64Histogram
	headerReceivedNum     metric.Int64Counter
	subscriptionNum       metric.Int64UpDownCounter
}

func newSubscriberMetrics() *subscriberMetrics {
	headerPropagationTime, err := meter.Float64Histogram(
		"header_p2p_subscriber_propagation_time",
		metric.WithDescription("header propagation time"),
	)
	if err != nil {
		panic(err)
	}
	headerReceivedNum, err := meter.Int64Counter(
		"header_p2p_subscriber_received_num",
		metric.WithDescription("header received from subscription counter"),
	)
	if err != nil {
		panic(err)
	}
	subscriptionNum, err := meter.Int64UpDownCounter(
		"header_p2p_subscriber_subscription_num",
		metric.WithDescription("number of active subscriptions"),
	)
	if err != nil {
		panic(err)
	}
	return &subscriberMetrics{
		headerPropagationTime: headerPropagationTime,
		headerReceivedNum:     headerReceivedNum,
		subscriptionNum:       subscriptionNum,
	}
}

func (m *subscriberMetrics) accept(ctx context.Context, duration time.Duration, size int) {
	m.observe(ctx, func(ctx context.Context) {
		m.headerReceivedNum.Add(ctx, 1, metric.WithAttributeSet(attribute.NewSet(
			attribute.String(statusKey, "accept"),
			attribute.Int64(sizeKey, int64(size)),
		)))

		m.headerPropagationTime.Record(ctx, duration.Seconds())
	})
}

func (m *subscriberMetrics) ignore(ctx context.Context, size int, reason error) {
	m.observe(ctx, func(ctx context.Context) {
		m.headerReceivedNum.Add(ctx, 1, metric.WithAttributes(
			attribute.String(statusKey, "ignore"),
			attribute.Int64(sizeKey, int64(size)),
			attribute.String(reasonKey, reason.Error()),
		))
	})
}

func (m *subscriberMetrics) reject(ctx context.Context, reason error) {
	m.observe(ctx, func(ctx context.Context) {
		m.headerReceivedNum.Add(ctx, 1, metric.WithAttributes(
			attribute.String(statusKey, "reject"),
			attribute.String(reasonKey, reason.Error()),
		))
	})
}

func (m *subscriberMetrics) subscription(ctx context.Context, num int) {
	m.observe(ctx, func(ctx context.Context) {
		m.subscriptionNum.Add(ctx, int64(num))
	})
}

func (m *subscriberMetrics) observe(ctx context.Context, observeFn func(context.Context)) {
	if m == nil {
		return
	}
	if ctx.Err() != nil {
		ctx = context.Background()
	}

	observeFn(ctx)
}
