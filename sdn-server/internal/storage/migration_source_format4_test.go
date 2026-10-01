package storage

// The format-1 reads store-migrate --to 4 adds (migration_source_format4.go):
// held index rows with format 1's structured columns, in rowid pages, and the
// IQC ingest identities as digests.

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestMigrationSourceFormat4IndexEntriesAndIdentities(t *testing.T) {
	dir := t.TempDir()
	s := openBootStore(t, dir, bootTestValidator(t))
	defer s.Close()

	// IQC with a lane: each record gets an ingest identity row.
	iqc := identityTestBatch(12, "2026-09-15T02:11:22Z")
	if _, err := s.StoreBatchWithSourceTags("IQC.fbs", iqc, "module:sigmf", nil, identityTestTags("b1")); err != nil {
		t.Fatal(err)
	}
	// OMM: one record without a NORAD number (its index row has no
	// norad_cat_id), the rest with one.
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix()
	var omm [][]byte
	for i := 0; i < 9; i++ {
		omm = append(omm, buildEngineOMM(t, uint32(i), "OBJ", base+int64(i)*3600))
	}
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", omm, "source:celestrak", nil,
		SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-1"}); err != nil {
		t.Fatal(err)
	}

	src := &MigrationSource{s: s, base: dir}
	tables, err := src.ProducerTables()
	if err != nil {
		t.Fatal(err)
	}
	bySchema := map[string][]LegacyTable{}
	for _, tb := range tables {
		bySchema[tb.Schema] = append(bySchema[tb.Schema], tb)
	}

	// Pages: rowid order, after the cursor, at most limit rows.
	all, err := src.HeldIndexEntries("OMM.fbs", bySchema["OMM.fbs"], 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(omm) {
		t.Fatalf("%d held OMM rows, want %d", len(all), len(omm))
	}
	page, err := src.HeldIndexEntries("OMM.fbs", bySchema["OMM.fbs"], all[3].RowID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 4 || page[0].RowID != all[4].RowID || page[3].RowID != all[7].RowID {
		t.Fatalf("page after rowid %d: %+v", all[3].RowID, page)
	}
	// The columns are what format 1 extracted from each record.
	cids := make([]string, len(all))
	for i, e := range all {
		cids[i] = e.CID
	}
	recs, err := src.RecordsByCID(bySchema["OMM.fbs"][0], cids)
	if err != nil {
		t.Fatal(err)
	}
	withoutNorad := 0
	for i, e := range all {
		if i > 0 && e.RowID <= all[i-1].RowID {
			t.Fatalf("rows out of rowid order: %d after %d", e.RowID, all[i-1].RowID)
		}
		f, err := extractIndexedFields("OMM.fbs", recs[e.CID].Plain)
		if err != nil {
			t.Fatal(err)
		}
		if (f.noradCatID != nil) != e.NoradCatID.Valid || (f.noradCatID != nil && int64(*f.noradCatID) != e.NoradCatID.Int64) {
			t.Fatalf("%s norad: entry %+v, record %v", e.CID, e.NoradCatID, f.noradCatID)
		}
		if !e.NoradCatID.Valid {
			withoutNorad++
		}
		if !e.EntityID.Valid || e.EntityID.String != f.entityID {
			t.Fatalf("%s entity: entry %+v, record %q", e.CID, e.EntityID, f.entityID)
		}
		if !e.EpochUnix.Valid || e.EpochUnix.Int64 != *f.epochUnix || e.EpochDay.String != f.epochDay {
			t.Fatalf("%s epoch: entry %+v %+v, record %d %s", e.CID, e.EpochUnix, e.EpochDay, *f.epochUnix, f.epochDay)
		}
		if e.ObjectType.Valid || e.OpsStatusCode.Valid {
			t.Fatalf("%s: an OMM row with CAT columns %+v", e.CID, e)
		}
	}
	if withoutNorad != 1 {
		t.Fatalf("%d rows without a NORAD number, want 1", withoutNorad)
	}

	// Identities: one per IQC record, the digest recordIngestIdentity names.
	ids, err := src.IngestIdentities("IQC.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(iqc) {
		t.Fatalf("%d IQC identities, want %d", len(ids), len(iqc))
	}
	for _, rec := range iqc {
		id := recordIngestIdentity("IQC.fbs", rec)
		raw, _ := hex.DecodeString(id[len(ingestIdentityPrefix):])
		var want [32]byte
		copy(want[:], raw)
		if got, ok := ids[computeCID(rec)]; !ok || got != want {
			t.Fatalf("identity of %s: %x (%v), want %x", computeCID(rec), got, ok, want)
		}
	}
	if ids, err := src.IngestIdentities("OMM.fbs"); err != nil || len(ids) != 0 {
		t.Fatalf("OMM identities %v, %v; want none", ids, err)
	}
	if _, err := ingestIdentityDigest("ingest-v2:" + hex.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("an identity of another version was accepted")
	}
	if d, err := ingestIdentityDigest(ingestIdentityPrefix + hex.EncodeToString(func() []byte { h := sha256.Sum256([]byte("x")); return h[:] }())); err != nil || d != sha256.Sum256([]byte("x")) {
		t.Fatalf("digest of a valid identity: %x %v", d, err)
	}
}
