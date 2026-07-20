package flowrt

// flatsql_query_test.go — local, cross-platform (no linux/env-var gate) proof
// of LinkedStore.Query: the data-surface board's read-only SQL surface over
// the sds_omm/sds_ocm/sds_obd arena tables the OD flow's store node writes.
// The engine itself (flatsqlrt over WasmEdge) runs fine on darwin — proven by
// sdn/flatsqlrt's own ungated test suite — so this does not need the
// wasi-threads bake toolchain or SDN_FLATSQL_STORE_TEST at all; it only needs
// OpenLinkedStore, which flatsql_store_link.go already builds everywhere.

import (
	"path/filepath"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
)

// queryTestStoreRow hand-builds the store node's wrapper FlatBuffer, matching
// buildStoreRow in flatsql_store_link_test.go (kept local so this file has no
// dependency on that linux-gated test file's helpers).
func queryTestStoreRow(fileID, cid, provider, sourceName, batchID string, data []byte) []byte {
	b := flatbuffers.NewBuilder(256)
	cidOff := b.CreateString(cid)
	provOff := b.CreateString(provider)
	srcOff := b.CreateString(sourceName)
	batchOff := b.CreateString(batchID)
	dataOff := b.CreateByteVector(data)
	b.StartObject(5)
	b.PrependUOffsetTSlot(0, cidOff, 0)
	b.PrependUOffsetTSlot(1, provOff, 0)
	b.PrependUOffsetTSlot(2, srcOff, 0)
	b.PrependUOffsetTSlot(3, batchOff, 0)
	b.PrependUOffsetTSlot(4, dataOff, 0)
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte(fileID))
	return b.FinishedBytes()
}

func TestLinkedStoreQueryReadOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenLinkedStore(filepath.Join(dir, "aot"), filepath.Join(dir, "store.snapshot"))
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}
	defer s.Close()

	dOMM := []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', 9, 9}
	if seq := s.ingestRecord(queryTestStoreRow("SOMM", "cid-1", "spacex-starlink", "SpaceX-E", "batch-1", dOMM)); seq < 0 {
		t.Fatalf("ingestRecord failed rc=%d", seq)
	}

	res, err := s.Query("SELECT cid, provider, data FROM sds_omm WHERE cid = ?", "cid-1")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("Query rows = %d, want 1 (%#v)", len(res.Rows), res.Rows)
	}
	if res.Rows[0][0] != "cid-1" || res.Rows[0][1] != "spacex-starlink" {
		t.Fatalf("Query row mismatch: %#v", res.Rows[0])
	}
	blob, ok := res.Rows[0][2].([]byte)
	if !ok || string(blob) != string(dOMM) {
		t.Fatalf("Query data BLOB mismatch: %#v", res.Rows[0][2])
	}
}

// TestLinkedStoreQueryRejectsNonSelect proves the read-only guard: anything
// but a single SELECT is refused before ever reaching the engine — this
// surface exists ONLY for the data-surface board's search/list/download API,
// never as a second write path.
func TestLinkedStoreQueryRejectsNonSelect(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenLinkedStore(filepath.Join(dir, "aot"), filepath.Join(dir, "store.snapshot"))
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}
	defer s.Close()

	cases := []string{
		"DELETE FROM sds_omm",
		"INSERT INTO sds_omm (cid) VALUES ('x')",
		"SELECT * FROM sds_omm; DROP TABLE sds_omm",
		"  ",
	}
	for _, sql := range cases {
		if _, err := s.Query(sql); err == nil {
			t.Fatalf("Query(%q) should have been rejected as non-read-only", sql)
		}
	}
	// A single, whitespace-padded SELECT (with a trailing semicolon) is fine.
	if _, err := s.Query("  select count(*) from sds_omm ;  "); err != nil {
		t.Fatalf("Query(padded SELECT) should be accepted: %v", err)
	}
}

// TestServiceFlowStoreNilSafety proves ServiceFlow.Store() never panics on a
// bare/closed flow or a bridge-mode flow with no engine link (rt.store nil) —
// the data-surface board's read path must fail closed (nil), never trap.
func TestServiceFlowStoreNilSafety(t *testing.T) {
	if sf := (&ServiceFlow{}); sf.Store() != nil {
		t.Fatalf("Store() on a bare ServiceFlow should be nil")
	}
	rt := &FlowRuntime{} // no linked store (bridge-mode)
	sf := &ServiceFlow{inst: &flowInstance{rt: rt}}
	if sf.Store() != nil {
		t.Fatalf("Store() on a bridge-mode (non-engine-linked) flow should be nil")
	}
}

// TestServiceFlowStoreAvailableDuringFire reproduces the production run-board
// stall: FireTrigger holds sf.mu for the full (potentially multi-hour) fire,
// but read-only result APIs still need the already-open linked store so they
// can render OngoingFire progress. Store must therefore not wait on the fire
// serialization mutex.
func TestServiceFlowStoreAvailableDuringFire(t *testing.T) {
	want := &LinkedStore{}
	sf := &ServiceFlow{inst: &flowInstance{rt: &FlowRuntime{store: want}}}

	// Simulate FireTrigger owning the serialization lock for an in-flight fire.
	sf.mu.Lock()
	defer sf.mu.Unlock()

	got := make(chan *LinkedStore, 1)
	go func() { got <- sf.Store() }()

	select {
	case store := <-got:
		if store != want {
			t.Fatalf("Store() during a fire = %p, want %p", store, want)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Store() blocked behind the in-flight fire mutex")
	}
}

func TestIsReadOnlySelectHelper(t *testing.T) {
	ok := []string{"SELECT 1", "select * from sds_omm", "  SELECT cid FROM sds_omm WHERE cid=? ", "SELECT 1;"}
	bad := []string{"", "  ", "INSERT INTO x VALUES (1)", "SELECT 1; DELETE FROM x", "DROP TABLE sds_omm"}
	for _, s := range ok {
		if !isReadOnlySelect(s) {
			t.Errorf("isReadOnlySelect(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if isReadOnlySelect(s) {
			t.Errorf("isReadOnlySelect(%q) = true, want false", s)
		}
	}
}
