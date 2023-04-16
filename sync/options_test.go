package sync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOptionsWithParams(t *testing.T) {
	params := DefaultParameters()

	bt := time.Second
	opt := WithParams(Parameters{
		blockTime: bt,
	})

	opt(&params)
	assert.Equal(t, bt, params.blockTime)
}
