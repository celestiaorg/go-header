package p2p

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOptionsClientWithParams(t *testing.T) {
	params := DefaultClientParameters()

	timeout := time.Second
	opt := WithParams(ClientParameters{
		RangeRequestTimeout: timeout,
	})

	opt(&params)
	assert.Equal(t, timeout, params.RangeRequestTimeout)
}

func TestOptionsServerWithParams(t *testing.T) {
	params := DefaultServerParameters()

	timeout := time.Second
	opt := WithParams(ServerParameters{
		RangeRequestTimeout: timeout,
	})

	opt(&params)
	assert.Equal(t, timeout, params.RangeRequestTimeout)
}
