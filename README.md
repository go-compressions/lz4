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
| this package, **matchlen SIMD** | **~4.5 GB/s** | **~2.0x** |
| this package, scalar match-count | ~2.25 GB/s | 1.0x |
| `pierrec/lz4` (reference, tuned) | ~5.3 GB/s | — |

So the SIMD common-prefix primitive **doubles encode throughput** here (with an
LZ4-style search-step acceleration over incompressible runs).
Compression ratio is identical to `pierrec/lz4` (≈0.065 on this corpus; ours is
marginally smaller), and blocks are mutually decodable. `pierrec/lz4` is faster
overall thanks to more match-finder tuning — this package is a compact reference
that isolates matchlen's contribution.

## License

BSD-3-Clause.
