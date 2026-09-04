package storefront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// The daemon's publisher pins through Kubo and reports Kubo's CID with the
// shard file's own SHA-256 and length (PUB-03).
func TestKuboListingDatasetPublisherReportsKuboCIDAndShardHash(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/add" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		if q.Get("cid-version") != "1" || q.Get("raw-leaves") != "true" {
			http.Error(w, "add must ask for CIDv1 raw leaves", http.StatusBadRequest)
			return
		}
		calls = append(calls, r.URL.RawQuery)
		hash := "bafykuboshard"
		if len(calls) > 1 {
			hash = "bafykuboindex"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"Name": "x", "Hash": hash, "Size": "1"})
	}))
	defer server.Close()

	// Real OMM records: the export pipeline indexes every record's fields.
	var records []storage.DatasetExportRecord
	for i := 0; i < 2; i++ {
		data := sds.NewOMMBuilder().
			WithNoradCatID(uint32(44000 + i)).
			WithObjectID("PIN-" + hex.EncodeToString([]byte{byte(i)})).
			WithEpoch(time.Unix(1700000000+int64(i), 0).UTC().Format(time.RFC3339)).
			Build()
		records = append(records, storage.DatasetExportRecord{CID: storage.ComputeCID(data), Data: data})
	}
	publisher := &KuboListingDatasetPublisher{IPFSAPIURL: server.URL, OutputDir: t.TempDir()}
	asset, err := publisher.PublishListingDataset(context.Background(), "listing-1", "1700000000", storage.IndexedRecordQuery{SchemaName: "OMM.fbs"}, records)
	if err != nil {
		// The export needs a schema the index extractor knows; OMM is one.
		t.Fatalf("PublishListingDataset: %v", err)
	}
	if asset.CID != "bafykuboshard" || asset.IndexCID != "bafykuboindex" {
		t.Fatalf("asset CIDs = %s / %s, want Kubo's", asset.CID, asset.IndexCID)
	}
	shard, err := os.ReadFile(asset.ShardPath)
	if err != nil {
		t.Fatalf("shard not on disk: %v", err)
	}
	sum := sha256.Sum256(shard)
	if asset.SHA256 != hex.EncodeToString(sum[:]) || asset.ByteLength != uint64(len(shard)) {
		t.Fatalf("asset hash/length = %s/%d, want the shard file's %s/%d", asset.SHA256, asset.ByteLength, hex.EncodeToString(sum[:]), len(shard))
	}
	if len(calls) != 2 {
		t.Fatalf("Kubo add calls = %d, want 2 (shard, index)", len(calls))
	}

	if _, err := (&KuboListingDatasetPublisher{OutputDir: t.TempDir()}).PublishListingDataset(context.Background(), "l", "1", storage.IndexedRecordQuery{SchemaName: "OMM.fbs"}, records); err == nil || !strings.Contains(err.Error(), "Kubo") {
		t.Fatalf("publisher without an API URL: err = %v, want a refusal naming Kubo", err)
	}
}
