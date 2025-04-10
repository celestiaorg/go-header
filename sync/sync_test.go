package sync

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/local"
	"github.com/celestiaorg/go-header/store"
)

func TestSyncSimpleRequestingHead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	err := remoteStore.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)

	_, err = remoteStore.GetByHeight(ctx, 100)
	require.NoError(t, err)

	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		local.NewExchange(remoteStore),
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Second*30),
		WithRecencyThreshold(time.Second*35), // add 5 second buffer
		WithTrustingPeriod(time.Microsecond),
	)
	require.NoError(t, err)
	err = syncer.Start(ctx)
	require.NoError(t, err)

	time.Sleep(time.Millisecond * 100) // needs some to realize it is syncing
	err = syncer.SyncWait(ctx)
	require.NoError(t, err)

	// force sync to update underlying stores.
	syncer.wantSync()

	// we need to wait for a flush
	assert.Eventually(t, func() bool {
		exp, err := remoteStore.Head(ctx)
		require.NoError(t, err)

		have, err := localStore.Head(ctx)
		require.NoError(t, err)

		state := syncer.State()
		switch {
		case exp.Height() != have.Height():
			return false
		case syncer.pending.Head() != nil:
			return false

		case exp.Height() != state.Height:
			return false
		case uint64(2) != state.FromHeight:
			return false

		case exp.Height() != state.ToHeight:
			return false
		case !state.Finished():
			return false
		default:
			return true
		}
	}, 2*time.Second, 100*time.Millisecond)
}

func TestDoSyncFullRangeFromExternalPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		local.NewExchange(remoteStore),
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)
	require.NoError(t, syncer.Start(ctx))

	err = remoteStore.Append(ctx, suite.GenDummyHeaders(int(header.MaxRangeRequestSize))...)
	require.NoError(t, err)
	// give store time to update heightSub index
	time.Sleep(time.Millisecond * 100)

	// trigger sync by calling Head
	_, err = syncer.Head(ctx)
	require.NoError(t, err)

	// give store time to sync
	time.Sleep(time.Millisecond * 100)

	remoteHead, err := remoteStore.Head(ctx)
	require.NoError(t, err)

	newHead, err := localStore.Head(ctx)
	require.NoError(t, err)
	require.Equal(t, newHead.Height(), remoteHead.Height())
}

func TestSyncCatchUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		local.NewExchange(remoteStore),
		localStore,
		headertest.NewDummySubscriber(),
		WithTrustingPeriod(time.Minute),
	)
	require.NoError(t, err)
	// 1. Initial sync
	err = syncer.Start(ctx)
	require.NoError(t, err)

	// 2. chain grows and syncer misses that
	err = remoteStore.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)

	incomingHead := suite.GenDummyHeaders(1)[0]
	// 3. syncer rcvs header from the future and starts catching-up
	err = syncer.incomingNetworkHead(ctx, incomingHead)
	require.NoError(t, err)

	time.Sleep(time.Millisecond * 100) // needs some to realize it is syncing
	err = syncer.SyncWait(ctx)
	require.NoError(t, err)

	exp, err := remoteStore.Head(ctx)
	require.NoError(t, err)

	// 4. assert syncer caught-up
	have, err := localStore.Head(ctx)
	require.NoError(t, err)

	assert.Equal(t, have.Height(), incomingHead.Height())
	assert.Equal(
		t,
		exp.Height()+1,
		have.Height(),
	) // plus one as we didn't add last header to remoteStore
	assert.Empty(t, syncer.pending.Head())

	state := syncer.State()
	assert.Equal(t, exp.Height()+1, state.Height)
	assert.EqualValues(t, 2, state.FromHeight)
	assert.Equal(t, exp.Height()+1, state.ToHeight)
	assert.True(t, state.Finished(), state)
}

