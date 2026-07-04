package protocol

// Wire-compat fixture recorder (sdn-js loop D.4).
//
// Records GENUINE flatsql-sync response byte streams from THIS server
// implementation (the exact bytes a deployed peer emits over
// /space-data-network/flatsql-sync/1.0.0) so the sdn-js suite can assert
// wire compatibility of its datasync-fed engine store against a recorded
// peer stream: byte-stable framing, rowid-snapshot cursor semantics,
// provider/source/batch provenance, and computeCID identity.
//
// Run manually to (re)record:
//
//	SDN_SYNC_FIXTURE_DIR=../../../sdn-js/src/testdata/flatsql-sync \
//	  go test ./internal/protocol -run TestRecordFlatSQLSyncWireFixtures -v
//
// The fixtures are committed into sdn-js; this test is skipped otherwise.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type fixtureRecordMeta struct {
	CID        string `json:"cid"`
	Norad      uint32 `json:"norad"`
	ObjectName string `json:"objectName"`
	ProviderID string `json:"providerId"`
	SourceName string `json:"sourceName"`
	BatchID    string `json:"batchId"`
	SizeBytes  int    `json:"sizeBytes"`
}

type fixtureCursorMeta struct {
	Encoded    string `json:"encoded"`
	Version    int    `json:"v"`
	Mode       string `json:"mode"`
	AfterRowID int64  `json:"afterRowId"`
	MaxRowID   int64  `json:"maxRowId"`
	SnapshotID string `json:"snapshotId"`
}

type fixturePageMeta struct {
	File       string             `json:"file"`
	SHA256     string             `json:"sha256"`
	Request    map[string]any     `json:"request"`
	TotalCount int64              `json:"totalCount"`
	Count      int                `json:"count"`
	SnapshotID string             `json:"snapshotId"`
	Head       string             `json:"head"`
	Cursor     *fixtureCursorMeta `json:"cursor,omitempty"`
	NextCursor *fixtureCursorMeta `json:"nextCursor,omitempty"`
	ResultCIDs []string           `json:"resultCids"`
}

type fixtureShardMeta struct {
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	ShardCID    string `json:"shardCid"`
	ShardSHA256 string `json:"shardSha256"`
	RowCount    int64  `json:"rowCount"`
	ByteCount   int64  `json:"byteCount"`
	ProviderID  string `json:"providerId"`
	SourceName  string `json:"sourceName"`
	BatchID     string `json:"batchId"`
}

type fixtureManifestMeta struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type syncFixtureMeta struct {
	Protocol       string              `json:"protocol"`
	RecordedFrom   string              `json:"recordedFrom"`
	Records        []fixtureRecordMeta `json:"records"`
	Page1          fixturePageMeta     `json:"page1"`
	Page2          fixturePageMeta     `json:"page2"`
	Manifest       fixtureManifestMeta `json:"manifest"`
	PublishedShard fixtureShardMeta    `json:"publishedShard"`
}

