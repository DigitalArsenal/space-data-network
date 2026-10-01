package storage

// The format-1 reads store-migrate --to 4 adds (migration_source_format4.go):
// index rows with format 1's structured columns and every table's copy, in
// rowid pages, and the IQC ingest identities as digests.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestMigrationSourceFormat4IndexPagesAndIdentities(t *testing.T) {
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
	all, err := src.IndexPageCopies("OMM.fbs", bySchema["OMM.fbs"], 0, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(omm) {
		t.Fatalf("%d OMM rows, want %d", len(all), len(omm))
	}
	page, err := src.IndexPageCopies("OMM.fbs", bySchema["OMM.fbs"], all[3].RowID, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 4 || page[0].RowID != all[4].RowID || page[3].RowID != all[7].RowID || page[0].Copies != nil || !page[0].Held[0] {
		t.Fatalf("page after rowid %d: %+v", all[3].RowID, page)
	}
	// The copies are the table's rows; the columns are what format 1
	// extracted from each record.
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
		want := recs[e.CID]
		want.RowID, want.SupersedeKey = e.RowID, ""
		if e.Orphan() || len(e.Copies) != 1 || fmt.Sprintf("%+v", e.Copies[0]) != fmt.Sprintf("%+v", want) {
			t.Fatalf("%s: copies %+v, the table holds %+v", e.CID, e.Copies, want)
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

// A schema held by more tables than one query joins is read in groups over
// the same rows: every table's copy lands in its slot, and an orphan holds
// none.
func TestMigrationSourceFormat4CopiesAcrossTableGroups(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFlatSQLStore(dir, bootTestValidator(t), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const producers = 2*copyJoinGroup + 7
	const schema = "OMM.fbs"
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	tables := make([]LegacyTable, producers)
	for i := range tables {
		name, err := ProducerStandardTableName(fmt.Sprintf("peer%03d", i), schema)
		if err != nil {
			t.Fatal(err)
		}
		tables[i] = LegacyTable{Name: name, Token: fmt.Sprintf("peer%03d", i), Schema: schema}
		exec(fmt.Sprintf(`CREATE TABLE %s (cid TEXT PRIMARY KEY, peer_id TEXT NOT NULL, timestamp INTEGER NOT NULL,
			data BLOB NOT NULL, record_length INTEGER NOT NULL, signature_hex TEXT, supersede_key TEXT, created_at INTEGER)`, name))
	}
	// Record r is held by the tables i with i % (r+1) == 0; "orphan" by none.
	const records = 9
	for r := 0; r < records; r++ {
		cid := fmt.Sprintf("c-%d", r)
		exec(`INSERT INTO sdn_record_index (schema_name, cid, norad_cat_id, source_timestamp) VALUES (?, ?, ?, 1)`, schema, cid, 100+r)
		for i := range tables {
			if i%(r+1) == 0 {
				exec(fmt.Sprintf(`INSERT INTO %s (cid, peer_id, timestamp, data, record_length, signature_hex) VALUES (?, ?, ?, ?, 1, ?)`,
					tables[i].Name), cid, tables[i].Token, 1000+i, []byte{byte(i), byte(r)}, fmt.Sprintf("%02x", i))
			}
		}
		if r == 4 {
			exec(`INSERT INTO sdn_record_index (schema_name, cid, source_timestamp) VALUES (?, 'orphan', 1)`, schema)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	src := &MigrationSource{s: s, base: dir}
	var got []IndexCopies
	for after := int64(0); ; {
		page, err := src.IndexPageCopies(schema, tables, after, 4, true)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page...)
		if len(page) < 4 {
			break
		}
		after = page[len(page)-1].RowID
	}
	if len(got) != records+1 {
		t.Fatalf("%d rows, want %d", len(got), records+1)
	}
	r := 0
	for _, e := range got {
		if e.CID == "orphan" {
			if !e.Orphan() {
				t.Fatalf("the orphan is held: %v", e.Held)
			}
			continue
		}
		if e.CID != fmt.Sprintf("c-%d", r) || e.NoradCatID.Int64 != int64(100+r) {
			t.Fatalf("row %+v, want c-%d", e.IndexEntry, r)
		}
		for i := range tables {
			held := i%(r+1) == 0
			if e.Held[i] != held {
				t.Fatalf("c-%d table %d: held %v, want %v", r, i, e.Held[i], held)
			}
			if c := e.Copies[i]; held && (c.PeerID != tables[i].Token || c.Timestamp != int64(1000+i) ||
				string(c.Stored) != string([]byte{byte(i), byte(r)}) || c.SignatureHex != fmt.Sprintf("%02x", i)) {
				t.Fatalf("c-%d table %d: copy %+v", r, i, c)
			}
		}
		r++
	}
}