func TestSyncPendingRangesWithMisses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		local.NewExchange(remoteStore),
		localStore,
		headertest.NewDummySubscriber(),
		WithTrustingPeriod(time.Minute),
	)
	require.NoError(t, err)
	err = syncer.Start(ctx)
	require.NoError(t, err)

	// miss 1 (helps to test that Syncer properly requests missed Headers from Exchange)
	err = remoteStore.Append(ctx, suite.GenDummyHeaders(1)...)
	require.NoError(t, err)

	range1 := suite.GenDummyHeaders(15)
	err = remoteStore.Append(ctx, range1...)
	require.NoError(t, err)

	// miss 2
	err = remoteStore.Append(ctx, suite.GenDummyHeaders(3)...)
	require.NoError(t, err)

	range2 := suite.GenDummyHeaders(23)
	err = remoteStore.Append(ctx, range2...)
	require.NoError(t, err)

	// manually add to pending
	for _, h := range append(range1, range2...) {
		syncer.pending.Add(h)
	}

	// and fire up a sync
	syncer.sync(ctx)

	_, err = remoteStore.GetByHeight(ctx, 43)
	require.NoError(t, err)
	_, err = localStore.GetByHeight(ctx, 43)
	require.NoError(t, err)

	lastHead, err := syncer.store.Head(ctx)
	require.NoError(t, err)
	require.Equal(t, lastHead.Height(), uint64(43))
	exp, err := remoteStore.Head(ctx)
	require.NoError(t, err)

	// we need to wait for a flush
	assert.Eventually(t, func() bool {
		have, err := localStore.Head(ctx)
		require.NoError(t, err)

		switch {
		case exp.Height() != have.Height():
			return false
		case !syncer.pending.Head().IsZero():
			return false
		default:
			return true
		}
	}, 2*time.Second, 100*time.Millisecond)
}

// TestSyncer_FindHeadersReturnsCorrectRange ensures that `findHeaders` returns
// range [from;to]
func TestSyncer_FindHeadersReturnsCorrectRange(t *testing.T) {
	// Test consists of 3 steps:
	// 1. get range of headers from pending; [2;11]
	// 2. get headers from the remote store; [12;20]
	// 3. apply last header from pending;
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		local.NewExchange(remoteStore),
		localStore,
		headertest.NewDummySubscriber(),
	)
	require.NoError(t, err)

	range1 := suite.GenDummyHeaders(10)
	// manually add to pending
	for _, h := range range1 {
		syncer.pending.Add(h)
	}
	err = remoteStore.Append(ctx, range1...)
	require.NoError(t, err)
	err = remoteStore.Append(ctx, suite.GenDummyHeaders(9)...)
	require.NoError(t, err)

	syncer.pending.Add(suite.NextHeader())
	require.NoError(t, err)
	err = syncer.processHeaders(ctx, head, 21)
	require.NoError(t, err)

	head, err = syncer.store.Head(ctx)
	require.NoError(t, err)
	assert.Equal(t, head.Height(), uint64(21))
}

func TestSyncerIncomingDuplicate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	remoteStore := newTestStore(t, ctx, head)
	localStore := newTestStore(t, ctx, head)
	syncer, err := NewSyncer(
		&delayedGetter[*headertest.DummyHeader]{Getter: local.NewExchange(remoteStore)},
		localStore,
		headertest.NewDummySubscriber(),
	)
	require.NoError(t, err)
	err = syncer.Start(ctx)
	require.NoError(t, err)

	range1 := suite.GenDummyHeaders(10)
	err = remoteStore.Append(ctx, range1...)
	require.NoError(t, err)

	err = syncer.incomingNetworkHead(ctx, range1[len(range1)-1])
	require.NoError(t, err)

	time.Sleep(time.Millisecond * 10)

	var verErr *header.VerifyError
	err = syncer.incomingNetworkHead(ctx, range1[len(range1)-1])
	assert.ErrorAs(t, err, &verErr)

	err = syncer.SyncWait(ctx)
	require.NoError(t, err)
}

