// Command gen writes the LZ4 block decoder's fast loop with go-asmgen:
//
//	decode_amd64.s  fastSeqs (16-byte SSE moves)
//	decode_arm64.s  fastSeqs (16-byte NEON moves)
//
//	func fastSeqs(src, dst []byte, ip, di int) (int, int)
//
// fastSeqs decodes whole sequences from src[ip:] into dst[di:] while every
// bound is comfortably clear, and returns the (ip, di) of the first sequence it
// did not do. It never stops inside a sequence and writes nothing it cannot
// afford: the caller (DecodeBlock) finishes the block, and reports every error,
// with the careful Go code, which re-parses from the returned sequence start.
//
// A sequence is left to the caller when
//
//   - fewer than 32 source or 48 destination bytes remain at its start;
//   - its literals or match need more than the 16-byte wild copy's margin
//     (ip+ll+18 <= len(src) and di+ll+16 <= len(dst), likewise di+ml+16);
//   - its offset is 0, beyond the output so far, or under 16 (the match then
//     overlaps itself within one 16-byte move);
//   - a length extension would run into the last 16 source bytes.
//
// Literals and matches are copied 16 bytes at a time and may write up to 15
// bytes past their end, inside dst; the next write overwrites them.
//
// Run from this directory: go run .
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/amd64"
	"github.com/go-asmgen/asmgen/arm64"
	"github.com/go-asmgen/asmgen/emit"
)

func sig() abi.Signature {
	return abi.LayoutArgs(
		[]abi.Arg{abi.Slice("src"), abi.Slice("dst"), abi.Scalar("ip", abi.Int64), abi.Scalar("di", abi.Int64)},
		[]abi.Arg{abi.Scalar("ret", abi.Int64), abi.Scalar("ret1", abi.Int64)},
	)
}

// amd64: SI=src, R8=len(src), DI=dst, R9=len(dst), R10=ip, R11=di;
// R14/R15 = start of the current sequence; R12 = match nibble.
func genAMD64(f *emit.File) {
	b := amd64.NewFunc("fastSeqs", sig(), 0)
	b.LoadArg("src_base", "SI").LoadArg("src_len", "R8").LoadArg("dst_base", "DI").LoadArg("dst_len", "R9").
		LoadArg("ip", "R10").LoadArg("di", "R11").
		Raw("PCALIGN $32").
		Label("loop").
		Raw("LEAQ 32(R10), AX").Raw("CMPQ AX, R8").Raw("JA done").
		Raw("LEAQ 48(R11), AX").Raw("CMPQ AX, R9").Raw("JA done").
		Raw("MOVQ R10, R14").Raw("MOVQ R11, R15").
		Raw("MOVBQZX (SI)(R10*1), AX").Raw("INCQ R10").
		Raw("MOVQ AX, BX").Raw("SHRQ $4, BX").
		Raw("ANDQ $15, AX").Raw("MOVQ AX, R12").
		// Common case, no loops: literal run < 15 (one 16-byte move) and match
		// nibble < 15 (length <= 18, two 16-byte moves). The 32-byte margins
		// checked at the top of the loop cover every read and write.
		Raw("CMPQ BX, $14").Raw("JA general").
		Raw("CMPQ R12, $15").Raw("JE general").
		Raw("MOVOU (SI)(R10*1), X0").Raw("MOVOU X0, (DI)(R11*1)").
		Raw("ADDQ BX, R10").Raw("ADDQ BX, R11").
		Raw("MOVWQZX (SI)(R10*1), CX").Raw("ADDQ $2, R10").
		Raw("LEAQ -1(CX), DX").Raw("CMPQ DX, R11").Raw("JAE exitseq").
		Raw("CMPQ CX, $8").Raw("JB exitseq").
		Raw("MOVQ R11, DX").Raw("SUBQ CX, DX").Raw("LEAQ (DI)(DX*1), DX").Raw("LEAQ (DI)(R11*1), AX").
		// 8+8+2 bytes, as liblz4 and pierrec do: a 16-byte load that straddles two
		// recent smaller stores cannot be forwarded and stalls; 8-byte loads inside
		// one earlier store can. Valid for offsets >= 8: each 8-byte piece reads
		// bytes that are already written.
		Raw("MOVQ (DX), BX").Raw("MOVQ BX, (AX)").Raw("MOVQ 8(DX), BX").Raw("MOVQ BX, 8(AX)").
		Raw("MOVWQZX 16(DX), BX").Raw("MOVW BX, 16(AX)").
		Raw("LEAQ 4(R12), BX").Raw("ADDQ BX, R11").Raw("JMP loop").
		Label("general").
		Raw("CMPQ BX, $15").Raw("JNE litok").
		Label("litext").
		Raw("LEAQ 16(R10), AX").Raw("CMPQ AX, R8").Raw("JAE exitseq").
		Raw("MOVBQZX (SI)(R10*1), AX").Raw("INCQ R10").Raw("ADDQ AX, BX").
		Raw("CMPQ AX, $255").Raw("JE litext").
		Label("litok").
		Raw("LEAQ 18(R10)(BX*1), AX").Raw("CMPQ AX, R8").Raw("JA exitseq").
		Raw("LEAQ 16(R11)(BX*1), AX").Raw("CMPQ AX, R9").Raw("JA exitseq").
		Raw("LEAQ (SI)(R10*1), DX").Raw("LEAQ (DI)(R11*1), AX").Raw("MOVQ BX, CX").
		Label("litcopy").
		Raw("MOVOU (DX), X0").Raw("MOVOU X0, (AX)").Raw("ADDQ $16, DX").Raw("ADDQ $16, AX").
		Raw("SUBQ $16, CX").Raw("JG litcopy").
		Raw("ADDQ BX, R10").Raw("ADDQ BX, R11").
		Raw("MOVWQZX (SI)(R10*1), AX").Raw("ADDQ $2, R10").
		Raw("TESTQ AX, AX").Raw("JZ exitseq").
		Raw("CMPQ AX, R11").Raw("JA exitseq").
		Raw("CMPQ AX, $16").Raw("JB exitseq").
		Raw("LEAQ 4(R12), BX").
		Raw("CMPQ R12, $15").Raw("JNE mlok").
		Label("mlext").
		Raw("LEAQ 16(R10), CX").Raw("CMPQ CX, R8").Raw("JAE exitseq").
		Raw("MOVBQZX (SI)(R10*1), CX").Raw("INCQ R10").Raw("ADDQ CX, BX").
		Raw("CMPQ CX, $255").Raw("JE mlext").
		Label("mlok").
		Raw("LEAQ 16(R11)(BX*1), CX").Raw("CMPQ CX, R9").Raw("JA exitseq").
		Raw("MOVQ R11, CX").Raw("SUBQ AX, CX").
		Raw("LEAQ (DI)(CX*1), DX").Raw("LEAQ (DI)(R11*1), AX").Raw("MOVQ BX, CX").
		Label("mcopy").
		Raw("MOVOU (DX), X0").Raw("MOVOU X0, (AX)").Raw("ADDQ $16, DX").Raw("ADDQ $16, AX").
		Raw("SUBQ $16, CX").Raw("JG mcopy").
		Raw("ADDQ BX, R11").Raw("JMP loop").
		Label("exitseq").
		Raw("MOVQ R14, R10").Raw("MOVQ R15, R11").
		Label("done").
		StoreRet("R10", "ret").StoreRet("R11", "ret1").Ret()
	f.Add(b.Func())
}

