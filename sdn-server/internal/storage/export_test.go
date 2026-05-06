package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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
		SourceName: "celestrak-satcat-csv",
		SourceURL:  "https://celestrak.org/satcat/records.php?GROUP=active&FORMAT=CSV",
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
		if _, err := store.StoreWithSourceTags("CAT.fbs", record, "source:celestrak", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}

	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          "space-data-network-02",
		SourceName:          "celestrak-satcat-csv",
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

func TestPublishDatasetPublicationManifestToIPFSPinsManifestCID(t *testing.T) {
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
		if r.URL.Path != "/api/v0/block/put" || r.URL.Query().Get("pin") != "true" {
			t.Fatalf("unexpected request URL: %s", r.URL.String())
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
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
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
		FileID:    "celestrak:cat:CAT.fbs:2023-11-14T22:13:20Z",
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
		ProviderID:   "celestrak.eth",
		SourceName:   "celestrak-satcat-csv",
		SourceURL:    "https://celestrak.org/satcat/records.php?GROUP=active&FORMAT=CSV",
		BatchID:      "source-sha-002",
		ContentKeyID: "public",
	}
	recordA := sds.NewCATBuilder().WithNoradCatID(25544).WithObjectName("ISS").WithObjectType("PAYLOAD").WithOpsStatus("OPERATIONAL").Build()
	recordB := sds.NewCATBuilder().WithNoradCatID(40909).WithObjectName("STARLINK").WithObjectType("PAYLOAD").WithOpsStatus("OPERATIONAL").Build()
	if _, err := providerStore.StoreWithSourceTags("CAT.fbs", recordA, "celestrak.eth", nil, tags); err != nil {
		t.Fatalf("store record A failed: %v", err)
	}
	if _, err := providerStore.StoreWithSourceTags("CAT.fbs", recordB, "celestrak.eth", nil, tags); err != nil {
		t.Fatalf("store record B failed: %v", err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          "celestrak.eth",
		SourceName:          "celestrak-satcat-csv",
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
		ProviderPeerID: "celestrak.eth",
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
		ProviderID: "celestrak.eth",
		SourceName: "celestrak-satcat-csv",
		BatchID:    "source-sha-002",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query imported records failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("imported records = %d, want 2", len(records))
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
		SchemaName:         "CAT.fbs",
		ProviderID:         "provider-1",
		SourceName:         "celestrak-satcat-csv",
		BatchID:            "batch-sha",
		CAReadyResidentSet: true,
		From:               &from,
		To:                 &to,
		Limit:              100,
	}
	queryJSON, err := canonicalQueryJSON(query)
	if err != nil {
		t.Fatalf("canonicalQueryJSON failed: %v", err)
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
			SourceName:    "celestrak-satcat-csv",
			SourceURL:     "https://celestrak.org/satcat/records.php",
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
