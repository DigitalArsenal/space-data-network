package flatsqlrt

// storev2_prototype_test.go proves the load-bearing mechanics of the store-v2
// design (docs/flatsql-store-v2.md, loop B.1) inside one engine:
//
//  1. one FlatSQL database can hold MULTIPLE SDS record tables (multi-table
//     schema string, per-table file-id routing) PLUS plain control tables;
//  2. the vtab `_rowid` equals the sequence returned by IngestOneWithSource,
//     so control rows can link to vtab records via engine_seq;
//  3. the datasync cursor contract runs as SQL over the control table with
//     EXPLICIT rowids (rowid = durable journal seq): paging
//     `rowid > ? AND rowid <= ? ORDER BY rowid LIMIT n`, head `MAX(rowid)`,
//     eviction leaves seqs stable (pages skip gaps);
//  4. a fresh engine replaying the same order reproduces identical engine
//     sequences and control rowids (deterministic boot rebuild).

import (
	"fmt"
	"testing"
)

const multiStandardSchema = ommTestSchema + `
  table CATLite {
    OBJECT_NAME:string;
    NORAD_CAT_ID:uint32;
    OBJECT_TYPE:string;
  }
`

const controlDDL = `
CREATE TABLE sdn_record_index (
  schema_name TEXT NOT NULL,
  cid TEXT NOT NULL,
  table_ref TEXT NOT NULL,
  engine_seq INTEGER NOT NULL,
  stream_offset INTEGER NOT NULL,
  record_length INTEGER NOT NULL
)`

type protoRecord struct {
	seq     int64 // journal seq == control rowid
	norad   uint32
	name    string
	epoch   string
	source  string
	engine  int64 // engine-assigned vtab sequence
	payload []byte
}

// replayStore ingests the fixed record sequence into a fresh engine db,
// mimicking the boot replay path (explicit rowid = seq).
func replayStore(t *testing.T, rt *Runtime, name string) (*Database, []*protoRecord) {
	t.Helper()
	db, err := rt.CreateDatabase(multiStandardSchema, name)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(db.Destroy)
	if err := db.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatalf("RegisterFileID OMM: %v", err)
	}
	// A table only materializes in SQLite once it has a file id registered
	// (FlatSQLDatabase::initializeSQLiteEngine) — every SDS table the store
	// serves gets its identifier registered up front.
	if err := db.RegisterFileID("$CAT", "CATLite"); err != nil {
		t.Fatalf("RegisterFileID CATLite: %v", err)
	}
	for _, src := range []string{"celestrak-gp", "provider-two"} {
		if err := db.RegisterSource(src); err != nil {
			t.Fatalf("RegisterSource %s: %v", src, err)
		}
	}
	if _, err := db.Query(controlDDL); err != nil {
		t.Fatalf("control DDL: %v", err)
	}

	records := []*protoRecord{
		{seq: 1, norad: 1001, name: "SAT-A", epoch: "2026-05-10T00:00:00Z", source: "celestrak-gp"},
		{seq: 2, norad: 1002, name: "SAT-B", epoch: "2026-05-10T00:00:00Z", source: "celestrak-gp"},
		{seq: 3, norad: 2001, name: "OTHER", epoch: "2026-05-11T00:00:00Z", source: "provider-two"},
		{seq: 4, norad: 1001, name: "SAT-A", epoch: "2026-05-12T00:00:00Z", source: "celestrak-gp"},
		{seq: 5, norad: 1002, name: "SAT-B", epoch: "2026-05-12T00:00:00Z", source: "celestrak-gp"},
	}
	for _, r := range records {
		r.payload = buildOMM(r.norad, r.name, r.epoch)[4:] // strip size prefix
		engineSeq, err := db.IngestOneWithSource(r.payload, r.source)
		if err != nil {
			t.Fatalf("IngestOneWithSource seq %d: %v", r.seq, err)
		}
		r.engine = int64(engineSeq)
		// Explicit rowid = durable journal seq — THE cursor invariant.
		if _, err := db.Query(
			`INSERT INTO sdn_record_index (rowid, schema_name, cid, table_ref, engine_seq, stream_offset, record_length)
			 VALUES (?, 'OMM.fbs', ?, ?, ?, 0, ?)`,
			r.seq, fmt.Sprintf("cid-%d", r.seq), "OMM@"+r.source, r.engine, int64(len(r.payload))); err != nil {
			t.Fatalf("control insert seq %d: %v", r.seq, err)
		}
	}
	return db, records
}

