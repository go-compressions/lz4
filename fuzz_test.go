package lz4

import (
	"bytes"
	"testing"
)

// FuzzDecompressBlock: no input panics, and nothing past the ceiling is ever
// returned. The ceiling is small so that the fuzzer's expanding blocks are
// refused instead of filling memory.
func FuzzDecompressBlock(f *testing.F) {
	f.Add(CompressBlock([]byte("hello hello hello hello")), 0)
	f.Add(CompressBlock(bytes.Repeat([]byte{1}, 1000)), 100)
	f.Add([]byte{0x1F, 'a', 1, 0, 255, 255, 0}, 0)
	f.Add([]byte{0xF0, 255, 3}, 7)
	f.Fuzz(func(t *testing.T, src []byte, hint int) {
		const ceiling = 1 << 20
		out, err := DecompressBlockLimit(src, hint, ceiling)
		if err == nil && len(out) > ceiling {
			t.Fatalf("returned %d bytes past the %d ceiling", len(out), ceiling)
		}
	})
}

// FuzzDecompressApple: the same for the frame, whose headers carry sizes.
func FuzzDecompressApple(f *testing.F) {
	data := bytes.Repeat([]byte("0123456789"), 40)
	f.Add(buildAppleFrame(storedBlock(data), compressedBlock(data)), 1<<20)
	f.Add(buildAppleFrame(compressedBlock(bytes.Repeat([]byte{7}, 70000))), 1<<20)
	f.Add([]byte("bv41\xff\xff\xff\xff\xff\xff\xff\xff"), 1<<20)
	f.Add([]byte("bv4-\xff\xff\xff\xff"), 100)
	f.Fuzz(func(t *testing.T, src []byte, maxOut int) {
		maxOut = max(0, min(maxOut, 1<<22))
		out, err := DecompressAppleLimit(src, maxOut)
		if err == nil && len(out) > maxOut {
			t.Fatalf("returned %d bytes past the %d ceiling", len(out), maxOut)
		}
	})
}

// FuzzRoundTrip: the encoder's output is always decoded back to its input.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("abcabcabcabcabcabc"))
	f.Add(bytes.Repeat([]byte{0}, 5000))
	f.Fuzz(func(t *testing.T, in []byte) {
		got, err := DecompressBlock(CompressBlock(in), len(in))
		if err != nil || !bytes.Equal(got, in) {
			t.Fatalf("round trip of %d bytes: err=%v", len(in), err)
		}
	})
}
