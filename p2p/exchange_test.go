package p2p

import (
	"context"
	"strconv"
	sync2 "sync"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	libhost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	swarm "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-libp2p-messenger/serde"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	p2p_pb "github.com/celestiaorg/go-header/p2p/pb"
)

const networkID = "test" // must match the chain-id in test suite

func TestExchange_RequestHead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hosts := createMocknet(t, 3)
	exchg, trustedStore := createP2PExAndServer(t, hosts[0], hosts[1])

	// create new server-side exchange that will act as the tracked peer
	// it will have a higher chain head than the trusted peer so that the
	// test can determine which peer was asked
	trackedStore := headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 50)
	serverSideEx, err := NewExchangeServer[*headertest.DummyHeader](hosts[2], trackedStore,
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	err = serverSideEx.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err = serverSideEx.Stop(ctx)
		require.NoError(t, err)
	})

	tests := []struct {
		requestFromTrusted bool
		lastHeader         *headertest.DummyHeader
		expectedHeight     uint64
		expectedHash       header.Hash
	}{
		// routes to trusted peer only
		{
			requestFromTrusted: true,
			lastHeader:         trustedStore.Headers[trustedStore.HeadHeight-1],
			expectedHeight:     trustedStore.HeadHeight,
			expectedHash:       trustedStore.Headers[trustedStore.HeadHeight].Hash(),
		},
		// routes to tracked peers and takes highest chain head
		{
			requestFromTrusted: false,
			lastHeader:         trackedStore.Headers[trackedStore.HeadHeight-1],
			expectedHeight:     trackedStore.HeadHeight,
			expectedHash:       trackedStore.Headers[trackedStore.HeadHeight].Hash(),
		},
	}

	for i, tt := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			var opts []header.HeadOption[*headertest.DummyHeader]
			if !tt.requestFromTrusted {
				opts = append(opts, header.WithTrustedHead[*headertest.DummyHeader](tt.lastHeader))
			}

			header, err := exchg.Head(ctx, opts...)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedHeight, header.Height())
			assert.Equal(t, tt.expectedHash, header.Hash())
		})
	}
}

// TestExchange_RequestHead_SoftFailure tests that the exchange still processes
// a Head response that has a SoftFailure.
func TestExchange_RequestHead_SoftFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hosts := createMocknet(t, 3)
	exchg, _ := createP2PExAndServer(t, hosts[0], hosts[1])

	// create a tracked peer
	suite := headertest.NewTestSuite(t)
	trackedStore := headertest.NewStore[*headertest.DummyHeader](t, suite, 50)
	// create a header that will SoftFail verification and append it to tracked
	// peer's store
	hdr := suite.GenDummyHeaders(1)[0]
	hdr.VerifyFailure = true
	hdr.SoftFailure = true
	err := trackedStore.Append(ctx, hdr)
	require.NoError(t, err)
	// start the tracked peer's server
	serverSideEx, err := NewExchangeServer[*headertest.DummyHeader](hosts[2], trackedStore,
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	err = serverSideEx.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err = serverSideEx.Stop(ctx)
		require.NoError(t, err)
	})

	// get first subjective head from trusted peer to initialize the
	// exchange's store
	head, err := exchg.Head(ctx)
	require.NoError(t, err)

	// now use that trusted head to request a new head from the exchange
	// from the tracked peer
	softFailHead, err := exchg.Head(ctx, header.WithTrustedHead[*headertest.DummyHeader](head))
	require.NoError(t, err)
	assert.Equal(t, trackedStore.HeadHeight, softFailHead.Height())
}

func TestExchange_RequestHead_UnresponsivePeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// we need host with real transport here, so we can test timeouts
	hosts := quicHosts(t, 3)

	trustedPeers := []peer.ID{hosts[1].ID(), hosts[2].ID()}
	client := client(ctx, t, hosts[0], trustedPeers)

	goodStore := headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 5)
	_ = server(ctx, t, hosts[1], goodStore)

	badStore := &timedOutStore{timeout: time.Millisecond * 500} // simulates peer that does not respond
	_ = server(ctx, t, hosts[2], badStore)

	ctx, cancel = context.WithTimeout(ctx, time.Millisecond*500)
	t.Cleanup(cancel)

	// should still succeed with one responder
	head, err := client.Head(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, head)
}

