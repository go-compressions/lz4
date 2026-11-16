//go:build !amd64 && !arm64

package lz4

// No assembly fast loop on this architecture: DecodeBlock's Go code does it all.
func fastSeqs(src, dst []byte, ip, di int) (int, int) { return ip, di }

const haveFast = false
