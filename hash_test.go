package header

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHash(t *testing.T) {
	h := randHash()

	buf, err := h.MarshalJSON()
	require.NoError(t, err)

	var h2 Hash
	err = h2.UnmarshalJSON(buf)
	require.NoError(t, err)

	require.Equal(t, h.String(), h2.String())
}

func BenchmarkHashMarshaling(b *testing.B) {
	h := randHash()

	golden, err := h.MarshalJSON()
	require.NoError(b, err)

	b.ResetTimer()

	b.Run("String", func(b *testing.B) {
		wantSize := hex.EncodedLen(len(h))

		for i := 0; i < b.N; i++ {
			ln := len(h.String())
			require.Equal(b, ln, wantSize)
		}
	})

	b.Run("Marshal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			buf, err := h.MarshalJSON()
			require.NoError(b, err)
			require.NotZero(b, buf)
		}
	})

	b.Run("Unmarshal", func(b *testing.B) {
		var h2 Hash

		for i := 0; i < b.N; i++ {
			err := h2.UnmarshalJSON(golden)
			require.NoError(b, err)
		}
	})
}

func Fuzz_hexToUpper(f *testing.F) {
	f.Add([]byte("48656c6c6f20476f7068657221"))

	f.Fuzz(func(t *testing.T, buf []byte) {
		hexToUpper(buf)
	})
}

func randHash() Hash {
	var buf [sha256.Size]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	return Hash(buf[:])
}
