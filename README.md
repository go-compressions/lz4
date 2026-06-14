# lz4

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

## Performance

Encoding a 1 MiB redundant corpus, `-count=6`/`-count=8` medians,
**re-benched as of 2026-06-14** (matchlen kernels unchanged — this is a
freshness/confirmation pass).

**Native arm64 (Apple Silicon, this host):**

| encoder | throughput | vs our scalar | vs `pierrec` |
|---|---:|---:|---:|
| this package, **matchlen SIMD** | ~4.5 GB/s | **~2.1×** | ~0.86× |
| this package, scalar match-count | ~2.1 GB/s | 1.0× | — |
| `pierrec/lz4` (reference) | ~5.3 GB/s | — | 1.0× |

**amd64 (QEMU x86_64 lima VM — TCG, so absolutes are low; the *ratios* are the signal):**

| encoder | throughput | vs our scalar | vs `pierrec` |
|---|---:|---:|---:|
| this package, **matchlen SIMD** | ~0.22 GB/s | **~1.9×** | ~0.67× |
| this package, scalar match-count | ~0.12 GB/s | 1.0× | — |
| `pierrec/lz4` (reference) | ~0.33 GB/s | — | 1.0× |

**Honest finding (verdict updated).** The SIMD common-prefix primitive is the
real, confirmed contribution: it gives **~1.9–2.1× over our scalar match-count**
on both arches, and lz4 inherits it for free via `matchlen.MatchLen`. However the
earlier "slightly ahead of `pierrec/lz4`" claim **did not reproduce** in this
pass — on both native arm64 (~0.86×) and the amd64 TCG VM (~0.67×) **`pierrec` is
ahead end-to-end**. That is expected and honest: `pierrec/lz4` is a mature,
heavily-tuned whole-block encoder, whereas this package pairs the fast `MatchLen`
primitive with a deliberately simple greedy compressor — so a faster *match
count* does not by itself overtake a better *parse/search* strategy. Compression
ratio stays essentially identical (≈0.065) and blocks remain mutually decodable.
The takeaway: **matchlen SIMD is a clear win over scalar match-counting; the
end-to-end gap to `pierrec` is closed by encoder tuning, not by the kernel.**
(`matchlen` ships SIMD on all six 64-bit Go targets; native ppc64le/s390x
throughput is pending hardware — see the `matchlen` repo for its per-arch
llvm-mca cycle estimates.)

## License

BSD-3-Clause.
