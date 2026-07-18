package sdnruns

// flow_fullstack_test.go — LOCAL FULL-STACK verification of the assembled OD run
// engine (owner pre-deploy gate). It drives the REAL production path end-to-end
// over the REAL baked OD runtime.wasm: FlowRunner.Run -> engine.RunProvider ->
// (stub provider $OEM stream) -> RunOEMStream -> FlowPool feeder -> od.fit -> store
// (CollectingSink) -> run record. Only the provider MODULE invoke is stubbed (that
// half — provider MEME fetch -> $OEM stream — is proven network-free by the spacex
// module's own module.test.mjs); everything else is the actual assembly the
// sdnruntime plugin wires. Asserts the run records one distinct $OMM per object.
//
// Requires the baked asset (SDN_OD_RUNTIME_WASM) + WasmEdge; skipped otherwise.

import (
	"context"
	_ "embed"
	"encoding/binary"
	"os"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	"github.com/ipfs/go-cid"
)

//go:embed testdata/iss_oem.bin
var issOEMFixtureBin []byte

// stubStreamInvoker returns a fixed provider $OEM stream (stands in for the
// modulert provider-module invoke, which is proven separately).
type stubStreamInvoker struct{ stream []byte }

func (s stubStreamInvoker) InvokePull(context.Context, string) ([]byte, error) {
	return s.stream, nil
}

// nopStoreSink is the inner StoreSink under the CollectingSink: it accepts the
// persisted record (no real store needed for the assembly test).
type nopStoreSink struct{}

func (nopStoreSink) Store(context.Context, string, string, []byte) (cid.Cid, error) {
	return cid.Undef, nil
}

func TestFlowRunnerEndToEnd(t *testing.T) {
	wasmPath := os.Getenv("SDN_OD_RUNTIME_WASM")
	if wasmPath == "" {
		t.Skip("SDN_OD_RUNTIME_WASM not set (baked OD runtime.wasm asset)")
	}
	runtimeWasm, err := os.ReadFile(wasmPath)
	if err != nil || len(runtimeWasm) == 0 {
		t.Fatalf("read baked OD runtime.wasm %q: %v", wasmPath, err)
	}
	if len(issOEMFixtureBin) < 12 || string(issOEMFixtureBin[4:8]) != "$OEM" {
		t.Fatalf("embedded $OEM fixture invalid (len=%d)", len(issOEMFixtureBin))
	}

	// Frame K distinct-NORAD $OEM records into a provider stream ([u32le count]
	// then K x [u32le len][$OEM]) — the exact shape a provider's pull returns.
	const K = 6
	const baseNORAD = 70000
	stream := make([]byte, 4)
	binary.LittleEndian.PutUint32(stream[0:4], K)
	wantN := map[uint32]bool{}
	for k := 0; k < K; k++ {
		buf := append([]byte(nil), issOEMFixtureBin...)
		norad := uint32(baseNORAD + k)
		if !OEM.GetRootAsOEM(buf, 0).MutateBlockNORAD(0, norad) {
			t.Fatalf("mutate NORAD k=%d", k)
		}
		var l [4]byte
		binary.LittleEndian.PutUint32(l[:], uint32(len(buf)))
		stream = append(stream, l[:]...)
		stream = append(stream, buf...)
		wantN[norad] = true
	}

	// Assemble the ACTUAL production run engine.
	sink := NewCollectingSink(nopStoreSink{})
	engine, err := NewFlowRunEngineForOD(runtimeWasm, 4, stubStreamInvoker{stream: stream}, sink)
	if err != nil {
		t.Fatalf("NewFlowRunEngineForOD: %v", err)
	}
	runStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	runner, err := NewFlowRunner(engine, sink, runStore,
		func() RunConfig { return RunConfig{EnabledProviders: []string{"spacex"}, ProducedSource: DefaultProducedSource} },
		t.Logf)
	if err != nil {
		t.Fatalf("NewFlowRunner: %v", err)
	}

	run, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("FlowRunner.Run: %v", err)
	}
	if run.Status != StatusCompleted {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
	if run.ObjectsDone != K {
		t.Fatalf("objects_done = %d, want %d", run.ObjectsDone, K)
	}
	gotN := map[uint32]bool{}
	for _, o := range run.Objects {
		gotN[o.Norad] = true
	}
	if len(gotN) != K {
		t.Fatalf("recorded %d distinct NORADs, want %d", len(gotN), K)
	}
	for n := range wantN {
		if !gotN[n] {
			t.Fatalf("expected NORAD %d not in run record", n)
		}
	}
	t.Logf("★ FULL-STACK: FlowRunner.Run(provider spacex) -> baked OD flow -> %d distinct $OMM recorded (NORAD %d..%d), run %s completed. The assembled production run engine fits + records a constellation.",
		run.ObjectsDone, baseNORAD, baseNORAD+K-1, run.ID)
}
