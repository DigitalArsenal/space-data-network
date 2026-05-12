package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestFlatSQLSyncProtocolReadChunkReturnsSnapshotMetadataAndFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store := newFlatSQLSyncTestStore(t)
	payload := storeFlatSQLSyncTestOMM(t, store, 56775, "STARLINK-6292")

	server, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("server libp2p failed: %v", err)
	}
	defer server.Close()
	client, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("client libp2p failed: %v", err)
	}
	defer client.Close()

	server.SetStreamHandler(FlatSQLSyncProtocolID, NewFlatSQLSyncHandler(store).HandleStream)
	client.Peerstore().AddAddrs(server.ID(), server.Addrs(), peerstore.PermanentAddrTTL)
	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	stream, err := client.NewStream(ctx, server.ID(), FlatSQLSyncProtocolID)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	defer stream.Close()

	writeFlatSQLSyncTestFrame(t, stream, map[string]interface{}{
		"op":          "read_chunk",
		"schema":      "OMM.fbs",
		"provider_id": "space-data-network-02",
		"source_name": "celestrak-gp",
		"limit":       1,
	})

	var header struct {
		Schema        string `json:"schema"`
		TotalCount    int64  `json:"total_count"`
		Count         int    `json:"count"`
		Cursor        string `json:"cursor"`
		SnapshotID    string `json:"snapshot_id"`
		Head          string `json:"head"`
		HighWaterMark string `json:"high_water_mark"`
		ScanHash      string `json:"scan_hash"`
		ChunkHash     string `json:"chunk_hash"`
		SyncProtocol  string `json:"sync_protocol"`
		Results       []struct {
			SchemaName string `json:"schema_name"`
			CID        string `json:"cid"`
			SizeBytes  int    `json:"size_bytes"`
		} `json:"results"`
	}
	readFlatSQLSyncTestJSONFrame(t, stream, &header)
	if header.Schema != "OMM.fbs" || header.TotalCount != 1 || header.Count != 1 {
		t.Fatalf("unexpected sync header: %+v", header)
	}
	if header.Cursor == "" || header.SnapshotID == "" || header.Head == "" || header.SnapshotID != header.Head {
		t.Fatalf("missing resumable snapshot metadata: %+v", header)
	}
	if header.HighWaterMark == "" || header.ScanHash == "" || header.ChunkHash != header.ScanHash {
		t.Fatalf("missing chunk binding metadata: %+v", header)
	}
	if header.SyncProtocol != FlatSQLSyncProtocolID {
		t.Fatalf("sync protocol = %q, want %q", header.SyncProtocol, FlatSQLSyncProtocolID)
	}
	if len(header.Results) != 1 || header.Results[0].SizeBytes != len(payload) {
		t.Fatalf("unexpected refs: %+v", header.Results)
	}
	records := readFlatSQLSyncTestRawFrames(t, stream)
	if len(records) != 1 || !bytes.Equal(records[0], payload) {
		t.Fatalf("raw frames did not match stored FlatBuffer payload")
	}
}

