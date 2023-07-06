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

	"github.com/celestiaorg/go-header/headertest"
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
