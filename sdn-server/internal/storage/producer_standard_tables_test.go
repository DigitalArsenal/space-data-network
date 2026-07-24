package storage

import (
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestProducerStandardTableName(t *testing.T) {
	name, err := ProducerStandardTableName("12D3KooWabc", "OMM.fbs")
	if err != nil {
		t.Fatalf("ProducerStandardTableName() error = %v", err)
	}
	if name != "sds_p_12D3KooWabc__OMM" {
		t.Errorf("name = %q, want sds_p_12D3KooWabc__OMM", name)
	}

	// Distinct producers -> distinct tables.
	other, err := ProducerStandardTableName("peerB", "OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if other == name {
		t.Error("distinct producers must yield distinct tables")
	}

	// Distinct standards -> distinct tables.
	oem, err := ProducerStandardTableName("12D3KooWabc", "OEM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if oem == name {
		t.Error("distinct standards must yield distinct tables")
	}

	// Non-identifier characters in the producer are sanitized.
	sani, err := ProducerStandardTableName("peer:with/weird.chars", "OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if sani != "sds_p_peer_with_weird_chars__OMM" {
		t.Errorf("sanitized = %q, want sds_p_peer_with_weird_chars__OMM", sani)
	}

	// Empty producer -> error.
	if _, err := ProducerStandardTableName("", "OMM.fbs"); err == nil {
		t.Error("expected error for empty producer")
	}
	// Invalid schema name -> error (propagated from SchemaNameToTable).
	if _, err := ProducerStandardTableName("peerA", "not a schema"); err == nil {
		t.Error("expected error for invalid schema name")
	}
}

func TestEnsureProducerStandardTable(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	name, err := store.ensureProducerStandardTable("peerA", "OMM.fbs")
	if err != nil {
		t.Fatalf("ensureProducerStandardTable() error = %v", err)
	}
	if name != "sds_p_peerA__OMM" {
		t.Errorf("name = %q, want sds_p_peerA__OMM", name)
	}
	if exists, err := store.tableExists(name); err != nil || !exists {
		t.Fatalf("table %s should exist after ensure (exists=%v err=%v)", name, exists, err)
	}

	// Idempotent: a second ensure returns the same table without error.
	name2, err := store.ensureProducerStandardTable("peerA", "OMM.fbs")
	if err != nil || name2 != name {
		t.Errorf("idempotent ensure failed: name=%q err=%v", name2, err)
	}

	// A different producer materializes a separate table.
	nameB, err := store.ensureProducerStandardTable("peerB", "OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if nameB == name {
		t.Error("distinct producer must create a distinct table")
	}
	if exists, err := store.tableExists(nameB); err != nil || !exists {
		t.Fatalf("peerB table %s should exist (exists=%v err=%v)", nameB, exists, err)
	}
}

func TestStoreRoutedByProducer(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	rowCount := func(table, cid string) int {
		var n int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE cid = ?", cid).Scan(&n); err != nil {
			t.Fatalf("count in %s: %v", table, err)
		}
		return n
	}

	data := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	cid, err := store.StoreRoutedByProducer("OMM.fbs", data, "peerA", nil)
	if err != nil {
		t.Fatalf("StoreRoutedByProducer() error = %v", err)
	}
	if cid == "" {
		t.Fatal("empty cid")
	}

	tableA := "sds_p_peerA__OMM"
	if got := rowCount(tableA, cid); got != 1 {
		t.Errorf("row count in %s = %d, want 1", tableA, got)
	}

	// Idempotent: re-storing the same record is a no-op.
	if _, err := store.StoreRoutedByProducer("OMM.fbs", data, "peerA", nil); err != nil {
		t.Fatal(err)
	}
	if got := rowCount(tableA, cid); got != 1 {
		t.Errorf("after re-store, count = %d, want 1", got)
	}

	// A different producer's record for the same schema lands in a separate table.
	data2 := sds.NewOMMBuilder().WithNoradCatID(40909).WithObjectName("SATELLITE").Build()
	cidB, err := store.StoreRoutedByProducer("OMM.fbs", data2, "peerB", nil)
	if err != nil {
		t.Fatal(err)
	}
	tableB := "sds_p_peerB__OMM"
	if got := rowCount(tableB, cidB); got != 1 {
		t.Errorf("peerB row count = %d, want 1", got)
	}
	// peerA's table must not contain peerB's record (producer separation).
	if got := rowCount(tableA, cidB); got != 0 {
		t.Errorf("peerA table leaked peerB record: %d", got)
	}
}

func TestParseProducerStandardTable(t *testing.T) {
	if p, s, ok := parseProducerStandardTable("sds_p_peerA__OMM"); !ok || p != "peerA" || s != "OMM" {
		t.Errorf("parse(sds_p_peerA__OMM) = %q, %q, %v", p, s, ok)
	}
	// Split on the LAST "__" so a sanitized producer containing "__" stays intact.
	if p, s, ok := parseProducerStandardTable("sds_p_a__b__OMM"); !ok || p != "a__b" || s != "OMM" {
		t.Errorf("parse(sds_p_a__b__OMM) = %q, %q, %v", p, s, ok)
	}
	if _, _, ok := parseProducerStandardTable("some_other_table"); ok {
		t.Error("non-matching name must not parse")
	}
	if _, _, ok := parseProducerStandardTable("sds_p_x"); ok {
		t.Error("missing __standard must not parse")
	}
}

