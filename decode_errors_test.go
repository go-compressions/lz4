package lz4

import (
	"bytes"
	"errors"
	"testing"
)

// Every refusal of DecodeBlock, with the assembly loop and without it.
func TestDecodeBlockRefusals(t *testing.T) {
	pad := bytes.Repeat([]byte{0x77}, 64)
	cases := []struct {
		name string
		src  []byte
		dst  int
		want error
	}{
		{"literal length cut", []byte{0xF0}, 64, errCorrupt},
		{"literal length past src", []byte{0xF0, 255, 255, 255, 255}, 64, errCorrupt},
		{"literals past src", []byte{0x50, 'a'}, 64, errCorrupt},
		{"literals past dst", []byte{0x30, 'a', 'b', 'c'}, 2, ErrShortDst},
		{"offset cut", []byte{0x10, 'a', 0x01}, 64, errCorrupt},
		{"offset zero", []byte{0x10, 'a', 0x00, 0x00, 0x00}, 64, errCorrupt},
		{"offset beyond output", []byte{0x10, 'a', 0x05, 0x00, 0x00}, 64, errCorrupt},
		{"match length cut", []byte{0x1F, 'a', 0x01, 0x00}, 64, errCorrupt},
		{"match length past dst", []byte{0x1F, 'a', 0x01, 0x00, 0xFF, 0xFF, 0xFF, 0x00}, 100, ErrShortDst},
		{"match past dst", []byte{0x11, 'a', 0x01, 0x00, 'b'}, 3, ErrShortDst},
		// The same faults far from the buffer ends, where the Go fast path runs.
		{"fast path offset zero", append([]byte{0x10, 'a', 0x00, 0x00}, pad...), 200, errCorrupt},
		{"fast path offset beyond output", append([]byte{0x10, 'a', 0x09, 0x00}, pad...), 200, errCorrupt},
	}
	for _, c := range cases {
		for _, asm := range []bool{true, false} {
			dst := make([]byte, c.dst)
			if _, err := decodeBlock(dst, c.src, asm); !errors.Is(err, c.want) {
				t.Errorf("%s (asm %v): err = %v, want %v", c.name, asm, err, c.want)
			}
		}
	}
}

// DecodeBlock on both paths equals DecompressBlock on every corpus block.
func TestDecodeBlockGoPathMatches(t *testing.T) {
	for name, raw := range decodeCorpora(newRand(9)) {
		comp := CompressBlock(raw)
		for _, asm := range []bool{true, false} {
			dst := make([]byte, len(raw))
			n, err := decodeBlock(dst, comp, asm)
			if err != nil || !bytes.Equal(dst[:n], raw) {
				t.Fatalf("%s (asm %v): n=%d err=%v", name, asm, n, err)
			}
		}
	}
}
