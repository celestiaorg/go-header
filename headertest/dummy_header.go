package headertest

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/celestiaorg/go-header"
)

type ErrDummyVerify struct {
	Reason string
}

func (edv ErrDummyVerify) Error() string {
	return edv.Reason
}

type DummyHeader struct {
	Chainid      string
	PreviousHash header.Hash
	HeightI      uint64
	Timestamp    time.Time
	HashI        header.Hash

	// VerifyFailure allows for testing scenarios where a header would fail
	// verification. When set to true, it forces a failure.
	VerifyFailure bool
	// SoftFailure allows for testing scenarios where a header would fail
	// verification with SoftFailure set to true
	SoftFailure bool
}

func RandDummyHeader(t *testing.T) *DummyHeader {
	t.Helper()

	dh := &DummyHeader{
		PreviousHash: RandBytes(32),
		HeightI:      randUint63(),
		Timestamp:    time.Now().UTC(),
	}
	return dh
}

func (d *DummyHeader) New() *DummyHeader {
	return new(DummyHeader)
}

func (d *DummyHeader) IsZero() bool {
	return d == nil
}

func (d *DummyHeader) ChainID() string {
	return d.Chainid
}

func (d *DummyHeader) Hash() header.Hash {
	return d.HashI
}

func (d *DummyHeader) Height() uint64 {
	return d.HeightI
}

func (d *DummyHeader) LastHeader() header.Hash {
	return d.PreviousHash
}

func (d *DummyHeader) Time() time.Time {
	return d.Timestamp
}

func (d *DummyHeader) IsRecent(blockTime time.Duration) bool {
	return time.Since(d.Time()) <= blockTime
}

func (d *DummyHeader) IsExpired(period time.Duration) bool {
	expirationTime := d.Time().Add(period)
	return expirationTime.Before(time.Now())
}

func (d *DummyHeader) Verify(hdr *DummyHeader) error {
	if hdr.VerifyFailure {
		return &header.VerifyError{Reason: ErrDummyVerify, SoftFailure: hdr.SoftFailure}
	}

	// if adjacent, check PreviousHash -- this check is necessary
	// to mock fork-following scenarios with the dummy header.
	if header.Height() == d.Height()+1 {
		if !bytes.Equal(header.PreviousHash, d.Hash()) {
			return ErrDummyVerify{
				Reason: fmt.Sprintf("adjacent verify failure on header at height %d, err: %x != %x",
					header.Height(), header.PreviousHash, d.Hash()),
			}
		}
	}
	return nil
}

func (d *DummyHeader) Validate() error {
	return nil
}

func (d *DummyHeader) MarshalBinary() ([]byte, error) {
	return json.Marshal(d)
}

func (d *DummyHeader) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, d)
}

// RandBytes returns slice of n-bytes, or nil in case of error
func RandBytes(n int) []byte {
	buf := make([]byte, n)

	c, err := rand.Read(buf)
	if err != nil || c != n {
		return nil
	}

	return buf
}

func randUint63() uint64 {
	var buf [8]byte

	_, err := rand.Read(buf[:])
	if err != nil {
		return math.MaxInt64
	}

	return binary.BigEndian.Uint64(buf[:]) & math.MaxInt64
}
