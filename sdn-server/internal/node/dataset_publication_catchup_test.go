package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestMaterializeStoredDatasetPublicationPNMsReplaysTrustedProviderPNM(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	providerStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("provider store: %v", err)
	}
	defer providerStore.Close()
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("subscriber store: %v", err)
	}
	defer subscriberStore.Close()

	priv, pub, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x44}, 128)))
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	rawPriv, err := priv.Raw()
	if err != nil {
		t.Fatalf("raw private key: %v", err)
	}
	rawPub, err := pub.Raw()
	if err != nil {
		t.Fatalf("raw public key: %v", err)
	}
	if len(rawPriv) != ed25519.PrivateKeySize || len(rawPub) != ed25519.PublicKeySize {
		t.Fatalf("unexpected ed25519 key sizes: private=%d public=%d", len(rawPriv), len(rawPub))
	}
	providerID, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey failed: %v", err)
	}

	tags := storage.SourceTags{
		ProviderID:   "celestrak.eth",
		SourceName:   "celestrak-satcat-csv",
		SourceURL:    "https://celestrak.org/satcat/records.php?GROUP=active&FORMAT=CSV",
		BatchID:      "batch-sha-001",
		ContentKeyID: "public",
	}
	record := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	if _, err := providerStore.StoreWithSourceTags("CAT.fbs", record, providerID.String(), nil, tags); err != nil {
		t.Fatalf("store provider record: %v", err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), storage.IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          "celestrak.eth",
		SourceName:          "celestrak-satcat-csv",
		BatchID:             "batch-sha-001",
		Limit:               10,
		AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	publishedAt := time.Unix(1700002222, 0).UTC()
	manifest, err := storage.BuildSignedDatasetPublicationManifest(filepath.Join(tmpDir, "publish"), storage.DatasetPublicationManifestOptions{
		Export:         export,
		DatasetID:      "cat-catchup",
		UpdateID:       "batch-sha-001",
		ProviderPeerID: providerID.String(),
		ProviderEPMCID: "bafy-provider-epm",
		PublishedAt:    publishedAt,
		SigningKey:     ed25519.PrivateKey(rawPriv),
		SchemaHash:     "cat-schema-hash",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest failed: %v", err)
	}
	pnmBytes, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{
		PublishedAt: publishedAt,
		SigningKey:  ed25519.PrivateKey(rawPriv),
	})
	if err != nil {
		t.Fatalf("BuildDatasetPublicationPNM failed: %v", err)
	}
	shardBytes := mustReadTestFile(t, export.ShardPath)
	indexBytes := mustReadTestFile(t, export.IndexPath)
	objects := map[string][]byte{
		manifest.CID:    manifest.Bytes,
		export.ShardCID: shardBytes,
		export.IndexCID: indexBytes,
	}
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/v0/block/get" {
			http.Error(w, "unexpected path "+got, http.StatusNotFound)
			return
		}
		cidValue := r.URL.Query().Get("arg")
		data, ok := objects[cidValue]
		if !ok {
			http.Error(w, fmt.Sprintf("missing cid %s", cidValue), http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	}))
	defer ipfs.Close()

	if _, err := subscriberStore.Store("PNM.fbs", pnmBytes, providerID.String(), nil); err != nil {
		t.Fatalf("store subscriber PNM: %v", err)
	}
	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: providerID, TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("add trusted provider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &Node{
		store:        subscriberStore,
		peerRegistry: registry,
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmpDir, "subscriber-storage")},
			Admin:   config.AdminConfig{IPFSAPIURL: ipfs.URL},
		},
		ctx:                     ctx,
		datasetMaterializedPNMs: make(map[string]time.Time),
	}

	materialized, err := n.materializeStoredDatasetPublicationPNMs(ctx, 10)
	if err != nil {
		t.Fatalf("materializeStoredDatasetPublicationPNMs failed: %v", err)
	}
	if materialized != 1 {
		t.Fatalf("materialized = %d, want 1", materialized)
	}
	records, err := subscriberStore.QueryIndexedRecords(storage.IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "celestrak.eth",
		SourceName: "celestrak-satcat-csv",
		BatchID:    "batch-sha-001",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query subscriber CAT records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("subscriber CAT records = %d, want 1", len(records))
	}
}

func mustReadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