func TestStoreV2Prototype(t *testing.T) {
	rt := newTestRuntime(t)
	db, records := replayStore(t, rt, "storev2")

	// (2) vtab _rowid == IngestOneWithSource sequence, payload retrievable
	// per control row.
	for _, r := range records {
		stream, err := db.QueryRawFlatBufferStream(
			fmt.Sprintf(`SELECT _data FROM "OMM@%s" WHERE _rowid = ?`, r.source), r.engine)
		if err != nil {
			t.Fatalf("payload by engine_seq %d: %v", r.engine, err)
		}
		frames, err := DecodeSizePrefixedStream(stream.Bytes)
		if err != nil || len(frames) != 1 {
			t.Fatalf("seq %d: %d frames err=%v", r.seq, len(frames), err)
		}
		if string(frames[0]) != string(r.payload) {
			t.Fatalf("seq %d: payload mismatch via _rowid=%d", r.seq, r.engine)
		}
	}

	// (3) cursor paging: rowid > after AND rowid <= max ORDER BY rowid.
	head, err := db.Query(`SELECT MAX(rowid) FROM sdn_record_index`)
	if err != nil || head.Rows[0][0].(int64) != 5 {
		t.Fatalf("head: %#v err=%v", head, err)
	}
	page, err := db.Query(
		`SELECT rowid, cid FROM sdn_record_index WHERE rowid > ? AND rowid <= ? ORDER BY rowid LIMIT 2`,
		int64(1), int64(5))
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page.Rows) != 2 || page.Rows[0][0].(int64) != 2 || page.Rows[1][0].(int64) != 3 {
		t.Fatalf("page rows: %#v", page.Rows)
	}

	// Eviction (hot-window GC) deletes control rows but never renumbers:
	// the next page simply skips the gap.
	if _, err := db.Query(`DELETE FROM sdn_record_index WHERE rowid = 4`); err != nil {
		t.Fatalf("evict: %v", err)
	}
	page, err = db.Query(
		`SELECT rowid FROM sdn_record_index WHERE rowid > ? AND rowid <= ? ORDER BY rowid LIMIT 10`,
		int64(3), int64(5))
	if err != nil || len(page.Rows) != 1 || page.Rows[0][0].(int64) != 5 {
		t.Fatalf("post-evict page: %#v err=%v", page, err)
	}

	// Cursor query joined with a vtab (single SQLite context) — refs +
	// provider partition in one statement.
	joined, err := db.Query(`
SELECT idx.rowid, o.OBJECT_NAME
FROM sdn_record_index idx
JOIN "OMM@celestrak-gp" o ON o._rowid = idx.engine_seq
WHERE idx.rowid > ? AND idx.table_ref = 'OMM@celestrak-gp'
ORDER BY idx.rowid`, int64(0))
	if err != nil {
		t.Fatalf("joined cursor query: %v", err)
	}
	if len(joined.Rows) != 3 { // seqs 1,2,5 (4 evicted; 3 is provider-two)
		t.Fatalf("joined rows: %#v", joined.Rows)
	}

	// (1) second SDS table coexists in the same database (schema-level
	// multi-table). CATLite has no ingested rows; the table must exist.
	if _, err := db.Query(`SELECT COUNT(*) FROM CATLite`); err != nil {
		t.Fatalf("second SDS table missing: %v", err)
	}
}

func TestStoreV2DeterministicRebuild(t *testing.T) {
	rt := newTestRuntime(t)
	_, first := replayStore(t, rt, "storev2-a")

	rt2 := newTestRuntime(t)
	db2, second := replayStore(t, rt2, "storev2-b")

	// (4) same replay order → identical engine sequences and rowids.
	for i := range first {
		if first[i].engine != second[i].engine {
			t.Fatalf("engine seq drift at %d: %d vs %d", i, first[i].engine, second[i].engine)
		}
	}
	res, err := db2.Query(`SELECT rowid FROM sdn_record_index ORDER BY rowid`)
	if err != nil || len(res.Rows) != 5 {
		t.Fatalf("rebuild control rows: %#v err=%v", res, err)
	}
	for i, row := range res.Rows {
		if row[0].(int64) != int64(i+1) {
			t.Fatalf("rebuild rowid %d = %v", i, row[0])
		}
	}
}
