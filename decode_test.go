package lz4

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"

	pierrec "github.com/pierrec/lz4/v4"
)

func decodeCorpora(r *rand.Rand) map[string][]byte {
	text := func(n int) []byte {
		var b []byte
		for len(b) < n {
			w := make([]byte, 3+r.IntN(8))
			for j := range w {
				w[j] = byte('a' + r.IntN(5))
			}
			b = append(b, w...)
			b = append(b, ' ')
		}
		return b[:n]
	}
	runs := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('A' + (i/300)%26)
		}
		return b
	}
	noise := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		return b
	}
	out := map[string][]byte{}
	for _, n := range []int{0, 1, 4, 11, 12, 13, 17, 31, 64, 255, 256, 300, 4096, 65535, 65536, 131072, 300000} {
		out["text"+string(rune('0'+n%10))+itoa(n)] = text(n)
		out["runs"+itoa(n)] = runs(n)
		out["noise"+itoa(n)] = noise(n)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for ; n > 0; n /= 10 {
		s = append([]byte{byte('0' + n%10)}, s...)
	}
	return string(s)
}

// DecodeBlock equals DecompressBlock and pierrec on blocks from this package's
// encoder, with a destination of exactly the output size and with slack.
func TestDecodeBlockMatches(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for name, raw := range decodeCorpora(r) {
		comp := CompressBlock(raw)
		want, err := DecompressBlock(comp, len(raw))
		if err != nil || !bytes.Equal(want, raw) {
			t.Fatalf("%s: DecompressBlock: %v", name, err)
		}
		for _, extra := range []int{0, 1, 16, 4096} {
			dst := make([]byte, len(raw)+extra)
			n, err := DecodeBlock(dst, comp)
			if err != nil || n != len(raw) || !bytes.Equal(dst[:n], raw) {
				t.Fatalf("%s +%d: n=%d err=%v", name, extra, n, err)
			}
		}
		pd := make([]byte, len(raw))
		if pn, perr := pierrec.UncompressBlock(comp, pd); perr != nil || !bytes.Equal(pd[:pn], raw) {
			t.Fatalf("%s: pierrec disagrees: %v", name, perr)
		}
	}
}

// A destination one byte too small is refused, and nothing past it is touched.
func TestDecodeBlockShortDst(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for name, raw := range decodeCorpora(r) {
		if len(raw) == 0 {
			continue
		}
		comp := CompressBlock(raw)
		buf := make([]byte, len(raw)+32)
		for i := range buf {
			buf[i] = 0xEE
		}
		_, err := DecodeBlock(buf[:len(raw)-1], comp)
		if !errors.Is(err, ErrShortDst) {
			t.Fatalf("%s: err = %v, want ErrShortDst", name, err)
		}
		for i := len(raw) - 1; i < len(buf); i++ {
			if buf[i] != 0xEE {
				t.Fatalf("%s: wrote past dst at %d", name, i)
			}
		}
	}
}

func FuzzDecodeBlock(f *testing.F) {
	f.Add([]byte{0x00}, 8)
	f.Add(CompressBlock(bytes.Repeat([]byte("abcabcabcabc"), 40)), 480)
	f.Add([]byte{0x1F, 'a', 0x01, 0x00, 0xFF, 0xFF, 0x10}, 1000)
	f.Fuzz(func(t *testing.T, src []byte, k int) {
		if k < 0 || k > 1<<20 {
			return
		}
		dst := make([]byte, k+7)
		for i := range dst {
			dst[i] = 0xA5
		}
		n, err := DecodeBlock(dst[:k], src)
		for i := k; i < len(dst); i++ {
			if dst[i] != 0xA5 {
				t.Fatalf("wrote past dst at %d", i)
			}
		}
		want, werr := DecompressBlockLimit(src, k, k)
		switch {
		case err == nil && werr == nil:
			if !bytes.Equal(dst[:n], want) {
				t.Fatalf("outputs differ (%d vs %d bytes)", n, len(want))
			}
		case err == nil && werr != nil:
			t.Fatalf("DecodeBlock accepted what DecompressBlockLimit refused: %v", werr)
		case err != nil && werr == nil:
			t.Fatalf("DecodeBlock refused what DecompressBlockLimit decoded: %v", err)
		}
	})
}

func BenchmarkDecodeBlock(b *testing.B) {
	r := rand.New(rand.NewPCG(5, 6))
	raw := decodeCorpora(r)["text0131072"]
	comp := CompressBlock(raw)
	dst := make([]byte, len(raw))
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		if _, err := DecodeBlock(dst, comp); err != nil {
			b.Fatal(err)
		}
	}
}
