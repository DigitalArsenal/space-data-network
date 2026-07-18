package sdnruns

// flow_aot_test.go — validates that the baked OD runtime.wasm AOT-compiles via the
// shared FlatSQL AOT mechanism and that the AOT artifact loads + fits correctly
// through the real FlowPool. This is the local de-risk for the plugin's startup
// AOT step (interpreted WasmEdge runs the SGP4 fit ~1000x slower — the
// full-constellation bottleneck). Requires SDN_OD_RUNTIME_WASM + WasmEdge.

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
)

func oneObjectOEMStream(t *testing.T, norad uint32) []byte {
	t.Helper()
	buf := append([]byte(nil), issOEMFixtureBin...)
	if !OEM.GetRootAsOEM(buf, 0).MutateBlockNORAD(0, norad) {
		t.Fatalf("mutate NORAD")
	}
	stream := make([]byte, 4)
	binary.LittleEndian.PutUint32(stream[0:4], 1)
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(buf)))
	stream = append(stream, l[:]...)
	stream = append(stream, buf...)
	return stream
}

// fitOnce builds a 1-instance engine over the given runtime bytes and fits one
// object, returning the number of $OMM collected and the wall-clock.
func fitOnce(t *testing.T, runtime []byte, norad uint32) (int, time.Duration) {
	t.Helper()
	sink := NewCollectingSink(nopStoreSink{})
	eng, err := NewFlowRunEngineForOD(runtime, 1, stubStreamInvoker{stream: oneObjectOEMStream(t, norad)}, sink)
	if err != nil {
		t.Fatalf("NewFlowRunEngineForOD: %v", err)
	}
	start := time.Now()
	if _, err := eng.RunProvider(context.Background(), "intelsat", 0); err != nil {
		t.Fatalf("RunProvider: %v", err)
	}
	return len(sink.Collected()), time.Since(start)
}

func TestODRuntimeAOTCompilesAndFits(t *testing.T) {
	wasmPath := os.Getenv("SDN_OD_RUNTIME_WASM")
	if wasmPath == "" {
		t.Skip("SDN_OD_RUNTIME_WASM not set (baked OD runtime.wasm asset)")
	}
	runtimeWasm, err := os.ReadFile(wasmPath)
	if err != nil || len(runtimeWasm) == 0 {
		t.Fatalf("read baked OD runtime.wasm %q: %v", wasmPath, err)
	}

	// AOT-compile via the shared mechanism the plugin uses at startup.
	aotDir := t.TempDir()
	tStart := time.Now()
	aotWasm, err := flatsqlrt.EnsureAOTArtifact(aotDir, "od-runtime", runtimeWasm)
	if err != nil {
		t.Fatalf("EnsureAOTArtifact: %v", err)
	}
	compileMs := time.Since(tStart)
	if len(aotWasm) == 0 {
		t.Fatalf("AOT artifact is empty")
	}
	t.Logf("AOT compile: %d bytes -> %d bytes in %v", len(runtimeWasm), len(aotWasm), compileMs)

	// The AOT path must fit correctly (1 $OMM for the object).
	nAOT, dAOT := fitOnce(t, aotWasm, 70011)
	if nAOT != 1 {
		t.Fatalf("AOT fit produced %d $OMM, want 1", nAOT)
	}
	// Interpreted baseline for the speedup log (not asserted — machine-dependent).
	nInterp, dInterp := fitOnce(t, runtimeWasm, 70012)
	if nInterp != 1 {
		t.Fatalf("interpreted fit produced %d $OMM, want 1", nInterp)
	}
	t.Logf("fit wall-clock: AOT=%v interpreted=%v (speedup ~%.1fx)", dAOT, dInterp,
		float64(dInterp)/float64(dAOT))
}
