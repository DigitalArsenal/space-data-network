package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestImportLegacySQLiteStoresHistoricalOMMWithSourceProvenance(t *testing.T) {
	tmpDir := t.TempDir()
	sourcePath := filepath.Join(tmpDir, "satellite_data.db")
	sourceDB, err := sql.Open("sqlite3", sourcePath)
	if err != nil {
		t.Fatalf("open source sqlite: %v", err)
	}
	if _, err := sourceDB.Exec(`
		CREATE TABLE satellite_data (
			OBJECT_ID TEXT,
			EPOCH TEXT,
			MEAN_MOTION REAL,
			ECCENTRICITY REAL,
			INCLINATION REAL,
			RA_OF_ASC_NODE REAL,
			ARG_OF_PERICENTER REAL,
			MEAN_ANOMALY REAL,
			NORAD_CAT_ID INTEGER,
			BSTAR REAL,
			MEAN_MOTION_DOT REAL,
			MEAN_MOTION_DDOT REAL
		);
		INSERT INTO satellite_data (
			OBJECT_ID, EPOCH, MEAN_MOTION, ECCENTRICITY, INCLINATION,
			RA_OF_ASC_NODE, ARG_OF_PERICENTER, MEAN_ANOMALY, NORAD_CAT_ID, BSTAR
		) VALUES
			('1957-001A', '1959-01-11T01:49:23.461536', 10.1, 0.001, 55.0, 20.0, 30.0, 40.0, 1, 0.00001),
			('1998-067A', '2024-01-16T11:51:22.650624', 15.5, 0.0002, 51.6, 10.0, 20.0, 30.0, 25544, 0.00002);
	`); err != nil {
		_ = sourceDB.Close()
		t.Fatalf("seed source sqlite: %v", err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatalf("close source sqlite: %v", err)
	}

	storagePath := filepath.Join(tmpDir, "sdn")
	restore := captureImportLegacyGlobals()
	t.Cleanup(restore)
	configPath = filepath.Join(tmpDir, "missing-config.yaml")
	importLegacySourceDB = sourcePath
	importLegacySourceTable = "satellite_data"
	importLegacyStoragePath = storagePath
	importLegacySourcePeer = "source:legacy-sqlite"
	importLegacyBatchSize = 10
	importLegacyCheckpointPath = filepath.Join(storagePath, "checkpoint.json")
	importLegacyResetCheckpoint = true
	importLegacyMaxRows = 0
	importLegacyStoreMPE = false

	if err := runImportLegacySQLite(nil, nil); err != nil {
		t.Fatalf("runImportLegacySQLite failed: %v", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(storagePath, validator)
	if err != nil {
		t.Fatalf("open imported store: %v", err)
	}
	defer store.Close()

	total, err := store.CountRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords total failed: %v", err)
	}
	if total != 2 {
		t.Fatalf("total imported OMM rows = %d, want 2", total)
	}
	sourceTotal, err := store.CountRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp-historical",
	})
	if err != nil {
		t.Fatalf("CountRawRecords source failed: %v", err)
	}
	if sourceTotal != 2 {
		t.Fatalf("source-tagged historical OMM rows = %d, want 2", sourceTotal)
	}
	summary, err := store.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary failed: %v", err)
	}
	if len(summary.Sources) != 1 {
		t.Fatalf("source summaries = %d, want 1: %#v", len(summary.Sources), summary.Sources)
	}
	got := summary.Sources[0]
	if got.ProviderID != "space-data-network-02" || got.SourceName != "celestrak-gp-historical" || got.BatchID == "" || got.ProducerPeerID != "source:legacy-sqlite" || got.Count != 2 {
		t.Fatalf("unexpected source summary: %#v", got)
	}

	checkpointBytes, err := os.ReadFile(importLegacyCheckpointPath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if len(checkpointBytes) == 0 {
		t.Fatal("checkpoint file is empty")
	}
}

