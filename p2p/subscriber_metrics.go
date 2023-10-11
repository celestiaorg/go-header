package p2p

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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

func (m *subscriberMetrics) observeAccept(ctx context.Context, duration time.Duration, size int) {
	m.observe(ctx, func(ctx context.Context) {
		m.headerReceivedNum.Add(ctx, 1, metric.WithAttributeSet(attribute.NewSet(
			attribute.String("status", "accept"),
			attribute.Int64("size", int64(size)),
		)))

		m.headerPropagationTime.Record(ctx, duration.Seconds())
	})
}

func (m *subscriberMetrics) observeIgnore(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.headerReceivedNum.Add(ctx, 1, metric.WithAttributes(
			attribute.String("status", "ignore"),
			// TODO(@Wondertan): Do we wanna track reason string and sizes?
		))
	})
}

func (m *subscriberMetrics) observeReject(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.headerReceivedNum.Add(ctx, 1, metric.WithAttributes(
			attribute.String("status", "reject"),
			// TODO(@Wondertan): Do we wanna track reason string and sizes?
		))
	})
}

func (m *subscriberMetrics) observeSubscription(ctx context.Context, num int) {
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
