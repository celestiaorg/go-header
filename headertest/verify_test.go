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
	suite := NewTestSuite(t)
	trusted := suite.GenDummyHeaders(1)[0]
	var zero *DummyHeader

	tests := []struct {
		name     string
		trusted  *DummyHeader
		untrstd  []*DummyHeader
		wantErr  bool
		errType  error
		verified int // number of headers that should be verified before error
	}{
		{
			name:     "successful verification of all headers",
			trusted:  trusted,
			untrstd:  suite.GenDummyHeaders(5),
			wantErr:  false,
			verified: 5,
		},
		{
			name:    "empty untrusted headers",
			trusted: trusted,
			untrstd: []*DummyHeader{},
			wantErr: false,
		},
		{
			name:    "verification fails in middle of range",
			trusted: trusted,
			untrstd: modifyHeaders(
				suite.GenDummyHeaders(5),
				2,
				true,
				false,
			), // make 3rd header fail verification
			wantErr:  true,
			errType:  ErrDummyVerify,
			verified: 2,
		},
		{
			name:    "verification fails with soft failure",
			trusted: trusted,
			untrstd: modifyHeaders(
				suite.GenDummyHeaders(5),
				2,
				true,
				true,
			), // make 3rd header fail with soft failure
			wantErr:  true,
			errType:  ErrDummyVerify,
			verified: 2,
		},
		{
			name:     "zero trusted header",
			trusted:  zero,                 // use nil pointer
			untrstd:  []*DummyHeader{zero}, // use nil pointer for untrusted header too
			wantErr:  true,
			errType:  header.ErrZeroHeader,
			verified: 0, // no headers should be verified when trusted header is zero
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verified, err := header.VerifyRange(tt.trusted, tt.untrstd)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errType != nil {
					var verErr *header.VerifyError
					assert.ErrorAs(t, err, &verErr)
					assert.ErrorIs(t, errors.Unwrap(verErr), tt.errType)
				}
				assert.Len(
					t,
					verified,
					tt.verified,
					"unexpected number of verified headers before error",
				)
			} else {
				assert.NoError(t, err)
				assert.Len(t, verified, len(tt.untrstd), "all headers should be verified")
				if len(tt.untrstd) > 0 {
					assert.Equal(t,
						tt.untrstd[len(tt.untrstd)-1], verified[len(verified)-1],
						"last verified header should match last input header",
					)
				}
			}
		})
	}
}

// modifyHeaders is a helper function that modifies a header at the specified index to fail verification
func modifyHeaders(
	headers []*DummyHeader,
	index int,
	verifyFailure, softFailure bool,
) []*DummyHeader {
	if index < len(headers) {
		headers[index].VerifyFailure = verifyFailure
		headers[index].SoftFailure = softFailure
	}
	return headers
}
