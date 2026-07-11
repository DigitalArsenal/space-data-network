package caps

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// ---------------------------------------------------------------------------
// ActivityRing
// ---------------------------------------------------------------------------

// fixedActivityClock returns a deterministic, monotonically-increasing
// clock (base, base+1s, base+2s, ...) so ring tests never depend on
// wall-clock time.
func fixedActivityClock(base time.Time) func() time.Time {
	n := 0
	return func() time.Time {
		t := base.Add(time.Duration(n) * time.Second)
		n++
		return t
	}
}

func TestActivityRingOrderWithinCapacity(t *testing.T) {
	r := NewActivityRingWithClock(5, fixedActivityClock(time.Unix(0, 0).UTC()))
	r.Append("peer_connected", "peerA", "")
	r.Append("peer_connected", "peerB", "")
	r.Append("record_stored", "peerC", "OMM.fbs")

	got := r.Snapshot(0)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].PeerID != "peerC" || got[1].PeerID != "peerB" || got[2].PeerID != "peerA" {
		t.Fatalf("Snapshot order = %+v, want newest-first (C, B, A)", got)
	}
}

func TestActivityRingOverflowDropsOldest(t *testing.T) {
	r := NewActivityRingWithClock(3, fixedActivityClock(time.Unix(0, 0).UTC()))
	for i := 0; i < 5; i++ {
		r.Append("kind", "", "")
	}
	got := r.Snapshot(0)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (capacity)", len(got))
	}
}

func TestActivityRingOverflowKeepsNewest(t *testing.T) {
	r := NewActivityRingWithClock(3, fixedActivityClock(time.Unix(0, 0).UTC()))
	for i := 0; i < 5; i++ {
		r.Append("kind", peerLabel(i), "")
	}
	got := r.Snapshot(0)
	want := []string{"peer4", "peer3", "peer2"} // newest-first; peer0/peer1 evicted
	for i, w := range want {
		if got[i].PeerID != w {
			t.Fatalf("Snapshot()[%d].PeerID = %q, want %q (post-overflow, newest-first)", i, got[i].PeerID, w)
		}
	}
}

func peerLabel(n int) string {
	return "peer" + string(rune('0'+n))
}

func TestActivityRingLimitClamp(t *testing.T) {
	r := NewActivityRingWithClock(10, fixedActivityClock(time.Unix(0, 0).UTC()))
	for i := 0; i < 10; i++ {
		r.Append("kind", "", "")
	}
	if got := r.Snapshot(3); len(got) != 3 {
		t.Fatalf("Snapshot(3) len = %d, want 3", len(got))
	}
	if got := r.Snapshot(0); len(got) != 10 {
		t.Fatalf("Snapshot(0) len = %d, want 10 (all)", len(got))
	}
	if got := r.Snapshot(1000); len(got) != 10 {
		t.Fatalf("Snapshot(1000) len = %d, want 10 (clamped to available)", len(got))
	}
}

func TestActivityRingCapacityZeroTreatedAsOne(t *testing.T) {
	r := NewActivityRingWithClock(0, fixedActivityClock(time.Unix(0, 0).UTC()))
	r.Append("a", "", "")
	r.Append("b", "", "")
	got := r.Snapshot(0)
	if len(got) != 1 || got[0].Kind != "b" {
		t.Fatalf("got = %+v, want exactly the newest event", got)
	}
}

func TestActivityRingNilSafe(t *testing.T) {
	var r *ActivityRing
	r.Append("kind", "peer", "detail") // must not panic
	if got := r.Snapshot(10); got != nil {
		t.Fatalf("nil ring Snapshot() = %v, want nil", got)
	}
}

func TestActivityRingTimestampsUseInjectedClock(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	r := NewActivityRingWithClock(5, fixedActivityClock(base))
	r.Append("peer_connected", "peerA", "")
	got := r.Snapshot(1)
	if !got[0].At.Equal(base) {
		t.Fatalf("At = %v, want %v", got[0].At, base)
	}
}

func TestActivityRingConcurrentAppend(t *testing.T) {
	r := NewActivityRing(ActivityRingCapacity)
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Append("kind", "peer", "detail")
		}()
	}
	wg.Wait()
	got := r.Snapshot(0)
	if len(got) != ActivityRingCapacity {
		t.Fatalf("len = %d, want %d (capacity) after concurrent overflow", len(got), ActivityRingCapacity)
	}
}

// ---------------------------------------------------------------------------
// node_activity_read.activity
// ---------------------------------------------------------------------------

// fakeActivityRing implements ActivityRingReader over a fixed, already
// newest-first slice — a test double standing in for a live *ActivityRing
// (nodeActivityCapAdapter.activity only ever calls Snapshot).
type fakeActivityRing struct {
	events []ActivityEvent
}

func (f *fakeActivityRing) Snapshot(limit int) []ActivityEvent {
	if limit <= 0 || limit > len(f.events) {
		limit = len(f.events)
	}
	out := make([]ActivityEvent, limit)
	copy(out, f.events[:limit])
	return out
}

func invokeNodeActivity(t *testing.T, materials NodeActivityMaterials, op string, payload []byte) []byte {
	t.Helper()
	factory := NewNodeActivityCapFactory(materials)
	handler := factory(&modulert.Module{})
	response, err := handler(op, payload)
	if err != nil {
		t.Fatalf("%s: unexpected transport error: %v", op, err)
	}
	return response
}

