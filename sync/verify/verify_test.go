package verify

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/celestiaorg/go-header/headertest"
)

func TestVerify(t *testing.T) {
	suite := headertest.NewTestSuite(t)
	trusted := suite.GenDummyHeaders(1)[0]

	next := func() *headertest.DummyHeader {
		next := *suite.NextHeader()
		return &next
	}

	tests := []struct {
		prepare func() *headertest.DummyHeader
		err     bool
		soft    bool
	}{
		{
			prepare: func() *headertest.DummyHeader {
				return nil
			},
			err: true,
		},
		{
			prepare: func() *headertest.DummyHeader {
				untrusted := next()
				untrusted.VerifyFailure = true
				return untrusted
			},
			err: true,
		},
		{
			prepare: func() *headertest.DummyHeader {
				untrusted := next()
				untrusted.VerifyFailure = true
				return untrusted
			},
			err:  true,
			soft: true, // soft because non-adjacent
		},
		{
			prepare: func() *headertest.DummyHeader {
				return next()
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			err := Verify(trusted, test.prepare(), 0)
			if test.err {
				var verErr *VerifyError
				assert.ErrorAs(t, err, &verErr)
				assert.NotNil(t, errors.Unwrap(verErr))
				assert.Equal(t, test.soft, verErr.SoftFailure)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_verify(t *testing.T) {
	suite := headertest.NewTestSuite(t)
	trusted := suite.GenDummyHeaders(1)[0]

	next := func() *headertest.DummyHeader {
		next := *suite.NextHeader() // copy is required
		return &next
	}

	tests := []struct {
		prepare func() *headertest.DummyHeader
		err     error
	}{
		{
			prepare: func() *headertest.DummyHeader {
				return next()
			},
		},
		{
			prepare: func() *headertest.DummyHeader {
				return nil
			},
			err: errZero,
		},
		{
			prepare: func() *headertest.DummyHeader {
				untrusted := next()
				untrusted.Raw.ChainID = "gtmb"
				return untrusted
			},
			err: errWrongChain,
		},
		{
			prepare: func() *headertest.DummyHeader {
				untrusted := next()
				untrusted.Raw.Time = untrusted.Raw.Time.Truncate(time.Minute * 10)
				return untrusted
			},
			err: errUnordered,
		},
		{
			prepare: func() *headertest.DummyHeader {
				untrusted := next()
				untrusted.Raw.Time = untrusted.Raw.Time.Add(time.Minute)
				return untrusted
			},
			err: errFromFuture,
		},
		{
			prepare: func() *headertest.DummyHeader {
				untrusted := next()
				untrusted.Raw.Height = trusted.Height()
				return untrusted
			},
			err: errKnown,
		},
		{
			prepare: func() *headertest.DummyHeader {
				untrusted := next()
				untrusted.Raw.Height += 100000
				return untrusted
			},
			err: errHeightFromFuture,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			err := verify(trusted, test.prepare(), 0)
			assert.ErrorIs(t, err, test.err)
			t.Log(err)
		})
	}
}
