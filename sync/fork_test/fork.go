package main

import (
	"context"
	"testing"
	"time"

	logging "github.com/ipfs/go-log/v2"

	"github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
	"github.com/celestiaorg/go-header/local"
	"github.com/celestiaorg/go-header/store"
	"github.com/celestiaorg/go-header/sync"
)

// This program is for test purposes only. See TestForkFollowingPrevention
// for further context.
//
// This program runs an instance of a syncer against a modified p2p Exchange
// that is designed to serve it a fork instead of the canonical chain.
func main() {
	t := &testing.T{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	// set up syncer with a malicious peer as its remote peer
	ee := newEclipsedExchange(ctx, t, head)

	localStore := store.NewTestStore(ctx, t, head)
	syncer, err := sync.NewSyncer[*headertest.DummyHeader](
		local.NewExchange[*headertest.DummyHeader](ee),
		localStore,
		headertest.NewDummySubscriber(),
		// TrustingPeriod can be set to a nanosecond so even if the head
		// given by the trusted peer expires by the time `subjectiveHead` is
		// called again, it will still call Head on the `eclipsedPeer`
		// which will return the same head as before.
		sync.WithTrustingPeriod(time.Nanosecond),
	)
	if err != nil {
		panic(err)
	}

	// generate a good canonical chain
	canonical := suite.GenDummyHeaders(99)

	// give good headers to the trusted peer in order to return a good subjective head
	// to the syncer upon its start
	err = ee.appendToTrusted(ctx, canonical...)
	if err != nil {
		panic(err)
	}

	// generate a fork starting at block height 50 of the canonical chain
	fork := canonical[:50]
	maliciousSuite := headertest.NewTestSuiteWithHead(t, fork[len(fork)-1])
	// generate 50 blocks on the fork
	fork = append(fork, maliciousSuite.GenDummyHeaders(50)...)
	// give bad headers to the malicious (eclipsing) peer in order
	// to attempt to get syncer to follow a fork
	err = ee.appendToEclipsedExchange(ctx, fork...)
	if err != nil {
		panic(err)
	}

	_, err = ee.trustedPeer.GetByHeight(ctx, 100)
	if err != nil {
		panic(err)
	}
	_, err = ee.eclipsedPeer.GetByHeight(ctx, 100)
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
	// trusted peer that serves a good Head to the syncer
	trustedPeer header.Store[*headertest.DummyHeader]
	// bad peers who attempt to eclipse the syncer and get it to follow a fork
	eclipsedPeer header.Store[*headertest.DummyHeader]
}

func newEclipsedExchange(
	ctx context.Context,
	t *testing.T,
	head *headertest.DummyHeader,
) *eclipsedExchange {
	return &eclipsedExchange{
		trustedPeer:  store.NewTestStore(ctx, t, head),
		eclipsedPeer: store.NewTestStore(ctx, t, head),
	}
}

// Head returns a good header from the trusted peer.
func (e *eclipsedExchange) Head(ctx context.Context, h ...header.HeadOption[*headertest.DummyHeader]) (*headertest.DummyHeader, error) {
	return e.trustedPeer.Head(ctx, h...)
}

// GetVerifiedRange returns a fork from the eclipsed exchange in an attempt to
// eclipse the syncer.
func (e *eclipsedExchange) GetVerifiedRange(ctx context.Context, from *headertest.DummyHeader, amount uint64) ([]*headertest.DummyHeader, error) {
	return e.eclipsedPeer.GetVerifiedRange(ctx, from, amount)
}

func (e *eclipsedExchange) appendToTrusted(ctx context.Context, h ...*headertest.DummyHeader) error {
	return e.trustedPeer.Append(ctx, h...)
}

func (e *eclipsedExchange) appendToEclipsedExchange(ctx context.Context, h ...*headertest.DummyHeader) error {
	return e.eclipsedPeer.Append(ctx, h...)
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
