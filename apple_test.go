package lz4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// appleBlock is one block of a synthetic Apple LZ4 frame.
type appleBlock struct {
	magic [4]byte
	// For compressed blocks: rawSize/compSize headers and the LZ4 payload.
	rawSize  uint32
	compSize uint32
	payload  []byte
	// For uncompressed blocks: the stored bytes (rawSize is len(stored)).
	stored []byte
}

// buildAppleFrame serialises blocks followed by a "bv4$" end marker.
func buildAppleFrame(blocks ...appleBlock) []byte {
	var b bytes.Buffer
	le := func(v uint32) {
		var x [4]byte
		binary.LittleEndian.PutUint32(x[:], v)
		b.Write(x[:])
	}
	for _, blk := range blocks {
		b.Write(blk.magic[:])
		switch blk.magic {
		case appleMagicCompressed:
			le(blk.rawSize)
			le(blk.compSize)
			b.Write(blk.payload)
		case appleMagicUncompressed:
			le(uint32(len(blk.stored)))
			b.Write(blk.stored)
		}
	}
	b.Write(appleMagicEnd[:])
	return b.Bytes()
}

// compressedBlock builds a bv41 block from a standalone LZ4 block of src.
func compressedBlock(src []byte) appleBlock {
	payload := CompressBlock(src)
	return appleBlock{
		magic:    appleMagicCompressed,
		rawSize:  uint32(len(src)),
		compSize: uint32(len(payload)),
		payload:  payload,
	}
}

func storedBlock(src []byte) appleBlock {
	return appleBlock{magic: appleMagicUncompressed, stored: src}
}

