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
		RequestTimeout: timeout,
	})

	opt(&params)
	assert.Equal(t, timeout, params.RequestTimeout)
}

func TestOptionsServerWithParams(t *testing.T) {
	params := DefaultServerParameters()

	timeout := time.Second
	opt := WithParams(ServerParameters{
		RequestTimeout: timeout,
	})

	opt(&params)
	assert.Equal(t, timeout, params.RequestTimeout)
}
