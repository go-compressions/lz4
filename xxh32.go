package lz4

import (
	"encoding/binary"
	"math/bits"
)

const (
	xxPrime1 uint32 = 2654435761
	xxPrime2 uint32 = 2246822519
	xxPrime3 uint32 = 3266489917
	xxPrime4 uint32 = 668265263
	xxPrime5 uint32 = 374761393
)

func xxRound(acc, in uint32) uint32 {
	return bits.RotateLeft32(acc+in*xxPrime2, 13) * xxPrime1
}

// xxh32 is XXH32 (seed 0 in every LZ4 frame use) of b: the hash the LZ4 frame
// format uses for the header checksum byte, the block checksums and the content
// checksum.
func xxh32(b []byte, seed uint32) uint32 {
	n := len(b)
	var h uint32
	if n >= 16 {
		v1 := seed + xxPrime1 + xxPrime2
		v2 := seed + xxPrime2
		v3 := seed
		v4 := seed - xxPrime1
		for ; len(b) >= 16; b = b[16:] {
			v1 = xxRound(v1, binary.LittleEndian.Uint32(b[0:]))
			v2 = xxRound(v2, binary.LittleEndian.Uint32(b[4:]))
			v3 = xxRound(v3, binary.LittleEndian.Uint32(b[8:]))
			v4 = xxRound(v4, binary.LittleEndian.Uint32(b[12:]))
		}
		h = bits.RotateLeft32(v1, 1) + bits.RotateLeft32(v2, 7) +
			bits.RotateLeft32(v3, 12) + bits.RotateLeft32(v4, 18)
	} else {
		h = seed + xxPrime5
	}
	h += uint32(n)
	for ; len(b) >= 4; b = b[4:] {
		h = bits.RotateLeft32(h+binary.LittleEndian.Uint32(b)*xxPrime3, 17) * xxPrime4
	}
	for _, c := range b {
		h = bits.RotateLeft32(h+uint32(c)*xxPrime5, 11) * xxPrime1
	}
	h ^= h >> 15
	h *= xxPrime2
	h ^= h >> 13
	h *= xxPrime3
	h ^= h >> 16
	return h
}
