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

// A mirrored row must be retirable by the source that owns it.
//
// The repeat-CID mirror re-derives the supersede key from the key stored on
// ANOTHER producer's row. When the lane became (producer, source) that key
// already carried a scope, so re-applying this lane's scope on top produced
// "src:<source>\x00src:<source>\x00<identity>" — a key no legitimate write's
// match set contains, leaving a row no source could ever retire. The symptom is
// not loss but NON-CONVERGENCE: one object under one source accumulating a row
// per edition forever.
//
// supersedeKeysForStoredKey strips the stored scope before re-applying this
// one. This holds it to that, through the store's real write path.
func TestMirroredRowIsRetirableByItsOwnSource(t *testing.T) {
	validator := bootTestValidator(t)
	store := openBootStore(t, filepath.Join(t.TempDir(), "db"), validator)
	defer store.Close()

	tags := SourceTags{ProviderID: "prov", SourceName: "satcat", BatchID: "b1", ContentKeyID: "public"}
	v1 := buildCATForTest("ISS v1", "1998-067A", 25544, "", "")
	v2 := buildCATForTest("ISS v2", "1998-067A", 25544, "", "")

	// Producer one publishes edition 1; producer two then sees the SAME CID,
	// which is what drives the repeat-CID mirror into producer two's table.
	if _, err := store.StoreWithSourceTags("CAT.fbs", v1, "peer-one", nil, tags); err != nil {
		t.Fatalf("seed producer one: %v", err)
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", v1, "peer-two", nil, tags); err != nil {
		t.Fatalf("mirror into producer two: %v", err)
	}

	table, err := store.ensureProducerStandardTable(routedProducerID("peer-two"), "CAT.fbs")
	if err != nil {
		t.Fatalf("table: %v", err)
	}
	var mirrored int
	if err := store.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&mirrored); err != nil {
		t.Fatalf("count mirrored: %v", err)
	}
	if mirrored != 1 {
		t.Fatalf("producer two holds %d rows after the mirror, want 1", mirrored)
	}

	// Edition 2 of the SAME object from the SAME source must retire the mirrored
	// row rather than sit beside it.
	if _, err := store.StoreWithSourceTags("CAT.fbs", v2, "peer-two", nil, tags); err != nil {
		t.Fatalf("second edition: %v", err)
	}
	var after int
	if err := store.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != 1 {
		var keys string
		rows, _ := store.db.Query(fmt.Sprintf(`SELECT quote(CAST(supersede_key AS BLOB)) FROM %s`, table))
		if rows != nil {
			for rows.Next() {
				var k string
				_ = rows.Scan(&k)
				keys += " " + k
			}
			rows.Close()
		}
		t.Fatalf("one object from one source left %d rows under producer two — the mirrored row is unreachable by its own source; keys:%s", after, keys)
	}
}
