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
	head := headertest.RandDummyHeader(t)
	head.HeightI %= 1000 // make it a bit lower
	err = s.Append(context.Background(), head)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
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

	_, err = server.handleRangeRequest(context.Background(), 1, 200)
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

	_, err = server.handleRangeRequest(context.Background(), 1, header.MaxRangeRequestSize*2)
	require.Error(t, err)
}

func TestExchangeServer_Timeout(t *testing.T) {
	const testRequestTimeout = 150 * time.Millisecond

	peer := createMocknet(t, 1)

	server, err := NewExchangeServer(
		peer[0],
		timeoutStore[*headertest.DummyHeader]{},
		WithNetworkID[ServerParameters](networkID),
		WithRequestTimeout[ServerParameters](testRequestTimeout),
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
				ctx, cancel := context.WithTimeout(context.Background(), testRequestTimeout)
				defer cancel()

				_, err := server.handleHeadRequest(ctx)
				return err
			},
		},
		{
			name: "handleRequest",
			fn: func() error {
				ctx, cancel := context.WithTimeout(context.Background(), testRequestTimeout)
				defer cancel()

				_, err := server.handleRangeRequest(ctx, 1, header.MaxRangeRequestSize)
				return err
			},
		},
		{
			name: "handleHeadRequest",
			fn: func() error {
				ctx, cancel := context.WithTimeout(context.Background(), testRequestTimeout)
				defer cancel()

				hash := headertest.RandDummyHeader(t).Hash()
				_, err := server.handleRequestByHash(ctx, hash)
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
			require.GreaterOrEqual(t, took, testRequestTimeout)
		})
	}
}

// TestExchangeServer_partialRangeNotExpanded ensures that a request for a range
// below the tail of a non-archival node does not expand `to` up to the head.
// Regression test: requesting [1;2) against a node with a very high head used to
// overwrite `to` with head.Height()+1, forcing GetRange over a huge range (OOM).
func TestExchangeServer_partialRangeNotExpanded(t *testing.T) {
	peer := createMocknet(t, 1)

	head := headertest.RandDummyHeader(t)
	head.HeightI = 10_000_000 // simulate a node synced far ahead
	store := &partialRangeStore[*headertest.DummyHeader]{head: head}

	server, err := NewExchangeServer[*headertest.DummyHeader](
		peer[0],
		store,
		WithNetworkID[ServerParameters](networkID),
	)
	require.NoError(t, err)

	// request a low range that is not stored (pruned/below tail)
	_, err = server.handleRangeRequest(context.Background(), 1, 2)
	require.NoError(t, err)

	// `to` must stay at the requested value, not be expanded to head.Height()+1
	require.Equal(t, uint64(2), store.gotTo)
}

// partialRangeStore is a minimal store where every height is reported as absent
// (HasAt == false) and the head is far ahead. It records the `to` passed to GetRange.
type partialRangeStore[H header.Header[H]] struct {
	header.Store[H]
	head  H
	gotTo uint64
}

func (s *partialRangeStore[H]) HasAt(context.Context, uint64) bool { return false }

func (s *partialRangeStore[H]) Head(context.Context, ...header.HeadOption[H]) (H, error) {
	return s.head, nil
}

func (s *partialRangeStore[H]) GetRange(_ context.Context, _, to uint64) ([]H, error) {
	s.gotTo = to
	return nil, nil
}

var _ header.Store[*headertest.DummyHeader] = timeoutStore[*headertest.DummyHeader]{}

// timeoutStore does nothing but waits till context cancellation for every method.
type timeoutStore[H header.Header[H]] struct{}

func (timeoutStore[H]) Head(ctx context.Context, _ ...header.HeadOption[H]) (H, error) {
	<-ctx.Done()
	var zero H
	return zero, ctx.Err()
}

func (timeoutStore[H]) Tail(ctx context.Context) (H, error) {
	<-ctx.Done()
	var zero H
	return zero, ctx.Err()
}

func (timeoutStore[H]) Get(ctx context.Context, _ header.Hash) (H, error) {
	<-ctx.Done()
	var zero H
	return zero, ctx.Err()
}

func (timeoutStore[H]) GetByHeight(ctx context.Context, _ uint64) (H, error) {
	<-ctx.Done()
	var zero H
	return zero, ctx.Err()
}

func (timeoutStore[H]) GetRangeByHeight(ctx context.Context, from H, to uint64) ([]H, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (timeoutStore[H]) Init(ctx context.Context, _ H) error {
	<-ctx.Done()
	return ctx.Err()
}

func (timeoutStore[H]) Height() uint64 {
	return 0
}

func (timeoutStore[H]) Has(ctx context.Context, _ header.Hash) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (timeoutStore[H]) HasAt(ctx context.Context, _ uint64) bool {
	<-ctx.Done()
	return false
}

func (timeoutStore[H]) Append(ctx context.Context, _ ...H) error {
	<-ctx.Done()
	return ctx.Err()
}

func (timeoutStore[H]) GetRange(ctx context.Context, _, _ uint64) ([]H, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (timeoutStore[H]) DeleteRange(ctx context.Context, _, _ uint64) error {
	<-ctx.Done()
	return ctx.Err()
}

func (timeoutStore[H]) OnDelete(fn func(context.Context, uint64) error) {}
