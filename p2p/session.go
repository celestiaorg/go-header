package p2p

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/protocol"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/internal/otelattr"
	p2p_pb "github.com/celestiaorg/go-header/p2p/pb"
)

var tracerSession = otel.Tracer("header/p2p-session")

// errEmptyResponse means that server side closes the connection without sending at least 1
// response.
var errEmptyResponse = errors.New("empty response")

type option[H header.Header[H]] func(*session[H])

func withValidation[H header.Header[H]](from H) option[H] {
	return func(s *session[H]) {
		s.from = from
	}
}

// session aims to divide a range of headers
// into several smaller requests among different peers.
type session[H header.Header[H]] struct {
	host       host.Host
	protocolID protocol.ID
	queue      *peerQueue
	// peerTracker contains discovered peers with records that describes their activity.
	peerTracker *peerTracker
	metrics     *exchangeMetrics

	// Otherwise, it will be nil.
	// `from` is set when additional validation for range is needed.
	from           H
	requestTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	reqCh  chan *p2p_pb.HeaderRequest
}

func newSession[H header.Header[H]](
	ctx context.Context,
	h host.Host,
	peerTracker *peerTracker,
	protocolID protocol.ID,
	requestTimeout time.Duration,
	metrics *exchangeMetrics,
	options ...option[H],
) *session[H] {
	ctx, cancel := context.WithCancel(ctx)
	ses := &session[H]{
		ctx:            ctx,
		cancel:         cancel,
		protocolID:     protocolID,
		host:           h,
		queue:          newPeerQueue(ctx, peerTracker.peers()),
		peerTracker:    peerTracker,
		requestTimeout: requestTimeout,
		metrics:        metrics,
	}

	for _, opt := range options {
		opt(ses)
	}
	return ses
}

// getRangeByHeight requests headers from different peers.
func (s *session[H]) getRangeByHeight(
	ctx context.Context,
	from, amount, headersPerPeer uint64,
) (_ []H, err error) {
	log.Debugw(
		"requesting headers",
		"from",
		from,
		"to",
		from+amount-1,
	) // -1 need to exclude to+1 height

	ctx, span := tracerSession.Start(ctx, "get-range-by-height", trace.WithAttributes(
		otelattr.Uint64("from", from),
		otelattr.Uint64("to", from+amount-1),
	))
	defer span.End()

	requests := prepareRequests(from, amount, headersPerPeer)
	result := make(chan []H, len(requests))
	s.reqCh = make(chan *p2p_pb.HeaderRequest, len(requests))

	go s.handleOutgoingRequests(ctx, result)
	for _, req := range requests {
		s.reqCh <- req
	}

	headers := make([]H, 0, amount)
LOOP:
	for {
		select {
		case <-s.ctx.Done():
			err = errors.New("header/p2p: exchange is closed")
			span.SetStatus(codes.Error, err.Error())
			return nil, err
		case <-ctx.Done():
			span.SetStatus(codes.Error, ctx.Err().Error())
			return nil, ctx.Err()
		case res := <-result:
			headers = append(headers, res...)
			if uint64(len(headers)) >= amount {
				break LOOP
			}
		}
	}

	sort.Slice(headers, func(i, j int) bool {
		return headers[i].Height() < headers[j].Height()
	})

	log.Debugw("received headers range",
		"from", headers[0].Height(),
		"to", headers[len(headers)-1].Height(),
	)
	span.SetStatus(codes.Ok, "")
	return headers, nil
}

// close stops the session.
func (s *session[H]) close() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// handleOutgoingRequests pops a peer from the queue and sends a prepared request to the peer.
// Will exit via canceled session context or when all request are processed.
func (s *session[H]) handleOutgoingRequests(ctx context.Context, result chan []H) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.ctx.Done():
			return
		case req := <-s.reqCh:
			// select peer with the highest score among the available ones for the request
			stats := s.queue.waitPop(ctx)
			if stats.peerID == "" {
				return
			}
			go s.doRequest(ctx, stats, req, result)
		}
	}
}

