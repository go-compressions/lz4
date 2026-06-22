// Command bench measures go-compressions/lz4 against the pierrec/lz4 reference
// (the de-facto Go LZ4 library) on the same machine: LZ4-block compression
// ratio, single-core compression throughput, and single-core decompression
// throughput.
//
// LZ4 blocks address a 64 KiB window, so files larger than that are split into
// 64 KiB blocks and each block is compressed independently — exactly how an
// LZ4 frame works — and the per-block sizes/times are summed. Both codecs are
// driven the same way over the same block boundaries, so the comparison is fair.
//
// It is a standalone module (see go.mod) so it stays isolated from the library's
// coverage gate.
//
// Usage:
//
//	go run . -corpus <dir> -iters 21
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ourlz4 "github.com/go-compressions/lz4"
	plz4 "github.com/pierrec/lz4/v4"
)

const blockSize = 64 << 10 // LZ4's 64 KiB window

func main() {
	corpus := flag.String("corpus", "corpus", "directory of corpus files")
	iters := flag.Int("iters", 21, "timed iterations per file")
	flag.Parse()

	files, err := listCorpus(*corpus)
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}

	fmt.Printf("# lz4 parity run  (%s)\n", time.Now().Format("2006-01-02"))
	fmt.Printf("%-16s %12s %12s %12s %12s %10s %10s %s\n",
		"file", "ourComp", "refComp", "ourDecomp", "refDecomp", "ourRatio", "refRatio", "rt/xc")
	fmt.Printf("%-16s %12s %12s %12s %12s %10s %10s %s\n",
		"", "MB/s", "MB/s", "MB/s", "MB/s", "", "", "")

	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		name := filepath.Base(f)
		blocks := split(src)

		o := benchOurs(blocks, *iters)
		r := benchPierrec(blocks, *iters)
		fmt.Printf("%-16s %12.1f %12.1f %12.1f %12.1f %10.3f %10.3f %s\n",
			name, o.comp, r.comp, o.decomp, r.decomp, o.ratio, r.ratio, mark(o.ok))
	}
}

type result struct {
	comp, decomp, ratio float64
	ok                  bool
}

func split(src []byte) [][]byte {
	var bs [][]byte
	for i := 0; i < len(src); i += blockSize {
		end := i + blockSize
		if end > len(src) {
			end = len(src)
		}
		bs = append(bs, src[i:end])
	}
	if len(bs) == 0 {
		bs = [][]byte{nil}
	}
	return bs
}

func benchOurs(blocks [][]byte, iters int) result {
	var orig, comp int
	ok := true
	for _, b := range blocks {
		c := ourlz4.CompressBlock(b)
		comp += len(c)
		orig += len(b)
		d, err := ourlz4.DecompressBlock(c, len(b))
		if err != nil || !bytesEqual(d, b) {
			ok = false
		}
		// cross-decode: pierrec must decode our block.
		if len(b) > 0 {
			out := make([]byte, len(b))
			n, err := plz4.UncompressBlock(c, out)
			if err != nil || !bytesEqual(out[:n], b) {
				ok = false
			}
		}
	}
	cBest := timeBlocks(iters, func() {
		for _, b := range blocks {
			_ = ourlz4.CompressBlock(b)
		}
	})
	comps := make([][]byte, len(blocks))
	for i, b := range blocks {
		comps[i] = ourlz4.CompressBlock(b)
	}
	dBest := timeBlocks(iters, func() {
		for i, c := range comps {
			_, _ = ourlz4.DecompressBlock(c, len(blocks[i]))
		}
	})
	return result{
		comp:   mbps(orig, cBest),
		decomp: mbps(orig, dBest),
		ratio:  ratio(comp, orig),
		ok:     ok,
	}
}

func benchPierrec(blocks [][]byte, iters int) result {
	var orig, comp int
	var c plz4.Compressor
	comps := make([][]byte, len(blocks))
	for i, b := range blocks {
		buf := make([]byte, plz4.CompressBlockBound(len(b)))
		n, _ := c.CompressBlock(b, buf)
		if n == 0 { // incompressible: pierrec stores raw
			n = copy(buf, b)
			buf = buf[:n]
		}
		comps[i] = buf[:n]
		comp += n
		orig += len(b)
	}
	cBest := timeBlocks(iters, func() {
		var cc plz4.Compressor
		for _, b := range blocks {
			buf := make([]byte, plz4.CompressBlockBound(len(b)))
			_, _ = cc.CompressBlock(b, buf)
		}
	})
	dBest := timeBlocks(iters, func() {
		for i, cb := range comps {
			out := make([]byte, len(blocks[i]))
			_, _ = plz4.UncompressBlock(cb, out)
		}
	})
	return result{
		comp:   mbps(orig, cBest),
		decomp: mbps(orig, dBest),
		ratio:  ratio(comp, orig),
		ok:     true,
	}
}

func timeBlocks(iters int, fn func()) time.Duration {
	for i := 0; i < 3; i++ {
		fn()
	}
	best := time.Duration(1 << 62)
	for i := 0; i < iters; i++ {
		t0 := time.Now()
		fn()
		if d := time.Since(t0); d < best {
			best = d
		}
	}
	return best
}

func listCorpus(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasPrefix(filepath.Base(p), ".") {
			return nil
		}
		files = append(files, p)
		return nil
	})
	sort.Strings(files)
	return files, err
}

func mbps(n int, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return (float64(n) / 1e6) / d.Seconds()
}

func ratio(comp, orig int) float64 {
	if orig == 0 {
		return 0
	}
	return float64(comp) / float64(orig)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mark(ok bool) string {
	if ok {
		return "ok"
	}
	return "FAIL"
}