// arm64: R0=src, R1=len(src), R2=dst, R3=len(dst), R4=ip, R5=di;
// R14/R15 = start of the current sequence; R9 = match nibble.
func genARM64(f *emit.File) {
	b := arm64.NewFunc("fastSeqs", sig(), 0)
	b.LoadArg("src_base", "R0").LoadArg("src_len", "R1").LoadArg("dst_base", "R2").LoadArg("dst_len", "R3").
		LoadArg("ip", "R4").LoadArg("di", "R5").
		Label("loop").
		Raw("ADD $32, R4, R6").Raw("CMP R1, R6").Raw("BHI done").
		Raw("ADD $48, R5, R6").Raw("CMP R3, R6").Raw("BHI done").
		Raw("MOVD R4, R14").Raw("MOVD R5, R15").
		Raw("MOVBU (R0)(R4), R7").Raw("ADD $1, R4").
		Raw("LSR $4, R7, R8").Raw("AND $15, R7, R9").
		Raw("CMP $15, R8").Raw("BNE litok").
		Label("litext").
		Raw("ADD $16, R4, R6").Raw("CMP R1, R6").Raw("BHS exitseq").
		Raw("MOVBU (R0)(R4), R6").Raw("ADD $1, R4").Raw("ADD R6, R8").
		Raw("CMP $255, R6").Raw("BEQ litext").
		Label("litok").
		Raw("ADD R4, R8, R6").Raw("ADD $18, R6").Raw("CMP R1, R6").Raw("BHI exitseq").
		Raw("ADD R5, R8, R6").Raw("ADD $16, R6").Raw("CMP R3, R6").Raw("BHI exitseq").
		Raw("ADD R0, R4, R10").Raw("ADD R2, R5, R11").Raw("MOVD R8, R12").
		Label("litcopy").
		Raw("VLD1.P 16(R10), [V0.B16]").Raw("VST1.P [V0.B16], 16(R11)").
		Raw("SUBS $16, R12").Raw("BGT litcopy").
		Raw("ADD R8, R4").Raw("ADD R8, R5").
		Raw("MOVHU (R0)(R4), R6").Raw("ADD $2, R4").
		Raw("CBZ R6, exitseq").
		Raw("CMP R5, R6").Raw("BHI exitseq").
		Raw("CMP $16, R6").Raw("BLO exitseq").
		Raw("ADD $4, R9, R8").
		Raw("CMP $15, R9").Raw("BNE mlok").
		Label("mlext").
		Raw("ADD $16, R4, R7").Raw("CMP R1, R7").Raw("BHS exitseq").
		Raw("MOVBU (R0)(R4), R7").Raw("ADD $1, R4").Raw("ADD R7, R8").
		Raw("CMP $255, R7").Raw("BEQ mlext").
		Label("mlok").
		Raw("ADD R5, R8, R7").Raw("ADD $16, R7").Raw("CMP R3, R7").Raw("BHI exitseq").
		Raw("SUB R6, R5, R7").Raw("ADD R2, R7, R10").Raw("ADD R2, R5, R11").Raw("MOVD R8, R12").
		Label("mcopy").
		Raw("VLD1.P 16(R10), [V0.B16]").Raw("VST1.P [V0.B16], 16(R11)").
		Raw("SUBS $16, R12").Raw("BGT mcopy").
		Raw("ADD R8, R5").Raw("B loop").
		Label("exitseq").
		Raw("MOVD R14, R4").Raw("MOVD R15, R5").
		Label("done").
		StoreRet("R4", "ret").StoreRet("R5", "ret1").Ret()
	f.Add(b.Func())
}

func write(name, text string) {
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote", name)
}

func main() {
	fa := emit.NewFile("amd64")
	genAMD64(fa)
	write("../../decode_amd64.s", fa.String())
	fn := emit.NewFile("arm64")
	genARM64(fn)
	write("../../decode_arm64.s", fn.String())
}
