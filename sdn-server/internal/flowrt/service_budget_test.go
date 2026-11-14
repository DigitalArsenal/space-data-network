package flowrt

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/spacedatanetwork/sdn-server/internal/config"
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

// The linked drain traps. The host loop uses the alternate direct export and completes.
const failedLinkedDrainHex = "0061736d0100000001140460017f017f6000017f60027f7f017f60017f000308070001020000030105030100010606017f0141000b07d60208066d656d6f727902002673706163655f646174615f6d6f64756c655f72756e74696d655f647261696e5f6c696e6b656400002e73706163655f646174615f6d6f64756c655f72756e74696d655f6765745f72656164795f6e6f64655f696e64657800012f73706163655f646174615f6d6f64756c655f72756e74696d655f626567696e5f6e6f64655f696e766f636174696f6e00023c73706163655f646174615f6d6f64756c655f72756e74696d655f64697370617463685f63757272656e745f696e766f636174696f6e5f64697265637400033d5f73706163655f646174615f6d6f64756c655f72756e74696d655f64697370617463685f63757272656e745f696e766f636174696f6e5f64697265637400043273706163655f646174615f6d6f64756c655f72756e74696d655f636f6d706c6574655f6e6f64655f696e766f636174696f6e000509636f6d706c6574656400060a33070300000b0c002300047f417f0541000b0b040041010b0300000b0b004100410136020041000b0600410124000b040023000b"

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
	result, err := rt.Drain(ctx, HandlerMap{}, DrainOptions{})
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
