package flowrt

import (
	"errors"
	"strings"
	"testing"
)

// THE POOLED-INSTANCE POISONING these pin.
//
// An HTTP mount enqueues exactly ONE $HTQ frame per request and then hands the
// pooled instance to the next request. The trigger queue is instance state, so
// a frame no node consumed — a client that disconnected mid-drain, a guest
// entry node that refused before reading its port — survives the exchange.
// The next request enqueues its frame on top, the entry node sees TWO frames on
// a port declared single-stream, refuses in-guest, and the mount can only
// answer 502 "flow produced no HTTP response". The residue is never consumed,
// so the instance stays poisoned until the daemon restarts.
//
// Measured on host-01 2026-08-27: /api/v1/cellular/providers, /tiles/meta and
// /tiles/0/0/0 all 502 in ~13 ms with the flow loaded, healthy and fully
// approved (graph sdn-host01-cellular-providers-502).

type fakeExchangeRuntime struct {
	state  *FlowIngressRuntimeState
	err    error
	resets int
	asked  []uint32
}

func (f *fakeExchangeRuntime) GetIngressRuntimeState(index uint32) (*FlowIngressRuntimeState, error) {
	f.asked = append(f.asked, index)
	return f.state, f.err
}

func (f *fakeExchangeRuntime) ResetState() { f.resets++ }

func TestIngressResidueIsClearedBeforeTheInstanceIsPooled(t *testing.T) {
	rt := &fakeExchangeRuntime{state: &FlowIngressRuntimeState{QueuedFrames: 1, TotalReceived: 2}}

	queued, reset := discardIngressResidue(rt, 0)

	if !reset {
		t.Fatal("an exchange that left its request frame queued returned the instance to the pool " +
			"unreset: the NEXT request arrives as a batched input and the mount 502s forever")
	}
	if queued != 1 {
		t.Fatalf("queued = %d, want 1 (the count is what the operator sees)", queued)
	}
	if rt.resets != 1 {
		t.Fatalf("ResetState called %d times, want exactly 1", rt.resets)
	}
}

func TestCleanExchangeIsNotReset(t *testing.T) {
	// The overwhelming majority of exchanges consume their frame. Resetting
	// those would be a per-request guest call bought for nothing, and it would
	// hide the residue signal an operator needs.
	rt := &fakeExchangeRuntime{state: &FlowIngressRuntimeState{QueuedFrames: 0, TotalReceived: 9}}

	queued, reset := discardIngressResidue(rt, 0)

	if reset || queued != 0 {
		t.Fatalf("clean exchange reported residue: queued=%d reset=%v", queued, reset)
	}
	if rt.resets != 0 {
		t.Fatalf("ResetState called %d times on a clean exchange, want 0", rt.resets)
	}
}

func TestResidueGuardReadsTheMountsOwnTrigger(t *testing.T) {
	// A mount whose trigger is not index 0 must not inspect somebody else's
	// ingress: reading the wrong trigger would both miss real residue and
	// reset instances that were fine.
	rt := &fakeExchangeRuntime{state: &FlowIngressRuntimeState{QueuedFrames: 3}}

	discardIngressResidue(rt, 2)

	if len(rt.asked) != 1 || rt.asked[0] != 2 {
		t.Fatalf("ingress states read for %v, want exactly [2]", rt.asked)
	}
}

func TestUnreadableIngressStateNeverResets(t *testing.T) {
	// This is a diagnostic read on the success path of every request. If the
	// artifact cannot answer it, the request still stands: a guard may not turn
	// a served response into a failure.
	for name, rt := range map[string]*fakeExchangeRuntime{
		"error": {err: errors.New("no ingress states")},
		"nil":   {state: nil},
	} {
		queued, reset := discardIngressResidue(rt, 0)
		if reset || queued != 0 || rt.resets != 0 {
			t.Fatalf("%s: unreadable ingress state caused a reset (queued=%d reset=%v resets=%d)",
				name, queued, reset, rt.resets)
		}
	}
}

// A mounted flow that answers nothing is a host-visible refusal. Every node of
// a linked-direct artifact runs inside the guest, so its refusal is a wasm
// return value: Drain reports no error and no handler fires. The artifact's
// node-state table is the only record of the cause, and the HTTP lane used to
// throw it away — three live cellular routes served 502 for hours without one
// log line naming a node.

func TestNoResponseDiagnosisNamesTheRefusingNode(t *testing.T) {
	got := describeNodeRunDigest([]nodeRunOutcome{
		{NodeID: "cache_plan", PluginID: "com.digitalarsenal.data-source.cell-tower-ingest", MethodID: "cache_plan", Invocations: 1, LastStatus: 500},
		{NodeID: "route", PluginID: "com.digitalarsenal.data-source.cell-tower-source", MethodID: "route"},
		{NodeID: "respond", PluginID: "com.digitalarsenal.foundation.http-respond", MethodID: "respond"},
	})

	for _, want := range []string{
		"cache_plan(com.digitalarsenal.data-source.cell-tower-ingest:cache_plan) x1 last_status=500",
		"never reached: route, respond",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnosis does not name %q: %s", want, got)
		}
	}
}

func TestNoResponseDiagnosisReportsAFlowNothingEntered(t *testing.T) {
	// Nothing ran at all: the ingress frame was never accepted. "Which nodes
	// were idle" is the entire diagnosis in that case, and reporting "nodes
	// ran:" with an empty list would state the opposite of the truth.
	got := describeNodeRunDigest([]nodeRunOutcome{
		{NodeID: "cache_plan", PluginID: "p", MethodID: "cache_plan"},
		{NodeID: "respond", PluginID: "q", MethodID: "respond"},
	})

	if !strings.Contains(got, "NO node ran") || !strings.Contains(got, "cache_plan, respond") {
		t.Fatalf("a flow nothing entered was not reported as such: %s", got)
	}
}

func TestNoResponseDiagnosisSurvivesAnArtifactWithNoNodeState(t *testing.T) {
	got := describeNodeRunDigest(nil)
	if !strings.Contains(got, "no per-node state") {
		t.Fatalf("missing node state was not reported honestly: %s", got)
	}
}
