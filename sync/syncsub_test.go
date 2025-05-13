package sync

import (
	"context"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/p2p"
)

func TestSyncerWithSubscriber(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	netw, err := mocknet.FullMeshLinked(1)
	require.NoError(t, err)

	gossipSub, err := pubsub.NewGossipSub(
		ctx,
		netw.Hosts()[0],
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
	)
	require.NoError(t, err)

	p2pSub, err := p2p.NewSubscriber[*headertest.DummyHeader](
		gossipSub,
		pubsub.DefaultMsgIdFn,
	)
	require.NoError(t, err)
	err = p2pSub.Start(context.Background())
	require.NoError(t, err)

	sub, err := p2pSub.Subscribe()
	require.NoError(t, err)

	head := suite.Head()
	syncer, err := NewSyncer(
		newTestStore(t, ctx, head),
		newTestStore(t, ctx, head),
		p2pSub,
	)
	require.NoError(t, err)
	err = syncer.Start(ctx)
	require.NoError(t, err)

	t.Cleanup(func() {
		err = syncer.Stop(ctx)
		require.NoError(t, err)
	})

	expectedHeader := suite.GenDummyHeaders(1)[0]

	err = p2pSub.Broadcast(ctx, expectedHeader)
	require.NoError(t, err)

	header, err := sub.NextHeader(ctx)
	require.NoError(t, err)
	assert.Equal(t, expectedHeader.Height(), header.Height())
	assert.Equal(t, expectedHeader.Hash(), header.Hash())

	state := syncer.State()
	require.NoError(t, err)
	assert.Equal(t, expectedHeader.Height(), state.Height)
}
