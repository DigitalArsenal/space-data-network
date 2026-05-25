// Package storage provides SQLite-based storage with FlatBuffer support.
package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"

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

func TestNewFlatSQLStoreCreatesCanonicalSchemaTablesWithoutSQLiteBlobs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-no-blob-table-test-*")
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

	assertNoSQLiteBlobColumns(t, store, "OMM")
	assertHasColumns(t, store, "OMM", "cid", "peer_id", "timestamp", "stream_path", "stream_offset", "record_length", "signature_hex")
}

func TestNewFlatSQLStoreMigratesExistingCanonicalBlobTableToStreamMetadata(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-existing-table-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := sql.Open("sqlite3", filepath.Join(tmpDir, "sdn.db"))
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	payload := []byte("canonical-omm-payload")
	if _, err := db.Exec(`
		CREATE TABLE OMM (
			cid TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			data BLOB NOT NULL,
			signature BLOB,
			created_at INTEGER DEFAULT (strftime('%s', 'now')),
			UNIQUE(cid)
		)
	`); err != nil {
		t.Fatalf("create existing schema table failed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO OMM (cid, peer_id, timestamp, data, signature)
		VALUES ('canonical-cid', 'source:celestrak', 1700000000, ?, x'010203')
	`, payload); err != nil {
		t.Fatalf("insert existing canonical record failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite db failed: %v", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	assertNoSQLiteBlobColumns(t, store, "OMM")
	record, err := store.Get("OMM.fbs", "canonical-cid")
	if err != nil {
		t.Fatalf("migrated canonical record lookup failed: %v", err)
	}
	if string(record) != string(payload) {
		t.Fatalf("migrated canonical payload = %q, want %q", string(record), string(payload))
	}
}

func TestNewFlatSQLStoreMigratesLegacySDSTableToCanonicalSchemaTable(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-legacy-table-migration-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := sql.Open("sqlite3", filepath.Join(tmpDir, "sdn.db"))
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	legacyPayload := []byte("legacy-omm-payload")
	if _, err := db.Exec(`
		CREATE TABLE sds_omm (
			cid TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			data BLOB NOT NULL,
			signature BLOB,
			created_at INTEGER DEFAULT (strftime('%s', 'now')),
			UNIQUE(cid)
		)
	`); err != nil {
		t.Fatalf("create legacy schema table failed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO sds_omm (cid, peer_id, timestamp, data, signature)
		VALUES ('legacy-cid', 'source:celestrak', 1700000000, ?, NULL)
	`, legacyPayload); err != nil {
		t.Fatalf("insert legacy record failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite db failed: %v", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	if exists, err := store.tableExists("OMM"); err != nil {
		t.Fatalf("canonical table lookup failed: %v", err)
	} else if !exists {
		t.Fatal("canonical OMM table was not created from legacy sds_omm")
	}
	if exists, err := store.tableExists("sds_omm"); err != nil {
		t.Fatalf("legacy table lookup failed: %v", err)
	} else if exists {
		t.Fatal("legacy sds_omm table still exists after migration")
	}
	assertNoSQLiteBlobColumns(t, store, "OMM")
	assertHasColumns(t, store, "OMM", "stream_path", "stream_offset", "record_length", "signature_hex")
	record, err := store.Get("OMM.fbs", "legacy-cid")
	if err != nil {
		t.Fatalf("migrated record lookup failed: %v", err)
	}
	if string(record) != string(legacyPayload) {
		t.Fatalf("migrated payload = %q, want %q", string(record), string(legacyPayload))
	}
}

func TestNewFlatSQLStoreDefersInterruptedLegacyMigrationWhenCanonicalHasRows(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-interrupted-legacy-migration-test-*")
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

	if _, err := store.db.Exec(`
		CREATE TABLE sds_omm (
			cid TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			data BLOB NOT NULL,
			signature BLOB,
			UNIQUE(cid)
		)
	`); err != nil {
		t.Fatalf("create legacy schema table failed: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO sds_omm (cid, peer_id, timestamp, data, signature)
		VALUES ('legacy-cid', 'source:celestrak', 1700000001, ?, NULL)
	`, []byte("legacy-omm-payload")); err != nil {
		t.Fatalf("insert legacy record failed: %v", err)
	}
	existingPayload := []byte("already-migrated-omm-payload")
	streamPath, streamOffset, recordLength, err := store.appendFlatSQLStreamRecord("OMM.fbs", existingPayload)
	if err != nil {
		t.Fatalf("append existing stream record failed: %v", err)
	}
	if err := insertSchemaMetadata(store.db, "OMM", "existing-cid", "source:celestrak", 1700000000, streamPath, streamOffset, recordLength, nil, 1700000000); err != nil {
		t.Fatalf("insert existing metadata failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close initial store failed: %v", err)
	}

	store, err = NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("reopen store failed: %v", err)
	}
	defer store.Close()

	if exists, err := store.tableExists("sds_omm"); err != nil {
		t.Fatalf("legacy table lookup failed: %v", err)
	} else if !exists {
		t.Fatal("interrupted legacy table should remain for maintenance migration")
	}
	record, err := store.Get("OMM.fbs", "existing-cid")
	if err != nil {
		t.Fatalf("existing migrated record lookup failed: %v", err)
	}
	if string(record) != string(existingPayload) {
		t.Fatalf("existing payload = %q, want %q", string(record), string(existingPayload))
	}
}

func TestCopyBlobSchemaRowsToMetadataTableSkipsExistingMetadataRows(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-resume-migration-test-*")
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

	if _, err := store.db.Exec(`
		CREATE TABLE sds_omm (
			cid TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			data BLOB NOT NULL,
			signature BLOB,
			UNIQUE(cid)
		)
	`); err != nil {
		t.Fatalf("create legacy schema table failed: %v", err)
	}
	existingPayload := []byte("existing-omm-payload")
	newPayload := []byte("new-omm-payload")
	if _, err := store.db.Exec(`
		INSERT INTO sds_omm (cid, peer_id, timestamp, data, signature)
		VALUES
			('existing-cid', 'source:celestrak', 1700000000, ?, NULL),
			('new-cid', 'source:celestrak', 1700000001, ?, NULL)
	`, existingPayload, newPayload); err != nil {
		t.Fatalf("insert legacy records failed: %v", err)
	}

	streamPath, streamOffset, recordLength, err := store.appendFlatSQLStreamRecord("OMM.fbs", existingPayload)
	if err != nil {
		t.Fatalf("append existing stream record failed: %v", err)
	}
	if err := insertSchemaMetadata(store.db, "OMM", "existing-cid", "source:celestrak", 1700000000, streamPath, streamOffset, recordLength, nil, 1700000000); err != nil {
		t.Fatalf("insert existing metadata failed: %v", err)
	}
	streamFile := filepath.Join(tmpDir, streamPath)
	before, err := os.Stat(streamFile)
	if err != nil {
		t.Fatalf("stat stream before migration failed: %v", err)
	}

	if err := store.copyBlobSchemaRowsToMetadataTable("OMM.fbs", "sds_omm", "OMM"); err != nil {
		t.Fatalf("copy legacy rows failed: %v", err)
	}

	after, err := os.Stat(streamFile)
	if err != nil {
		t.Fatalf("stat stream after migration failed: %v", err)
	}
	expectedGrowth := int64(4 + len(newPayload))
	if growth := after.Size() - before.Size(); growth != expectedGrowth {
		t.Fatalf("stream file grew by %d bytes, want only new record growth %d", growth, expectedGrowth)
	}
	record, err := store.Get("OMM.fbs", "new-cid")
	if err != nil {
		t.Fatalf("migrated new record lookup failed: %v", err)
	}
	if string(record) != string(newPayload) {
		t.Fatalf("migrated new payload = %q, want %q", string(record), string(newPayload))
	}
}

func TestNewFlatSQLStoreDoesNotSynchronouslyIndexExistingGlobalTables(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-existing-global-index-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := sql.Open("sqlite3", filepath.Join(tmpDir, "sdn.db"))
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE sdn_record_index (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			norad_cat_id INTEGER,
			entity_id TEXT,
			object_type TEXT,
			ops_status_code TEXT,
			epoch_unix INTEGER,
			epoch_day TEXT,
			source_timestamp INTEGER NOT NULL,
			created_at INTEGER DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY (schema_name, cid)
		)
	`); err != nil {
		t.Fatalf("create existing record index table failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite db failed: %v", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_sdn_record_index_time_window'`).Scan(&count); err != nil {
		t.Fatalf("index lookup failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("existing global table index should be deferred, count=%d", count)
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
		SourceURL:    "https://celestrak.org/pub/satcat.csv",
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

	byCID, err := store.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		BatchID:    "batch-001",
		Limit:      10,
		OrderByCID: true,
	})
	if err != nil {
		t.Fatalf("QueryIndexedRecords CID order failed: %v", err)
	}
	wantCIDs := []string{payloadCID, rocketBodies[0].CID}
	sort.Strings(wantCIDs)
	if len(byCID) != len(wantCIDs) {
		t.Fatalf("CID-ordered query returned %d records, want %d", len(byCID), len(wantCIDs))
	}
	for i, want := range wantCIDs {
		if byCID[i].CID != want {
			t.Fatalf("CID-ordered query[%d] = %s, want %s", i, byCID[i].CID, want)
		}
	}
}

func TestFlatSQLStoreQueryRecentRecordsAvoidsIndexJoin(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-recent-records-test-*")
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

	first := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	second := sds.NewOMMBuilder().WithNoradCatID(40909).WithObjectName("STARLINK").Build()
	if _, err := store.Store("OMM.fbs", first, "source:celestrak", nil); err != nil {
		t.Fatalf("store first OMM failed: %v", err)
	}
	if _, err := store.Store("OMM.fbs", second, "source:celestrak", nil); err != nil {
		t.Fatalf("store second OMM failed: %v", err)
	}

	records, err := store.QueryRecentRecords("OMM.fbs", 1)
	if err != nil {
		t.Fatalf("QueryRecentRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if string(records[0].Data) == string(first) {
		t.Fatalf("QueryRecentRecords returned oldest record first")
	}
}

func TestFlatSQLStoreQueryRecentRecordsPrefersLatestSourceTagMaterialization(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-recent-source-tags-test-*")
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

	current := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("CURRENT").Build()
	laterInsertedButOlderMaterialization := sds.NewOMMBuilder().WithNoradCatID(40909).WithObjectName("OLDER").Build()

	currentCID, err := store.StoreWithSourceTags("OMM.fbs", current, "source:celestrak", nil, SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		BatchID:    "current-batch",
	})
	if err != nil {
		t.Fatalf("store current OMM failed: %v", err)
	}
	olderCID, err := store.StoreWithSourceTags("OMM.fbs", laterInsertedButOlderMaterialization, "source:celestrak", nil, SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		BatchID:    "older-batch",
	})
	if err != nil {
		t.Fatalf("store older OMM failed: %v", err)
	}
	if _, err := store.db.Exec(`
		UPDATE sdn_record_source_tags
		SET created_at = CASE cid
			WHEN ? THEN 200
			WHEN ? THEN 100
			ELSE created_at
		END
		WHERE schema_name = 'OMM.fbs'
	`, currentCID, olderCID); err != nil {
		t.Fatalf("set source tag materialization times failed: %v", err)
	}

	records, err := store.QueryRecentRecords("OMM.fbs", 1)
	if err != nil {
		t.Fatalf("QueryRecentRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if records[0].CID != currentCID {
		t.Fatalf("QueryRecentRecords returned CID %q, want latest materialized CID %q", records[0].CID, currentCID)
	}
}

func TestFlatSQLStoreDataSummaryGroupsBySchemaAndSource(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-data-summary-test-*")
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

	cat := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	epm := sds.NewEPMBuilder().
		WithDN("CelesTrak Node").
		WithEmail("operator@example.test").
		WithMultiAddrs([]string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWDataSummary"}).
		Build()

	if _, err := store.StoreWithSourceTags("CAT.fbs", cat, "source:celestrak", nil, SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		BatchID:    "satcat-batch",
	}); err != nil {
		t.Fatalf("store CAT failed: %v", err)
	}
	if err := store.SaveLocalEPM("12D3KooWDataSummary", epm); err != nil {
		t.Fatalf("SaveLocalEPM failed: %v", err)
	}

	summary, err := store.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary failed: %v", err)
	}
	if summary.TotalRecords < 2 {
		t.Fatalf("TotalRecords = %d, want at least 2", summary.TotalRecords)
	}
	if got := findSchemaCount(summary.Schemas, "CAT.fbs"); got != 1 {
		t.Fatalf("CAT schema count = %d, want 1", got)
	}
	if got := findSchemaCount(summary.Schemas, "EPM.fbs"); got != 1 {
		t.Fatalf("EPM schema count = %d, want local EPM count 1", got)
	}
	if got := findSourceCount(summary.Sources, "CAT.fbs", "space-data-network-02", "celestrak-satcat-csv"); got != 1 {
		t.Fatalf("CAT source count = %d, want 1", got)
	}
	if got := findSourceCount(summary.Sources, "EPM.fbs", "local-node", "local-epm"); got != 1 {
		t.Fatalf("local EPM source count = %d, want 1", got)
	}
}

func TestFlatSQLStoreMaintainsSourceSummaryForMultipleProducers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-source-summary-test-*")
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

	alpha := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	bravo := sds.NewOMMBuilder().WithNoradCatID(40909).WithObjectName("STARLINK").Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", alpha, "peer-alpha", nil, SourceTags{
		ProviderID:   "peer-alpha",
		SourceName:   "celestrak-gp",
		BatchID:      "alpha-batch",
		ContentKeyID: "alpha-public-key",
	}); err != nil {
		t.Fatalf("store alpha OMM failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", bravo, "peer-bravo", nil, SourceTags{
		ProviderID:   "peer-bravo",
		SourceName:   "celestrak-gp",
		BatchID:      "bravo-batch",
		ContentKeyID: "bravo-public-key",
	}); err != nil {
		t.Fatalf("store bravo OMM failed: %v", err)
	}

	rows, err := store.db.Query(`
		SELECT provider_id, source_name, batch_id, record_count, total_bytes, max_rowid
		FROM sdn_record_source_summary
		WHERE schema_name = 'OMM.fbs'
		ORDER BY provider_id
	`)
	if err != nil {
		t.Fatalf("query source summary failed: %v", err)
	}
	defer rows.Close()

	type sourceRow struct {
		providerID  string
		sourceName  string
		batchID     string
		recordCount int64
		totalBytes  int64
		maxRowID    int64
	}
	var got []sourceRow
	for rows.Next() {
		var row sourceRow
		if err := rows.Scan(&row.providerID, &row.sourceName, &row.batchID, &row.recordCount, &row.totalBytes, &row.maxRowID); err != nil {
			t.Fatalf("scan source summary failed: %v", err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("source summary rows failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("source summary row count = %d, want 2: %#v", len(got), got)
	}
	if got[0].providerID != "peer-alpha" || got[0].recordCount != 1 || got[0].totalBytes != int64(len(alpha)) {
		t.Fatalf("alpha summary = %#v, want one alpha row with %d bytes", got[0], len(alpha))
	}
	if got[0].maxRowID <= 0 {
		t.Fatalf("alpha summary max rowid = %d, want populated row boundary", got[0].maxRowID)
	}
	if got[1].providerID != "peer-bravo" || got[1].recordCount != 1 || got[1].totalBytes != int64(len(bravo)) {
		t.Fatalf("bravo summary = %#v, want one bravo row with %d bytes", got[1], len(bravo))
	}
	if got[1].maxRowID <= got[0].maxRowID {
		t.Fatalf("bravo summary max rowid = %d, want after alpha rowid %d", got[1].maxRowID, got[0].maxRowID)
	}
}

func TestFlatSQLStoreStoresBatchWithSourceTags(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID:   "source:legacy-sqlite",
		SourceName:   "legacy-satellite-data",
		SourceURL:    "file:///opt/data/satellite_data.db",
		BatchID:      "legacy-batch",
		ContentKeyID: "public",
	}
	records := [][]byte{
		sds.NewOMMBuilder().
			WithNoradCatID(1).
			WithObjectID("1957-001A").
			WithEpoch("1959-01-11T01:49:23Z").
			Build(),
		sds.NewOMMBuilder().
			WithNoradCatID(25544).
			WithObjectID("1998-067A").
			WithEpoch("2024-01-16T11:51:22Z").
			Build(),
	}

	inserted, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "source:legacy-sqlite", nil, tags)
	if err != nil {
		t.Fatalf("StoreBatchWithSourceTags failed: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted = %d, want 2", inserted)
	}
	inserted, err = store.StoreBatchWithSourceTags("OMM.fbs", records, "source:legacy-sqlite", nil, tags)
	if err != nil {
		t.Fatalf("second StoreBatchWithSourceTags failed: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("second inserted = %d, want 0 for content-addressed replay", inserted)
	}

	total, err := store.CountRawRecords(RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
	})
	if err != nil {
		t.Fatalf("CountRawRecords failed: %v", err)
	}
	if total != 2 {
		t.Fatalf("source-tagged rows = %d, want 2", total)
	}
}

func TestFlatSQLStoreStoresSameRecordCIDForMultipleProducers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-source-producer-test-*")
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

	omm := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	alphaCID, err := store.StoreWithSourceTags("OMM.fbs", omm, "peer-alpha", nil, SourceTags{
		ProviderID:        "provider-alpha",
		SourceName:        "celestrak-gp",
		BatchID:           "alpha-batch",
		ContentKeyID:      "alpha-content-key",
		ProducerPeerID:    "peer-alpha",
		ProducerPublicKey: "alpha-public-key",
	})
	if err != nil {
		t.Fatalf("store alpha OMM failed: %v", err)
	}
	bravoCID, err := store.StoreWithSourceTags("OMM.fbs", omm, "peer-bravo", nil, SourceTags{
		ProviderID:        "provider-bravo",
		SourceName:        "celestrak-gp",
		BatchID:           "bravo-batch",
		ContentKeyID:      "bravo-content-key",
		ProducerPeerID:    "peer-bravo",
		ProducerPublicKey: "bravo-public-key",
	})
	if err != nil {
		t.Fatalf("store bravo OMM failed: %v", err)
	}
	if alphaCID != bravoCID {
		t.Fatalf("same FlatBuffer bytes produced different CIDs: %s != %s", alphaCID, bravoCID)
	}

	alphaRows, err := store.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", ProviderID: "provider-alpha", Limit: 10})
	if err != nil {
		t.Fatalf("query alpha records failed: %v", err)
	}
	bravoRows, err := store.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", ProviderID: "provider-bravo", Limit: 10})
	if err != nil {
		t.Fatalf("query bravo records failed: %v", err)
	}
	if len(alphaRows) != 1 || alphaRows[0].CID != alphaCID || alphaRows[0].SourceTags.ProducerPublicKey != "alpha-public-key" {
		t.Fatalf("alpha rows = %#v, want one alpha producer row", alphaRows)
	}
	if len(bravoRows) != 1 || bravoRows[0].CID != alphaCID || bravoRows[0].SourceTags.ProducerPublicKey != "bravo-public-key" {
		t.Fatalf("bravo rows = %#v, want one bravo producer row", bravoRows)
	}
}

func TestFlatSQLStoreQueryRawRecordsReturnsRawFlatBuffersByProducerAndType(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-raw-query-test-*")
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

	cat := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	cid, err := store.StoreWithSourceTags("CAT.fbs", cat, "source:celestrak", nil, SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		BatchID:    "satcat-batch",
	})
	if err != nil {
		t.Fatalf("store CAT failed: %v", err)
	}

	records, err := store.QueryRawRecords(RawRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		BatchID:    "satcat-batch",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QueryRawRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if records[0].CID != cid {
		t.Fatalf("CID = %q, want %q", records[0].CID, cid)
	}
	if string(records[0].Data) != string(cat) {
		t.Fatal("raw query did not return original FlatBuffer bytes")
	}
	if records[0].SourceTags.ProviderID != "space-data-network-02" || records[0].SourceTags.SourceName != "celestrak-satcat-csv" {
		t.Fatalf("source tags not populated: %#v", records[0].SourceTags)
	}
}

func TestFlatSQLStoreCountRawRecordsMatchesSourceFiltersWithoutHydratingRows(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-raw-count-test-*")
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

	ommA := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	ommB := sds.NewOMMBuilder().WithNoradCatID(40909).WithObjectName("STARLINK").Build()
	ommC := sds.NewOMMBuilder().WithNoradCatID(43013).WithObjectName("OTHER").Build()
	ommUntagged := sds.NewOMMBuilder().WithNoradCatID(99901).WithObjectName("UNTAGGED").Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", ommA, "source:celestrak", nil, SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "batch-a",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	}); err != nil {
		t.Fatalf("store OMM A failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", ommB, "source:celestrak", nil, SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "batch-a",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	}); err != nil {
		t.Fatalf("store OMM B failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", ommC, "source:other", nil, SourceTags{
		ProviderID:        "other-provider",
		SourceName:        "other-gp",
		BatchID:           "batch-b",
		ProducerPeerID:    "peer-other",
		ProducerPublicKey: "public-other",
	}); err != nil {
		t.Fatalf("store OMM C failed: %v", err)
	}
	if _, err := store.Store("OMM.fbs", ommUntagged, "source:untagged", nil); err != nil {
		t.Fatalf("store untagged OMM failed: %v", err)
	}

	count, err := store.CountRawRecords(RawRecordQuery{
		SchemaName:        "OMM.fbs",
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	})
	if err != nil {
		t.Fatalf("CountRawRecords failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountRawRecords = %d, want 2", count)
	}

	allCount, err := store.CountRawRecords(RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords all failed: %v", err)
	}
	if allCount != 4 {
		t.Fatalf("CountRawRecords all = %d, want 4", allCount)
	}
}

func TestFlatSQLStoreQueryRawRecordRefsUsesRowIDCursorWithSourceFilters(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-rowid-source-query-test-*")
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

	celestrakTags := SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "batch-a",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	}
	otherTags := SourceTags{
		ProviderID:        "other-provider",
		SourceName:        "other-gp",
		BatchID:           "batch-b",
		ProducerPeerID:    "peer-other",
		ProducerPublicKey: "public-other",
	}
	ommA := sds.NewOMMBuilder().WithNoradCatID(10001).WithObjectName("A").Build()
	ommB := sds.NewOMMBuilder().WithNoradCatID(10002).WithObjectName("B").Build()
	ommC := sds.NewOMMBuilder().WithNoradCatID(10003).WithObjectName("C").Build()
	ommD := sds.NewOMMBuilder().WithNoradCatID(10004).WithObjectName("D").Build()
	cidA, err := store.StoreWithSourceTags("OMM.fbs", ommA, "source:celestrak", nil, celestrakTags)
	if err != nil {
		t.Fatalf("store OMM A failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", ommB, "source:other", nil, otherTags); err != nil {
		t.Fatalf("store OMM B failed: %v", err)
	}
	cidC, err := store.StoreWithSourceTags("OMM.fbs", ommC, "source:celestrak", nil, celestrakTags)
	if err != nil {
		t.Fatalf("store OMM C failed: %v", err)
	}

	firstPage, err := store.QueryRawRecordRefs(RawRecordQuery{
		SchemaName:     "OMM.fbs",
		ProviderID:     celestrakTags.ProviderID,
		SourceName:     celestrakTags.SourceName,
		UseRowIDCursor: true,
		MaxRowID:       1_000_000,
		Limit:          2,
	})
	if err != nil {
		t.Fatalf("QueryRawRecordRefs first page failed: %v", err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("first page length = %d, want 2", len(firstPage))
	}
	if firstPage[0].CID != cidA || firstPage[1].CID != cidC {
		t.Fatalf("first page CIDs = %s, %s; want %s, %s", firstPage[0].CID, firstPage[1].CID, cidA, cidC)
	}
	if firstPage[0].RowID <= 0 || firstPage[1].RowID <= firstPage[0].RowID {
		t.Fatalf("rowids = %d, %d; want increasing source-filtered row cursor", firstPage[0].RowID, firstPage[1].RowID)
	}
	if firstPage[0].SourceTags.ProviderID != celestrakTags.ProviderID || firstPage[0].SourceTags.ProducerPublicKey != celestrakTags.ProducerPublicKey {
		t.Fatalf("source tags not preserved on first page: %#v", firstPage[0].SourceTags)
	}

	if _, err := store.StoreWithSourceTags("OMM.fbs", ommD, "source:celestrak", nil, celestrakTags); err != nil {
		t.Fatalf("store OMM D failed: %v", err)
	}
	snapshotPage, err := store.QueryRawRecordRefs(RawRecordQuery{
		SchemaName:     "OMM.fbs",
		ProviderID:     celestrakTags.ProviderID,
		SourceName:     celestrakTags.SourceName,
		UseRowIDCursor: true,
		MaxRowID:       firstPage[1].RowID,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("QueryRawRecordRefs snapshot page failed: %v", err)
	}
	if len(snapshotPage) != 2 || snapshotPage[0].CID != cidA || snapshotPage[1].CID != cidC {
		t.Fatalf("snapshot page = %+v, want only original celestrak rows", snapshotPage)
	}

	resumePage, err := store.QueryRawRecordRefs(RawRecordQuery{
		SchemaName:     "OMM.fbs",
		ProviderID:     celestrakTags.ProviderID,
		SourceName:     celestrakTags.SourceName,
		UseRowIDCursor: true,
		AfterRowID:     firstPage[0].RowID,
		MaxRowID:       firstPage[1].RowID,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("QueryRawRecordRefs resume page failed: %v", err)
	}
	if len(resumePage) != 1 || resumePage[0].CID != cidC {
		t.Fatalf("resume page = %+v, want only %s", resumePage, cidC)
	}
}

func TestFlatSQLStoreRawRecordQueriesApplySubscriptionSyncFilters(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-sync-filter-test-*")
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
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "batch-a",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	}
	ommA := sds.NewOMMBuilder().
		WithNoradCatID(10001).
		WithObjectID("1998-067A").
		WithObjectName("ISS").
		WithEpoch("2026-05-10T12:00:00Z").
		Build()
	ommB := sds.NewOMMBuilder().
		WithNoradCatID(20002).
		WithObjectID("2024-001A").
		WithObjectName("MATCH").
		WithEpoch("2026-05-11T12:00:00Z").
		Build()
	ommC := sds.NewOMMBuilder().
		WithNoradCatID(30003).
		WithObjectID("2025-001A").
		WithObjectName("EXCLUDED").
		WithEpoch("2026-05-12T12:00:00Z").
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", ommA, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store OMM A failed: %v", err)
	}
	matchCID, err := store.StoreWithSourceTags("OMM.fbs", ommB, "source:celestrak", nil, tags)
	if err != nil {
		t.Fatalf("store OMM B failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", ommC, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store OMM C failed: %v", err)
	}

	filter := RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Limit:      10,
		SyncFilter: "EPOCH >= '2026-05-11T00:00:00Z' AND NORAD_CAT_ID != 30003",
	}
	count, err := store.CountRawRecords(filter)
	if err != nil {
		t.Fatalf("CountRawRecords failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountRawRecords = %d, want 1", count)
	}
	records, err := store.QueryRawRecordRefs(filter)
	if err != nil {
		t.Fatalf("QueryRawRecordRefs failed: %v", err)
	}
	if len(records) != 1 || records[0].CID != matchCID {
		t.Fatalf("filtered records = %+v, want one CID %s", records, matchCID)
	}

	pnmCID, err := store.StoreWithSourceTags("PNM.fbs", buildTestPNM("celestrak:OMM:batch-a"), "source:celestrak", nil, SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-publications",
		BatchID:           "pnm-batch",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	})
	if err != nil {
		t.Fatalf("store CelesTrak PNM failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("PNM.fbs", buildTestPNM("other:OMM:batch-a"), "source:other", nil, SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-publications",
		BatchID:           "pnm-batch",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	}); err != nil {
		t.Fatalf("store other PNM failed: %v", err)
	}
	pnmRecords, err := store.QueryRawRecordRefs(RawRecordQuery{
		SchemaName: "PNM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-publications",
		Limit:      10,
		SyncFilter: "FILE_ID LIKE 'celestrak:%'",
	})
	if err != nil {
		t.Fatalf("QueryRawRecordRefs PNM failed: %v", err)
	}
	if len(pnmRecords) != 1 || pnmRecords[0].CID != pnmCID {
		t.Fatalf("filtered PNM records = %+v, want one CID %s", pnmRecords, pnmCID)
	}
}

func buildTestPNM(fileID string) []byte {
	builder := flatbuffers.NewBuilder(256)
	addr := builder.CreateString("/ipfs/bafydatasetmanifest")
	publishedAt := builder.CreateString("2026-05-13T00:00:00Z")
	cid := builder.CreateString("bafydatasetmanifest")
	fileName := builder.CreateString("dataset.dpm")
	fileIDOffset := builder.CreateString(fileID)
	signature := builder.CreateString("signature")
	signatureType := builder.CreateString("Ed25519")

	PNM.PNMStart(builder)
	PNM.PNMAddMULTIFORMAT_ADDRESS(builder, addr)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, publishedAt)
	PNM.PNMAddCID(builder, cid)
	PNM.PNMAddFILE_NAME(builder, fileName)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddSIGNATURE(builder, signature)
	PNM.PNMAddSIGNATURE_TYPE(builder, signatureType)
	root := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, root)
	return builder.FinishedBytes()
}

