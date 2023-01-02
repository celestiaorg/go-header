package header

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"golang.org/x/crypto/sha3"
	"math/rand"
	"testing"
	"time"
)

type Raw struct {
	PreviousHash Hash

	Height int64
	Time   time.Time
}

type DummyHeader struct {
	Raw

	hash Hash
}

func (d *DummyHeader) New() Header {
	return new(DummyHeader)
}

func (d *DummyHeader) Hash() Hash {
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

func (d *DummyHeader) LastHeader() Hash {
	return d.Raw.PreviousHash
}

func (d *DummyHeader) Time() time.Time {
	return d.Raw.Time
}

func (d *DummyHeader) IsRecent(blockTime time.Duration) bool {
	return time.Since(d.Time()) <= blockTime
}

// TrustingPeriod is period through which we can trust a header's validators set.
// This is copied from ceelstia-node
//
// Should be significantly less than the unbonding period (e.g. unbonding
// period = 3 weeks, trusting period = 2 weeks).
//
// More specifically, trusting period + Time needed to check headers + Time
// needed to report and punish misbehavior should be less than the unbonding
// period.
var TrustingPeriod = 168 * time.Hour

func (d *DummyHeader) IsExpired() bool {
	expirationTime := d.Time().Add(TrustingPeriod)
	return expirationTime.Before(time.Now())
}

func (d *DummyHeader) VerifyAdjacent(other Header) error {
	if other.Height() != d.Height()+1 {
		return fmt.Errorf("invalid Height, expected: %d, got: %d", d.Height()+1, other.Height())
	}

	if err := d.Verify(other); err != nil {
		return err
	}

	return nil
}

func (d *DummyHeader) VerifyNonAdjacent(other Header) error {
	return d.Verify(other)
}

func (d *DummyHeader) Verify(header Header) error {
	// wee1
	epsilon := 10 * time.Second
	if header.Time().After(time.Now().Add(epsilon)) {
		return errors.New("header Time too far in the future")
	}

	if header.Height() <= d.Height() {
		return errors.New("expected new header Height to be larger than old header Time")
	}

	if header.Time().Before(d.Time()) {
		return errors.New("expected new header Time to be after old header Time")
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

func randBytes(n int) []byte {
	buf := make([]byte, n)

	c, err := rand.Read(buf)
	if err != nil || c != n {
		return nil
	}

	return buf
}

func RandDummyHeader(t *testing.T) *DummyHeader {
	t.Helper()
	dh, err := randDummyHeader()
	if err != nil {
		t.Fatal(err)
	}
	return dh
}

func randDummyHeader() (*DummyHeader, error) {
	dh := &DummyHeader{
		Raw{
			PreviousHash: randBytes(32),
			Height:       rand.Int63(),
			Time:         time.Now().UTC(),
		},
		nil,
	}
	err := dh.rehash()
	return dh, err
}

func mustRandDummyHeader() *DummyHeader {
	dh, err := randDummyHeader()
	if err != nil {
		panic(err)
	}
	return dh
}

// TestSuite provides everything you need to test chain of Headers.
// If not, please don't hesitate to extend it for your case.
type TestSuite struct {
	t *testing.T

	head *DummyHeader
}

// NewTestSuite setups a new test suite.
func NewTestSuite(t *testing.T) *TestSuite {
	return &TestSuite{
		t: t,
	}
}

func (s *TestSuite) genesis() *DummyHeader {
	return &DummyHeader{
		hash: nil,
		Raw: Raw{
			PreviousHash: nil,
			Height:       1,
			Time:         time.Now().Add(-10 * time.Second).UTC(),
		},
	}
}

func (s *TestSuite) Head() *DummyHeader {
	if s.head == nil {
		s.head = s.genesis()
	}
	return s.head
}

func (s *TestSuite) GenDummyHeaders(num int) []*DummyHeader {
	headers := make([]*DummyHeader, num)
	for i := range headers {
		headers[i] = s.GenDummyHeader()
	}
	return headers
}

func (s *TestSuite) GenDummyHeader() *DummyHeader {
	if s.head == nil {
		s.head = s.genesis()
		return s.head
	}

	dh := mustRandDummyHeader()
	dh.Raw.Height = s.head.Height() + 1
	dh.Raw.PreviousHash = s.head.Hash()
	_ = dh.rehash()
	s.head = dh
	return s.head
}

type DummySubscriber struct {
	Headers []*DummyHeader
}

func (mhs *DummySubscriber) AddValidator(func(context.Context, *DummyHeader) pubsub.ValidationResult) error {
	return nil
}

func (mhs *DummySubscriber) Subscribe() (Subscription[*DummyHeader], error) {
	return mhs, nil
}

func (mhs *DummySubscriber) NextHeader(ctx context.Context) (*DummyHeader, error) {
	defer func() {
		if len(mhs.Headers) > 1 {
			// pop the already-returned header
			cp := mhs.Headers
			mhs.Headers = cp[1:]
		} else {
			mhs.Headers = make([]*DummyHeader, 0)
		}
	}()
	if len(mhs.Headers) == 0 {
		return nil, context.Canceled
	}
	return mhs.Headers[0], nil
}

func (mhs *DummySubscriber) Stop(context.Context) error { return nil }
func (mhs *DummySubscriber) Cancel()                    {}
