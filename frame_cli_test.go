package lz4

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// lz4Bin returns the reference lz4(1). It skips when the binary is absent,
// unless LZ4_CLI_REQUIRED is set (CI sets it where it installs lz4): a suite of
// skips is green, and green must not be able to mean "never asked lz4".
func lz4Bin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("lz4")
	if err != nil {
		if os.Getenv("LZ4_CLI_REQUIRED") != "" {
			t.Fatalf("LZ4_CLI_REQUIRED is set but lz4 is not installed: %v", err)
		}
		t.Skip("lz4 CLI not installed")
	}
	return p
}

func cliSamples() [][]byte {
	r := rand.New(rand.NewPCG(41, 42))
	var out [][]byte
	for _, n := range []int{0, 1, 100, 65536, 300000, 5<<20 + 123} {
		out = append(out, sample(r, n))
	}
	return out
}

// What we write, lz4 reads: -d checks the header checksum, every block, and the
// content checksum when present.
func TestCLIDecodesOurFrames(t *testing.T) {
	bin := lz4Bin(t)
	for i, src := range cliSamples() {
		for _, o := range []FrameOptions{{}, {ContentChecksum: true}, {BlockChecksum: true}, {true, true}} {
			cmd := exec.Command(bin, "-d", "-c")
			cmd.Stdin = bytes.NewReader(CompressFrameOptions(src, o))
			got, err := cmd.Output()
			if err != nil || !bytes.Equal(got, src) {
				t.Fatalf("sample %d %+v: lz4 -d: err=%v equal=%v", i, o, err, bytes.Equal(got, src))
			}
		}
	}
}

// What lz4 writes, we read: linked and independent blocks, every block size,
// with and without the checksums and the content size.
func TestWeDecodeCLIFrames(t *testing.T) {
	bin := lz4Bin(t)
	dir := t.TempDir()
	for i, src := range cliSamples() {
		in := filepath.Join(dir, "in")
		if err := os.WriteFile(in, src, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			nil, {"-1"}, {"-9"}, {"-12"}, {"-BI"}, {"-BD", "-B4"}, {"-BI", "-B5"}, {"-B6"}, {"-B7", "-BD"},
			{"--content-size"}, {"-BX"}, {"--no-frame-crc"}, {"--content-size", "-BX", "-BI"},
		} {
			cmd := exec.Command(bin, append(append([]string{"-c", "-f"}, args...), in)...)
			comp, err := cmd.Output()
			if err != nil {
				t.Fatalf("lz4 %v: %v", args, err)
			}
			got, err := DecompressFrame(comp)
			if err != nil || !bytes.Equal(got, src) {
				t.Fatalf("sample %d lz4 %v: err=%v equal=%v", i, args, err, bytes.Equal(got, src))
			}
		}
	}
}

// BenchmarkLZ4CLI prints the reference's own in-memory speeds (lz4 -b1: level 1
// compress and decompress, one thread, no I/O) on the 32 MiB sample that
// BenchmarkCompressFrame and BenchmarkDecompressFrame use, to read side by side.
func BenchmarkLZ4CLI(b *testing.B) {
	p, err := exec.LookPath("lz4")
	if err != nil {
		b.Skip("lz4 CLI not installed")
	}
	in := filepath.Join(b.TempDir(), "in")
	if err := os.WriteFile(in, frameBenchSample(), 0o600); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		out, err := exec.Command(p, "-b1", "-i5", "-T1", in).CombinedOutput()
		if err != nil {
			b.Fatal(err)
		}
		b.Logf("%s", out)
	}
}

// The bomb is refused at the ceiling without the memory it asked for, with the
// Go heap capped far below what the frame claims.
func TestFrameBombUnderMemoryLimit(t *testing.T) {
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(64 << 20))
	// 4 MiB-max block of 0xFF extension bytes: ~255x expansion = 255 MiB claimed.
	bomb := append([]byte{0x1F, 'a', 1, 0}, bytes.Repeat([]byte{255}, 1<<20)...)
	bomb = append(bomb, 0)
	f := mkFrame(0x68, 0x70, nil, [][]byte{blk(bomb, false), blk(bomb, false)}, endMark)
	if _, err := DecompressFrameLimit(f, 1<<20); err == nil {
		t.Fatal("bomb accepted")
	}
	if _, err := DecompressFrame(mkFrame(0x68, 0x70, sz(1<<62), nil, endMark)); err == nil {
		t.Fatal("2^62 content size accepted")
	}
}