func TestExchange_RequestHeadFlightProtection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hosts := createMocknet(t, 3)
	exchg, trustedStore := createP2PExAndServer(t, hosts[0], hosts[1])

	// create the same requests
	tests := []struct {
		requestFromTrusted bool
		lastHeader         *headertest.DummyHeader
		expectedHeight     uint64
		expectedHash       header.Hash
	}{
		{
			requestFromTrusted: true,
			lastHeader:         trustedStore.Headers[trustedStore.HeadHeight-1],
			expectedHeight:     trustedStore.HeadHeight,
			expectedHash:       trustedStore.Headers[trustedStore.HeadHeight].Hash(),
		},
		{
			// request from untrusted peer should be the same as trusted bc of single-preflight
			requestFromTrusted: false,
			lastHeader:         trustedStore.Headers[trustedStore.HeadHeight-1],
			expectedHeight:     trustedStore.HeadHeight,
			expectedHash:       trustedStore.Headers[trustedStore.HeadHeight].Hash(),
		},
	}

	var wg sync2.WaitGroup
	// run over goroutine
	for i, tt := range tests {
		wg.Add(1)
		go func(testStruct struct {
			requestFromTrusted bool
			lastHeader         *headertest.DummyHeader
			expectedHeight     uint64
			expectedHash       header.Hash
		}, it int) {
			defer wg.Done()
			var opts []header.HeadOption[*headertest.DummyHeader]
			if !testStruct.requestFromTrusted {
				opts = append(opts, header.WithTrustedHead[*headertest.DummyHeader](testStruct.lastHeader))
			}

			h, errG := exchg.Head(ctx, opts...)
			require.NoError(t, errG)

			assert.Equal(t, testStruct.expectedHeight, h.Height())
			assert.Equal(t, testStruct.expectedHash, h.Hash())

		}(tt, i)
		// ensure first Head will be locked by request from trusted peer
		time.Sleep(time.Microsecond)
	}
	wg.Wait()
}

func TestExchange_RequestHeader(t *testing.T) {
	hosts := createMocknet(t, 2)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])
	// perform expected request
	header, err := exchg.GetByHeight(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, store.Headers[5].Height(), header.Height())
	assert.Equal(t, store.Headers[5].Hash(), header.Hash())
}

func TestExchange_GetRangeByHeight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hosts := createMocknet(t, 2)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])

	from, err := store.GetByHeight(ctx, 1)
	require.NoError(t, err)

	firstHeaderInRangeHeight := from.Height() + 1
	lastHeaderInRangeHeight := uint64(4)
	to := lastHeaderInRangeHeight + 1
	expectedLenHeaders := to - firstHeaderInRangeHeight // expected amount

	// perform expected request
	gotHeaders, err := exchg.GetRangeByHeight(ctx, from, to)
	require.NoError(t, err)

	assert.Len(t, gotHeaders, int(expectedLenHeaders))
	assert.Equal(t, firstHeaderInRangeHeight, gotHeaders[0].Height())
	assert.Equal(t, lastHeaderInRangeHeight, gotHeaders[len(gotHeaders)-1].Height())

	for _, got := range gotHeaders {
		assert.Equal(t, store.Headers[got.Height()].Height(), got.Height())
		assert.Equal(t, store.Headers[got.Height()].Hash(), got.Hash())
	}
}

func TestExchange_GetRangeByHeight_FailsVerification(t *testing.T) {
	hosts := createMocknet(t, 2)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])
	store.Headers[3].VerifyFailure = true // force a verification failure on the 3rd header
	// perform expected request
	h := store.Headers[1]
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*500)
	t.Cleanup(cancel)

	// requests range (1:4)
	_, err := exchg.GetRangeByHeight(ctx, h, 4)
	assert.Error(t, err)

	// ensure that peer was added to the blacklist
	peers := exchg.peerTracker.connGater.ListBlockedPeers()
	require.Len(t, peers, 1)
	require.True(t, hosts[1].ID() == peers[0])
}

