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

var _ Peerstore = (*peerStore)(nil)

type peerStore struct {
	ds datastore.Datastore
}

// NewPeerStore creates a new peerstore backed by the given datastore which is
// wrapped with `perssted_peerstore` prefix.
func NewPeerStore(ds datastore.Datastore) Peerstore {
	return &peerStore{ds: namespace.Wrap(ds, storePrefix)}
}

// Load loads the peerlist from the datastore.
func (s *peerStore) Load(ctx context.Context) ([]peer.AddrInfo, error) {
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
func (s *peerStore) Put(ctx context.Context, peerlist []peer.AddrInfo) error {
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
