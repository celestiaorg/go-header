package peerstore

import "github.com/libp2p/go-libp2p/core/peer"

type Peerstore interface {
	Put([]peer.AddrInfo) error
	Load() ([]peer.AddrInfo, error)
}
