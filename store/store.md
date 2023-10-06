# Store

Store implements the Store interface (shown below) for headers over [datastore][go-datastore].

```
// Store encompasses the behavior necessary to store and retrieve Headers
// from a node's local storage.
type Store[H Header[H]] interface {
	// Getter encompasses all getter methods for headers.
	Getter[H]

	// Init initializes Store with the given head, meaning it is initialized with the genesis header.
	Init(context.Context, H) error

	// Height reports current height of the chain head.
	Height() uint64

	// Has checks whether Header is already stored.
	Has(context.Context, Hash) (bool, error)

	// HasAt checks whether Header at the given height is already stored.
	HasAt(context.Context, uint64) bool

	// Append stores and verifies the given Header(s).
	// It requires them to be adjacent and in ascending order,
	// as it applies them contiguously on top of the current head height.
	// It returns the amount of successfully applied headers,
	// so caller can understand what given header was invalid, if any.
	Append(context.Context, ...H) error
}

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

A new store is created by passing a [datastore][go-datastore] instance and an optional head. If the head is not passed while creating a new store, `Init` method can be used to later initialize the store with head. The store must have a head before start. The head is considered trusted header and generally it is the genesis header. A custom store prefix can be passed during the store initialization. Further, a set of parameters can be passed during the store initialization to configure the store as described below.

```
// Parameters is the set of parameters that must be configured for the store.
type Parameters struct {
	// StoreCacheSize defines the maximum amount of entries in the Header Store cache.
	StoreCacheSize int

	// IndexCacheSize defines the maximum amount of entries in the Height to Hash index cache.
	IndexCacheSize int

	// WriteBatchSize defines the size of the batched header write.
	// Headers are written in batches not to thrash the underlying Datastore with writes.
	WriteBatchSize int

	// storePrefix defines the prefix used to wrap the store
	// OPTIONAL
	storePrefix datastore.Key
}
```
The default values for store `Parameters` are as described below.

```
// DefaultParameters returns the default params to configure the store.
func DefaultParameters() Parameters {
	return Parameters{
		StoreCacheSize: 4096,
		IndexCacheSize: 16384,
		WriteBatchSize: 2048,
	}
}
```

The store runs a flush loop during the start which performs writing task to the underlying datastore in a separate routine. This way writes are controlled and manageable from one place allowing:
* `Append`s not blocked on long disk IO writes and underlying DB compactions
* Helps with batching header writes

`Append` appends a list of headers to the store head. It requires that all headers to be appended are adjacent to each other (sequential). Also, append invokes adjacency header verification by calling the `Verify` header interface method to ensure that only verified headers are appended. As described above, append does not directly writes to the underlying datastore, which is taken care by the flush loop.

`Has` method checks if a header with a given hash exists in the store. The check is performed on a cache ([lru.ARCCache][lru.ARCCache]) first, followed by the pending queue which contains headers that are not flushed (written to disk), and finally the datastore. The `Get` method works similar to `Has`, where the retrieval first checks cache, followed by the pending queue, and finally the datastore (disk access).

# References

[go-datastore]: https://github.com/ipfs/go-datastore
[lru.ARCCache]: https://github.com/hashicorp/golang-lru