//go:build stress
// +build stress

package protocol

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const flatSQLSync256GiBBytes int64 = 256 * 1024 * 1024 * 1024

func TestFlatSQLSyncProtocolStreamsPublishedShard256GB(t *testing.T) {
	if strings.TrimSpace(os.Getenv("STRESS_LIVE_FLATSQL_256GB")) != "1" {
		t.Skip("set STRESS_LIVE_FLATSQL_256GB=1 to stream the full 256 GiB payload")
	}

	store := newFlatSQLSyncTestStore(t)
	pub := storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "test-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        1,
		RecordCount:  1,
		ByteCount:    flatSQLSync256GiBBytes,
		ShardCID:     "bafk256gibstreamshard",
		IndexCID:     "bafk256gibstreamindex",
		ManifestCID:  "bafk256gibstreammanifest",
		ShardSHA256:  strings.Repeat("2", 64),
		IndexSHA256:  strings.Repeat("3", 64),
		QuerySHA256:  strings.Repeat("1", 64),
		ResultSHA256: strings.Repeat("2", 64),
		PublishedAt:  time.Now().UTC(),
	}
	if err := store.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}
	writeSparsePublishedShard(t, store, pub)

	handler := NewFlatSQLSyncHandler(store)
	reader, writer := io.Pipe()
	defer reader.Close()

	errCh := make(chan error, 1)
	go func() {
		err := handler.handleReadPublishedShard(writer, flatSQLSyncRequest{
			Op:           "read_published_shard",
			Schema:       pub.SchemaName,
			ProviderID:   pub.ProviderID,
			SourceName:   pub.SourceName,
			BatchID:      pub.BatchID,
			QueryProfile: pub.QueryProfile,
			CID:          pub.ShardCID,
		})
		if err != nil {
			_ = writer.CloseWithError(err)
			errCh <- err
			return
		}
		errCh <- writer.Close()
	}()

	var header struct {
		Op             string `json:"op"`
		Status         string `json:"status"`
		SyncProtocol   string `json:"sync_protocol"`
		CID            string `json:"cid"`
		ByteCount      int64  `json:"byte_count"`
		TotalByteCount int64  `json:"total_byte_count"`
		PayloadFormat  string `json:"payload_format"`
		ImmutableBytes bool   `json:"immutable_bytes"`
	}
	readFlatSQLSyncTestJSONFrame(t, reader, &header)
	if header.Op != "read_published_shard" || header.Status != "ok" || header.SyncProtocol != FlatSQLSyncProtocolID {
		t.Fatalf("unexpected published shard stream header: %+v", header)
	}
	if header.CID != pub.ShardCID || header.ByteCount != flatSQLSync256GiBBytes || header.TotalByteCount != flatSQLSync256GiBBytes {
		t.Fatalf("unexpected 256 GiB stream sizing: %+v", header)
	}
	if header.PayloadFormat != "flatsql-size-prefixed-flatbuffers" || !header.ImmutableBytes {
		t.Fatalf("unexpected 256 GiB stream payload metadata: %+v", header)
	}

	started := time.Now()
	streamedBytes, err := io.CopyN(io.Discard, reader, header.ByteCount)
	if err != nil {
		t.Fatalf("stream 256 GiB published shard payload failed after %d bytes: %v", streamedBytes, err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error after streaming 256 GiB: %v", err)
	}
	if streamedBytes != flatSQLSync256GiBBytes {
		t.Fatalf("streamed %d bytes, want %d", streamedBytes, flatSQLSync256GiBBytes)
	}
	duration := time.Since(started)
	bytesPerSecond := float64(streamedBytes) / duration.Seconds()
	if required, ok := configured256GiBWireSpeedRequirement(); ok && bytesPerSecond < required {
		t.Fatalf("stream speed %.2f MiB/s is below configured 99%% link gate %.2f MiB/s", bytesPerSecond/(1024*1024), required/(1024*1024))
	}
	t.Logf("streamed %.2f GiB in %s (%.2f MiB/s)", float64(streamedBytes)/(1024*1024*1024), duration, bytesPerSecond/(1024*1024))
}

func writeSparsePublishedShard(t *testing.T, store *storage.FlatSQLStore, pub storage.DatasetShardPublication) {
	t.Helper()
	shardPath, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		t.Fatalf("DatasetPublicationShardPath failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(shardPath), 0o700); err != nil {
		t.Fatalf("MkdirAll shard dir failed: %v", err)
	}
	file, err := os.OpenFile(shardPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create sparse published shard failed: %v", err)
	}
	if err := file.Truncate(pub.ByteCount); err != nil {
		_ = file.Close()
		t.Fatalf("truncate sparse published shard failed: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close sparse published shard failed: %v", err)
	}
}

func configured256GiBWireSpeedRequirement() (float64, bool) {
	if strings.TrimSpace(os.Getenv("SDN_WIRESPEED_TEST")) != "1" {
		return 0, false
	}
	linkGBit := strings.TrimSpace(os.Getenv("SDN_TEST_LINK_GBIT"))
	if linkGBit == "" {
		return 0, false
	}
	gbits, err := strconv.ParseFloat(linkGBit, 64)
	if err != nil || gbits <= 0 {
		return 0, false
	}
	return gbits * 1_000_000_000 / 8 * 0.99, true
}
