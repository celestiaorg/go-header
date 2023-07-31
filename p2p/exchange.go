package p2p

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"

	"github.com/celestiaorg/go-header"
	p2p_pb "github.com/celestiaorg/go-header/p2p/pb"
)

var log = logging.Logger("header/p2p")

// minHeadResponses is the minimum number of headers of the same height
// received from peers to determine the network head. If all trusted peers
// will return headers with non-equal height, then the highest header will be
// chosen.
const minHeadResponses = 2

// numUntrustedHeadRequests is the number of head requests to be made to
// the network in order to determine the network head.
var numUntrustedHeadRequests = 4

// Exchange enables sending outbound HeaderRequests to the network as well as
// handling inbound HeaderRequests from the network.
type Exchange[H header.Header] struct {
	ctx    context.Context
	cancel context.CancelFunc

	protocolID protocol.ID
	host       host.Host

	trustedPeers func() peer.IDSlice
	peerTracker  *peerTracker

	Params ClientParameters

	metrics *metrics
}

func NewExchange[H header.Header](
	host host.Host,
	peers peer.IDSlice,
	gater *conngater.BasicConnectionGater,
	opts ...Option[ClientParameters],
) (*Exchange[H], error) {
	params := DefaultClientParameters()
	for _, opt := range opts {
		opt(&params)
	}

	err := params.Validate()
	if err != nil {
		return nil, err
	}

	ex := &Exchange[H]{
		host:        host,
		protocolID:  protocolID(params.networkID),
		peerTracker: newPeerTracker(host, gater, params.pidstore),
		Params:      params,
	}

	ex.trustedPeers = func() peer.IDSlice {
		return shufflePeers(peers)
	}
	return ex, nil
}

func (ex *Exchange[H]) Start(ctx context.Context) error {
	ex.ctx, ex.cancel = context.WithCancel(context.Background())
	log.Infow("client: starting client", "protocol ID", ex.protocolID)

	go ex.peerTracker.gc()
	go ex.peerTracker.track()

	// bootstrap the peerTracker with trusted peers as well as previously seen
	// peers if provided. If previously seen peers were provided, bootstrap
	// method will block until the given number of connections were attempted
	// (successful or not).
	return ex.peerTracker.bootstrap(ctx, ex.trustedPeers())
}

func (ex *Exchange[H]) Stop(ctx context.Context) error {
	// cancel the session if it exists
	ex.cancel()
	// stop the peerTracker
	return ex.peerTracker.stop(ctx)
}

// Head requests the latest Header from trusted peers.
//
// The Head must be verified thereafter where possible.
// We request in parallel all the trusted peers, compare their response
// and return the highest one.
func (ex *Exchange[H]) Head(ctx context.Context, opts ...header.HeadOption) (H, error) {
	log.Debug("requesting head")

	reqCtx := ctx
	if deadline, ok := ctx.Deadline(); ok {
		// allocate 90% of caller's set deadline for requests
		// and give leftover to determine the bestHead from gathered responses
		// this avoids DeadlineExceeded error when any of the peers are unresponsive
		now := time.Now()
		sub := deadline.Sub(now) * 9 / 10
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithDeadline(ctx, now.Add(sub))
		defer cancel()
	}

	reqParams := header.HeadParams{}
	for _, opt := range opts {
		opt(&reqParams)
	}

	peers := ex.trustedPeers()

	// the TrustedHead field indicates whether the Exchange should use
	// trusted peers for its Head request. If nil, trusted peers will
	// be used. If non-nil, Exchange will ask several peers from its network for
	// their Head and verify against the given trusted header.
	useTrackedPeers := reqParams.TrustedHead != nil
	if useTrackedPeers {
		trackedPeers := ex.peerTracker.getPeers()
		switch {
		case len(trackedPeers) > numUntrustedHeadRequests:
			peers = trackedPeers[:numUntrustedHeadRequests]
		case len(trackedPeers) == 0:
			// in the unlikely case no peers are in tracker, just use trusted
			// peers
		default:
			peers = trackedPeers
		}
	}

	var (
		zero      H
		headerReq = &p2p_pb.HeaderRequest{
			Data:   &p2p_pb.HeaderRequest_Origin{Origin: uint64(0)},
			Amount: 1,
		}
		headerRespCh = make(chan H, len(peers))
	)
	for _, from := range peers {
		go func(from peer.ID) {
			headers, err := ex.request(reqCtx, from, headerReq)
			if err != nil {
				log.Errorw("head request to trusted peer failed", "trustedPeer", from, "err", err)
				headerRespCh <- zero
				return
			}
			// if tracked (untrusted) peers were requested, verify head
			if useTrackedPeers {
				err = reqParams.TrustedHead.Verify(headers[0])
				if err != nil {
					log.Errorw("head request to untrusted peer failed", "untrusted peer", from,
						"err", err)
					// bad head was given, block peer
					ex.peerTracker.blockPeer(from, fmt.Errorf("returned bad head: %w", err))
					headerRespCh <- zero
					return
				}
			}
			// request ensures that the result slice will have at least one Header
			headerRespCh <- headers[0]
		}(from)
	}

	headers := make([]H, 0, len(peers))
	for range peers {
		select {
		case h := <-headerRespCh:
			if !h.IsZero() {
				headers = append(headers, h)
			}
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-ex.ctx.Done():
			return zero, ex.ctx.Err()
		}
	}
	return bestHead[H](headers)
}

// GetByHeight performs a request for the Header at the given
// height to the network. Note that the Header must be verified
// thereafter.
func (ex *Exchange[H]) GetByHeight(ctx context.Context, height uint64) (H, error) {
	log.Debugw("requesting header", "height", height)
	var zero H
	// sanity check height
	if height == 0 {
		return zero, fmt.Errorf("specified request height must be greater than 0")
	}
	// create request
	req := &p2p_pb.HeaderRequest{
		Data:   &p2p_pb.HeaderRequest_Origin{Origin: height},
		Amount: 1,
	}
	headers, err := ex.performRequest(ctx, req)
	if err != nil {
		return zero, err
	}
	return headers[0], nil
}

