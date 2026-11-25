package flowrt

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
	"testing"
	"time"
)

// Both dispatch exports run an infinite WebAssembly loop with one i32 argument.
const budgetLoopHex = "0061736d0100000001060160017f017f030201000503010001077203066d656d6f727902002673706163655f646174615f6d6f64756c655f72756e74696d655f647261696e5f6c696e6b656400003c73706163655f646174615f6d6f64756c655f72756e74696d655f64697370617463685f63757272656e745f696e766f636174696f6e5f64697265637400000a0b01090003400c000b41000b"

func TestServiceBudgetGuestDispatch(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "linked", true: "fallback"}[fallback], func(t *testing.T) {
			b, _ := hex.DecodeString(budgetLoopHex)
			mod, err := wasmrt.NewModule(b, wasmrt.WithExecTimeout(5*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			defer mod.Release()
			sf := &ServiceFlow{deps: FlowMountDeps{ExecutionTimeout: 80 * time.Millisecond}}
			ctx, cancel := sf.executionContext(context.Background())
			defer cancel()
			rt := &FlowRuntime{mod: mod, hasDrainLinked: true}
			started := time.Now()
			if fallback {
				err = rt.dispatchDirect(ctx, 0)
			} else {
				_, err = rt.Drain(ctx, HandlerMap{}, DrainOptions{})
			}
			if !errors.Is(err, wasmrt.ErrExecutionTimeout) {
				t.Fatalf("dispatch error: %v", err)
			}
			if time.Since(started) < 40*time.Millisecond {
				t.Fatal("service grant did not replace the 5ms runtime default")
			}
		})
	}
}

func TestServiceBudgetParentAndDefault(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	sf := &ServiceFlow{}
	unchanged, done := sf.executionContext(parent)
	defer done()
	if unchanged != parent {
		t.Fatal("unconfigured service altered caller context")
	}
	sf.deps.ExecutionTimeout = time.Minute
	ctx, done := sf.executionContext(parent)
	defer done()
	want, _ := parent.Deadline()
	got, _ := ctx.Deadline()
	if !got.Equal(want) {
		t.Fatal("service grant extended caller deadline")
	}
	cancel()
	if _, err := sf.FireTrigger(ctx, "unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled trigger: %v", err)
	}
}

