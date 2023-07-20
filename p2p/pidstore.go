package p2p

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerIDStore is a utility for persisting peer AddrInfo of good peers to a
// datastore.
type PeerIDStore interface {
	// Put stores the given peers' AddrInfo.
	Put(ctx context.Context, peers []peer.AddrInfo) error
	// Load loads the peers' AddrInfo from the store.
	Load(ctx context.Context) ([]peer.AddrInfo, error)
}
