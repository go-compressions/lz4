package lz4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"testing"

	plz4 "github.com/pierrec/lz4/v4"
)

func TestXXH32Vectors(t *testing.T) {
	for _, c := range []struct {
		in   string
		want uint32
	}{
		{"", 0x02CC5D05},
		{"a", 0x550D7456},
		{"abc", 0x32D153FF},
		{"Nobody inspects the spammish repetition", 0xE2293B2F},
	} {
		if got := xxh32([]byte(c.in), 0); got != c.want {
			t.Errorf("xxh32(%q) = %#08x, want %#08x", c.in, got, c.want)
		}
	}
}

// mkFrame builds a frame by hand: the header checksum is computed, the blocks
// are given as raw block-header+payload (+checksum) bytes, tail follows.
func mkFrame(flg, bd byte, size *uint64, blocks [][]byte, tail []byte) []byte {
	if size == nil {
		flg &^= flgContentSize // the callers write flags as if the size were there
	}
	desc := []byte{flg, bd}
	if size != nil {
		desc = binary.LittleEndian.AppendUint64(desc, *size)
	}
	out := binary.LittleEndian.AppendUint32(nil, frameMagic)
	out = append(out, desc...)
	out = append(out, byte(xxh32(desc, 0)>>8))
	for _, b := range blocks {
		out = append(out, b...)
	}
	return append(out, tail...)
}

func blk(payload []byte, stored bool) []byte {
	h := uint32(len(payload))
	if stored {
		h |= frameStored
	}
	return append(binary.LittleEndian.AppendUint32(nil, h), payload...)
}

var endMark = []byte{0, 0, 0, 0}

func sz(n uint64) *uint64 { return &n }

func TestFrameRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	for _, n := range []int{0, 1, 15, 16, 100, 4096, 65536, 300000, frameBlockMax - 1, frameBlockMax, frameBlockMax + 1, 2*frameBlockMax + 77} {
		src := sample(r, n)
		for _, o := range []FrameOptions{{}, {ContentChecksum: true}, {BlockChecksum: true}, {true, true}} {
			f := CompressFrameOptions(src, o)
			got, err := DecompressFrame(f)
			if err != nil || !bytes.Equal(got, src) {
				t.Fatalf("n=%d %+v: err=%v equal=%v", n, o, err, bytes.Equal(got, src))
			}
		}
	}
}

func TestFrameHeaderIsWhatUBootAccepts(t *testing.T) {
	f := CompressFrame([]byte("hello hello hello hello hello hello"))
	if !bytes.Equal(f[:6], []byte{0x04, 0x22, 0x4D, 0x18, 0x68, 0x70}) {
		t.Fatalf("header % x", f[:6])
	}
	if binary.LittleEndian.Uint64(f[6:]) != 35 {
		t.Fatal("content size")
	}
	if !bytes.Equal(f[len(f)-4:], endMark) {
		t.Fatal("no end mark")
	}
	if !bytes.Equal(CompressFrame(nil), mkFrame(0x68, 0x70, sz(0), nil, endMark)) {
		t.Fatal("empty frame differs")
	}
	if !bytes.Equal(f, CompressFrame([]byte("hello hello hello hello hello hello"))) {
		t.Fatal("not deterministic")
	}
	// ulz4fn needs: version 1, independent, no reserved bits; checksums optional.
	if g := CompressFrameOptions([]byte("x"), FrameOptions{true, true}); g[4] != 0x7C {
		t.Fatalf("flags %#x", g[4])
	}
}

func TestIncompressibleBlockIsStored(t *testing.T) {
	src := sample(rand.New(rand.NewPCG(1, 2)), 5000)
	for i := range src {
		src[i] = byte(rand.New(rand.NewPCG(uint64(i), 9)).Uint32())
	}
	f := CompressFrame(src)
	if binary.LittleEndian.Uint32(f[15:])&frameStored == 0 {
		t.Fatal("noise was not stored")
	}
	if got, err := DecompressFrame(f); err != nil || !bytes.Equal(got, src) {
		t.Fatalf("err=%v", err)
	}
}

