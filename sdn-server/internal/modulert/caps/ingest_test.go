package caps

// Loop C.8a: storage.ingest_with_source — the provenance/batch-capable flow
// ingest cap op. Real FlatSQLStore, real record streams; asserts capability
// policy, SourceTags attribution, replay idempotence (reconcile), current-
// batch reconcile, disk guardrail, and raw/provenance archiving.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func buildIngestTestOMM(t *testing.T, norad uint32, epochUnix int64) []byte {
	t.Helper()
	epoch := time.Unix(epochUnix, 0).UTC().Format("2006-01-02T15:04:05Z")
	data := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(fmt.Sprintf("ING-SAT-%d", norad)).
		WithObjectID(fmt.Sprintf("2026-%03dA", norad%1000)).
		WithEpoch(epoch).
		WithEpochTimestamp(float64(epochUnix)).
		WithMeanMotion(14.9).
		WithEccentricity(0.0002).
		WithInclination(97.4).
		Build()
	return data[4:]
}

// sizePrefixedStream packs unprefixed records into the [u32le len][bytes]...
// wire shape the parser nodes emit.
func sizePrefixedStream(records [][]byte) []byte {
	var out []byte
	var hdr [4]byte
	for _, rec := range records {
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(rec)))
		out = append(out, hdr[:]...)
		out = append(out, rec...)
	}
	return out
}

func decodeCapMeta(t *testing.T, resp []byte) map[string]interface{} {
	t.Helper()
	var meta map[string]interface{}
	if err := json.Unmarshal(resp, &meta); err != nil {
		t.Fatalf("cap response is not JSON: %v (%q)", err, string(resp))
	}
	return meta
}

func capResultField(t *testing.T, meta map[string]interface{}, key string) interface{} {
	t.Helper()
	result, ok := meta["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("cap response has no result object: %v", meta)
	}
	return result[key]
}

func newIngestTestHandler(t *testing.T, opts StorageCapOptions, grants ...string) (modulert.CapHandler, *storage.FlatSQLStore) {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if grants == nil {
		grants = []string{"storage_ingest"}
	}
	bridge := modulert.NewHostBridge(nil, grants)
	handler := NewStorageCapFactoryWithOptions(store, opts)(nil, bridge)
	return handler, store
}

func TestIngestWithSourceStoresTaggedBatch(t *testing.T) {
	rawRoot := filepath.Join(t.TempDir(), "raw")
	handler, store := newIngestTestHandler(t, StorageCapOptions{RawRoot: rawRoot, MinFreeDiskBytes: 1})

	records := [][]byte{
		buildIngestTestOMM(t, 7001, 1700000000),
		buildIngestTestOMM(t, 7002, 1700000000),
		buildIngestTestOMM(t, 7003, 1700000000),
	}
	stream := sizePrefixedStream(records)
	rawPayload := []byte("NORAD_CAT_ID,EPOCH\n7001,2023-11-14T22:13:20Z\n")
	provenance := []byte(`{"source_url":"https://fixture.test/gp","parser_version":"celestrak-gp-wasm/v1","normalized_count":3}`)

	payload := map[string]interface{}{
		"schema":         "OMM.fbs",
		"provider_id":    "space-data-network-02",
		"source_name":    "celestrak-gp",
		"source_url":     "https://fixture.test/gp",
		"batch_id":       "batch-ingest-1",
		"content_key_id": "public",
		"source_peer":    "source:celestrak",
		"records":        base64.StdEncoding.EncodeToString(stream),
		"archive": map[string]interface{}{
			"source": "celestrak",
			"name":   "catalog.csv",
			"raw":    base64.StdEncoding.EncodeToString(rawPayload),
		},
		"provenance": map[string]interface{}{
			"source": "celestrak-gp",
			"json":   base64.StdEncoding.EncodeToString(provenance),
		},
	}
	body, _ := json.Marshal(payload)
	resp, err := handler("storage.ingest_with_source", body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	meta := decodeCapMeta(t, resp)
	if ok, _ := meta["ok"].(bool); !ok {
		t.Fatalf("ingest failed: %v", meta)
	}
	if inserted, _ := capResultField(t, meta, "inserted").(float64); inserted != 3 {
		t.Fatalf("inserted = %v, want 3", capResultField(t, meta, "inserted"))
	}

	// Records are attributed: provider/source/batch tags present.
	tagged, err := store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		BatchID:    "batch-ingest-1",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QuerySourceTaggedRecords: %v", err)
	}
	if len(tagged) != 3 {
		t.Fatalf("tagged records = %d, want 3", len(tagged))
	}

	// Raw archive landed under <raw>/celestrak/<day>/catalog.csv.
	day := time.Now().UTC().Format("2006-01-02")
	archived, err := os.ReadFile(filepath.Join(rawRoot, "celestrak", day, "catalog.csv"))
	if err != nil {
		t.Fatalf("raw archive missing: %v", err)
	}
	if string(archived) != string(rawPayload) {
		t.Fatal("raw archive bytes differ from the submitted payload")
	}

	// Provenance JSON landed under <raw>/provenance/celestrak-gp/.
	provDir := filepath.Join(rawRoot, "provenance", "celestrak-gp")
	entries, err := os.ReadDir(provDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("provenance dir entries=%v err=%v, want exactly 1 file", entries, err)
	}

	// REPLAYING the same batch (same batch_id, same attribution — exactly
	// what a re-fired timer with an unchanged payload produces) must not
	// duplicate records — pre/post reconcile keeps the batch idempotent
	// (runner parity).
	payload2 := map[string]interface{}{
		"schema":         "OMM.fbs",
		"provider_id":    "space-data-network-02",
		"source_name":    "celestrak-gp",
		"source_url":     "https://fixture.test/gp",
		"batch_id":       "batch-ingest-1",
		"content_key_id": "public",
		"source_peer":    "source:celestrak",
		"records":        base64.StdEncoding.EncodeToString(stream),
	}
	body2, _ := json.Marshal(payload2)
	resp2, err := handler("storage.ingest_with_source", body2)
	if err != nil {
		t.Fatalf("replay handler: %v", err)
	}
	meta2 := decodeCapMeta(t, resp2)
	if ok, _ := meta2["ok"].(bool); !ok {
		t.Fatalf("replay ingest failed: %v", meta2)
	}
	replayTagged, err := store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		BatchID:    "batch-ingest-1",
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("replay QuerySourceTaggedRecords: %v", err)
	}
	if len(replayTagged) != 3 {
		t.Fatalf("after replay: tagged records = %d, want 3 (no duplicates)", len(replayTagged))
	}
}

