package protocol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	// NO wall-clock deadline on the dial. A loaded box must not decide this
	// test: measured 2026-08-14, this test passed in ~1.3s alone and FAILED
	// inside the parallel package run at load ~18 — the 10s context expired
	// while the scheduler starved the test goroutine, so the verdict measured
	// the machine, not the sync protocol. A broken dial surfaces as a
	// Connect/NewStream error (libp2p bounds its own dial attempts); a
	// genuinely hung test is caught by go test's own -timeout, the gate's
	// deadlock guard at the scale where a budget means something.
	// See gauntlet-go-host-tier-tests-fail-under-machine-load.
	ctx := context.Background()

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

func TestFlatSQLSyncProtocolReadChunkCanRouteRegisteredDatastoreNamespace(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-flatsql-sync-namespace-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	rootStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { _ = rootStore.Close() })
	storeFlatSQLSyncTestOMM(t, rootStore, 1, "ROOT-ONLY")

	identity := storage.DatastoreIdentity{
		SchemaName:      "OMM.fbs",
		SourcePeerID:    "source:history",
		SourcePublicKey: "history-public-key",
		ProviderID:      "space-data-network-02",
		SourceName:      "celestrak-gp-historical",
	}
	datastoreKey, err := identity.Key()
	if err != nil {
		t.Fatalf("identity key failed: %v", err)
	}
	namespaceStore, err := storage.NewFlatSQLStoreForIdentity(filepath.Join(tmpDir, "db"), validator, identity)
	if err != nil {
		t.Fatalf("NewFlatSQLStoreForIdentity failed: %v", err)
	}
	namespacePayload := storeFlatSQLSyncTestOMM(t, namespaceStore, 56775, "NAMESPACE-ONLY")
	if err := namespaceStore.Close(); err != nil {
		t.Fatalf("namespace close failed: %v", err)
	}

	handler := NewFlatSQLSyncHandler(rootStore)
	var out bytes.Buffer
	if err := handler.handleReadChunk(&out, flatSQLSyncRequest{
		Op:           "read_chunk",
		DatastoreKey: datastoreKey,
		Schema:       "OMM.fbs",
		Limit:        1,
	}); err != nil {
		t.Fatalf("handleReadChunk failed: %v", err)
	}

	var header struct {
		TotalCount int `json:"total_count"`
		Count      int `json:"count"`
		Results    []struct {
			CID string `json:"cid"`
		} `json:"results"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &header)
	if header.TotalCount != 1 || header.Count != 1 {
		t.Fatalf("expected only namespace rows, got %+v", header)
	}
	records := readFlatSQLSyncTestRawFrames(t, &out)
	if len(records) != 1 || !bytes.Equal(records[0], namespacePayload) {
		t.Fatalf("raw frames did not come from the requested datastore namespace")
	}
}

func TestFlatSQLSyncProtocolListDatastoresReturnsRegisteredNamespaces(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-flatsql-sync-list-datastores-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	rootStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { _ = rootStore.Close() })
	identity := storage.DatastoreIdentity{
		SchemaName:      "OMM.fbs",
		SourcePeerID:    "source:history",
		SourcePublicKey: "history-public-key",
		ProviderID:      "space-data-network-02",
		SourceName:      "celestrak-gp-historical",
	}
	datastoreKey, err := identity.Key()
	if err != nil {
		t.Fatalf("identity key failed: %v", err)
	}
	namespaceStore, err := storage.NewFlatSQLStoreForIdentity(filepath.Join(tmpDir, "db"), validator, identity)
	if err != nil {
		t.Fatalf("NewFlatSQLStoreForIdentity failed: %v", err)
	}
	if err := namespaceStore.Close(); err != nil {
		t.Fatalf("namespace close failed: %v", err)
	}

	handler := NewFlatSQLSyncHandler(rootStore)
	var out bytes.Buffer
	if err := handler.handleListDatastores(&out); err != nil {
		t.Fatalf("handleListDatastores failed: %v", err)
	}

	var body struct {
		Count   int `json:"count"`
		Results []struct {
			Key      string `json:"key"`
			Identity struct {
				SchemaName      string `json:"schema_name"`
				SourcePublicKey string `json:"source_public_key"`
				ProviderID      string `json:"provider_id"`
				SourceName      string `json:"source_name"`
			} `json:"identity"`
		} `json:"results"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &body)
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("unexpected datastore list body: %+v", body)
	}
	if body.Results[0].Key != datastoreKey || body.Results[0].Identity.SourceName != "celestrak-gp-historical" {
		t.Fatalf("datastore identity not returned: %+v", body.Results[0])
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

func TestFlatSQLSyncProtocolWireSpeedProbeStreamsRequestedBytes(t *testing.T) {
	// No wall-clock deadline on the dial — a loaded box must not decide this
	// test (same measurement + reasoning as the read-chunk test above: ~1.3s
	// alone, FAIL at load ~18 under the old 10s context).
	ctx := context.Background()

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

	server.SetStreamHandler(FlatSQLSyncProtocolID, NewFlatSQLSyncHandler(nil).HandleStream)
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
		"op":          "wire_speed_probe",
		"probe_bytes": 32 * 1024,
	})

	var header struct {
		Op           string `json:"op"`
		Status       string `json:"status"`
		SyncProtocol string `json:"sync_protocol"`
		ProbeBytes   int64  `json:"probe_bytes"`
		PayloadBytes int64  `json:"payload_bytes"`
	}
	readFlatSQLSyncTestJSONFrame(t, stream, &header)
	if header.Op != "wire_speed_probe" || header.Status != "ok" || header.SyncProtocol != FlatSQLSyncProtocolID {
		t.Fatalf("unexpected probe header: %+v", header)
	}
	if header.ProbeBytes != 32*1024 || header.PayloadBytes != 32*1024 {
		t.Fatalf("probe size header = %+v, want 32768 bytes", header)
	}
	payload, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read probe payload failed: %v", err)
	}
	if len(payload) != 32*1024 {
		t.Fatalf("probe payload length = %d, want %d", len(payload), 32*1024)
	}
	if bytes.Count(payload, []byte{0}) == len(payload) {
		t.Fatalf("probe payload should be deterministic bytes, not all zeroes")
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

func TestFlatSQLSyncProtocolOpenManifestIncludesPartialPublishedShardCIDs(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	storeFlatSQLSyncTestOMM(t, store, 56775, "STARLINK-6292")
	if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        1,
		RecordCount:  1,
		ByteCount:    2048,
		ShardCID:     "bafkpartialshard",
		IndexCID:     "bafkpartialindex",
		ManifestCID:  "bafkpartialmanifest",
		ShardSHA256:  "partial-shard-sha",
		ResultSHA256: "partial-result-sha",
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
		Segments []struct {
			CID       string `json:"cid"`
			ChunkHash string `json:"chunk_hash"`
		} `json:"segments"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &body)
	if len(body.Segments) != 1 {
		t.Fatalf("segments = %d, want 1: %+v", len(body.Segments), body.Segments)
	}
	if body.Segments[0].CID != "bafkpartialshard" || body.Segments[0].ChunkHash != "partial-result-sha" {
		t.Fatalf("partial published CID missing from manifest segment: %+v", body.Segments[0])
	}
}

func TestFlatSQLSyncProtocolOpenManifestUsesPublishedShardLayoutWhenRequestLimitDiffers(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	for _, norad := range []uint32{10001, 10002, 10003, 10004} {
		storeFlatSQLSyncTestOMM(t, store, norad, "OBJECT")
	}
	if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        4,
		RecordCount:  4,
		ByteCount:    8192,
		ShardCID:     "bafklargeshard",
		IndexCID:     "bafklargeindex",
		ManifestCID:  "bafklargemanifest",
		ShardSHA256:  "large-shard-sha",
		ResultSHA256: "large-result-sha",
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
		Limit:        2,
	}); err != nil {
		t.Fatalf("open manifest failed: %v", err)
	}

	var body struct {
		Segments []struct {
			RowCount int    `json:"row_count"`
			CID      string `json:"cid"`
		} `json:"segments"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &body)
	if len(body.Segments) != 1 {
		t.Fatalf("segments = %d, want published layout segment: %+v", len(body.Segments), body.Segments)
	}
	if body.Segments[0].RowCount != 4 || body.Segments[0].CID != "bafklargeshard" {
		t.Fatalf("manifest did not use published shard layout: %+v", body.Segments[0])
	}
}

