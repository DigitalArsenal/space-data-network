package storage

// record_store_test.go — the record store IS the control database.
//
// A boot opens it and serves; a corrupt file is refused, never discarded; a
// crash mid-ingest reopens to exactly the committed rows; a $CAT record
// supersedes the producer's previous record for the same object; a delete or
// supersede reaches the engine hot window at once and stays gone across a
// restart.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// buildCATForTest builds a $CAT record. catalogURI/catalogObjectID land in
// the CATALOG_URI / CATALOG_OBJECT_ID slots the vendored builder does not
// expose yet (record_supersede.go reads them by slot for the same reason).
func buildCATForTest(name, objectID string, norad uint32, catalogURI, catalogObjectID string) []byte {
	b := flatbuffers.NewBuilder(256)
	nameOff := b.CreateString(name)
	var objectIDOff, uriOff, catIDOff flatbuffers.UOffsetT
	if objectID != "" {
		objectIDOff = b.CreateString(objectID)
	}
	if catalogURI != "" {
		uriOff = b.CreateString(catalogURI)
	}
	if catalogObjectID != "" {
		catIDOff = b.CreateString(catalogObjectID)
	}
	CAT.CATStart(b)
	CAT.CATAddOBJECT_NAME(b, nameOff)
	if objectIDOff != 0 {
		CAT.CATAddOBJECT_ID(b, objectIDOff)
	}
	if norad != 0 {
		CAT.CATAddNORAD_CAT_ID(b, norad)
	}
	if uriOff != 0 {
		b.PrependUOffsetTSlot(24, uriOff, 0)
	}
	if catIDOff != 0 {
		b.PrependUOffsetTSlot(25, catIDOff, 0)
	}
	root := CAT.CATEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$CAT"))
	return b.FinishedBytes()
}

func engineCount(t *testing.T, s *FlatSQLStore, table string) int64 {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	res, err := s.engineDB.Query(`SELECT COUNT(*) FROM "` + table + `"`)
	if err != nil {
		t.Fatalf("engine count %s: %v", table, err)
	}
	n, _ := res.Rows[0][0].(int64)
	return n
}

