package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestFlatSQLStoreExportDatasetWindowWritesShardAndIndex(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-export-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-satcat-csv",
		SourceURL:    "https://fixture.test/pub/satcat.csv",
		BatchID:      "source-sha-001",
		ContentKeyID: "public",
	}
	recordA := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	recordB := sds.NewCATBuilder().
		WithNoradCatID(40909).
		WithObjectName("SATELLITE-1001").
		WithObjectID("2015-049A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()

	if _, err := store.StoreWithSourceTags("CAT.fbs", recordA, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store record A failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordB, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store record B failed: %v", err)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:         "CAT.fbs",
		ProviderID:         "space-data-network-02",
		SourceName:         "catalogfixture-satcat-csv",
		BatchID:            "source-sha-001",
		CAReadyResidentSet: true,
		From:               &from,
		To:                 &to,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	if export.RecordCount != 2 {
		t.Fatalf("RecordCount = %d, want 2", export.RecordCount)
	}
	if export.ShardPath == "" || export.IndexPath == "" {
		t.Fatalf("export paths must be set: %+v", export)
	}
	if export.ShardSHA256 == "" || export.IndexSHA256 == "" || export.QuerySHA256 == "" || export.ResultSHA256 == "" {
		t.Fatalf("export hashes must be set: %+v", export)
	}
	if export.ShardCID == "" || export.IndexCID == "" {
		t.Fatalf("export CIDs must be set: %+v", export)
	}
	if export.ShardCID[0] != 'b' || export.IndexCID[0] != 'b' {
		t.Fatalf("export CIDs must be CIDv1/base32 strings: shard=%q index=%q", export.ShardCID, export.IndexCID)
	}

	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatalf("read shard failed: %v", err)
	}
	reader := bytes.NewReader(shardBytes)
	var recordLengths []uint32
	for reader.Len() > 0 {
		var length uint32
		if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
			t.Fatalf("read length prefix failed: %v", err)
		}
		payload := make([]byte, length)
		if _, err := reader.Read(payload); err != nil {
			t.Fatalf("read record payload failed: %v", err)
		}
		recordLengths = append(recordLengths, length)
	}
	if len(recordLengths) != 2 {
		t.Fatalf("shard contains %d records, want 2", len(recordLengths))
	}

	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read index failed: %v", err)
	}
	var index DatasetExportIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatalf("unmarshal index failed: %v", err)
	}
	if index.SchemaName != "CAT.fbs" {
		t.Fatalf("SchemaName = %q, want CAT.fbs", index.SchemaName)
	}
	if index.ShardCID != export.ShardCID {
		t.Fatalf("ShardCID = %q, want %q", index.ShardCID, export.ShardCID)
	}
	if index.ProviderID != "space-data-network-02" || index.SourceName != "catalogfixture-satcat-csv" || index.BatchID != "source-sha-001" {
		t.Fatalf("source tags not preserved in index: %+v", index)
	}
	if len(index.Records) != 2 {
		t.Fatalf("index contains %d records, want 2", len(index.Records))
	}
	for i, record := range index.Records {
		if record.CID == "" || record.NoradCatID == nil || record.ObjectType != "PAYLOAD" || record.OpsStatusCode != "OPERATIONAL" {
			t.Fatalf("record %d missing query metadata: %+v", i, record)
		}
		if record.Offset < 0 || record.Length <= 0 {
			t.Fatalf("record %d has invalid byte range: %+v", i, record)
		}
	}
}

// TestExportDatasetWindowCarriesSourceBatchLicense is the regression test
// for sdn-dataset-publication-license-carriage: SourceTags already carries
// License/LicenseURL/Citation/ShareAlike (ingest_license_test.go proves
// that side), and buildDPMSourceBatch already writes them onto the wire
// when DatasetExportSourceBatch carries them (manifest.go) — the missing
// link was summarizeExportSourceBatches (export.go) silently dropping all
// four fields when building DatasetExportSourceBatch from
// DatasetExportIndexRecord.SourceTags at export time, blocking the
// owner-ordered SatNOGS $RFB publication (CC-BY-SA).
func TestExportDatasetWindowCarriesSourceBatchLicense(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-export-license-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "satnogs-rfb",
		SourceURL:    "https://db.satnogs.org/api/",
		BatchID:      "batch-license-export-1",
		ContentKeyID: "public",
		License:      "CC-BY-SA-4.0",
		LicenseURL:   "https://creativecommons.org/licenses/by-sa/4.0/",
		Citation:     "SatNOGS DB contributors, CC BY-SA 4.0",
		ShareAlike:   true,
	}
	record := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:satnogs", nil, tags); err != nil {
		t.Fatalf("store record failed: %v", err)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:         "CAT.fbs",
		ProviderID:         "space-data-network-02",
		SourceName:         "satnogs-rfb",
		BatchID:            "batch-license-export-1",
		CAReadyResidentSet: true,
		From:               &from,
		To:                 &to,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	if len(export.SourceBatches) != 1 {
		t.Fatalf("SourceBatches = %#v, want exactly 1", export.SourceBatches)
	}
	got := export.SourceBatches[0]
	if got.License != tags.License {
		t.Fatalf("License = %q, want %q", got.License, tags.License)
	}
	if got.LicenseURL != tags.LicenseURL {
		t.Fatalf("LicenseURL = %q, want %q", got.LicenseURL, tags.LicenseURL)
	}
	if got.Citation != tags.Citation {
		t.Fatalf("Citation = %q, want %q", got.Citation, tags.Citation)
	}
	if got.ShareAlike != tags.ShareAlike {
		t.Fatalf("ShareAlike = %v, want %v", got.ShareAlike, tags.ShareAlike)
	}
}