// pierrec/lz4 is a second, independent frame implementation: it must read what
// we write (it checks the header checksum and the optional ones) and we must
// read what it writes.
func TestPierrecFrames(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 32))
	for _, n := range []int{0, 1, 1000, 70000, 300000} {
		src := sample(r, n)
		for _, o := range []FrameOptions{{}, {true, true}} {
			pr := plz4.NewReader(bytes.NewReader(CompressFrameOptions(src, o)))
			got, err := io.ReadAll(pr)
			if err != nil || !bytes.Equal(got, src) {
				t.Fatalf("pierrec reading ours: n=%d %+v err=%v", n, o, err)
			}
		}
		for _, bs := range []plz4.BlockSize{plz4.Block64Kb, plz4.Block4Mb} {
			var buf bytes.Buffer
			w := plz4.NewWriter(&buf)
			if err := w.Apply(plz4.BlockSizeOption(bs), plz4.ChecksumOption(n%2 == 0), plz4.BlockChecksumOption(n%3 == 0), plz4.ConcurrencyOption(1)); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(src); err != nil || w.Close() != nil {
				t.Fatal("pierrec write", err)
			}
			got, err := DecompressFrame(buf.Bytes())
			if err != nil || !bytes.Equal(got, src) {
				t.Fatalf("ours reading pierrec: n=%d bs=%v err=%v", n, bs, err)
			}
		}
	}
}

