package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ipfs/go-cid"
	_ "github.com/mattn/go-sqlite3"
	mh "github.com/multiformats/go-multihash"

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

func TestImportLegacySQLiteCanPublishHistoricalOMMArtifactsWithoutMaterializingRows(t *testing.T) {
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
			('1998-067A', '2024-01-16T11:51:22.650624', 15.5, 0.0002, 51.6, 10.0, 20.0, 30.0, 25544, 0.00002),
			('2024-001A', '2024-01-17T11:51:22.650624', 15.6, 0.0003, 52.1, 11.0, 21.0, 31.0, 60000, 0.00003);
	`); err != nil {
		_ = sourceDB.Close()
		t.Fatalf("seed source sqlite: %v", err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatalf("close source sqlite: %v", err)
	}

	pinned := map[string][]byte{}
	kubo := newImportLegacyKuboTestServer(t, pinned)
	defer kubo.Close()
	_, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	storagePath := filepath.Join(tmpDir, "sdn")
	restore := captureImportLegacyGlobals()
	t.Cleanup(restore)
	configPath = filepath.Join(tmpDir, "missing-config.yaml")
	importLegacySourceDB = sourcePath
	importLegacySourceTable = "satellite_data"
	importLegacyStoragePath = storagePath
	importLegacySourcePeer = "source:legacy-sqlite"
	importLegacyBatchSize = 2
	importLegacyCheckpointPath = ""
	importLegacyResetCheckpoint = true
	importLegacyMaxRows = 0
	importLegacyStoreMPE = false
	importLegacyPublishArtifactsOnly = true
	importLegacyIPFSAPIURL = kubo.URL
	importLegacyPublicationOutputDir = filepath.Join(tmpDir, "publications")
	importLegacyPublicationSigningKeyHex = hex.EncodeToString(signingKey)
	importLegacyPublicationProviderPeerID = "16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4"
	importLegacyPublicationProviderEPMCID = "bafy-provider-epm"

	if err := runImportLegacySQLite(nil, nil); err != nil {
		t.Fatalf("runImportLegacySQLite failed: %v", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(storagePath, validator)
	if err != nil {
		t.Fatalf("open root store: %v", err)
	}
	defer store.Close()

	rootCount, err := store.CountRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("root CountRawRecords failed: %v", err)
	}
	if rootCount != 0 {
		t.Fatalf("root OMM rows = %d, want 0 for artifact-only publication", rootCount)
	}
	entries, err := store.ListDatastoreIdentities()
	if err != nil {
		t.Fatalf("ListDatastoreIdentities failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("datastore registry entries = %d, want 1: %#v", len(entries), entries)
	}
	namespaceStore, err := store.OpenRegisteredDatastore(entries[0].Key)
	if err != nil {
		t.Fatalf("OpenRegisteredDatastore failed: %v", err)
	}
	defer namespaceStore.Close()
	namespaceCount, err := namespaceStore.CountRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("namespace CountRawRecords failed: %v", err)
	}
	if namespaceCount != 0 {
		t.Fatalf("namespace OMM rows = %d, want 0 for artifact-only publication", namespaceCount)
	}
	pnmCount, err := namespaceStore.CountRawRecords(storage.RawRecordQuery{SchemaName: "PNM.fbs"})
	if err != nil {
		t.Fatalf("PNM CountRawRecords failed: %v", err)
	}
	if pnmCount != 2 {
		t.Fatalf("publication PNMs = %d, want 2", pnmCount)
	}
	batchID, err := legacyImportBatchID(sourcePath)
	if err != nil {
		t.Fatalf("legacyImportBatchID failed: %v", err)
	}
	publications, err := namespaceStore.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp-historical",
		BatchID:      batchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("ListDatasetShardPublications failed: %v", err)
	}
	if len(publications) != 2 {
		t.Fatalf("publications = %d, want 2: %#v", len(publications), publications)
	}
	if publications[0].Offset != 0 || publications[0].Limit != 2 || publications[0].RecordCount != 2 {
		t.Fatalf("first publication = %#v, want offset 0 limit 2 count 2", publications[0])
	}
	if publications[1].Offset != 2 || publications[1].Limit != 2 || publications[1].RecordCount != 1 {
		t.Fatalf("second publication = %#v, want offset 2 limit 2 count 1", publications[1])
	}
	if publications[0].FeedHead == "" || publications[1].PreviousHead != publications[0].FeedHead {
		t.Fatalf("publication feed chain not linked: first=%#v second=%#v", publications[0], publications[1])
	}
	if len(pinned) != 6 {
		t.Fatalf("pinned IPFS objects = %d, want shard/index/manifest for each publication", len(pinned))
	}
}

func TestImportLegacySQLitePlanOnlyCanRegisterHistoricalArtifactsWithNodeSigningKey(t *testing.T) {
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
			('1998-067A', '2024-01-16T11:51:22.650624', 15.5, 0.0002, 51.6, 10.0, 20.0, 30.0, 25544, 0.00002),
			('2024-001A', '2024-01-17T11:51:22.650624', 15.6, 0.0003, 52.1, 11.0, 21.0, 31.0, 60000, 0.00003);
	`); err != nil {
		_ = sourceDB.Close()
		t.Fatalf("seed source sqlite: %v", err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatalf("close source sqlite: %v", err)
	}

	pinned := map[string][]byte{}
	kubo := newImportLegacyKuboTestServer(t, pinned)
	defer kubo.Close()

	stagingStoragePath := filepath.Join(tmpDir, "staging-sdn")
	planPath := filepath.Join(tmpDir, "historical-plan.json")
	restore := captureImportLegacyGlobals()
	t.Cleanup(restore)
	configPath = filepath.Join(tmpDir, "missing-config.yaml")
	importLegacySourceDB = sourcePath
	importLegacySourceTable = "satellite_data"
	importLegacyStoragePath = stagingStoragePath
	importLegacySourcePeer = "source:legacy-sqlite"
	importLegacyBatchSize = 2
	importLegacyCheckpointPath = ""
	importLegacyResetCheckpoint = true
	importLegacyMaxRows = 0
	importLegacyStoreMPE = false
	importLegacyPublishArtifactsOnly = true
	importLegacyIPFSAPIURL = kubo.URL
	importLegacyPublicationOutputDir = filepath.Join(tmpDir, "publications")
	importLegacyPublicationPlanOnly = true
	importLegacyPublicationPlanOutput = planPath
	importLegacyPublicationProviderPeerID = "16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4"
	importLegacyPublicationProviderEPMCID = "bafy-provider-epm"
	importLegacyPublicationDatasetID = "sdn-omm-celestrak-gp-historical"

	if err := runImportLegacySQLite(nil, nil); err != nil {
		t.Fatalf("runImportLegacySQLite plan-only failed: %v", err)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("plan output was not written: %v", err)
	}

	registeredStoragePath := filepath.Join(tmpDir, "registered-sdn")
	registerConfigPath := filepath.Join(tmpDir, "register-config.yaml")
	registerDataPath := filepath.Join(tmpDir, "register-data")
	if err := os.WriteFile(registerConfigPath, []byte(fmt.Sprintf(`
storage:
  path: %s
setup:
  data_path: %s
admin:
  ipfs_api_url: %s
`, registeredStoragePath, registerDataPath, kubo.URL)), 0600); err != nil {
		t.Fatalf("write register config: %v", err)
	}
	configPath = registerConfigPath
	result, err := registerLegacyPublicationPlan(context.Background(), legacyPublicationPlanRegistrationOptions{
		PlanPath: planPath,
	})
	if err != nil {
		t.Fatalf("registerLegacyPublicationPlan failed: %v", err)
	}
	if result.Publications != 2 || result.Records != 3 {
		t.Fatalf("registered result = %+v, want 2 publications and 3 records", result)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	rootStore, err := storage.NewFlatSQLStore(registeredStoragePath, validator)
	if err != nil {
		t.Fatalf("open registered root store: %v", err)
	}
	defer rootStore.Close()
	rootCount, err := rootStore.CountRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("root CountRawRecords failed: %v", err)
	}
	if rootCount != 0 {
		t.Fatalf("root OMM rows = %d, want 0 after registering artifact plan", rootCount)
	}
	entries, err := rootStore.ListDatastoreIdentities()
	if err != nil {
		t.Fatalf("ListDatastoreIdentities failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("datastore registry entries = %d, want 1: %#v", len(entries), entries)
	}
	namespaceStore, err := rootStore.OpenRegisteredDatastore(entries[0].Key)
	if err != nil {
		t.Fatalf("OpenRegisteredDatastore failed: %v", err)
	}
	defer namespaceStore.Close()
	namespaceCount, err := namespaceStore.CountRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("namespace CountRawRecords failed: %v", err)
	}
	if namespaceCount != 0 {
		t.Fatalf("namespace OMM rows = %d, want 0 after registering artifact plan", namespaceCount)
	}
	pnmCount, err := namespaceStore.CountRawRecords(storage.RawRecordQuery{SchemaName: "PNM.fbs"})
	if err != nil {
		t.Fatalf("PNM CountRawRecords failed: %v", err)
	}
	if pnmCount != 2 {
		t.Fatalf("publication PNMs = %d, want 2", pnmCount)
	}
	publications, err := namespaceStore.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp-historical",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("ListDatasetShardPublications failed: %v", err)
	}
	if len(publications) != 2 || publications[0].RecordCount != 2 || publications[1].RecordCount != 1 {
		t.Fatalf("registered publications = %#v, want two planned artifact windows", publications)
	}
	if len(pinned) != 6 {
		t.Fatalf("pinned IPFS objects = %d, want shard/index locally plus signed manifests on registration", len(pinned))
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
	oldPublishArtifactsOnly := importLegacyPublishArtifactsOnly
	oldIPFSAPIURL := importLegacyIPFSAPIURL
	oldPublicationOutputDir := importLegacyPublicationOutputDir
	oldPublicationSigningKeyHex := importLegacyPublicationSigningKeyHex
	oldPublicationProviderPeerID := importLegacyPublicationProviderPeerID
	oldPublicationProviderEPMCID := importLegacyPublicationProviderEPMCID
	oldPublicationDatasetID := importLegacyPublicationDatasetID
	oldPublicationPlanOnly := importLegacyPublicationPlanOnly
	oldPublicationPlanOutput := importLegacyPublicationPlanOutput
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
		importLegacyPublishArtifactsOnly = oldPublishArtifactsOnly
		importLegacyIPFSAPIURL = oldIPFSAPIURL
		importLegacyPublicationOutputDir = oldPublicationOutputDir
		importLegacyPublicationSigningKeyHex = oldPublicationSigningKeyHex
		importLegacyPublicationProviderPeerID = oldPublicationProviderPeerID
		importLegacyPublicationProviderEPMCID = oldPublicationProviderEPMCID
		importLegacyPublicationDatasetID = oldPublicationDatasetID
		importLegacyPublicationPlanOnly = oldPublicationPlanOnly
		importLegacyPublicationPlanOutput = oldPublicationPlanOutput
	}
}

func newImportLegacyKuboTestServer(t *testing.T, pinned map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var responseKey string
		var wantField string
		switch r.URL.Path {
		case "/api/v0/add":
			responseKey = "Hash"
			wantField = "file"
		case "/api/v0/block/put":
			responseKey = "Key"
			wantField = "data"
		default:
			t.Fatalf("unexpected IPFS path: %s", r.URL.Path)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("multipart reader: %v", err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		if part.FormName() != wantField {
			t.Fatalf("multipart field = %q, want %s", part.FormName(), wantField)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		sum := sha256.Sum256(body)
		cidValue := fmt.Sprintf("bafyimportlegacy%x", sum[:8])
		if r.URL.Path == "/api/v0/block/put" {
			hash, err := mh.Sum(body, mh.SHA2_256, -1)
			if err != nil {
				t.Fatalf("multihash raw block: %v", err)
			}
			cidValue = cid.NewCidV1(cid.Raw, hash).String()
		}
		pinned[cidValue] = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"` + responseKey + `":"` + cidValue + `"}` + "\n"))
	}))
}
