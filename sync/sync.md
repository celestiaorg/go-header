# Sync

Syncer implements efficient synchronization for headers.

There are two main processes running in Syncer:

* Main syncing loop(`syncLoop`)
  * Performs syncing from the latest stored header up to the latest known Subjective Head
    * Subjective head: the latest known local valid header and a sync target.
    * Network head: the latest valid network-wide header. Becomes subjective once applied locally.
  * Syncs by requesting missing headers from Exchange or
  * By accessing cache of pending headers
* Receives every new Network Head from PubSub gossip subnetwork (`incomingNetworkHead`)
  * Validates against the latest known Subjective Head, is so
  * Sets as the new Subjective Head, which
  * If there is a gap between the previous and the new Subjective Head
  * Triggers s.syncLoop and saves the Subjective Head in the pending so s.syncLoop can access it

For creating a new instance of the Syncer following components are needed:

* A getter, e.g., [Exchange][exchange]
* A [Store][store]
* A [Subscriber][subscriber]
* Additional options such as block time. More options as described below.

Options for configuring the syncer:

|Parameter|Type|Description|Default|
|--|--|--|--|
| TrustingPeriod | time.Duration | TrustingPeriod is period through which we can trust a header's validators set. Should be significantly less than the unbonding period (e.g. unbonding period = 3 weeks, trusting period = 2 weeks). More specifically, trusting period + time needed to check headers + time needed to report and punish misbehavior should be less than the unbonding period. | 336 hours (tendermint's default trusting period) |
| blockTime | time.Duration | blockTime provides a reference point for the Syncer to determine whether its subjective head is outdated. Keeping it private to disable serialization for it. | 0 (reason: syncer will constantly request networking head.) |
| recencyThreshold | time.Duration | recencyThreshold describes the time period for which a header is considered "recent". | blockTime + 5 seconds |

When the syncer is started:

* `incomingNetworkHead` is set as validator for any incoming header over the subscriber
* Retrieve the latest head to kick off syncing. Note that, syncer cannot be started without head.
* `syncLoop` is started, which listens to sync trigger

## Fetching the Head to Start Syncing

Known subjective head is considered network head if it is recent (`now - timestamp <= blocktime`). Otherwise, a head is requested from a trusted peer and set as the new subjective head, assuming that trusted peer is always fully synced.

* If the event of network not able to be retrieved and subjective head is not recent, as fallback, the subjective head is used as head.
* The network head retrieved is subjected to validation (via `incomingNetworkHead`) before setting as the new subjective head.

## Verify

The header interface defines a `Verify` method which gets invoked when any new header is received via `incomingNetworkHead`.

|Method|Input|Output|Description|
|--|--|--|--|
| Verify | H | error | Verify validates given untrusted Header against trusted Header. |

## syncLoop

When a new network head is received which gets validated and set as subjective head, it triggers the `syncLoop` which tries to sync headers from old subjective head till new network head (the sync target) by utilizing the `getter`(the `Exchange` client).

# References

[1] [Exchange][exchange]

[2] [Subscriber][subscriber]

[exchange]: https://github.com/celestiaorg/go-header/blob/main/p2p/exchange.go
[subscriber]: https://github.com/celestiaorg/go-header/blob/main/p2p/subscriber.go
[store]: https://github.com/celestiaorg/go-header/blob/main/store/store.md