func TestFlatSQLSyncProtocolAckProgress(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	handler := NewFlatSQLSyncHandler(store)

	var out bytes.Buffer
	handler.handleAckProgress(&out, flatSQLSyncRequest{
		Op:         "ack_progress",
		Schema:     "OMM.fbs",
		SnapshotID: "snapshot-1",
		NextCursor: "cursor-2",
		ChunkHash:  "chunk-1",
		LocalRows:  25000,
	})

	var body struct {
		Status       string `json:"status"`
		SyncProtocol string `json:"sync_protocol"`
		SnapshotID   string `json:"snapshot_id"`
		NextCursor   string `json:"next_cursor"`
		LocalRows    int64  `json:"local_rows"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &body)
	if body.Status != "acknowledged" || body.SyncProtocol != FlatSQLSyncProtocolID || body.SnapshotID != "snapshot-1" || body.NextCursor != "cursor-2" || body.LocalRows != 25000 {
		t.Fatalf("unexpected ack body: %+v", body)
	}
}

func TestFlatSQLSyncProtocolReadChunkReusesProvidedSnapshotMetadata(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	payload := storeFlatSQLSyncTestOMM(t, store, 56775, "STARLINK-6292")
	handler := NewFlatSQLSyncHandler(store)

	var out bytes.Buffer
	if err := handler.handleReadChunk(&out, flatSQLSyncRequest{
		Op:            "read_chunk",
		Schema:        "OMM.fbs",
		ProviderID:    "space-data-network-02",
		SourceName:    "celestrak-gp",
		Limit:         1,
		SnapshotID:    "snapshot-fast-path",
		Head:          "snapshot-fast-path",
		HighWaterMark: "123:456:789:100",
		TotalCount:    100,
	}); err != nil {
		t.Fatalf("read chunk failed: %v", err)
	}

	var header struct {
		TotalCount    int64  `json:"total_count"`
		SnapshotID    string `json:"snapshot_id"`
		Head          string `json:"head"`
		HighWaterMark string `json:"high_water_mark"`
		Results       []struct {
			CID string `json:"cid"`
		} `json:"results"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &header)
	if header.TotalCount != 100 || header.SnapshotID != "snapshot-fast-path" || header.Head != "snapshot-fast-path" || header.HighWaterMark != "123:456:789:100" {
		t.Fatalf("snapshot metadata was not reused: %+v", header)
	}
	if len(header.Results) != 1 || header.Results[0].CID == "" {
		t.Fatalf("unexpected refs: %+v", header.Results)
	}
	records := readFlatSQLSyncTestRawFrames(t, &out)
	if len(records) != 1 || !bytes.Equal(records[0], payload) {
		t.Fatalf("raw frames did not match stored FlatBuffer payload")
	}
}