func findSchemaCount(schemas []DataSchemaSummary, schema string) int64 {
	for _, entry := range schemas {
		if entry.SchemaName == schema {
			return entry.Count
		}
	}
	return 0
}

func findSourceCount(sources []DataSourceSummary, schema, providerID, sourceName string) int64 {
	for _, entry := range sources {
		if entry.SchemaName == schema && entry.ProviderID == providerID && entry.SourceName == sourceName {
			return entry.Count
		}
	}
	return 0
}

func assertNoSQLiteBlobColumns(t *testing.T, store *FlatSQLStore, tableName string) {
	t.Helper()
	columns := tableColumnTypes(t, store, tableName)
	for name, typ := range columns {
		if typ == "BLOB" {
			t.Fatalf("%s.%s is a SQLite BLOB column; schema tables must be stream-backed metadata only", tableName, name)
		}
	}
	if _, ok := columns["data"]; ok {
		t.Fatalf("%s still has legacy data column", tableName)
	}
	if _, ok := columns["signature"]; ok {
		t.Fatalf("%s still has legacy signature column", tableName)
	}
}

func assertHasColumns(t *testing.T, store *FlatSQLStore, tableName string, names ...string) {
	t.Helper()
	columns := tableColumnTypes(t, store, tableName)
	for _, name := range names {
		if _, ok := columns[name]; !ok {
			t.Fatalf("%s missing expected column %s; columns=%v", tableName, name, columns)
		}
	}
}