func TestConcatenatedAndSkippableFrames(t *testing.T) {
	a, b := []byte("first first first first"), []byte("second second second")
	skip := binary.LittleEndian.AppendUint32(nil, 0x184D2A53)
	skip = binary.LittleEndian.AppendUint32(skip, 3)
	skip = append(skip, 1, 2, 3)
	in := append(append(append(append(CompressFrame(a), skip...), CompressFrameOptions(b, FrameOptions{true, true})...), skip...), CompressFrame(nil)...)
	got, err := DecompressFrame(in)
	if err != nil || string(got) != string(a)+string(b) {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := DecompressFrameLimit(in, len(a)+len(b)-1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("total across frames not bounded: %v", err)
	}
	if got, err := DecompressFrameLimit(in, len(a)+len(b)); err != nil || len(got) != len(a)+len(b) {
		t.Fatalf("exact ceiling refused: %v", err)
	}
}

func TestLinkedBlocks(t *testing.T) {
	// block 2 = literal 'x' then a 4-byte match at offset 8, into block 1.
	linked := []byte{0x10, 'x', 8, 0}
	b1 := blk([]byte("abcdefgh"), true)
	f := mkFrame(0x48, 0x70, nil, [][]byte{b1, blk(linked, false)}, endMark)
	got, err := DecompressFrame(f)
	if err != nil || string(got) != "abcdefghxbcde" {
		t.Fatalf("linked: %q %v", got, err)
	}
	// the same bytes declared independent reach before the block: corrupt.
	f = mkFrame(0x68, 0x70, nil, [][]byte{b1, blk(linked, false)}, endMark)
	if _, err := DecompressFrame(f); err == nil {
		t.Fatal("independent block reached into its predecessor")
	}
	// frames never share a window, even when linked.
	two := append(mkFrame(0x48, 0x70, nil, [][]byte{b1}, endMark), mkFrame(0x48, 0x70, nil, [][]byte{blk(linked, false)}, endMark)...)
	if _, err := DecompressFrame(two); err == nil {
		t.Fatal("second frame saw the first one's output")
	}
}

func TestFrameWithoutContentSize(t *testing.T) {
	f := mkFrame(0x60, 0x40, nil, [][]byte{blk([]byte("abc"), true), blk([]byte{0x30, 'x', 'y', 'z'}, false)}, endMark)
	got, err := DecompressFrame(f)
	if err != nil || string(got) != "abcxyz" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestFrameErrors(t *testing.T) {
	good := CompressFrame([]byte("hello hello hello hello"))
	sum := func(flg byte, blocks [][]byte, tail []byte) []byte { return mkFrame(flg, 0x70, nil, blocks, tail) }
	bomb := append([]byte{0x1F, 'a', 1, 0}, bytes.Repeat([]byte{255}, 300)...)
	bomb = append(bomb, 0)
	stored := blk([]byte("abcd"), true)
	cs := func(p []byte) []byte { return binary.LittleEndian.AppendUint32(nil, xxh32(p, 0)) }
	badHC := append([]byte(nil), good...)
	badHC[14] ^= 1
	badBD := mkFrame(0x68, 0x71, sz(0), nil, endMark)

	cases := []struct {
		name string
		in   []byte
		max  int
		want error
	}{
		{"empty", nil, 1 << 20, ErrFrame},
		{"short magic", []byte{4, 0x22}, 1 << 20, ErrFrame},
		{"bad magic", []byte("bv41\x00\x00\x00\x00"), 1 << 20, ErrFrame},
		{"trailing bytes", append(append([]byte(nil), good...), 1, 2), 1 << 20, ErrFrame},
		{"truncated header", good[:6], 1 << 20, ErrFrame},
		{"truncated header after size flag", good[:14], 1 << 20, ErrFrame},
		{"version 0", sum(0x28, nil, endMark), 1 << 20, ErrFrame},
		{"reserved flg", sum(0x6A, nil, endMark), 1 << 20, ErrFrame},
		{"reserved bd", badBD, 1 << 20, ErrFrame},
		{"dictionary", sum(0x69, nil, endMark), 1 << 20, ErrFrame},
		{"block max id", mkFrame(0x68, 0x30, sz(0), nil, endMark), 1 << 20, ErrFrame},
		{"header checksum", badHC, 1 << 20, ErrFrame},
		{"declared size past ceiling", mkFrame(0x68, 0x70, sz(1<<40), nil, endMark), 1 << 20, ErrTooLarge},
		{"truncated block header", good[:len(good)-2], 1 << 20, ErrFrame},
		{"block past maximum", sum(0x68, [][]byte{binary.LittleEndian.AppendUint32(nil, 1<<30)}, nil), 1 << 20, ErrFrame},
		{"stored block past a 64 KiB maximum", mkFrame(0x68, 0x40, nil, [][]byte{blk(make([]byte, 65537), true)}, endMark), 1 << 24, ErrFrame},
		{"truncated block", sum(0x68, [][]byte{{9, 0, 0, 0, 1, 2}}, nil), 1 << 20, ErrFrame},
		{"truncated block checksum", sum(0x78, [][]byte{stored}, nil), 1 << 20, ErrFrame},
		{"block checksum", sum(0x78, [][]byte{append(append([]byte(nil), stored...), 0, 0, 0, 0)}, endMark), 1 << 20, ErrFrame},
		{"stored past ceiling", sum(0x68, [][]byte{stored}, endMark), 3, ErrTooLarge},
		{"compressed past ceiling", mkFrame(0x68, 0x70, nil, [][]byte{blk(bomb, false)}, endMark), 1000, ErrTooLarge},
		{"compressed past block maximum", mkFrame(0x68, 0x40, nil, [][]byte{blk(bomb, false)}, endMark), 1 << 24, ErrFrame},
		{"corrupt block", sum(0x68, [][]byte{blk([]byte{0xF0}, false)}, endMark), 1 << 20, errCorrupt},
		{"content size mismatch", mkFrame(0x68, 0x70, sz(5), [][]byte{stored}, endMark), 1 << 20, ErrFrame},
		{"truncated content checksum", sum(0x6C, [][]byte{stored}, []byte{0, 0, 0, 0, 1, 2}), 1 << 20, ErrFrame},
		{"content checksum", sum(0x6C, [][]byte{stored}, []byte{0, 0, 0, 0, 0, 0, 0, 0}), 1 << 20, ErrFrame},
		{"truncated skippable", []byte{0x55, 0x2A, 0x4D, 0x18, 1}, 1 << 20, ErrFrame},
		{"skippable past input", []byte{0x50, 0x2A, 0x4D, 0x18, 9, 0, 0, 0, 1}, 1 << 20, ErrFrame},
	}
	// sanity: the pieces the cases are made of are themselves valid
	if got, err := DecompressFrame(sum(0x6C, [][]byte{stored}, append(endMark, cs([]byte("abcd"))...))); err != nil || string(got) != "abcd" {
		t.Fatalf("control frame: %q %v", got, err)
	}
	if got, err := DecompressFrame(sum(0x78, [][]byte{append(append([]byte(nil), stored...), cs([]byte("abcd"))...)}, endMark)); err != nil || string(got) != "abcd" {
		t.Fatalf("control frame with block checksum: %q %v", got, err)
	}
	if got, err := DecompressFrame(mkFrame(0x68, 0x40, nil, [][]byte{blk(make([]byte, 65536), true)}, endMark)); err != nil || len(got) != 65536 {
		t.Fatalf("a block of exactly the maximum: %d %v", len(got), err)
	}
	for _, c := range cases {
		got, err := DecompressFrameLimit(c.in, c.max)
		if got != nil || !errors.Is(err, c.want) {
			t.Errorf("%s: got %d bytes, err=%v, want %v", c.name, len(got), err, c.want)
		}
	}
	if _, err := DecompressFrameLimit(good, -5); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("negative ceiling: %v", err)
	}
}

// A forged content size on a tiny frame must neither allocate what it claims
// nor be believed: a 512 MiB claim inside a 1 GiB ceiling costs a few bytes.
func TestFrameBombAllocatesNothing(t *testing.T) {
	f := mkFrame(0x68, 0x70, sz(1<<29), [][]byte{blk([]byte("abcd"), true)}, endMark)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := DecompressFrame(f)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrFrame) {
		t.Fatalf("err=%v", err)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 1<<20 {
		t.Fatalf("a %d-byte frame allocated %d bytes", len(f), d)
	}
	// 1 KiB of 0xFF extension bytes inside one 4 MiB-max block: refused at the
	// ceiling, long before the 255x expansion is made.
	bomb := append([]byte{0x1F, 'a', 1, 0}, bytes.Repeat([]byte{255}, 1<<16)...)
	bomb = append(bomb, 0)
	f = mkFrame(0x68, 0x70, nil, [][]byte{blk(bomb, false)}, endMark)
	runtime.ReadMemStats(&before)
	_, err = DecompressFrameLimit(f, 1<<16)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrTooLarge) || after.TotalAlloc-before.TotalAlloc > 1<<20 {
		t.Fatalf("err=%v alloc=%d", err, after.TotalAlloc-before.TotalAlloc)
	}
}

