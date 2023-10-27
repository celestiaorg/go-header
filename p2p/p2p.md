# P2P

The P2P package mainly contains two services:

1) [Subscriber](#subscriber)
2) [Exchange](#exchange)

## Subscriber

Subscriber is a service that manages the gossip of headers among the nodes in the P2P network by using [libp2p][libp2p] and its [pubsub][pubsub] modules. The pubsub topic is used for gossip (`/<networkID>/header-sub/v0.0.1`) and is configurable based on the `networkID` parameter used to initialize the subscriber service.

The Subscriber encompasses the behavior necessary to subscribe/unsubscribe from new Header events from the network. The Subscriber interface consists of:

|Method|Input|Output|Description|
|--|--|--|--|
| Subscribe | | `Subscription[H], error` | Subscribe creates long-living Subscription for validated Headers. Multiple Subscriptions can be created. |
| SetVerifier | `func(context.Context, H) error` | error | SetVerifier registers verification func for all Subscriptions. Registered func screens incoming headers before they are forwarded to Subscriptions. Only one func can be set.|

The `Subscribe()` method allows listening to any new headers that are published to the P2P network. The `SetVerifier()` method allows for setting a custom verifier that will be executed upon receiving any new headers from the P2P network. This is a very useful customization for the consumers of go-header library to pass any custom logic as part of the pubsub. While multiple simultaneous subscriptions are possible via `Subscribe()` interface, only a single verifier can be set using the `SetVerifier` interface method.

## Exchange

An exchange is a combination of:

* [Exchange](#exchange-client): a client for requesting headers from the P2P network (outbound)
* [ExchangeServer](#exchange-server): a P2P server for handling inbound header requests

### Exchange Client

Exchange defines a client for requesting headers from the P2P network. An exchange client is initialized using self [host.Host][host], a list of peers in the form of slice [peer.IDSlice][peer], and a [connection gater][gater] for blocking and allowing nodes. Optional parameters like `ChainID` and `NetworkID` can also be passed. The exchange client also maintains a list of trusted peers via a peer tracker. The peer tracker will continue to discover peers until:

* the total peers (connected and disconnected) does not exceed [`maxPeerTrackerSize`][maxPeerTrackerSize] or
* connected peer count does not exceed disconnected peer count.

A set of client parameters (shown in the table below) can be passed while initializing an exchange client.

|Parameter|Type|Description|Default|
|--|--|--|--|
| MaxHeadersPerRangeRequest | uint64 | MaxHeadersPerRangeRequest defines the max amount of headers that can be requested per 1 request. | 64 |
| RangeRequestTimeout | time.Duration | RangeRequestTimeout defines a timeout after which the session will try to re-request headers from another peer. | 8s |
| chainID | string | chainID is an identifier of the chain. | "" |

#### Peer Tracker

The three main functionalities of the peer tracker are:

* bootstrap
* track
* garbage collection (gc)

When the exchange client is started, it bootstraps the peer tracker using the set of trusted peers used to initialize the exchange client.

The new peers are tracked by subscribing to `event.EvtPeerConnectednessChanged{}`.

The peer tracker also runs garbage collector (gc) that removes the disconnected peers (determined as disconnected for more than [maxAwaitingTime][maxAwaitingTime] or connected peers whose scores are less than or equal to [defaultScore][defaultScore]) from the tracked peers list once every [gcCycle][gcCycle].

The peer tracker also provides a block peer functionality which is used to block peers that send invalid network headers. Invalid header is a header that fails when `Verify` method of the header interface is invoked.

#### Getter Interface

The exchange client implements the following `Getter` interface which contains the behavior necessary for a component to retrieve headers that have been processed during header sync. The `Getter` interface consists of:

|Method|Input|Output|Description|
|--|--|--|--|
| Head | `context.Context, ...HeadOption[H]` | `H, error` | Head returns the latest known chain header. Note that "chain head" is subjective to the component reporting it. |
| Get | `context.Context, Hash` | `H, error` | Get returns the Header corresponding to the given hash. |
| GetByHeight | `context.Context, uint64` | `H, error` | GetByHeight returns the Header corresponding to the given block height. |
| GetRangeByHeight | `ctx context.Context, from H, to uint64` | `[]H, error` | GetRangeByHeight requests the header range from the provided Header and verifies that the returned headers are adjacent to each other. Expected to return the range [from.Height()+1:to).|

`Head()` method requests the latest header from trusted or tracked peers. The `Head()` call also allows passing an optional `TrustedHead`, which allows the caller to specify a trusted head against which the untrusted head is verified. By default, `Head()` requests only trusted peers and if `TrustedHead` is provided untrusted tracked peers are also requested, limited to [maxUntrustedHeadRequests][maxUntrustedHeadRequests]. The `Head()` requests utilize 90% of the set deadline (in the form of context deadline) for requests and the remaining for determining the best head from gathered responses. Upon receiving headers from peers (either trusted or tracked), the best head is determined as the head:

* with max height among the received
* which is received from at least [minHeadResponses][minHeadResponses] peers
* when neither or both conditions meet, the head with highest height is used

Apart from requesting the latest header, any arbitrary header(s) can be requested (with 3 retries) using height (`GetByHeight`), hash (`Get`), or range (`GetRangeByHeight`) from trusted peers as defined in the request proto message which encapsulates all three kinds of header requests:

```
message HeaderRequest {
  oneof data {
    uint64 origin = 1;
    bytes hash = 2;
  }
  uint64 amount = 3;
}
```

Note that, `GetRangeByHeight` as it ensures that the returned headers are correct against the begin header of the range.

### Exchange Server

ExchangeServer represents the server-side component of the exchange (a P2P server) for responding to inbound header requests. The exchange server needs to be initialized using self [host.Host][host] and a [store][store]. Optional `ServerParameters` as shown below, can be set during the server initialization.

|Parameter|Type|Description|Default|
|--|--|--|--|
| WriteDeadline | time.Duration | WriteDeadline sets the timeout for sending messages to the stream | 8s |
| ReadDeadline | time.Duration | ReadDeadline sets the timeout for reading messages from the stream | 60s |
| RangeRequestTimeout | time.Duration | RangeRequestTimeout defines a timeout after which the session will try to re-request headers from another peer | 10s |
| networkID | string | networkID is a network that will be used to create a protocol.ID | "" |

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

The request handler utilizes its local [store][store] for serving the header requests and only up to [MaxRangeRequestSize][MaxRangeRequestSize] of 512 headers can be requested while requesting headers by range. If the requested range is not available, the range is reset to whatever is available.

### Session

Session aims to divide a header range requests into several smaller requests among different peers. This service is used by the exchange client for making the `GetRangeByHeight`.

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
[store]: https://github.com/celestiaorg/go-header/blob/main/store/store.md
[maxPeerTrackerSize]: https://github.com/celestiaorg/go-header/blob/main/p2p/peer_tracker.go#L19
[maxAwaitingTime]: https://github.com/celestiaorg/go-header/blob/main/p2p/peer_tracker.go#L25
[defaultScore]: https://github.com/celestiaorg/go-header/blob/main/p2p/peer_tracker.go#L17
[gcCycle]: https://github.com/celestiaorg/go-header/blob/main/p2p/peer_tracker.go#L27
[maxUntrustedHeadRequests]: https://github.com/celestiaorg/go-header/blob/main/p2p/exchange.go#L32
[minHeadResponses]: https://github.com/celestiaorg/go-header/blob/main/p2p/exchange.go#L28
[MaxRangeRequestSize]: https://github.com/celestiaorg/go-header/blob/main/interface.go#L13