func TestFlatSQLSyncProtocolOpenManifestReturnsOrderedSegments(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	storeFlatSQLSyncTestOMM(t, store, 56775, "STARLINK-6292")
	storeFlatSQLSyncTestOMM(t, store, 25544, "ISS")
	handler := NewFlatSQLSyncHandler(store)

	var out bytes.Buffer
	if err := handler.handleOpenManifest(&out, flatSQLSyncRequest{
		Op:         "open_manifest",
		Schema:     "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Limit:      1,
	}); err != nil {
		t.Fatalf("open manifest failed: %v", err)
	}

	var body struct {
		ManifestID   string `json:"manifest_id"`
		Schema       string `json:"schema"`
		TotalCount   int64  `json:"total_count"`
		TotalBytes   int64  `json:"total_bytes"`
		Head         string `json:"head"`
		SyncProtocol string `json:"sync_protocol"`
		Segments     []struct {
			Index      int    `json:"index"`
			Cursor     string `json:"cursor"`
			NextCursor string `json:"next_cursor"`
			RowCount   int    `json:"row_count"`
			ByteCount  int64  `json:"byte_count"`
			ChunkHash  string `json:"chunk_hash"`
		} `json:"segments"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &body)

	if body.Schema != "OMM.fbs" || body.TotalCount != 2 || body.TotalBytes <= 0 || body.ManifestID == "" || body.Head == "" {
		t.Fatalf("unexpected manifest header: %+v", body)
	}
	if body.SyncProtocol != FlatSQLSyncProtocolID {
		t.Fatalf("sync protocol = %q, want %q", body.SyncProtocol, FlatSQLSyncProtocolID)
	}
	if len(body.Segments) != 2 {
		t.Fatalf("segments = %d, want 2: %+v", len(body.Segments), body.Segments)
	}
	if body.Segments[0].Index != 0 || body.Segments[0].RowCount != 1 || body.Segments[0].NextCursor == "" || body.Segments[0].ChunkHash == "" {
		t.Fatalf("unexpected first segment: %+v", body.Segments[0])
	}
	if body.Segments[1].Index != 1 || body.Segments[1].RowCount != 1 || body.Segments[1].NextCursor != "" || body.Segments[1].ChunkHash == "" {
		t.Fatalf("unexpected second segment: %+v", body.Segments[1])
	}
}

func TestFlatSQLSyncProtocolOpenManifestIncludesPublishedShardCIDs(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	storeFlatSQLSyncTestOMM(t, store, 56775, "STARLINK-6292")
	if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50000,
		RecordCount:  1,
		ByteCount:    2048,
		ShardCID:     "bafkshard",
		IndexCID:     "bafkindex",
		ManifestCID:  "bafkmanifest",
		ShardSHA256:  "shard-sha",
		ResultSHA256: "result-sha",
	}); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}
	handler := NewFlatSQLSyncHandler(store)

	var out bytes.Buffer
	if err := handler.handleOpenManifest(&out, flatSQLSyncRequest{
		Op:           "open_manifest",
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Limit:        50000,
	}); err != nil {
		t.Fatalf("open manifest failed: %v", err)
	}

	var body struct {
		QueryProfile string `json:"query_profile"`
		Segments     []struct {
			CID         string `json:"cid"`
			IndexCID    string `json:"index_cid"`
			ManifestCID string `json:"manifest_cid"`
			ShardSHA256 string `json:"shard_sha256"`
			ChunkHash   string `json:"chunk_hash"`
		} `json:"segments"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &body)
	if body.QueryProfile != storage.DatasetPublicationQueryProfile {
		t.Fatalf("query profile = %q, want %q", body.QueryProfile, storage.DatasetPublicationQueryProfile)
	}
	if len(body.Segments) != 1 {
		t.Fatalf("segments = %d, want 1: %+v", len(body.Segments), body.Segments)
	}
	segment := body.Segments[0]
	if segment.CID != "bafkshard" || segment.IndexCID != "bafkindex" || segment.ManifestCID != "bafkmanifest" {
		t.Fatalf("published CIDs missing from manifest segment: %+v", segment)
	}
	if segment.ShardSHA256 != "shard-sha" || segment.ChunkHash != "result-sha" {
		t.Fatalf("published hashes missing from manifest segment: %+v", segment)
	}
}

func newFlatSQLSyncTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "sdn-flatsql-sync-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func storeFlatSQLSyncTestOMM(t *testing.T, store *storage.FlatSQLStore, norad uint32, objectName string) []byte {
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
	payload := append([]byte(nil), builder.FinishedBytes()...)
	_, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:celestrak", nil, storage.SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		BatchID:    "test-batch",
	})
	if err != nil {
		t.Fatalf("StoreWithSourceTags failed: %v", err)
	}
	return payload
}

func writeFlatSQLSyncTestFrame(t *testing.T, writer io.Writer, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal sync frame failed: %v", err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := writer.Write(header[:]); err != nil {
		t.Fatalf("write sync frame header failed: %v", err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("write sync frame payload failed: %v", err)
	}
}

func readFlatSQLSyncTestJSONFrame(t *testing.T, reader io.Reader, target any) {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		t.Fatalf("read sync JSON frame header failed: %v", err)
	}
	length := binary.BigEndian.Uint32(header[:])
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("read sync JSON frame payload failed: %v", err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("decode sync JSON frame failed: %v", err)
	}
}

func readFlatSQLSyncTestRawFrames(t *testing.T, reader io.Reader) [][]byte {
	t.Helper()
	var records [][]byte
	for {
		var header [4]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return records
			}
			t.Fatalf("read raw frame header failed: %v", err)
		}
		length := binary.LittleEndian.Uint32(header[:])
		payload := make([]byte, length)
		if _, err := io.ReadFull(reader, payload); err != nil {
			t.Fatalf("read raw frame payload failed: %v", err)
		}
		records = append(records, payload)
	}
}
