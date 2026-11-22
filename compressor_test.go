package lz4

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// sample returns n bytes of mixed compressibility: runs, repeated words and
// noise, so every match path (short, long, overlapping, none) is taken.
func sample(r *rand.Rand, n int) []byte {
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

func roundTrip(t testing.TB, comp, src []byte) {
	t.Helper()
	got, err := DecompressBlock(comp, len(src))
	if err != nil {
		t.Fatalf("decode of %d bytes: %v", len(src), err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("round trip of %d bytes differs", len(src))
	}
}

// One Compressor over many blocks of changing size, never cleared: a slot left
// by an earlier block must never be taken for a match in a later one.
func TestCompressorReuseRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	c := NewCompressor()
	sizes := []int{0, 1, 15, 16, 17, 100, 4096, 4096, 70000, 300, 65536, 65537, 200000, 9, 4096}
	for round := 0; round < 4; round++ {
		for _, n := range sizes {
			src := sample(r, n)
			roundTrip(t, c.Compress(src), src)
			roundTrip(t, c.AppendCompress([]byte("prefix"), src)[6:], src)
		}
	}
}

// Identical consecutive blocks must compress identically: the second sees
// only stale slots from the first, so it must find the same matches as a
// fresh table does.
func TestCompressorMatchesFreshTable(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	c := NewCompressor()
	for _, n := range []int{100, 4096, 65536, 150000} {
		src := sample(r, n)
		want := NewCompressor().Compress(src)
		for i := 0; i < 3; i++ {
			if got := c.Compress(src); !bytes.Equal(got, want) {
				t.Fatalf("n=%d pass %d: reused table gave a different block (%d vs %d bytes)", n, i, len(got), len(want))
			}
		}
	}
}

// compress, the benchmarks' entry with a swappable match counter, writes the
// same block as the public path.
func TestCompressWithScalarMatchLen(t *testing.T) {
	src := sample(rand.New(rand.NewPCG(9, 10)), 20000)
	if got, want := compress(src, scalarMatchLen), CompressBlock(src); !bytes.Equal(got, want) {
		t.Fatal("scalar match counter gave a different block")
	}
}

// The generation counter is rewound with one clear before it can leave int32.
func TestCompressorBaseRewind(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	src := sample(r, 4096)
	c := NewCompressor()
	c.base = maxBase - len(src) - 1 // the next call fits, the one after rewinds
	for i := 0; i < 3; i++ {
		roundTrip(t, c.Compress(src), src)
	}
	if c.base > maxBase {
		t.Fatalf("base %d past %d", c.base, maxBase)
	}
	c.base = maxBase // a stale table must be cleared, not trusted
	roundTrip(t, c.Compress(src), src)
}

// A 4 KiB block costs the output buffer and nothing else: no 256 KiB table.
func TestCompressBlockAllocs(t *testing.T) {
	src := sample(rand.New(rand.NewPCG(5, 6)), 4096)
	CompressBlock(src) // warm the pool
	if n := testing.AllocsPerRun(50, func() { CompressBlock(src) }); n > 1 {
		t.Fatalf("CompressBlock: %v allocs per 4 KiB block, want 1 (the result)", n)
	}
	c, dst := NewCompressor(), make([]byte, 0, 8192)
	if n := testing.AllocsPerRun(50, func() { dst = c.AppendCompress(dst[:0], src) }); n != 0 {
		t.Fatalf("AppendCompress: %v allocs, want 0", n)
	}
}

func TestHashLogFor(t *testing.T) {
	for _, tc := range []struct{ n, want int }{{0, minHashLog}, {16, minHashLog}, {4096, 13}, {65535, 16}, {1 << 20, hashLog}} {
		if got := hashLogFor(tc.n); int(got) != tc.want {
			t.Errorf("hashLogFor(%d) = %d, want %d", tc.n, got, tc.want)
		}
	}
}

// FuzzCompressor: a reused Compressor round-trips whatever it is given, in any
// order, including after other inputs have filled its table.
func FuzzCompressor(f *testing.F) {
	f.Add([]byte("hello hello hello hello hello"), []byte("hello world hello world hello"))
	f.Add(bytes.Repeat([]byte{1}, 5000), bytes.Repeat([]byte{1, 2}, 3000))
	f.Add([]byte{}, []byte{0})
	c := NewCompressor()
	f.Fuzz(func(t *testing.T, a, b []byte) {
		for _, src := range [][]byte{a, b, a} {
			roundTrip(t, c.Compress(src), src)
		}
	})
}
