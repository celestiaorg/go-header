package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	libhost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-libp2p-messenger/serde"

	swarm "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/celestiaorg/celestia-node/libs/header"
	headerMock "github.com/celestiaorg/celestia-node/libs/header/mocks"
	p2p_pb "github.com/celestiaorg/celestia-node/libs/header/p2p/pb"
	"github.com/celestiaorg/celestia-node/libs/header/test"
)

var privateProtocolID = protocolID("private")

func TestExchange_RequestHead(t *testing.T) {
	hosts := createMocknet(t, 2)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])
	// perform header request
	header, err := exchg.Head(context.Background())
	require.NoError(t, err)

	assert.Equal(t, store.Headers[store.HeadHeight].Height(), header.Height())
	assert.Equal(t, store.Headers[store.HeadHeight].Hash(), header.Hash())
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

func TestExchange_RequestHeaders(t *testing.T) {
	hosts := createMocknet(t, 2)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])
	// perform expected request
	gotHeaders, err := exchg.GetRangeByHeight(context.Background(), 1, 5)
	require.NoError(t, err)
	for _, got := range gotHeaders {
		assert.Equal(t, store.Headers[got.Height()].Height(), got.Height())
		assert.Equal(t, store.Headers[got.Height()].Hash(), got.Hash())
	}
}

func TestExchange_RequestVerifiedHeaders(t *testing.T) {
	hosts := createMocknet(t, 2)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])
	// perform expected request
	h := store.Headers[1]
	_, err := exchg.GetVerifiedRange(context.Background(), h, 3)
	require.NoError(t, err)
}

func TestExchange_RequestVerifiedHeadersFails(t *testing.T) {
	hosts := createMocknet(t, 2)
	exchg, store := createP2PExAndServer(t, hosts[0], hosts[1])
	store.Headers[2] = store.Headers[3]
	// perform expected request
	h := store.Headers[1]
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*500)
	t.Cleanup(cancel)
	_, err := exchg.GetVerifiedRange(ctx, h, 3)
	require.Error(t, err)

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
	totalAmount := 80
	store := headerMock.NewStore[*test.DummyHeader](t, test.NewTestSuite(t), totalAmount)
	protocolSuffix := "private"
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)
	// create new exchange
	exchange, err := NewExchange[*test.DummyHeader](hosts[len(hosts)-1], []peer.ID{}, protocolSuffix, connGater)
	require.NoError(t, err)
	exchange.Params.MaxHeadersPerRequest = 10
	exchange.ctx, exchange.cancel = context.WithCancel(context.Background())
	t.Cleanup(exchange.cancel)
	// amount of servers is len(hosts)-1 because one peer acts as a client
	servers := make([]*ExchangeServer[*test.DummyHeader], len(hosts)-1)
	for index := range servers {
		servers[index], err = NewExchangeServer[*test.DummyHeader](hosts[index], store, protocolSuffix)
		require.NoError(t, err)
		servers[index].Start(context.Background()) //nolint:errcheck
		exchange.peerTracker.peerLk.Lock()
		exchange.peerTracker.trackedPeers[hosts[index].ID()] = &peerStat{peerID: hosts[index].ID()}
		exchange.peerTracker.peerLk.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	// request headers from 1 to totalAmount(80)
	headers, err := exchange.GetRangeByHeight(ctx, 1, uint64(totalAmount))
	require.NoError(t, err)
	require.Len(t, headers, 80)
}

// TestExchange_RequestHeadersLimitExceeded tests that the Exchange instance will return
// header.ErrHeadersLimitExceeded if the requested range will be move than MaxRequestSize.
func TestExchange_RequestHeadersLimitExceeded(t *testing.T) {
	hosts := createMocknet(t, 2)
	exchg, _ := createP2PExAndServer(t, hosts[0], hosts[1])
	_, err := exchg.GetRangeByHeight(context.Background(), 1, 600)
	require.Error(t, err)
	require.ErrorAs(t, err, &header.ErrHeadersLimitExceeded)
}

