package storage

// Computable outcomes for the storage-cost work
// (sdn-cellular-ingest-store-write-is-the-bottleneck):
//
//  1. the per-record ON-DISK OVERHEAD is bounded,
//  2. the batched engine mirror lands EXACTLY what the per-record one landed,
//  3. both boot migrations are idempotent and preserve the public read surface.
//
// These assert numbers and equalities, not wiring.

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// maxOverheadBytesPerRecord is the ceiling on everything the store writes to
// disk for one record BESIDES the record's own bytes: the control database
// (record index + interned source tags + the producer/standard row + the
// engine's own decorations) and the record-catalog journal.
//
// Measured on 20,000 REAL $TBS records from host-02's stream (703 B each):
//
//	before this task .... 3,610.0 B/record total, 2,903.3 B/record overhead
//	after ............... 2,174.5 B/record total, 1,467.8 B/record overhead
//
// The ceiling is set just above the measured figure so that a regression which
// re-introduces a per-record copy of the provenance strings (1,525 B/record),
// a duplicate index, or a full index over all-NULL satellite keys (154 B/record)
// fails here instead of on a droplet.
const maxOverheadBytesPerRecord = 1500

// syntheticTBSFixture builds n distinct $TBS records. It exists so this test
// needs no downloaded fixture: per-record overhead is per RECORD, not per
// byte, so a smaller payload measures the same thing.
func syntheticTBSFixture(n int) [][]byte {
	records := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, newTBSRecord(
			fmt.Sprintf("cost-fixture-%08d", i), "cost-fixture-provider",
			uint32(200+i%600), -80+float64(i%16000)/100.0, -170+float64(i%34000)/100.0))
	}
	return records
}

func measurementTags() SourceTags {
	return SourceTags{
		ProviderID:        "mls-archive",
		SourceName:        "mls-final-full-cell-export",
		SourceURL:         "https://example.invalid/export/MLS-full-cell-export-2024-05-01T000000.csv.gz",
		BatchID:           "mls-archive@0",
		ProducerPeerID:    "12D3KooWQYV9dGMFoRzNStwpXztXaBUjtPqi6aU76ZgUriHhKust",
		ProducerPublicKey: "08011220b9cbd0a3fdbb5a1e2f4c8d9e0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3",
	}
}

func newMeasurementStore(t *testing.T, base string) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore(%s): %v", base, err)
	}
	return store
}