func tableColumnTypes(t *testing.T, store *FlatSQLStore, tableName string) map[string]string {
	t.Helper()
	rows, err := store.db.Query(`PRAGMA table_info(` + tableName + `)`)
	if err != nil {
		t.Fatalf("inspect %s columns failed: %v", tableName, err)
	}
	defer rows.Close()

	columns := map[string]string{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan %s column failed: %v", tableName, err)
		}
		columns[name] = typ
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s column rows failed: %v", tableName, err)
	}
	return columns
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
	assertNoSQLiteBlobColumns(t, store, "OMM")

	var streamPath string
	var streamOffset, recordLength int64
	var signatureHex sql.NullString
	if err := store.db.QueryRow(`
		SELECT stream_path, stream_offset, record_length, signature_hex
		FROM OMM
		WHERE cid = ?
	`, cid).Scan(&streamPath, &streamOffset, &recordLength, &signatureHex); err != nil {
		t.Fatalf("stored metadata lookup failed: %v", err)
	}
	if streamPath == "" {
		t.Fatal("stream_path is empty")
	}
	if streamOffset < 0 {
		t.Fatalf("stream_offset = %d, want non-negative", streamOffset)
	}
	if recordLength != int64(len(testData)) {
		t.Fatalf("record_length = %d, want %d", recordLength, len(testData))
	}
	if !signatureHex.Valid || signatureHex.String == "" {
		t.Fatal("signature_hex was not stored as text")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, streamPath)); err != nil {
		t.Fatalf("FlatSQL stream file was not created: %v", err)
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

func TestFlatSQLStoreUpsertSourceTagsLeavesExistingTagTimestampStable(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-source-tag-idempotent-test-*")
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
		SourceName:   "celestrak-gp",
		SourceURL:    "https://celestrak.example/gp.csv",
		BatchID:      "source-batch",
		ContentKeyID: "public",
	}
	cid, err := store.StoreWithSourceTags("OMM.fbs", sds.NewOMMBuilder().Build(), "source:celestrak", nil, tags)
	if err != nil {
		t.Fatalf("StoreWithSourceTags failed: %v", err)
	}
	var firstCreatedAt int64
	if err := store.db.QueryRow(`
		SELECT created_at
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND cid = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`, "OMM.fbs", cid, tags.ProviderID, tags.SourceName, tags.BatchID).Scan(&firstCreatedAt); err != nil {
		t.Fatalf("query first created_at: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)
	if err := store.UpsertSourceTags("OMM.fbs", cid, tags); err != nil {
		t.Fatalf("UpsertSourceTags replay failed: %v", err)
	}

	var secondCreatedAt int64
	if err := store.db.QueryRow(`
		SELECT created_at
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND cid = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`, "OMM.fbs", cid, tags.ProviderID, tags.SourceName, tags.BatchID).Scan(&secondCreatedAt); err != nil {
		t.Fatalf("query second created_at: %v", err)
	}
	if secondCreatedAt != firstCreatedAt {
		t.Fatalf("created_at changed on idempotent source tag replay: first=%d second=%d", firstCreatedAt, secondCreatedAt)
	}
}

func TestFlatSQLStoreWaitsForExternalWriterLock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-writer-lock-test-*")
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

	locker, err := sql.Open("sqlite3", filepath.Join(tmpDir, "sdn.db")+"?_journal_mode=WAL&_busy_timeout=1000")
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	defer locker.Close()

	conn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatalf("open lock conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("begin external writer lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := store.StoreWithSourceTags("OMM.fbs", []byte("writer-lock-test-record"), "source:celestrak", nil, SourceTags{
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			SourceURL:    "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
			BatchID:      "writer-lock-batch",
			ContentKeyID: "public",
		})
		done <- err
	}()

	select {
	case err := <-done:
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		t.Fatalf("StoreWithSourceTags returned before external writer lock was released: %v", err)
	case <-time.After(5500 * time.Millisecond):
	}

	if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatalf("release external writer lock: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StoreWithSourceTags after writer lock release failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("StoreWithSourceTags did not complete after writer lock release")
	}
}

func TestFlatSQLStoreSourceTagUpsertWaitsWhenExistingRecordReadCanProceed(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-source-tag-upgrade-lock-test-*")
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

	cid, err := store.Store("OMM.fbs", []byte("source-tag-upgrade-lock-test-record"), "source:celestrak", nil)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	locker, err := sql.Open("sqlite3", filepath.Join(tmpDir, "sdn.db")+"?_journal_mode=WAL&_busy_timeout=1000")
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	defer locker.Close()

	conn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatalf("open lock conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("begin external writer lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- store.UpsertSourceTags("OMM.fbs", cid, SourceTags{
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			SourceURL:    "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
			BatchID:      "source-tag-upgrade-lock-batch",
			ContentKeyID: "public",
		})
	}()

	select {
	case err := <-done:
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		t.Fatalf("UpsertSourceTags returned before external writer lock was released: %v", err)
	case <-time.After(5500 * time.Millisecond):
	}

	if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatalf("release external writer lock: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UpsertSourceTags after writer lock release failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("UpsertSourceTags did not complete after writer lock release")
	}
}

func TestFlatSQLStoreReconcileSourceBatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-reconcile-batch-test-*")
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

	currentTags := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "current-batch"}
	oldTags := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "old-batch"}
	currentCID, err := store.StoreWithSourceTags("OMM.fbs", []byte(`{"satellite":"current"}`), "provider", nil, currentTags)
	if err != nil {
		t.Fatalf("store current record: %v", err)
	}
	oldCID, err := store.StoreWithSourceTags("OMM.fbs", []byte(`{"satellite":"old"}`), "provider", nil, oldTags)
	if err != nil {
		t.Fatalf("store old record: %v", err)
	}

	dryRun, err := store.ReconcileSourceBatch("OMM.fbs", "space-data-network-02", "celestrak-gp", "current-batch", false)
	if err != nil {
		t.Fatalf("dry-run reconcile source batch: %v", err)
	}
	if dryRun.Matched != 1 || dryRun.Deleted != 0 || dryRun.Apply {
		t.Fatalf("dry run result = %+v, want one matched and no delete", dryRun)
	}
	if _, err := store.Get("OMM.fbs", oldCID); err != nil {
		t.Fatalf("dry run deleted old record: %v", err)
	}

	applied, err := store.ReconcileSourceBatch("OMM.fbs", "space-data-network-02", "celestrak-gp", "current-batch", true)
	if err != nil {
		t.Fatalf("apply reconcile source batch: %v", err)
	}
	if applied.Matched != 1 || applied.Deleted != 1 || !applied.Apply {
		t.Fatalf("apply result = %+v, want one deleted", applied)
	}
	if _, err := store.Get("OMM.fbs", currentCID); err != nil {
		t.Fatalf("current record should remain: %v", err)
	}
	if _, err := store.Get("OMM.fbs", oldCID); err == nil {
		t.Fatal("old record should be deleted")
	}
	if _, err := store.GetSourceTags("OMM.fbs", oldCID); err == nil {
		t.Fatal("old source tags should be deleted")
	}
}

