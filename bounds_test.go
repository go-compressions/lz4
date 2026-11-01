package lz4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/rand"
	"runtime"
	"testing"
)

// allocated reports the bytes f allocated, measured as the pilot in
// go-compressions/lzfse did: TotalAlloc before and after.
func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// expandingBlock is a valid LZ4 block of `ext` 0xFF length bytes: 1 literal,
// then one match of 19+255*ext bytes at offset 1. 1 MiB of source turns into
// ~255 MiB, the amplification the format allows.
func expandingBlock(ext int) (blk []byte, out int) {
	blk = []byte{0x1F, 'a', 1, 0}
	blk = append(blk, bytes.Repeat([]byte{255}, ext)...)
	blk = append(blk, 0) // terminates the extension
	// 1 literal + (4 + 15 + 255*ext + 0) match bytes
	return blk, 1 + 4 + 15 + 255*ext
}

// TestBombs_NothingAllocatedFromAHeader plants each road a size field can
// reach an allocation by, and requires the refusal to cost under 1 MiB.
// Before the bounds: the bv41 and bv4- bombs allocated 1 GiB from 12 and 8
// bytes, and DecompressBlock(1 byte, 1<<30) allocated 1 GiB.
func TestBombs_NothingAllocatedFromAHeader(t *testing.T) {
	bv41 := func(raw, comp uint32, payload ...byte) []byte {
		return append(le32(le32([]byte("bv41"), raw), comp), payload...)
	}
	bv4s := func(raw uint32, payload ...byte) []byte {
		return append(le32([]byte("bv4-"), raw), payload...)
	}
	cases := []struct {
		name  string
		frame []byte
		want  error
	}{
		{"bv41 raw=comp=1GiB", bv41(1<<30, 1<<30), ErrTooLarge},
		{"bv41 raw=4GiB-1", bv41(math.MaxUint32, 1), ErrTooLarge},
		{"bv41 raw=16MiB comp=1 (255:1 cannot reach it)", bv41(1<<24, 1, 0), ErrAppleFrame},
		{"bv41 comp=4GiB-1 raw=100", bv41(100, math.MaxUint32), ErrAppleFrame},
		{"bv41 raw=16MiB comp=16MiB, nothing sent", bv41(1<<24, 1<<24), ErrAppleFrame},
		{"bv4- 1GiB", bv4s(1 << 30), ErrTooLarge},
		{"bv4- 4GiB-1", bv4s(math.MaxUint32), ErrTooLarge},
		{"bv4- 16MiB, nothing sent", bv4s(1 << 24), ErrAppleFrame},
	}
	for _, tc := range cases {
		var err error
		n := allocated(func() { _, err = DecompressApple(tc.frame) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if n >= 1<<20 {
			t.Errorf("%s: allocated %d bytes, want < 1 MiB", tc.name, n)
		}
	}

	// The hint passed to DecompressBlock is clamped to what 1 byte can make.
	n := allocated(func() { _, _ = DecompressBlock([]byte{0}, 1<<30) })
	if n >= 1<<20 {
		t.Errorf("DecompressBlock(1 byte, dstCap=1GiB) allocated %d bytes", n)
	}
	// ...and to the ceiling, even for a source that could legitimately fill it.
	big := make([]byte, 1<<22)
	n = allocated(func() { _, _ = DecompressBlockLimit(big, 1<<30, 1000) })
	if n >= 1<<20 {
		t.Errorf("DecompressBlockLimit(dstCap=1GiB, maxOut=1000) allocated %d bytes", n)
	}
}

// TestBounds_StructuralExpansionIs255 shows the bound used for dstCap and for
// bv41 headers is not an estimate: the most expanding block there is reaches
// ~255 output bytes per input byte and no more.
func TestBounds_StructuralExpansionIs255(t *testing.T) {
	blk, want := expandingBlock(1 << 12)
	got, err := DecompressBlock(blk, 0)
	if err != nil || len(got) != want {
		t.Fatalf("len=%d err=%v, want %d", len(got), err, want)
	}
	if len(got) > maxExpansion*len(blk) {
		t.Fatalf("%d output bytes from %d input: more than %d:1", len(got), len(blk), maxExpansion)
	}
}

// TestBounds_BlockCeilingIsExact: at the ceiling passes, one under refused, for
// every shape of write decodeInto has (literals, the two match shortcuts, the
// bulk copy, the extension loops) and from every starting hint.
func TestBounds_BlockCeilingIsExact(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	random := make([]byte, 5000)
	r.Read(random)
	inputs := map[string][]byte{
		"one byte":    {7},
		"random":      random,
		"zeros":       make([]byte, 70000),
		"text":        bytes.Repeat([]byte("the quick brown fox "), 400),
		"short match": []byte("abcdabcdabcdabcdabcdabcdabcdabcdabcd"),
	}
	for name, in := range inputs {
		blk := CompressBlock(in)
		for _, hint := range []int{0, len(in) / 2, len(in), 10 * len(in)} {
			got, err := DecompressBlockLimit(blk, hint, len(in))
			if err != nil || !bytes.Equal(got, in) {
				t.Errorf("%s hint=%d: at the ceiling: err=%v len=%d, want %d", name, hint, err, len(got), len(in))
			}
			if _, err := DecompressBlockLimit(blk, hint, len(in)-1); !errors.Is(err, ErrTooLarge) {
				t.Errorf("%s hint=%d: one over: err = %v, want ErrTooLarge", name, hint, err)
			}
		}
	}
	// The extension loop of the match length: refused while reading the 0xFF
	// bytes, before the match is expanded.
	blk, out := expandingBlock(1 << 14)
	if got, err := DecompressBlockLimit(blk, 0, out); err != nil || len(got) != out {
		t.Errorf("expanding block at its ceiling: err=%v len=%d", err, len(got))
	}
	n := allocated(func() {
		_, err := DecompressBlockLimit(blk, 0, 1000)
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("expanding block, maxOut 1000: err = %v", err)
		}
	})
	if n >= 1<<20 {
		t.Errorf("expanding block refused after allocating %d bytes", n)
	}
	// A short overlapping match (ml=10 > offset=1) that passes the ceiling on
	// the grow path, and a short match whose fixed 16-byte shortcut stays
	// inside the slack while di passes the ceiling: only the check after the
	// loop sees that one.
	grow := []byte{0x16, 'a', 1, 0}
	if _, err := DecompressBlockLimit(grow, 0, 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("overlapping match past the ceiling: err = %v", err)
	}
	slack := []byte{0x40, 'a', 'b', 'c', 'd', 4, 0}
	if got, err := DecompressBlockLimit(slack, 0, 8); err != nil || string(got) != "abcdabcd" {
		t.Errorf("shortcut at the ceiling: %q %v", got, err)
	}
	if _, err := DecompressBlockLimit(slack, 0, 6); !errors.Is(err, ErrTooLarge) {
		t.Errorf("shortcut overshooting the ceiling by 2: err = %v", err)
	}
	// A literal run that alone passes the ceiling.
	lits := append([]byte{0xF0, 0xFF, 0x10}, make([]byte, 15+255+16)...)
	if _, err := DecompressBlockLimit(lits, 0, 100); !errors.Is(err, ErrTooLarge) {
		t.Errorf("literal run past the ceiling: err = %v", err)
	}
	// A ceiling so large that it would overflow is clamped, not wrapped.
	if _, err := DecompressBlockLimit(CompressBlock(random), 0, math.MaxInt); err != nil {
		t.Errorf("maxOut=MaxInt: %v", err)
	}
	if _, err := DecompressBlockLimit(CompressBlock(random), 0, -5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("negative maxOut: err = %v, want ErrTooLarge", err)
	}
}

// TestBounds_LiteralExtensionPastSource: a literal length built from 0xFF bytes
// that exceeds the source is corrupt, whatever int is (on 32-bit the sum would
// wrap and slip past `ip+ll > len(src)`).
func TestBounds_LiteralExtensionPastSource(t *testing.T) {
	src := append([]byte{0xF0}, bytes.Repeat([]byte{255}, 4096)...)
	src = append(src, 1)
	if _, err := DecompressBlock(src, 0); !errors.Is(err, errCorrupt) {
		t.Errorf("err = %v, want errCorrupt", err)
	}
}

// TestBounds_AppleBlockCeilings: maxBlock and the total ceiling are exact.
func TestBounds_AppleBlockCeilings(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 40) // 400 bytes
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"stored", buildAppleFrame(storedBlock(data))},
		{"compressed", buildAppleFrame(compressedBlock(data))},
	} {
		var sink bytes.Buffer
		n, err := DecompressAppleStreamLimit(&sink, bytes.NewReader(tc.frame), len(data))
		if err != nil || n != int64(len(data)) || !bytes.Equal(sink.Bytes(), data) {
			t.Errorf("%s: at maxBlock: n=%d err=%v", tc.name, n, err)
		}
		if _, err := DecompressAppleStreamLimit(io.Discard, bytes.NewReader(tc.frame), len(data)-1); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: one over maxBlock: err = %v, want ErrTooLarge", tc.name, err)
		}
		if _, err := DecompressAppleStreamLimit(io.Discard, bytes.NewReader(tc.frame), -1); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: negative maxBlock: err = %v, want ErrTooLarge", tc.name, err)
		}
		if got, err := DecompressAppleLimit(tc.frame, len(data)); err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s: at the total ceiling: err=%v len=%d", tc.name, err, len(got))
		}
		if _, err := DecompressAppleLimit(tc.frame, len(data)-1); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: one over the total ceiling: err = %v, want ErrTooLarge", tc.name, err)
		}
	}
	// The total is cumulative over blocks, each of them within maxBlock.
	two := buildAppleFrame(storedBlock(data), compressedBlock(data))
	if got, err := DecompressAppleLimit(two, 2*len(data)); err != nil || len(got) != 2*len(data) {
		t.Errorf("two blocks at the total ceiling: err=%v len=%d", err, len(got))
	}
	if _, err := DecompressAppleLimit(two, 2*len(data)-1); !errors.Is(err, ErrTooLarge) {
		t.Errorf("two blocks, one over: err = %v, want ErrTooLarge", err)
	}
	// Negative total ceiling behaves as zero.
	if _, err := DecompressAppleLimit(buildAppleFrame(storedBlock(data)), -1); !errors.Is(err, ErrTooLarge) {
		t.Errorf("negative total: err = %v", err)
	}
	// The default admits a 64 KiB block (what Apple writes) and refuses past 16 MiB.
	big := bytes.Repeat([]byte{9}, 1<<16)
	if got, err := DecompressApple(buildAppleFrame(compressedBlock(big))); err != nil || !bytes.Equal(got, big) {
		t.Errorf("64 KiB block under the default: err=%v", err)
	}
}

