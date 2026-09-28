package lz4_test

// Apple's own encoder, as a witness.
//
// apple_test.go builds bv41/bv4-/bv4$ frames BY HAND. Those tests are worth
// having -- they reach shapes a real encoder may never emit, like a stored
// block next to a compressed one -- but they share an author with the decoder,
// so they can be wrong in exactly the way it is wrong and agree with it.
//
// This asks macOS instead. appleoracle is a thin call into
// compression_encode_buffer with COMPRESSION_LZ4, which is precisely the
// framed stream apple.go documents itself as reading.

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/go-compressions/appleoracle"
	"github.com/go-compressions/lz4"
)

func oracleSample(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	phrase := []byte("the quick brown fox jumps over the lazy dog. ")
	for i := range b {
		if r.Intn(3) == 0 {
			b[i] = byte(r.Intn(256))
		} else {
			b[i] = phrase[i%len(phrase)]
		}
	}
	return b
}

func TestDecompressAppleReadsWhatAppleWrote(t *testing.T) {
	if !appleoracle.Available() {
		t.Skip("no libcompression: this build cannot ask Apple")
	}
	// Sizes around Apple's block boundary, where one frame becomes several and
	// a match may reach back into the previous block's window -- the property
	// apple.go calls the defining one.
	sizes := []int{0, 1, 1024, 65535, 65536, 65537, 131072, 300000}
	asked := 0
	for _, n := range sizes {
		src := oracleSample(n, int64(n)+1)
		enc, ok := appleoracle.Encode(appleoracle.LZ4, src)
		if !ok {
			// The platform declines inputs it cannot shrink; that is not a
			// failure of ours. The floor below keeps this from swallowing a
			// real one.
			t.Logf("Apple declined to encode %d bytes", n)
			continue
		}
		asked++
		got, err := lz4.DecompressApple(enc)
		if err != nil {
			t.Errorf("%d bytes: DecompressApple on a stream Apple wrote: %v", n, err)
			continue
		}
		if !bytes.Equal(got, src) {
			t.Errorf("%d bytes: decoded %d bytes that differ from the input", n, len(got))
		}
	}
	// A population floor: a run where the oracle served nothing reports no
	// failures either, and would be indistinguishable from this test's absence.
	if asked < 5 {
		t.Errorf("only %d of %d sizes were actually witnessed", asked, len(sizes))
	}
}

// TestAHighlyCompressibleStreamCrossesBlocks: a run of one byte is what makes
// Apple emit many small blocks and lean on the shared window between them.
func TestAHighlyCompressibleStreamCrossesBlocks(t *testing.T) {
	if !appleoracle.Available() {
		t.Skip("no libcompression: this build cannot ask Apple")
	}
	src := bytes.Repeat([]byte("abcd"), 1<<18) // 1 MiB, far past one block
	enc, ok := appleoracle.Encode(appleoracle.LZ4, src)
	if !ok {
		t.Fatal("Apple declined to encode a 1 MiB run")
	}
	if n := bytes.Count(enc, []byte("bv4")); n < 2 {
		t.Fatalf("only %d block magics: this stream does not cross a block, "+
			"so it cannot show the property it was chosen for", n)
	}
	got, err := lz4.DecompressApple(enc)
	if err != nil {
		t.Fatalf("DecompressApple: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Error("the 1 MiB round trip through Apple's encoder did not come back")
	}
}

// TestTheOracleIsNotAgreeingWithAnything is the control on the control. If
// DecompressApple accepted a damaged stream just as readily, the two tests
// above would pass for a decoder that ignores its input.
func TestTheOracleIsNotAgreeingWithAnything(t *testing.T) {
	if !appleoracle.Available() {
		t.Skip("no libcompression: this build cannot ask Apple")
	}
	src := oracleSample(200000, 42)
	enc, ok := appleoracle.Encode(appleoracle.LZ4, src)
	if !ok {
		t.Fatal("Apple declined to encode")
	}
	damaged := append([]byte(nil), enc...)
	damaged[len(damaged)/2] ^= 0xff
	got, err := lz4.DecompressApple(damaged)
	if err == nil && bytes.Equal(got, src) {
		t.Error("a flipped byte decoded back to the original: this decoder is " +
			"not reading the bytes it was given")
	}
}
