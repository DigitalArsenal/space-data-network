package flowrt

import (
	"errors"
	"strings"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/typedfb/flownotice"
	"github.com/spacedatanetwork/sdn-server/internal/typedfb/timertick"
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

// A conditional fetch that answers 304 starves the tail of the pipeline BY
// DESIGN: the parser says so with an "unchanged" egress frame and lands
// nothing. That is not a failure, and the ledger must not widen the
// publisher's window or raise a lane alert over it.
func TestRetrievalDiagnosisCallsAnUnchangedRunUnchanged(t *testing.T) {
	sf := &ServiceFlow{lastRunUnchanged: true, lastNodeDigest: []nodeRunOutcome{
		{NodeID: "http", PluginID: "h", MethodID: "request", Invocations: 1},
		{NodeID: "parse", PluginID: "p", MethodID: "parse", Invocations: 1},
		{NodeID: "ingest", PluginID: "h", MethodID: "ingest"},
		{NodeID: "publish", PluginID: "h", MethodID: "request"},
	}}

	if err := sf.retrievalDiagnosis(nil); !errors.Is(err, ErrRetrievalUnchanged) {
		t.Fatalf("an unchanged run was diagnosed as %v", err)
	}

	// A refusal still outranks the notice: the node that said no is the cause.
	sf.lastNodeDigest[1].LastStatus = 400
	err := sf.retrievalDiagnosis(nil)
	if err == nil || errors.Is(err, ErrRetrievalUnchanged) || !strings.Contains(err.Error(), "parse (p:parse) status 400") {
		t.Fatalf("a refusal beside an unchanged notice was diagnosed as %v", err)
	}
}

func TestEgressReportsUnchangedReadsTheNotice(t *testing.T) {
	for _, tc := range []struct {
		frames []string
		want   bool
	}{
		{[]string{`{"status":304,"unchanged":true,"source_name":"celestrak-satcat-txt"}`}, true},
		{[]string{`{"status":200}`, `{"status":304}`}, true},
		{[]string{`{"status":200,"inserted":12}`}, false},
		{[]string{`"plain text frame"`, `{"unchanged":false}`}, false},
		{nil, false},
	} {
		frames := make([][]byte, 0, len(tc.frames))
		for _, frame := range tc.frames {
			frames = append(frames, []byte(frame))
		}
		if got := egressReportsUnchanged(frames); got != tc.want {
			t.Errorf("egressReportsUnchanged(%v) = %v, want %v", tc.frames, got, tc.want)
		}
	}

	// A typed module's notice: a FlowNotice (FNTC) of KIND Unchanged is the
	// quiet tick; any other kind is not.
	notice := func(kind byte) []byte {
		builder := flatbuffers.NewBuilder(64)
		reason := builder.CreateString("not-modified")
		flownotice.FlowNoticeStart(builder)
		flownotice.FlowNoticeAddReason(builder, reason)
		builder.PrependByteSlot(0, kind, 255)
		flownotice.FinishFlowNoticeBuffer(builder, flownotice.FlowNoticeEnd(builder))
		return builder.FinishedBytes()
	}
	if !egressReportsUnchanged([][]byte{notice(0)}) {
		t.Error("a FlowNotice of KIND Unchanged was not read as unchanged")
	}
	if egressReportsUnchanged([][]byte{notice(1)}) {
		t.Error("a FlowNotice of KIND Skipped was read as unchanged")
	}
	if got := egressSummary([][]byte{notice(0)}); len(got) != 1 || !strings.Contains(string(got[0]), `"fileIdentifier":"FNTC"`) {
		t.Errorf("egressSummary of a FlowNotice = %s", got)
	}
}

func TestTimerTickFrameIsATimerTick(t *testing.T) {
	at := time.UnixMilli(1760472000123)
	frame := timerTickFrame("timer-hourly", at, 7)
	if !timertick.TimerTickBufferHasIdentifier(frame) {
		t.Fatalf("tick frame has no TICK identifier: % x", frame[:8])
	}
	tick := timertick.GetRootAsTimerTick(frame, 0)
	if string(tick.TriggerId()) != "timer-hourly" || tick.FiredMs() != 1760472000123 || tick.ScheduledMs() != 1760472000123 || tick.Sequence() != 7 {
		t.Fatalf("tick = %q fired %d scheduled %d seq %d", tick.TriggerId(), tick.FiredMs(), tick.ScheduledMs(), tick.Sequence())
	}
}
