package store

import (
	"strconv"

	"github.com/ipfs/go-datastore"

	"github.com/celestiaorg/go-header"
)

var (
	headKey = datastore.NewKey("head")
	tailKey = datastore.NewKey("tail")
)

func heightKey(h uint64) datastore.Key {
	return datastore.NewKey(strconv.FormatUint(h, 10))
}

func headerKey[H header.Header[H]](h H) datastore.Key {
	return datastore.NewKey(h.Hash().String())
}