func TestFlatSQLStoreReconcileSourceBatchIndexedDuplicates(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-reconcile-duplicates-test-*")
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

	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "current-batch"}
	older := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectID("1998-067A").
		WithEpoch("2026-05-12T00:00:00Z").
		WithCreationDate("2026-05-12T01:00:00Z").
		Build()
	newer := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectID("1998-067A").
		WithEpoch("2026-05-12T00:00:00Z").
		WithCreationDate("2026-05-12T00:00:00Z").
		Build()
	other := sds.NewOMMBuilder().
		WithNoradCatID(40909).
		WithObjectID("2015-049A").
		WithEpoch("2026-05-12T00:00:00Z").
		WithCreationDate("2026-05-12T00:00:00Z").
		Build()

	olderCID, err := store.StoreWithSourceTags("OMM.fbs", older, "provider", nil, tags)
	if err != nil {
		t.Fatalf("store older duplicate: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	newerCID, err := store.StoreWithSourceTags("OMM.fbs", newer, "provider", nil, tags)
	if err != nil {
		t.Fatalf("store newer duplicate: %v", err)
	}
	otherCID, err := store.StoreWithSourceTags("OMM.fbs", other, "provider", nil, tags)
	if err != nil {
		t.Fatalf("store other record: %v", err)
	}

	dryRun, err := store.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", "space-data-network-02", "celestrak-gp", "current-batch", false)
	if err != nil {
		t.Fatalf("dry-run duplicate reconcile: %v", err)
	}
	if dryRun.Matched != 1 || dryRun.Deleted != 0 || dryRun.Apply {
		t.Fatalf("dry run result = %+v, want one matched and no delete", dryRun)
	}
	if _, err := store.Get("OMM.fbs", olderCID); err != nil {
		t.Fatalf("dry run deleted older duplicate: %v", err)
	}

	applied, err := store.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", "space-data-network-02", "celestrak-gp", "current-batch", true)
	if err != nil {
		t.Fatalf("apply duplicate reconcile: %v", err)
	}
	if applied.Matched != 1 || applied.Deleted != 1 || !applied.Apply {
		t.Fatalf("apply result = %+v, want one deleted", applied)
	}
	if _, err := store.Get("OMM.fbs", olderCID); err == nil {
		t.Fatal("older duplicate should be deleted")
	}
	if _, err := store.Get("OMM.fbs", newerCID); err != nil {
		t.Fatalf("newer duplicate should remain: %v", err)
	}
	if _, err := store.Get("OMM.fbs", otherCID); err != nil {
		t.Fatalf("other record should remain: %v", err)
	}
	count, err := store.CountRawRecords(RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		BatchID:    "current-batch",
	})
	if err != nil {
		t.Fatalf("CountRawRecords failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("source batch count = %d, want two logical records after duplicate reconcile", count)
	}
}

func TestFlatSQLStoreReconcileSourceBatchIndexedDuplicatesRefreshesOnlyAffectedBatchSummary(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-reconcile-duplicates-summary-test-*")
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

	currentTags := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "current-batch"}
	otherTags := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "other-batch"}
	for _, data := range [][]byte{
		sds.NewOMMBuilder().
			WithNoradCatID(25544).
			WithObjectID("1998-067A").
			WithEpoch("2026-05-12T00:00:00Z").
			WithCreationDate("2026-05-12T01:00:00Z").
			Build(),
		sds.NewOMMBuilder().
			WithNoradCatID(25544).
			WithObjectID("1998-067A").
			WithEpoch("2026-05-12T00:00:00Z").
			WithCreationDate("2026-05-12T00:00:00Z").
			Build(),
	} {
		if _, err := store.StoreWithSourceTags("OMM.fbs", data, "provider", nil, currentTags); err != nil {
			t.Fatalf("store current duplicate: %v", err)
		}
	}
	if _, err := store.StoreWithSourceTags("OMM.fbs", sds.NewOMMBuilder().
		WithNoradCatID(40909).
		WithObjectID("2015-049A").
		WithEpoch("2026-05-12T00:00:00Z").
		WithCreationDate("2026-05-12T00:00:00Z").
		Build(), "provider", nil, otherTags); err != nil {
		t.Fatalf("store other batch: %v", err)
	}

	var otherUpdatedAtBefore int64
	if err := store.db.QueryRow(`
		SELECT updated_at
		FROM sdn_record_source_summary
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`, "OMM.fbs", otherTags.ProviderID, otherTags.SourceName, otherTags.BatchID).Scan(&otherUpdatedAtBefore); err != nil {
		t.Fatalf("query other summary before reconcile: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)
	result, err := store.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", currentTags.ProviderID, currentTags.SourceName, currentTags.BatchID, true)
	if err != nil {
		t.Fatalf("apply duplicate reconcile: %v", err)
	}
	if result.Matched != 1 || result.Deleted != 1 {
		t.Fatalf("reconcile result = %+v, want one duplicate deleted", result)
	}

	var otherUpdatedAtAfter int64
	if err := store.db.QueryRow(`
		SELECT updated_at
		FROM sdn_record_source_summary
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`, "OMM.fbs", otherTags.ProviderID, otherTags.SourceName, otherTags.BatchID).Scan(&otherUpdatedAtAfter); err != nil {
		t.Fatalf("query other summary after reconcile: %v", err)
	}
	if otherUpdatedAtAfter != otherUpdatedAtBefore {
		t.Fatalf("other batch summary updated_at changed: before=%d after=%d", otherUpdatedAtBefore, otherUpdatedAtAfter)
	}
}

func TestFlatSQLStoreRefreshSourceBatchSummaryRepairsStaleCount(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-refresh-source-summary-test-*")
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

	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "current-batch", ContentKeyID: "public"}
	if _, err := store.StoreWithSourceTags("OMM.fbs", sds.NewOMMBuilder().Build(), "provider", nil, tags); err != nil {
		t.Fatalf("store source-tagged record: %v", err)
	}
	if _, err := store.db.Exec(`
		UPDATE sdn_record_source_summary
		SET record_count = 99, total_bytes = 99999
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`, "OMM.fbs", tags.ProviderID, tags.SourceName, tags.BatchID); err != nil {
		t.Fatalf("corrupt source summary: %v", err)
	}

	if err := store.RefreshSourceBatchSummary("OMM.fbs", tags.ProviderID, tags.SourceName, tags.BatchID); err != nil {
		t.Fatalf("RefreshSourceBatchSummary failed: %v", err)
	}

	var count, totalBytes int64
	if err := store.db.QueryRow(`
		SELECT record_count, total_bytes
		FROM sdn_record_source_summary
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`, "OMM.fbs", tags.ProviderID, tags.SourceName, tags.BatchID).Scan(&count, &totalBytes); err != nil {
		t.Fatalf("query source summary: %v", err)
	}
	if count != 1 {
		t.Fatalf("record_count = %d, want 1", count)
	}
	if totalBytes <= 0 || totalBytes == 99999 {
		t.Fatalf("total_bytes = %d, want repaired record byte count", totalBytes)
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
	cid, err := store.StoreWithSourceTags("CAT.fbs", testData, "TestPeer", make([]byte, 64), SourceTags{
		ProviderID: "provider",
		SourceName: "source",
		SourceURL:  "https://example.com/source.csv",
		BatchID:    "batch",
	})
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

	_, err = store.GetSourceTags("CAT.fbs", cid)
	if err == nil {
		t.Error("Expected source tags to be removed for deleted record")
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
