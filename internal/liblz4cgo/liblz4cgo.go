//go:build liblz4 && cgo

// Package liblz4cgo calls the reference liblz4 (lz4.c) through cgo. It exists
// only for the differential tests and the benchmark against the reference, and
// only builds with `-tags liblz4` and liblz4 installed (pkg-config liblz4).
package liblz4cgo

/*
#cgo pkg-config: liblz4
#include <lz4.h>
*/
import "C"

import (
	"errors"
	"unsafe"
)

// Compress is LZ4_compress_default.
func Compress(src []byte) ([]byte, error) {
	dst := make([]byte, int(C.LZ4_compressBound(C.int(len(src)))))
	var s *C.char
	if len(src) > 0 {
		s = (*C.char)(unsafe.Pointer(&src[0]))
	}
	n := C.LZ4_compress_default(s, (*C.char)(unsafe.Pointer(&dst[0])), C.int(len(src)), C.int(len(dst)))
	if n <= 0 {
		return nil, errors.New("liblz4: LZ4_compress_default failed")
	}
	return dst[:n], nil
}

// Decompress is LZ4_decompress_safe into a buffer of exactly rawLen bytes, the
// strictest case: liblz4 refuses end-of-block violations there.
func Decompress(comp []byte, rawLen int) ([]byte, error) {
	dst := make([]byte, rawLen+1)
	var s *C.char
	if len(comp) > 0 {
		s = (*C.char)(unsafe.Pointer(&comp[0]))
	}
	n := C.LZ4_decompress_safe(s, (*C.char)(unsafe.Pointer(&dst[0])), C.int(len(comp)), C.int(rawLen))
	if n < 0 {
		return nil, errors.New("liblz4: LZ4_decompress_safe refused the block")
	}
	return dst[:n], nil
}

// CompressedSize is the reference's output size, for a ratio comparison.
func CompressedSize(src []byte) int {
	b, err := Compress(src)
	if err != nil {
		return -1
	}
	return len(b)
}
