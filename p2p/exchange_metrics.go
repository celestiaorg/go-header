package p2p

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("header/p2p")

const (
	failedKey           = "failed"
	headerReceivedKey   = "num_headers_received"
	headTypeKey         = "request_type"
	headTypeTrusted     = "trusted_request"
	headTypeUntrusted   = "untrusted_request"
	headStatusKey       = "status"
	headStatusOk        = "ok"
	headStatusTimeout   = "timeout"
	headStatusCanceled  = "canceled"
	headStatusNoHeaders = "no_headers"
)

type exchangeMetrics struct {
	headRequestTimeInst metric.Int64Histogram
	responseSizeInst    metric.Int64Histogram
	responseTimeInst    metric.Float64Histogram
	blockedPeersNum     metric.Int64Counter

	trackerPeersNum     atomic.Int64
	trackedPeersNumInst metric.Int64ObservableGauge
	trackedPeersNumReg  metric.Registration

	disconnectedPeersNum     atomic.Int64
	disconnectedPeersNumInst metric.Int64ObservableGauge
	disconnectedPeersNumReg  metric.Registration
}

func newExchangeMetrics() (m *exchangeMetrics, err error) {
	m = new(exchangeMetrics)
	m.headRequestTimeInst, err = meter.Int64Histogram(
		"hdr_p2p_exch_clnt_head_time_hist",
		metric.WithDescription("exchange client head request time in milliseconds"),
	)
	if err != nil {
		return nil, err
	}
	m.responseSizeInst, err = meter.Int64Histogram(
		"hdr_p2p_exch_clnt_resp_size_hist",
		metric.WithDescription("exchange client header response size in bytes"),
	)
	if err != nil {
		return nil, err
	}
	m.responseTimeInst, err = meter.Float64Histogram(
		"hdr_p2p_exch_clnt_resp_time_hist",
		metric.WithDescription("exchange client response time in milliseconds"),
	)
	if err != nil {
		return nil, err
	}
	m.trackedPeersNumInst, err = meter.Int64ObservableGauge(
		"hdr_p2p_exch_clnt_trck_peer_num_gauge",
		metric.WithDescription("exchange client tracked peers number"),
	)
	if err != nil {
		return nil, err
	}
	m.trackedPeersNumReg, err = meter.RegisterCallback(m.observeTrackedPeers, m.trackedPeersNumInst)
	if err != nil {
		return nil, err
	}
	m.disconnectedPeersNumInst, err = meter.Int64ObservableGauge(
		"hdr_p2p_exch_clnt_disc_peer_num_gauge",
		metric.WithDescription("exchange client tracked disconnected peers number"),
	)
	if err != nil {
		return nil, err
	}
	m.disconnectedPeersNumReg, err = meter.RegisterCallback(m.observeDisconnectedPeers, m.disconnectedPeersNumInst)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (m *exchangeMetrics) head(ctx context.Context, duration time.Duration, headersReceived int, tp, status string) {
	m.observe(ctx, func(ctx context.Context) {
		m.headRequestTimeInst.Record(ctx,
			duration.Milliseconds(),
			metric.WithAttributes(
				attribute.Int(headerReceivedKey, headersReceived),
				attribute.String(headTypeKey, tp),
				attribute.String(headStatusKey, status),
			),
		)
	})
}

func (m *exchangeMetrics) response(ctx context.Context, size uint64, duration time.Duration, err error) {
	m.observe(ctx, func(ctx context.Context) {
		m.responseSizeInst.Record(ctx,
			int64(size),
			metric.WithAttributes(attribute.Bool(failedKey, err != nil)),
		)
		m.responseTimeInst.Record(ctx,
			duration.Seconds(),
			metric.WithAttributes(attribute.Bool(failedKey, err != nil)),
		)
	})
}

func (m *exchangeMetrics) peersTracked(num int) {
	m.observe(context.Background(), func(context.Context) {
		m.trackerPeersNum.Add(int64(num))
	})
}

func (m *exchangeMetrics) peersDisconnected(num int) {
	m.observe(context.Background(), func(context.Context) {
		m.disconnectedPeersNum.Add(int64(num))
	})
}

func (m *exchangeMetrics) peerBlocked(ctx context.Context) {
	m.observe(ctx, func(ctx context.Context) {
		m.blockedPeersNum.Add(ctx, 1)
	})
}

func (m *exchangeMetrics) observeTrackedPeers(_ context.Context, obs metric.Observer) error {
	obs.ObserveInt64(m.trackedPeersNumInst, m.trackerPeersNum.Load())
	return nil
}

func (m *exchangeMetrics) observeDisconnectedPeers(_ context.Context, obs metric.Observer) error {
	obs.ObserveInt64(m.disconnectedPeersNumInst, m.disconnectedPeersNum.Load())
	return nil
}

func (m *exchangeMetrics) observe(ctx context.Context, observeFn func(context.Context)) {
	if m == nil {
		return
	}
	if ctx.Err() != nil {
		ctx = context.Background()
	}

	observeFn(ctx)
}

func (m *exchangeMetrics) Close() (err error) {
	if m == nil {
		return nil
	}

	err = errors.Join(err, m.trackedPeersNumReg.Unregister())
	err = errors.Join(err, m.disconnectedPeersNumReg.Unregister())
	return err
}
