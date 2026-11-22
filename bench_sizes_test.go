package lz4

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// benchSample is mixed-compressibility data (runs, repeated words, noise). It
// is self-contained so this file can be dropped into an older checkout and
// built there as the baseline of an A/B run.
func benchSample(n int) []byte {
	r := rand.New(rand.NewPCG(99, 100))
	b := make([]byte, 0, n)
	for len(b) < n {
		switch r.IntN(3) {
		case 0:
			b = append(b, bytes.Repeat([]byte{byte(r.Uint32())}, 1+r.IntN(300))...)
		case 1:
			b = append(b, bytes.Repeat([]byte("lorem ipsum dolor "), 1+r.IntN(20))...)
		default:
			for i := r.IntN(200); i > 0; i-- {
				b = append(b, byte(r.Uint32()))
			}
		}
	}
	return b[:n]
}

func sizeName(n int) string {
	switch {
	case n >= 1<<20:
		return "1MiB"
	case n >= 64<<10:
		return "64KiB"
	}
	return "4KiB"
}

// benchSizes runs fn over one block per call at the three sizes that matter:
// 4 KiB (per-page callers), 64 KiB (the LZ4 window) and 1 MiB.
func benchSizes(b *testing.B, fn func(src []byte)) {
	for _, n := range []int{4 << 10, 64 << 10, 1 << 20} {
		src := benchSample(n)
		b.Run(sizeName(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				fn(src)
			}
		})
	}
}

func BenchmarkCompressBlock(b *testing.B) {
	benchSizes(b, func(src []byte) { _ = CompressBlock(src) })
}