// TestExchange_RequestFullRangeHeaders requests max amount of headers
// to verify how session will parallelize all requests.
func TestExchange_RequestFullRangeHeaders(t *testing.T) {
	// create mocknet with 5 peers
	hosts := createMocknet(t, 5)
	store := headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), int(header.MaxRangeRequestSize)+1)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	// create new exchange
	exchange, err := NewExchange[*headertest.DummyHeader](hosts[len(hosts)-1], []peer.ID{hosts[4].ID()}, connGater,
		WithNetworkID[ClientParameters](networkID),
		WithChainID(networkID),
	)
	require.NoError(t, err)
	exchange.ctx, exchange.cancel = context.WithCancel(context.Background())
	t.Cleanup(exchange.cancel)
	// amount of servers is len(hosts)-1 because one peer acts as a client
	servers := make([]*ExchangeServer[*headertest.DummyHeader], len(hosts)-1)
	for index := range servers {
		servers[index], err = NewExchangeServer[*headertest.DummyHeader](
			hosts[index],
			store,
			WithNetworkID[ServerParameters](networkID),
		)
		require.NoError(t, err)
		servers[index].Start(context.Background()) //nolint:errcheck
		exchange.peerTracker.peerLk.Lock()
		exchange.peerTracker.trackedPeers[hosts[index].ID()] = &peerStat{peerID: hosts[index].ID()}
		exchange.peerTracker.peerLk.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	gen, err := store.GetByHeight(ctx, 1)
	require.NoError(t, err)

	// request the max amount of headers
	to := gen.Height() + header.MaxRangeRequestSize + 1 // add 1 to account for `from` being inclusive
	headers, err := exchange.GetRangeByHeight(ctx, gen, to)
	require.NoError(t, err)
	assert.Len(t, headers, int(header.MaxRangeRequestSize))
}

// TestExchange_RequestHeadersFromAnotherPeer tests that the Exchange instance will request range
// from another peer with lower score after receiving header.ErrNotFound
func TestExchange_RequestHeadersFromAnotherPeer(t *testing.T) {
	hosts := createMocknet(t, 3)
	// create client + server(it does not have needed headers)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])
	// create one more server(with more headers in the store)
	serverSideEx, err := NewExchangeServer[*headertest.DummyHeader](
		hosts[2], headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 10),
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	require.NoError(t, serverSideEx.Start(context.Background()))
	t.Cleanup(func() {
		serverSideEx.Stop(context.Background()) //nolint:errcheck
	})
	exchg.peerTracker.peerLk.Lock()
	exchg.peerTracker.trackedPeers[hosts[2].ID()] = &peerStat{peerID: hosts[2].ID(), peerScore: 20}
	exchg.peerTracker.peerLk.Unlock()

	h, err := store.GetByHeight(context.Background(), 5)
	require.NoError(t, err)

	_, err = exchg.GetRangeByHeight(context.Background(), h, 8)
	require.NoError(t, err)
	// ensure that peerScore for the second peer is changed
	newPeerScore := exchg.peerTracker.trackedPeers[hosts[2].ID()].score()
	require.NotEqual(t, 20, newPeerScore)
}

