# lz4

A clean Go implementation of the **LZ4 block format** (`CompressBlock` /
`DecompressBlock`), wire-compatible with the reference (cross-checked against
`pierrec/lz4`). Its compressor delegates LZ4's hot "count the matching bytes"
inner loop (`LZ4_count`) to [matchlen](https://github.com/go-compressions/matchlen),
whose SIMD common-prefix kernel makes match extension fast.

```go
c := lz4.CompressBlock(src)
out, _ := lz4.DecompressBlock(c, len(src))
```

## Why LZ4 is the ideal matchlen consumer

LZ4 has **no entropy-coding stage** — encode time is dominated by match-finding
and extension — so a faster `MatchLen` shows up end-to-end (unlike codecs whose
time goes to FSE/Huffman).

## Performance

Encoding a 1 MiB redundant corpus (Apple Silicon, arm64):

| encoder | throughput | vs scalar |
|---|---|---|
| this package, **matchlen SIMD** | **~5.6 GB/s** | **~2.3x** |
| this package, scalar match-count | ~2.4 GB/s | 1.0x |
| `pierrec/lz4` (reference) | ~5.2 GB/s | — |

The SIMD common-prefix primitive gives **~2.3x** over a scalar match-count, and
with an LZ4-style search-step acceleration and a zero-init hash table the package
is **on par with — slightly ahead of — `pierrec/lz4`** here, at an essentially
identical compression ratio (≈0.065), blocks mutually decodable. (Measured
interleaved to cancel machine drift; results are corpus-dependent.)

## License

BSD-3-Clause.
