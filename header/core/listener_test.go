package core

import (
	"context"
	"testing"
	"time"

	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/libp2p/go-libp2p-core/event"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/celestia-node/core"
	"github.com/celestiaorg/celestia-node/header"
	"github.com/celestiaorg/celestia-node/header/p2p"
)

// TestListener tests the lifecycle of the core listener.
func TestListener(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	// create mocknet with two pubsub endpoints
	ps0, ps1 := createMocknetWithTwoPubsubEndpoints(ctx, t)
	// create second subscription endpoint to listen for Listener's pubsub messages
	topic, err := ps1.Join(p2p.PubSubTopic)
	require.NoError(t, err)
	sub, err := topic.Subscribe()
	require.NoError(t, err)

	// create one block to store as Head in local store and then unsubscribe from block events
	fetcher := createCoreFetcher(t)

	// create Listener and start listening
	cl := createListener(ctx, t, fetcher, ps0)
	err = cl.Start(ctx)
	require.NoError(t, err)

	// ensure headers are getting broadcasted to the gossipsub topic
	for i := 1; i < 6; i++ {
		msg, err := sub.Next(ctx)
		require.NoError(t, err)

		var resp header.ExtendedHeader
		err = resp.UnmarshalBinary(msg.Data)
		require.NoError(t, err)
	}

	err = cl.Stop(ctx)
	require.NoError(t, err)
	require.Nil(t, cl.cancel)
}

func createMocknetWithTwoPubsubEndpoints(ctx context.Context, t *testing.T) (*pubsub.PubSub, *pubsub.PubSub) {
	net, err := mocknet.FullMeshLinked(context.Background(), 2)
	require.NoError(t, err)
	host0, host1 := net.Hosts()[0], net.Hosts()[1]

	sub0, err := host0.EventBus().Subscribe(&event.EvtPeerIdentificationCompleted{})
	require.NoError(t, err)
	sub1, err := host1.EventBus().Subscribe(&event.EvtPeerIdentificationCompleted{})
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

	// create pubsub for host
	ps0, err := pubsub.NewGossipSub(context.Background(), host0,
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign))
	require.NoError(t, err)
	// create pubsub for peer-side (to test broadcast comes through network)
	ps1, err := pubsub.NewGossipSub(context.Background(), host1,
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign))
	require.NoError(t, err)
	return ps0, ps1
}

func createListener(
	ctx context.Context,
	t *testing.T,
	fetcher *core.BlockFetcher,
	ps *pubsub.PubSub,
) *Listener {
	p2pSub := p2p.NewSubscriber(ps)
	err := p2pSub.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		err := p2pSub.Stop(ctx)
		require.NoError(t, err)
	})

	return NewListener(p2pSub, fetcher, mdutils.Mock())
}
