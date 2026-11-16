package lz4

import "errors"

// ErrShortDst reports an output buffer too small for the block.
var ErrShortDst = errors.New("lz4: destination too small")

// DecodeBlock decompresses the LZ4 block src into dst and returns the number of
// bytes written. It allocates nothing: the caller owns dst, sized for the
// largest output it accepts (zfs and squashfs know the block's exact size). A
// block that would write past len(dst) is refused with [ErrShortDst], a
// malformed one with a corrupt-block error; nothing is written outside dst.
//
// It is [DecompressBlock] without the allocation, the growth and the clamp, and
// the same decoder otherwise: a 16-byte fast path while a sequence is at least
// 16 bytes clear of both buffers' ends (the LZ4 format keeps the last 5 bytes
// literal and the last match 12 bytes from the end, so that is almost every
// sequence), exact copies for the last few bytes.
func DecodeBlock(dst, src []byte) (int, error) {
	di, ip := 0, 0
	for ip < len(src) {
		// The assembly loop takes every whole sequence it can; the Go code below
		// then does one (the sequence it declined, or the end of the block) and
		// hands over again.
		if haveFast {
			ip, di = fastSeqs(src, dst, ip, di)
			if ip >= len(src) {
				break
			}
		}
		// Fast path: both buffers are far enough from their ends that a sequence
		// of short literals (< 15) and a short match (<= 16 bytes, not
		// overlapping its source) needs no further bounds test. Anything else
		// falls through to the general code below, untouched.
		if ip+32 <= len(src) && di+48 <= len(dst) {
			t := int(src[ip])
			if ll := t >> 4; ll < 15 {
				mlN := t & 0x0F
				p := ip + 1
				copy(dst[di:di+16], src[p:p+16])
				p += ll
				d := di + ll
				offset := int(src[p]) | int(src[p+1])<<8
				if offset-1 >= d { // offset == 0 or reaches before dst[0]
					return 0, errCorrupt
				}
				if mlN <= 12 && mlN+minMatch <= offset {
					m := d - offset
					copy(dst[d:d+16], dst[m:m+16])
					di = d + mlN + minMatch
					ip = p + 2
					continue
				}
			}
		}
		token := int(src[ip])
		ip++
		ll := token >> 4
		if ll == 15 {
			for {
				if ip >= len(src) {
					return 0, errCorrupt
				}
				b := src[ip]
				ip++
				ll += int(b)
				if ll > len(src) { // also stops int overflow on 32-bit
					return 0, errCorrupt
				}
				if b != 255 {
					break
				}
			}
		}
		if ip+ll > len(src) {
			return 0, errCorrupt
		}
		if di+ll > len(dst) {
			return 0, ErrShortDst
		}
		if ll <= 16 && ip+16 <= len(src) && di+16 <= len(dst) {
			copy(dst[di:di+16], src[ip:ip+16]) // reads/writes up to 16 bytes; the overrun is rewritten
		} else {
			copy(dst[di:di+ll], src[ip:ip+ll])
		}
		ip += ll
		di += ll
		if ip == len(src) {
			break
		}
		if ip+2 > len(src) {
			return 0, errCorrupt
		}
		offset := int(src[ip]) | int(src[ip+1])<<8
		ip += 2
		ml := token&0x0F + minMatch
		mpos := di - offset
		if offset == 0 || mpos < 0 {
			return 0, errCorrupt
		}
		if ml <= 16 {
			if ml <= offset && di+16 <= len(dst) {
				copy(dst[di:di+16], dst[mpos:mpos+16])
				di += ml
				continue
			}
		} else if token&0x0F == 15 {
			for {
				if ip >= len(src) {
					return 0, errCorrupt
				}
				b := src[ip]
				ip++
				ml += int(b)
				if ml > len(dst) { // a match longer than dst cannot fit; also stops overflow
					return 0, ErrShortDst
				}
				if b != 255 {
					break
				}
			}
		}
		if di+ml > len(dst) {
			return 0, ErrShortDst
		}
		di = matchCopy(dst, di, mpos, ml)
	}
	return di, nil
}
