package headertest

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/celestiaorg/go-header"
)

func TestVerify(t *testing.T) {
	suite := NewTestSuite(t)
	trusted := suite.GenDummyHeaders(1)[0]
	var zero *DummyHeader

	next := func() *DummyHeader {
		next := *suite.NextHeader()
		return &next
	}

	tests := []struct {
		trusted *DummyHeader
		prepare func() *DummyHeader
		err     error
		soft    bool
	}{
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.VerifyFailure = true
				return untrusted
			},
			err: ErrDummyVerify,
		},
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.VerifyFailure = true
				return untrusted
			},
			err:  ErrDummyVerify,
			soft: true, // soft because non-adjacent
		},
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				return next()
			},
		},
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				return nil
			},
			err: header.ErrZeroHeader,
		},
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.Chainid = "gtmb"
				return untrusted
			},
			err: header.ErrWrongChainID,
		},
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.Timestamp = untrusted.Timestamp.Truncate(time.Minute * 10)
				return untrusted
			},
			err: header.ErrUnorderedTime,
		},
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.Timestamp = untrusted.Timestamp.Add(time.Minute)
				return untrusted
			},
			err: header.ErrFromFuture,
		},
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.HeightI = trusted.Height()
				return untrusted
			},
			err: header.ErrKnownHeader,
		},
		{
			trusted: trusted,
			prepare: func() *DummyHeader {
				return zero
			},
			err: header.ErrZeroHeader,
		},
		{
			trusted: zero,
			prepare: func() *DummyHeader {
				return next()
			},
			err: header.ErrZeroHeader,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			err := header.Verify(test.trusted, test.prepare())
			if test.err != nil {
				var verErr *header.VerifyError
				assert.ErrorAs(t, err, &verErr)
				assert.ErrorIs(t, errors.Unwrap(verErr), test.err)
				assert.Equal(t, test.soft, verErr.SoftFailure)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVerifyRange(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*DummySuite) (*DummyHeader, []*DummyHeader)
		err      error
		verified int // number of headers that should be verified before error
	}{
		{
			name: "successful verification of all headers",
			setup: func(suite *DummySuite) (*DummyHeader, []*DummyHeader) {
				trusted := suite.GenDummyHeaders(1)[0]
				untrstd := suite.GenDummyHeaders(5)
				return trusted, untrstd
			},
			verified: 5,
		},
		{
			name: "empty untrusted headers range",
			setup: func(suite *DummySuite) (*DummyHeader, []*DummyHeader) {
				trusted := suite.GenDummyHeaders(1)[0]
				return trusted, []*DummyHeader{}
			},
			err: header.ErrEmptyRange,
		},
		{
			name: "zero trusted header",
			setup: func(suite *DummySuite) (*DummyHeader, []*DummyHeader) {
				var zero *DummyHeader
				return zero, []*DummyHeader{zero}
			},
			err: header.ErrZeroHeader,
		},
		{
			name: "zero untrusted header",
			setup: func(suite *DummySuite) (*DummyHeader, []*DummyHeader) {
				var zero *DummyHeader
				return zero, []*DummyHeader{zero}
			},
			err: header.ErrZeroHeader,
		},
		{
			name: "verification fails in middle of range",
			setup: func(suite *DummySuite) (*DummyHeader, []*DummyHeader) {
				trusted := suite.GenDummyHeaders(1)[0]
				headers := suite.GenDummyHeaders(5)
				headers[2].VerifyFailure = true // make 3rd header fail verification
				return trusted, headers
			},
			err:      ErrDummyVerify,
			verified: 2,
		},
		{
			name: "non-adjacent header range ",
			setup: func(suite *DummySuite) (*DummyHeader, []*DummyHeader) {
				trusted := suite.GenDummyHeaders(1)[0]
				_ = suite.GenDummyHeaders(1) // generate a header to ensure the range can be non-adjacent
				headers := suite.GenDummyHeaders(3)
				headers = append(headers[0:1], headers[2:]...)
				return trusted, headers
			},
			err:      header.ErrNonAdjacentRange,
			verified: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suite := NewTestSuite(t)
			trusted, untrstd := tt.setup(suite)
			verified, err := header.VerifyRange(trusted, untrstd)

			if tt.err != nil {
				var verErr *header.VerifyError
				assert.ErrorAs(t, err, &verErr)
				assert.ErrorIs(t, errors.Unwrap(verErr), tt.err)
				assert.Len(t, verified, tt.verified, "unexpected number of verified headers before error")
			} else {
				assert.NoError(t, err)
				assert.Len(t, verified, len(untrstd), "all headers should be verified")
				if len(untrstd) > 0 {
					assert.Equal(t, untrstd[len(untrstd)-1], verified[len(verified)-1], "last verified header should match last input header")
				}
			}
		})
	}
}
