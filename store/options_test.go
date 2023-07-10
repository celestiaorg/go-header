package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestDefaultParamsMarshalRountrip(t *testing.T) {
	paramsIn := DefaultParameters()
	paramsOut := Parameters{}

	bytes, err := json.Marshal(paramsIn)
	require.NoError(t, err)

	err = json.Unmarshal(bytes, &paramsOut)
	require.NoError(t, err)

	assert.Equal(t, paramsIn, paramsOut)
}