func TestImportLegacySQLiteCanStoreHistoricalOMMInSourceDatastoreNamespace(t *testing.T) {
	tmpDir := t.TempDir()
	sourcePath := filepath.Join(tmpDir, "satellite_data.db")
	sourceDB, err := sql.Open("sqlite3", sourcePath)
	if err != nil {
		t.Fatalf("open source sqlite: %v", err)
	}
	if _, err := sourceDB.Exec(`
		CREATE TABLE satellite_data (
			OBJECT_ID TEXT,
			EPOCH TEXT,
			MEAN_MOTION REAL,
			ECCENTRICITY REAL,
			INCLINATION REAL,
			RA_OF_ASC_NODE REAL,
			ARG_OF_PERICENTER REAL,
			MEAN_ANOMALY REAL,
			NORAD_CAT_ID INTEGER,
			BSTAR REAL
		);
		INSERT INTO satellite_data (
			OBJECT_ID, EPOCH, MEAN_MOTION, ECCENTRICITY, INCLINATION,
			RA_OF_ASC_NODE, ARG_OF_PERICENTER, MEAN_ANOMALY, NORAD_CAT_ID, BSTAR
		) VALUES
			('1957-001A', '1959-01-11T01:49:23.461536', 10.1, 0.001, 55.0, 20.0, 30.0, 40.0, 1, 0.00001),
			('1998-067A', '2024-01-16T11:51:22.650624', 15.5, 0.0002, 51.6, 10.0, 20.0, 30.0, 25544, 0.00002);
	`); err != nil {
		_ = sourceDB.Close()
		t.Fatalf("seed source sqlite: %v", err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatalf("close source sqlite: %v", err)
	}

	storagePath := filepath.Join(tmpDir, "sdn")
	restore := captureImportLegacyGlobals()
	t.Cleanup(restore)
	configPath = filepath.Join(tmpDir, "missing-config.yaml")
	importLegacySourceDB = sourcePath
	importLegacySourceTable = "satellite_data"
	importLegacyStoragePath = storagePath
	importLegacySourcePeer = "source:legacy-sqlite"
	importLegacyBatchSize = 10
	importLegacyCheckpointPath = ""
	importLegacyResetCheckpoint = true
	importLegacyMaxRows = 0
	importLegacyStoreMPE = false
	importLegacyDatastoreNamespace = true

	if err := runImportLegacySQLite(nil, nil); err != nil {
		t.Fatalf("runImportLegacySQLite failed: %v", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	rootStore, err := storage.NewFlatSQLStore(storagePath, validator)
	if err != nil {
		t.Fatalf("open root store: %v", err)
	}
	rootCount, err := rootStore.CountRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("root CountRawRecords failed: %v", err)
	}
	if rootCount != 0 {
		t.Fatalf("root OMM rows = %d, want 0", rootCount)
	}
	if err := rootStore.Close(); err != nil {
		t.Fatalf("close root store failed: %v", err)
	}

	batchID, err := legacyImportBatchID(sourcePath)
	if err != nil {
		t.Fatalf("legacyImportBatchID failed: %v", err)
	}
	identity := storage.DatastoreIdentity{
		SchemaName:    "OMM.fbs",
		SourcePeerID:  "source:legacy-sqlite",
		ProviderID:    "space-data-network-02",
		SourceName:    "celestrak-gp-historical",
		BatchHead:     batchID,
		QueryProfile:  storage.DatasetPublicationQueryProfile,
		SnapshotID:    batchID,
		HighWaterMark: batchID,
		ArtifactHash:  batchID,
	}
	namespaceStore, err := storage.NewFlatSQLStoreForIdentity(storagePath, validator, identity)
	if err != nil {
		t.Fatalf("open namespace store: %v", err)
	}
	namespaceCount, err := namespaceStore.CountRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("namespace CountRawRecords failed: %v", err)
	}
	if namespaceCount != 2 {
		t.Fatalf("namespace OMM rows = %d, want 2", namespaceCount)
	}
	summary, err := namespaceStore.DataSummary()
	if err != nil {
		t.Fatalf("namespace DataSummary failed: %v", err)
	}
	if len(summary.Sources) != 0 {
		t.Fatalf("namespace source tag rows = %d, want 0: %#v", len(summary.Sources), summary.Sources)
	}
	storedIdentity, ok, err := namespaceStore.DatastoreIdentity()
	if err != nil {
		t.Fatalf("DatastoreIdentity failed: %v", err)
	}
	if !ok {
		t.Fatal("namespace store did not record datastore identity")
	}
	if storedIdentity.ProviderID != identity.ProviderID || storedIdentity.SourceName != identity.SourceName || storedIdentity.BatchHead != identity.BatchHead {
		t.Fatalf("stored identity = %#v, want provider/source/batch from %#v", storedIdentity, identity)
	}
	if err := namespaceStore.Close(); err != nil {
		t.Fatalf("close namespace store failed: %v", err)
	}
}

func captureImportLegacyGlobals() func() {
	oldConfigPath := configPath
	oldSourceDB := importLegacySourceDB
	oldSourceTable := importLegacySourceTable
	oldStoragePath := importLegacyStoragePath
	oldSourcePeer := importLegacySourcePeer
	oldBatchSize := importLegacyBatchSize
	oldCheckpointPath := importLegacyCheckpointPath
	oldResetCheckpoint := importLegacyResetCheckpoint
	oldMaxRows := importLegacyMaxRows
	oldStoreMPE := importLegacyStoreMPE
	oldProviderID := importLegacyProviderID
	oldSourceNameForTags := importLegacySourceNameForTags
	oldSourceURL := importLegacySourceURL
	oldBatchID := importLegacyBatchID
	oldContentKeyID := importLegacyContentKeyID
	oldProducerPeerID := importLegacyProducerPeerID
	oldProducerPublicKey := importLegacyProducerPublicKey
	oldDatastoreNamespace := importLegacyDatastoreNamespace
	return func() {
		configPath = oldConfigPath
		importLegacySourceDB = oldSourceDB
		importLegacySourceTable = oldSourceTable
		importLegacyStoragePath = oldStoragePath
		importLegacySourcePeer = oldSourcePeer
		importLegacyBatchSize = oldBatchSize
		importLegacyCheckpointPath = oldCheckpointPath
		importLegacyResetCheckpoint = oldResetCheckpoint
		importLegacyMaxRows = oldMaxRows
		importLegacyStoreMPE = oldStoreMPE
		importLegacyProviderID = oldProviderID
		importLegacySourceNameForTags = oldSourceNameForTags
		importLegacySourceURL = oldSourceURL
		importLegacyBatchID = oldBatchID
		importLegacyContentKeyID = oldContentKeyID
		importLegacyProducerPeerID = oldProducerPeerID
		importLegacyProducerPublicKey = oldProducerPublicKey
		importLegacyDatastoreNamespace = oldDatastoreNamespace
	}
}
