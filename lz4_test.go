package lz4

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/go-simd/matchlen"
	plz4 "github.com/pierrec/lz4/v4"
)

func scalarMatchLen(a, b []byte) int {
	n, limit := 0, len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for n < limit && a[n] == b[n] {
		n++
	}
	return n
}

func testInputs() [][]byte {
	rng := rand.New(rand.NewSource(1))
	in := [][]byte{nil, []byte("a"), []byte("abcabcabcabcabcabc"),
		[]byte("hello hello hello hello hello world hello world")}
	r := make([]byte, 12345)
	rng.Read(r)
	in = append(in, r) // incompressible
	in = append(in, bytes.Repeat([]byte("The quick brown fox. "), 3000))
	mix := make([]byte, 0, 60000)
	base := []byte("lorem ipsum dolor sit amet consectetur adipiscing ")
	for len(mix) < 60000 {
		if rng.Intn(3) == 0 {
			b := make([]byte, 24)
			rng.Read(b)
			mix = append(mix, b...)
		} else {
			mix = append(mix, base...)
		}
	}
	return append(in, mix)
}

func TestRoundTrip(t *testing.T) {
	for i, src := range testInputs() {
		got, err := DecompressBlock(CompressBlock(src), len(src))
		if err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("input %d: round-trip mismatch (%d vs %d bytes)", i, len(got), len(src))
		}
	}
}

func TestCrossCompatPierrec(t *testing.T) {
	for i, src := range testInputs() {
		if len(src) == 0 {
			continue
		}
		// our block decoded by pierrec
		out := make([]byte, len(src))
		n, err := plz4.UncompressBlock(CompressBlock(src), out)
		if err != nil || !bytes.Equal(out[:n], src) {
			t.Fatalf("input %d: pierrec failed to decode our block: %v", i, err)
		}
		// pierrec block decoded by us
		var c plz4.Compressor
		buf := make([]byte, plz4.CompressBlockBound(len(src)))
		m, err := c.CompressBlock(src, buf)
		if err != nil {
			t.Fatalf("input %d: pierrec compress: %v", i, err)
		}
		if m == 0 {
			continue // stored (incompressible)
		}
		got, err := DecompressBlock(buf[:m], len(src))
		if err != nil || !bytes.Equal(got, src) {
			t.Fatalf("input %d: we failed to decode pierrec block: %v", i, err)
		}
	}
}

func benchCorpus() []byte {
	mix := make([]byte, 0, 1<<20)
	base := []byte("The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs. ")
	rng := rand.New(rand.NewSource(3))
	for len(mix) < 1<<20 {
		if rng.Intn(8) == 0 {
			b := make([]byte, 32)
			rng.Read(b)
			mix = append(mix, b...)
		} else {
			mix = append(mix, base...)
		}
	}
	return mix
}

func BenchmarkEncodeMatchlen(b *testing.B) {
	src := benchCorpus()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = compress(src, matchlen.MatchLen)
	}
}

func BenchmarkEncodeScalar(b *testing.B) {
	src := benchCorpus()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = compress(src, scalarMatchLen)
	}
}

func BenchmarkEncodePierrec(b *testing.B) {
	src := benchCorpus()
	var c plz4.Compressor
	buf := make([]byte, plz4.CompressBlockBound(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.CompressBlock(src, buf)
	}
}
