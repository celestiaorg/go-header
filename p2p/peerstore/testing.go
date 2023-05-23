package peerstore

import (
	"crypto/rand"
	"crypto/rsa"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func GenerateRandomPeerlist(length int) ([]peer.AddrInfo, error) {
	peerlist := make([]peer.AddrInfo, 0)
	for i := 0; i < length; i++ {
		key, err := rsa.GenerateKey(rand.Reader, 2096)
		if err != nil {
			return nil, err
		}

		_, pubkey, err := crypto.KeyPairFromStdKey(key)
		if err != nil {
			return nil, err
		}

		peerID, err := peer.IDFromPublicKey(pubkey)
		if err != nil {
			return nil, err
		}

		peerlist = append(peerlist, peer.AddrInfo{
			ID: peerID,
			Addrs: []ma.Multiaddr{
				ma.StringCast("/ip4/0.0.0.0/tcp/2121"),
			},
		})
	}

	return peerlist, nil
}