// TestExchange_RequestHeadersFromAnotherPeer tests that the Exchange instance will request range
// from another peer with lower score after receiving header.ErrNotFound
func TestExchange_RequestHeadersFromAnotherPeer(t *testing.T) {
	hosts := createMocknet(t, 3)
	// create client + server(it does not have needed headers)
	exchg, _ := createP2PExAndServer(t, hosts[0], hosts[1])
	// create one more server(with more headers in the store)
	serverSideEx, err := NewExchangeServer[*test.DummyHeader](
		hosts[2], headerMock.NewStore[*test.DummyHeader](t, test.NewTestSuite(t), 10), "private",
	)
	require.NoError(t, err)
	require.NoError(t, serverSideEx.Start(context.Background()))
	t.Cleanup(func() {
		serverSideEx.Stop(context.Background()) //nolint:errcheck
	})
	exchg.peerTracker.peerLk.Lock()
	exchg.peerTracker.trackedPeers[hosts[2].ID()] = &peerStat{peerID: hosts[2].ID(), peerScore: 20}
	exchg.peerTracker.peerLk.Unlock()
	_, err = exchg.GetRangeByHeight(context.Background(), 5, 3)
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
	store := headerMock.NewStore[*test.DummyHeader](t, test.NewTestSuite(t), 5)
	serv, err := NewExchangeServer[*test.DummyHeader](host, store, "private")
	require.NoError(t, err)
	err = serv.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		serv.Stop(context.Background()) //nolint:errcheck
	})

	// start a new stream via Peer to see if Host can handle inbound requests
	stream, err := peer.NewStream(context.Background(), libhost.InfoFromHost(host).ID, privateProtocolID)
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
	var eh test.DummyHeader
	err = eh.UnmarshalBinary(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, store.Headers[reqHeight].Height(), eh.Height())
	assert.Equal(t, store.Headers[reqHeight].Hash(), eh.Hash())
}

func Test_bestHead(t *testing.T) {
	params := DefaultClientParameters()
	gen := func() []*test.DummyHeader {
		suite := test.NewTestSuite(t)
		res := make([]*test.DummyHeader, 0)
		for i := 0; i < 3; i++ {
			res = append(res, suite.GetRandomHeader())
		}
		return res
	}
	testCases := []struct {
		precondition   func() []*test.DummyHeader
		expectedHeight int64
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
			precondition: func() []*test.DummyHeader {
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
			precondition: func() []*test.DummyHeader {
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
		header, err := bestHead(res, params.MinResponses)
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
	serv, err := NewExchangeServer[*test.DummyHeader](
		host, headerMock.NewStore[*test.DummyHeader](t, test.NewTestSuite(t), 0), "private",
	)
	require.NoError(t, err)
	err = serv.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		serv.Stop(context.Background()) //nolint:errcheck
	})

	stream, err := peer.NewStream(context.Background(), libhost.InfoFromHost(host).ID, privateProtocolID)
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
	exchg, _ := createP2PExAndServer(t, host0, host1)
	exchg.Params.RequestTimeout = time.Millisecond * 100
	// create one more server(with more headers in the store)
	serverSideEx, err := NewExchangeServer[*test.DummyHeader](
		host2, headerMock.NewStore[*test.DummyHeader](t, test.NewTestSuite(t), 10), "private",
	)
	require.NoError(t, err)
	// change store implementation
	serverSideEx.getter = &timedOutStore{exchg.Params.RequestTimeout}
	require.NoError(t, serverSideEx.Start(context.Background()))
	t.Cleanup(func() {
		serverSideEx.Stop(context.Background()) //nolint:errcheck
	})
	exchg.peerTracker.peerLk.Lock()
	exchg.peerTracker.trackedPeers[host2.ID()] = &peerStat{peerID: host2.ID(), peerScore: 200}
	exchg.peerTracker.peerLk.Unlock()
	_, err = exchg.GetRangeByHeight(context.Background(), 1, 3)
	require.NoError(t, err)
	// ensure that peerScore for the first peer was decrease by 20%
	newPeerScore := exchg.peerTracker.trackedPeers[host2.ID()].score()
	assert.Less(t, newPeerScore, float32(200))
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
) (*Exchange[*test.DummyHeader], *headerMock.MockStore[*test.DummyHeader]) {
	store := headerMock.NewStore[*test.DummyHeader](t, test.NewTestSuite(t), 5)
	serverSideEx, err := NewExchangeServer[*test.DummyHeader](tpeer, store, "private")
	require.NoError(t, err)
	err = serverSideEx.Start(context.Background())
	require.NoError(t, err)
	connGater, err := conngater.NewBasicConnectionGater(sync.MutexWrap(datastore.NewMapDatastore()))
	require.NoError(t, err)
	ex, err := NewExchange[*test.DummyHeader](host, []peer.ID{tpeer.ID()}, "private", connGater)
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

type timedOutStore struct {
	timeout time.Duration
}

func (t *timedOutStore) Head(context.Context) (*test.DummyHeader, error) {
	// TODO implement me
	panic("implement me")
}

func (t *timedOutStore) Get(context.Context, header.Hash) (*test.DummyHeader, error) {
	// TODO implement me
	panic("implement me")
}

func (t *timedOutStore) GetByHeight(context.Context, uint64) (*test.DummyHeader, error) {
	// TODO implement me
	panic("implement me")
}

func (t *timedOutStore) GetRangeByHeight(_ context.Context, _, _ uint64) ([]*test.DummyHeader, error) {
	time.Sleep(t.timeout + 1)
	return []*test.DummyHeader{}, nil
}

func (t *timedOutStore) GetVerifiedRange(
	context.Context,
	*test.DummyHeader,
	uint64,
) ([]*test.DummyHeader, error) {
	// TODO implement me
	panic("implement me")
}
