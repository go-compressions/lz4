package lz4

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"os"
	"testing"
)

// testdata/liblz4/{text,runs,mixed}.blk hold the first three 32 KiB blocks of
// three deterministic corpora, compressed by liblz4 1.11 (LZ4_compress_default),
// as records [u32 rawLen][u32 compLen][bytes]. The raw corpora are regenerated
// here (one PCG stream each, seeds (42, 43+i)); the decoders must give back
// exactly those bytes: liblz4 is the oracle for the format as the reference
// encoder writes it, not only for what this package's own encoder emits.
const liblz4Total = 8 << 20

func genText(r *rand.Rand) []byte {
	vocab := make([][]byte, 6000)
	for i := range vocab {
		w := make([]byte, 3+r.IntN(8))
		for j := range w {
			w[j] = byte('a' + r.IntN(26))
		}
		vocab[i] = w
	}
	var b []byte
	for len(b) < liblz4Total {
		k := int(float64(len(vocab)) * r.Float64() * r.Float64() * r.Float64())
		b = append(b, vocab[k]...)
		if r.IntN(12) == 0 {
			b = append(b, '\n')
		} else {
			b = append(b, ' ')
		}
	}
	return b[:liblz4Total]
}

func genRuns(r *rand.Rand) []byte {
	b := make([]byte, liblz4Total)
	for i := range b {
		b[i] = byte('A' + (i/4096)%26)
	}
	for i := 0; i < liblz4Total/512; i++ {
		b[r.IntN(liblz4Total)] = byte(r.Uint32())
	}
	return b
}

func genMixed(r *rand.Rand) []byte {
	b := make([]byte, 0, liblz4Total)
	for len(b) < liblz4Total {
		if len(b) > 64 && r.IntN(3) == 0 {
			n := 4 + r.IntN(28)
			off := 1 + r.IntN(min(len(b), 60000))
			for k := 0; k < n; k++ {
				b = append(b, b[len(b)-off])
			}
		} else {
			n := 1 + r.IntN(24)
			for k := 0; k < n; k++ {
				b = append(b, byte(r.Uint32()))
			}
		}
	}
	return b[:liblz4Total]
}

func TestDecodersAgainstLiblz4(t *testing.T) {
	for i, c := range []struct {
		name string
		gen  func(*rand.Rand) []byte
	}{{"text", genText}, {"runs", genRuns}, {"mixed", genMixed}} {
		raw := c.gen(rand.New(rand.NewPCG(42, uint64(43+i))))
		blk, err := os.ReadFile("testdata/liblz4/" + c.name + ".blk")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for ; len(blk) > 0; n++ {
			rawLen := int(binary.LittleEndian.Uint32(blk))
			compLen := int(binary.LittleEndian.Uint32(blk[4:]))
			comp := blk[8 : 8+compLen]
			blk = blk[8+compLen:]
			want := raw[n*rawLen : (n+1)*rawLen]

			dst := make([]byte, rawLen)
			m, err := DecodeBlock(dst, comp)
			if err != nil || !bytes.Equal(dst[:m], want) {
				t.Fatalf("%s block %d: DecodeBlock n=%d err=%v", c.name, n, m, err)
			}
			got, err := DecompressBlock(comp, rawLen)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s block %d: DecompressBlock err=%v", c.name, n, err)
			}
		}
		if n != 3 {
			t.Fatalf("%s: %d blocks in the fixture, want 3", c.name, n)
		}
	}
}
