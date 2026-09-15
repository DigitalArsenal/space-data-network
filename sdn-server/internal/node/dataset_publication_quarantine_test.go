package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// A stored dataset PNM whose signature no key known for its producer verifies
// is a record the node holds and cannot use — not a producer refusing it now.
// On the fleet (2026-09-15) the stored-PNM catch-up re-verified, re-logged and
// re-alerted such frames every five minutes, so both hosts read "degraded"
// over junk they could never materialize. The catch-up must quarantine the
// frame after one look and leave publication_rejected to live refusals.
func TestStoredPNMCatchupQuarantinesAFrameNoKnownKeyVerifies(t *testing.T) {
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

	_, providerPub, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x61}, 128)))
	if err != nil {
		t.Fatalf("GenerateEd25519Key provider failed: %v", err)
	}
	providerID, err := peer.IDFromPublicKey(providerPub)
	if err != nil {
		t.Fatalf("IDFromPublicKey failed: %v", err)
	}
	// The impostor signs with a key the provider never had.
	impostorPriv, _, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x62}, 128)))
	if err != nil {
		t.Fatalf("GenerateEd25519Key impostor failed: %v", err)
	}
	rawImpostor, err := impostorPriv.Raw()
	if err != nil {
		t.Fatalf("raw impostor key: %v", err)
	}

	tags := storage.SourceTags{
		ProviderID:   "celestrak.eth",
		SourceName:   "celestrak-satcat-csv",
		SourceURL:    "https://celestrak.org/pub/satcat.csv",
		BatchID:      "batch-junk-001",
		ContentKeyID: "public",
	}
	record := sds.NewCATBuilder().WithNoradCatID(25544).WithObjectName("ISS").WithObjectType("PAYLOAD").WithOpsStatus("OPERATIONAL").Build()
	if _, err := providerStore.StoreWithSourceTags("CAT.fbs", record, providerID.String(), nil, tags); err != nil {
		t.Fatalf("store provider record: %v", err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), storage.IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          "celestrak.eth",
		SourceName:          "celestrak-satcat-csv",
		BatchID:             "batch-junk-001",
		Limit:               10,
		AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	publishedAt := time.Unix(1700003333, 0).UTC()
	manifest, err := storage.BuildSignedDatasetPublicationManifest(filepath.Join(tmpDir, "publish"), storage.DatasetPublicationManifestOptions{
		Export:         export,
		DatasetID:      "cat-junk",
		UpdateID:       "batch-junk-001",
		ProviderPeerID: providerID.String(),
		ProviderEPMCID: "bafy-provider-epm",
		PublishedAt:    publishedAt,
		SigningKey:     ed25519.PrivateKey(rawImpostor),
		SchemaHash:     "cat-schema-hash",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest failed: %v", err)
	}
	pnmBytes, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{
		PublishedAt: publishedAt,
		SigningKey:  ed25519.PrivateKey(rawImpostor),
	})
	if err != nil {
		t.Fatalf("BuildDatasetPublicationPNM failed: %v", err)
	}
	cid, err := subscriberStore.Store("PNM.fbs", pnmBytes, providerID.String(), nil)
	if err != nil {
		t.Fatalf("store subscriber PNM: %v", err)
	}

	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: providerID, TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("add trusted provider: %v", err)
	}
	// The signature check comes before any fetch; the IPFS API only has to
	// be configured, never reached.
	fetched := 0
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched++
		http.Error(w, "the frame must be refused before anything is fetched", http.StatusNotFound)
	}))
	defer ipfs.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	alerts := ops.NewRegistry()
	n := &Node{
		store:        subscriberStore,
		peerRegistry: registry,
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmpDir, "subscriber-storage")},
			Admin:   config.AdminConfig{IPFSAPIURL: ipfs.URL},
		},
		ctx:                     ctx,
		alerts:                  alerts,
		datasetMaterializedPNMs: make(map[string]time.Time),
	}

	for pass := 1; pass <= 2; pass++ {
		materialized, err := n.materializeStoredDatasetPublicationPNMs(ctx, 10)
		if err != nil {
			t.Fatalf("pass %d: a quarantined frame is not a catch-up error, got %v", pass, err)
		}
		if materialized != 0 {
			t.Fatalf("pass %d: materialized = %d, want 0", pass, materialized)
		}
		if _, quarantined := n.quarantinedStoredPNMs.Load(cid); !quarantined {
			t.Fatalf("pass %d: %s is not quarantined", pass, cid)
		}
		if active := alerts.Active(); len(active) != 0 {
			t.Fatalf("pass %d: a stored frame no key verifies raised %+v; publication_rejected is for live refusals", pass, active)
		}
	}
	if fetched != 0 {
		t.Fatalf("the catch-up fetched %d object(s) for a frame it could not verify", fetched)
	}
}