// TestExchange_RequestByHash tests that the Exchange instance can
// respond to an HeaderRequest for a hash instead of a height.
func TestExchange_RequestByHash(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	net, err := mocknet.FullMeshConnected(2)
	require.NoError(t, err)
	// get host and peer
	host, peer := net.Hosts()[0], net.Hosts()[1]
	// create and start the ExchangeServer
	store := headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 5)
	serv, err := NewExchangeServer[*headertest.DummyHeader](
		host,
		store,
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	err = serv.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		serv.Stop(context.Background()) //nolint:errcheck
	})

	// start a new stream via Peer to see if Host can handle inbound requests
	stream, err := peer.NewStream(context.Background(), libhost.InfoFromHost(host).ID, protocolID(networkID))
	require.NoError(t, err)
	// create request for a header at a random height
	reqHeight := store.HeadHeight - 2
	req := &p2p_pb.HeaderRequest{
		Data:   &p2p_pb.HeaderRequest_Hash{Hash: store.Headers[reqHeight].Hash()},
		Amount: 1,
	}
	// send request
	_, err = serde.Write(stream, req)
	require.NoError(t, err)
	// read resp
	resp := new(p2p_pb.HeaderResponse)
	_, err = serde.Read(stream, resp)
	require.NoError(t, err)
	// compare
	var eh headertest.DummyHeader
	err = eh.UnmarshalBinary(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, store.Headers[reqHeight].Height(), eh.Height())
	assert.Equal(t, store.Headers[reqHeight].Hash(), eh.Hash())
}

func Test_bestHead(t *testing.T) {
	gen := func() []*headertest.DummyHeader {
		suite := headertest.NewTestSuite(t)
		res := make([]*headertest.DummyHeader, 0)
		for i := 0; i < 3; i++ {
			res = append(res, suite.NextHeader())
		}
		return res
	}
	testCases := []struct {
		precondition   func() []*headertest.DummyHeader
		expectedHeight uint64
	}{
		/*
			Height -> Amount
			headerHeight[0]=1 -> 1
			headerHeight[1]=2 -> 1
			headerHeight[2]=3 -> 1
			result -> headerHeight[2]
		*/
		{
			precondition:   gen,
			expectedHeight: 3,
		},
		/*
			Height -> Amount
			headerHeight[0]=1 -> 2
			headerHeight[1]=2 -> 1
			headerHeight[2]=3 -> 1
			result -> headerHeight[0]
		*/
		{
			precondition: func() []*headertest.DummyHeader {
				res := gen()
				res = append(res, res[0])
				return res
			},
			expectedHeight: 1,
		},
		/*
			Height -> Amount
			headerHeight[0]=1 -> 3
			headerHeight[1]=2 -> 2
			headerHeight[2]=3 -> 1
			result -> headerHeight[1]
		*/
		{
			precondition: func() []*headertest.DummyHeader {
				res := gen()
				res = append(res, res[0])
				res = append(res, res[0])
				res = append(res, res[1])
				return res
			},
			expectedHeight: 2,
		},
	}
	for _, tt := range testCases {
		res := tt.precondition()
		header, err := bestHead(res)
		require.NoError(t, err)
		require.True(t, header.Height() == tt.expectedHeight)
	}
}

// TestExchange_RequestByHashFails tests that the Exchange instance can
// respond with a StatusCode_NOT_FOUND if it will not have requested header.
func TestExchange_RequestByHashFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	net, err := mocknet.FullMeshConnected(2)
	require.NoError(t, err)
	// get host and peer
	host, peer := net.Hosts()[0], net.Hosts()[1]
	serv, err := NewExchangeServer[*headertest.DummyHeader](
		host, headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 0),
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	err = serv.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		serv.Stop(context.Background()) //nolint:errcheck
	})

	stream, err := peer.NewStream(context.Background(), libhost.InfoFromHost(host).ID, protocolID(networkID))
	require.NoError(t, err)
	req := &p2p_pb.HeaderRequest{
		Data:   &p2p_pb.HeaderRequest_Hash{Hash: []byte("dummy_hash")},
		Amount: 1,
	}
	// send request
	_, err = serde.Write(stream, req)
	require.NoError(t, err)
	// read resp
	resp := new(p2p_pb.HeaderResponse)
	_, err = serde.Read(stream, resp)
	require.NoError(t, err)
	require.Equal(t, resp.StatusCode, p2p_pb.StatusCode_NOT_FOUND)
}