// GetRangeByHeight performs a request for the given range of Headers
// to the network. Note that the Headers must be verified thereafter.
func (ex *Exchange[H]) GetRangeByHeight(ctx context.Context, from, amount uint64) ([]H, error) {
	if amount == 0 {
		return make([]H, 0), nil
	}
	if amount > header.MaxRangeRequestSize {
		return nil, header.ErrHeadersLimitExceeded
	}
	session := newSession[H](ex.ctx, ex.host, ex.peerTracker, ex.protocolID, ex.Params.RangeRequestTimeout)
	defer session.close()
	return session.getRangeByHeight(ctx, from, amount, ex.Params.MaxHeadersPerRangeRequest)
}

// GetVerifiedRange performs a request for the given range of Headers to the network and
// ensures that returned headers are correct against the passed one.
func (ex *Exchange[H]) GetVerifiedRange(
	ctx context.Context,
	from H,
	amount uint64,
) ([]H, error) {
	if amount == 0 {
		return make([]H, 0), nil
	}
	session := newSession[H](
		ex.ctx, ex.host, ex.peerTracker, ex.protocolID, ex.Params.RangeRequestTimeout, withValidation(from),
	)
	defer session.close()
	// we request the next header height that we don't have: `fromHead`+1
	return session.getRangeByHeight(ctx, uint64(from.Height())+1, amount, ex.Params.MaxHeadersPerRangeRequest)
}

// Get performs a request for the Header by the given hash corresponding
// to the RawHeader. Note that the Header must be verified thereafter.
func (ex *Exchange[H]) Get(ctx context.Context, hash header.Hash) (H, error) {
	log.Debugw("requesting header", "hash", hash.String())
	var zero H
	// create request
	req := &p2p_pb.HeaderRequest{
		Data:   &p2p_pb.HeaderRequest_Hash{Hash: hash},
		Amount: 1,
	}
	headers, err := ex.performRequest(ctx, req)
	if err != nil {
		return zero, err
	}

	if !bytes.Equal(headers[0].Hash(), hash) {
		return zero, fmt.Errorf("incorrect hash in header: expected %x, got %x", hash, headers[0].Hash())
	}
	return headers[0], nil
}

const requestRetry = 3

func (ex *Exchange[H]) performRequest(
	ctx context.Context,
	req *p2p_pb.HeaderRequest,
) ([]H, error) {
	if req.Amount == 0 {
		return make([]H, 0), nil
	}

	trustedPeers := ex.trustedPeers()
	if len(trustedPeers) == 0 {
		return nil, fmt.Errorf("no trusted peers")
	}

	var reqErr error

	for i := 0; i < requestRetry; i++ {
		for _, peer := range trustedPeers {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ex.ctx.Done():
				return nil, ex.ctx.Err()
			default:
			}

			h, err := ex.request(ctx, peer, req)
			if err != nil {
				reqErr = err
				log.Debugw("requesting header from trustedPeer failed",
					"trustedPeer", peer, "err", err, "try", i)
				continue
			}
			return h, err
		}
	}
	return nil, reqErr
}

// request sends the HeaderRequest to a remote peer.
func (ex *Exchange[H]) request(
	ctx context.Context,
	to peer.ID,
	req *p2p_pb.HeaderRequest,
) ([]H, error) {
	log.Debugw("requesting peer", "peer", to)
	responses, size, duration, err := sendMessage(ctx, ex.host, to, ex.protocolID, req)
	ex.metrics.observeResponse(ctx, size, duration, err)
	if err != nil {
		log.Debugw("err sending request", "peer", to, "err", err)
		return nil, err
	}

	headers := make([]H, 0, len(responses))
	for _, response := range responses {
		if err = convertStatusCodeToError(response.StatusCode); err != nil {
			return nil, err
		}
		var empty H
		header := empty.New()
		err := header.UnmarshalBinary(response.Body)
		if err != nil {
			return nil, err
		}
		err = validateChainID(ex.Params.chainID, header.(H).ChainID())
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

// shufflePeers changes the order of trusted peers.
func shufflePeers(peers peer.IDSlice) peer.IDSlice {
	tpeers := make(peer.IDSlice, len(peers))
	copy(tpeers, peers)
	//nolint:gosec // G404: Use of weak random number generator
	rand.New(rand.NewSource(time.Now().UnixNano())).Shuffle(
		len(tpeers),
		func(i, j int) { tpeers[i], tpeers[j] = tpeers[j], tpeers[i] },
	)
	return tpeers
}

// bestHead chooses Header that matches the conditions:
// * should have max height among received;
// * should be received at least from 2 peers;
// If neither condition is met, then latest Header will be returned (header of the highest
// height).
func bestHead[H header.Header](result []H) (H, error) {
	if len(result) == 0 {
		var zero H
		return zero, header.ErrNotFound
	}
	counter := make(map[string]int)
	// go through all of Headers and count the number of headers with a specific hash
	for _, res := range result {
		counter[res.Hash().String()]++
	}
	// sort results in a decreasing order
	sort.Slice(result, func(i, j int) bool {
		return result[i].Height() > result[j].Height()
	})

	// try to find Header with the maximum height that was received at least from 2 peers
	for _, res := range result {
		if counter[res.Hash().String()] >= minHeadResponses {
			return res, nil
		}
	}
	log.Debug("could not find latest header received from at least two peers, returning header with the max height")
	// otherwise return header with the max height
	return result[0], nil
}
