# P2P

The p2p package mainly contains two services:

1) Subscriber
2) Exchange

## Subscriber

Subscriber is a service that manages gossip of headers among the nodes in the p2p network by using [libp2p][libp2p] and its [pubsub][pubsub] modules. The pubsub topic used for gossip (`/<networkID>/header-sub/v0.0.1`) is configurable based on `networkID` parameter used to initialize the subscriber service.

The Subscriber implements the following interface:

```
// Subscriber encompasses the behavior necessary to
// subscribe/unsubscribe from new Header events from the
// network.
type Subscriber[H Header[H]] interface {
 // Subscribe creates long-living Subscription for validated Headers.
 // Multiple Subscriptions can be created.
 Subscribe() (Subscription[H], error)
 // SetVerifier registers verification func for all Subscriptions.
 // Registered func screens incoming headers
 // before they are forwarded to Subscriptions.
 // Only one func can be set.
 SetVerifier(func(context.Context, H) error) error
}
```

The `Subscribe()` method allows listening to any new headers that are published to the p2p network. The `SetVerifier()` method allows for setting a custom verifier that will be executed upon receiving any new headers from the p2p network. This is a very useful customization for the consumers of go-header library to pass any custom logic as part of the pubsub.

## Exchange

An exchange is a combination of:

* Exchange: a client for requesting headers from the p2p network (outbound)
* ExchangeServer: a p2p server for handling inbound header requests

### Exchange Client

Exchange defines a client for requesting headers from the p2p network. An exchange client is initialized using self [host.Host][host], a list of peers in the form of slice [peer.IDSlice][peer], and a [connection gater][gater] for blocking and allowing nodes. Optional parameters like `ChainID` and `NetworkID` can also be passed. The exchange client also maintains a list of trusted peers via a peer tracker.

#### Peer Tracker

The three main functionalities of the peer tracker are:
* bootstrap
* track
* gc

When the exchange client is started, it bootstraps the peer tracker using the set of trusted peers used to initialize the exchange client.

The new peers are tracked by subscribing to `event.EvtPeerConnectednessChanged{}`.

The peer tracker also runs garbage collector (gc) that removes the disconnected peers (determined as disconnected for more than `maxAwaitingTime` or connected peers whose scores are less than or equal to `defaultScore`) from the tracked peers list once every `gcPeriod`.

The peer tracker also provides a block peer functionality which is used to block peers that send invalid network headers. Invalid header is a header that fails when `Verify` method of the header interface is invoked.

The exchange client implements the following interface:

```
// Getter contains the behavior necessary for a component to retrieve
// headers that have been processed during header sync.
type Getter[H Header[H]] interface {
 Head[H]

 // Get returns the Header corresponding to the given hash.
 Get(context.Context, Hash) (H, error)

 // GetByHeight returns the Header corresponding to the given block height.
 GetByHeight(context.Context, uint64) (H, error)

 // GetRangeByHeight returns the given range of Headers.
 GetRangeByHeight(ctx context.Context, from, amount uint64) ([]H, error)

 // GetVerifiedRange requests the header range from the provided Header and
 // verifies that the returned headers are adjacent to each other.
 GetVerifiedRange(ctx context.Context, from H, amount uint64) ([]H, error)
}

// Head contains the behavior necessary for a component to retrieve
// the chain head. Note that "chain head" is subjective to the component
// reporting it.
type Head[H Header[H]] interface {
 // Head returns the latest known header.
 Head(context.Context, ...HeadOption[H]) (H, error)
}
```

`Head()` method requests the latest header from trusted peers. The `Head()` requests utilizes 90% of the set deadline (in the form of context deadline) for requests and remaining for determining the best head from gathered responses. The `Head()` call also allows passing an optional `TrustedHead` which allows the caller to specify a trusted header against which the untrusted headers received from a list of tracked peers (limited to `maxUntrustedHeadRequests` of 4) can be verified against, in the absence of trusted peers. Upon receiving headers from peers (either trusted or tracked), the best head is determined as the head:

* with max height among the received
* which is received from at least `minHeadResponses` of 2 peers
* when neither or both conditions meet, the head with highest height is used

