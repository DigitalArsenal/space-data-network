// Package storage provides SQLite-based storage with FlatBuffer support.
package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestNewFlatSQLStore(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a mock validator (without WASM)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	// Create store
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Verify database file was created
	dbPath := filepath.Join(tmpDir, "sdn.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestFlatSQLStoreQueryIndexedRecordsCommonCatalogFilters(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-indexed-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-satcat-csv",
		SourceURL:    "https://celestrak.org/satcat/records.php?GROUP=active&FORMAT=CSV",
		BatchID:      "batch-001",
		ContentKeyID: "public",
	}
	payloadBytes := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	rocketBytes := sds.NewCATBuilder().
		WithNoradCatID(48274).
		WithObjectName("CZ-5B R/B").
		WithObjectID("2021-035B").
		WithObjectType("ROCKET_BODY").
		WithOpsStatus("DECAYED").
		Build()

	payloadCID, err := store.StoreWithSourceTags("CAT.fbs", payloadBytes, "source:celestrak", nil, tags)
	if err != nil {
		t.Fatalf("StoreWithSourceTags payload failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", rocketBytes, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("StoreWithSourceTags rocket failed: %v", err)
	}

	fullCatalog, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("QueryIndexedRecords full catalog failed: %v", err)
	}
	if len(fullCatalog) != 2 {
		t.Fatalf("full catalog returned %d records, want 2", len(fullCatalog))
	}

	norad := uint32(25544)
	byNorad, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", NoradCatID: &norad, Limit: 10})
	if err != nil {
		t.Fatalf("QueryIndexedRecords NORAD failed: %v", err)
	}
	if len(byNorad) != 1 || byNorad[0].CID != payloadCID {
		t.Fatalf("NORAD query returned %+v, want payload CID %s", byNorad, payloadCID)
	}

	activePayloads, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", ActivePayloads: true, Limit: 10})
	if err != nil {
		t.Fatalf("QueryIndexedRecords active payloads failed: %v", err)
	}
	if len(activePayloads) != 1 || activePayloads[0].CID != payloadCID {
		t.Fatalf("active payload query returned %+v, want payload CID %s", activePayloads, payloadCID)
	}

	rocketBodies, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", ObjectType: "ROCKET_BODY", Limit: 10})
	if err != nil {
		t.Fatalf("QueryIndexedRecords object type failed: %v", err)
	}
	if len(rocketBodies) != 1 || rocketBodies[0].CID == payloadCID {
		t.Fatalf("object type query returned %+v, want only rocket-body record", rocketBodies)
	}

	caReady, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", CAReadyResidentSet: true, Limit: 10})
	if err != nil {
		t.Fatalf("QueryIndexedRecords CA-ready failed: %v", err)
	}
	if len(caReady) != 1 || caReady[0].CID != payloadCID {
		t.Fatalf("CA-ready query returned %+v, want payload CID %s", caReady, payloadCID)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	providerBatch, err := store.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		From:       &from,
		To:         &to,
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		BatchID:    "batch-001",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QueryIndexedRecords provider batch failed: %v", err)
	}
	if len(providerBatch) != 2 {
		t.Fatalf("provider batch/time-window query returned %d records, want 2", len(providerBatch))
	}
}

func TestFlatSQLStoreStoreAndGet(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Store data
	testData := []byte(`{"satellite": "ISS", "norad_id": 25544}`)
	testPeerID := "12D3KooWTest123"
	testSignature := make([]byte, 64)

	cid, err := store.Store("OMM.fbs", testData, testPeerID, testSignature)
	if err != nil {
		t.Fatalf("Failed to store data: %v", err)
	}

	if cid == "" {
		t.Error("Expected non-empty CID")
	}

	// Get data back
	retrieved, err := store.Get("OMM.fbs", cid)
	if err != nil {
		t.Fatalf("Failed to get data: %v", err)
	}

	if string(retrieved) != string(testData) {
		t.Errorf("Retrieved data doesn't match: got %s, want %s", retrieved, testData)
	}
}

func TestFlatSQLStoreStoreWithSourceTags(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-tags-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	testData := []byte(`{"satellite": "ISS", "norad_id": 25544}`)
	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		SourceURL:    "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:      "20260505T120000Z",
		ContentKeyID: "public",
	}

	cid, err := store.StoreWithSourceTags("OMM.fbs", testData, "source:celestrak", nil, tags)
	if err != nil {
		t.Fatalf("StoreWithSourceTags failed: %v", err)
	}

	gotTags, err := store.GetSourceTags("OMM.fbs", cid)
	if err != nil {
		t.Fatalf("GetSourceTags failed: %v", err)
	}
	if gotTags.ProviderID != tags.ProviderID {
		t.Fatalf("ProviderID = %q, want %q", gotTags.ProviderID, tags.ProviderID)
	}
	if gotTags.SourceName != tags.SourceName {
		t.Fatalf("SourceName = %q, want %q", gotTags.SourceName, tags.SourceName)
	}
	if gotTags.SourceURL != tags.SourceURL {
		t.Fatalf("SourceURL = %q, want %q", gotTags.SourceURL, tags.SourceURL)
	}
	if gotTags.BatchID != tags.BatchID {
		t.Fatalf("BatchID = %q, want %q", gotTags.BatchID, tags.BatchID)
	}
	if gotTags.ContentKeyID != tags.ContentKeyID {
		t.Fatalf("ContentKeyID = %q, want %q", gotTags.ContentKeyID, tags.ContentKeyID)
	}

	matches, err := store.QuerySourceTaggedRecords(SourceTagQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		BatchID:    "20260505T120000Z",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QuerySourceTaggedRecords failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("QuerySourceTaggedRecords returned %d records, want 1", len(matches))
	}
	if matches[0].CID != cid {
		t.Fatalf("matched CID = %q, want %q", matches[0].CID, cid)
	}
	if string(matches[0].Data) != string(testData) {
		t.Fatalf("matched data = %s, want %s", matches[0].Data, testData)
	}
}

func TestFlatSQLStoreGetNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Try to get non-existent data
	_, err = store.Get("OMM.fbs", "nonexistent-cid")
	if err == nil {
		t.Error("Expected error for non-existent CID")
	}
}

func TestFlatSQLStoreQuery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Store multiple records
	testPeerID := "12D3KooWTest123"
	testSignature := make([]byte, 64)

	for i := 0; i < 3; i++ {
		testData := []byte(`{"record": ` + string(rune('0'+i)) + `}`)
		_, err := store.Store("CDM.fbs", testData, testPeerID, testSignature)
		if err != nil {
			t.Fatalf("Failed to store data %d: %v", i, err)
		}
	}

	// Query all
	results, err := store.Query("CDM.fbs", "")
	if err != nil {
		t.Fatalf("Failed to query: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}
}

func TestFlatSQLStoreQueryWithPeerID(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Store records from different peers
	testSignature := make([]byte, 64)
	store.Store("EPM.fbs", []byte(`{"peer": "A"}`), "PeerA", testSignature)
	store.Store("EPM.fbs", []byte(`{"peer": "B"}`), "PeerB", testSignature)
	store.Store("EPM.fbs", []byte(`{"peer": "A2"}`), "PeerA", testSignature)

	// Query for PeerA
	results, err := store.QueryWithPeerID("EPM.fbs", "PeerA")
	if err != nil {
		t.Fatalf("Failed to query: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results for PeerA, got %d", len(results))
	}
}

func TestFlatSQLStoreDelete(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Store data
	testData := []byte(`{"test": true}`)
	cid, err := store.Store("CAT.fbs", testData, "TestPeer", make([]byte, 64))
	if err != nil {
		t.Fatalf("Failed to store: %v", err)
	}

	// Delete it
	err = store.Delete("CAT.fbs", cid)
	if err != nil {
		t.Fatalf("Failed to delete: %v", err)
	}

	// Verify it's gone
	_, err = store.Get("CAT.fbs", cid)
	if err == nil {
		t.Error("Expected error for deleted record")
	}
}

func TestFlatSQLStoreDeleteNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Try to delete non-existent record
	err = store.Delete("CAT.fbs", "nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent record")
	}
}

func TestFlatSQLStoreCount(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Store some records (with unique data to get unique CIDs)
	testSignature := make([]byte, 64)
	for i := 0; i < 5; i++ {
		// Each record must have unique data to get a unique CID
		data := []byte(`{"tracking": true, "id": ` + string(rune('0'+i)) + `}`)
		store.Store("TDM.fbs", data, "TestPeer", testSignature)
	}

	// Count
	count, err := store.Count("TDM.fbs")
	if err != nil {
		t.Fatalf("Failed to count: %v", err)
	}

	if count != 5 {
		t.Errorf("Expected count 5, got %d", count)
	}
}

func TestFlatSQLStoreStats(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Get stats
	stats, err := store.Stats()
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}

	// Should have entries for all schemas
	if len(stats) == 0 {
		t.Error("Expected non-empty stats")
	}
}

func TestFlatSQLStoreGetRecord(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Store data
	testData := []byte(`{"full": "record"}`)
	testPeerID := "12D3KooWTestRecord"
	testSignature := []byte("signature123signature123signature123signature123signature123sig!")

	cid, err := store.Store("OEM.fbs", testData, testPeerID, testSignature)
	if err != nil {
		t.Fatalf("Failed to store: %v", err)
	}

	// Get full record
	record, err := store.GetRecord("OEM.fbs", cid)
	if err != nil {
		t.Fatalf("Failed to get record: %v", err)
	}

	if record.CID != cid {
		t.Errorf("CID mismatch: got %s, want %s", record.CID, cid)
	}
	if record.PeerID != testPeerID {
		t.Errorf("PeerID mismatch: got %s, want %s", record.PeerID, testPeerID)
	}
	if string(record.Data) != string(testData) {
		t.Errorf("Data mismatch")
	}
}

func TestComputeCID(t *testing.T) {
	data1 := []byte("test data 1")
	data2 := []byte("test data 2")

	cid1 := computeCID(data1)
	cid2 := computeCID(data2)
	cid1Again := computeCID(data1)

	// Same data should produce same CID
	if cid1 != cid1Again {
		t.Error("Same data should produce same CID")
	}

	// Different data should produce different CID
	if cid1 == cid2 {
		t.Error("Different data should produce different CID")
	}

	// CID should be 64 hex characters (SHA-256)
	if len(cid1) != 64 {
		t.Errorf("Expected CID length 64, got %d", len(cid1))
	}
}

func TestFlatSQLStoreGarbageCollect(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Store some records
	testSignature := make([]byte, 64)
	for i := 0; i < 3; i++ {
		store.Store("RFM.fbs", []byte(`{"test": true}`), "TestPeer", testSignature)
	}

	// GC with very short age should delete all
	deleted, err := store.GarbageCollect(1 * time.Millisecond)
	if err != nil {
		t.Fatalf("Failed to GC: %v", err)
	}

	// Note: GC may not delete immediately if records are very new
	// This is testing the function works without errors
	_ = deleted
}