func TestCrossTableRoutedQueries(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	ommA := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	ommB := sds.NewOMMBuilder().WithNoradCatID(40909).WithObjectName("SATELLITE").Build()
	if _, err := store.StoreRoutedByProducer("OMM.fbs", ommA, "peerA", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreRoutedByProducer("OMM.fbs", ommB, "peerB", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreRoutedByProducer("EPM.fbs", []byte("epm-record-peerA"), "peerA", nil); err != nil {
		t.Fatal(err)
	}

	// "all OMM across producers" -> both peerA and peerB.
	omm, err := store.QueryRoutedByStandard("OMM.fbs", 0)
	if err != nil {
		t.Fatalf("QueryRoutedByStandard: %v", err)
	}
	if len(omm) != 2 {
		t.Fatalf("OMM across producers = %d, want 2", len(omm))
	}
	producers := map[string]bool{}
	for _, r := range omm {
		producers[r.ProducerID] = true
		if r.Standard != "OMM" {
			t.Errorf("record standard = %q, want OMM", r.Standard)
		}
	}
	if !producers["peerA"] || !producers["peerB"] {
		t.Errorf("producers = %v, want {peerA, peerB}", producers)
	}

	// "all records from producer peerA" -> across standards OMM + EPM.
	fromA, err := store.QueryRoutedByProducer("peerA", 0)
	if err != nil {
		t.Fatalf("QueryRoutedByProducer: %v", err)
	}
	if len(fromA) != 2 {
		t.Fatalf("from peerA = %d, want 2", len(fromA))
	}
	standards := map[string]bool{}
	for _, r := range fromA {
		standards[r.Standard] = true
		if r.ProducerID != "peerA" {
			t.Errorf("record producer = %q, want peerA", r.ProducerID)
		}
	}
	if !standards["OMM"] || !standards["EPM"] {
		t.Errorf("standards = %v, want {OMM, EPM}", standards)
	}

	// Everything.
	all, err := store.QueryRoutedAll(0)
	if err != nil {
		t.Fatalf("QueryRoutedAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all routed records = %d, want 3", len(all))
	}

	// A standard with no records -> empty, not an error.
	none, err := store.QueryRoutedByStandard("CAT.fbs", 0)
	if err != nil {
		t.Fatalf("QueryRoutedByStandard(CAT): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("CAT across producers = %d, want 0", len(none))
	}
}

// WS7.3 phased write flip: the legacy Store path dual-writes the metadata row
// into the producer's (producer, standard) table.
func TestStoreDualWritesRoutedTable(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	data := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	cid, err := store.Store("OMM.fbs", data, "peerA", nil)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	// Legacy reader still sees it.
	if got, err := store.Get("OMM.fbs", cid); err != nil || len(got) == 0 {
		t.Fatalf("legacy Get() = (%d bytes, %v)", len(got), err)
	}

	// Producer-scoped routed queries see the same record.
	routed, err := store.QueryRoutedByProducer("peerA", 10)
	if err != nil {
		t.Fatalf("QueryRoutedByProducer() error = %v", err)
	}
	if len(routed) != 1 || routed[0].CID != cid || routed[0].Standard != "OMM" {
		t.Fatalf("routed = %+v, want 1 record with cid %s", routed, cid)
	}

	// A repeat CID from ANOTHER producer dedupes in the legacy table but still
	// lands a row in the second producer's table.
	if _, err := store.Store("OMM.fbs", data, "peerB", nil); err != nil {
		t.Fatalf("Store() repeat from peerB error = %v", err)
	}
	routedB, err := store.QueryRoutedByProducer("peerB", 10)
	if err != nil {
		t.Fatalf("QueryRoutedByProducer(peerB) error = %v", err)
	}
	if len(routedB) != 1 || routedB[0].CID != cid {
		t.Fatalf("routedB = %+v, want the deduped cid %s", routedB, cid)
	}

	// Empty producer identity (WS7.3d routed-only): the record lands under
	// the reserved "unattributed" producer instead of being dropped.
	anon := sds.NewOMMBuilder().WithNoradCatID(43013).WithObjectName("ANON").Build()
	if _, err := store.Store("OMM.fbs", anon, "", nil); err != nil {
		t.Fatalf("Store() with empty peer error = %v", err)
	}
	all, err := store.QueryRoutedAll(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("QueryRoutedAll = %d records, want 3 (peerA + peerB + unattributed)", len(all))
	}
	unattributed, err := store.QueryRoutedByProducer("unattributed", 10)
	if err != nil {
		t.Fatalf("QueryRoutedByProducer(unattributed) error = %v", err)
	}
	if len(unattributed) != 1 {
		t.Fatalf("unattributed = %+v, want 1 record", unattributed)
	}
}

func TestStoreBatchDualWritesRoutedTable(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	batch := [][]byte{
		sds.NewOMMBuilder().WithNoradCatID(1).WithObjectName("A").Build(),
		sds.NewOMMBuilder().WithNoradCatID(2).WithObjectName("B").Build(),
	}
	inserted, err := store.StoreBatch("OMM.fbs", batch, "peerBatch", nil)
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted = %d, want 2", inserted)
	}
	routed, err := store.QueryRoutedByProducer("peerBatch", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(routed) != 2 {
		t.Fatalf("routed rows = %d, want 2", len(routed))
	}

	// Re-storing the same batch dedupes in the legacy table and leaves the
	// routed table unchanged (INSERT OR IGNORE on repeat CIDs).
	if _, err := store.StoreBatch("OMM.fbs", batch, "peerBatch", nil); err != nil {
		t.Fatalf("repeat StoreBatch() error = %v", err)
	}
	routedAgain, err := store.QueryRoutedByProducer("peerBatch", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(routedAgain) != 2 {
		t.Fatalf("routed rows after repeat = %d, want 2", len(routedAgain))
	}
}

// WS7.3b: hydrating readers span the legacy and (producer, standard) tables —
// a record that exists ONLY in a producer table is visible to the standard
// read surface, and dual-written records are not double-counted.
func TestHydratingReadersSpanProducerTables(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	// Routed-only record (never written to the legacy table).
	routedData := sds.NewOMMBuilder().WithNoradCatID(11111).WithObjectName("ROUTED-ONLY").Build()
	routedCID, err := store.StoreRoutedByProducer("OMM.fbs", routedData, "peerRouted", nil)
	if err != nil {
		t.Fatalf("StoreRoutedByProducer() error = %v", err)
	}
	// Dual-written record (legacy + mirror).
	dualData := sds.NewOMMBuilder().WithNoradCatID(22222).WithObjectName("DUAL").Build()
	dualCID, err := store.Store("OMM.fbs", dualData, "peerDual", nil)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	// Get hydrates the routed-only record.
	got, err := store.Get("OMM.fbs", routedCID)
	if err != nil || len(got) == 0 {
		t.Fatalf("Get(routed-only) = (%d bytes, %v)", len(got), err)
	}

	// QueryAll sees both, each exactly once.
	all, err := store.QueryAll("OMM.fbs", 100)
	if err != nil {
		t.Fatalf("QueryAll() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("QueryAll() = %d records, want 2 (routed-only + dual, deduped)", len(all))
	}

	// Count dedupes dual-written rows.
	count, err := store.Count("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("Count() = %d, want 2", count)
	}

	// The materialized index join hydrates routed-only records too.
	indexed, err := store.QueryByIndexedFields("OMM.fbs", "", func() *uint32 { v := uint32(11111); return &v }(), "", 10)
	if err != nil {
		t.Fatalf("QueryByIndexedFields() error = %v", err)
	}
	if len(indexed) != 1 || indexed[0].CID != routedCID {
		t.Fatalf("indexed = %+v, want the routed-only record %s", indexed, routedCID)
	}
	_ = dualCID
}

// WS7.3c: the raw-record family (datasync's rowid-based sync cursors and
// snapshot hashes) reads the legacy tables only, and dual-write mirrors must
// not perturb it — same counts, same head, no duplicate refs — so incremental
// sync against peers stays byte-stable until legacy retirement lands with a
// protocol-versioned cursor redesign.
func TestRawRecordCursorsUnaffectedByRoutedMirrors(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	for i := 0; i < 3; i++ {
		data := sds.NewOMMBuilder().WithNoradCatID(uint32(1000 + i)).WithObjectName("SYNC").Build()
		if _, err := store.Store("OMM.fbs", data, "peerSync", nil); err != nil {
			t.Fatalf("Store #%d failed: %v", i, err)
		}
	}

	filter := RawRecordQuery{SchemaName: "OMM.fbs"}
	count, err := store.CountRawRecords(filter)
	if err != nil {
		t.Fatalf("CountRawRecords failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("CountRawRecords = %d, want 3 (mirrors must not double-count)", count)
	}

	refs, err := store.QueryRawRecordRefs(filter)
	if err != nil {
		t.Fatalf("QueryRawRecordRefs failed: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("refs = %d, want 3", len(refs))
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.CID] {
			t.Fatalf("duplicate ref for cid %s", ref.CID)
		}
		seen[ref.CID] = true
	}

	head, err := store.RawRecordHead(filter)
	if err != nil {
		t.Fatalf("RawRecordHead failed: %v", err)
	}
	if head.MaxRowID != 3 {
		t.Fatalf("MaxRowID = %d, want 3 (legacy-table rowid sequence)", head.MaxRowID)
	}
}
