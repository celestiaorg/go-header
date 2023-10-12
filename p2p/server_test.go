package p2p

import (
	"context"
	"testing"

	"github.com/ipfs/go-datastore"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/store"
)

func TestExchangeServer_handleRequestTimeout(t *testing.T) {
	peer := createMocknet(t, 1)
	s, err := store.NewStore[*headertest.DummyHeader](datastore.NewMapDatastore())
	require.NoError(t, err)
	server, err := NewExchangeServer[*headertest.DummyHeader](
		peer[0],
		s,
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	err = server.Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		server.Stop(context.Background()) //nolint:errcheck
	})

	_, err = server.handleRequest(1, 200)
	require.Error(t, err)
}

func TestExchangeServer_errorsOnLargeRequest(t *testing.T) {
	peer := createMocknet(t, 1)
	s, err := store.NewStore[*headertest.DummyHeader](datastore.NewMapDatastore())
	require.NoError(t, err)
	server, err := NewExchangeServer[*headertest.DummyHeader](
		peer[0],
		s,
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)
	err = server.Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		server.Stop(context.Background()) //nolint:errcheck
	})

	_, err = server.handleRequest(1, header.MaxRangeRequestSize*2)
	require.Error(t, err)
}
