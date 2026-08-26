package flowrt

import (
	"errors"
	"strings"
	"testing"
)

// The cellular worldwide ingest ran three times on host-02, fetched 3,145,728 B
// each time and stored nothing, and the only thing written down anywhere was
// "run completed but landed no batch" (graph: sdn-cellular-ingest-lands-no-batch).
// The cause was in the guest the whole time: cell-tower-source's `parse` was
// handed a run contract naming a provider it had no entry for and dropped the
// body. Every node of that artifact is linked-direct, so its refusal never
// crossed the host boundary and Drain returned clean.
//
// retrievalDiagnosis is the host's answer to that: read the node-state table the
// guest already maintains and say WHICH node refused or WHICH node never ran.
// These assert the exact text a future operator reads out of the ledger.

func TestRetrievalDiagnosisNamesTheRefusingNode(t *testing.T) {
	sf := &ServiceFlow{lastNodeDigest: []nodeRunOutcome{
		{NodeID: "mark_read_q", PluginID: "com.digitalarsenal.data-source.cell-tower-ingest", MethodID: "mark_query", Invocations: 1},
		{NodeID: "http", PluginID: "com.digitalarsenal.hostcap.http-request", MethodID: "request", Invocations: 1},
		{NodeID: "parse", PluginID: "com.digitalarsenal.data-source.cell-tower-source", MethodID: "parse", Invocations: 1, LastStatus: 400},
		{NodeID: "ingest", PluginID: "com.digitalarsenal.hostcap.storage-ingest", MethodID: "ingest"},
	}}

	err := sf.retrievalDiagnosis(nil)
	if err == nil {
		t.Fatal("a clean run whose parse node returned 400 reported no cause at all")
	}
	got := err.Error()
	for _, want := range []string{
		"landed no batch",
		"parse (com.digitalarsenal.data-source.cell-tower-source:parse) status 400",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnosis does not name %q: %s", want, got)
		}
	}
	// A refusal outranks starvation: the node that said no is the cause, and the
	// nodes downstream of it are only its consequence.
	if strings.Contains(got, "never ran") {
		t.Fatalf("a refusal was reported as starvation: %s", got)
	}
}

func TestRetrievalDiagnosisNamesStarvedNodesWhenNothingRefused(t *testing.T) {
	// Nothing refused, but the tail of the pipeline never fired — the frames
	// stopped somewhere upstream. Naming the nodes that never ran is the whole
	// diagnosis in that case.
	sf := &ServiceFlow{lastNodeDigest: []nodeRunOutcome{
		{NodeID: "mark_read_q", PluginID: "p", MethodID: "mark_query", Invocations: 1},
		{NodeID: "plan", PluginID: "p", MethodID: "ingest_plan", Invocations: 1},
		{NodeID: "http", PluginID: "h", MethodID: "request"},
		{NodeID: "ingest", PluginID: "h", MethodID: "ingest"},
	}}

	err := sf.retrievalDiagnosis(nil)
	if err == nil {
		t.Fatal("a run whose fetch and store nodes never fired reported no cause")
	}
	got := err.Error()
	if !strings.Contains(got, "never ran") || !strings.Contains(got, "http (h:request)") || !strings.Contains(got, "ingest (h:ingest)") {
		t.Fatalf("diagnosis does not name the starved nodes: %s", got)
	}
}

func TestRetrievalDiagnosisStaysSilentOnAHealthyDrain(t *testing.T) {
	// Every node ran and none refused. The host has nothing to add, and MUST
	// add nothing: the ledger discards a reason when a batch landed, so
	// inventing one here would only mislead when it does not.
	sf := &ServiceFlow{lastNodeDigest: []nodeRunOutcome{
		{NodeID: "plan", PluginID: "p", MethodID: "ingest_plan", Invocations: 1},
		{NodeID: "ingest", PluginID: "h", MethodID: "ingest", Invocations: 1},
	}}
	if err := sf.retrievalDiagnosis(nil); err != nil {
		t.Fatalf("a healthy drain manufactured a failure reason: %v", err)
	}
	// No digest at all (an artifact whose state table could not be read) is
	// also silence, never a guess.
	if err := (&ServiceFlow{}).retrievalDiagnosis(nil); err != nil {
		t.Fatalf("an empty digest manufactured a failure reason: %v", err)
	}
}

func TestRetrievalDiagnosisPassesTheRunErrorThrough(t *testing.T) {
	// A drain that already failed carries its own answer. Replacing it with a
	// node summary would lose the specific one for a generic one.
	runErr := errors.New("flow service \"x\" trigger \"t\": allocate tick frame: out of memory")
	sf := &ServiceFlow{lastNodeDigest: []nodeRunOutcome{
		{NodeID: "parse", PluginID: "p", MethodID: "parse", Invocations: 1, LastStatus: 400},
	}}
	if got := sf.retrievalDiagnosis(runErr); got != runErr {
		t.Fatalf("the run's own error was replaced: %v", got)
	}
}
