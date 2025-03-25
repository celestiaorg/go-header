package store

import (
	"github.com/celestiaorg/go-header/headertest"
	"github.com/stretchr/testify/assert"
	"slices"
	"testing"
)

func TestBatches_GetByHeight(t *testing.T) {
	headers := headertest.NewTestSuite(t).GenDummyHeaders(8)
	// reverse the order to be descending
	slices.SortFunc(headers, func(a, b *headertest.DummyHeader) int {
		return int(b.Height() - a.Height())
	})

	setup := [][]*headertest.DummyHeader{
		headers[:2], // Batch 8-7
		headers[4:], // Batch 4-1
	}
	expected := headers[5]

	bs := newEmptyBatches[*headertest.DummyHeader]()
	for _, headers := range setup {
		b := newBatch[*headertest.DummyHeader](len(headers))
		b.Append(headers...)
		bs.batches = append(bs.batches, b)
	}

	actual, err := bs.GetByHeight(expected.Height())
	assert.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestBatches_Append(t *testing.T) {
	headers := headertest.NewTestSuite(t).GenDummyHeaders(8) // Pre-generate headers
	// reverse the order to be descending
	slices.SortFunc(headers, func(a, b *headertest.DummyHeader) int {
		return int(b.Height() - a.Height())
	})

	tests := []struct {
		name              string
		setup             func() [][]*headertest.DummyHeader
		appendAndExpected func() ([]*headertest.DummyHeader, [][]*headertest.DummyHeader)
	}{
		{
			name: "Append fills gap between two batches",
			setup: func() [][]*headertest.DummyHeader {
				return [][]*headertest.DummyHeader{
					headers[:2], // Batch 8-7
					headers[4:], // Batch 4-1
				}
			},
			appendAndExpected: func() ([]*headertest.DummyHeader, [][]*headertest.DummyHeader) {
				toAppend := headers[2:4] // Headers 6,5
				expected := [][]*headertest.DummyHeader{
					headers, // Merged 8-1
				}
				return toAppend, expected
			},
		},
		{
			name: "Append adjacent to a batch and merges",
			setup: func() [][]*headertest.DummyHeader {
				return [][]*headertest.DummyHeader{
					headers[:2], // Headers 8-7
				}
			},
			appendAndExpected: func() ([]*headertest.DummyHeader, [][]*headertest.DummyHeader) {
				toAppend := headers[2:4] // Headers 6,5
				expected := [][]*headertest.DummyHeader{
					headers[:4], // Merged 8-5
				}
				return toAppend, expected
			},
		},
		{
			name: "Append creates a new batch in between existing batches",
			setup: func() [][]*headertest.DummyHeader {
				return [][]*headertest.DummyHeader{
					headers[:2], // Batch 8-7
					headers[6:], // Batch 2-1
				}
			},
			appendAndExpected: func() ([]*headertest.DummyHeader, [][]*headertest.DummyHeader) {
				toAppend := headers[3:5] // Headers 4,3
				expected := [][]*headertest.DummyHeader{
					headers[:2],  // Batch 8-7
					headers[3:5], // Batch 4-3
					headers[6:],  // Batch 2-1
				}

				return toAppend, expected

			},
		},
		{
			name: "Append creates a new batch at the end",
			setup: func() [][]*headertest.DummyHeader {
				return [][]*headertest.DummyHeader{
					headers[:2], // Batch 8-7
				}
			},
			appendAndExpected: func() ([]*headertest.DummyHeader, [][]*headertest.DummyHeader) {
				toAppend := headers[4:] // Headers 5-1
				expected := [][]*headertest.DummyHeader{
					headers[:2], // Batch 8-7
					headers[4:], // Batch 5-1
				}

				return toAppend, expected
			},
		},
		{
			name: "Append overrides existing headers",
			setup: func() [][]*headertest.DummyHeader {
				return [][]*headertest.DummyHeader{
					headers, // Entire batch 8-1
				}
			},
			appendAndExpected: func() ([]*headertest.DummyHeader, [][]*headertest.DummyHeader) {
				differentHeaders := headertest.NewTestSuite(t).GenDummyHeaders(8)
				// reverse the order to be descending
				slices.SortFunc(differentHeaders, func(a, b *headertest.DummyHeader) int {
					return int(b.Height() - a.Height())
				})

				expectedHeaders := make([]*headertest.DummyHeader, len(headers))
				copy(expectedHeaders, headers)
				expectedHeaders[6] = differentHeaders[6]
				expectedHeaders[7] = differentHeaders[7]

				toAppend := expectedHeaders[6:] // Headers 1,2

				return toAppend, [][]*headertest.DummyHeader{
					expectedHeaders, // Batch 1-8 with 1,2 replaced
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := test.setup()
			toAppend, expected := test.appendAndExpected()

			bs := newEmptyBatches[*headertest.DummyHeader]()
			for _, headers := range setup {
				b := newBatch[*headertest.DummyHeader](len(headers))
				b.Append(slices.Clone(headers)...)
				bs.batches = append(bs.batches, b)
			}
			bs.Append(slices.Clone(toAppend)...)

			// Verify expected batch structure
			var actualBatches [][]*headertest.DummyHeader
			for _, b := range bs.batches {
				actualBatches = append(actualBatches, b.GetAll())
			}

			assert.EqualValues(t, expected, actualBatches)
		})
	}
}
