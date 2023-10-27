# Store

Store implements the Store interface (shown below) for headers over .

Store encompasses the behavior necessary to store and retrieve headers from a node's local storage ([datastore][go-datastore]). The Store interface includes checker and append methods on top of [Getter](../p2p/p2p.md#getter-interface) methods as shown in the table below.

|Method|Input|Output|Description|
|--|--|--|--|
| Init | `context.Context, H` | `error` | Init initializes Store with the given head, meaning it is initialized with the genesis header. |
| Height | | `uint64` | Height reports current height of the chain head. |
| Has | `context.Context, Hash` | `bool, error` | Has checks whether Header is already stored. |
| HasAt | `context.Context, uint64` | `bool` | HasAt checks whether Header at the given height is already stored. |
| Append | `context.Context, ...H` | `error` | Append stores and verifies the given Header(s). It requires them to be adjacent and in ascending order, as it applies them contiguously on top of the current head height. It returns the amount of successfully applied headers, so caller can understand what given header was invalid, if any. |

A new store is created by passing a [datastore][go-datastore] instance and an optional head. If the head is not passed while creating a new store, `Init` method can be used to later initialize the store with head. The store must have a head before start. The head is considered trusted header and generally it is the genesis header. A custom store prefix can be passed during the store initialization. Further, a set of parameters can be passed during the store initialization to configure the store as described below.

|Parameter|Type|Description|Default|
|--|--|--|--|
| StoreCacheSize | int | StoreCacheSize defines the maximum amount of entries in the Header Store cache. | 4096 |
| IndexCacheSize | int | IndexCacheSize defines the maximum amount of entries in the Height to Hash index cache. | 16384 |
| WriteBatchSize | int | WriteBatchSize defines the size of the batched header write. Headers are written in batches not to thrash the underlying Datastore with writes. | 2048 |
| storePrefix | datastore.Key | storePrefix defines the prefix used to wrap the store | nil |

The store runs a flush loop during the start which performs writing task to the underlying datastore in a separate routine. This way writes are controlled and manageable from one place allowing:

* `Append`s not blocked on long disk IO writes and underlying DB compactions
* Helps with batching header writes

`Append` appends a list of headers to the store head. It requires that all headers to be appended are adjacent to each other (sequential). Also, append invokes adjacency header verification by calling the `Verify` header interface method to ensure that only verified headers are appended. As described above, append does not directly writes to the underlying datastore, which is taken care by the flush loop.

`Has` method checks if a header with a given hash exists in the store. The check is performed on a cache ([lru.ARCCache][lru.ARCCache]) first, followed by the pending queue which contains headers that are not flushed (written to disk), and finally the datastore. The `Get` method works similar to `Has`, where the retrieval first checks cache, followed by the pending queue, and finally the datastore (disk access).

# References

[1] [datastore][go-datastore]

[2] [lru.ARCCache][lru.ARCCache]

[go-datastore]: https://github.com/ipfs/go-datastore
[lru.ARCCache]: https://github.com/hashicorp/golang-lru
