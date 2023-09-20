package main

import (
	"context"
	logging "github.com/ipfs/go-log/v2"
	"testing"
	"time"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/local"
	"github.com/celestiaorg/go-header/store"
	"github.com/celestiaorg/go-header/sync"
)

func main() {
	t := &testing.T{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	maliciousSuite := headertest.NewTestSuiteWithHead(t, head)
	maliciousHead := maliciousSuite.Head()

	// set up syncer with a malicious peer as its remote peer
	ee := newEclipsedExchange(ctx, t, head, maliciousHead)

	localStore := store.NewTestStore(ctx, t, head)
	syncer, err := sync.NewSyncer[*headertest.DummyHeader](
		local.NewExchange[*headertest.DummyHeader](ee),
		localStore,
		headertest.NewDummySubscriber(),
		// TrustingPeriod can be set to a nanosecond so even if the head
		// given by the trusted peer expires by the time `subjectiveHead` is
		// called again, it will still call Head on the `eclipsedExchange`
		// which will return the same head as before.
		sync.WithTrustingPeriod(time.Nanosecond),
	)
	if err != nil {
		panic(err)
	}

	// give bad headers to the malicious (eclipsing) peer in order
	// to attempt to get syncer to follow a fork
	err = ee.appendToEclipsedExchange(ctx, maliciousSuite.GenDummyHeaders(99)...)
	if err != nil {
		panic(err)
	}
	// give good headers to the trusted peer in order to return a good subjective head
	// to the syncer upon its start
	err = ee.appendToTrusted(ctx, suite.GenDummyHeaders(99)...)
	if err != nil {
		panic(err)
	}

	_, err = ee.trustedPeer.GetByHeight(ctx, 100)
	if err != nil {
		panic(err)
	}
	_, err = ee.eclipsedExchange.GetByHeight(ctx, 100)
	if err != nil {
		panic(err)
	}

	logging.Logger("sync")

	err = syncer.Start(ctx)
	if err != nil {
		panic(err)
	}

	// this sleep is necessary to allow the syncer to trigger a job
	// as calling SyncWait prematurely may falsely return without error
	// as the syncer has not yet registered a sync job.
	//time.Sleep(time.Millisecond * 100)
	time.Sleep(time.Millisecond * 500)
	syncer.SyncWait(ctx) //nolint:errcheck
}

// eclipsedExchange is an exchange that can serve a good Head to the syncer
// but attempts to "eclipse" the syncer by serving it a fork as it requests
// headers between its storeHead --> subjectiveHead.
type eclipsedExchange struct {
	trustedPeer      header.Store[*headertest.DummyHeader]
	eclipsedExchange header.Store[*headertest.DummyHeader]
}

func newEclipsedExchange(
	ctx context.Context,
	t *testing.T,
	head, maliciousHead *headertest.DummyHeader,
) *eclipsedExchange {
	return &eclipsedExchange{
		trustedPeer:      store.NewTestStore(ctx, t, head),
		eclipsedExchange: store.NewTestStore(ctx, t, maliciousHead),
	}

}

// Head returns a good header from the trusted peer.
func (e *eclipsedExchange) Head(ctx context.Context, h ...header.HeadOption[*headertest.DummyHeader]) (*headertest.DummyHeader, error) {
	return e.trustedPeer.Head(ctx, h...)
}

// GetVerifiedRange returns a fork from the eclipsed exchange in an attempt to
// eclipse the syncer.
func (e *eclipsedExchange) GetVerifiedRange(ctx context.Context, from *headertest.DummyHeader, amount uint64) ([]*headertest.DummyHeader, error) {
	return e.eclipsedExchange.GetVerifiedRange(ctx, from, amount)
}

// TODO document
func (e *eclipsedExchange) appendToTrusted(ctx context.Context, h ...*headertest.DummyHeader) error {
	return e.trustedPeer.Append(ctx, h...)
}

func (e *eclipsedExchange) appendToEclipsedExchange(ctx context.Context, h ...*headertest.DummyHeader) error {
	return e.eclipsedExchange.Append(ctx, h...)
}

func (e *eclipsedExchange) Get(ctx context.Context, hash header.Hash) (*headertest.DummyHeader, error) {
	panic("implement me")
}

func (e *eclipsedExchange) GetByHeight(ctx context.Context, u uint64) (*headertest.DummyHeader, error) {
	panic("implement me")
}

func (e *eclipsedExchange) GetRangeByHeight(ctx context.Context, from, amount uint64) ([]*headertest.DummyHeader, error) {
	panic("implement me")
}

func (e *eclipsedExchange) Init(ctx context.Context, h *headertest.DummyHeader) error {
	panic("implement me")
}

func (e *eclipsedExchange) Height() uint64 {
	panic("implement me")
}

func (e *eclipsedExchange) Has(ctx context.Context, hash header.Hash) (bool, error) {
	panic("implement me")
}

func (e *eclipsedExchange) HasAt(ctx context.Context, u uint64) bool {
	panic("implement me")
}

func (e *eclipsedExchange) Append(ctx context.Context, h ...*headertest.DummyHeader) error {
	panic("implement me")
}