Apart from requesting the latest header, any arbitrary header(s) can be requested (with 3 retries) using height (`GetByHeight`), hash (`Get`), range (`GetRangeByHeight` and `GetVerifiedRange`) from trusted peers as defined in the request proto message:

```
message HeaderRequest {
  oneof data {
    uint64 origin = 1;
    bytes hash = 2;
  }
  uint64 amount = 3;
}
```

The `GetVerifiedRange` differs from `GetRangeByHeight` as it ensures that the returned headers are correct against another header (passed as parameter to the call).

### Exchange Server

ExchangeServer represents the server-side component (p2p server) for responding to inbound header requests. The exchange server needs to be initialized using self [host.Host][host] and a [store](../specs/src/specs/store.md). Optional `ServerParameters` as shown below, can be set during the server initialization.

```
// ServerParameters is the set of parameters that must be configured for the exchange.
type ServerParameters struct {
 // WriteDeadline sets the timeout for sending messages to the stream
 WriteDeadline time.Duration
 // ReadDeadline sets the timeout for reading messages from the stream
 ReadDeadline time.Duration
 // RangeRequestTimeout defines a timeout after which the session will try to re-request headers
 // from another peer.
 RangeRequestTimeout time.Duration
 // networkID is a network that will be used to create a protocol.ID
 // Is empty by default
 networkID string
}
```

The default values for `ServerParameters` are as described below.

```
// DefaultServerParameters returns the default params to configure the store.
func DefaultServerParameters() ServerParameters {
 return ServerParameters{
  WriteDeadline:       time.Second * 8,
  ReadDeadline:        time.Minute,
  RangeRequestTimeout: time.Second * 10,
 }
}
```

During the server start, a request handler for the `protocolID` (`/networkID/header-ex/v0.0.3`) which defined using the `networkID` configurable parameter is setup to serve the inbound header requests.

The request handler returns a response which contains bytes of the requested header(s) and a status code as shown below.

```
message HeaderResponse {
  bytes body = 1;
  StatusCode statusCode = 2;
}
```

The `OK` status code for success, `NOT_FOUND` for requested headers not found, and `INVALID` for error (default).

```
enum StatusCode {
  INVALID = 0;
  OK = 1;
  NOT_FOUND = 2;
}
```

The request handler utilizes its local [store](../specs/src/specs/store.md) for serving the header requests and only up to `MaxRangeRequestSize` of 512 headers can be requested while requesting headers by range. If the requested range is not available, the range is reset to whatever is available.

### Session

Session aims to divide a header range requests into several smaller requests among different peers. This service is used by the exchange client for making the `GetRangeByHeight` and `GetVerifiedRange` calls.

```
// ClientParameters is the set of parameters that must be configured for the exchange.
type ClientParameters struct {
 // MaxHeadersPerRangeRequest defines the max amount of headers that can be requested per 1 request.
 MaxHeadersPerRangeRequest uint64
 // RangeRequestTimeout defines a timeout after which the session will try to re-request headers
 // from another peer.
 RangeRequestTimeout time.Duration
 // networkID is a network that will be used to create a protocol.ID
 networkID string
 // chainID is an identifier of the chain.
 chainID string

 pidstore PeerIDStore
}
```

The default values for `ClientParameters` are as described below.

```
// DefaultClientParameters returns the default params to configure the store.
func DefaultClientParameters() ClientParameters {
 return ClientParameters{
  MaxHeadersPerRangeRequest: 64,
  RangeRequestTimeout:       time.Second * 8,
 }
}
```

## Metrics

Currently only following metrics are collected:
* P2P header exchange response size
* Duration of the get headers request in seconds
* Total synced headers


# References

[1] [libp2p][libp2p]

[2] [pubsub][pubsub]

[3] [host.Host][host]

[4] [peer.IDSlice][peer]

[5] [connection gater][gater]

[libp2p]: https://github.com/libp2p/go-libp2p
[pubsub]: https://github.com/libp2p/go-libp2p-pubsub
[host]: https://github.com/libp2p/go-libp2p/core/host
[peer]: https://github.com/libp2p/go-libp2p/core/peer
[gater]: https://github.com/libp2p/go-libp2p/p2p/net/conngater
