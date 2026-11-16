//go:build unix

package lz4

import (
	"bytes"
	"math/rand/v2"
	"syscall"
	"testing"
)

// The assembly loop copies 16 bytes at a time and must not read past src or
// write past dst: both buffers end flush against a PROT_NONE page.
func TestDecodeBlockStaysInsideBuffers(t *testing.T) {
	page := syscall.Getpagesize()
	size := 1 << 20 // 1 MiB (a multiple of any page size) + a guard page
	size = (size + page - 1) / page * page
	mk := func() []byte {
		m, err := syscall.Mmap(-1, 0, size+page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
		if err != nil {
			t.Skip("mmap:", err)
		}
		if err := syscall.Mprotect(m[size:], syscall.PROT_NONE); err != nil {
			t.Skip("mprotect:", err)
		}
		t.Cleanup(func() { syscall.Munmap(m) })
		return m
	}
	srcMem, dstMem := mk(), mk()
	r := rand.New(rand.NewPCG(21, 22))
	for name, raw := range decodeCorpora(r) {
		if len(raw) > size {
			continue
		}
		comp := CompressBlock(raw)
		src := srcMem[size-len(comp) : size : size]
		copy(src, comp)
		dst := dstMem[size-len(raw) : size : size]
		n, err := DecodeBlock(dst, src)
		if err != nil || n != len(raw) || !bytes.Equal(dst[:n], raw) {
			t.Fatalf("%s: n=%d err=%v", name, n, err)
		}
	}
}
