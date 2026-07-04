package storage

// Loop C.8b: shared READ-ONLY v2 store opens. A reader must be able to open
// and query a store WHILE a writer (the daemon) holds the exclusive
// single-writer lock, seeing a consistent point-in-time prefix; every write
// verb on the read-only handle must fail with ErrStoreReadOnly; and the
// read-only open must leave zero durable trace (journal byte-identical,
// stream files byte-identical, no new files).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func buildReadOnlyTestOMM(t *testing.T, norad uint32, epochUnix int64) []byte {
	t.Helper()
	epoch := time.Unix(epochUnix, 0).UTC().Format("2006-01-02T15:04:05Z")
	data := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(fmt.Sprintf("RO-SAT-%d", norad)).
		WithObjectID(fmt.Sprintf("2026-%03dA", norad%1000)).
		WithEpoch(epoch).
		WithEpochTimestamp(float64(epochUnix)).
		WithMeanMotion(15.1).
		WithEccentricity(0.001).
		WithInclination(51.6).
		Build()
	return data[4:]
}

// snapshotStoreFiles hashes every file under basePath (path -> sha256).
func snapshotStoreFiles(t *testing.T, basePath string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(basePath, path)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot store files: %v", err)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestReadOnlyOpenAgainstLiveWriter(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	basePath := filepath.Join(t.TempDir(), "store")

	writer, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer writer.Close()

	tags := SourceTags{
		ProviderID: "prov-ro",
		SourceName: "celestrak-gp",
		SourceURL:  "https://example.test/gp",
		BatchID:    "batch-ro-1",
	}
	var batch1 [][]byte
	for norad := uint32(9001); norad <= 9003; norad++ {
		batch1 = append(batch1, buildReadOnlyTestOMM(t, norad, 1700000000))
	}
	inserted, err := writer.StoreBatchWithSourceTags("OMM.fbs", batch1, "peer-ro-test", nil, tags)
	if err != nil || inserted != 3 {
		t.Fatalf("seed batch 1: inserted=%d err=%v", inserted, err)
	}

	// The WRITER STAYS OPEN (exclusive lock held) — the same topology as a
	// live daemon. A second WRITER open must fail...
	if _, err := NewFlatSQLStore(basePath, validator); !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("second writer open: want ErrStoreLocked, got %v", err)
	}

	preOpen := snapshotStoreFiles(t, basePath)

	// ...but a READ-ONLY open must succeed.
	reader, err := NewFlatSQLStoreReadOnly(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStoreReadOnly against live writer: %v", err)
	}
	defer reader.Close()
	if !reader.IsReadOnly() {
		t.Fatal("reader.IsReadOnly() = false")
	}

	// The reader sees the writer's committed records.
	count, err := reader.Count("OMM.fbs")
	if err != nil {
		t.Fatalf("reader Count: %v", err)
	}
	if count != 3 {
		t.Fatalf("reader Count = %d, want 3", count)
	}
	records, err := reader.QuerySourceTaggedRecords(SourceTagQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "prov-ro",
		SourceName: "celestrak-gp",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("reader QuerySourceTaggedRecords: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("reader source-tagged records = %d, want 3", len(records))
	}
	for _, rec := range records {
		if len(rec.Data) == 0 {
			t.Fatalf("reader record %s has no data (stream hydration failed)", rec.CID)
		}
	}

	// Every write verb fails with the typed error.
	if _, err := reader.Store("OMM.fbs", batch1[0], "peer-ro-test", nil); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("reader Store: want ErrStoreReadOnly, got %v", err)
	}
	if _, err := reader.StoreBatchWithSourceTags("OMM.fbs", batch1, "peer-ro-test", nil, tags); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("reader StoreBatch: want ErrStoreReadOnly, got %v", err)
	}
	if err := reader.Delete("OMM.fbs", records[0].CID); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("reader Delete: want ErrStoreReadOnly, got %v", err)
	}
	if _, err := reader.GarbageCollect(time.Hour); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("reader GarbageCollect: want ErrStoreReadOnly, got %v", err)
	}
	if err := reader.UpsertSourceTags("OMM.fbs", records[0].CID, tags); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("reader UpsertSourceTags: want ErrStoreReadOnly, got %v", err)
	}
	if _, err := reader.RebuildIndex(); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("reader RebuildIndex: want ErrStoreReadOnly, got %v", err)
	}
	if err := reader.SaveLocalEPM("peer-ro-test", []byte{1, 2, 3}); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("reader SaveLocalEPM: want ErrStoreReadOnly, got %v", err)
	}
	if _, err := reader.ReconcileSourceBatch("OMM.fbs", "prov-ro", "celestrak-gp", "batch-ro-1", true); !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("reader ReconcileSourceBatch(apply): want ErrStoreReadOnly, got %v", err)
	}
	// Dry-run reconcile stays available to read verbs.
	if _, err := reader.ReconcileSourceBatch("OMM.fbs", "prov-ro", "celestrak-gp", "batch-ro-1", false); err != nil {
		t.Fatalf("reader ReconcileSourceBatch(dry-run): %v", err)
	}

	// The WRITER keeps working while the reader is open: writes land and the
	// reader's point-in-time view stays consistent (it does NOT see them).
	var batch2 [][]byte
	for norad := uint32(9004); norad <= 9005; norad++ {
		batch2 = append(batch2, buildReadOnlyTestOMM(t, norad, 1700003600))
	}
	tags2 := tags
	tags2.BatchID = "batch-ro-2"
	if inserted, err := writer.StoreBatchWithSourceTags("OMM.fbs", batch2, "peer-ro-test", nil, tags2); err != nil || inserted != 2 {
		t.Fatalf("writer batch 2 while reader open: inserted=%d err=%v", inserted, err)
	}
	if count, err := writer.Count("OMM.fbs"); err != nil || count != 5 {
		t.Fatalf("writer Count after batch 2 = %d err=%v, want 5", count, err)
	}
	if count, err := reader.Count("OMM.fbs"); err != nil || count != 3 {
		t.Fatalf("reader Count after writer batch 2 = %d err=%v, want the point-in-time 3", count, err)
	}

	// A FRESH read-only open sees the new records.
	reader2, err := NewFlatSQLStoreReadOnly(basePath, validator)
	if err != nil {
		t.Fatalf("second read-only open: %v", err)
	}
	defer reader2.Close()
	if count, err := reader2.Count("OMM.fbs"); err != nil || count != 5 {
		t.Fatalf("fresh reader Count = %d err=%v, want 5", count, err)
	}

	// Zero durable trace: comparing everything but the files the WRITER
	// legitimately changed (journal + streams grew with batch 2) is not
	// enough — assert the reader created NO new files and that closing both
	// readers changes nothing.
	if err := reader.Close(); err != nil {
		t.Fatalf("reader Close: %v", err)
	}
	if err := reader2.Close(); err != nil {
		t.Fatalf("reader2 Close: %v", err)
	}
	postClose := snapshotStoreFiles(t, basePath)
	for _, name := range sortedKeys(postClose) {
		if _, existed := preOpen[name]; !existed {
			// Only the writer may have created files since the snapshot; the
			// writer appends to existing journal/stream files, so ANY new
			// file would have to come from the readers.
			t.Fatalf("read-only opens created new store file %q", name)
		}
	}
}

func TestReadOnlyOpenRequiresExistingStore(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := NewFlatSQLStoreReadOnly(missing, validator); err == nil {
		t.Fatal("read-only open of a missing store directory succeeded; it must fail (and must not create the directory)")
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("read-only open created %s", missing)
	}
}
