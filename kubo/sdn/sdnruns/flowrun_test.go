package sdnruns

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/ipfs/kubo/sdn/flowrt"
)

type stubInvoker struct {
	out []byte
	err error
}

func (s stubInvoker) InvokePull(_ context.Context, _ string) ([]byte, error) { return s.out, s.err }

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
	res, err := eng.RunProvider(context.Background(), "spacex")
	if err != nil {
		t.Fatalf("RunProvider: %v", err)
	}
	if res.Objects != 0 || res.Fitted != 0 {
		t.Fatalf("empty stream: got %+v, want zero", res)
	}
}

func TestFlowRunEngineInvokeErrorPropagates(t *testing.T) {
	eng, _ := NewFlowRunEngine([]byte{0x00}, 2, 1024, stubInvoker{err: errors.New("boom")}, nil, stubBatchCfg)
	if _, err := eng.RunProvider(context.Background(), "spacex"); err == nil {
		t.Fatalf("expected the invoke error to propagate")
	}
}

func TestFlowRunEngineBadStreamErrors(t *testing.T) {
	bad := make([]byte, 4)
	binary.LittleEndian.PutUint32(bad, 1) // claims 1 record, provides no data
	eng, _ := NewFlowRunEngine([]byte{0x00}, 2, 1024, stubInvoker{out: bad}, nil, stubBatchCfg)
	if _, err := eng.RunProvider(context.Background(), "spacex"); err == nil {
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