func FuzzDecompressFrame(f *testing.F) {
	data := bytes.Repeat([]byte("0123456789"), 40)
	f.Add(CompressFrame(data), 1<<20)
	f.Add(CompressFrameOptions(data, FrameOptions{true, true}), 1<<20)
	f.Add(CompressFrame(nil), 0)
	f.Add(mkFrame(0x48, 0x40, nil, [][]byte{blk([]byte("abcdefgh"), true), blk([]byte{0x10, 'x', 8, 0}, false)}, endMark), 1<<20)
	f.Add(mkFrame(0x68, 0x70, sz(1<<40), nil, endMark), 1<<20)
	f.Add([]byte("\x50\x2a\x4d\x18\x00\x00\x00\x00"), 10)
	f.Fuzz(func(t *testing.T, src []byte, maxOut int) {
		maxOut = max(0, min(maxOut, 1<<22))
		out, err := DecompressFrameLimit(src, maxOut)
		if err == nil && len(out) > maxOut {
			t.Fatalf("returned %d bytes past the %d ceiling", len(out), maxOut)
		}
	})
}

func FuzzFrameRoundTrip(f *testing.F) {
	f.Add([]byte("abcabcabcabcabcabcabc"), true, false)
	f.Add(bytes.Repeat([]byte{9}, 70000), false, true)
	f.Fuzz(func(t *testing.T, src []byte, content, block bool) {
		got, err := DecompressFrame(CompressFrameOptions(src, FrameOptions{content, block}))
		if err != nil || !bytes.Equal(got, src) {
			t.Fatalf("err=%v equal=%v", err, bytes.Equal(got, src))
		}
	})
}

func frameBenchSample() []byte { return sample(rand.New(rand.NewPCG(5, 6)), 32<<20) }

func BenchmarkCompressFrame(b *testing.B) {
	src := frameBenchSample()
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		CompressFrame(src)
	}
}

func BenchmarkDecompressFrame(b *testing.B) {
	src := frameBenchSample()
	f := CompressFrame(src)
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		if _, err := DecompressFrame(f); err != nil {
			b.Fatal(err)
		}
	}
}
