//go:build linux

package flowrt

// flatsql_store_link_test.go — validates the store link's TWO-symbol ABI and the
// persistence round-trip WITHOUT the full provider-fetch drain (that is the live
// full-stack verify):
//   * exec_envelope (READ): envelope decode + the store node's dedup pre-check
//     (SELECT COUNT / SELECT 1 ... LIMIT 1 -> scalar existence).
//   * ingest_record (WRITE): wrapper FlatBuffer -> arena -> ExportData -> a FRESH
//     LinkedStore LoadAndRebuild -> rows survive (real cross-restart persistence).
// This is the proof the SQL-INSERT store node could NOT deliver: FlatSQL only
// snapshots the arena, and arena vtabs reject SQL INSERT — records must be
// INGESTED to persist.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
)

type envParam struct {
	tag  byte // 4=TEXT, 5=BLOB
	data []byte
}

func buildEnvelope(sql string, params ...envParam) []byte {
	var b []byte
	u32 := func(v uint32) { var t [4]byte; binary.LittleEndian.PutUint32(t[:], v); b = append(b, t[:]...) }
	u32(uint32(len(sql)))
	b = append(b, sql...)
	u32(uint32(len(params)))
	for _, p := range params {
		b = append(b, p.tag)
		u32(uint32(len(p.data)))
		b = append(b, p.data...)
	}
	return b
}