// TestBuildDPMSourceBatchWritesLicenseFields is a narrow unit test for the
// OTHER half of the license-carriage chain: buildDPMSourceBatch must write
// LICENSE/LICENSE_URL/CITATION onto the wire when DatasetExportSourceBatch
// carries them (this half was already correct — TestExportDatasetWindow
// CarriesSourceBatchLicense above covers the half that was missing), and
// must leave them unset (not empty-string) when the batch declares none, so
// a licence-free batch still produces byte-identical vtables/signatures.
func TestBuildDPMSourceBatchWritesLicenseFields(t *testing.T) {
	licensed := DatasetExportSourceBatch{
		SourceName:   "satnogs-rfb",
		SourceURL:    "https://db.satnogs.org/api/",
		SourceSHA256: "batch-sha",
		RecordCount:  1,
		License:      "CC-BY-SA-4.0",
		LicenseURL:   "https://creativecommons.org/licenses/by-sa/4.0/",
		Citation:     "SatNOGS DB contributors, CC BY-SA 4.0",
	}
	builder := flatbuffers.NewBuilder(256)
	offset := buildDPMSourceBatch(builder, licensed)
	builder.Finish(offset)
	got := dpm.GetRootAsDPMSourceBatch(builder.FinishedBytes(), 0)
	if string(got.LICENSE()) != licensed.License {
		t.Fatalf("wire LICENSE = %q, want %q", got.LICENSE(), licensed.License)
	}
	if string(got.LICENSE_URL()) != licensed.LicenseURL {
		t.Fatalf("wire LICENSE_URL = %q, want %q", got.LICENSE_URL(), licensed.LicenseURL)
	}
	if string(got.CITATION()) != licensed.Citation {
		t.Fatalf("wire CITATION = %q, want %q", got.CITATION(), licensed.Citation)
	}

	unlicensed := DatasetExportSourceBatch{SourceName: "no-license-source", SourceSHA256: "batch-sha-2", RecordCount: 1}
	builder2 := flatbuffers.NewBuilder(256)
	offset2 := buildDPMSourceBatch(builder2, unlicensed)
	builder2.Finish(offset2)
	got2 := dpm.GetRootAsDPMSourceBatch(builder2.FinishedBytes(), 0)
	if len(got2.LICENSE()) != 0 || len(got2.LICENSE_URL()) != 0 || len(got2.CITATION()) != 0 {
		t.Fatalf("unlicensed batch must leave license fields unset: LICENSE=%q LICENSE_URL=%q CITATION=%q",
			got2.LICENSE(), got2.LICENSE_URL(), got2.CITATION())
	}
}

func TestFlatSQLStoreRepairDatasetPublicationIndexFromShard(t *testing.T) {
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "catalogfixture-gp",
		SourceURL:         "https://fixture.test/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:           "source-sha-001",
		ContentKeyID:      "public",
		ProducerPeerID:    "space-data-network-02",
		ProducerPublicKey: "public",
	}
	record := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectID("1998-067A").
		WithObjectName("ISS").
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}

	outputDir := filepath.Join(store.DatasetPublicationOutputDir(), datasetPublicationPathComponent("OMM.fbs"))
	export, err := store.ExportDatasetWindow(outputDir, IndexedRecordQuery{
		SchemaName:          "OMM.fbs",
		ProviderID:          "space-data-network-02",
		SourceName:          "catalogfixture-gp",
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	publication := DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-gp",
		QueryProfile: DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        10,
		RecordCount:  export.RecordCount,
		ByteCount:    export.ShardBytes,
		ShardCID:     export.ShardCID,
		IndexCID:     export.IndexCID,
		ShardSHA256:  export.ShardSHA256,
		IndexSHA256:  export.IndexSHA256,
		QuerySHA256:  export.QuerySHA256,
		ResultSHA256: export.ResultSHA256,
		PublishedAt:  time.Unix(1700005555, 0).UTC(),
	}
	if err := os.Remove(export.IndexPath); err != nil {
		t.Fatalf("remove index failed: %v", err)
	}

	repaired, err := store.RepairDatasetPublicationIndexFromShard(outputDir, publication)
	if err != nil {
		t.Fatalf("RepairDatasetPublicationIndexFromShard failed: %v", err)
	}
	if repaired.IndexCID != export.IndexCID || repaired.IndexSHA256 != export.IndexSHA256 || repaired.QuerySHA256 != export.QuerySHA256 {
		t.Fatalf("repaired index identity changed: got %+v want %+v", repaired, export)
	}
	if _, err := os.Stat(export.IndexPath); err != nil {
		t.Fatalf("repaired index file missing: %v", err)
	}
}

func TestFlatSQLStoreRepairDatasetPublicationIndexFindsLegacyQueryShardPath(t *testing.T) {
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-gp",
		BatchID:      "source-sha-legacy-query",
		ContentKeyID: "public",
	}
	record := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectID("1998-067A").
		WithObjectName("ISS").
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}

	outputDir := filepath.Join(store.DatasetPublicationOutputDir(), datasetPublicationPathComponent("OMM.fbs"))
	export, err := store.ExportDatasetWindow(outputDir, IndexedRecordQuery{
		SchemaName:          "OMM.fbs",
		ProviderID:          "space-data-network-02",
		SourceName:          "catalogfixture-gp",
		BatchID:             "source-sha-legacy-query",
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	publication := DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-gp",
		BatchID:      "source-sha-legacy-query",
		QueryProfile: DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        10,
		RecordCount:  export.RecordCount,
		ByteCount:    export.ShardBytes,
		ShardCID:     export.ShardCID,
		IndexCID:     export.IndexCID,
		ShardSHA256:  export.ShardSHA256,
		IndexSHA256:  export.IndexSHA256,
		QuerySHA256:  strings.Repeat("a", 64),
		ResultSHA256: export.ResultSHA256,
		PublishedAt:  time.Unix(1700005555, 0).UTC(),
	}
	canonicalPath, err := store.DatasetPublicationShardPath(publication)
	if err != nil {
		t.Fatalf("DatasetPublicationShardPath failed: %v", err)
	}
	if canonicalPath == export.ShardPath {
		t.Fatal("test setup did not create a legacy query shard path mismatch")
	}
	if err := os.Remove(export.IndexPath); err != nil {
		t.Fatalf("remove index failed: %v", err)
	}

	repaired, err := store.RepairDatasetPublicationIndexFromShard(outputDir, publication)
	if err != nil {
		t.Fatalf("RepairDatasetPublicationIndexFromShard failed for legacy query shard path: %v", err)
	}
	if repaired.ShardCID != export.ShardCID || repaired.ShardSHA256 != export.ShardSHA256 || repaired.RecordCount != export.RecordCount {
		t.Fatalf("repaired shard identity changed: got %+v want %+v", repaired, export)
	}
	if repaired.QuerySHA256 == publication.QuerySHA256 {
		t.Fatalf("repair should derive current query hash from shard records, not keep stale publication query hash")
	}
	if _, err := os.Stat(repaired.IndexPath); err != nil {
		t.Fatalf("repaired index file missing: %v", err)
	}
}

