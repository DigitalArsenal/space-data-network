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
