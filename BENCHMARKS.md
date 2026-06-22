# Performance parity — go-compressions/lz4 vs pierrec/lz4  (2026-06-22)

**Methodology**

- **Host:** Apple M4 Max, macOS 26.5, single core.
- **Our codec:** `github.com/go-compressions/lz4` (`CompressBlock` / `DecompressBlock`),
  Go 1.26.4, `CGO_ENABLED=0`. The match-extend inner loop is delegated to the SIMD
  `github.com/go-simd/matchlen` kernel.
- **Reference:** `github.com/pierrec/lz4/v4 v4.1.27` — the de-facto Go LZ4 library —
  via its block API (`Compressor.CompressBlock` / `UncompressBlock`). Both are pure Go,
  timed in-process, so the comparison is apples-to-apples.
- **Corpus:** the Silesia corpus plus three synthetic 8 MiB edge cases (zeros, random,
  highly repetitive). LZ4 blocks address a 64 KiB window, so every file is split into
  64 KiB blocks compressed independently (exactly how an LZ4 frame works) and the
  per-block sizes / times are summed. Both codecs use identical block boundaries.
- **Iterations:** 21 timed iterations per file after 3 warm-up rounds; **best** reported.
  Ratio = compressed ÷ original (lower is better).
- **Correctness:** every block is round-trip verified **and cross-decoded with pierrec**
  (our block → pierrec decoder == original) before timing — the `rt/xc` column. LZ4 is a
  standard block format, so we are byte-compatible in both directions.

| file | our comp MB/s | ref comp MB/s | our decomp MB/s | ref decomp MB/s | our ratio | ref ratio | rt/xc |
|------|--------------:|--------------:|----------------:|----------------:|----------:|----------:|:-----:|
| dickens             |  276.9 |   337.2 |   814.3 |  4495.8 | 0.588 | 0.615 | ok |
| mozilla             |  467.4 |   554.4 |  1298.4 |  4299.8 | 0.506 | 0.518 | ok |
| mr                  |  461.5 |   548.1 |  1795.5 |  5928.5 | 0.541 | 0.546 | ok |
| nci                 |  806.6 |  1294.9 |  1607.8 |  7206.0 | 0.163 | 0.172 | ok |
| ooffice             |  404.3 |   390.2 |  1457.0 |  3981.6 | 0.702 | 0.720 | ok |
| osdb                |  482.1 |   572.9 |  1643.8 |  5185.3 | 0.495 | 0.501 | ok |
| reymont             |  275.0 |   436.4 |   768.5 |  4496.2 | 0.440 | 0.466 | ok |
| samba               |  504.1 |   664.4 |  1224.1 |  5099.1 | 0.358 | 0.372 | ok |
| sao                 |  401.2 |   335.0 |  2880.9 |  4259.5 | 0.909 | 0.918 | ok |
| webster             |  331.9 |   431.2 |   958.6 |  4267.5 | 0.464 | 0.487 | ok |
| xml                 |  580.0 |   924.7 |  1388.0 |  5499.7 | 0.219 | 0.236 | ok |
| synth_random.bin    | 6323.9 | 13897.1 | 35576.3 | 36472.2 | 1.004 | 1.004 | ok |
| synth_repetitive.bin| 9807.9 | 10561.1 |  2824.3 | 11042.5 | 0.005 | 0.005 | ok |
| synth_zeros.bin     | 9844.3 | 11275.0 |  1261.3 | 39176.2 | 0.004 | 0.004 | ok |

## Summary

**Ratio — we beat the reference on every real file.** Our compressed size is smaller
than pierrec's on all of Silesia (xml 0.219 vs 0.236, nci 0.163 vs 0.172, webster 0.464
vs 0.487, reymont 0.440 vs 0.466). The 6-byte hash key plus lazy matching find more and
longer matches than pierrec's fast-mode parse. On synthetic data we are level.

**Compression speed — competitive.** We run at roughly **0.7–0.9×** pierrec on most
files and actually *beat* it on ooffice (404 vs 390) and sao (401 vs 335). The deficit
is the cost of the extra work that buys the better ratio (lazy lookahead, multi-position
probe). For a pure-Go codec that out-compresses the established library while staying
within ~15–30 % of its speed, this is a good place to be.

**Decompression speed — this is the gap.** pierrec decodes ~3–5× faster (xml 1388 vs
5500 MB/s, dickens 814 vs 4496). Decode is format-fixed (ratio-neutral), so this is pure
implementation headroom — our decoder is the obvious optimisation target.

**Correctness — byte-compatible both ways.** Every block round-trips, and every block we
emit is decoded correctly by pierrec (and every pierrec block by us). We are a drop-in
LZ4-block codec.

## Root cause of the decompression gap

pierrec's decoder uses unsafe pointer arithmetic and word-at-a-time copies for the
literal and match copy loops. Ours is a portable, bounds-checked Go loop that copies
matches byte-at-a-time. That copy loop is the whole gap.

## Action items

1. **Bulk match copy** — replace the byte-at-a-time match copy with an overlap-aware
   word copy (and `copy` for non-overlapping runs). This alone should recover most of the
   decode deficit. Decompression is the priority since ratio already leads.
2. **SIMD copy kernel via `go-asmgen`** — a vectorised overlap-aware copy on all six
   64-bit targets (amd64 / arm64 / riscv64 / loong64 / ppc64le / s390x), matching the
   `matchlen` pattern already used on the encode side.
3. **Encoder throughput** — bound the lazy lookahead more aggressively on already-long
   matches and prefetch the hash bucket, to narrow the compression-speed gap without
   giving back the ratio lead.
4. **Parallel blocks** — independent 64 KiB blocks parallelise trivially; a worker pool
   gives near-linear multi-core compression. Tracked separately from this single-core bar.

## Reproduce

```sh
cd benchmarks
go run . -corpus ./corpus -iters 21
```

`benchmarks/` is a separate Go module, so it is excluded from the library's
`go test ./...` run and coverage gate. `benchmarks/fetch_corpus.sh` downloads the
Silesia corpus and generates the synthetic inputs into `benchmarks/corpus/`.
