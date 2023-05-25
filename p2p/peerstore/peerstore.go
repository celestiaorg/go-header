package peerstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/peer"
)

var (
	storePrefix = datastore.NewKey("persisted_peerstore")
	peersKey    = datastore.NewKey("peers")

	log = logging.Logger("peerstore")
)

// Peerstore is used to store/load peers to/from disk.
type Peerstore struct {
	ds datastore.Datastore
}

// NewPeerstore creates a new peerstore backed by the given datastore which is
// wrapped with `persisted_peerstore` prefix.
func NewPeerstore(ds datastore.Datastore) *Peerstore {
	return &Peerstore{ds: namespace.Wrap(ds, storePrefix)}
}

// Load loads the peerlist from datastore.
func (s *Peerstore) Load(ctx context.Context) ([]peer.AddrInfo, error) {
	log.Debug("Loading peerlist")
	bs, err := s.ds.Get(ctx, peersKey)
	if err != nil {
		return make([]peer.AddrInfo, 0), fmt.Errorf("peerstore: loading peers from datastore: %w", err)
	}

	peerlist := make([]peer.AddrInfo, 0)
	err = json.Unmarshal(bs, &peerlist)
	if err != nil {
		return make([]peer.AddrInfo, 0), fmt.Errorf("peerstore: unmarshalling peerlist: %w", err)
	}

	log.Info("Loaded peerlist", peerlist)
	return peerlist, err
}

// Put stores the peerlist in the datastore.
func (s *Peerstore) Put(ctx context.Context, peerlist []peer.AddrInfo) error {
	log.Debug("Storing peerlist", peerlist)

	bs, err := json.Marshal(peerlist)
	if err != nil {
		return fmt.Errorf("peerstore: marshal peerlist: %w", err)
	}

	if err = s.ds.Put(ctx, peersKey, bs); err != nil {
		return fmt.Errorf("peerstore: error writing to datastore: %w", err)
	}

	log.Info("Stored peerlist successfully.")
	return nil
}