func TestApple_RoundTripStoredAndCompressed(t *testing.T) {
	part1 := []byte("the quick brown fox jumps over the lazy dog. ")
	part2 := bytes.Repeat([]byte("compress me compress me "), 500) // multi-block-ish
	frame := buildAppleFrame(storedBlock(part1), compressedBlock(part2))
	want := append(append([]byte{}, part1...), part2...)

	got, err := DecompressApple(frame)
	if err != nil {
		t.Fatalf("DecompressApple: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

// TestApple_CrossBlockWindow exercises the defining property of the format: a
// compressed block whose match references bytes decoded in the *previous*
// block (the shared 64 KiB window). The hand-built block1 emits one literal
// 'X' then a 4-byte match at offset 17, which points at the start of block0's
// output.
func TestApple_CrossBlockWindow(t *testing.T) {
	block0 := []byte("HELLOWORLD123456") // 16 bytes, stored
	// block1 LZ4 payload: token 0x10 (1 literal, match nibble 0 => match len 4),
	// literal 'X', offset = 17 (little-endian) => match source at di-17 = 0.
	payload := []byte{0x10, 'X', 17, 0x00}
	block1 := appleBlock{
		magic:    appleMagicCompressed,
		rawSize:  5, // 'X' + 4 copied bytes
		compSize: uint32(len(payload)),
		payload:  payload,
	}
	frame := buildAppleFrame(storedBlock(block0), block1)

	got, err := DecompressApple(frame)
	if err != nil {
		t.Fatalf("DecompressApple: %v", err)
	}
	want := append(append([]byte{}, block0...), []byte("XHELL")...)
	if !bytes.Equal(got, want) {
		t.Fatalf("cross-block mismatch: got %q, want %q", got, want)
	}
}

// TestApple_LargeStoredWindowSlide drives slideWindow's len(out) >= winSize
// branch with a stored block bigger than the 64 KiB window.
func TestApple_LargeStoredWindowSlide(t *testing.T) {
	big := bytes.Repeat([]byte{0xA5}, winSize+1000)
	frame := buildAppleFrame(storedBlock(big), storedBlock([]byte("tail")))
	got, err := DecompressApple(frame)
	if err != nil {
		t.Fatalf("DecompressApple: %v", err)
	}
	if len(got) != len(big)+4 {
		t.Fatalf("length = %d, want %d", len(got), len(big)+4)
	}
}

func TestApple_EmptyStreamIsMalformed(t *testing.T) {
	if _, err := DecompressApple(nil); !errors.Is(err, ErrAppleFrame) {
		t.Fatalf("empty stream: err = %v, want ErrAppleFrame", err)
	}
}

func TestApple_UnknownMagic(t *testing.T) {
	if _, err := DecompressApple([]byte("bogus")); !errors.Is(err, ErrAppleFrame) {
		t.Fatalf("unknown magic: err = %v, want ErrAppleFrame", err)
	}
}

func TestApple_TruncatedHeaders(t *testing.T) {
	cases := map[string][]byte{
		"trunc compressed header": append(append([]byte{}, appleMagicCompressed[:]...), 0x00, 0x01),
		"trunc compressed payload": func() []byte {
			b := append([]byte{}, appleMagicCompressed[:]...)
			var h [8]byte
			binary.LittleEndian.PutUint32(h[0:4], 10) // rawSize
			binary.LittleEndian.PutUint32(h[4:8], 10) // compSize, but no payload
			return append(b, h[:]...)
		}(),
		"trunc stored header":  append(append([]byte{}, appleMagicUncompressed[:]...), 0x00),
		"trunc stored payload": append(append(append([]byte{}, appleMagicUncompressed[:]...), 0x05, 0, 0, 0), 'a'),
	}
	for name, in := range cases {
		if _, err := DecompressApple(in); !errors.Is(err, ErrAppleFrame) {
			t.Errorf("%s: err = %v, want ErrAppleFrame", name, err)
		}
	}
}

func TestApple_CorruptCompressedBlock(t *testing.T) {
	// A bv41 payload that is a corrupt LZ4 block (literal length runs past end).
	payload := []byte{0x30, 'a'} // says 3 literals but only 1 present
	blk := appleBlock{magic: appleMagicCompressed, rawSize: 3, compSize: uint32(len(payload)), payload: payload}
	frame := buildAppleFrame(blk)
	if _, err := DecompressApple(frame); !errors.Is(err, errCorrupt) {
		t.Fatalf("corrupt block: err = %v, want errCorrupt", err)
	}
}

func TestApple_SizeMismatch(t *testing.T) {
	// A valid LZ4 payload but a rawSize header that disagrees with it.
	payload := CompressBlock([]byte("hello"))
	blk := appleBlock{magic: appleMagicCompressed, rawSize: 99, compSize: uint32(len(payload)), payload: payload}
	frame := buildAppleFrame(blk)
	_, err := DecompressApple(frame)
	if !errors.Is(err, ErrAppleFrame) {
		t.Fatalf("size mismatch: err = %v, want ErrAppleFrame", err)
	}
}

// failWriter fails after allowing n bytes through.
type failWriter struct {
	remaining int
}

func (w *failWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, errors.New("write failed")
	}
	w.remaining -= len(p)
	return len(p), nil
}

func TestApple_WriteErrors(t *testing.T) {
	compFrame := buildAppleFrame(compressedBlock([]byte("hello world")))
	if _, err := DecompressAppleStream(&failWriter{remaining: 0}, bytes.NewReader(compFrame)); err == nil {
		t.Fatal("expected write error on compressed block")
	}
	storedFrame := buildAppleFrame(storedBlock([]byte("hello world")))
	if _, err := DecompressAppleStream(&failWriter{remaining: 0}, bytes.NewReader(storedFrame)); err == nil {
		t.Fatal("expected write error on stored block")
	}
}

func TestApple_StreamReturnsWrittenCount(t *testing.T) {
	data := []byte("stream me")
	var buf bytes.Buffer
	n, err := DecompressAppleStream(&buf, bytes.NewReader(buildAppleFrame(storedBlock(data))))
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n != int64(len(data)) || !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("n=%d out=%q", n, buf.Bytes())
	}
}

func TestApple_ByteReaderEOF(t *testing.T) {
	r := &byteReader{b: []byte("ab")}
	p := make([]byte, 8)
	n, err := r.Read(p)
	if n != 2 || err != nil {
		t.Fatalf("first read: n=%d err=%v", n, err)
	}
	if _, err := r.Read(p); err != io.EOF {
		t.Fatalf("second read: err=%v, want EOF", err)
	}
}