func TestFlatSQLStoreExportDatasetWindowAllowsLargePublicationWindows(t *testing.T) {
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "catalogfixture-satcat-csv",
		SourceURL:  "https://fixture.test/pub/satcat.csv",
		BatchID:    "source-sha-large",
	}
	for i := 0; i < 1005; i++ {
		norad := uint32(40000 + i)
		record := sds.NewCATBuilder().
			WithNoradCatID(norad).
			WithObjectName(fmt.Sprintf("PAYLOAD-%d", norad)).
			WithObjectID(fmt.Sprintf("2026-%03dA", i)).
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:catalogfixture", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}

	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          "space-data-network-02",
		SourceName:          "catalogfixture-satcat-csv",
		BatchID:             "source-sha-large",
		Limit:               1005,
		AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	if export.RecordCount != 1005 {
		t.Fatalf("RecordCount = %d, want 1005", export.RecordCount)
	}
}

func TestPublishDatasetExportToIPFSPinsShardAndIndexCIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-ipfs-publish-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	shardBytes := []byte("shard-bytes")
	indexBytes := []byte(`{"shardCid":"placeholder"}`)
	shardCID, err := cidV1RawSHA256(shardBytes)
	if err != nil {
		t.Fatalf("compute shard cid: %v", err)
	}
	indexCID, err := cidV1RawSHA256(indexBytes)
	if err != nil {
		t.Fatalf("compute index cid: %v", err)
	}

	shardPath := filepath.Join(tmpDir, "shard.fbshard")
	indexPath := filepath.Join(tmpDir, "index.json")
	if err := os.WriteFile(shardPath, shardBytes, 0600); err != nil {
		t.Fatalf("write shard: %v", err)
	}
	if err := os.WriteFile(indexPath, indexBytes, 0600); err != nil {
		t.Fatalf("write index: %v", err)
	}

	const shardTransportCID = "bafybeishardtransportcid"
	const indexTransportCID = "bafybeiindextransportcid"
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v0/add" {
			t.Fatalf("path = %q, want /api/v0/add", r.URL.Path)
		}
		if r.URL.Query().Get("pin") != "true" || r.URL.Query().Get("cid-version") != "1" || r.URL.Query().Get("raw-leaves") != "true" || r.URL.Query().Get("hash") != "sha2-256" {
			t.Fatalf("unexpected IPFS add query: %s", r.URL.RawQuery)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("create multipart reader: %v", err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}
		if part.FormName() != "file" {
			t.Fatalf("multipart form name = %q, want file", part.FormName())
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart body: %v", err)
		}
		var cidValue string
		switch string(body) {
		case string(shardBytes):
			cidValue = shardTransportCID
		case string(indexBytes):
			cidValue = indexTransportCID
		default:
			t.Fatalf("unexpected body %q", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Name":"` + part.FileName() + `","Hash":"` + cidValue + `","Size":"123"}` + "\n"))
	}))
	defer server.Close()

	published, err := PublishDatasetExportToIPFS(context.Background(), server.URL, &DatasetExport{
		ShardPath: shardPath,
		ShardCID:  shardCID,
		IndexPath: indexPath,
		IndexCID:  indexCID,
	})
	if err != nil {
		t.Fatalf("PublishDatasetExportToIPFS failed: %v", err)
	}
	if published.ShardCID != shardTransportCID || published.IndexCID != indexTransportCID {
		t.Fatalf("published CIDs = %+v, want shard=%s index=%s", published, shardTransportCID, indexTransportCID)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if !strings.Contains(requests[0], "raw-leaves=true") || !strings.Contains(requests[1], "raw-leaves=true") {
		t.Fatalf("chunked UnixFS policy not applied to every request: %v", requests)
	}
}

