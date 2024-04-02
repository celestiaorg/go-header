package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarm "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/headertest"
)

func Test_PrepareRequests(t *testing.T) {
	// from : 1, amount: 10, headersPerPeer: 5
	// result -> {{1, 2, 3, 4, 5}, {6, 7, 8, 9, 10}}
	requests := prepareRequests(1, 10, 5)
	require.Len(t, requests, 2)
	require.Equal(t, requests[0].GetOrigin(), uint64(1))
	require.Equal(t, requests[1].GetOrigin(), uint64(6))
}

// Test_Validate ensures that headers range is adjacent and valid.
func Test_Validate(t *testing.T) {
	suite := headertest.NewTestSuite(t)
	head := suite.Head()
	peerId := peer.ID("test")
	pT := &peerTracker{trackedPeers: make(map[peer.ID]struct{})}
	pT.trackedPeers[peerId] = struct{}{}
	pT.host = blankhost.NewBlankHost(swarm.GenSwarm(t))
	ses, err := newSession(
		context.Background(),
		nil,
		pT,
		"", time.Second, nil,
		withValidation(head),
	)

	require.NoError(t, err)
	headers := suite.GenDummyHeaders(5)
	err = ses.verify(headers)
	assert.NoError(t, err)
}

// Test_ValidateFails ensures that non-adjacent range will return an error.
func Test_ValidateFails(t *testing.T) {
	suite := headertest.NewTestSuite(t)
	head := suite.Head()

	peerId := peer.ID("test")
	pT := &peerTracker{trackedPeers: make(map[peer.ID]struct{})}
	pT.trackedPeers[peerId] = struct{}{}
	pT.host = blankhost.NewBlankHost(swarm.GenSwarm(t))
	ses, err := newSession(
		context.Background(),
		blankhost.NewBlankHost(swarm.GenSwarm(t)),
		pT,
		"", time.Second, nil,
		withValidation(head),
	)
	require.NoError(t, err)
	headers := suite.GenDummyHeaders(5)
	// break adjacency
	headers[2] = headers[4]
	err = ses.verify(headers)
	assert.Error(t, err)
}