func TestFlatSQLSyncProtocolReadPublishedShardStreamsProviderFlatSQLShardFile(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	shardBytes := []byte{
		0x05, 0x00, 0x00, 0x00, 'O', 'M', 'M', '-', '1',
		0x05, 0x00, 0x00, 0x00, 'O', 'M', 'M', '-', '2',
	}
	shardSum := sha256.Sum256(shardBytes)
	querySum := sha256.Sum256([]byte("OMM test publication query"))
	pub := storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50000,
		RecordCount:  2,
		ByteCount:    int64(len(shardBytes)),
		ShardCID:     "bafkpublishedshard",
		IndexCID:     "bafkpublishedindex",
		ManifestCID:  "bafkpublishedmanifest",
		ShardSHA256:  hex.EncodeToString(shardSum[:]),
		QuerySHA256:  hex.EncodeToString(querySum[:]),
		ResultSHA256: hex.EncodeToString(shardSum[:]),
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

	handler := NewFlatSQLSyncHandler(store)
	var out bytes.Buffer
	if err := handler.handleReadPublishedShard(&out, flatSQLSyncRequest{
		Op:           "read_published_shard",
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		CID:          "bafkpublishedshard",
	}); err != nil {
		t.Fatalf("handleReadPublishedShard failed: %v", err)
	}

	var header struct {
		Op           string `json:"op"`
		Status       string `json:"status"`
		Schema       string `json:"schema"`
		CID          string `json:"cid"`
		RowCount     int    `json:"row_count"`
		ByteCount    int64  `json:"byte_count"`
		ShardSHA256  string `json:"shard_sha256"`
		SyncProtocol string `json:"sync_protocol"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &header)
	if header.Op != "read_published_shard" || header.Status != "ok" || header.Schema != "OMM.fbs" || header.CID != "bafkpublishedshard" {
		t.Fatalf("unexpected published shard header: %+v", header)
	}
	if header.RowCount != 2 || header.ByteCount != int64(len(shardBytes)) || header.ShardSHA256 != pub.ShardSHA256 || header.SyncProtocol != FlatSQLSyncProtocolID {
		t.Fatalf("published shard metadata mismatch: %+v", header)
	}
	if got := out.Bytes(); !bytes.Equal(got, shardBytes) {
		t.Fatalf("published shard payload mismatch: got %d bytes, want %d", len(got), len(shardBytes))
	}
}

func TestFlatSQLSyncProtocolReadPublishedShardStreamsRequestedByteRange(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	shardBytes := []byte("0123456789abcdef")
	shardSum := sha256.Sum256(shardBytes)
	querySum := sha256.Sum256([]byte("OMM range publication query"))
	pub := storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50000,
		RecordCount:  2,
		ByteCount:    int64(len(shardBytes)),
		ShardCID:     "bafkrangedpublishedshard",
		IndexCID:     "bafkrangedpublishedindex",
		ManifestCID:  "bafkrangedpublishedmanifest",
		ShardSHA256:  hex.EncodeToString(shardSum[:]),
		QuerySHA256:  hex.EncodeToString(querySum[:]),
		ResultSHA256: hex.EncodeToString(shardSum[:]),
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

	handler := NewFlatSQLSyncHandler(store)
	var out bytes.Buffer
	if err := handler.handleReadPublishedShard(&out, flatSQLSyncRequest{
		Op:           "read_published_shard",
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		CID:          "bafkrangedpublishedshard",
		ByteOffset:   4,
		ByteLength:   6,
	}); err != nil {
		t.Fatalf("handleReadPublishedShard range failed: %v", err)
	}

	var header struct {
		Op             string `json:"op"`
		Status         string `json:"status"`
		CID            string `json:"cid"`
		ByteOffset     int64  `json:"byte_offset"`
		ByteLength     int64  `json:"byte_length"`
		ByteCount      int64  `json:"byte_count"`
		TotalByteCount int64  `json:"total_byte_count"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &header)
	if header.Op != "read_published_shard" || header.Status != "ok" || header.CID != "bafkrangedpublishedshard" {
		t.Fatalf("unexpected range header: %+v", header)
	}
	if header.ByteOffset != 4 || header.ByteLength != 6 || header.ByteCount != 6 || header.TotalByteCount != int64(len(shardBytes)) {
		t.Fatalf("range metadata mismatch: %+v", header)
	}
	if got, want := out.Bytes(), shardBytes[4:10]; !bytes.Equal(got, want) {
		t.Fatalf("published shard range payload mismatch: got %q, want %q", got, want)
	}
}

