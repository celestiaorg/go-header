package header

import (
	"encoding/hex"
	"fmt"
)

// Hash represents cryptographic hash and provides basic serialization functions.
type Hash []byte

// String implements fmt.Stringer interface.
func (h Hash) String() string {
	buf := make([]byte, hex.EncodedLen(len(h)))
	hex.Encode(buf, h)
	hexToUpper(buf)
	return string(buf)
}

// MarshalJSON serializes Hash into valid JSON.
func (h Hash) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 2+hex.EncodedLen(len(h)))
	buf[0] = '"'
	hex.Encode(buf[1:], h)
	hexToUpper(buf)
	buf[len(buf)-1] = '"'
	return buf, nil
}

// UnmarshalJSON deserializes JSON representation of a Hash into object.
func (h *Hash) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("invalid hex string: %s", data)
	}

	buf := make([]byte, hex.DecodedLen(len(data)-2))
	_, err := hex.Decode(buf, data[1:len(data)-1])
	if err != nil {
		return err
	}
	*h = buf
	return nil
}

// because we encode hex (alphabet: 0-9a-f) we can do this inplace.
func hexToUpper(b []byte) {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		b[i] = c
	}
}
