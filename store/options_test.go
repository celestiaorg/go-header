package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOptionsWithParams(t *testing.T) {
	params := DefaultParameters()

	size := 1
	opt := WithParams(Parameters{
		StoreCacheSize: 1,
	})

	opt(&params)
	assert.Equal(t, size, params.StoreCacheSize)
}
