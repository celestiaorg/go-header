package headertest

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"math"
	"testing"
	"time"

	"golang.org/x/crypto/sha3"

	"github.com/celestiaorg/go-header"
)

type Raw struct {
	ChainID      string
	PreviousHash header.Hash

	Height int64
	Time   time.Time
}

type DummyHeader struct {
	Raw

	hash header.Hash
}

func RandDummyHeader(t *testing.T) *DummyHeader {
	t.Helper()

	dh := &DummyHeader{
		Raw{
			PreviousHash: RandBytes(32),
			Height:       randInt63(),
			Time:         time.Now().UTC(),
		},
		nil,
	}
	err := dh.rehash()
	if err != nil {
		t.Fatal(err)
	}
	return dh
}

func (d *DummyHeader) New() header.Header {
	return new(DummyHeader)
}

func (d *DummyHeader) IsZero() bool {
	return d == nil
}

func (d *DummyHeader) ChainID() string {
	return "private"
}

func (d *DummyHeader) Hash() header.Hash {
	if len(d.hash) == 0 {
		if err := d.rehash(); err != nil {
			panic(err)
		}
	}
	return d.hash
}

func (d *DummyHeader) rehash() error {
	b, err := d.MarshalBinary()
	if err != nil {
		return err
	}
	hash := sha3.Sum512(b)
	d.hash = hash[:]
	return nil
}

func (d *DummyHeader) Height() int64 {
	return d.Raw.Height
}

func (d *DummyHeader) LastHeader() header.Hash {
	return d.Raw.PreviousHash
}

func (d *DummyHeader) Time() time.Time {
	return d.Raw.Time
}

func (d *DummyHeader) IsRecent(blockTime time.Duration) bool {
	return time.Since(d.Time()) <= blockTime
}

func (d *DummyHeader) IsExpired(period time.Duration) bool {
	expirationTime := d.Time().Add(period)
	return expirationTime.Before(time.Now())
}

func (d *DummyHeader) Verify(header header.Header) error {
	epsilon := 10 * time.Second
	if header.Time().After(time.Now().Add(epsilon)) {
		return fmt.Errorf("header Time too far in the future")
	}

	if header.Height() <= d.Height() {
		return fmt.Errorf("expected new header Height to be larger than old header Time")
	}

	if header.Time().Before(d.Time()) {
		return fmt.Errorf("expected new header Time to be after old header Time")
	}

	return nil
}

func (d *DummyHeader) Validate() error {
	return nil
}

func (d *DummyHeader) MarshalBinary() ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(d.Raw)
	return buf.Bytes(), err
}

func (d *DummyHeader) UnmarshalBinary(data []byte) error {
	dec := gob.NewDecoder(bytes.NewReader(data))
	err := dec.Decode(&d.Raw)
	if err != nil {
		return err
	}
	err = d.rehash()
	if err != nil {
		return err
	}

	return nil
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

func randInt63() int64 {
	var buf [8]byte

	_, err := rand.Read(buf[:])
	if err != nil {
		return math.MaxInt64
	}

	return int64(binary.BigEndian.Uint64(buf[:]) & math.MaxInt64)
}
