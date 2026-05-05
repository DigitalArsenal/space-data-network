package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		SourceName:   "celestrak-satcat-csv",
		SourceURL:    "https://celestrak.org/satcat/records.php?GROUP=active&FORMAT=CSV",
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
		WithObjectName("STARLINK-1001").
		WithObjectID("2015-049A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()

	if _, err := store.StoreWithSourceTags("CAT.fbs", recordA, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store record A failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordB, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store record B failed: %v", err)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:         "CAT.fbs",
		ProviderID:         "space-data-network-02",
		SourceName:         "celestrak-satcat-csv",
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
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
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
	if index.ProviderID != "space-data-network-02" || index.SourceName != "celestrak-satcat-csv" || index.BatchID != "source-sha-001" {
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

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v0/block/put" {
			t.Fatalf("path = %q, want /api/v0/block/put", r.URL.Path)
		}
		if r.URL.Query().Get("pin") != "true" || r.URL.Query().Get("format") != "raw" || r.URL.Query().Get("mhtype") != "sha2-256" {
			t.Fatalf("unexpected IPFS block put query: %s", r.URL.RawQuery)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var cidValue string
		switch string(body) {
		case string(shardBytes):
			cidValue = shardCID
		case string(indexBytes):
			cidValue = indexCID
		default:
			t.Fatalf("unexpected body %q", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Key":"` + cidValue + `"}`))
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
	if published.ShardCID != shardCID || published.IndexCID != indexCID {
		t.Fatalf("published CIDs = %+v, want shard=%s index=%s", published, shardCID, indexCID)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if !strings.Contains(requests[0], "pin=true") || !strings.Contains(requests[1], "pin=true") {
		t.Fatalf("pin policy not applied to every request: %v", requests)
	}
}