func TestRecordFlatSQLSyncWireFixtures(t *testing.T) {
	fixtureDir := os.Getenv("SDN_SYNC_FIXTURE_DIR")
	if fixtureDir == "" {
		t.Skip("SDN_SYNC_FIXTURE_DIR not set; fixture recording is manual")
	}
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("MkdirAll fixture dir failed: %v", err)
	}

	store := newFlatSQLSyncTestStore(t)
	specs := []struct {
		norad      uint32
		objectName string
		tags       storage.SourceTags
	}{
		{56775, "STARLINK-6292", storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "batch-a"}},
		{25544, "ISS (ZARYA)", storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "batch-a"}},
		{43013, "NOAA-20", storage.SourceTags{ProviderID: "demo-provider", SourceName: "spacetrack-gp", BatchID: "batch-b"}},
	}
	meta := syncFixtureMeta{
		Protocol:     FlatSQLSyncProtocolID,
		RecordedFrom: "sdn-server internal/protocol.FlatSQLSyncHandler (genuine handler output)",
	}
	for _, spec := range specs {
		payload := buildFixtureOMM(t, spec.norad, spec.objectName)
		cid, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:"+spec.tags.SourceName, nil, spec.tags)
		if err != nil {
			t.Fatalf("StoreWithSourceTags failed: %v", err)
		}
		meta.Records = append(meta.Records, fixtureRecordMeta{
			CID:        cid,
			Norad:      spec.norad,
			ObjectName: spec.objectName,
			ProviderID: spec.tags.ProviderID,
			SourceName: spec.tags.SourceName,
			BatchID:    spec.tags.BatchID,
			SizeBytes:  len(payload),
		})
	}

	handler := NewFlatSQLSyncHandler(store)

	// --- read_chunk page 1 (limit 2, schema-wide: refs carry per-record
	// provider/source/batch provenance across both sources) ---
	page1Req := flatSQLSyncRequest{Op: "read_chunk", Schema: "OMM.fbs", Limit: 2}
	var page1 bytes.Buffer
	if err := handler.handleReadChunk(&page1, page1Req); err != nil {
		t.Fatalf("handleReadChunk page1 failed: %v", err)
	}
	page1Header := decodeFixtureChunkHeader(t, page1.Bytes())
	meta.Page1 = fixturePageMeta{
		File:   "read_chunk_page1.bin",
		SHA256: fixtureSHA256(page1.Bytes()),
		Request: map[string]any{
			"op": "read_chunk", "schema": "OMM.fbs", "limit": 2,
		},
		TotalCount: page1Header.TotalCount,
		Count:      page1Header.Count,
		SnapshotID: page1Header.SnapshotID,
		Head:       page1Header.Head,
		Cursor:     decodeFixtureCursor(t, page1Header.Cursor),
		NextCursor: decodeFixtureCursor(t, page1Header.NextCursor),
		ResultCIDs: page1Header.resultCIDs(),
	}
	writeFixtureFile(t, fixtureDir, meta.Page1.File, page1.Bytes())

	// --- read_chunk page 2 (resume on the rowid-snapshot cursor) ---
	page2Req := flatSQLSyncRequest{
		Op:         "read_chunk",
		Schema:     "OMM.fbs",
		Limit:      2,
		Cursor:     page1Header.NextCursor,
		SnapshotID: page1Header.SnapshotID,
		Head:       page1Header.Head,
	}
	var page2 bytes.Buffer
	if err := handler.handleReadChunk(&page2, page2Req); err != nil {
		t.Fatalf("handleReadChunk page2 failed: %v", err)
	}
	page2Header := decodeFixtureChunkHeader(t, page2.Bytes())
	meta.Page2 = fixturePageMeta{
		File:   "read_chunk_page2.bin",
		SHA256: fixtureSHA256(page2.Bytes()),
		Request: map[string]any{
			"op": "read_chunk", "schema": "OMM.fbs", "limit": 2,
			"cursor": page1Header.NextCursor, "snapshot_id": page1Header.SnapshotID, "head": page1Header.Head,
		},
		TotalCount: page2Header.TotalCount,
		Count:      page2Header.Count,
		SnapshotID: page2Header.SnapshotID,
		Head:       page2Header.Head,
		Cursor:     decodeFixtureCursor(t, page2Header.Cursor),
		NextCursor: decodeFixtureCursor(t, page2Header.NextCursor),
		ResultCIDs: page2Header.resultCIDs(),
	}
	writeFixtureFile(t, fixtureDir, meta.Page2.File, page2.Bytes())

	// --- open_manifest (ordered segment fan-out over the same cursor space) ---
	var manifest bytes.Buffer
	if err := handler.handleOpenManifest(&manifest, flatSQLSyncRequest{
		Op: "open_manifest", Schema: "OMM.fbs", Limit: 2,
	}); err != nil {
		t.Fatalf("handleOpenManifest failed: %v", err)
	}
	meta.Manifest = fixtureManifestMeta{
		File:   "open_manifest.bin",
		SHA256: fixtureSHA256(manifest.Bytes()),
	}
	writeFixtureFile(t, fixtureDir, meta.Manifest.File, manifest.Bytes())

	// --- published shard: the celestrak-gp partition streamed by the SAME
	// record framing the dataset publisher writes (WriteRawRecordFrames),
	// registered as a dataset publication and served by the genuine
	// read_published_shard handler. ---
	var shardStream bytes.Buffer
	if err := handler.handleReadChunk(&shardStream, flatSQLSyncRequest{
		Op:         "read_chunk",
		Schema:     "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		BatchID:    "batch-a",
		Limit:      50000,
	}); err != nil {
		t.Fatalf("handleReadChunk shard stream failed: %v", err)
	}
	shardBytes := rawFramesSection(t, shardStream.Bytes())
	shardSum := sha256.Sum256(shardBytes)
	querySum := sha256.Sum256([]byte("OMM D.4 wire-compat publication query"))
	pub := storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-a",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50000,
		RecordCount:  2,
		ByteCount:    int64(len(shardBytes)),
		ShardCID:     "bafkd4wirecompatshard",
		IndexCID:     "bafkd4wirecompatindex",
		ManifestCID:  "bafkd4wirecompatmanifest",
		ShardSHA256:  hex.EncodeToString(shardSum[:]),
		QuerySHA256:  hex.EncodeToString(querySum[:]),
		ResultSHA256: hex.EncodeToString(shardSum[:]),
		FeedSequence: 1,
		FeedHead:     "bafkd4wirecompatshard",
	}
	if err := store.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}
	shardPath, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		t.Fatalf("DatasetPublicationShardPath failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(shardPath), 0o700); err != nil {
		t.Fatalf("MkdirAll shard dir failed: %v", err)
	}
	if err := os.WriteFile(shardPath, shardBytes, 0o600); err != nil {
		t.Fatalf("WriteFile shard failed: %v", err)
	}
	var shard bytes.Buffer
	if err := handler.handleReadPublishedShard(&shard, flatSQLSyncRequest{
		Op:           "read_published_shard",
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-a",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		CID:          pub.ShardCID,
	}); err != nil {
		t.Fatalf("handleReadPublishedShard failed: %v", err)
	}
	meta.PublishedShard = fixtureShardMeta{
		File:        "read_published_shard.bin",
		SHA256:      fixtureSHA256(shard.Bytes()),
		ShardCID:    pub.ShardCID,
		ShardSHA256: pub.ShardSHA256,
		RowCount:    int64(pub.RecordCount),
		ByteCount:   pub.ByteCount,
		ProviderID:  pub.ProviderID,
		SourceName:  pub.SourceName,
		BatchID:     pub.BatchID,
	}
	writeFixtureFile(t, fixtureDir, meta.PublishedShard.File, shard.Bytes())

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture meta failed: %v", err)
	}
	writeFixtureFile(t, fixtureDir, "meta.json", append(metaBytes, '\n'))
	t.Logf("recorded flatsql-sync wire fixtures into %s", fixtureDir)
}

