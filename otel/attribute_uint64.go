package otelattr

import (
	"math"

	"go.opentelemetry.io/otel/attribute"
)

// Uint64 returns an attribute.KeyValue for the given uint64.
// TODO(@Wondertan): Otel doesn't support uin64 for unknown reason while it should.
//
//	This helper localizes the unsafe conversion from uint64 to int64 in a single place.
func Uint64(key string, value uint64) attribute.KeyValue {
	if value > math.MaxInt64 {
		return attribute.String(key, "height overflows int64")
	}
	return attribute.Int64(key, int64(value))
}