// TestExchange_HandleHeaderWithDifferentChainID ensures that headers with different
// chainIDs will not be served and stored.
func TestExchange_HandleHeaderWithDifferentChainID(t *testing.T) {
	hosts := createMocknet(t, 2)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])
	exchg.Params.chainID = "test1"

	_, err := exchg.Head(context.Background())
	require.Error(t, err)

	_, err = exchg.GetByHeight(context.Background(), 1)
	require.Error(t, err)

	h, err := store.GetByHeight(context.Background(), 1)
	require.NoError(t, err)
	_, err = exchg.Get(context.Background(), h.Hash())
	require.Error(t, err)
}

// TestExchange_RequestHeadersFromAnotherPeer tests that the Exchange instance will request range
// from another peer with lower score after receiving header.ErrNotFound
func TestExchange_RequestHeadersFromAnotherPeerWhenTimeout(t *testing.T) {
	// create blankhost because mocknet does not support deadlines
	swarm0 := swarm.GenSwarm(t)
	host0 := blankhost.NewBlankHost(swarm0)
	swarm1 := swarm.GenSwarm(t)
	host1 := blankhost.NewBlankHost(swarm1)
	swarm2 := swarm.GenSwarm(t)
	host2 := blankhost.NewBlankHost(swarm2)
	dial := func(a, b network.Network) {
		swarm.DivulgeAddresses(b, a)
		if _, err := a.DialPeer(context.Background(), b.LocalPeer()); err != nil {
			t.Fatalf("Failed to dial: %s", err)
		}
	}
	// dial peers
	dial(swarm0, swarm1)
	dial(swarm0, swarm2)
	dial(swarm1, swarm2)

	// create client + server(it does not have needed headers)
	exchg, store := createP2PExAndServer(t, host0, host1)
	exchg.Params.RequestTimeout = time.Millisecond * 100
	// create one more server(with more headers in the store)
	serverSideEx, err := NewExchangeServer[*headertest.DummyHeader](
		host2, headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 10),
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	// change store implementation
	serverSideEx.store = &timedOutStore{timeout: exchg.Params.RequestTimeout}
	require.NoError(t, serverSideEx.Start(context.Background()))
	t.Cleanup(func() {
		serverSideEx.Stop(context.Background()) //nolint:errcheck
	})
	prevScore := exchg.peerTracker.trackedPeers[host1.ID()].score()
	exchg.peerTracker.peerLk.Lock()
	exchg.peerTracker.trackedPeers[host2.ID()] = &peerStat{peerID: host2.ID(), peerScore: 200}
	exchg.peerTracker.peerLk.Unlock()

	gen, err := store.GetByHeight(context.Background(), 1)
	require.NoError(t, err)

	_, err = exchg.GetRangeByHeight(context.Background(), gen, 3)
	require.NoError(t, err)
	newPeerScore := exchg.peerTracker.trackedPeers[host1.ID()].score()
	assert.NotEqual(t, newPeerScore, prevScore)
}

// TestExchange_RequestPartialRange enusres in case of receiving a partial response
// from server, Exchange will re-request remaining headers from another peer
func TestExchange_RequestPartialRange(t *testing.T) {
	hosts := createMocknet(t, 3)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])

	// create one more server(with more headers in the store)
	serverSideEx, err := NewExchangeServer[*headertest.DummyHeader](
		hosts[2], headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 10),
		WithNetworkID[ServerParameters](networkID),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	require.NoError(t, err)
	require.NoError(t, serverSideEx.Start(ctx))
	exchg.peerTracker.peerLk.Lock()
	prevScoreBefore1 := exchg.peerTracker.trackedPeers[hosts[1].ID()].peerScore
	prevScoreBefore2 := 50
	// reducing peerScore of the second server, so our exchange will request host[1] first.
	exchg.peerTracker.trackedPeers[hosts[2].ID()] = &peerStat{peerID: hosts[2].ID(), peerScore: 50}
	exchg.peerTracker.peerLk.Unlock()

	gen, err := store.GetByHeight(context.Background(), 1)
	require.NoError(t, err)

	h, err := exchg.GetRangeByHeight(ctx, gen, 8)
	require.NotNil(t, h)
	require.NoError(t, err)

	exchg.peerTracker.peerLk.Lock()
	prevScoreAfter1 := exchg.peerTracker.trackedPeers[hosts[1].ID()].peerScore
	prevScoreAfter2 := exchg.peerTracker.trackedPeers[hosts[2].ID()].peerScore
	exchg.peerTracker.peerLk.Unlock()

	assert.NotEqual(t, prevScoreBefore1, prevScoreAfter1)
	assert.NotEqual(t, prevScoreBefore2, prevScoreAfter2)
}