func TestServiceBudgetRejectsInvalidConfiguration(t *testing.T) {
	for _, raw := range []string{"0s", "-1m", "ten minutes"} {
		if _, err := LoadFlowServices([]config.FlowService{{Flow: "absent", ExecutionTimeout: raw}}, FlowMountDeps{}); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

// Inject dispatch failures; the real guest still supplies and completes the host-loop work.
const failedLinkedDrainHex = "0061736d0100000001140460017f017f6000017f60027f7f017f60017f000308070000010200030105030100010606017f0141000b07d602082673706163655f646174615f6d6f64756c655f72756e74696d655f647261696e5f6c696e6b656400003c73706163655f646174615f6d6f64756c655f72756e74696d655f64697370617463685f63757272656e745f696e766f636174696f6e5f6469726563740001066d656d6f727902002e73706163655f646174615f6d6f64756c655f72756e74696d655f6765745f72656164795f6e6f64655f696e64657800022f73706163655f646174615f6d6f64756c655f72756e74696d655f626567696e5f6e6f64655f696e766f636174696f6e00033d5f73706163655f646174615f6d6f64756c655f72756e74696d655f64697370617463685f63757272656e745f696e766f636174696f6e5f64697265637400043273706163655f646174615f6d6f64756c655f72756e74696d655f636f6d706c6574655f6e6f64655f696e766f636174696f6e000509636f6d706c6574656400060a3507040041000b040041000b0c002300047f417f0541000b0b040041010b0b004100410136020041000b0600410124000b040023000b"

func TestServiceWithoutBudgetKeepsHostFallback(t *testing.T) {
	b, _ := hex.DecodeString(failedLinkedDrainHex)
	mod, err := wasmrt.NewModule(b)
	if err != nil {
		t.Fatal(err)
	}
	defer mod.Release()
	sf := &ServiceFlow{}
	ctx, cancel := sf.executionContext(context.Background())
	defer cancel()
	rt := &FlowRuntime{mod: mod, hasDrainLinked: true, nodeInfo: []flowNodeInfo{{DispatchModel: "linked-direct"}}}
	result, err := rt.drain(ctx, HandlerMap{}, DrainOptions{}, func(ctx context.Context, name string, args ...interface{}) ([]interface{}, error) {
		if name == runtimeExportDrainLinked || name == runtimeExportDispatchCurrentInvocation {
			return nil, errors.New("injected dispatch failure")
		}
		return mod.ExecuteContext(ctx, name, args...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NodesInvoked != 1 || result.HandlersSkipped != 1 {
		t.Fatalf("host loop did not run: %+v", result)
	}
	v, err := mod.Execute("completed")
	if err != nil || wasmrt.ToInt32(v[0]) != 1 {
		t.Fatalf("not completed: %v %v", v, err)
	}
	mem, err := mod.ReadMemory(0, 4)
	if err != nil || mem[0] != 1 {
		t.Fatalf("alternate export was not called: %v %v", mem, err)
	}
}
func TestDirectDispatchCancellation(t *testing.T) {
	b, _ := hex.DecodeString(failedLinkedDrainHex)
	mod, err := wasmrt.NewModule(b)
	if err != nil {
		t.Fatal(err)
	}
	defer mod.Release()
	rt := &FlowRuntime{mod: mod}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rt.dispatchDirect(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	v, err := mod.Execute("completed")
	if err != nil || wasmrt.ToInt32(v[0]) != 0 {
		t.Fatalf("canceled call completed: %v %v", v, err)
	}
}

// buildTriggerTestWasm constructs a minimal valid WASM module exposing the
// exports FireTrigger uses: memory, malloc, enqueue_trigger_frame, drain_linked
// (either an infinite loop to test async interruption, or an immediate return
// 0), and get_ready_node_index (returns -1 / InvalidIndex).
func buildTriggerTestWasm(loopDrain bool) []byte {
	sec := func(id byte, payload []byte) []byte {
		out := []byte{id}
		out = appendULEB(out, uint32(len(payload)))
		return append(out, payload...)
	}
	str := func(s string) []byte {
		out := appendULEB(nil, uint32(len(s)))
		return append(out, s...)
	}
	// Types:
	// 0: (i32) -> (i32)       malloc, drain_linked
	// 1: (i32, i32) -> (i32)  enqueue_trigger_frame
	// 2: () -> (i32)          get_ready_node_index
	types := []byte{
		3,
		0x60, 1, 0x7f, 1, 0x7f,
		0x60, 2, 0x7f, 0x7f, 1, 0x7f,
		0x60, 0, 1, 0x7f,
	}
	funcs := []byte{4, 0, 1, 0, 2}
	mem := []byte{1, 0x00, 1}
	var exports []byte
	exports = append(exports, 5)
	addExp := func(name string, kind byte, idx byte) {
		exports = append(exports, str(name)...)
		exports = append(exports, kind, idx)
	}
	addExp("memory", 0x02, 0)
	addExp("malloc", 0x00, 0)
	addExp(runtimeExportEnqueueTriggerFrame, 0x00, 1)
	addExp(runtimeExportDrainLinked, 0x00, 2)
	addExp(runtimeExportReadyNode, 0x00, 3)

	// Function bodies:
	// 0 (malloc): return 1024 (i32.const 1024 = 0x41 0x80 0x08)
	bodyMalloc := []byte{0, 0x41, 0x80, 0x08, 0x0b}
	// 1 (enqueue_trigger_frame): return 0
	bodyEnqueue := []byte{0, 0x41, 0x00, 0x0b}
	// 2 (drain_linked): infinite loop if loopDrain, else return 0
	bodyDrain := []byte{0, 0x41, 0x00, 0x0b}
	if loopDrain {
		bodyDrain = []byte{0, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x41, 0x00, 0x0b}
	}
	// 3 (get_ready_node_index): return -1 (0x7f signed LEB128)
	bodyReady := []byte{0, 0x41, 0x7f, 0x0b}

	var code []byte
	code = append(code, 4)
	for _, b := range [][]byte{bodyMalloc, bodyEnqueue, bodyDrain, bodyReady} {
		code = appendULEB(code, uint32(len(b)))
		code = append(code, b...)
	}

	var wasm []byte
	wasm = append(wasm, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	wasm = append(wasm, sec(1, types)...)
	wasm = append(wasm, sec(3, funcs)...)
	wasm = append(wasm, sec(5, mem)...)
	wasm = append(wasm, sec(7, exports)...)
	wasm = append(wasm, sec(10, code)...)
	return wasm
}

func appendULEB(dst []byte, v uint32) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		dst = append(dst, b)
		if v == 0 {
			return dst
		}
	}
}

func TestFireTriggerDiscardsInterruptedInstance(t *testing.T) {
	// Interrupted run: even with releaseAfterRun=false, FireTrigger must discard
	// the interrupted WASM instance so the next run loads a fresh one instead of
	// reusing torn linear-memory/guest state.
	loopMod, err := wasmrt.NewModule(buildTriggerTestWasm(true))
	if err != nil {
		t.Fatal(err)
	}
	sf := &ServiceFlow{
		programID: "test.cancel-discard",
		triggers:  []serviceTrigger{{TriggerID: "tick", Index: 0, PortID: "tick"}},
		runBytes:  []byte{0x00, 0x61, 0x73, 0x6d},
		inst: &flowInstance{
			rt:     &FlowRuntime{mod: loopMod, hasDrainLinked: true},
			bridge: modulert.NewHostBridge(nil, nil),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := sf.FireTrigger(ctx, "tick"); err == nil {
		t.Fatal("expected cancelled FireTrigger to return an error")
	}
	if sf.inst != nil {
		t.Fatal("interrupted FireTrigger retained its WASM instance; expected it to be discarded")
	}

	// Clean run: with releaseAfterRun=false, a healthy drain keeps its instance.
	cleanMod, err := wasmrt.NewModule(buildTriggerTestWasm(false))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanMod.Release()
	sfClean := &ServiceFlow{
		programID: "test.clean-retain",
		triggers:  []serviceTrigger{{TriggerID: "tick", Index: 0, PortID: "tick"}},
		runBytes:  []byte{0x00, 0x61, 0x73, 0x6d},
		inst: &flowInstance{
			rt:     &FlowRuntime{mod: cleanMod, hasDrainLinked: true},
			bridge: modulert.NewHostBridge(nil, nil),
		},
	}
	if _, err := sfClean.FireTrigger(context.Background(), "tick"); err != nil {
		t.Fatalf("clean FireTrigger failed: %v", err)
	}
	if sfClean.inst == nil {
		t.Fatal("clean FireTrigger discarded its WASM instance when releaseAfterRun=false")
	}
}
