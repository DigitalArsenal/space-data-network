package storage

import (
	"fmt"
	"path/filepath"
	"testing"
)

// A supersede key that contains a NUL must survive the mirror's readback.
//
// recordSupersedeKey produces "uri:<CATALOG_URI>\x00<CATALOG_OBJECT_ID>" for a
// $CAT record carrying both fields (record_supersede.go). The repeat-CID mirror
// reads supersede_key back out of the read source, and through this driver a
// TEXT value is truncated at the first NUL — so the mirrored row landed under a
// key naming only the CATALOG, making every object in one catalog look like the
// same object to supersede. The column is read as a BLOB for that reason.
//
// This asserts the CONSEQUENCE rather than the byte count: two distinct objects
// in one catalog, mirrored into a second producer's table, must both survive.
func TestMirrorKeepsDistinctObjectsWithinOneCatalog(t *testing.T) {
	validator := bootTestValidator(t)
	store := openBootStore(t, filepath.Join(t.TempDir(), "db"), validator)
	defer store.Close()

	const uri = "https://example.test/catalog"
	// Same CATALOG_URI, DIFFERENT CATALOG_OBJECT_ID: distinct objects whose keys
	// differ only after the NUL.
	a := buildCATForTest("ALPHA", "1998-801A", 47001, uri, "OBJ-A")
	b := buildCATForTest("BRAVO", "1998-802A", 47002, uri, "OBJ-B")

	keyA := recordSupersedeKey("CAT.fbs", a)
	keyB := recordSupersedeKey("CAT.fbs", b)
	if keyA == keyB {
		t.Fatalf("fixture is wrong: both records produced the same key %q", keyA)
	}
	if got := len(keyA); got == len(uri)+4 {
		t.Fatalf("fixture is wrong: key %q carries no object id", keyA)
	}

	// Producer one holds both objects.
	for _, rec := range [][]byte{a, b} {
		if _, err := store.Store("CAT.fbs", rec, "peer-one", nil); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// Producer two sees the same two CIDs, which drives the repeat-CID mirror.
	for _, rec := range [][]byte{a, b} {
		if _, err := store.Store("CAT.fbs", rec, "peer-two", nil); err != nil {
			t.Fatalf("mirror: %v", err)
		}
	}

	table, err := store.ensureProducerStandardTable(routedProducerID("peer-two"), "CAT.fbs")
	if err != nil {
		t.Fatalf("table: %v", err)
	}
	var rows int
	if err := store.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Fatalf("producer two holds %d of 2 objects from one catalog — the truncated key made supersede treat them as the same object", rows)
	}

	// And the mirrored keys must still carry their object ids, so the NEXT
	// edition of each object retires the right row.
	var keys int
	if err := store.db.QueryRow(fmt.Sprintf(
		`SELECT COUNT(DISTINCT CAST(supersede_key AS BLOB)) FROM %s`, table)).Scan(&keys); err != nil {
		t.Fatalf("distinct keys: %v", err)
	}
	if keys != 2 {
		t.Fatalf("producer two's rows carry %d distinct supersede keys, want 2 — the object id was dropped on the way in", keys)
	}
}
