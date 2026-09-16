package flatsqlrt

import (
	"runtime"
	"strings"

	"golang.org/x/sys/cpu"
)

// An AOT artifact is NATIVE CODE FOR THE CPU THAT COMPILED IT. WasmEdge's AOT
// compiler targets the host, so a machine with AVX-512 produces AVX-512
// instructions, and running that artifact on a CPU without them is an illegal
// instruction — not a slow path, a dead process.
//
// It happened exactly that way: the container image prewarms at BUILD time, so
// the artifact was compiled on whichever runner built the image and baked into
// an image meant to run anywhere. When the release install-test drew a runner
// without AVX-512 the node died on boot with
//
//	SIGILL: illegal instruction ... signal arrived during cgo execution
//	instruction bytes: 0x62 0xf1 0x76 0x8 ...   (0x62 is the EVEX prefix)
//	goroutine ... WasmEdge_VMExecuteRegistered
//
// and it looked intermittent only because it depended on which CPU generation
// each runner happened to be. The cache key held the engine hash and the
// libwasmedge version but nothing about the CPU, so a mismatched artifact was
// indistinguishable from a correct one.
//
// Including the microarchitecture level turns that into a cache MISS, which is
// recoverable, instead of a crash, which is not.

// aotCPUProfile identifies the instruction set an artifact was compiled for.
//
// x86-64 uses the psABI microarchitecture levels, which is what compilers
// actually key codegen on. Anything below v2 is reported as v1; the exact
// feature set below that does not vary in ways WasmEdge's output depends on.
// Other architectures report GOARCH alone: arm64 server CPUs do not differ in
// the baseline the way x86-64 does, and inventing finer detail would evict
// caches for no benefit.
func aotCPUProfile() string {
	if runtime.GOARCH != "amd64" {
		return runtime.GOARCH
	}
	switch {
	case cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512CD &&
		cpu.X86.HasAVX512DQ && cpu.X86.HasAVX512VL:
		return "x86-64-v4"
	case cpu.X86.HasAVX2 && cpu.X86.HasBMI1 && cpu.X86.HasBMI2 &&
		cpu.X86.HasFMA && cpu.X86.HasAVX:
		return "x86-64-v3"
	case cpu.X86.HasSSE41 && cpu.X86.HasSSE42 && cpu.X86.HasPOPCNT:
		return "x86-64-v2"
	default:
		return "x86-64-v1"
	}
}

// sanitizeAOTKeyPart keeps a key component safe for a filename.
func sanitizeAOTKeyPart(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			r == '.' || r == '-' {
			return r
		}
		return '_'
	}, s)
}
