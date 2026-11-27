package lz4

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The standard LZ4 frame format (lz4_Frame_format.md), magic 0x184D2204: the
// container lz4(1), U-Boot's ulz4fn (uImage -C lz4) and every LZ4 library read.
// It is not the Apple "bv41" container of [DecompressApple].

// ErrFrame reports a malformed, truncated or unsupported LZ4 frame.
var ErrFrame = errors.New("lz4: malformed frame")

const (
	frameMagic         = 0x184D2204
	frameSkippableLo   = 0x184D2A50 // ..0x184D2A5F: skippable frames
	frameSkippableMask = 0xFFFFFFF0

	// What CompressFrame writes. FLG 0x68: version 01, block-independent, no
	// block checksum, content size present, no content checksum, no dictionary
	// ID. BD 0x70: 4 MiB maximum block. These are exactly what U-Boot's ulz4fn
	// accepts (it refuses linked blocks and reserved bits, skips the header
	// checksum and stops at the end mark).
	frameFLG      = 0x68
	frameBD       = 0x70
	frameBlockMax = 4 << 20

	flgBlockChecksum   = 0x10
	flgContentSize     = 0x08
	flgContentChecksum = 0x04
	flgIndependent     = 0x20
	frameStored        = 0x80000000
)

// FrameOptions selects the optional checksums of [CompressFrameOptions].
type FrameOptions struct {
	// ContentChecksum appends an XXH32 of the whole content after the end mark.
	// U-Boot's ulz4fn ignores it; lz4(1) verifies it.
	ContentChecksum bool
	// BlockChecksum follows every block with an XXH32 of its stored bytes.
	BlockChecksum bool
}

// CompressFrame compresses src into one standard LZ4 frame: independent 4 MiB
// blocks, the content size in the header, no checksum but the header's. The
// output is deterministic. A block that does not shrink is stored raw.
func CompressFrame(src []byte) []byte { return CompressFrameOptions(src, FrameOptions{}) }

// CompressFrameOptions is [CompressFrame] with optional checksums.
func CompressFrameOptions(src []byte, o FrameOptions) []byte {
	flg := byte(frameFLG)
	if o.BlockChecksum {
		flg |= flgBlockChecksum
	}
	if o.ContentChecksum {
		flg |= flgContentChecksum
	}
	out := make([]byte, 0, 32+len(src)/2)
	out = binary.LittleEndian.AppendUint32(out, frameMagic)
	out = append(out, flg, frameBD)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(src)))
	out = append(out, byte(xxh32(out[4:], 0)>>8))

	c := compressorPool.Get().(*Compressor)
	for rest := src; len(rest) > 0; {
		n := min(len(rest), frameBlockMax)
		blk := rest[:n]
		rest = rest[n:]
		hdr := len(out)
		out = append(out, 0, 0, 0, 0)
		out = c.AppendCompress(out, blk)
		size := uint32(len(out) - hdr - 4)
		if int(size) >= n {
			out = append(out[:hdr+4], blk...)
			size = uint32(n) | frameStored
		}
		binary.LittleEndian.PutUint32(out[hdr:], size)
		if o.BlockChecksum {
			out = binary.LittleEndian.AppendUint32(out, xxh32(out[hdr+4:], 0))
		}
	}
	compressorPool.Put(c)

	out = append(out, 0, 0, 0, 0) // end mark
	if o.ContentChecksum {
		out = binary.LittleEndian.AppendUint32(out, xxh32(src, 0))
	}
	return out
}

// DecompressFrame decodes the LZ4 frames in src (concatenated frames and
// skippable frames are accepted) and returns the content, at most
// [DefaultMaxOutput] bytes.
func DecompressFrame(src []byte) ([]byte, error) {
	return DecompressFrameLimit(src, DefaultMaxOutput)
}

// DecompressFrameLimit is [DecompressFrame] refusing, with [ErrTooLarge], any
// input whose content passes maxOut bytes in total. The refusal comes before
// the allocation it protects: the declared content size and every block's
// declared size are checked against maxOut (and against what the bytes present
// can expand to) before a buffer is made. Header, block and content checksums
// are verified; reserved bits, a bad header checksum, a dictionary ID, a block
// past the declared maximum, a content size that disagrees with the content,
// truncation and trailing bytes that are not a frame are [ErrFrame].
func DecompressFrameLimit(src []byte, maxOut int) ([]byte, error) {
	maxOut = max(0, min(maxOut, maxInt/2))
	if len(src) == 0 {
		return nil, fmt.Errorf("%w: empty input", ErrFrame)
	}
	var out []byte
	for len(src) > 0 {
		if len(src) < 4 {
			return nil, fmt.Errorf("%w: truncated magic", ErrFrame)
		}
		magic := binary.LittleEndian.Uint32(src)
		src = src[4:]
		switch {
		case magic == frameMagic:
			var err error
			out, src, err = decodeFrame(out, src, maxOut)
			if err != nil {
				return nil, err
			}
		case magic&frameSkippableMask == frameSkippableLo:
			if len(src) < 4 {
				return nil, fmt.Errorf("%w: truncated skippable frame", ErrFrame)
			}
			n := uint64(binary.LittleEndian.Uint32(src))
			if n > uint64(len(src)-4) {
				return nil, fmt.Errorf("%w: skippable frame past the input", ErrFrame)
			}
			src = src[4+n:]
		default:
			return nil, fmt.Errorf("%w: bad magic %#08x", ErrFrame, magic)
		}
	}
	return out, nil
}

