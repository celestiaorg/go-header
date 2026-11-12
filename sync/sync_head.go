package sync

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/codes"

	"github.com/celestiaorg/go-header"
)

// syncHead is a wrapper around the header.Head that provides single-flight access to the Head.
type syncHead[H header.Header[H]] struct {
	headMu sync.Mutex
	headCh chan struct{}
	head   header.Head[H]

	resHead H
	resErr  error
}

func (sh *syncHead[H]) Head(ctx context.Context, opts ...header.HeadOption[H]) (head H, err error) {
	sh.headMu.Lock()
	doneCh := sh.headCh
	acquired := doneCh == nil
	if acquired {
		doneCh = make(chan struct{})
		sh.headCh = doneCh
	}
	sh.headMu.Unlock()

	ctx, span := tracerSync.Start(ctx, "request-head")
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	if acquired {
		span.AddEvent("requesting-head-from-source")
		head, err = sh.head.Head(ctx, opts...)

		sh.headMu.Lock()
		sh.resHead, sh.resErr = head, err
		sh.headCh = nil
		sh.headMu.Unlock()

		close(doneCh)
		return head, err
	}

	span.AddEvent("waiting-for-ongoing-head-request")
	select {
	case <-doneCh:
		sh.headMu.Lock()
		defer sh.headMu.Unlock()
		head, err = sh.resHead, sh.resErr
		return head, err
	case <-ctx.Done():
		var zero H
		return zero, ctx.Err()
	}
}
