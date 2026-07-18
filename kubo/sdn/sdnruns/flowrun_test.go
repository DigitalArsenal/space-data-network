package sdnruns

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	"github.com/ipfs/kubo/sdn/flowrt"
)

type stubInvoker struct {
	out []byte
	err error
}

// recordingInvoker answers a Probe with a u32le total and every batch with an
// empty $OEM stream (count=0, a RunOEMStream no-op), recording the batch windows
// it was asked for so the orchestration can be asserted.
type recordingInvoker struct {
	total   int
	mu      sync.Mutex
	probes  int
	batches []PullOpts
}

func (r *recordingInvoker) InvokePull(_ context.Context, _ string, opts PullOpts) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if opts.Probe {
		r.probes++
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(r.total))
		return b, nil
	}
	r.batches = append(r.batches, opts)
	return []byte{0, 0, 0, 0}, nil // empty $OEM stream
}

func TestRunBatchedProbesThenFansBatches(t *testing.T) {
	inv := &recordingInvoker{total: 10}
	eng, err := NewFlowRunEngine([]byte{0x00}, 2, 1024, inv, nil, stubBatchCfg)
	if err != nil {
		t.Fatalf("NewFlowRunEngine: %v", err)
	}
	eng.batched = map[string]bool{"spacex": true}
	eng.batchSize = 4
	eng.batchConc = 3
	if _, err := eng.RunProvider(context.Background(), "spacex", 100); err != nil {
		t.Fatalf("RunProvider: %v", err)
	}
	if inv.probes != 1 {
		t.Fatalf("probes = %d, want 1", inv.probes)
	}
	// total=10, batchSize=4 -> windows [0,4)[4,4)[8,2). Order is nondeterministic
	// (concurrent), so assert the SET of (offset,count) covers every object once.
	if len(inv.batches) != 3 {
		t.Fatalf("batches = %d, want 3", len(inv.batches))
	}
	covered := map[int]int{}
	for _, b := range inv.batches {
		if b.ObjectCap != 100 {
			t.Fatalf("batch objectCap = %d, want 100", b.ObjectCap)
		}
		for i := b.Offset; i < b.Offset+b.Count; i++ {
			covered[i]++
		}
	}
	for i := 0; i < 10; i++ {
		if covered[i] != 1 {
			t.Fatalf("object %d covered %d times, want 1", i, covered[i])
		}
	}
}

func TestBuildPullConfig(t *testing.T) {
	cases := []struct {
		opts PullOpts
		want string
	}{
		{PullOpts{ObjectCap: 0, Offset: -1, Count: -1}, ""}, // fallback (nil)
		{PullOpts{ObjectCap: 100, Offset: -1, Count: -1}, `{"objectCap":100}`},
		{PullOpts{ObjectCap: 100, Probe: true}, `{"objectCap":100,"probe":true}`},
		{PullOpts{ObjectCap: 100, Offset: 8, Count: 4}, `{"objectCap":100,"offset":8,"count":4}`},
		{PullOpts{Offset: 0, Count: 64}, `{"offset":0,"count":64}`},
	}
	for _, c := range cases {
		got := buildPullConfig(nil, c.opts)
		if c.want == "" {
			if got != nil {
				t.Fatalf("opts %+v: got %q, want nil fallback", c.opts, got)
			}
			continue
		}
		if string(got) != c.want {
			t.Fatalf("opts %+v: got %q, want %q", c.opts, got, c.want)
		}
	}
}

func (s stubInvoker) InvokePull(_ context.Context, _ string, _ PullOpts) ([]byte, error) { return s.out, s.err }

var stubBatchCfg = flowrt.OEMBatchConfig{FeederPluginID: "feeder", StorePluginID: "store"}

// These exercise the engine's orchestration (invoke -> split -> dispatch) without
// baking: the empty/error paths return before the pool is ever driven, so a dummy
// non-empty runtime.wasm never gets instantiated. The records>0 path (real fit) is
// covered by the flowrt bake tests (RunOEMBatch) + the local full-stack test.

func TestFlowRunEngineEmptyStreamIsNoop(t *testing.T) {
	stream := make([]byte, 4) // count = 0
	eng, err := NewFlowRunEngine([]byte{0x00}, 2, 1024, stubInvoker{out: stream}, nil, stubBatchCfg)
	if err != nil {
		t.Fatalf("NewFlowRunEngine: %v", err)
	}
	res, err := eng.RunProvider(context.Background(), "iss", 0)
	if err != nil {
		t.Fatalf("RunProvider: %v", err)
	}
	if res.Objects != 0 || res.Fitted != 0 {
		t.Fatalf("empty stream: got %+v, want zero", res)
	}
}

func TestFlowRunEngineInvokeErrorPropagates(t *testing.T) {
	eng, _ := NewFlowRunEngine([]byte{0x00}, 2, 1024, stubInvoker{err: errors.New("boom")}, nil, stubBatchCfg)
	if _, err := eng.RunProvider(context.Background(), "iss", 0); err == nil {
		t.Fatalf("expected the invoke error to propagate")
	}
}

func TestFlowRunEngineBadStreamErrors(t *testing.T) {
	bad := make([]byte, 4)
	binary.LittleEndian.PutUint32(bad, 1) // claims 1 record, provides no data
	eng, _ := NewFlowRunEngine([]byte{0x00}, 2, 1024, stubInvoker{out: bad}, nil, stubBatchCfg)
	if _, err := eng.RunProvider(context.Background(), "iss", 0); err == nil {
		t.Fatalf("expected a stream-split error")
	}
}

func TestNewFlowRunEngineValidates(t *testing.T) {
	if _, err := NewFlowRunEngine(nil, 2, 1024, stubInvoker{}, nil, stubBatchCfg); err == nil {
		t.Fatalf("expected error for empty runtime.wasm")
	}
	if _, err := NewFlowRunEngine([]byte{0x00}, 2, 1024, nil, nil, stubBatchCfg); err == nil {
		t.Fatalf("expected error for nil invoker")
	}
}
