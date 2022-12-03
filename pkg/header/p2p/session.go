package p2p

import (
	"context"
	"errors"
	"sort"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/protocol"

	"github.com/celestiaorg/celestia-node/pkg/header"
	p2p_pb "github.com/celestiaorg/celestia-node/pkg/header/p2p/pb"
)

// session aims to divide a range of headers
// into several smaller requests among different peers.
type session[H header.Header] struct {
	ctx        context.Context
	cancel     context.CancelFunc
	host       host.Host
	protocolID protocol.ID
	// peerTracker contains discovered peers with records that describes their activity.
	queue *peerQueue

	reqCh chan *p2p_pb.ExtendedHeaderRequest
	errCh chan error
}

func newSession[H header.Header](ctx context.Context, h host.Host, peerTracker []*peerStat, protocolID protocol.ID) *session[H] {
	ctx, cancel := context.WithCancel(ctx)
	return &session[H]{
		ctx:        ctx,
		cancel:     cancel,
		protocolID: protocolID,
		host:       h,
		queue:      newPeerQueue(ctx, peerTracker),
		errCh:      make(chan error),
	}
}

// GetRangeByHeight requests headers from different peers.
func (s *session[H]) getRangeByHeight(
	ctx context.Context,
	from, amount, headersPerPeer uint64,
) ([]H, error) {
	log.Debugw("requesting headers", "from", from, "to", from+amount)

	requests := prepareRequests(from, amount, headersPerPeer)
	result := make(chan []H, len(requests))
	s.reqCh = make(chan *p2p_pb.ExtendedHeaderRequest, len(requests))

	go s.handleOutgoingRequests(ctx, result)
	for _, req := range requests {
		s.reqCh <- req
	}

	headers := make([]H, 0, amount)
	for i := 0; i < len(requests); i++ {
		select {
		case <-s.ctx.Done():
			return nil, errors.New("header/p2p: exchange is closed")
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-s.errCh:
			return nil, err
		case res := <-result:
			headers = append(headers, res...)
		}
	}
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].Height() < headers[j].Height()
	})
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

// doRequest chooses the best peer to fetch headers and sends a request in range of available maxRetryAttempts.
func (s *session[H]) doRequest(
	ctx context.Context,
	stat *peerStat,
	req *p2p_pb.ExtendedHeaderRequest,
	headers chan []H,
) {
	r, size, duration, err := sendMessage(ctx, s.host, stat.peerID, s.protocolID, req)
	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return
		}
		log.Errorw("requesting headers from peer failed."+
			"Retrying the request from different peer", "failed peer", stat.peerID, "err", err)
		select {
		case <-ctx.Done():
		case <-s.ctx.Done():
		case s.reqCh <- req:
		}
		return
	}

	h, err := s.processResponse(r)
	if err != nil {
		s.errCh <- err
		return
	}
	log.Debugw("request headers from peer succeed ", "from", s.host.ID(), "pID", stat.peerID, "amount", req.Amount)
	// send headers to the channel, update peer stats and return peer to the queue, so it can be re-used in case
	// if there are other requests awaiting
	headers <- h
	stat.updateStats(size, duration)
	s.queue.push(stat)
}

// processResponse converts ExtendedHeaderResponse to ExtendedHeader.
func (s *session[H]) processResponse(responses []*p2p_pb.ExtendedHeaderResponse) ([]H, error) {
	headers := make([]H, 0)
	for _, resp := range responses {
		err := convertStatusCodeToError(resp.StatusCode)
		if err != nil {
			return nil, err
		}
		var empty H
		header := empty.New()
		err = header.UnmarshalBinary(resp.Body)
		if err != nil {
			return nil, err
		}
		headers = append(headers, header.(H))
	}
	if len(headers) == 0 {
		return nil, header.ErrNotFound
	}
	return headers, nil
}

// prepareRequests converts incoming range into separate ExtendedHeaderRequest.
func prepareRequests(from, amount, headersPerPeer uint64) []*p2p_pb.ExtendedHeaderRequest {
	requests := make([]*p2p_pb.ExtendedHeaderRequest, 0, amount/headersPerPeer)
	for amount > uint64(0) {
		var requestSize uint64
		request := &p2p_pb.ExtendedHeaderRequest{
			Data: &p2p_pb.ExtendedHeaderRequest_Origin{Origin: from},
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