func TestFlatSQLSyncProtocolReadPublishedAssetStreamsIndexSidecar(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	shardBytes := []byte{0x05, 0x00, 0x00, 0x00, 'O', 'M', 'M', '-', '1'}
	indexBytes := []byte(`{"version":1,"schemaName":"OMM.fbs","recordCount":1}` + "\n")
	shardSum := sha256.Sum256(shardBytes)
	indexSum := sha256.Sum256(indexBytes)
	querySum := sha256.Sum256([]byte("OMM asset publication query"))
	pub := storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50000,
		RecordCount:  1,
		ByteCount:    int64(len(shardBytes)),
		ShardCID:     "bafkassetpublishedshard",
		IndexCID:     "bafkassetpublishedindex",
		ManifestCID:  "bafkassetpublishedmanifest",
		ShardSHA256:  hex.EncodeToString(shardSum[:]),
		IndexSHA256:  hex.EncodeToString(indexSum[:]),
		QuerySHA256:  hex.EncodeToString(querySum[:]),
		ResultSHA256: hex.EncodeToString(shardSum[:]),
	}
	if err := store.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}
	shardPath, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		t.Fatalf("DatasetPublicationShardPath failed: %v", err)
	}
	indexPath, err := store.DatasetPublicationIndexPath(pub)
	if err != nil {
		t.Fatalf("DatasetPublicationIndexPath failed: %v", err)
	}
	for path, data := range map[string][]byte{
		shardPath: shardBytes,
		indexPath: indexBytes,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll %s failed: %v", path, err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("WriteFile %s failed: %v", path, err)
		}
	}

	handler := NewFlatSQLSyncHandler(store)
	var out bytes.Buffer
	if err := handler.handleReadPublishedAsset(&out, flatSQLSyncRequest{
		Op:           "read_published_asset",
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		CID:          "bafkassetpublishedindex",
		AssetRole:    "index",
	}); err != nil {
		t.Fatalf("handleReadPublishedAsset failed: %v", err)
	}

	var header struct {
		Op             string `json:"op"`
		Status         string `json:"status"`
		Schema         string `json:"schema"`
		Role           string `json:"role"`
		CID            string `json:"cid"`
		ByteCount      int64  `json:"byte_count"`
		SHA256         string `json:"sha256"`
		SyncProtocol   string `json:"sync_protocol"`
		ImmutableBytes bool   `json:"immutable_bytes"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &header)
	if header.Op != "read_published_asset" || header.Status != "ok" || header.Schema != "OMM.fbs" || header.Role != "index" || header.CID != "bafkassetpublishedindex" {
		t.Fatalf("unexpected published asset header: %+v", header)
	}
	if header.ByteCount != int64(len(indexBytes)) || header.SHA256 != pub.IndexSHA256 || header.SyncProtocol != FlatSQLSyncProtocolID || !header.ImmutableBytes {
		t.Fatalf("published asset metadata mismatch: %+v", header)
	}
	if got := out.Bytes(); !bytes.Equal(got, indexBytes) {
		t.Fatalf("published asset payload mismatch: got %d bytes, want %d", len(got), len(indexBytes))
	}
}

func TestFlatSQLSyncProtocolListPublishedShardsReturnsPublicationIndex(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	for index, item := range []struct {
		cid         string
		indexCID    string
		recordCount int
	}{
		{cid: "bafkfirstpublishedshard", indexCID: "bafkfirstpublishedindex", recordCount: 10},
		{cid: "bafksecondpublishedshard", indexCID: "bafksecondpublishedindex", recordCount: 20},
	} {
		pub := storage.DatasetShardPublication{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "test-batch",
			QueryProfile: storage.DatasetPublicationQueryProfile,
			Offset:       index * 50000,
			Limit:        50000,
			RecordCount:  item.recordCount,
			ByteCount:    int64(1000 + index),
			ShardCID:     item.cid,
			IndexCID:     item.indexCID,
			ManifestCID:  "bafkpublishedmanifest",
			PNMCID:       "bafkpublishedpnm",
			ShardSHA256:  "1111111111111111111111111111111111111111111111111111111111111111",
			IndexSHA256:  "2222222222222222222222222222222222222222222222222222222222222222",
			QuerySHA256:  "3333333333333333333333333333333333333333333333333333333333333333",
			ResultSHA256: "1111111111111111111111111111111111111111111111111111111111111111",
			FeedSequence: int64(index + 1),
			PreviousHead: "previous-head",
			FeedHead:     "feed-head",
			PublishedAt:  time.Unix(1700000000+int64(index), 0).UTC(),
		}
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
		}
		shardPath, err := store.DatasetPublicationShardPath(pub)
		if err != nil {
			t.Fatalf("DatasetPublicationShardPath failed: %v", err)
		}
		indexPath, err := store.DatasetPublicationIndexPath(pub)
		if err != nil {
			t.Fatalf("DatasetPublicationIndexPath failed: %v", err)
		}
		for path, payload := range map[string][]byte{
			shardPath: []byte(item.cid),
			indexPath: []byte(item.indexCID),
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("MkdirAll %s failed: %v", path, err)
			}
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatalf("WriteFile %s failed: %v", path, err)
			}
		}
	}

	handler := NewFlatSQLSyncHandler(store)
	var out bytes.Buffer
	if err := handler.handleListPublishedShards(&out, flatSQLSyncRequest{
		Op:                "list_published_shards",
		Schema:            "OMM.fbs",
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "test-batch",
		QueryProfile:      storage.DatasetPublicationQueryProfile,
		PublicationOffset: 1,
		PublicationLimit:  1,
	}); err != nil {
		t.Fatalf("handleListPublishedShards failed: %v", err)
	}

	var header struct {
		Op                    string `json:"op"`
		Status                string `json:"status"`
		Schema                string `json:"schema"`
		SyncProtocol          string `json:"sync_protocol"`
		PublicationOffset     int    `json:"publication_offset"`
		PublicationCount      int    `json:"publication_count"`
		TotalPublicationCount int    `json:"total_publication_count"`
		Publications          []struct {
			Schema       string    `json:"schema"`
			ProviderID   string    `json:"provider_id"`
			SourceName   string    `json:"source_name"`
			BatchID      string    `json:"batch_id"`
			QueryProfile string    `json:"query_profile"`
			Offset       int       `json:"offset"`
			Limit        int       `json:"limit"`
			RecordCount  int       `json:"record_count"`
			ByteCount    int64     `json:"byte_count"`
			ShardCID     string    `json:"shard_cid"`
			IndexCID     string    `json:"index_cid"`
			FeedSequence int64     `json:"feed_sequence"`
			PublishedAt  time.Time `json:"published_at"`
		} `json:"publications"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &header)
	if header.Op != "list_published_shards" || header.Status != "ok" || header.Schema != "OMM.fbs" || header.SyncProtocol != FlatSQLSyncProtocolID {
		t.Fatalf("unexpected publication list header: %+v", header)
	}
	if header.PublicationOffset != 1 || header.PublicationCount != 1 || header.TotalPublicationCount != 2 {
		t.Fatalf("unexpected publication paging: %+v", header)
	}
	if len(header.Publications) != 1 {
		t.Fatalf("publications = %d, want 1", len(header.Publications))
	}
	got := header.Publications[0]
	if got.ShardCID != "bafksecondpublishedshard" || got.IndexCID != "bafksecondpublishedindex" || got.Offset != 50000 || got.RecordCount != 20 {
		t.Fatalf("unexpected publication listing: %+v", got)
	}
	if got.ProviderID != "space-data-network-02" || got.SourceName != "celestrak-gp" || got.BatchID != "test-batch" || got.QueryProfile != storage.DatasetPublicationQueryProfile {
		t.Fatalf("publication identity was not preserved: %+v", got)
	}
	if got.PublishedAt.IsZero() {
		t.Fatalf("published_at was not preserved: %+v", got)
	}
}

