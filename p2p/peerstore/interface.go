package peerstore

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Peerstore is an interface for storing and loading libp2p peers' information.
type Peerstore interface {
	Put(context.Context, []peer.AddrInfo) error
	Load(context.Context) ([]peer.AddrInfo, error)
}