func storeDirBytes(t *testing.T, base string) int64 {
	t.Helper()
	var total int64
	if err := filepath.Walk(base, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
	return total
}

func TestStoreOverheadPerRecordIsBounded(t *testing.T) {
	// MARGINAL, not average. A store carries fixed costs that have nothing to
	// do with record count (the SQLite schema page for ~850 objects, the
	// engine's r-tree roots, page granularity), and on a small fixture those
	// swamp the per-record figure. So this measures what host-02 was measured
	// on: ingest a batch, snapshot the bytes, ingest a SECOND identical-sized
	// batch, and divide the growth by the records that caused it.
	const n = 3000
	first := syntheticTBSFixture(n)
	second := syntheticTBSFixture(2 * n)[n:]

	base := filepath.Join(t.TempDir(), "db")
	store := newMeasurementStore(t, base)
	if inserted, err := store.StoreBatchWithSourceTags("TBS.fbs", first, "cost-fixture-peer", nil, measurementTags()); err != nil {
		t.Fatalf("StoreBatchWithSourceTags (warm-up): %v", err)
	} else if inserted != n {
		t.Fatalf("warm-up inserted %d records, want %d", inserted, n)
	}
	before := storeDirBytes(t, base)

	if inserted, err := store.StoreBatchWithSourceTags("TBS.fbs", second, "cost-fixture-peer", nil, measurementTags()); err != nil {
		t.Fatalf("StoreBatchWithSourceTags (measured): %v", err)
	} else if inserted != n {
		t.Fatalf("measured batch inserted %d records, want %d", inserted, n)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	after := storeDirBytes(t, base)

	var payload int64
	for _, r := range second {
		payload += int64(len(r))
	}
	perRecord := float64(after-before) / float64(n)
	overhead := float64(after-before-payload) / float64(n)
	t.Logf("store grew %d B for %d records (%.1f B/record); payload %.1f B/record; OVERHEAD %.1f B/record",
		after-before, n, perRecord, float64(payload)/float64(n), overhead)
	if overhead > maxOverheadBytesPerRecord {
		t.Fatalf("per-record on-disk overhead is %.1f B, ceiling is %d B — a per-record copy of something that should be interned has come back",
			overhead, maxOverheadBytesPerRecord)
	}
}

// TestBatchedEngineMirrorMatchesPerRecordMirror pins the parity that lets the
// batched path replace the per-record one: same rows, same count, same
// answers from the unified view.
func TestBatchedEngineMirrorMatchesPerRecordMirror(t *testing.T) {
	const n = 200
	records := syntheticTBSFixture(n)
	binding, routed := engineRoutedSchemaFor("TBS.fbs")
	if !routed {
		t.Skip("TBS.fbs is not engine-routed in this build")
	}

	payloads := make([][]byte, 0, n)
	sources := make([]string, 0, n)
	for _, r := range records {
		p, reason, ok := engineIngestablePayload(binding, r)
		if !ok {
			t.Fatalf("engineIngestablePayload refused a fixture record: %s", reason)
		}
		payloads = append(payloads, p)
		sources = append(sources, "parity/source-a")
	}

	counts := map[string]int64{}
	for _, mode := range []string{"batched", "per-record"} {
		store := newMeasurementStore(t, filepath.Join(t.TempDir(), "db"))
		if err := store.ensureEngineSource("parity/source-a"); err != nil {
			t.Fatalf("ensureEngineSource: %v", err)
		}
		switch mode {
		case "batched":
			ingested, ok := store.bulkIngestEngineGroups(groupEngineIngests(sources, payloads))
			if !ok {
				t.Fatal("batched engine mirror refused a batch it should have taken")
			}
			if ingested != int64(n) {
				t.Fatalf("batched mirror reported %d records, want %d", ingested, n)
			}
		case "per-record":
			for i, p := range payloads {
				if _, err := store.engineDB.IngestOneWithSource(p, sources[i]); err != nil {
					t.Fatalf("IngestOneWithSource: %v", err)
				}
			}
		}
		res, err := store.engineDB.Query(`SELECT COUNT(*) FROM "TBS"`)
		if err != nil {
			t.Fatalf("%s: count unified view: %v", mode, err)
		}
		switch v := res.Rows[0][0].(type) {
		case int64:
			counts[mode] = v
		case float64:
			counts[mode] = int64(v)
		default:
			t.Fatalf("%s: unexpected count type %T", mode, v)
		}
		_ = store.Close()
	}
	if counts["batched"] != counts["per-record"] {
		t.Fatalf("batched mirror landed %d rows, per-record mirror landed %d — the batched path is not a drop-in",
			counts["batched"], counts["per-record"])
	}
	if counts["batched"] != int64(n) {
		t.Fatalf("both mirrors landed %d rows, want %d", counts["batched"], n)
	}
}

// TestSizePrefixedStreamRoundTrip pins the framing the bulk export consumes.
func TestSizePrefixedStreamRoundTrip(t *testing.T) {
	payloads := [][]byte{[]byte("a"), []byte("bcd"), {}, []byte("efghij")}
	stream := sizePrefixedStream(payloads)
	var got [][]byte
	for off := 0; off+4 <= len(stream); {
		n := int(binary.LittleEndian.Uint32(stream[off : off+4]))
		got = append(got, stream[off+4:off+4+n])
		off += 4 + n
	}
	if len(got) != len(payloads) {
		t.Fatalf("round-tripped %d frames, want %d", len(got), len(payloads))
	}
	for i := range payloads {
		if string(got[i]) != string(payloads[i]) {
			t.Fatalf("frame %d round-tripped as %q, want %q", i, got[i], payloads[i])
		}
	}
}

// TestSchemaNameCanonicalizationIsIdempotent seeds rows under the bare code
// the cellular module used to stamp and pins that a boot moves them to the
// canonical name, that a second boot changes nothing, and that no row is lost
// when the same CID already exists under both spellings.
func TestSchemaNameCanonicalizationIsIdempotent(t *testing.T) {
	base := filepath.Join(t.TempDir(), "db")
	store := newMeasurementStore(t, base)

	records := syntheticTBSFixture(6)
	if _, err := store.StoreBatchWithSourceTags("TBS.fbs", records, "canon-peer", nil, measurementTags()); err != nil {
		t.Fatalf("seed canonical records: %v", err)
	}

	// Re-file three of them under the bare code, exactly as a pre-39662af9
	// write would have left them, including one CID that stays present under
	// BOTH spellings.
	if _, err := store.db.Exec(`
		INSERT OR IGNORE INTO sdn_record_index (schema_name, cid, source_timestamp)
		SELECT 'TBS', cid, source_timestamp FROM sdn_record_index WHERE schema_name = 'TBS.fbs' LIMIT 3
	`); err != nil {
		t.Fatalf("seed legacy-spelling rows: %v", err)
	}
	if _, err := store.db.Exec(`
		DELETE FROM sdn_record_index
		WHERE schema_name = 'TBS.fbs'
		  AND cid IN (SELECT cid FROM sdn_record_index WHERE schema_name = 'TBS' LIMIT 2)
	`); err != nil {
		t.Fatalf("strand legacy-spelling rows: %v", err)
	}

	legacyBefore := countRows(t, store, `SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = 'TBS'`)
	if legacyBefore == 0 {
		t.Fatal("fixture did not produce any bare-code rows to correct")
	}

	if err := store.canonicalizeStoredSchemaNames(); err != nil {
		t.Fatalf("canonicalizeStoredSchemaNames: %v", err)
	}
	if left := countRows(t, store, `SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = 'TBS'`); left != 0 {
		t.Fatalf("%d row(s) still filed under the bare code after canonicalization", left)
	}
	canonical := countRows(t, store, `SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = 'TBS.fbs'`)
	if canonical != int64(len(records)) {
		t.Fatalf("canonical rows = %d, want %d — canonicalization lost or duplicated records", canonical, len(records))
	}

	if err := store.canonicalizeStoredSchemaNames(); err != nil {
		t.Fatalf("second canonicalizeStoredSchemaNames: %v", err)
	}
	if again := countRows(t, store, `SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = 'TBS.fbs'`); again != canonical {
		t.Fatalf("second run changed the canonical row count from %d to %d — not idempotent", canonical, again)
	}
	_ = store.Close()
}

// TestSourceTagsViewPreservesTheReadSurface pins that the interned layout
// answers the legacy table's queries with the legacy table's columns.
func TestSourceTagsViewPreservesTheReadSurface(t *testing.T) {
	store := newMeasurementStore(t, filepath.Join(t.TempDir(), "db"))
	defer store.Close()

	records := syntheticTBSFixture(5)
	tags := measurementTags()
	if _, err := store.StoreBatchWithSourceTags("TBS.fbs", records, "view-peer", nil, tags); err != nil {
		t.Fatalf("StoreBatchWithSourceTags: %v", err)
	}

	rows, err := store.db.Query(`
		SELECT schema_name, cid, provider_id, source_name, source_url, batch_id,
		       content_key_id, producer_peer_id, producer_public_key, created_at
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`, "TBS.fbs", tags.ProviderID, tags.SourceName, tags.BatchID)
	if err != nil {
		t.Fatalf("query source tags view: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var schema, cid, provider, source, url, batch, contentKey, peer, key string
		var createdAt int64
		if err := rows.Scan(&schema, &cid, &provider, &source, &url, &batch, &contentKey, &peer, &key, &createdAt); err != nil {
			t.Fatalf("scan source tags view: %v", err)
		}
		if provider != tags.ProviderID || source != tags.SourceName || url != tags.SourceURL ||
			batch != tags.BatchID || peer != tags.ProducerPeerID || key != tags.ProducerPublicKey {
			t.Fatalf("view returned %q/%q/%q/%q/%q/%q, want the tags that were written",
				provider, source, url, batch, peer, key)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate source tags view: %v", err)
	}
	if seen != len(records) {
		t.Fatalf("view returned %d tag rows, want %d", seen, len(records))
	}

	// ONE dictionary row for a batch that carries one provenance tuple: this
	// is the whole point of the layout.
	if got := countRows(t, store, `SELECT COUNT(*) FROM `+sourceProvenanceTable); got != 1 {
		t.Fatalf("interned %d provenance rows for one batch, want 1", got)
	}
}

// TestLegacySourceTagsTableIsInternedIdempotently seeds the pre-interning
// PHYSICAL table and pins that a boot migrates it, that the view then answers
// the same rows, and that a second run is a no-op.
func TestLegacySourceTagsTableIsInternedIdempotently(t *testing.T) {
	base := filepath.Join(t.TempDir(), "db")
	store := newMeasurementStore(t, base)
	defer store.Close()

	// Put the store back into the pre-interning shape: drop the view, create
	// the legacy physical table, fill it.
	if _, err := store.db.Exec(`DROP VIEW IF EXISTS ` + sourceTagsViewName); err != nil {
		t.Fatalf("drop view: %v", err)
	}
	if _, err := store.db.Exec(sourceTagsTableSQL(sourceTagsViewName)); err != nil {
		t.Fatalf("create legacy source tags table: %v", err)
	}
	tags := measurementTags()
	const legacyRows = 25
	for i := 0; i < legacyRows; i++ {
		if _, err := store.db.Exec(`
			INSERT INTO `+sourceTagsViewName+` (
				schema_name, cid, provider_id, source_name, source_url, batch_id,
				content_key_id, producer_peer_id, producer_public_key, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "TBS.fbs", fmt.Sprintf("bafkreilegacy%08d", i), tags.ProviderID, tags.SourceName,
			tags.SourceURL, tags.BatchID, "", tags.ProducerPeerID, tags.ProducerPublicKey, int64(1700000000+i)); err != nil {
			t.Fatalf("seed legacy tag row %d: %v", i, err)
		}
	}

	if err := store.migrateSourceTagsToProvenance(); err != nil {
		t.Fatalf("migrateSourceTagsToProvenance: %v", err)
	}
	if isTable, err := store.tableExists(sourceTagsViewName); err != nil || isTable {
		t.Fatalf("legacy physical table still present after migration (err=%v)", err)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM `+sourceTagsViewName); got != legacyRows {
		t.Fatalf("view returns %d rows after migration, want %d", got, legacyRows)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM `+sourceProvenanceTable); got != 1 {
		t.Fatalf("interned %d provenance rows for one tuple, want 1", got)
	}

	if err := store.migrateSourceTagsToProvenance(); err != nil {
		t.Fatalf("second migrateSourceTagsToProvenance: %v", err)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM `+sourceTagsViewName); got != legacyRows {
		t.Fatalf("second run changed the row count to %d, want %d — not idempotent", got, legacyRows)
	}
}

func countRows(t *testing.T, store *FlatSQLStore, query string) int64 {
	t.Helper()
	var n int64
	if err := store.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}