// buildStoreRow hand-builds the store node's wrapper FlatBuffer for
// table sds_* { cid:string(key)=0; provider:string=1; source_name:string=2;
// batch_id:string=3; data:[ubyte]=4 } with the store-local file identifier
// (SOMM/SOCM/SOBD) that routes ingest to the matching arena table.
func buildStoreRow(fileID, cid, provider, sourceName, batchID string, data []byte) []byte {
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

func TestLinkedStoreExecEnvelope(t *testing.T) {
	if os.Getenv("SDN_FLATSQL_STORE_TEST") == "" {
		t.Skip("set SDN_FLATSQL_STORE_TEST=1 (needs the flatsql engine wasm + WasmEdge)")
	}
	dir := t.TempDir()
	snap := filepath.Join(dir, "store.snapshot")
	s, err := OpenLinkedStore(filepath.Join(dir, "aot"), snap)
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}

	// The store node's in-wasm dedup pre-check (both forms must work via the
	// exec_envelope scalar-return contract).
	existsSel1 := func(table, cid string) bool {
		return s.execEnvelope(buildEnvelope(`SELECT 1 FROM `+table+` WHERE cid=? LIMIT 1`, envParam{4, []byte(cid)})) > 0
	}
	existsCount := func(table, cid string) bool {
		return s.execEnvelope(buildEnvelope(`SELECT COUNT(*) FROM `+table+` WHERE cid=?`, envParam{4, []byte(cid)})) > 0
	}

	// The store node's write: dedup pre-check, then ingest the wrapper FB.
	store := func(fileID, table, cid, provider string, data []byte) {
		if existsSel1(table, cid) {
			return // in-wasm content-addressed dedup: skip a re-store
		}
		if seq := s.ingestRecord(buildStoreRow(fileID, cid, provider, "spacex-starlink", "batch-1", data)); seq < 0 {
			t.Fatalf("ingestRecord %s cid=%s failed rc=%d", table, cid, seq)
		}
	}

	dOMM := []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', 1, 2, 3}
	store("SOMM", "sds_omm", "cidA", "SpaceX", dOMM)
	// Ingest must be immediately queryable (else live dedup is broken too).
	if !existsSel1("sds_omm", "cidA") || !existsCount("sds_omm", "cidA") {
		t.Fatalf("cidA not visible via dedup pre-check immediately after ingest")
	}
	store("SOMM", "sds_omm", "cidB", "SpaceX", []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', 4, 5, 6})
	store("SOMM", "sds_omm", "cidA", "SpaceX", []byte{9, 9, 9}) // dup cid -> pre-check skips
	store("SOCM", "sds_ocm", "cidC", "SpaceX", []byte{0, 0, 0, 0, '$', 'O', 'C', 'M', 7})
	store("SOBD", "sds_obd", "cidD", "SpaceX", []byte{0, 0, 0, 0, '$', 'O', 'B', 'D', 8})

	assertCount := func(db string, table string, want int64) {
		res, err := s.db.Query(`SELECT COUNT(*) FROM ` + table)
		if err != nil {
			t.Fatalf("[%s] count %s: %v", db, table, err)
		}
		if got := res.Rows[0][0].(int64); got != want {
			t.Fatalf("[%s] %s has %d rows, want %d", db, table, got, want)
		}
	}
	assertCount("live", "sds_omm", 2) // dup cidA deduped
	assertCount("live", "sds_ocm", 1)
	assertCount("live", "sds_obd", 1)
	t.Logf("live store: sds_omm=2 (dup deduped), sds_ocm=1, sds_obd=1 via ingest_record")

	// Snapshot MUST now be non-empty (the whole point of the fix).
	if err := s.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fi, err := os.Stat(snap)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("snapshot is empty (%v, size=%v) — arena persistence broken", err, fi)
	}
	t.Logf("opaque ExportData snapshot: %d bytes (non-zero — arena captured the ingested rows)", fi.Size())
	s.Close()

	// ── FRESH-RUNTIME reload: a brand-new LinkedStore (new WasmEdge VM + engine +
	// db + RegisterFileID) boots from the snapshot. This is the real restart. ──
	s2, err := OpenLinkedStore(filepath.Join(dir, "aot"), snap)
	if err != nil {
		t.Fatalf("OpenLinkedStore (reload): %v", err)
	}
	defer s2.Close()
	assertCount2 := func(table string, want int64) {
		res, err := s2.db.Query(`SELECT COUNT(*) FROM ` + table)
		if err != nil {
			t.Fatalf("[reload] count %s: %v", table, err)
		}
		if got := res.Rows[0][0].(int64); got != want {
			t.Fatalf("[reload] %s has %d rows after snapshot rebuild, want %d", table, got, want)
		}
	}
	assertCount2("sds_omm", 2)
	assertCount2("sds_ocm", 1)
	assertCount2("sds_obd", 1)

	// Row content survives (cid + provider + opaque data BLOB).
	res, err := s2.db.Query(`SELECT cid, provider, data FROM sds_omm ORDER BY cid`)
	if err != nil {
		t.Fatalf("[reload] read sds_omm: %v", err)
	}
	if len(res.Rows) != 2 || res.Rows[0][0] != "cidA" || res.Rows[1][0] != "cidB" {
		t.Fatalf("[reload] sds_omm rows: %#v", res.Rows)
	}
	if gotData, ok := res.Rows[0][2].([]byte); !ok || string(gotData) != string(dOMM) {
		t.Fatalf("[reload] cidA data BLOB mismatch: %#v", res.Rows[0][2])
	}

	// Dedup pre-check still behaves after reload (existing cid true, unknown false).
	if !(s2.execEnvelope(buildEnvelope(`SELECT 1 FROM sds_omm WHERE cid=? LIMIT 1`, envParam{4, []byte("cidA")})) > 0) {
		t.Fatalf("[reload] cidA should exist for dedup")
	}
	if s2.execEnvelope(buildEnvelope(`SELECT COUNT(*) FROM sds_omm WHERE cid=?`, envParam{4, []byte("cidZ")})) > 0 {
		t.Fatalf("[reload] cidZ must NOT exist")
	}

	t.Logf("★ INGEST + ExportData->fresh-LoadAndRebuild PERSISTENCE PROVEN: 2 $OMM + 1 $OCM + 1 $OBD survive a full restart; cid dedup pre-check works pre- and post-reload; host stayed a dumb byte-mover (opaque ingest + opaque snapshot).")
}
