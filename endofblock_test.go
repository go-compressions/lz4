package lz4

import (
	"math/rand/v2"
	"testing"
)

// lastMatchStart walks a block's token stream and returns where the last match
// starts in the decoded output, where the final literal run starts, and whether
// the block is well formed.
func lastMatchStart(block []byte) (matchStart, lastLitStart int, ok bool) {
	ip, op := 0, 0
	matchStart = -1
	for ip < len(block) {
		token := int(block[ip])
		ip++
		ll := token >> 4
		if ll == 15 {
			for {
				if ip >= len(block) {
					return 0, 0, false
				}
				b := int(block[ip])
				ip++
				ll += b
				if b != 255 {
					break
				}
			}
		}
		op += ll
		ip += ll
		if ip == len(block) {
			return matchStart, op - ll, true
		}
		ip += 2
		ml := token&15 + minMatch
		if token&15 == 15 {
			for {
				b := int(block[ip])
				ip++
				ml += b
				if b != 255 {
					break
				}
			}
		}
		matchStart = op
		op += ml
	}
	return 0, 0, false
}

// The LZ4 block format's end-of-block conditions: the last match starts at
// least 12 bytes before the end of the block, and the last 5 bytes are literals.
// liblz4's LZ4_decompress_safe refuses a block that breaks them when the output
// buffer is exactly the decoded size, which is how the kernel calls it. The
// encoder's probes at ip+1 and ip+2 used to start a match up to 10 bytes from
// the end.
func TestEncoderEndOfBlockConditions(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 32))
	for trial := 0; trial < 60000; trial++ {
		n := 13 + r.IntN(300)
		src := make([]byte, n)
		// A small alphabet makes matches likely, including near the end.
		for i := range src {
			src[i] = byte('a' + r.IntN(1+r.IntN(4)))
		}
		out := CompressBlock(src)
		ms, litStart, ok := lastMatchStart(out)
		if !ok {
			t.Fatalf("trial %d (n=%d): malformed block", trial, n)
		}
		if ms >= 0 && ms > n-12 {
			t.Fatalf("trial %d (n=%d): the last match starts at %d, past len-12 = %d", trial, n, ms, n-12)
		}
		if n-litStart < 5 {
			t.Fatalf("trial %d (n=%d): the final literal run is %d bytes, want at least 5", trial, n, n-litStart)
		}
		back, err := DecompressBlock(out, n)
		if err != nil || string(back) != string(src) {
			t.Fatalf("trial %d (n=%d): round trip: %v", trial, n, err)
		}
	}
}
