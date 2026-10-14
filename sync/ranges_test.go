package sync

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-header/headertest"
)

func TestAddParallel(t *testing.T) {
	var pending ranges[*headertest.DummyHeader]

	n := 500
	suite := headertest.NewTestSuite(t)
	headers := suite.GenDummyHeaders(n)

	wg := &sync.WaitGroup{}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			pending.Add(headers[i])
			wg.Done()
		}(i)
	}
	wg.Wait()

	last := uint64(0)
	for _, r := range pending.ranges {
		assert.Greater(t, r.start, last)
		last = r.start
	}
}

func TestRangeGet(t *testing.T) {
	n := 300
	suite := headertest.NewTestSuite(t)
	headers := suite.GenDummyHeaders(n)

	r := newRange(headers[200])
	r.Append(headers[201:]...)

	truncated := r.Get(100)
	assert.Len(t, truncated, 0)
}

func TestRangesPrune(t *testing.T) {
	suite := headertest.NewTestSuite(t)
	headers := suite.GenDummyHeaders(7)

	var pending ranges[*headertest.DummyHeader]
	for _, h := range headers[:3] {
		pending.Add(h)
	}
	pending.Add(headers[5])
	pending.Add(headers[6])

	pending.Prune(headers[1].Height())
	r, ok := pending.First()
	require.True(t, ok)
	assert.Equal(t, headers[2:3], r.Get(headers[2].Height()))

	pending.Prune(headers[5].Height())
	r, ok = pending.First()
	require.True(t, ok)
	assert.Equal(t, []*headertest.DummyHeader{headers[6]}, r.Get(headers[6].Height()))
	assert.Equal(t, headers[6], pending.Head())

	pending.Prune(headers[6].Height())
	_, ok = pending.First()
	assert.False(t, ok)
	assert.True(t, pending.Head().IsZero())
}

func TestRangeGetBeyondHead(t *testing.T) {
	suite := headertest.NewTestSuite(t)
	headers := suite.GenDummyHeaders(3)

	r := newRange(headers[0])
	r.Append(headers[1:]...)

	assert.Len(t, r.Get(headers[2].Height()+1), 3)
	r.Remove(headers[2].Height() + 1)
	assert.Empty(t, r.Get(headers[2].Height()+1))
}
