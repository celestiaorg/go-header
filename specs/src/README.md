# Welcome

Welcome to the go-header Specifications.

go-header is a library for syncing blockchain data such as block headers over the p2p network. It contains services for requesting and receiving headers from the p2p network, serving header requests from other nodes in the p2p network, storing headers, and syncing historical headers in case of fallbacks.

|Component|Description|
|---|---|
|[p2p.Subscriber](specs/p2p.md)|listens for new headers from the p2p network|
|[p2p.ExchangeServer](specs/p2p.md)|serve header requests from other nodes in the p2p network|
|[p2p.Exchange](specs/p2p.md)|client that requests headers from other nodes in the p2p network|
|[store.Store](specs/store.md)|storing headers and making them available for access by other services such as exchange and syncer|
|[sync.Syncer](specs/sync.md)|syncing of historical and new headers from the p2p network|

The go-header library makes it easy to consume by other projects by defining a clear interface (as described below). An example consumer is defined in [headertest/dummy_header.go][dummy header]

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
