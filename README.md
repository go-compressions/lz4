<p align="center"><img src="https://raw.githubusercontent.com/go-compressions/brand/main/social/go-compressions-lz4.png" alt="go-compressions/lz4" width="720"></p>

# lz4

[![ci](https://github.com/go-compressions/lz4/actions/workflows/ci.yml/badge.svg)](https://github.com/go-compressions/lz4/actions/workflows/ci.yml)
![coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-compressions/lz4.svg)](https://pkg.go.dev/github.com/go-compressions/lz4)

A clean Go implementation of the **LZ4 block format** (`CompressBlock` /
`DecompressBlock`), wire-compatible with the reference (cross-checked against
`pierrec/lz4`). Its compressor delegates LZ4's hot "count the matching bytes"
inner loop (`LZ4_count`) to [matchlen](https://github.com/go-compressions/matchlen),
whose SIMD common-prefix kernel makes match extension fast.

As of `matchlen` **v0.3.0**, that kernel ships SIMD on **all six** of Go's 64-bit
targets — amd64 (SSE2), arm64 (NEON), riscv64 (RVV), loong64 (LSX), ppc64le (VSX)
and s390x (vector facility). lz4 needs **no code change** to benefit: `MatchLen`
dispatches per-arch, so the bulk match now runs vectorized on ppc64le and s390x
too. Those two paths are qemu-validated (correct + bit-identical to scalar);
native ppc64le/s390x throughput numbers are pending hardware.

```go
c := lz4.CompressBlock(src)
out, _ := lz4.DecompressBlock(c, len(src))
```

## Why LZ4 is the ideal matchlen consumer

LZ4 has **no entropy-coding stage** — encode time is dominated by match-finding
and extension — so a faster `MatchLen` shows up end-to-end (unlike codecs whose
time goes to FSE/Huffman).

## The compressor

The parse is a single-cell hash table keyed on a **6-byte sequence** (the
reference LZ4 / `pierrec` fast-mode hash, far better dispersed than a 4-byte
key). Each step probes three adjacent positions (`ip`, `ip+1`, `ip+2`) from one
8-byte load, inserting every position so later matches see more candidates, and
ramps its skip distance on incompressible spans. On a hit it applies **lazy
matching** — it peeks one byte ahead and, if `ip+1` yields a strictly longer
match, defers the current one — capped to short matches so the lookahead cost is
only paid where it can help. Match-length extension is delegated to
[matchlen](https://github.com/go-compressions/matchlen)'s SIMD kernel.

## Performance

Encoded as single LZ4 blocks, three representative corpora — two text
(Project Gutenberg `pg1661`, Mark Twain) and one binary (a kernel `bzImage`) —
`-count=8` medians, **as of 2026-06-14**.

**Native arm64 (Apple Silicon, this host):**

| corpus | this package | `pierrec/lz4` | speed vs `pierrec` | our size | `pierrec` size | our size vs `pierrec` |
|---|---:|---:|---:|---:|---:|---:|
| text `pg1661` | 208 MB/s | 301 MB/s | 0.69× | **0.528** | 0.553 | **−4.6%** |
| text Twain | 205 MB/s | 285 MB/s | 0.72× | **0.548** | 0.575 | **−4.6%** |
| binary `bzImage` | 249 MB/s | 370 MB/s | 0.67× | **0.638** | 0.653 | **−2.2%** |

**amd64 (QEMU x86_64 lima VM — TCG, so absolutes are low and noisy; the
compressed output is *byte-identical* to arm64 and decodes both ways with
`pierrec`):** our encoder lands at ≈0.5–0.7× `pierrec`'s TCG throughput, with the
same size advantage as above (the parse is deterministic, so sizes match arm64
exactly).

**Honest verdict.** This pass **beat `pierrec` on compression ratio** on every
corpus (text ≈4.6% smaller, binary ≈2.2% smaller) — the 6-byte hash, 3-position
probe and lazy matching are real wins, and they fixed the prior parse, which was
actually **7–8% *worse* than `pierrec` on text**. We did **not** beat `pierrec`
on **speed**: it stays ahead at ≈1.4× (we are at ~0.67–0.72× native arm64). That
gap is the parse/table, not the kernel — `pierrec` uses a half-the-size 16-bit
position table (better cache footprint) and skips lazy matching in fast mode,
trading a little ratio for speed; we make the opposite trade. The SIMD
`matchlen` extension is correct and in use, but match extension is only ~10% of
encode time here — the bottleneck is match-*finding*. Blocks remain mutually
decodable with `pierrec` in both directions, verified on arm64 and amd64.
(`matchlen` ships SIMD on all six 64-bit Go targets; native ppc64le/s390x
throughput is pending hardware.)

## License

BSD-3-Clause.
