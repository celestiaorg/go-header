package headertest

import (
	"testing"
	"time"
)

// DummySuite provides everything you need to test chain of DummyHeaders.
// If not, please don't hesitate to extend it for your case.
type DummySuite struct {
	t *testing.T

	head *DummyHeader
}

// NewTestSuite setups a new test suite.
func NewTestSuite(t *testing.T) *DummySuite {
	return &DummySuite{
		t: t,
	}
}

func (s *DummySuite) Head() *DummyHeader {
	if s.head == nil {
		s.head = s.genesis()
	}
	return s.head
}

func (s *DummySuite) GenDummyHeaders(num int) []*DummyHeader {
	headers := make([]*DummyHeader, num)
	for i := range headers {
		headers[i] = s.NextHeader()
	}
	return headers
}

func (s *DummySuite) NextHeader() *DummyHeader {
	if s.head == nil {
		s.head = s.genesis()
		return s.head
	}

	dh := RandDummyHeader(s.t)
	dh.Raw.Height = s.head.Height() + 1
	dh.Raw.PreviousHash = s.head.Hash()
	_ = dh.rehash()
	s.head = dh
	return s.head
}

func (s *DummySuite) genesis() *DummyHeader {
	return &DummyHeader{
		hash: nil,
		Raw: Raw{
			PreviousHash: nil,
			Height:       1,
			Time:         time.Now().Add(-10 * time.Second).UTC(),
		},
	}
}
