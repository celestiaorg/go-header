package sync

import (
	"context"
	"testing"

	"github.com/celestiaorg/go-header/headertest"
	"github.com/stretchr/testify/assert"
)

func TestSyncStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ts := headertest.NewTestSuite(t)
	s := headertest.NewStore[*headertest.DummyHeader](t, ts, 100)
	ss := syncStore[*headertest.DummyHeader]{Store: s}

	h, err := ss.Head(ctx)
	assert.NoError(t, err)
	assert.Equal(t, ts.Head(), h)

	err = ss.Append(ctx, ts.NextHeader())
	assert.NoError(t, err)

	h, err = ss.Head(ctx)
	assert.NoError(t, err)
	assert.Equal(t, ts.Head(), h)
}
