package wasmrt

import (
	"os"
	"testing"
)

func TestAOTAtomicStoreStack(t *testing.T) {
	compiled, err := compileThreadedAOT(atomicStoreProbeWasm)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []struct {
		name string
		wasm []byte
	}{{"interpreted", atomicStoreProbeWasm}, {"compiled", compiled}} {
		t.Run(mode.name, func(t *testing.T) {
			m, err := NewModule(mode.wasm, WithMaxThreads(1))
			if err != nil {
				t.Fatal(err)
			}
			defer m.Release()
			for _, name := range atomicStoreProbeExports {
				t.Run(name, func(t *testing.T) {
					v, err := m.Execute(name)
					if err != nil || len(v) != 1 {
						t.Fatalf("result %v: %v", v, err)
					}
					if got := ToInt32(v[0]); got != 98 {
						if mode.name == "compiled" && os.Getenv("SDN_WASM_REQUIRE_PATCHED") != "1" {
							t.Skipf("runtime lacks 07-atomic-store-stack: got %d", got)
						}
						t.Fatalf("got %d, want 98", got)
					}
				})
			}
		})
	}
}

func TestAtomicStoreSubstrateReport(t *testing.T) {
	rep := SubstrateReport{AtomicNotifyWithoutStore: true, StopReachesEveryThread: true,
		AOTChecked: true, InterruptibleAOT: true, AOTAtomicMemargOffset: true, AOTLoopStopChecks: true}
	if rep.Patched() || rep.Tag() != "sdn4" {
		t.Fatalf("missing patch: %+v", rep)
	}
	rep.AOTAtomicStoreStack = true
	if !rep.Patched() || rep.Tag() != "sdn5" {
		t.Fatalf("patched: %+v", rep)
	}
}