func createMocknet(t *testing.T, amount int) []libhost.Host {
	net, err := mocknet.FullMeshConnected(amount)
	require.NoError(t, err)
	// get host and peer
	return net.Hosts()
}

// createP2PExAndServer creates a Exchange with 5 headers already in its store.
func createP2PExAndServer(
	t *testing.T,
	host, tpeer libhost.Host,
) (*Exchange[*headertest.DummyHeader], *headertest.Store[*headertest.DummyHeader]) {
	store := headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 5)

	serverSideEx, err := NewExchangeServer[*headertest.DummyHeader](tpeer, store,
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	err = serverSideEx.Start(context.Background())
	require.NoError(t, err)

	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)

	ex, err := NewExchange[*headertest.DummyHeader](host, []peer.ID{tpeer.ID()}, connGater,
		WithNetworkID[ClientParameters](networkID),
		WithChainID(networkID),
	)
	require.NoError(t, err)
	require.NoError(t, ex.Start(context.Background()))

	time.Sleep(time.Millisecond * 100) // give peerTracker time to add a trusted peer

	ex.peerTracker.peerLk.Lock()
	ex.peerTracker.trackedPeers[tpeer.ID()] = &peerStat{peerID: tpeer.ID(), peerScore: 100.0}
	ex.peerTracker.peerLk.Unlock()

	t.Cleanup(func() {
		serverSideEx.Stop(context.Background()) //nolint:errcheck
		ex.Stop(context.Background())           //nolint:errcheck
	})

	return ex, store
}

func quicHosts(t *testing.T, n int) []libhost.Host {
	hosts := make([]libhost.Host, n)
	for i := range hosts {
		swrm := swarm.GenSwarm(t, swarm.OptDisableTCP)
		hosts[i] = blankhost.NewBlankHost(swrm)
		for _, host := range hosts[:i] {
			hosts[i].Peerstore().AddAddrs(host.ID(), host.Network().ListenAddresses(), peerstore.PermanentAddrTTL)
			host.Peerstore().AddAddrs(hosts[i].ID(), hosts[i].Network().ListenAddresses(), peerstore.PermanentAddrTTL)
		}
	}

	return hosts
}

func client(ctx context.Context, t *testing.T, host libhost.Host, trusted []peer.ID) *Exchange[*headertest.DummyHeader] {
	client, err := NewExchange[*headertest.DummyHeader](host, trusted, nil)
	require.NoError(t, err)

	err = client.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err := client.Stop(ctx)
		require.NoError(t, err)
	})

	return client
}

func server(ctx context.Context, t *testing.T, host libhost.Host, store header.Store[*headertest.DummyHeader]) *ExchangeServer[*headertest.DummyHeader] {
	server, err := NewExchangeServer[*headertest.DummyHeader](host, store)
	require.NoError(t, err)
	err = server.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err := server.Stop(ctx)
		require.NoError(t, err)
	})
	return server
}

type timedOutStore struct {
	headertest.Store[*headertest.DummyHeader]
	timeout time.Duration
}

func (t *timedOutStore) HasAt(_ context.Context, _ uint64) bool {
	time.Sleep(t.timeout + time.Second)
	return true
}

func (t *timedOutStore) Head(context.Context, ...header.HeadOption[*headertest.DummyHeader]) (*headertest.DummyHeader, error) {
	time.Sleep(t.timeout)
	return nil, header.ErrNoHead
}
