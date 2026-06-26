// Module for the lz4 performance-parity harness. It is a SEPARATE module from
// github.com/go-compressions/lz4 so `go test ./...` and the coverage gate at the
// repo root never descend into it — the harness is a measurement tool, not part
// of the library's tested surface.
module github.com/go-compressions/lz4/benchmarks

go 1.26

require (
	github.com/go-compressions/lz4 v0.0.0
	github.com/pierrec/lz4/v4 v4.1.27
)

require (
	github.com/go-simd/matchlen v0.3.1 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/go-compressions/lz4 => ../
