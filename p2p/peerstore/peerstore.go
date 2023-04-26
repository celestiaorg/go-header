package peerstore

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"

	"encoding/json"
	"fmt"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"

	logging "github.com/ipfs/go-log/v2"
)

type Peerstore interface {
	Put(context.Context, []peer.AddrInfo) error
	Load(context.Context) ([]peer.AddrInfo, error)
}

var (
	storePrefix = datastore.NewKey("p2p")
	peersKey    = datastore.NewKey("good_peers")

	log = logging.Logger("module/header/peerstore")
)

var _ Peerstore = (*peerStore)(nil)

type peerStore struct {
	ds datastore.Datastore
}

// newPeerStore wraps the given datastore.Datastore with the `p2p` prefix.
func NewPeerStore(ds datastore.Datastore) Peerstore {
	return &peerStore{ds: namespace.Wrap(ds, storePrefix)}
}

func (s *peerStore) Load(ctx context.Context) ([]peer.AddrInfo, error) {
	log.Info("Loading peerlist")
	bs, err := s.ds.Get(ctx, peersKey)
	if err != nil {
		return make([]peer.AddrInfo, 0), err
	}

	peerlist := make([]peer.AddrInfo, 0)
	err = json.Unmarshal(bs, &peerlist)
	if err != nil {
		return make([]peer.AddrInfo, 0), fmt.Errorf("error unmarshalling peerlist: %w", err)
	}

	log.Info("Loaded peerlist", peerlist)
	return peerlist, err
}

func (s *peerStore) Put(ctx context.Context, peerlist []peer.AddrInfo) error {
	log.Info("Storing peerlist", peerlist)

	bs, err := json.Marshal(peerlist)
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	if err = s.ds.Put(ctx, peersKey, bs); err != nil {
		return err
	}

	return nil
}
