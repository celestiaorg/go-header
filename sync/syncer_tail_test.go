package sync

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/store"
)

func TestSyncer_TailHashOverHeight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	remoteStore := headertest.NewStore[*headertest.DummyHeader](t, headertest.NewTestSuite(t), 100)

	ds := dssync.MutexWrap(datastore.NewMapDatastore())
	localStore, err := store.NewStore[*headertest.DummyHeader](
		ds,
		store.WithWriteBatchSize(1),
	)
	require.NoError(t, err)
	err = localStore.Start(ctx)
	require.NoError(t, err)

	startFrom, err := remoteStore.GetByHeight(ctx, 50)
	require.NoError(t, err)

	syncer, err := NewSyncer[*headertest.DummyHeader](
		remoteStore,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(headertest.HeaderTime),
		WithSyncFromHash(startFrom.Hash()),
	)
	require.NoError(t, err)

	err = syncer.Start(ctx)
	require.NoError(t, err)
	time.Sleep(time.Millisecond * 10)
	err = syncer.SyncWait(ctx)
	require.NoError(t, err)

	tail, err := localStore.Tail(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 50, tail.Height())

	err = syncer.Stop(ctx)
	require.NoError(t, err)

	syncer.Params.SyncFromHeight = 99

	err = syncer.Start(ctx)
	require.NoError(t, err)
	time.Sleep(time.Millisecond * 10)

	tail, err = localStore.Tail(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 50, tail.Height())
}

func TestSyncer_TailEstimation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*50)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	remoteStore := headertest.NewStore[*headertest.DummyHeader](t, suite, 100)

	ds := dssync.MutexWrap(datastore.NewMapDatastore())
	localStore, err := store.NewStore[*headertest.DummyHeader](
		ds,
		store.WithWriteBatchSize(1),
	)
	require.NoError(t, err)
	err = localStore.Start(ctx)
	require.NoError(t, err)

	syncer, err := NewSyncer[*headertest.DummyHeader](
		remoteStore,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(headertest.HeaderTime),
		WithPruningWindow(time.Nanosecond*50),
	)
	require.NoError(t, err)

	err = syncer.Start(ctx)
	require.NoError(t, err)
	time.Sleep(time.Millisecond * 10)
	err = syncer.SyncWait(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 100, syncer.State().Height)

	tail, err := localStore.Tail(ctx)
	require.NoError(t, err)
	require.EqualValues(t, tail.Height(), 1)

	// simulate new head
	err = remoteStore.Append(ctx, suite.NextHeader())
	require.NoError(t, err)

	// trigger recency check
	head, err := syncer.Head(ctx)
	require.NoError(t, err)
	require.Equal(t, head.Height(), remoteStore.Height())

	tail, err = localStore.Tail(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 51, tail.Height())
}

func TestSyncer_TailReconfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	remoteStore := headertest.NewStore[*headertest.DummyHeader](t, suite, 100)

	ds := dssync.MutexWrap(datastore.NewMapDatastore())
	localStore, err := store.NewStore[*headertest.DummyHeader](
		ds,
		store.WithWriteBatchSize(1),
	)
	require.NoError(t, err)
	err = localStore.Start(ctx)
	require.NoError(t, err)

	syncer, err := NewSyncer[*headertest.DummyHeader](
		remoteStore,
		localStore,
		headertest.NewDummySubscriber(),
		WithBlockTime(time.Second*6),
		WithRecencyThreshold(time.Nanosecond),
	)
	require.NoError(t, err)

	err = syncer.Start(ctx)
	require.NoError(t, err)
	time.Sleep(time.Millisecond * 10)
	err = syncer.SyncWait(ctx)
	require.NoError(t, err)
	err = syncer.Stop(ctx)
	require.NoError(t, err)
	time.Sleep(time.Millisecond * 10)

	syncer.Params.SyncFromHeight = 69

	// simulate new head
	err = remoteStore.Append(ctx, suite.NextHeader())
	require.NoError(t, err)

	err = syncer.Start(ctx)
	require.NoError(t, err)

	storeTail, err := localStore.Tail(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, syncer.Params.SyncFromHeight, storeTail.Height())
}

func TestSyncer_TailInitialization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	suite := headertest.NewTestSuite(t)
	remoteStore := headertest.NewStore[*headertest.DummyHeader](t, suite, 100)

	expectedTail, err := remoteStore.GetByHeight(ctx, 69)
	require.NoError(t, err)

	tests := []struct {
		name                 string
		option               Option
		expected             func() *headertest.DummyHeader
		expectedAfterRestart func() *headertest.DummyHeader
	}{
		{
			"Estimate",
			func(p *Parameters) {}, // noop to trigger estimation,
			func() *headertest.DummyHeader {
				remoteTail, err := remoteStore.Tail(ctx)
				require.NoError(t, err)
				return remoteTail
			},
			func() *headertest.DummyHeader {
				remoteTail, err := remoteStore.Tail(ctx)
				require.NoError(t, err)
				return remoteTail
			},
		},
		{
			"SyncFromHash",
			WithSyncFromHash(expectedTail.Hash()),
			func() *headertest.DummyHeader {
				return expectedTail
			},
			func() *headertest.DummyHeader {
				expectedTail, err := remoteStore.GetByHeight(ctx, expectedTail.Height()+10)
				require.NoError(t, err)
				return expectedTail
			},
		},
		{
			"SyncFromHeight",
			WithSyncFromHeight(expectedTail.Height()),
			func() *headertest.DummyHeader {
				return expectedTail
			},
			func() *headertest.DummyHeader {
				expectedTail, err := remoteStore.GetByHeight(ctx, expectedTail.Height()-10)
				require.NoError(t, err)
				return expectedTail
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds := dssync.MutexWrap(datastore.NewMapDatastore())
			localStore, err := store.NewStore[*headertest.DummyHeader](
				ds,
				store.WithWriteBatchSize(1),
			)
			require.NoError(t, err)
			err = localStore.Start(ctx)
			require.NoError(t, err)

			syncer, err := NewSyncer[*headertest.DummyHeader](
				remoteStore,
				localStore,
				headertest.NewDummySubscriber(),
				// make sure the blocktime is set for proper tail estimation
				WithBlockTime(headertest.HeaderTime),
				test.option,
			)
			require.NoError(t, err)

			err = syncer.Start(ctx)
			require.NoError(t, err)
			time.Sleep(time.Millisecond * 100)

			// check that the syncer has the expected tail and head
			expectedTail := test.expected()
			storeTail, err := localStore.Tail(ctx)
			require.NoError(t, err)
			assert.EqualValues(t, expectedTail.Height(), storeTail.Height())
			storeHead, err := localStore.Head(ctx)
			require.NoError(t, err)
			assert.EqualValues(t, remoteStore.Height(), storeHead.Height())

			// restart the Syncer and set a new tail
			err = syncer.Stop(ctx)
			require.NoError(t, err)
			expectedTail = test.expectedAfterRestart()
			syncer.Params.SyncFromHeight = expectedTail.Height()
			syncer.Params.SyncFromHash = expectedTail.Hash()

			// simulate new head
			err = remoteStore.Append(ctx, suite.NextHeader())
			require.NoError(t, err)
			err = syncer.Start(ctx)
			require.NoError(t, err)

			time.Sleep(time.Millisecond * 10)

			// ensure that the Syncer moved to the new tail after restart
			storeTail, err = localStore.Tail(ctx)
			require.NoError(t, err)
			assert.EqualValues(t, expectedTail.Height(), storeTail.Height())
		})
	}
}