func TestIngestWithSourceCurrentBatchReconcile(t *testing.T) {
	handler, store := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: 1})

	send := func(batchID string, records [][]byte, reconcile string) map[string]interface{} {
		payload := map[string]interface{}{
			"schema":      "CAT.fbs",
			"provider_id": "space-data-network-02",
			"source_name": "celestrak-satcat",
			"batch_id":    batchID,
			"records":     base64.StdEncoding.EncodeToString(sizePrefixedStream(records)),
			"reconcile":   reconcile,
		}
		body, _ := json.Marshal(payload)
		resp, err := handler("storage.ingest_with_source", body)
		if err != nil {
			t.Fatalf("handler(%s): %v", batchID, err)
		}
		meta := decodeCapMeta(t, resp)
		if ok, _ := meta["ok"].(bool); !ok {
			t.Fatalf("ingest %s failed: %v", batchID, meta)
		}
		return meta
	}

	// Per-batch distinct content (a fresh SATCAT snapshot updates fields, so
	// record bytes — and CIDs — differ between batches).
	buildCAT := func(norad uint32, batch string) []byte {
		data := sds.NewCATBuilder().
			WithNoradCatID(norad).
			WithObjectName(fmt.Sprintf("CAT-%d-%s", norad, batch)).
			WithObjectID(fmt.Sprintf("2026-%03dA", norad%1000)).
			Build()
		return data[4:]
	}

	send("satcat-batch-1", [][]byte{buildCAT(8001, "b1"), buildCAT(8002, "b1")}, "current")
	meta := send("satcat-batch-2", [][]byte{buildCAT(8001, "b2"), buildCAT(8002, "b2"), buildCAT(8003, "b2")}, "current")

	if removed, _ := capResultField(t, meta, "reconciled_old_batches").(float64); removed != 2 {
		t.Fatalf("reconciled_old_batches = %v, want 2 (batch-1 rows dropped)", capResultField(t, meta, "reconciled_old_batches"))
	}
	old, err := store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat",
		BatchID:    "satcat-batch-1",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query old batch: %v", err)
	}
	if len(old) != 0 {
		t.Fatalf("old batch rows remain after current reconcile: %d", len(old))
	}
	current, err := store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat",
		BatchID:    "satcat-batch-2",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query current batch: %v", err)
	}
	if len(current) != 3 {
		t.Fatalf("current batch rows = %d, want 3", len(current))
	}
}

func TestIngestWithSourcePolicy(t *testing.T) {
	// Without the storage_ingest capability grant the op is refused even
	// though the storage handler itself is provisioned (e.g. via
	// storage_write).
	handler, _ := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: 1}, "storage_write")
	payload := map[string]interface{}{
		"schema":      "OMM.fbs",
		"provider_id": "p",
		"source_name": "s",
		"batch_id":    "b",
		"records":     base64.StdEncoding.EncodeToString(sizePrefixedStream([][]byte{buildIngestTestOMM(t, 1, 1700000000)})),
	}
	body, _ := json.Marshal(payload)
	resp, err := handler("storage.ingest_with_source", body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	meta := decodeCapMeta(t, resp)
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatal("ingest succeeded without the storage_ingest capability grant")
	}

	// Missing attribution is refused.
	handler2, _ := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: 1})
	payload["provider_id"] = ""
	body2, _ := json.Marshal(payload)
	resp2, _ := handler2("storage.ingest_with_source", body2)
	meta2 := decodeCapMeta(t, resp2)
	if ok, _ := meta2["ok"].(bool); ok {
		t.Fatal("ingest succeeded without provider attribution")
	}

	// Disk guardrail: an absurd floor refuses the batch before any write.
	handler3, store3 := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: int64(1) << 62})
	payload["provider_id"] = "p"
	body3, _ := json.Marshal(payload)
	resp3, _ := handler3("storage.ingest_with_source", body3)
	meta3 := decodeCapMeta(t, resp3)
	if ok, _ := meta3["ok"].(bool); ok {
		t.Fatal("ingest succeeded past the disk guardrail")
	}
	if count, err := store3.Count("OMM.fbs"); err != nil || count != 0 {
		t.Fatalf("guardrail-refused ingest wrote records: count=%d err=%v", count, err)
	}
}