func buildFixtureOMM(t *testing.T, norad uint32, objectName string) []byte {
	t.Helper()
	builder := flatbuffers.NewBuilder(256)
	objectNameOffset := builder.CreateString(objectName)
	objectIDOffset := builder.CreateString("2023-078J")
	epochOffset := builder.CreateString("2026-05-10T12:00:00Z")
	OMM.OMMStart(builder)
	OMM.OMMAddNORAD_CAT_ID(builder, norad)
	OMM.OMMAddOBJECT_NAME(builder, objectNameOffset)
	OMM.OMMAddOBJECT_ID(builder, objectIDOffset)
	OMM.OMMAddEPOCH(builder, epochOffset)
	omm := OMM.OMMEnd(builder)
	OMM.FinishSizePrefixedOMMBuffer(builder, omm)
	return append([]byte(nil), builder.FinishedBytes()...)
}

type fixtureChunkHeader struct {
	Schema     string `json:"schema"`
	TotalCount int64  `json:"total_count"`
	Count      int    `json:"count"`
	Cursor     string `json:"cursor"`
	NextCursor string `json:"next_cursor"`
	SnapshotID string `json:"snapshot_id"`
	Head       string `json:"head"`
	Results    []struct {
		CID string `json:"cid"`
	} `json:"results"`
}

func (h fixtureChunkHeader) resultCIDs() []string {
	cids := make([]string, 0, len(h.Results))
	for _, result := range h.Results {
		cids = append(cids, result.CID)
	}
	return cids
}

func decodeFixtureChunkHeader(t *testing.T, response []byte) fixtureChunkHeader {
	t.Helper()
	var header fixtureChunkHeader
	readFlatSQLSyncTestJSONFrame(t, bytes.NewReader(response), &header)
	return header
}

// rawFramesSection strips the big-endian JSON header frame and returns the
// little-endian size-prefixed record stream that follows — the exact bytes
// WriteRawRecordFrames emitted.
func rawFramesSection(t *testing.T, response []byte) []byte {
	t.Helper()
	if len(response) < 4 {
		t.Fatalf("response too short for header frame")
	}
	headerLen := int(uint32(response[0])<<24 | uint32(response[1])<<16 | uint32(response[2])<<8 | uint32(response[3]))
	start := 4 + headerLen
	if start > len(response) {
		t.Fatalf("truncated header frame")
	}
	return append([]byte(nil), response[start:]...)
}

func decodeFixtureCursor(t *testing.T, cursor string) *fixtureCursorMeta {
	t.Helper()
	if cursor == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("cursor is not base64url: %v", err)
	}
	var parsed struct {
		Version    int    `json:"v"`
		Mode       string `json:"mode"`
		AfterRowID int64  `json:"after_row_id"`
		MaxRowID   int64  `json:"max_row_id"`
		SnapshotID string `json:"snapshot_id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("cursor is not JSON: %v", err)
	}
	return &fixtureCursorMeta{
		Encoded:    cursor,
		Version:    parsed.Version,
		Mode:       parsed.Mode,
		AfterRowID: parsed.AfterRowID,
		MaxRowID:   parsed.MaxRowID,
		SnapshotID: parsed.SnapshotID,
	}
}

func fixtureSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeFixtureFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write fixture %s failed: %v", name, err)
	}
}
