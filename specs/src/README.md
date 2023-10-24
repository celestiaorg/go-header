# Welcome

Welcome to the go-header Specifications.

go-header is a library for syncing blockchain data, such as block headers, over the P2P network in a trust-minimized way. It contains services for requesting and receiving headers from the P2P network, serving header requests from other nodes in the P2P network, storing headers, and syncing historical headers in case of fallbacks.

|Component|Description|
|---|---|
|[p2p.Subscriber][p2p]|listens for new headers from the P2P network|
|[p2p.ExchangeServer][p2p]|serve header requests from other nodes in the P2P network|
|[p2p.Exchange][p2p]|client that requests headers from other nodes in the P2P network|
|[store.Store][store]|storing headers and making them available for access by other services such as exchange and syncer|
|[sync.Syncer][sync]|syncing of historical and new headers from the P2P network|

The go-header library makes it easy to be used by other projects by defining a clear interface (as described below). An example usage is defined in [headertest/dummy_header.go][dummy header]

```
type Header[H any] interface {
 // New creates new instance of a header.
 New() H
 
 // IsZero reports whether Header is a zero value of it's concrete type.
 IsZero() bool
 
 // ChainID returns identifier of the chain.
 ChainID() string
 
 // Hash returns hash of a header.
 Hash() Hash
 
 // Height returns the height of a header.
 Height() uint64
 
 // LastHeader returns the hash of last header before this header (aka. previous header hash).
 LastHeader() Hash
 
 // Time returns time when header was created.
 Time() time.Time
 
 // Verify validates given untrusted Header against trusted Header.
 Verify(H) error

 // Validate performs stateless validation to check for missed/incorrect fields.
 Validate() error

 encoding.BinaryMarshaler
 encoding.BinaryUnmarshaler
}
```

# References

[1] [Dummy Header][dummy header]

[dummy header]: https://github.com/celestiaorg/go-header/blob/main/headertest/dummy_header.go
[p2p]: https://github.com/celestiaorg/go-header/blob/main/p2p/p2p.md
[store]: https://github.com/celestiaorg/go-header/blob/main/store/store.md
[sync]: https://github.com/celestiaorg/go-header/blob/main/sync/sync.md