// TestSync_SoftFailureBifurcate asserts that network head soft failure is handled correctly,
// triggering a bifurcation.
// The expected behavior is that the syncer discards the invalid sync target if bifurcation confirms
// its invalidity. Yet it recovers if the new sync target is valid.
func TestSync_SoftFailureBifurcate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	// create a local store which is initialized at genesis height
	localStore := newTestStore(t, ctx, head)
	// create a peer which is already on height 100
	remoteStore := headertest.NewStore(t, suite, 100)

	syncer, err := NewSyncer(
		local.NewExchange(remoteStore),
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Nanosecond),
		WithRecencyThreshold(time.Nanosecond), // force syncer to request more recent sync target
	)
	require.NoError(t, err)

	// generate 300 more headers
	headers := suite.GenDummyHeaders(300)
	// malform the remote store's head so that it can serve
	// the syncer a soft failed head
	maliciousHeader := headers[299]
	maliciousHeader.VerifyFailure = true
	maliciousHeader.SoftFailure = true
	err = remoteStore.Append(ctx, headers...)
	require.NoError(t, err)

	// start syncer so that it immediately requests the bad
	// sync target from the remote peer via Head request
	err = syncer.Start(ctx)
	require.NoError(t, err)
	// await for sync to finish
	time.Sleep(time.Millisecond * 100)
	err = syncer.SyncWait(ctx)
	require.NoError(t, err)

	// ensure that syncer progressed even with invalid header
	// yet excluding the invalid header
	state := syncer.State()
	assert.Equal(t, maliciousHeader.Height()-1, state.Height)
	assert.True(t, state.Finished())
	assert.EqualValues(t, 2, state.FromHeight)

	// ensure syncer could only sync up to one header below the invalid header
	h, err := localStore.Head(ctx)
	require.NoError(t, err)
	require.Equal(t, maliciousHeader.Height()-1, h.Height())

	// manually change bad sync target to a good header in remote peer
	// store so it can re-serve it to syncer once it re-requests the height
	remoteStore.Headers[maliciousHeader.Height()].VerifyFailure = false

	// generate more headers
	err = remoteStore.Append(ctx, suite.GenDummyHeaders(100)...)
	require.NoError(t, err)

	// pretend new header is received from network to trigger
	// a new sync job to a good sync target
	expectedHead, err := remoteStore.Head(ctx)
	require.NoError(t, err)
	err = syncer.incomingNetworkHead(ctx, expectedHead)
	require.NoError(t, err)

	// wait for syncer to finish (give it a bit of time to register
	// new job with new sync target)
	time.Sleep(100 * time.Millisecond)
	err = syncer.SyncWait(ctx)
	require.NoError(t, err)

	// ensure that maliciousHeader height was re-requested and a good one was
	// found
	rerequested, err := localStore.GetByHeight(ctx, maliciousHeader.Height())
	require.NoError(t, err)
	require.False(t, rerequested.VerifyFailure)

	// check store head and syncer head
	storeHead, err := localStore.Head(ctx)
	require.NoError(t, err)
	syncHead, err := syncer.Head(ctx)
	require.NoError(t, err)

	require.Equal(t, expectedHead.Height(), syncHead.Height())
	require.Equal(t, expectedHead.Height(), storeHead.Height())
}

type delayedGetter[H header.Header[H]] struct {
	header.Getter[H]
}

func (d *delayedGetter[H]) GetRangeByHeight(ctx context.Context, from H, to uint64) ([]H, error) {
	select {
	case <-time.After(time.Millisecond * 100):
		return d.Getter.GetRangeByHeight(ctx, from, to)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// newTestStore creates initialized and started in memory header Store which is useful for testing.
func newTestStore(
	tb testing.TB,
	ctx context.Context,
	head *headertest.DummyHeader,
) header.Store[*headertest.DummyHeader] {
	ds := sync.MutexWrap(datastore.NewMapDatastore())
	return store.NewTestStore(tb, ctx, ds, head)
}
