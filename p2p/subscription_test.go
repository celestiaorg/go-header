package p2p

import (
	"context"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/event"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/headertest"
)

// TestSubscriber a simple test to check if the subscriber can receive headers from the network.
func TestSubscriber(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()

	// create mock network
	net, err := mocknet.FullMeshLinked(2)
	require.NoError(t, err)

	suite := headertest.NewTestSuite(t)

	// get mock host and create new gossipsub on it
	pubsub1, err := pubsub.NewGossipSub(
		ctx,
		net.Hosts()[0],
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
	)
	require.NoError(t, err)

	// create sub-service lifecycles for header service 1
	p2pSub1, err := NewSubscriber[*headertest.DummyHeader](
		pubsub1,
		pubsub.DefaultMsgIdFn,
		WithSubscriberNetworkID(networkID),
	)
	require.NoError(t, err)
	err = p2pSub1.Start(context.Background())
	require.NoError(t, err)
	err = p2pSub1.SetVerifier(func(context.Context, *headertest.DummyHeader) error {
		return nil
	})
	require.NoError(t, err)

	// get mock host and create new gossipsub on it
	pubsub2, err := pubsub.NewGossipSub(ctx, net.Hosts()[1],
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign))
	require.NoError(t, err)

	// create sub-service lifecycles for header service 2
	p2pSub2, err := NewSubscriber[*headertest.DummyHeader](
		pubsub2,
		pubsub.DefaultMsgIdFn,
		WithSubscriberNetworkID(networkID),
	)
	require.NoError(t, err)
	err = p2pSub2.Start(context.Background())
	require.NoError(t, err)
	err = p2pSub2.SetVerifier(func(context.Context, *headertest.DummyHeader) error {
		return nil
	})
	require.NoError(t, err)

	sub0, err := net.Hosts()[0].EventBus().Subscribe(&event.EvtPeerIdentificationCompleted{})
	require.NoError(t, err)
	sub1, err := net.Hosts()[1].EventBus().Subscribe(&event.EvtPeerIdentificationCompleted{})
	require.NoError(t, err)

	err = net.ConnectAllButSelf()
	require.NoError(t, err)

	// wait on both peer identification events
	for i := 0; i < 2; i++ {
		select {
		case <-sub0.Out():
		case <-sub1.Out():
		case <-ctx.Done():
			assert.FailNow(t, "timeout waiting for peers to connect")
		}
	}

	// subscribe
	senderSubscription, err := p2pSub2.Subscribe()
	require.NoError(t, err)

	subscription, err := p2pSub1.Subscribe()
	require.NoError(t, err)

	expectedHeader := suite.GenDummyHeaders(1)[0]
	err = p2pSub2.Broadcast(ctx, expectedHeader, pubsub.WithReadiness(pubsub.MinTopicSize(1)))
	require.NoError(t, err)

	// get next Header from network
	header, err := subscription.NextHeader(ctx)
	require.NoError(t, err)
	assert.Equal(t, expectedHeader.Height(), header.Height())
	assert.Equal(t, expectedHeader.Hash(), header.Hash())

	header, err = senderSubscription.NextHeader(ctx)
	require.NoError(t, err)
	assert.Equal(t, expectedHeader.Height(), header.Height())
	assert.Equal(t, expectedHeader.Hash(), header.Hash())
}