// doRequest chooses the best peer to fetch headers and sends a request in range of available
// maxRetryAttempts.
func (s *session[H]) doRequest(
	ctx context.Context,
	stat *peerStat,
	req *p2p_pb.HeaderRequest,
	headers chan []H,
) {
	ctx, span := tracerSession.Start(ctx, "request-headers-from-peer", trace.WithAttributes(
		attribute.String("peerID", stat.peerID.String()),
		otelattr.Uint64("from", req.GetOrigin()),
		otelattr.Uint64("amount", req.Amount),
	))
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()

	start := time.Now()
	r, size, err := sendMessage(ctx, s.host, stat.peerID, s.protocolID, req)
	took := time.Since(start)

	s.metrics.response(ctx, size, took, err)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		// we should not punish peer at this point and should try to parse responses, despite that error
		// was received.
		log.Debugw("requesting headers from peer failed", "peer", stat.peerID, "err", err)
	}

	h, err := s.processResponses(r)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		logFn := log.Errorw

		switch {
		case errors.Is(err, header.ErrNotFound), errors.Is(err, errEmptyResponse):
			logFn = log.Debugw
			stat.decreaseScore()
		default:
			s.peerTracker.blockPeer(stat.peerID, err)
		}

		select {
		case <-s.ctx.Done():
			return
		case s.reqCh <- req:
		}
		logFn("processing response",
			"from", req.GetOrigin(),
			"to", req.Amount+req.GetOrigin()-1,
			"err", err,
			"peer", stat.peerID,
		)
		// ErrNotFound is not a critical error here. It means
		// that peer hasn't synced the requested range yet.
		// Returning peer to the queue, so it could serve another ranges.
		if errors.Is(err, header.ErrNotFound) {
			s.queue.push(stat)
		}
		return
	}

	log.Debugw("request headers from peer succeeded",
		"peer", stat.peerID,
		"receivedAmount", len(h),
		"requestedAmount", req.Amount,
	)

	remainingHeaders := req.Amount - uint64(len(h))

	span.SetStatus(codes.Ok, "")

	// update peer stats
	stat.updateStats(size, took)

	// ensure that we received the correct amount of headers.
	if remainingHeaders > 0 {
		span.AddEvent("remaining headers", trace.WithAttributes(
			otelattr.Uint64("amount", remainingHeaders),
		))

		from := h[uint64(len(h))-1].Height()
		select {
		case <-s.ctx.Done():
			return
		// create a new request with the remaining headers.
		// prepareRequests will return a slice with 1 element at this point
		case s.reqCh <- prepareRequests(from+1, remainingHeaders, req.Amount)[0]:
			log.Debugw("sending additional request to get remaining headers")
		}
	}

	// send headers to the channel, return peer to the queue, so it can be
	// re-used in case if there are other requests awaiting
	headers <- h
	s.queue.push(stat)
}

// processResponses converts HeaderResponse to Header.
func (s *session[H]) processResponses(responses []*p2p_pb.HeaderResponse) (h []H, err error) {
	defer func() {
		r := recover()
		if r != nil {
			err = fmt.Errorf("PANIC processing responses: %s", r)
		}
	}()

	hdrs, err := processResponses[H](responses)
	if err != nil {
		return nil, err
	}

	return s.verify(hdrs)
}

// verify checks that the received range of headers is adjacent and is valid against the provided
// header.
func (s *session[H]) verify(headers []H) ([]H, error) {
	// if `s.from` is empty, then we just trust the headers
	if s.from.IsZero() {
		return headers, nil
	}

	return header.VerifyRange(s.from, headers)
}

// prepareRequests converts incoming range into separate HeaderRequest.
func prepareRequests(from, amount, headersPerPeer uint64) []*p2p_pb.HeaderRequest {
	requests := make([]*p2p_pb.HeaderRequest, 0, amount/headersPerPeer)
	for amount > uint64(0) {
		var requestSize uint64
		request := &p2p_pb.HeaderRequest{
			Data: &p2p_pb.HeaderRequest_Origin{Origin: from},
		}

		if amount < headersPerPeer {
			requestSize = amount
			amount = 0
		} else {
			amount -= headersPerPeer
			from += headersPerPeer
			requestSize = headersPerPeer
		}

		request.Amount = requestSize
		requests = append(requests, request)
	}
	return requests
}

// processResponses converts HeaderResponses to Headers
func processResponses[H header.Header[H]](resps []*p2p_pb.HeaderResponse) ([]H, error) {
	if len(resps) == 0 {
		return nil, errEmptyResponse
	}

	hdrs := make([]H, 0, len(resps))
	for _, resp := range resps {
		err := convertStatusCodeToError(resp.StatusCode)
		if err != nil {
			return nil, err
		}

		hdr := header.New[H]()
		err = hdr.UnmarshalBinary(resp.Body)
		if err != nil {
			return nil, err
		}

		err = hdr.Validate()
		if err != nil {
			return nil, err
		}

		hdrs = append(hdrs, hdr)
	}
	return hdrs, nil
}