// TestBounds_AppleBlockDecodingPastItsHeader: a payload that expands beyond the
// rawSize its header declares is a malformed frame, and stops growing at the
// declared size rather than at the payload's.
func TestBounds_AppleBlockDecodingPastItsHeader(t *testing.T) {
	data := bytes.Repeat([]byte("abcd"), 1000)
	blk := compressedBlock(data)
	blk.rawSize = 100
	var err error
	n := allocated(func() { _, err = DecompressApple(buildAppleFrame(blk)) })
	if !errors.Is(err, ErrAppleFrame) {
		t.Errorf("err = %v, want ErrAppleFrame", err)
	}
	if n >= 1<<20 {
		t.Errorf("allocated %d bytes", n)
	}
	// Same, but the overshoot is found by the check after the loop (short
	// matches write inside the slack).
	short := compressedBlock([]byte("abcdabcdabcdabcd"))
	short.rawSize = 14
	if _, err := DecompressApple(buildAppleFrame(short)); !errors.Is(err, ErrAppleFrame) {
		t.Errorf("short overshoot: err = %v, want ErrAppleFrame", err)
	}
}

// trickle returns one byte per Read, to drive readBlock's loop.
type trickle struct{ r io.Reader }

func (t trickle) Read(p []byte) (int, error) { return t.r.Read(p[:min(1, len(p))]) }