const maxInt = int(^uint(0) >> 1)

// decodeFrame decodes one frame whose magic was consumed, appending to out
// (whose length is what earlier frames produced, counted against maxOut), and
// returns the rest of src.
func decodeFrame(out, src []byte, maxOut int) ([]byte, []byte, error) {
	if len(src) < 3 {
		return nil, nil, fmt.Errorf("%w: truncated header", ErrFrame)
	}
	flg, bd := src[0], src[1]
	hlen := 2
	if flg>>6 != 1 {
		return nil, nil, fmt.Errorf("%w: version %d", ErrFrame, flg>>6)
	}
	if flg&0x02 != 0 || bd&0x8F != 0 {
		return nil, nil, fmt.Errorf("%w: reserved bits set", ErrFrame)
	}
	if flg&0x01 != 0 {
		return nil, nil, fmt.Errorf("%w: dictionary frames are not supported", ErrFrame)
	}
	bmaxID := bd >> 4 & 7
	if bmaxID < 4 {
		return nil, nil, fmt.Errorf("%w: block maximum id %d", ErrFrame, bmaxID)
	}
	blockMax := 1 << (8 + 2*bmaxID)
	independent := flg&flgIndependent != 0
	blockSum := flg&flgBlockChecksum != 0
	contentSum := flg&flgContentChecksum != 0
	var declared uint64
	hasSize := flg&flgContentSize != 0
	if hasSize {
		hlen += 8
	}
	if len(src) < hlen+1 {
		return nil, nil, fmt.Errorf("%w: truncated header", ErrFrame)
	}
	if hasSize {
		declared = binary.LittleEndian.Uint64(src[2:])
	}
	if byte(xxh32(src[:hlen], 0)>>8) != src[hlen] {
		return nil, nil, fmt.Errorf("%w: header checksum", ErrFrame)
	}
	src = src[hlen+1:]

	start := len(out)
	room := maxOut - start // what this frame may still produce
	if hasSize {
		if declared > uint64(room) {
			return nil, nil, ErrTooLarge
		}
		// Reserve only what the input could earn: a forged size on a tiny
		// frame must not allocate.
		want := min(int(declared), room)
		if len(src) <= maxInt/maxExpansion {
			want = min(want, maxExpansion*len(src))
		}
		if cap(out)-start < want {
			grown := make([]byte, start, start+want)
			copy(grown, out)
			out = grown
		}
	}

	for {
		if len(src) < 4 {
			return nil, nil, fmt.Errorf("%w: truncated block header", ErrFrame)
		}
		bh := binary.LittleEndian.Uint32(src)
		src = src[4:]
		if bh == 0 {
			break
		}
		stored := bh&frameStored != 0
		size := uint64(bh &^ frameStored)
		if size > uint64(blockMax) {
			return nil, nil, fmt.Errorf("%w: block of %d bytes past the maximum %d", ErrFrame, size, blockMax)
		}
		need := size
		if blockSum {
			need += 4
		}
		if need > uint64(len(src)) {
			return nil, nil, fmt.Errorf("%w: truncated block", ErrFrame)
		}
		payload := src[:size]
		if blockSum && binary.LittleEndian.Uint32(src[size:]) != xxh32(payload, 0) {
			return nil, nil, fmt.Errorf("%w: block checksum", ErrFrame)
		}
		src = src[need:]

		left := maxOut - len(out)
		if stored {
			if int(size) > left {
				return nil, nil, ErrTooLarge
			}
			out = append(out, payload...)
			continue
		}
		var err error
		if out, err = decodeFrameBlock(out, start, payload, independent, min(blockMax, left), blockMax <= left); err != nil {
			return nil, nil, err
		}
	}

	got := uint64(len(out) - start)
	if hasSize && got != declared {
		return nil, nil, fmt.Errorf("%w: content size %d, header says %d", ErrFrame, got, declared)
	}
	if contentSum {
		if len(src) < 4 {
			return nil, nil, fmt.Errorf("%w: truncated content checksum", ErrFrame)
		}
		if binary.LittleEndian.Uint32(src) != xxh32(out[start:], 0) {
			return nil, nil, fmt.Errorf("%w: content checksum", ErrFrame)
		}
		src = src[4:]
	}
	return out, src, nil
}

// decodeFrameBlock decodes one compressed block onto out. limit bounds the raw
// size (the block maximum, or what the caller's ceiling leaves); a block that
// passes it is ErrFrame when limit is the block maximum, ErrTooLarge when the
// ceiling is what stopped it. Linked blocks see the last 64 KiB of out (from
// start, the frame's first byte: frames never share a window); for
// independent ones the window is empty, so a match reaching before the block
// is corrupt.
func decodeFrameBlock(out []byte, start int, payload []byte, independent bool, limit int, byFormat bool) ([]byte, error) {
	var window []byte
	if !independent {
		window = out[max(start, len(out)-winSize):]
	}
	if len(payload) <= maxInt/maxExpansion {
		limit = min(limit, maxExpansion*len(payload))
	}
	maxEnd := len(window) + limit
	buf := make([]byte, maxEnd+decodeSlack)
	copy(buf, window)
	buf, di, err := decodeInto(buf, len(window), payload, maxEnd)
	if errors.Is(err, ErrTooLarge) && byFormat {
		return nil, fmt.Errorf("%w: block decodes past the maximum block size", ErrFrame)
	}
	if err != nil {
		return nil, err
	}
	return append(out, buf[len(window):di]...), nil
}
