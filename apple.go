package lz4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
)

// Apple's LZ4 frame format (the byte stream produced by macOS's Compression
// framework with COMPRESSION_LZ4, and by Apple tooling such as Tart) is a
// sequence of 4-byte-magic blocks:
//
//	"bv41" u32 rawSize u32 compSize  <compSize bytes: a standard LZ4 block>
//	"bv4-" u32 rawSize               <rawSize bytes: stored, uncompressed>
//	"bv4$"                           end-of-stream marker
//
// All integers are little-endian. Crucially — and unlike an isolated LZ4 block —
// a compressed block's matches may reference the previous block's output: the
// blocks share one 64 KiB sliding window (LZ4 offsets are 16-bit). Decoders
// must therefore carry the trailing window across block boundaries, which is
// what DecompressAppleStream does via decodeInto's dictionary support.
//
// This mirrors the "bvx*" frame that github.com/go-compressions/lzfse decodes
// for LZFSE/LZVN; the "bv4*" frame is the LZ4 member of the same family.
var (
	appleMagicCompressed   = [4]byte{'b', 'v', '4', '1'}
	appleMagicUncompressed = [4]byte{'b', 'v', '4', '-'}
	appleMagicEnd          = [4]byte{'b', 'v', '4', '$'}
)

// ErrAppleFrame reports a malformed Apple LZ4 frame (unknown block magic,
// truncated header/payload, or a block whose decoded length disagrees with its
// declared raw size).
var ErrAppleFrame = errors.New("lz4: malformed Apple LZ4 frame")

// DefaultMaxBlock is the largest raw block [DecompressAppleStream] accepts:
// 16 MiB. The block header declares its raw size in 32 bits, and the decoder
// holds one whole block in memory. Apple's encoder emits blocks of 64 KiB (the
// window; measured with compression_encode_buffer on 64 MiB inputs: every
// block's rawSize <= 65536), so the default is 256 times what it writes;
// [DecompressAppleStreamLimit] takes another.
const DefaultMaxBlock = 1 << 24

// readChunk is the first allocation made for a block whose size a header
// declares; the buffer then doubles only as bytes actually arrive.
const readChunk = 1 << 16

// readBlock reads exactly n bytes from r. It allocates in proportion to the
// bytes the stream delivers, not to n: a header declaring 16 MiB over a
// truncated stream costs readChunk, not 16 MiB.
func readBlock(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, 0, min(n, readChunk))
	for len(buf) < n {
		want := min(n-len(buf), max(readChunk, len(buf)))
		buf = slices.Grow(buf, want)
		m, err := io.ReadFull(r, buf[len(buf):len(buf)+want])
		buf = buf[:len(buf)+m]
		if err != nil {
			return nil, err
		}
	}
	return buf, nil
}

// DecompressAppleStream decodes Apple's LZ4 frame format from src, writing the
// decompressed bytes to dst, and returns the number of bytes written. It streams
// block by block, holding at most one block plus the 64 KiB cross-block window
// in memory, so it is safe for multi-gigabyte inputs (e.g. Tart disk layers).
//
// A block declaring more than [DefaultMaxBlock] raw bytes is refused with
// [ErrTooLarge]; see [DecompressAppleStreamLimit].
//
// A well-formed frame ends with a "bv4$" marker; a stream that ends before one
// (including an empty stream) is reported as ErrAppleFrame.
func DecompressAppleStream(dst io.Writer, src io.Reader) (int64, error) {
	return DecompressAppleStreamLimit(dst, src, DefaultMaxBlock)
}

// DecompressAppleStreamLimit is [DecompressAppleStream] refusing, with
// [ErrTooLarge], any block that declares more than maxBlock raw bytes. The
// total is not limited (the stream is not held); the memory held is one block,
// whose size is checked against maxBlock, against what its compressed payload
// can expand to (LZ4 yields at most 255 bytes per input byte), and, for the
// payload itself, against the bytes that actually arrive.
func DecompressAppleStreamLimit(dst io.Writer, src io.Reader, maxBlock int) (int64, error) {
	return appleStream(dst, src, maxBlock, math.MaxInt64)
}

// DecompressAppleLimit is [DecompressApple] refusing, with [ErrTooLarge], a
// frame that would decompress past maxOut bytes in total (or any block past
// [DefaultMaxBlock]).
func DecompressAppleLimit(src []byte, maxOut int) ([]byte, error) {
	var out sliceWriter
	if _, err := appleStream(&out, &byteReader{b: src}, DefaultMaxBlock, int64(max(0, maxOut))); err != nil {
		return nil, err
	}
	return out, nil
}

