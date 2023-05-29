package persisted_peerstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

var (
	storePrefix = datastore.NewKey("persisted_peerstore")
	peersKey    = datastore.NewKey("peers")

	log = logging.Logger("persisted_peerstore")
)

// PersistedPeerstore is used to store/load peers to/from disk.
type PersistedPeerstore struct {
	ds       datastore.Datastore
	addrBook peerstore.AddrBook
}

// NewPersistedPeerstore creates a new peerstore backed by the given datastore which is
// wrapped with `persisted_peerstore` prefix. It also takes an AddrBook so that it can
// look up a peer by its ID to get AddrInfo.
func NewPersistedPeerstore(ds datastore.Datastore, addrBook peerstore.AddrBook) *PersistedPeerstore {
	return &PersistedPeerstore{
		ds:       namespace.Wrap(ds, storePrefix),
		addrBook: addrBook,
	}
}

// Load loads the peers from datastore and returns their AddrInfo.
func (pp *PersistedPeerstore) Load(ctx context.Context) ([]peer.AddrInfo, error) {
	log.Debug("Loading peers")

	bin, err := pp.ds.Get(ctx, peersKey)
	if err != nil {
		return make([]peer.AddrInfo, 0), fmt.Errorf("peerstore: loading peers from datastore: %w", err)
	}

	var peers []peer.ID
	err = json.Unmarshal(bin, &peers)
	if err != nil {
		return make([]peer.AddrInfo, 0), fmt.Errorf("peerstore: unmarshalling peer IDs: %w", err)
	}

	log.Info("Loaded peers from disk", "amount", len(peers))

	// lookup and collect their AddrInfo
	addrInfos := make([]peer.AddrInfo, len(peers))
	for i, peerID := range peers {
		addrs := pp.addrBook.Addrs(peerID)
		addrInfos[i] = peer.AddrInfo{
			ID:    peerID,
			Addrs: addrs,
		}
	}

	return addrInfos, nil
}

// Put persists the given peer IDs to the datastore.
func (pp *PersistedPeerstore) Put(ctx context.Context, peers []peer.ID) error {
	log.Debug("Persisting peers to disk", "amount", len(peers))

	bin, err := json.Marshal(peers)
	if err != nil {
		return fmt.Errorf("peerstore: marshal peerlist: %w", err)
	}

	if err = pp.ds.Put(ctx, peersKey, bin); err != nil {
		return fmt.Errorf("peerstore: error writing to datastore: %w", err)
	}

	log.Info("Persisted peers successfully", "amount", len(peers))
	return nil
}
