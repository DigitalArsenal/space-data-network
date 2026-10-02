package wasmrt

import (
	"os"
	"testing"
)

// 04-atomic-memarg-offset: AOT memory.atomic.notify and wait32 act on the
// operand plus the instruction's memarg offset. The upstream AOT compiler
// dropped the offset, so wasi-libc's thread-list-lock wakeups went to address
// 0 and thread exit/join hung. The probe's checks hold interpreted on every
// runtime; compiled, only on the patched one, which then tags its threaded AOT
// artifacts "sdn3" (a runtime without the patch never loads them).
func TestAOTAtomicMemargOffset(t *testing.T) {
	requirePatchedRuntime(t)
	interp, err := probeMemargOffset(substrateProbeWasm)
	if err != nil {
		t.Fatal(err)
	}
	if !interp {
		t.Fatal("interpreted: memarg-offset notify/wait failed (the probe itself is wrong)")
	}
	compiled, err := compileProbeAOT()
	if err != nil {
		t.Fatal(err)
	}
	aot, err := probeMemargOffset(compiled)
	if err != nil {
		t.Fatal(err)
	}
	if !aot {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatal("AOT: memarg-offset notify/wait failed: the runtime lacks 04-atomic-memarg-offset")
		}
		t.Skip("linked libwasmedge lacks 04-atomic-memarg-offset")
	}
	rep := RunSubstrateSelfTest(SubstrateOptions{})
	if !rep.AOTAtomicMemargOffset || !rep.Patched() {
		t.Fatalf("self-test: memarg=%t patched=%t (%+v)", rep.AOTAtomicMemargOffset, rep.Patched(), rep)
	}
}