func appleStream(dst io.Writer, src io.Reader, maxBlock int, maxTotal int64) (int64, error) {
	var (
		written int64
		window  []byte // up to winSize bytes of previously decoded output
		magic   [4]byte
		hdr     [8]byte
	)
	// fits reports whether a block of n raw bytes is within both ceilings; a
	// uint64 compare, so a 32-bit field cannot go negative on a 32-bit build.
	fits := func(n uint64) bool {
		return n <= uint64(max(0, maxBlock)) && n <= uint64(maxTotal-written)
	}
	for {
		if _, err := io.ReadFull(src, magic[:]); err != nil {
			return written, fmt.Errorf("%w: reading block magic: %w", ErrAppleFrame, err)
		}
		switch magic {
		case appleMagicEnd:
			return written, nil

		case appleMagicCompressed:
			if _, err := io.ReadFull(src, hdr[:]); err != nil {
				return written, fmt.Errorf("%w: reading compressed header: %w", ErrAppleFrame, err)
			}
			rawSize := uint64(binary.LittleEndian.Uint32(hdr[0:4]))
			compSize := uint64(binary.LittleEndian.Uint32(hdr[4:8]))
			if !fits(rawSize) {
				return written, ErrTooLarge
			}
			// The payload must be able to produce rawSize (<= 255 bytes per
			// input byte) and must not be longer than any encoder makes it:
			// each sequence costs at most 3 bytes of overhead for >= 4 output
			// bytes, plus one length byte per 255, so compSize stays below
			// rawSize + rawSize/64 + 64. Both checks run before anything is
			// allocated from either field.
			if rawSize > compSize*maxExpansion || compSize > rawSize+rawSize/64+64 {
				return written, fmt.Errorf("%w: block header declares raw %d, compressed %d bytes", ErrAppleFrame, rawSize, compSize)
			}
			payload, err := readBlock(src, int(compSize))
			if err != nil {
				return written, fmt.Errorf("%w: reading compressed payload: %w", ErrAppleFrame, err)
			}
			// Prefix the previous window so this block's matches can reference it,
			// then decode the block immediately after it. The block may not grow
			// past the window plus its declared size.
			maxEnd := len(window) + int(rawSize)
			buf := make([]byte, maxEnd+decodeSlack)
			copy(buf, window)
			buf, di, err := decodeInto(buf, len(window), payload, maxEnd)
			if errors.Is(err, ErrTooLarge) {
				return written, fmt.Errorf("%w: block decodes past its declared %d bytes", ErrAppleFrame, rawSize)
			}
			if err != nil {
				return written, err
			}
			out := buf[len(window):di]
			if len(out) != int(rawSize) {
				return written, fmt.Errorf("%w: block decoded to %d bytes, header says %d", ErrAppleFrame, len(out), rawSize)
			}
			if _, err := dst.Write(out); err != nil {
				return written, err
			}
			written += int64(len(out))
			window = slideWindow(window, out)

		case appleMagicUncompressed:
			if _, err := io.ReadFull(src, hdr[:4]); err != nil {
				return written, fmt.Errorf("%w: reading stored header: %w", ErrAppleFrame, err)
			}
			rawSize := uint64(binary.LittleEndian.Uint32(hdr[0:4]))
			if !fits(rawSize) {
				return written, ErrTooLarge
			}
			raw, err := readBlock(src, int(rawSize))
			if err != nil {
				return written, fmt.Errorf("%w: reading stored payload: %w", ErrAppleFrame, err)
			}
			if _, err := dst.Write(raw); err != nil {
				return written, err
			}
			written += int64(len(raw))
			window = slideWindow(window, raw)

		default:
			return written, fmt.Errorf("%w: unknown block magic %q", ErrAppleFrame, magic[:])
		}
	}
}

// DecompressApple decodes an in-memory Apple LZ4 frame and returns the
// decompressed bytes, at most [DefaultMaxOutput] of them. For large inputs
// prefer DecompressAppleStream, which does not buffer the whole output.
func DecompressApple(src []byte) ([]byte, error) {
	return DecompressAppleLimit(src, DefaultMaxOutput)
}

// slideWindow returns the trailing winSize bytes of window++out in a fresh
// buffer, so the returned slice never keeps an oversized decode buffer alive.
func slideWindow(window, out []byte) []byte {
	if len(out) >= winSize {
		nw := make([]byte, winSize)
		copy(nw, out[len(out)-winSize:])
		return nw
	}
	keep := winSize - len(out)
	if keep > len(window) {
		keep = len(window)
	}
	nw := make([]byte, 0, keep+len(out))
	nw = append(nw, window[len(window)-keep:]...)
	nw = append(nw, out...)
	return nw
}

// sliceWriter is a minimal in-memory io.Writer used by DecompressApple.
type sliceWriter []byte

func (w *sliceWriter) Write(p []byte) (int, error) {
	*w = append(*w, p...)
	return len(p), nil
}

// byteReader is a minimal io.Reader over a byte slice (like bytes.Reader, but
// without the extra imports) used by DecompressApple.
type byteReader struct {
	b []byte
	i int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
