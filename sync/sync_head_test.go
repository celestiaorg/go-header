package sync

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/local"
	"github.com/celestiaorg/go-header/store"
)

func TestSyncer_incomingNetworkHeadRaces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)

	store := headertest.NewStore[*headertest.DummyHeader](t, suite, 1)
	syncer, err := NewSyncer[*headertest.DummyHeader](
		store,
		store,
		headertest.NewDummySubscriber(),
	)
	require.NoError(t, err)

	incoming := suite.NextHeader()

	var hits atomic.Uint32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if syncer.incomingNetworkHead(ctx, incoming) == pubsub.ValidationAccept {
				hits.Add(1)
			}
		}()
	}

	wg.Wait()
	assert.EqualValues(t, 1, hits.Load())

}

func TestSubjectiveInit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := store.NewTestStore(ctx, t, head)
	err := remoteStore.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)

	_, err = remoteStore.GetByHeight(ctx, 100)
	require.NoError(t, err)

	localStore := store.NewTestStore(ctx, t, head)
	syncer, err := NewSyncer[*headertest.DummyHeader](
		local.NewExchange(remoteStore),
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Second*30),
		WithTrustingPeriod(time.Microsecond),
	)
	require.NoError(t, err)
	err = syncer.Start(ctx)
	require.NoError(t, err)
}

type trustedGetter[H header.Header] struct {
	hits int
}

func (t *trustedGetter[H]) Head(ctx context.Context, option ...header.RequestOption) (H, error) {
	//TODO implement me
	panic("implement me")
}

func (t *trustedGetter[H]) Get(ctx context.Context, hash header.Hash) (H, error) {
	//TODO implement me
	panic("implement me")
}

func (t *trustedGetter[H]) GetByHeight(ctx context.Context, u uint64) (H, error) {
	//TODO implement me
	panic("implement me")
}

func (t *trustedGetter[H]) GetRangeByHeight(ctx context.Context, from, amount uint64) ([]H, error) {
	//TODO implement me
	panic("implement me")
}

func (t *trustedGetter[H]) GetVerifiedRange(ctx context.Context, from H, amount uint64) ([]H, error) {
	//TODO implement me
	panic("implement me")
}
