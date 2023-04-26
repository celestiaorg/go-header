package peerstore

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
)

type Peerstore interface {
	Put(context.Context, []peer.AddrInfo) error
	Load(context.Context) ([]peer.AddrInfo, error)
}
