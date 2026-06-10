// Package lz4 implements the LZ4 block format (compress and decompress). The
// compressor's match extension — LZ4's hot "count the matching bytes" inner
// loop — is delegated to github.com/go-compressions/matchlen, whose SIMD
// common-prefix kernel makes it fast on amd64/arm64/riscv64.
package lz4

import "github.com/go-compressions/matchlen"

const (
	minMatch     = 4
	hashLog      = 16
	hashTableLen = 1 << hashLog
	mfLimit      = 12 // matches may not start in the last 12 bytes
	lastLiterals = 5  // the last 5 bytes are always literals
)

func hash4(x uint32) uint32 { return (x * 2654435761) >> (32 - hashLog) }

func u32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// CompressBlock compresses src into a standard LZ4 block.
func CompressBlock(src []byte) []byte { return compress(src, matchlen.MatchLen) }

// compress is parameterised by the match-counter so benchmarks can compare the
// SIMD matchlen against a scalar reference.
func compress(src []byte, mlen func(a, b []byte) int) []byte {
	dst := make([]byte, 0, len(src)/2+16)
	if len(src) < mfLimit+minMatch {
		return emitLast(dst, src)
	}
	var table [hashTableLen]int32
	for i := range table {
		table[i] = -1
	}
	matchLimit := len(src) - lastLiterals
	limit := len(src) - mfLimit
	anchor := 0
	table[hash4(u32(src))] = 0
	ip := 1
	for ip < limit {
		seq := u32(src[ip:])
		h := hash4(seq)
		ref := int(table[h])
		table[h] = int32(ip)
		if ref < 0 || ip-ref > 65535 || u32(src[ref:]) != seq {
			ip++
			continue
		}
		fwd := mlen(src[ip+minMatch:matchLimit], src[ref+minMatch:])
		ml := minMatch + fwd
		mStart, mref := ip, ref
		for mStart > anchor && mref > 0 && src[mStart-1] == src[mref-1] {
			mStart--
			mref--
			ml++
		}
		dst = emitSequence(dst, src[anchor:mStart], mStart-mref, ml)
		ip = mStart + ml
		anchor = ip
		if ip >= limit {
			break
		}
		table[hash4(u32(src[ip-2:]))] = int32(ip - 2)
	}
	return emitLast(dst, src[anchor:])
}

func emitLength(dst []byte, n int) []byte {
	for n >= 255 {
		dst = append(dst, 255)
		n -= 255
	}
	return append(dst, byte(n))
}

func emitSequence(dst, lits []byte, offset, matchLen int) []byte {
	ll, ml := len(lits), matchLen-minMatch
	var token byte
	if ll >= 15 {
		token = 0xF0
	} else {
		token = byte(ll) << 4
	}
	if ml >= 15 {
		token |= 0x0F
	} else {
		token |= byte(ml)
	}
	dst = append(dst, token)
	if ll >= 15 {
		dst = emitLength(dst, ll-15)
	}
	dst = append(dst, lits...)
	dst = append(dst, byte(offset), byte(offset>>8))
	if ml >= 15 {
		dst = emitLength(dst, ml-15)
	}
	return dst
}

func emitLast(dst, lits []byte) []byte {
	ll := len(lits)
	if ll >= 15 {
		dst = append(dst, 0xF0)
		dst = emitLength(dst, ll-15)
	} else {
		dst = append(dst, byte(ll)<<4)
	}
	return append(dst, lits...)
}

// DecompressBlock decompresses an LZ4 block. dstCap is a hint for the output
// capacity (the decompressed size if known).
func DecompressBlock(src []byte, dstCap int) ([]byte, error) {
	dst := make([]byte, 0, dstCap)
	ip := 0
	for ip < len(src) {
		token := src[ip]
		ip++
		ll := int(token >> 4)
		if ll == 15 {
			for {
				if ip >= len(src) {
					return nil, errCorrupt
				}
				b := src[ip]
				ip++
				ll += int(b)
				if b != 255 {
					break
				}
			}
		}
		if ip+ll > len(src) {
			return nil, errCorrupt
		}
		dst = append(dst, src[ip:ip+ll]...)
		ip += ll
		if ip == len(src) {
			break
		}
		if ip+2 > len(src) {
			return nil, errCorrupt
		}
		offset := int(src[ip]) | int(src[ip+1])<<8
		ip += 2
		ml := int(token&0x0F) + minMatch
		if token&0x0F == 15 {
			for {
				if ip >= len(src) {
					return nil, errCorrupt
				}
				b := src[ip]
				ip++
				ml += int(b)
				if b != 255 {
					break
				}
			}
		}
		mpos := len(dst) - offset
		if offset <= 0 || mpos < 0 {
			return nil, errCorrupt
		}
		for k := 0; k < ml; k++ {
			dst = append(dst, dst[mpos])
			mpos++
		}
	}
	return dst, nil
}

type lzError string

func (e lzError) Error() string { return string(e) }

const errCorrupt = lzError("lz4: corrupt block")
