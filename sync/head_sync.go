package sync

import (
	"context"
	"sync"

	"github.com/celestiaorg/go-header"
)

// syncHead is a wrapper around the header.Head that provides single-flight access to the Head.
// TODO: Should we extract this as public component?
type syncHead[H header.Header[H]] struct {
	headMu sync.Mutex
	headCh chan struct{}
	head   header.Head[H]

	resHead H
	resErr  error
}

func (sh *syncHead[H]) Head(ctx context.Context, opts ...header.HeadOption[H]) (H, error) {
	sh.headMu.Lock()
	doneCh := sh.headCh
	acquired := doneCh == nil
	if acquired {
		doneCh = make(chan struct{})
		sh.headCh = doneCh
	}
	sh.headMu.Unlock()

	if acquired {
		head, err := sh.head.Head(ctx, opts...)

		sh.headMu.Lock()
		sh.resHead, sh.resErr = head, err
		sh.headCh = nil
		sh.headMu.Unlock()

		close(doneCh)
		return head, err
	}

	select {
	case <-doneCh:
		sh.headMu.Lock()
		defer sh.headMu.Unlock()
		return sh.resHead, sh.resErr
	case <-ctx.Done():
		var zero H
		return zero, ctx.Err()
	}
}
