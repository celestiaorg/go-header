package peerstore

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
)

type mockPeerstore struct {
	container []peer.AddrInfo
}

func NewMockPeerstore() Peerstore {
	return &mockPeerstore{}
}

func (m *mockPeerstore) Put(ctx context.Context, peers []peer.AddrInfo) error {
	m.container = peers
	return nil
}

func (m *mockPeerstore) Load(ctx context.Context) ([]peer.AddrInfo, error) {
	return m.container, nil
}
