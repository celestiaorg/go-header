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

	next := func() *DummyHeader {
		next := *suite.NextHeader()
		return &next
	}

	tests := []struct {
		prepare func() *DummyHeader
		err     error
		soft    bool
	}{
		{
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.VerifyFailure = true
				return untrusted
			},
			err: ErrDummyVerify,
		},
		{
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.VerifyFailure = true
				return untrusted
			},
			err:  ErrDummyVerify,
			soft: true, // soft because non-adjacent
		},
		{
			prepare: func() *DummyHeader {
				return next()
			},
		},
		{
			prepare: func() *DummyHeader {
				return nil
			},
			err: header.ErrZeroHeader,
		},
		{
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.Chainid = "gtmb"
				return untrusted
			},
			err: header.ErrWrongChainID,
		},
		{
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.Timestamp = untrusted.Timestamp.Truncate(time.Minute * 10)
				return untrusted
			},
			err: header.ErrUnorderedTime,
		},
		{
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.Timestamp = untrusted.Timestamp.Add(time.Minute)
				return untrusted
			},
			err: header.ErrFromFuture,
		},
		{
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.HeightI = trusted.Height()
				return untrusted
			},
			err:  header.ErrKnownHeader,
			soft: true,
		},
		{
			prepare: func() *DummyHeader {
				untrusted := next()
				untrusted.HeightI += 100000
				return untrusted
			},
			err: header.ErrHeightFromFuture,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			err := header.Verify(trusted, test.prepare(), 0)
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
