//go:build liblz4 && cgo

package lz4

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/go-compressions/lz4/internal/liblz4cgo"
)

// Run with: go test -tags liblz4 -run Liblz4 .
//
// Whatever this package's encoder writes — fresh table or reused, any size —
// the reference liblz4 decoder must accept, with the output buffer exactly the
// decoded size, and give the bytes back. And the reference's blocks must
// decode here.
func TestLiblz4DecodesOurBlocks(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	c := NewCompressor()
	for _, n := range []int{0, 1, 12, 16, 17, 31, 100, 1000, 4096, 4097, 65535, 65536, 65537, 300000, 1 << 20} {
		for i := 0; i < 3; i++ {
			src := sample(r, n)
			for name, comp := range map[string][]byte{"pooled": CompressBlock(src), "reused": c.Compress(src)} {
				got, err := liblz4cgo.Decompress(comp, len(src))
				if err != nil || !bytes.Equal(got, src) {
					t.Fatalf("n=%d %s: liblz4 decode: err=%v equal=%v", n, name, err, bytes.Equal(got, src))
				}
			}
		}
	}
}

func TestLiblz4BlocksDecodeHere(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	for _, n := range []int{0, 1, 100, 4096, 65536, 300000} {
		src := sample(r, n)
		comp, err := liblz4cgo.Compress(src)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip(t, comp, src)
	}
}

func FuzzLiblz4Differential(f *testing.F) {
	f.Add([]byte("abcabcabcabcabcabcabcabc"), []byte{})
	f.Add(bytes.Repeat([]byte{9}, 70000), []byte("x"))
	c := NewCompressor()
	f.Fuzz(func(t *testing.T, a, b []byte) {
		for _, src := range [][]byte{a, b, a} {
			got, err := liblz4cgo.Decompress(c.Compress(src), len(src))
			if err != nil || !bytes.Equal(got, src) {
				t.Fatalf("liblz4 refused or changed a %d-byte block: %v", len(src), err)
			}
		}
	})
}

// The reference, on the same inputs as BenchmarkCompressBlock, one call per block.
func BenchmarkLiblz4Reference(b *testing.B) {
	benchSizes(b, func(src []byte) { _, _ = liblz4cgo.Compress(src) })
}