func TestNodeActivityEmptyRingUnwired(t *testing.T) {
	response := invokeNodeActivity(t, NodeActivityMaterials{}, "node_activity_read.activity", nil)
	meta, segments := decodePreEncodedEnvelope(t, response)
	if len(segments) != 0 {
		t.Fatalf("node_activity_read.activity must carry no binary segments, got %d", len(segments))
	}
	result := resultOf(t, meta)
	if result["count"] != float64(0) {
		t.Fatalf("count = %v, want 0", result["count"])
	}
	events, ok := result["events"].([]interface{})
	if !ok || len(events) != 0 {
		t.Fatalf("events = %v, want an empty array (not null)", result["events"])
	}
}

func TestNodeActivityEmptyRealRing(t *testing.T) {
	ring := NewActivityRing(4)
	response := invokeNodeActivity(t, NodeActivityMaterials{Ring: ring}, "node_activity_read.activity", nil)
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["count"] != float64(0) {
		t.Fatalf("count = %v, want 0", result["count"])
	}
	events, ok := result["events"].([]interface{})
	if !ok || len(events) != 0 {
		t.Fatalf("events = %v, want an empty array (not null)", result["events"])
	}
}

func TestNodeActivityExactJSON(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	fake := &fakeActivityRing{events: []ActivityEvent{
		{At: base.Add(2 * time.Second), Kind: "peer_connected", PeerID: "peerB"},
		{At: base.Add(1 * time.Second), Kind: "record_stored", PeerID: "peerA", Detail: "OMM.fbs"},
		{At: base, Kind: "grant_issued", Detail: "chan-1"}, // no PeerID -> peer_id omitted
	}}
	response := invokeNodeActivity(t, NodeActivityMaterials{Ring: fake}, "node_activity_read.activity", nil)
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)

	got, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	var wantMap map[string]interface{}
	wantLiteral := `{"count":3,"events":[` +
		`{"ts":"2026-07-11T12:00:02Z","kind":"peer_connected","peer_id":"peerB","detail":""},` +
		`{"ts":"2026-07-11T12:00:01Z","kind":"record_stored","peer_id":"peerA","detail":"OMM.fbs"},` +
		`{"ts":"2026-07-11T12:00:00Z","kind":"grant_issued","detail":"chan-1"}` +
		`]}`
	if err := json.Unmarshal([]byte(wantLiteral), &wantMap); err != nil {
		t.Fatalf("unmarshal expected literal: %v", err)
	}
	want, err := json.Marshal(wantMap)
	if err != nil {
		t.Fatalf("marshal expected: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("result JSON mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestNodeActivityPeerIDOmittedWhenEmpty(t *testing.T) {
	fake := &fakeActivityRing{events: []ActivityEvent{{Kind: "grant_issued", Detail: "chan-1"}}}
	response := invokeNodeActivity(t, NodeActivityMaterials{Ring: fake}, "node_activity_read.activity", nil)
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	events := result["events"].([]interface{})
	ev := events[0].(map[string]interface{})
	if _, present := ev["peer_id"]; present {
		t.Fatalf("peer_id should be omitted when empty, got %v", ev)
	}
}

func manyFakeEvents(n int) *fakeActivityRing {
	events := make([]ActivityEvent, n)
	for i := range events {
		events[i] = ActivityEvent{Kind: "kind"}
	}
	return &fakeActivityRing{events: events}
}

func TestNodeActivityDefaultLimitIs50(t *testing.T) {
	response := invokeNodeActivity(t, NodeActivityMaterials{Ring: manyFakeEvents(300)}, "node_activity_read.activity", nil)
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["count"] != float64(50) {
		t.Fatalf("count = %v, want 50 (default limit)", result["count"])
	}
}

func TestNodeActivityLimitClampedToRingCapacity(t *testing.T) {
	response := invokeNodeActivity(t, NodeActivityMaterials{Ring: manyFakeEvents(300)}, "node_activity_read.activity", []byte(`{"limit":1000}`))
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["count"] != float64(ActivityRingCapacity) {
		t.Fatalf("count = %v, want %d (clamped)", result["count"], ActivityRingCapacity)
	}
}

func TestNodeActivityExplicitLimitHonored(t *testing.T) {
	response := invokeNodeActivity(t, NodeActivityMaterials{Ring: manyFakeEvents(300)}, "node_activity_read.activity", []byte(`{"limit":5}`))
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["count"] != float64(5) {
		t.Fatalf("count = %v, want 5", result["count"])
	}
}

func TestNodeActivityNegativeOrZeroLimitFallsBackToDefault(t *testing.T) {
	for _, payload := range [][]byte{[]byte(`{"limit":0}`), []byte(`{"limit":-5}`)} {
		response := invokeNodeActivity(t, NodeActivityMaterials{Ring: manyFakeEvents(300)}, "node_activity_read.activity", payload)
		meta, _ := decodePreEncodedEnvelope(t, response)
		result := resultOf(t, meta)
		if result["count"] != float64(50) {
			t.Fatalf("payload %s: count = %v, want 50 (default)", payload, result["count"])
		}
	}
}

func TestNodeActivityMalformedPayloadFallsBackToDefault(t *testing.T) {
	response := invokeNodeActivity(t, NodeActivityMaterials{Ring: manyFakeEvents(300)}, "node_activity_read.activity", []byte(`not-json`))
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["count"] != float64(50) {
		t.Fatalf("count = %v, want 50 (default, malformed payload ignored)", result["count"])
	}
}

func TestNodeActivityUnknownOperation(t *testing.T) {
	factory := NewNodeActivityCapFactory(NodeActivityMaterials{})
	handler := factory(&modulert.Module{})
	response, err := handler("node_activity_read.reboot", nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(response, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("unknown op must fail: %v", meta)
	}
}
