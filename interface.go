package header

import (
	"context"
	"errors"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

const (
	// MaxRangeRequestSize defines the max amount of headers that can be handled/requested at once.
	MaxRangeRequestSize uint64 = 64
)

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

// Subscription listens for new Headers.
type Subscription[H Header[H]] interface {
	// NextHeader returns the newest verified and valid Header
	// in the network.
	NextHeader(ctx context.Context) (H, error)
	// Cancel cancels the subscription.
	Cancel()
}

// Broadcaster broadcasts a Header to the network.
type Broadcaster[H Header[H]] interface {
	Broadcast(ctx context.Context, header H, opts ...pubsub.PubOpt) error
}

// Exchange encompasses the behavior necessary to request Headers
// from the network.
type Exchange[H Header[H]] interface {
	Getter[H]
}

var (
	// ErrNotFound is returned when there is no requested header.
	ErrNotFound = errors.New("header: not found")

	// ErrEmptyStore is returned when Store is empty (does not contain any known header).
	ErrEmptyStore = errors.New("header/store: store is empty")

	// ErrHeadersLimitExceeded is returned when ExchangeServer receives header request for more
	// than maxRequestSize headers.
	ErrHeadersLimitExceeded = errors.New("header/p2p: header limit per 1 request exceeded")

	// ErrRangeMixUp is returned when `from` >= `to`.
	ErrRangeMixUp = errors.New("header/p2p: `from` must be less than `to`")
)

// Store encompasses the behavior necessary to store and retrieve Headers
// from a node's local storage.
type Store[H Header[H]] interface {
	// Getter encompasses all getter methods for headers.
	Getter[H]

	// Tail returns the oldest known header.
	Tail(context.Context) (H, error)

	// Height reports current height of the chain head.
	Height() uint64

	// Has checks whether Header is already stored.
	Has(context.Context, Hash) (bool, error)

	// HasAt checks whether Header at the given height is already stored.
	HasAt(context.Context, uint64) bool

	// Append stores and verifies the given Header(s).
	Append(context.Context, ...H) error

	// GetRange returns the range [from:to).
	GetRange(context.Context, uint64, uint64) ([]H, error)

	// DeleteTo deletes the range [Tail():to).
	DeleteTo(ctx context.Context, to uint64) error

	// OnDelete registers given handler to be called whenever headers are removed from the Store.
	OnDelete(func(context.Context, []H) error)
}

// Getter contains the behavior necessary for a component to retrieve
// headers that have been processed during header sync.
type Getter[H Header[H]] interface {
	Head[H]

	// Get returns the Header corresponding to the given hash.
	Get(context.Context, Hash) (H, error)

	// GetByHeight returns the Header corresponding to the given block height.
	GetByHeight(context.Context, uint64) (H, error)

	// GetRangeByHeight requests the header range from the provided Header and
	// verifies that the returned headers are adjacent to each other.
	// Expected to return the range [from.Height()+1:to).
	GetRangeByHeight(ctx context.Context, from H, to uint64) ([]H, error)
}

// Head contains the behavior necessary for a component to retrieve
// the chain head. Note that "chain head" is subjective to the component
// reporting it.
type Head[H Header[H]] interface {
	// Head returns the latest known header.
	Head(context.Context, ...HeadOption[H]) (H, error)
}