func indexCount(t *testing.T, s *FlatSQLStore, schemaName string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = ?`, schemaName).Scan(&n); err != nil {
		t.Fatalf("index count: %v", err)
	}
	return n
}

func TestSupersedeKeyFollowsCATIdentityRule(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"catalog pair wins over norad", buildCATForTest("A", "2026-001A", 90001, "https://catalog.example/v1", "obj-7"), "uri:https://catalog.example/v1\x00obj-7"},
		{"norad when the pair is incomplete", buildCATForTest("A", "2026-001A", 90001, "https://catalog.example/v1", ""), "norad:90001"},
		{"object id when un-numbered", buildCATForTest("A", "2026-001A", 0, "", ""), "object:2026-001A"},
		{"no identity, no supersede", buildCATForTest("A", "", 0, "", ""), ""},
	}
	for _, tc := range cases {
		if got := recordSupersedeKey("CAT.fbs", tc.data); got != tc.want {
			t.Fatalf("%s: key = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := recordSupersedeKey("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1700000000)); got != "" {
		t.Fatalf("$OMM is historical and must never supersede, got key %q", got)
	}
}

// Two successive ingests of the same catalog edition leave ONE row per
// object: in the producer table, in the index, in the source summary and in
// the engine. Records with no identity are kept as written.
func TestCATSupersedeKeepsOneRowPerObject(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := openBootStore(t, basePath, bootTestValidator(t))
	defer store.Close()

	tags := SourceTags{ProviderID: "prov", SourceName: "satcat", BatchID: "edition"}
	edition := func(suffix string) [][]byte {
		return [][]byte{
			buildCATForTest("ISS "+suffix, "1998-067A", 25544, "", ""),
			buildCATForTest("ANALYST "+suffix, "2026-900A", 0, "", ""),
			buildCATForTest("CATALOGUED "+suffix, "2026-001A", 0, "https://catalog.example/v1", "obj-7"),
			buildCATForTest("NAMELESS "+suffix, "", 0, "", ""),
		}
	}
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", edition("v1"), "peer", nil, tags); err != nil {
		t.Fatalf("first edition: %v", err)
	}
	firstCIDs := make([]string, 0, 4)
	for _, data := range edition("v1") {
		firstCIDs = append(firstCIDs, computeCID(data))
	}
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", edition("v2"), "peer", nil, tags); err != nil {
		t.Fatalf("second edition: %v", err)
	}

	// 3 identified objects + 2 nameless records (no identity, no supersede).
	const wantRows = 5
	if n, err := store.Count("CAT.fbs"); err != nil || n != wantRows {
		t.Fatalf("CAT rows after two editions = %d (err %v), want %d", n, err, wantRows)
	}
	if n := indexCount(t, store, "CAT.fbs"); n != wantRows {
		t.Fatalf("index rows = %d, want %d", n, wantRows)
	}
	if n := engineCount(t, store, "CAT"); n != wantRows {
		t.Fatalf("engine CAT rows = %d, want %d", n, wantRows)
	}
	summary, err := store.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	if summary.TotalRecords != wantRows {
		t.Fatalf("summary total = %d, want %d", summary.TotalRecords, wantRows)
	}
	// The superseded copies are gone; the nameless v1 record survives.
	for i, cid := range firstCIDs[:3] {
		if _, err := store.GetRecord("CAT.fbs", cid); err == nil {
			t.Fatalf("superseded record %d (%s) still readable", i, cid)
		}
	}
	if _, err := store.GetRecord("CAT.fbs", firstCIDs[3]); err != nil {
		t.Fatalf("nameless v1 record was superseded without an identity: %v", err)
	}
	// The current edition reads back.
	for _, data := range edition("v2") {
		if _, err := store.GetRecord("CAT.fbs", computeCID(data)); err != nil {
			t.Fatalf("current record missing: %v", err)
		}
	}
	// Ingesting the SAME edition again is a no-op on every count.
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", edition("v2"), "peer", nil, tags); err != nil {
		t.Fatalf("repeat edition: %v", err)
	}
	if n, _ := store.Count("CAT.fbs"); n != wantRows {
		t.Fatalf("CAT rows after a repeat ingest = %d, want %d", n, wantRows)
	}
	if n := engineCount(t, store, "CAT"); n != wantRows {
		t.Fatalf("engine CAT rows after a repeat ingest = %d, want %d", n, wantRows)
	}
}

// sourceRecordCounts is the live per-(schema, provider, source) record count,
// which is what a node's board and /api/v1/stats report for a source.
func sourceRecordCounts(t *testing.T, s *FlatSQLStore) map[string]int64 {
	t.Helper()
	progress, err := s.SourceBatchProgress()
	if err != nil {
		t.Fatalf("SourceBatchProgress: %v", err)
	}
	counts := map[string]int64{}
	for _, p := range progress {
		counts[p.SchemaName+"|"+p.ProviderID+"|"+p.SourceName] += p.Count
	}
	return counts
}

// indexRowID is a record's position in the datasync cursor order.
func indexRowID(t *testing.T, s *FlatSQLStore, schemaName, cid string) int64 {
	t.Helper()
	var rowID int64
	if err := s.db.QueryRow(`SELECT rowid FROM sdn_record_index WHERE schema_name = ? AND cid = ?`, schemaName, cid).Scan(&rowID); err != nil {
		t.Fatalf("index rowid for %s/%s: %v", schemaName, cid, err)
	}
	return rowID
}

// The supersede lane is (producer, SOURCE), not the producer alone.
//
// One provider publishes the same catalog in two encodings as two distinct
// SOURCES — CelesTrak's satcat.txt and satcat.csv — under one producer peer,
// and neither form carries a catalog URI, so both reduce to the same object
// identity. With the lane scoped to the producer alone, whichever source ran
// second deleted the other's entire edition: the losing source reported a full
// insert every single tick while holding zero records, and every unchanged
// record kept moving in the datasync cursor, so every subscriber re-downloaded
// the whole catalog each cycle.
func TestCATSupersedeIsScopedToTheSource(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := openBootStore(t, basePath, bootTestValidator(t))
	defer store.Close()

	fixedWidth := SourceTags{ProviderID: "prov", SourceName: "satcat", BatchID: "edition-1"}
	csv := SourceTags{ProviderID: "prov", SourceName: "satcat-csv", BatchID: "edition-1"}
	// The two encodings disagree about the same object (the real SATCAT forms
	// differ on MASS, OWNER and LAUNCH_SITE), which is exactly why collapsing
	// them by fetch order was silently picking a winner.
	txt := buildCATForTest("ISS (ZARYA) fixed-width", "1998-067A", 25544, "", "")
	csvV1 := buildCATForTest("ISS (ZARYA) csv", "1998-067A", 25544, "", "")

	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", [][]byte{txt}, "peer", nil, fixedWidth); err != nil {
		t.Fatalf("fixed-width ingest: %v", err)
	}
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", [][]byte{csvV1}, "peer", nil, csv); err != nil {
		t.Fatalf("csv ingest: %v", err)
	}

	// One row per object PER SOURCE, and both sources keep their attribution.
	if n, err := store.Count("CAT.fbs"); err != nil || n != 2 {
		t.Fatalf("CAT rows = %d (err %v), want 2 — one per source", n, err)
	}
	if n := engineCount(t, store, "CAT"); n != 2 {
		t.Fatalf("engine CAT rows = %d, want 2", n)
	}
	for label, cid := range map[string]string{"fixed-width": computeCID(txt), "csv": computeCID(csvV1)} {
		if _, err := store.GetRecord("CAT.fbs", cid); err != nil {
			t.Fatalf("the %s source's record is gone: %v", label, err)
		}
	}
	if counts := sourceRecordCounts(t, store); counts["CAT.fbs|prov|satcat"] != 1 || counts["CAT.fbs|prov|satcat-csv"] != 1 {
		t.Fatalf("per-source record counts = %v, want 1 for each source", counts)
	}
	txtRowID := indexRowID(t, store, "CAT.fbs", computeCID(txt))

	// A new edition from ONE source retires that source's previous row and
	// nothing else.
	csvV2 := buildCATForTest("ISS (ZARYA) csv, next edition", "1998-067A", 25544, "", "")
	csvNext := csv
	csvNext.BatchID = "edition-2"
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", [][]byte{csvV2}, "peer", nil, csvNext); err != nil {
		t.Fatalf("csv second edition: %v", err)
	}
	if n, err := store.Count("CAT.fbs"); err != nil || n != 2 {
		t.Fatalf("CAT rows after one source moved on = %d (err %v), want 2", n, err)
	}
	if _, err := store.GetRecord("CAT.fbs", computeCID(txt)); err != nil {
		t.Fatalf("the fixed-width record was retired by another source: %v", err)
	}
	if _, err := store.GetRecord("CAT.fbs", computeCID(csvV1)); err == nil {
		t.Fatal("the csv source's own previous record survived its new edition")
	}
	if _, err := store.GetRecord("CAT.fbs", computeCID(csvV2)); err != nil {
		t.Fatalf("the csv source's current record is missing: %v", err)
	}
	if counts := sourceRecordCounts(t, store); counts["CAT.fbs|prov|satcat"] != 1 || counts["CAT.fbs|prov|satcat-csv"] != 1 {
		t.Fatalf("per-source record counts = %v, want 1 for each source", counts)
	}

	// The store CONVERGES: re-ingesting what each source already published
	// inserts nothing, deletes nothing, and moves no cursor position
	// (flatsql-store-v2.md §3 — a record's cursor position never moves for the
	// life of the store).
	for tick := 0; tick < 2; tick++ {
		if _, err := store.StoreBatchWithSourceTags("CAT.fbs", [][]byte{txt}, "peer", nil, fixedWidth); err != nil {
			t.Fatalf("repeat fixed-width tick %d: %v", tick, err)
		}
		if _, err := store.StoreBatchWithSourceTags("CAT.fbs", [][]byte{csvV2}, "peer", nil, csvNext); err != nil {
			t.Fatalf("repeat csv tick %d: %v", tick, err)
		}
		if n, err := store.Count("CAT.fbs"); err != nil || n != 2 {
			t.Fatalf("CAT rows after repeat tick %d = %d (err %v), want 2", tick, n, err)
		}
		if n := indexCount(t, store, "CAT.fbs"); n != 2 {
			t.Fatalf("index rows after repeat tick %d = %d, want 2", tick, n)
		}
		if n := engineCount(t, store, "CAT"); n != 2 {
			t.Fatalf("engine CAT rows after repeat tick %d = %d, want 2", tick, n)
		}
		if got := indexRowID(t, store, "CAT.fbs", computeCID(txt)); got != txtRowID {
			t.Fatalf("an unchanged record moved in the cursor order on tick %d: rowid %d -> %d", tick, txtRowID, got)
		}
	}
}

// The supersede SELECT runs ONCE PER $CAT RECORD on the ingest hot path, over
// a producer table that holds the whole catalog, inside the store write lock.
// Matching the lane key AND the pre-scoping bare key must still SEEK
// <table>_supersede — an IN-list that degraded to a scan would put a full
// table scan per record in front of every reader.
func TestSupersedeLaneSelectSeeksTheSupersedeIndex(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := openBootStore(t, basePath, bootTestValidator(t))
	defer store.Close()

	tags := SourceTags{ProviderID: "prov", SourceName: "satcat", BatchID: "edition-1"}
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", [][]byte{
		buildCATForTest("ISS", "1998-067A", 25544, "", ""),
	}, "peer", nil, tags); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	table, err := ProducerStandardTableName(routedProducerID("peer"), "CAT.fbs")
	if err != nil {
		t.Fatalf("producer table name: %v", err)
	}
	keys := recordSupersedeKeys("CAT.fbs", buildCATForTest("ISS", "1998-067A", 25544, "", ""), "satcat")
	if len(keys.match) != 2 {
		t.Fatalf("a source-scoped write must match its own lane key and the bare identity, got %d keys", len(keys.match))
	}

	rows, err := store.db.Query(fmt.Sprintf(
		"EXPLAIN QUERY PLAN SELECT cid FROM %s WHERE supersede_key IN (%s) AND cid <> ?",
		table, placeholderList(len(keys.match)),
	), keys.match[0], keys.match[1], "cid")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	plan := ""
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		for _, v := range vals {
			switch typed := v.(type) {
			case string:
				plan += typed + "\n"
			case []byte:
				plan += string(typed) + "\n"
			}
		}
	}
	if !strings.Contains(plan, table+"_supersede") || !strings.Contains(plan, "SEARCH") {
		t.Fatalf("the supersede lane SELECT no longer seeks %s_supersede:\n%s", table, plan)
	}
}

// A store written under the producer-only rule carries BARE supersede keys.
// The first re-ingest of each object collapses them into the source's lane
// instead of stranding a generation no source can reach again — which is what
// makes this change safe to deploy without a store-wipe.
func TestSourceScopedSupersedeCollapsesPreScopingRows(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := openBootStore(t, basePath, bootTestValidator(t))
	defer store.Close()

	// An unattributed write is the pre-scoping shape: it stores the bare
	// object identity as its key.
	legacy := buildCATForTest("ISS legacy row", "1998-067A", 25544, "", "")
	if _, err := store.Store("CAT.fbs", legacy, "peer", nil); err != nil {
		t.Fatalf("legacy write: %v", err)
	}
	current := buildCATForTest("ISS current edition", "1998-067A", 25544, "", "")
	tags := SourceTags{ProviderID: "prov", SourceName: "satcat", BatchID: "edition-1"}
	if _, err := store.StoreBatchWithSourceTags("CAT.fbs", [][]byte{current}, "peer", nil, tags); err != nil {
		t.Fatalf("source-scoped ingest: %v", err)
	}
	if n, err := store.Count("CAT.fbs"); err != nil || n != 1 {
		t.Fatalf("CAT rows = %d (err %v), want 1 — the bare-key row must be collapsed, not stranded", n, err)
	}
	if _, err := store.GetRecord("CAT.fbs", computeCID(legacy)); err == nil {
		t.Fatal("the pre-scoping row survived the first source-scoped ingest")
	}
	if _, err := store.GetRecord("CAT.fbs", computeCID(current)); err != nil {
		t.Fatalf("the current record is missing: %v", err)
	}
}

// The supersede is per producer: another producer's copy of the same object
// is that producer's, and the CID stays a record while any producer holds it.
func TestCATSupersedeIsScopedToTheProducer(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := openBootStore(t, basePath, bootTestValidator(t))
	defer store.Close()

	v1 := buildCATForTest("ISS v1", "1998-067A", 25544, "", "")
	v2 := buildCATForTest("ISS v2", "1998-067A", 25544, "", "")
	if _, err := store.Store("CAT.fbs", v1, "peer-a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Store("CAT.fbs", v1, "peer-b", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Store("CAT.fbs", v2, "peer-a", nil); err != nil {
		t.Fatal(err)
	}
	// peer-a holds v2 only; peer-b still holds v1; v1 is still a record.
	if n, _ := store.Count("CAT.fbs"); n != 2 {
		t.Fatalf("distinct CAT rows = %d, want 2 (v1 under peer-b, v2 under peer-a)", n)
	}
	if _, err := store.GetRecord("CAT.fbs", computeCID(v1)); err != nil {
		t.Fatalf("v1 vanished although peer-b still holds it: %v", err)
	}
	if n := engineCount(t, store, "CAT"); n != 2 {
		t.Fatalf("engine CAT rows = %d, want 2", n)
	}
	// Now peer-b moves to v2 as well: v1's last holder is gone.
	if _, err := store.Store("CAT.fbs", v2, "peer-b", nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Count("CAT.fbs"); n != 1 {
		t.Fatalf("distinct CAT rows = %d, want 1", n)
	}
	if _, err := store.GetRecord("CAT.fbs", computeCID(v1)); err == nil {
		t.Fatal("v1 still readable after its last holder superseded it")
	}
	if n := indexCount(t, store, "CAT.fbs"); n != 1 {
		t.Fatalf("index rows = %d, want 1", n)
	}
	if n := engineCount(t, store, "CAT"); n != 1 {
		t.Fatalf("engine CAT rows = %d, want 1", n)
	}
}

// A delete reaches the engine at once, and the engine does not resurrect the
// row on a warm restart: the residency ledger re-applies what the engine
// forgot.
func TestDeleteAndSupersedeReachTheEngineAndSurviveARestart(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := openBootStore(t, basePath, bootTestValidator(t))

	omm := buildEngineOMM(t, 25544, "ISS", 1700000000)
	cid, err := store.Store("OMM.fbs", omm, "peer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := engineCount(t, store, "OMM"); n != 1 {
		t.Fatalf("engine OMM rows = %d, want 1", n)
	}
	if err := store.Delete("OMM.fbs", cid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := engineCount(t, store, "OMM"); n != 0 {
		t.Fatalf("engine OMM rows after Delete = %d, want 0 (delete must be mirrored)", n)
	}
	// Enough live rows that the arena is NOT mostly dead, so the restart takes
	// the warm path and has to re-apply the forgotten tombstones itself.
	for i := 0; i < 6; i++ {
		if _, err := store.Store("OMM.fbs", buildEngineOMM(t, uint32(30000+i), "LIVE", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	v1 := buildCATForTest("ISS v1", "1998-067A", 25544, "", "")
	v2 := buildCATForTest("ISS v2", "1998-067A", 25544, "", "")
	for _, data := range [][]byte{v1, v2} {
		if _, err := store.Store("CAT.fbs", data, "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := engineCount(t, store, "CAT"); n != 1 {
		t.Fatalf("engine CAT rows after supersede = %d, want 1", n)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	warm := openBootStore(t, basePath, bootTestValidator(t))
	defer warm.Close()
	if !warm.BootState().EngineWarm {
		t.Fatalf("expected a warm engine: %+v", warm.BootState())
	}
	if n := engineCount(t, warm, "OMM"); n != 6 {
		t.Fatalf("deleted OMM row came back after a restart: engine rows = %d, want 6", n)
	}
	if n := engineCount(t, warm, "CAT"); n != 1 {
		t.Fatalf("superseded CAT row came back after a restart: engine rows = %d", n)
	}
	// The ledger and the engine agree on WHICH row survived.
	warm.mu.RLock()
	rows, err := warm.engineResidencyRowsForCIDs("CAT.fbs", []string{computeCID(v1), computeCID(v2)}, nil)
	warm.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].cid != computeCID(v2) {
		t.Fatalf("residency ledger = %+v, want exactly v2", rows)
	}
}

// The residency ledger's seq is the engine's own _rowid: that identity is what
// makes MarkDeleted by ledger row exact.
func TestResidencyLedgerSequenceIsTheEngineRowID(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := openBootStore(t, basePath, bootTestValidator(t))
	defer store.Close()
	tags := SourceTags{ProviderID: "prov", SourceName: "seqcheck", BatchID: "b"}
	records := [][]byte{}
	for i := 0; i < 5; i++ {
		records = append(records, buildEngineOMM(t, uint32(60000+i), "SEQ", 1700000000+int64(i)))
	}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer", nil, tags); err != nil {
		t.Fatal(err)
	}
	ledger := map[int64]bool{}
	rows, err := store.db.Query(`SELECT seq FROM sdn_engine_rows WHERE schema_name = 'OMM.fbs' AND source = 'seqcheck'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		ledger[seq] = true
	}
	rows.Close()
	if len(ledger) != 5 {
		t.Fatalf("ledger holds %d rows, want 5", len(ledger))
	}
	res, err := store.engineDB.Query(`SELECT _rowid FROM "OMM@seqcheck"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 5 {
		t.Fatalf("engine partition holds %d rows, want 5", len(res.Rows))
	}
	for _, row := range res.Rows {
		seq, _ := row[0].(int64)
		if !ledger[seq] {
			t.Fatalf("engine _rowid %d is not in the ledger %v", seq, ledger)
		}
	}
}

// A crash between writes loses nothing that was committed and invents
// nothing that was not: the reopened store answers exactly the committed rows
// and the datasync cursor space is untouched.
func TestCrashMidIngestReopensToTheCommittedRows(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := openBootStore(t, basePath, bootTestValidator(t))
	tags := SourceTags{ProviderID: "prov", SourceName: "crash", BatchID: "b"}
	var cids []string
	for i := 0; i < 130; i++ { // more than one storeWriteChunkSize window
		data := buildEngineOMM(t, uint32(70000+i), "CRASH", 1700000000+int64(i))
		cids = append(cids, computeCID(data))
		if _, err := store.StoreBatchWithSourceTags("OMM.fbs", [][]byte{data}, "peer", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	var maxRowID int64
	if err := store.db.QueryRow(`SELECT MAX(rowid) FROM sdn_record_index`).Scan(&maxRowID); err != nil {
		t.Fatal(err)
	}
	simulateCrash(t, store)

	reopened := reopenDeferred(t, basePath)
	defer reopened.Close()
	if n, err := reopened.Count("OMM.fbs"); err != nil || n != int64(len(cids)) {
		t.Fatalf("rows after crash = %d (err %v), want %d", n, err, len(cids))
	}
	for _, cid := range cids {
		if _, err := reopened.GetRecord("OMM.fbs", cid); err != nil {
			t.Fatalf("record %s lost across the crash: %v", cid, err)
		}
	}
	var maxAfter int64
	if err := reopened.db.QueryRow(`SELECT MAX(rowid) FROM sdn_record_index`).Scan(&maxAfter); err != nil {
		t.Fatal(err)
	}
	if maxAfter != maxRowID {
		t.Fatalf("datasync cursor high-water mark moved across the crash: %d -> %d", maxRowID, maxAfter)
	}
	// The engine window comes back from the tables, complete.
	if _, err := reopened.HydrateEngineHotWindow(); err != nil {
		t.Fatal(err)
	}
	if n := engineCount(t, reopened, "OMM"); n != int64(len(cids)) {
		t.Fatalf("engine rows after cold rebuild = %d, want %d", n, len(cids))
	}
}

// The control database is the record store: a boot that cannot open it FAILS
// and leaves the file where it is. There is no second copy to rebuild from,
// so discarding it would be data loss dressed up as recovery.
func TestBootRefusesACorruptControlDatabaseWithoutDiscardingIt(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)
	store := openBootStore(t, basePath, v)
	if _, err := store.Store("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1700000000), "peer", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(basePath, flatSQLControlDBName)
	garbage := []byte("this is not a database, and it must not be deleted\n")
	if err := os.WriteFile(dbPath, garbage, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewFlatSQLStore(basePath, v)
	if err == nil {
		t.Fatal("open of a corrupt control database succeeded")
	}
	if !errors.Is(err, errControlDatabaseUnusable) {
		t.Fatalf("open error = %v, want errControlDatabaseUnusable", err)
	}
	after, readErr := os.ReadFile(dbPath)
	if readErr != nil {
		t.Fatalf("the corrupt control database was removed by the failed open: %v", readErr)
	}
	if string(after) != string(garbage) {
		t.Fatal("the failed open rewrote the control database")
	}
}

// The wizard-shaped open: nothing but the auxiliary tables is touched, and a
// standard the store has never seen still answers empty from the engine.
func TestFreshStoreAnswersEmptyForEveryRoutedStandard(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := reopenDeferred(t, basePath)
	defer store.Close()
	if n := engineCount(t, store, "CAT"); n != 0 {
		t.Fatalf("fresh store engine CAT rows = %d", n)
	}
	if _, err := store.HydrateEngineHotWindow(); err != nil {
		t.Fatal(err)
	}
	if !store.EngineHotWindowHydrated() {
		t.Fatal("hydration did not complete on an empty store")
	}
	_ = sds.SchemaNameToTable
}
