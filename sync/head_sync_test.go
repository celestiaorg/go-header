package sync

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	libhead "github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/headertest"
)

func Test_syncHead(t *testing.T) {
	counter := &headCounter{}
	sh := &syncHead[*headertest.DummyHeader]{head: counter}

	callsN := 1000
	wg := sync.WaitGroup{}
	wg.Add(callsN)
	for i := 0; i < callsN; i++ {
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(rand.IntN(1000)) * time.Microsecond)
			_, _ = sh.Head(context.Background())
		}()
	}

	wg.Wait()

	assert.Less(t, int(counter.cntr.Load()), callsN)
}

type headCounter struct {
	cntr atomic.Int64
}

func (h *headCounter) Head(
	ctx context.Context,
	h2 ...libhead.HeadOption[*headertest.DummyHeader],
) (*headertest.DummyHeader, error) {
	time.Sleep(time.Millisecond * 1)
	h.cntr.Add(1)
	return &headertest.DummyHeader{}, nil
}
