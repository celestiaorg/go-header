package local

import (
	"context"

	"github.com/celestiaorg/go-header"
)

// Exchange is a simple Exchange that reads Headers from Store without any networking.
type Exchange[H header.Header[H]] struct {
	store header.Store[H]
}

// NewExchange creates a new local Exchange.
func NewExchange[H header.Header[H]](store header.Store[H]) header.Exchange[H] {
	return &Exchange[H]{
		store: store,
	}
}

func (l *Exchange[H]) Start(context.Context) error {
	return nil
}

func (l *Exchange[H]) Stop(context.Context) error {
	return nil
}

func (l *Exchange[H]) Head(ctx context.Context, opts ...header.HeadOption[H]) (H, error) {
	params := &header.HeadParams[H]{}
	for _, opt := range opts {
		opt(params)
	}

	head, err := l.store.Head(ctx)
	if err != nil {
		return head, err
	}

	if !params.TrustedHead.IsZero() {
		if err := header.Verify(params.TrustedHead, head); err != nil {
			return head, err
		}
	}

	return head, nil
}

func (l *Exchange[H]) GetByHeight(ctx context.Context, height uint64) (H, error) {
	return l.store.GetByHeight(ctx, height)
}

func (l *Exchange[H]) GetRangeByHeight(ctx context.Context, from H, to uint64) ([]H, error) {
	headers, err := l.store.GetRangeByHeight(ctx, from, to)
	if err != nil {
		return nil, err
	}

	return header.VerifyRange(from, headers)
}

func (l *Exchange[H]) Get(ctx context.Context, hash header.Hash) (H, error) {
	return l.store.Get(ctx, hash)
}
