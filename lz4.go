// Package lz4 implements the LZ4 block format (compress and decompress). The
// compressor's match extension — LZ4's hot "count the matching bytes" inner
// loop — is delegated to github.com/go-simd/matchlen, whose SIMD
// common-prefix kernel makes it fast on all six 64-bit Go targets.
//
// The match-finder is a single-cell hash table keyed on a 6-byte sequence (the
// reference LZ4 / pierrec hash), probing several adjacent positions per step
// and using lazy matching — if position+1 yields a strictly longer match the
// shorter one is deferred — for a better ratio on text.
package lz4

import (
	"encoding/binary"

	"github.com/go-simd/matchlen"
)

const (
	minMatch     = 4
	hashLog      = 16 // 64 KiB table — the reference LZ4 fast-mode size
	hashTableLen = 1 << hashLog
	winSize      = 1 << 16 // LZ4 offsets are 16-bit, so the window is 64 KiB
	mfLimit      = 12      // matches may not start in the last 12 bytes
	lastLiterals = 5       // the last 5 bytes are always literals
	adaptSkipLog = 7       // skip ramp on incompressible spans (matches pierrec)

	lazyEnabled = true // lazy matching: defer a match if pos+1 is longer
	// lazyMaxLen bounds the lazy lookahead to short matches. A long match is
	// already a big win, so peeking ahead rarely improves it and the extra
	// hash+confirm+count is wasted; skipping the peek there recovers speed at a
	// negligible ratio cost.
	lazyMaxLen = 64
)

// hash6 hashes the lower 6 bytes of x, exactly as pierrec/reference LZ4 do for
// fast-mode compression. A 6-byte key disperses far better than a 4-byte one,
// which both finds more candidates and cuts false-positive confirmations. Note
// it reads only the low 48 bits, so the key for position p+k is hash6(seq>>8*k)
// where seq is the 8-byte load at p — no extra memory load is needed.
func hash6(x uint64) uint32 {
	const prime6bytes = 227718039650203
	return uint32(((x << (64 - 48)) * prime6bytes) >> (64 - hashLog))
}

func u32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func u64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

// CompressBlock compresses src into a standard LZ4 block.
func CompressBlock(src []byte) []byte { return compress(src, matchlen.MatchLen) }

// compress is parameterised by the match-counter so benchmarks can compare the
// SIMD matchlen against a scalar reference.
//
// The parse keeps one position per 6-byte hash. Each step probes the table at
// ip, ip+1 and ip+2 (cheap: a single 8-byte load covers all three), taking the
// first confirmed hit. On a hit it then applies lazy matching: it looks one
// byte ahead and, if that yields a strictly longer match, it defers the current
// one (emitting one extra literal) and takes the longer match instead. That is
// the classic LZ4 ratio lever for text.
func compress(src []byte, mlen func(a, b []byte) int) []byte {
	dst := make([]byte, 0, len(src)/2+16)
	if len(src) < mfLimit+minMatch {
		return emitLast(dst, src)
	}
	// Zero-init (Go memclr): a slot of 0 means "empty or position 0"; the win
	// + u32 checks below disambiguate, so no -1 fill is needed.
	var table [hashTableLen]int32
	matchLimit := len(src) - lastLiterals
	limit := len(src) - mfLimit
	anchor := 0
	ip := 0

	for {
		// Search forward for the next match, probing three adjacent positions
		// per outer step from a single 8-byte load (the keys for ip, ip+1, ip+2
		// are hash6(seq), hash6(seq>>8), hash6(seq>>16)), ramping the skip
		// distance on incompressible spans. Every probed position is inserted so
		// later matches see more candidates.
		var ref int
		seq := u64(src[ip:])
		for {
			h0 := hash6(seq)
			r0 := int(table[h0])
			table[h0] = int32(ip)
			if ip-r0 <= winSize && r0 < ip && u32(src[r0:]) == uint32(seq) {
				ref = r0
				break
			}
			h1 := hash6(seq >> 8)
			r1 := int(table[h1])
			table[h1] = int32(ip + 1)
			if ip+1-r1 <= winSize && r1 < ip+1 && u32(src[r1:]) == uint32(seq>>8) {
				ip, ref = ip+1, r1
				break
			}
			h2 := hash6(seq >> 16)
			r2 := int(table[h2])
			table[h2] = int32(ip + 2)
			if ip+2-r2 <= winSize && r2 < ip+2 && u32(src[r2:]) == uint32(seq>>16) {
				ip, ref = ip+2, r2
				break
			}
			ip += 3 + (ip-anchor)>>adaptSkipLog
			if ip > limit {
				return emitLast(dst, src[anchor:])
			}
			seq = u64(src[ip:])
		}

		// Lazy matching: measure the match at ip, then peek one byte ahead. If
		// ip+1 yields a strictly longer match, defer the current one (emit one
		// extra literal) and take the longer match instead. A single lookahead
		// step captures most of the ratio win at a fraction of the cost of a
		// chained lazy search.
		ml := minMatch + mlen(src[ip+minMatch:matchLimit], src[ref+minMatch:])
		if lazyEnabled && ml < lazyMaxLen && ip+1 <= limit {
			lseq := u64(src[ip+1:])
			h := hash6(lseq)
			nref := int(table[h])
			table[h] = int32(ip + 1)
			if ip+1-nref <= winSize && nref < ip+1 && u32(src[nref:]) == uint32(lseq) {
				if nml := minMatch + mlen(src[ip+1+minMatch:matchLimit], src[nref+minMatch:]); nml > ml {
					ip++
					ref, ml = nref, nml
				}
			}
		}

		// Extend the match backwards over the pending literals.
		mStart, mref := ip, ref
		for mStart > anchor && mref > 0 && src[mStart-1] == src[mref-1] {
			mStart--
			mref--
			ml++
		}
		dst = emitSequence(dst, src[anchor:mStart], mStart-mref, ml)
		ip = mStart + ml
		anchor = ip
		if ip > limit {
			return emitLast(dst, src[anchor:])
		}
		// Seed the table with an interior position for better next matches.
		table[hash6(u64(src[ip-2:]))] = int32(ip - 2)
	}
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