func TestFetchIPFSBlockByCIDUsesCatForChunkedUnixFSFiles(t *testing.T) {
	payload := []byte("chunked-flatbuffer-shard")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v0/cat" {
			t.Fatalf("path = %q, want /api/v0/cat", r.URL.Path)
		}
		if r.URL.Query().Get("arg") != "bafybeichunkedshard" {
			t.Fatalf("arg = %q, want chunked shard CID", r.URL.Query().Get("arg"))
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	fetched, err := FetchIPFSBlockByCID(context.Background(), server.URL, "bafybeichunkedshard")
	if err != nil {
		t.Fatalf("FetchIPFSBlockByCID failed: %v", err)
	}
	if !bytes.Equal(fetched, payload) {
		t.Fatalf("fetched payload mismatch")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestPublishDatasetPublicationManifestToIPFSPinsManifestCIDAndRepublishesIPNSName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-dpm-ipfs-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manifestBytes := []byte("signed-manifest")
	manifestCID, err := cidV1RawSHA256(manifestBytes)
	if err != nil {
		t.Fatalf("compute manifest cid: %v", err)
	}
	manifestPath := filepath.Join(tmpDir, "manifest.dpm")
	if err := os.WriteFile(manifestPath, manifestBytes, 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/v0/block/put":
			if r.URL.Query().Get("pin") != "true" {
				t.Fatalf("block/put pin = %q, want true", r.URL.Query().Get("pin"))
			}
			reader, err := r.MultipartReader()
			if err != nil {
				t.Fatalf("create multipart reader: %v", err)
			}
			part, err := reader.NextPart()
			if err != nil {
				t.Fatalf("read multipart part: %v", err)
			}
			if part.FormName() != "data" {
				t.Fatalf("multipart form name = %q, want data", part.FormName())
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("read multipart body: %v", err)
			}
			if !bytes.Equal(body, manifestBytes) {
				t.Fatalf("manifest body mismatch")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Key":"` + manifestCID + `"}`))
		case "/api/v0/name/publish":
			if got := r.URL.Query().Get("arg"); got != "/ipfs/"+manifestCID {
				t.Fatalf("name/publish arg = %q, want /ipfs/%s", got, manifestCID)
			}
			if got := r.URL.Query().Get("lifetime"); got != ipnsRecordLifetime.String() {
				t.Fatalf("name/publish lifetime = %q, want %s", got, ipnsRecordLifetime.String())
			}
			if got := r.URL.Query().Get("allow-offline"); got != "true" {
				t.Fatalf("name/publish allow-offline = %q, want true", got)
			}
			if got := r.URL.Query().Get("key"); got != "" {
				t.Fatalf("name/publish key = %q, want unset (daemon's own identity)", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Name":"/ipns/k51qzi5uqu5dknk4691lf0hb9u8yqtly1cw2nvhuvdsrb5nem0b1695tl4xp52","Value":"/ipfs/` + manifestCID + `"}`))
		default:
			t.Fatalf("unexpected request URL: %s", r.URL.String())
		}
	}))
	defer server.Close()

	publishedCID, err := PublishDatasetPublicationManifestToIPFS(context.Background(), server.URL, &DatasetPublicationManifest{
		Path: manifestPath,
		CID:  manifestCID,
	})
	if err != nil {
		t.Fatalf("PublishDatasetPublicationManifestToIPFS failed: %v", err)
	}
	if publishedCID != manifestCID {
		t.Fatalf("published CID = %q, want %q", publishedCID, manifestCID)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (block/put then name/publish)", requests)
	}
}

func TestPublishDatasetPublicationManifestToIPFSLogsIPNSFailureButKeepsManifestCID(t *testing.T) {
	// A catalog publish that pin succeeds but the name re-publication fails
	// must still return the pinned manifest CID: the content is served, and
	// the deployment check (not the publish itself) is the tripwire for the
	// stale name.
	tmpDir, err := os.MkdirTemp("", "flatsql-dpm-ipfs-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manifestBytes := []byte("signed-manifest")
	manifestCID, err := cidV1RawSHA256(manifestBytes)
	if err != nil {
		t.Fatalf("compute manifest cid: %v", err)
	}
	manifestPath := filepath.Join(tmpDir, "manifest.dpm")
	if err := os.WriteFile(manifestPath, manifestBytes, 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/api/v0/block/put" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Key":"` + manifestCID + `"}`))
			return
		}
		if r.URL.Path == "/api/v0/name/publish" {
			http.Error(w, "record too old, please republish", http.StatusInternalServerError)
			return
		}
		t.Fatalf("unexpected request URL: %s", r.URL.String())
	}))
	defer server.Close()

	publishedCID, err := PublishDatasetPublicationManifestToIPFS(context.Background(), server.URL, &DatasetPublicationManifest{
		Path: manifestPath,
		CID:  manifestCID,
	})
	if err != nil {
		t.Fatalf("PublishDatasetPublicationManifestToIPFS failed on IPNS hiccup: %v", err)
	}
	if publishedCID != manifestCID {
		t.Fatalf("published CID = %q, want %q", publishedCID, manifestCID)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (name/publish must still be attempted)", requests)
	}
}

func TestIPNSRepublishPolicyStaysInsideRecordLifetime(t *testing.T) {
	// Acceptance: the recorded re-publication interval must be shorter than
	// the IPNS record lifetime, or records expire between re-publishes and
	// the name decays back to unresolvable exactly as observed when both
	// production names rotted.
	if ipnsKuboRepublishPeriod >= ipnsRecordLifetime {
		t.Fatalf("re-publish interval %s must be strictly shorter than record lifetime %s", ipnsKuboRepublishPeriod, ipnsRecordLifetime)
	}
}

func TestBuildDatasetPublicationPNMAnnouncesSignedManifestCID(t *testing.T) {
	_, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x24}, 128)))
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	publishedAt := time.Unix(1700000000, 0).UTC()
	manifest := &DatasetPublicationManifest{
		Path:      "/tmp/cat-active.dpm",
		CID:       "bafymanifestcid",
		FileID:    "catalogfixture:cat:CAT.fbs:2023-11-14T22:13:20Z",
		Signature: []byte{0x01, 0x02, 0x03},
	}
	pnmBytes, err := BuildDatasetPublicationPNM(manifest, DatasetPublicationPNMOptions{
		FileName:    "cat-active.dpm",
		PublishedAt: publishedAt,
		SigningKey:  signingKey,
	})
	if err != nil {
		t.Fatalf("BuildDatasetPublicationPNM failed: %v", err)
	}
	if !PNM.SizePrefixedPNMBufferHasIdentifier(pnmBytes) {
		t.Fatalf("PNM missing file identifier")
	}
	root := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	if got := string(root.CID()); got != manifest.CID {
		t.Fatalf("CID = %q, want %q", got, manifest.CID)
	}
	if got := string(root.FILE_ID()); got != manifest.FileID {
		t.Fatalf("FILE_ID = %q, want %q", got, manifest.FileID)
	}
	if got := string(root.MULTIFORMAT_ADDRESS()); got != "/ipfs/"+manifest.CID {
		t.Fatalf("MULTIFORMAT_ADDRESS = %q", got)
	}
	if got := string(root.SIGNATURE_TYPE()); got != "Ed25519" {
		t.Fatalf("SIGNATURE_TYPE = %q", got)
	}
	pnmSignature, err := hex.DecodeString(string(root.SIGNATURE()))
	if err != nil {
		t.Fatalf("decode PNM signature: %v", err)
	}
	if !ed25519.Verify(signingKey.Public().(ed25519.PublicKey), datasetPublicationPNMSignaturePayload(manifest.CID, manifest.FileID), pnmSignature) {
		t.Fatalf("PNM signature does not verify over manifest CID announcement")
	}
	if got := string(root.PUBLISH_TIMESTAMP()); got != publishedAt.Format(time.RFC3339) {
		t.Fatalf("PUBLISH_TIMESTAMP = %q", got)
	}
}

func TestVerifyDatasetPublicationReplayVerifiesPNMManifestAssetsAndQuery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-dpm-replay-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x63}, 128)))
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	providerPublicKey := signingKey.Public().(ed25519.PublicKey)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-satcat-csv",
		SourceURL:    "https://fixture.test/pub/satcat.csv",
		BatchID:      "source-sha-001",
		ContentKeyID: "public",
	}
	recordA := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	recordB := sds.NewCATBuilder().
		WithNoradCatID(40909).
		WithObjectName("SATELLITE-1001").
		WithObjectID("2015-049A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordA, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store record A failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordB, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store record B failed: %v", err)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:         "CAT.fbs",
		ProviderID:         "space-data-network-02",
		SourceName:         "catalogfixture-satcat-csv",
		BatchID:            "source-sha-001",
		CAReadyResidentSet: true,
		From:               &from,
		To:                 &to,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	publishedAt := time.Unix(1700000000, 0).UTC()
	manifest, err := BuildSignedDatasetPublicationManifest(filepath.Join(tmpDir, "publish"), DatasetPublicationManifestOptions{
		Export:          export,
		DatasetID:       "cat-active",
		UpdateID:        "source-sha-001",
		ProviderPeerID:  "16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4",
		ProviderEPMCID:  "bafy-provider-epm",
		PublishedAt:     publishedAt,
		SigningKey:      signingKey,
		SchemaHash:      "cat-schema-hash",
		QueryEngine:     "FlatSQL",
		QueryEngineVers: "sdn-index-v1",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest failed: %v", err)
	}
	pnmBytes, err := BuildDatasetPublicationPNM(manifest, DatasetPublicationPNMOptions{
		FileName:    filepath.Base(manifest.Path),
		PublishedAt: publishedAt,
		SigningKey:  signingKey,
	})
	if err != nil {
		t.Fatalf("BuildDatasetPublicationPNM failed: %v", err)
	}

	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	objects := map[string][]byte{
		manifest.CID:    manifest.Bytes,
		export.ShardCID: shardBytes,
		export.IndexCID: indexBytes,
	}
	result, err := VerifyDatasetPublicationReplay(context.Background(), store, DatasetPublicationReplayOptions{
		PNM:               pnmBytes,
		ProviderPublicKey: providerPublicKey,
		FetchByCID: func(_ context.Context, cid string) ([]byte, error) {
			data, ok := objects[cid]
			if !ok {
				return nil, os.ErrNotExist
			}
			return append([]byte(nil), data...), nil
		},
		WorkDir: filepath.Join(tmpDir, "replay"),
	})
	if err != nil {
		t.Fatalf("VerifyDatasetPublicationReplay failed: %v", err)
	}
	if result.ManifestCID != manifest.CID || result.ShardCID != export.ShardCID || result.IndexCID != export.IndexCID {
		t.Fatalf("unexpected replay result: %+v", result)
	}
	if result.RecordCount != export.RecordCount || result.ResultSHA256 != export.ResultSHA256 {
		t.Fatalf("unexpected replay result metadata: %+v", result)
	}

	pnmRoot := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	signatureHex := string(pnmRoot.SIGNATURE())
	tamperedSignatureHex := "0" + signatureHex[1:]
	if tamperedSignatureHex == signatureHex {
		tamperedSignatureHex = "1" + signatureHex[1:]
	}
	tamperedPNM := bytes.Replace(append([]byte(nil), pnmBytes...), []byte(signatureHex), []byte(tamperedSignatureHex), 1)
	if _, err := VerifyDatasetPublicationReplay(context.Background(), store, DatasetPublicationReplayOptions{
		PNM:               tamperedPNM,
		ProviderPublicKey: providerPublicKey,
		FetchByCID: func(_ context.Context, cid string) ([]byte, error) {
			return objects[cid], nil
		},
		WorkDir: filepath.Join(tmpDir, "tampered"),
	}); err == nil {
		t.Fatalf("tampered PNM must fail replay verification")
	}
}

func TestMaterializeDatasetPublicationImportsAdvertisedShard(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-dpm-materialize-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	providerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore provider failed: %v", err)
	}
	defer providerStore.Close()
	subscriberStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore subscriber failed: %v", err)
	}
	defer subscriberStore.Close()

	providerPublicKey, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x33}, 128)))
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	tags := SourceTags{
		ProviderID:   "catalogfixture.eth",
		SourceName:   "catalogfixture-satcat-csv",
		SourceURL:    "https://fixture.test/pub/satcat.csv",
		BatchID:      "source-sha-002",
		ContentKeyID: "public",
	}
	recordA := sds.NewCATBuilder().WithNoradCatID(25544).WithObjectName("ISS").WithObjectType("PAYLOAD").WithOpsStatus("OPERATIONAL").Build()
	recordB := sds.NewCATBuilder().WithNoradCatID(40909).WithObjectName("SATELLITE").WithObjectType("PAYLOAD").WithOpsStatus("OPERATIONAL").Build()
	if _, err := providerStore.StoreWithSourceTags("CAT.fbs", recordA, "catalogfixture.eth", nil, tags); err != nil {
		t.Fatalf("store record A failed: %v", err)
	}
	if _, err := providerStore.StoreWithSourceTags("CAT.fbs", recordB, "catalogfixture.eth", nil, tags); err != nil {
		t.Fatalf("store record B failed: %v", err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          "catalogfixture.eth",
		SourceName:          "catalogfixture-satcat-csv",
		BatchID:             "source-sha-002",
		CAReadyResidentSet:  true,
		Limit:               10,
		AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	publishedAt := time.Unix(1700001234, 0).UTC()
	manifest, err := BuildSignedDatasetPublicationManifest(filepath.Join(tmpDir, "publish"), DatasetPublicationManifestOptions{
		Export:         export,
		DatasetID:      "cat-active",
		UpdateID:       "source-sha-002",
		ProviderPeerID: "catalogfixture.eth",
		ProviderEPMCID: "bafy-provider-epm",
		PublishedAt:    publishedAt,
		SigningKey:     signingKey,
		SchemaHash:     "cat-schema-hash",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest failed: %v", err)
	}
	pnmBytes, err := BuildDatasetPublicationPNM(manifest, DatasetPublicationPNMOptions{
		PublishedAt: publishedAt,
		SigningKey:  signingKey,
	})
	if err != nil {
		t.Fatalf("BuildDatasetPublicationPNM failed: %v", err)
	}
	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	objects := map[string][]byte{
		manifest.CID:    manifest.Bytes,
		export.ShardCID: shardBytes,
		export.IndexCID: indexBytes,
	}
	fetchAttempts := make(map[string]int)
	fileFetchAttempts := make(map[string]int)
	result, err := MaterializeDatasetPublication(context.Background(), subscriberStore, DatasetPublicationReplayOptions{
		PNM:               pnmBytes,
		ProviderPublicKey: providerPublicKey,
		FetchByCID: func(_ context.Context, cid string) ([]byte, error) {
			fetchAttempts[cid]++
			if fetchAttempts[cid] == 1 {
				return nil, fmt.Errorf("transient content routing miss for %s", cid)
			}
			data, ok := objects[cid]
			if !ok {
				return nil, os.ErrNotExist
			}
			return append([]byte(nil), data...), nil
		},
		FetchByCIDToFile: func(_ context.Context, cid string, path string) error {
			fileFetchAttempts[cid]++
			data, ok := objects[cid]
			if !ok {
				return os.ErrNotExist
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			return os.WriteFile(path, data, 0o600)
		},
		FetchRetryDelays: []time.Duration{time.Millisecond},
		WorkDir:          filepath.Join(tmpDir, "materialize"),
	})
	if err != nil {
		t.Fatalf("MaterializeDatasetPublication failed: %v", err)
	}
	if result.Imported != 2 || result.RecordCount != 2 {
		t.Fatalf("materialize result = %+v, want 2 imported records", result)
	}
	records, err := subscriberStore.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "catalogfixture.eth",
		SourceName: "catalogfixture-satcat-csv",
		BatchID:    "source-sha-002",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query imported records failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("imported records = %d, want 2", len(records))
	}
	if fileFetchAttempts[export.ShardCID] == 0 || fileFetchAttempts[export.IndexCID] == 0 {
		t.Fatalf("materialization did not fetch shard/index through file fetcher: %+v", fileFetchAttempts)
	}
}

func TestFetchIPFSBlockByCIDToFileUsesCatForChunkedUnixFSFiles(t *testing.T) {
	payload := []byte("chunked-flatbuffer-shard-to-file")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v0/cat" {
			t.Fatalf("path = %q, want /api/v0/cat", r.URL.Path)
		}
		if r.URL.Query().Get("arg") != "bafybeichunkedshardfile" {
			t.Fatalf("arg = %q, want chunked shard CID", r.URL.Query().Get("arg"))
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "ipfs-fetch-file-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	outPath := filepath.Join(tmpDir, "shard.fbshard")
	if err := FetchIPFSBlockByCIDToFile(context.Background(), server.URL, "bafybeichunkedshardfile", outPath); err != nil {
		t.Fatalf("FetchIPFSBlockByCIDToFile failed: %v", err)
	}
	fetched, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read fetched file: %v", err)
	}
	if !bytes.Equal(fetched, payload) {
		t.Fatalf("fetched file payload mismatch")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestImportDatasetShardCountsOnlyNewRowsOnReplay(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-shard-replay-count-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	providerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore provider failed: %v", err)
	}
	defer providerStore.Close()
	subscriberStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore subscriber failed: %v", err)
	}
	defer subscriberStore.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-gp",
		SourceURL:    "https://fixture.test/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:      "source-sha-replay",
		ContentKeyID: "public",
	}
	for i := 0; i < 3; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(60000 + i)).
			WithObjectName("REPLAY-COUNT").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := providerStore.StoreWithSourceTags("CAT.fbs", record, "catalogfixture.eth", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	imported, index, err := subscriberStore.ImportDatasetShard(shardBytes, indexBytes, "catalogfixture.eth")
	if err != nil {
		t.Fatalf("first ImportDatasetShard failed: %v", err)
	}
	if imported != 3 || index.RecordCount != 3 {
		t.Fatalf("first imported=%d recordCount=%d, want 3/3", imported, index.RecordCount)
	}
	imported, _, err = subscriberStore.ImportDatasetShard(shardBytes, indexBytes, "catalogfixture.eth")
	if err != nil {
		t.Fatalf("second ImportDatasetShard failed: %v", err)
	}
	if imported != 0 {
		t.Fatalf("second imported=%d, want 0 for already-present immutable shard", imported)
	}

	records, err := subscriberStore.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query imported records failed: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("imported records = %d, want 3", len(records))
	}
}

func TestImportDatasetShardFromFilesStreamsShardIntoFlatSQL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-shard-file-import-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	providerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore provider failed: %v", err)
	}
	defer providerStore.Close()
	subscriberStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore subscriber failed: %v", err)
	}
	defer subscriberStore.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-gp",
		SourceURL:    "https://fixture.test/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:      "source-sha-file-import",
		ContentKeyID: "public",
	}
	for i := 0; i < 4; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(61000 + i)).
			WithObjectName("FILE-IMPORT").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := providerStore.StoreWithSourceTags("CAT.fbs", record, "catalogfixture.eth", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}

	imported, index, err := subscriberStore.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, "catalogfixture.eth")
	if err != nil {
		t.Fatalf("ImportDatasetShardFromFiles failed: %v", err)
	}
	if imported != 4 || index.RecordCount != 4 {
		t.Fatalf("imported=%d recordCount=%d, want 4/4", imported, index.RecordCount)
	}
	imported, _, err = subscriberStore.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, "catalogfixture.eth")
	if err != nil {
		t.Fatalf("replay ImportDatasetShardFromFiles failed: %v", err)
	}
	if imported != 0 {
		t.Fatalf("replay imported=%d, want 0 for already-present immutable shard", imported)
	}

	records, err := subscriberStore.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query imported records failed: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("imported records = %d, want 4", len(records))
	}
}

// TestImportDatasetShardAcceptsLegacyBareHexRecordCIDs is the loop A4 compat
// check: computeCID used to emit a bare SHA-256 hex digest (not a CID), so
// dataset shard bundles exported by a pre-A4 build carry that legacy format
// in their index JSON's per-record "cid" fields. Those bundles must still
// import cleanly after computeCID starts emitting real CIDv1 strings —
// manifest.go's importDatasetShardChunk now accepts either digest when
// verifying a record's bytes against its declared CID (see the
// computeCID(data) != record.CID && sha256Hex(data) != record.CID check).
// This test simulates a legacy export by rewriting a freshly exported
// index's record CIDs to the old bare-hex form and confirms import still
// succeeds, preserving the record's declared (legacy) identity rather than
// silently upgrading it.
func TestImportDatasetShardAcceptsLegacyBareHexRecordCIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-shard-legacy-cid-import-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	providerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore provider failed: %v", err)
	}
	defer providerStore.Close()
	subscriberStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore subscriber failed: %v", err)
	}
	defer subscriberStore.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-gp",
		SourceURL:    "https://fixture.test/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv",
		BatchID:      "source-legacy-cid-import",
		ContentKeyID: "public",
	}
	const recordCount = 3
	for i := 0; i < recordCount; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(62000 + i)).
			WithObjectName("LEGACY-CID-IMPORT").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := providerStore.StoreWithSourceTags("CAT.fbs", record, "catalogfixture.eth", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}

	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatalf("read shard file: %v", err)
	}
	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read index file: %v", err)
	}
	index, err := parseDatasetExportIndexBytes(indexBytes)
	if err != nil {
		t.Fatalf("parse index: %v", err)
	}
	if len(index.Records) != recordCount {
		t.Fatalf("exported %d records, want %d", len(index.Records), recordCount)
	}

	// Rewrite each record's CID from the current CIDv1 format to the legacy
	// bare SHA-256 hex digest computeCID emitted before loop A4, simulating
	// an export produced by a pre-A4 build.
	legacyCIDs := make(map[string]bool, len(index.Records))
	for i, rec := range index.Records {
		if rec.Offset < 0 || rec.Length < 0 || rec.Offset+4+rec.Length > int64(len(shardBytes)) {
			t.Fatalf("record %d offset/length outside shard", i)
		}
		frame := shardBytes[rec.Offset:]
		length := int64(binary.LittleEndian.Uint32(frame[:4]))
		if length != rec.Length {
			t.Fatalf("record %d frame length = %d, want %d", i, length, rec.Length)
		}
		data := frame[4 : 4+length]
		if computeCID(data) != rec.CID {
			t.Fatalf("record %d exported CID %q does not match freshly computed CIDv1", i, rec.CID)
		}
		legacyCID := sha256Hex(data)
		index.Records[i].CID = legacyCID
		legacyCIDs[legacyCID] = true
	}

	legacyIndexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal patched index: %v", err)
	}

	imported, importedIndex, err := subscriberStore.ImportDatasetShard(shardBytes, legacyIndexBytes, "catalogfixture.eth")
	if err != nil {
		t.Fatalf("ImportDatasetShard with legacy bare-hex record CIDs failed: %v", err)
	}
	if imported != recordCount || importedIndex.RecordCount != recordCount {
		t.Fatalf("imported=%d recordCount=%d, want %d/%d", imported, importedIndex.RecordCount, recordCount, recordCount)
	}

	records, err := subscriberStore.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query imported records failed: %v", err)
	}
	if len(records) != recordCount {
		t.Fatalf("imported records = %d, want %d", len(records), recordCount)
	}
	for _, rec := range records {
		if !legacyCIDs[rec.CID] {
			t.Errorf("imported record CID %q was not one of the legacy bare-hex CIDs from the patched index; import must preserve the declared identity, not rewrite it", rec.CID)
		}
	}
}

func TestBuildSignedDatasetPublicationManifestBindsExportAndQuery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-dpm-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x42}, 128)))
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	from := time.Unix(1700000000, 0).UTC()
	to := from.Add(time.Hour)
	shardPath := filepath.Join(tmpDir, "query.fbshard")
	indexPath := filepath.Join(tmpDir, "query.index.json")
	shardBytes := []byte("shard-bytes")
	indexBytes := []byte(`{"records":[]}`)
	if err := os.WriteFile(shardPath, shardBytes, 0600); err != nil {
		t.Fatalf("write shard: %v", err)
	}
	if err := os.WriteFile(indexPath, indexBytes, 0600); err != nil {
		t.Fatalf("write index: %v", err)
	}
	shardCID, err := cidV1RawSHA256(shardBytes)
	if err != nil {
		t.Fatalf("shard CID: %v", err)
	}
	indexCID, err := cidV1RawSHA256(indexBytes)
	if err != nil {
		t.Fatalf("index CID: %v", err)
	}
	query := IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          "provider-1",
		SourceName:          "catalogfixture-satcat-csv",
		BatchID:             "batch-sha",
		CAReadyResidentSet:  true,
		From:                &from,
		To:                  &to,
		Limit:               100,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	}
	queryJSON, err := canonicalQueryJSON(query)
	if err != nil {
		t.Fatalf("canonicalQueryJSON failed: %v", err)
	}
	parsedQuery, err := indexedRecordQueryFromCanonicalJSON(queryJSON)
	if err != nil {
		t.Fatalf("indexedRecordQueryFromCanonicalJSON failed: %v", err)
	}
	if !parsedQuery.AllowLargeResultSet || !parsedQuery.OrderByCID {
		t.Fatalf("canonical query did not preserve publication query options: %+v", parsedQuery)
	}
	export := &DatasetExport{
		SchemaName:     "CAT.fbs",
		RecordCount:    2,
		CanonicalQuery: string(queryJSON),
		QuerySHA256:    sha256Hex(queryJSON),
		ResultSHA256:   sha256Hex(shardBytes),
		ShardPath:      shardPath,
		ShardSHA256:    sha256Hex(shardBytes),
		ShardCID:       shardCID,
		ShardBytes:     int64(len(shardBytes)),
		IndexPath:      indexPath,
		IndexSHA256:    sha256Hex(indexBytes),
		IndexCID:       indexCID,
		IndexBytes:     int64(len(indexBytes)),
		SourceBatches: []DatasetExportSourceBatch{{
			ProviderID:    "provider-1",
			SourceName:    "catalogfixture-satcat-csv",
			SourceURL:     "https://fixture.test/satcat/records.php",
			SourceSHA256:  "batch-sha",
			ContentKeyID:  "public",
			RecordCount:   2,
			ParserVersion: "satcat-csv-v1",
		}},
	}

	manifest, err := BuildSignedDatasetPublicationManifest(tmpDir, DatasetPublicationManifestOptions{
		Export:          export,
		DatasetID:       "cat-active",
		UpdateID:        "batch-sha",
		ProviderPeerID:  "provider-1",
		ProviderEPMCID:  "bafy-provider-epm",
		PublishedAt:     from,
		SigningKey:      signingKey,
		SchemaHash:      "cat-schema-hash",
		QueryEngine:     "FlatSQL",
		QueryEngineVers: "sdn-index-v1",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest failed: %v", err)
	}
	if manifest.Path == "" || manifest.CID == "" || manifest.SHA256 == "" || len(manifest.Signature) != ed25519.SignatureSize {
		t.Fatalf("manifest result incomplete: %+v", manifest)
	}
	if manifest.CID[0] != 'b' {
		t.Fatalf("manifest CID must be CIDv1/base32, got %q", manifest.CID)
	}
	if !ed25519.Verify(signingKey.Public().(ed25519.PublicKey), manifest.SignaturePayloadSHA256[:], manifest.Signature) {
		t.Fatalf("manifest signature does not verify over unsigned manifest hash")
	}

	root := dpm.GetRootAsDPM(manifest.Bytes, 0)
	if got := string(root.DATASET_ID()); got != "cat-active" {
		t.Fatalf("DATASET_ID = %q", got)
	}
	if got := string(root.FILE_ID()); got != "cat-active:CAT.fbs:batch-sha" {
		t.Fatalf("FILE_ID = %q", got)
	}
	if got := string(root.PROVIDER_EPM_CID()); got != "bafy-provider-epm" {
		t.Fatalf("PROVIDER_EPM_CID = %q", got)
	}
	if root.ASSETSLength() != 2 {
		t.Fatalf("ASSETSLength = %d, want shard and index", root.ASSETSLength())
	}
	if got := string(root.QUERY(nil).CANONICAL_QUERY()); got != string(queryJSON) {
		t.Fatalf("canonical query mismatch\n got: %s\nwant: %s", got, queryJSON)
	}
	if got := string(root.QUERY(nil).QUERY_SHA256()); got != sha256Hex(queryJSON) {
		t.Fatalf("QUERY_SHA256 = %q", got)
	}
	if got := string(root.QUERY(nil).CANONICAL_ORDER()); got != "FlatSQL export order v1" {
		t.Fatalf("CANONICAL_ORDER = %q", got)
	}
	if root.SOURCESLength() != 1 {
		t.Fatalf("SOURCESLength = %d, want 1", root.SOURCESLength())
	}
	var source dpm.DPMSourceBatch
	if !root.SOURCES(&source, 0) || string(source.SOURCE_URL()) == "" {
		t.Fatalf("source batch missing: %+v", source)
	}
}

// TestExportDatasetWindowRefusesDuringRecordCatalogReplay is the regression
// test for sdn-replay-checkpoint-resume: the in-memory FlatSQL control tables
// hold no state across a restart (flatsql.go), so a store reopened the way the
// daemon opens it — WithDeferredRecordCatalogReplay — answers every query
// truthfully for whatever has landed so far and silently for what has not.
// Before this fix, ExportDatasetWindow queried straight through a partial
// replay and produced a partial (or, early on, empty) shard that reported
// success — the "run completed but landed no batch" failure that blocked the
// owner-ordered RFB publication lane. Export must now refuse with
// ErrRecordCatalogHydrating instead, and must succeed once hydration lands.
func TestExportDatasetWindowRefusesDuringRecordCatalogReplay(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}

	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	tags := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-1"}
	record := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store record failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopen exactly the way the daemon opens it post-boot: replay deferred so
	// admin/network surfaces can start before catalog-scale hydration finishes.
	reopened, err := NewFlatSQLStore(basePath, validator,
		WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("deferred reopen failed: %v", err)
	}
	defer reopened.Close()
	if reopened.RecordCatalogHydrated() {
		t.Fatal("record catalog must not report hydrated before replay on a deferred reopen")
	}

	outDir := filepath.Join(t.TempDir(), "export")
	filter := IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "prov-a",
		SourceName: "catalogfixture-gp",
		BatchID:    "batch-1",
		Limit:      10,
	}
	if _, err := reopened.ExportDatasetWindow(outDir, filter); !errors.Is(err, ErrRecordCatalogHydrating) {
		t.Fatalf("ExportDatasetWindow before replay: err = %v, want ErrRecordCatalogHydrating", err)
	}

	if _, err := reopened.ReplayRecordCatalog(false, nil); err != nil {
		t.Fatalf("ReplayRecordCatalog: %v", err)
	}
	if !reopened.RecordCatalogHydrated() {
		t.Fatal("record catalog must report hydrated after ReplayRecordCatalog")
	}

	export, err := reopened.ExportDatasetWindow(outDir, filter)
	if err != nil {
		t.Fatalf("ExportDatasetWindow after replay: %v", err)
	}
	if export.RecordCount != 1 {
		t.Fatalf("RecordCount = %d, want 1", export.RecordCount)
	}
}