func TestFlatSQLSyncProtocolReadPublishedShardBatchStreamsConcatenatedFlatSQLShardFiles(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	firstShardBytes := []byte{0x05, 0x00, 0x00, 0x00, 'O', 'M', 'M', '-', '1'}
	secondShardBytes := []byte{0x05, 0x00, 0x00, 0x00, 'O', 'M', 'M', '-', '2'}
	for index, item := range []struct {
		cid   string
		bytes []byte
	}{
		{cid: "bafkfirstpublishedshard", bytes: firstShardBytes},
		{cid: "bafksecondpublishedshard", bytes: secondShardBytes},
	} {
		shardSum := sha256.Sum256(item.bytes)
		querySum := sha256.Sum256([]byte(item.cid + " query"))
		pub := storage.DatasetShardPublication{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "test-batch",
			QueryProfile: storage.DatasetPublicationQueryProfile,
			Offset:       index,
			Limit:        50000,
			RecordCount:  1,
			ByteCount:    int64(len(item.bytes)),
			ShardCID:     item.cid,
			IndexCID:     item.cid + "-index",
			ManifestCID:  "bafkpublishedmanifest",
			ShardSHA256:  hex.EncodeToString(shardSum[:]),
			QuerySHA256:  hex.EncodeToString(querySum[:]),
			ResultSHA256: hex.EncodeToString(shardSum[:]),
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
		if err := os.WriteFile(shardPath, item.bytes, 0o600); err != nil {
			t.Fatalf("WriteFile shard failed: %v", err)
		}
	}

	handler := NewFlatSQLSyncHandler(store)
	var out bytes.Buffer
	if err := handler.handleReadPublishedShardBatch(&out, flatSQLSyncRequest{
		Op:           "read_published_shard_batch",
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		CIDs:         []string{"bafkfirstpublishedshard", "bafksecondpublishedshard"},
	}); err != nil {
		t.Fatalf("handleReadPublishedShardBatch failed: %v", err)
	}

	var header struct {
		Op            string `json:"op"`
		Status        string `json:"status"`
		Schema        string `json:"schema"`
		SyncProtocol  string `json:"sync_protocol"`
		PayloadFormat string `json:"payload_format"`
		Shards        []struct {
			CID       string `json:"cid"`
			ByteCount int64  `json:"byte_count"`
		} `json:"shards"`
	}
	readFlatSQLSyncTestJSONFrame(t, &out, &header)
	if header.Op != "read_published_shard_batch" || header.Status != "ok" || header.Schema != "OMM.fbs" || header.SyncProtocol != FlatSQLSyncProtocolID {
		t.Fatalf("unexpected published shard batch header: %+v", header)
	}
	if header.PayloadFormat != "concatenated-flatsql-size-prefixed-flatbuffers" {
		t.Fatalf("unexpected payload format: %q", header.PayloadFormat)
	}
	if len(header.Shards) != 2 || header.Shards[0].CID != "bafkfirstpublishedshard" || header.Shards[1].CID != "bafksecondpublishedshard" {
		t.Fatalf("unexpected shard metadata: %+v", header.Shards)
	}
	if header.Shards[0].ByteCount != int64(len(firstShardBytes)) || header.Shards[1].ByteCount != int64(len(secondShardBytes)) {
		t.Fatalf("unexpected shard byte counts: %+v", header.Shards)
	}
	wantPayload := append(append([]byte(nil), firstShardBytes...), secondShardBytes...)
	if got := out.Bytes(); !bytes.Equal(got, wantPayload) {
		t.Fatalf("published shard batch payload mismatch: got %d bytes, want %d", len(got), len(wantPayload))
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
