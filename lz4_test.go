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
	in = append(in, mix)
	// A large, semi-compressible input well over 64 KiB (>256 KiB), so the
	// cross-compatibility test exercises blocks with offsets and lengths far
	// beyond the small-input regime in both directions.
	big := make([]byte, 0, 300000)
	for len(big) < 300000 {
		if rng.Intn(4) == 0 {
			b := make([]byte, 16)
			rng.Read(b)
			big = append(big, b...)
		} else {
			big = append(big, base...)
		}
	}
	return append(in, big)
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
		// our block decoded by pierrec. pierrec's UncompressBlock wants a
		// destination strictly larger than the decoded size, so add slack.
		out := make([]byte, len(src)+1)
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

// TestDecompressCorrupt exercises every error path in DecompressBlock with
// minimal hand-built malformed blocks.
func TestDecompressCorrupt(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		// token says literal length is 15+ but the varint is truncated.
		{"trunc literal-length varint", []byte{0xF0}},
		// literal length runs past the end of the block.
		{"literals past end", []byte{0x30, 0x41}},
		// literals consumed, but the 2-byte offset is missing.
		{"missing offset", []byte{0x10, 'a', 0x01}},
		// 4 literals then a match whose nibble is 15 (so the match length is
		// read from a varint) but the varint is truncated. The literals make
		// the back-reference (offset 2) valid so decode reaches the varint loop.
		{"trunc match-length varint", []byte{0x4F, 'a', 'a', 'a', 'a', 0x02, 0x00}},
		// offset of zero is invalid (no back-reference possible).
		{"zero offset", []byte{0x10, 'a', 0x00, 0x00}},
	}
	for _, tc := range cases {
		if _, err := DecompressBlock(tc.in, 16); err != errCorrupt {
			t.Errorf("%s: got err=%v, want errCorrupt", tc.name, err)
		}
	}
	if errCorrupt.Error() != "lz4: corrupt block" {
		t.Errorf("Error() = %q", errCorrupt.Error())
	}
}

// TestDecompressGrow exercises matchCopy's capacity-grow path: when the dstCap
// hint underestimates the decompressed size, the match copy must append-grow
// the output instead of index-writing into spare capacity. We pass dstCap 0 so
// every literal append and every match copy has to grow.
func TestDecompressGrow(t *testing.T) {
	for i, src := range testInputs() {
		got, err := DecompressBlock(CompressBlock(src), 0)
		if err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("input %d: grow-path round-trip mismatch (%d vs %d bytes)", i, len(got), len(src))
		}
	}
	// A pure run-length fill (offset 1, long match) over an undersized hint
	// drives the overlapping doubling loop through a grow.
	run := bytes.Repeat([]byte{'Z'}, 5000)
	got, err := DecompressBlock(CompressBlock(run), 1)
	if err != nil || !bytes.Equal(got, run) {
		t.Fatalf("run-length grow: err=%v equal=%v", err, bytes.Equal(got, run))
	}
	// A negative capacity hint is clamped to zero rather than panicking the
	// make(); decode still grows to the real size.
	hello := []byte("hello hello hello hello world")
	got, err = DecompressBlock(CompressBlock(hello), -1)
	if err != nil || !bytes.Equal(got, hello) {
		t.Fatalf("negative hint: err=%v equal=%v", err, bytes.Equal(got, hello))
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