// TestReadBlock: the buffer follows the bytes that arrive, and short reads are
// reassembled.
func TestReadBlock(t *testing.T) {
	want := make([]byte, 3*readChunk+17)
	rand.New(rand.NewSource(3)).Read(want)
	got, err := readBlock(bytes.NewReader(want), len(want))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("whole read: err=%v", err)
	}
	got, err = readBlock(trickle{bytes.NewReader(want[:2000])}, 2000)
	if err != nil || !bytes.Equal(got, want[:2000]) {
		t.Fatalf("trickled read: err=%v", err)
	}
	if got, err := readBlock(bytes.NewReader(nil), 0); err != nil || len(got) != 0 {
		t.Fatalf("empty read: %v %d", err, len(got))
	}
	if _, err := readBlock(bytes.NewReader(want[:readChunk+5]), 2*readChunk); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated: err = %v, want ErrUnexpectedEOF", err)
	}
}

// TestBounds_MalformedShapes runs the malformed block shapes the fuzzers start
// from as plain tests, which emulated CI lanes (a compiled test binary run away
// from testdata/) would otherwise never see. The fuzzers found no crasher.
func TestBounds_MalformedShapes(t *testing.T) {
	for name, in := range map[string][]byte{
		"empty":           nil,
		"token only":      {0xFF},
		"match no offset": {0x1F, 'a'},
		"offset zero":     {0x1F, 'a', 0, 0},
		"offset too far":  {0x1F, 'a', 9, 0, 0},
	} {
		if out, err := DecompressBlockLimit(in, 0, 1<<20); err == nil && len(out) > 1<<20 {
			t.Errorf("%s: %d bytes past the ceiling", name, len(out))
		}
	}
}
