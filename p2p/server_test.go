package p2p

import (
	"context"
	"testing"
	"time"

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

	_, err = server.handleRangeRequest(1, 200)
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

	_, err = server.handleRangeRequest(1, header.MaxRangeRequestSize*2)
	require.Error(t, err)
}

func TestExchangeServer_Timeout(t *testing.T) {
	const testRangeRequestTimeout = 150 * time.Millisecond

	peer := createMocknet(t, 1)

	server, err := NewExchangeServer(
		peer[0],
		dummyStore[*headertest.DummyHeader]{},
		WithNetworkID[ServerParameters](networkID),
		WithRangeRequestTimeout[ServerParameters](time.Second),
	)
	require.NoError(t, err)

	err = server.Start(context.Background())
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = server.Stop(context.Background())
	})

	testCases := []struct {
		name string
		fn   func() error
	}{
		{
			name: "handleHeadRequest",
			fn: func() error {
				_, err := server.handleHeadRequest()
				return err
			},
		},
		{
			name: "handleRequest",
			fn: func() error {
				_, err := server.handleRangeRequest(1, 100)
				return err
			},
		},
		{
			name: "handleHeadRequest",
			fn: func() error {
				hash := headertest.RandDummyHeader(t).Hash()
				_, err := server.handleRequestByHash(hash)
				return err
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			start := time.Now()
			err := tc.fn()
			took := time.Since(start)

			require.Error(t, err)
			require.Greater(t, took, testRangeRequestTimeout)
		})
	}
}

type dummyStore[H header.Header[H]] struct{}

func (dummyStore[H]) Head(ctx context.Context, _ ...header.HeadOption[H]) (H, error) {
	<-ctx.Done()
	var zero H
	return zero, ctx.Err()
}

func (dummyStore[H]) Get(ctx context.Context, _ header.Hash) (H, error) {
	<-ctx.Done()
	var zero H
	return zero, ctx.Err()
}

func (dummyStore[H]) GetByHeight(ctx context.Context, _ uint64) (H, error) {
	<-ctx.Done()
	var zero H
	return zero, ctx.Err()
}

func (dummyStore[H]) GetRangeByHeight(ctx context.Context, from H, to uint64) ([]H, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (dummyStore[H]) Init(ctx context.Context, _ H) error {
	<-ctx.Done()
	return ctx.Err()
}

func (dummyStore[H]) Height() uint64 {
	return 0
}

func (dummyStore[H]) Has(ctx context.Context, _ header.Hash) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (dummyStore[H]) HasAt(ctx context.Context, _ uint64) bool {
	<-ctx.Done()
	return false
}

func (dummyStore[H]) Append(ctx context.Context, _ ...H) error {
	<-ctx.Done()
	return ctx.Err()
}

func (dummyStore[H]) GetRange(ctx context.Context, _ uint64, _ uint64) ([]H, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
