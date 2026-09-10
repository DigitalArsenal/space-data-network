package api

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func addRemoteSyncCatalog(t *testing.T, store *storage.FlatSQLStore, publisher, provider, source string) string {
	t.Helper()
	public, key, _ := ed25519.GenerateKey(nil)
	export, err := storage.ExportDatasetRecords(t.TempDir(), storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: provider, SourceName: source, Limit: 10}, []storage.DatasetExportRecord{{
		Data: sds.NewOMMBuilder().WithNoradCatID(25544).Build(), SourceTags: storage.SourceTags{ProviderID: provider, SourceName: source, BatchID: "current", ContentKeyID: "public"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest, err := storage.BuildSignedDatasetPublicationManifest(t.TempDir(), storage.DatasetPublicationManifestOptions{Export: export, DatasetID: provider, UpdateID: "current", FileID: provider + ":OMM.fbs:current", ProviderPeerID: publisher, PublishedAt: now, SigningKey: key})
	if err != nil {
		t.Fatal(err)
	}
	pnm, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{PublishedAt: now, SigningKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if err := channels.RememberDatasetCatalog(store, publisher, public, pnm, manifest.Bytes, now); err != nil {
		t.Fatal(err)
	}
	return manifest.CID
}

// Explicit live receipt input keeps ordinary unit tests independent of the
// fleet. Receipts contain public verification keys and signed PNM paths only.
func TestSyncLiveProviderCatalogs(t *testing.T) {
	input := os.Getenv("SDN_LIVE_DATASET_CATALOG_FIXTURES")
	if input == "" {
		t.Skip("live provider receipts not requested")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		PeerID, PublicKey, SourceID, PNMCID, PNMFile, ManifestCID string
		Records                                                   uint64
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 13 {
		t.Fatalf("expected 13 public provider receipts, got %d", len(fixtures))
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	store, err := storage.NewFlatSQLStore(path, validator)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	client := &http.Client{Timeout: 10 * time.Second}
	for _, fixture := range fixtures {
		key, err := hex.DecodeString(fixture.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			t.Fatal("invalid public fixture key")
		}
		pnm, err := os.ReadFile(filepath.Join(filepath.Dir(input), fixture.PNMFile))
		if err != nil {
			t.Fatal(err)
		}
		proof, err := channels.VerifySignedPNMEnvelopeWithProviderKey(pnm, key)
		if err != nil || proof.CID != fixture.ManifestCID {
			t.Fatalf("%s PNM: %v", fixture.SourceID, err)
		}
		response, err := client.Post("http://127.0.0.1:5001/api/v0/cat?arg="+url.QueryEscape(proof.CID), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := io.ReadAll(io.LimitReader(response.Body, channels.MaxDatasetCatalogManifestBytes+1))
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("%s independent Kubo fetch: status=%d err=%v", fixture.SourceID, response.StatusCode, err)
		}
		if err := channels.RememberDatasetCatalog(store, fixture.PeerID, key, pnm, manifest, time.Now()); err != nil {
			t.Fatalf("%s catalog: %v", fixture.SourceID, err)
		}
	}
	// Reopen proves the local manifest cache alone supplies the directory.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewFlatSQLStore(path, validator)
	if err != nil {
		t.Fatal(err)
	}
	lanes, err := BuildSyncFrames(&AdminMountDeps{Store: store}, SyncFilter{})
	if err != nil || len(lanes) != len(fixtures) {
		t.Fatalf("remote lanes=%d err=%v", len(lanes), err)
	}
	for _, fixture := range fixtures {
		found := false
		for _, lane := range lanes {
			if lane.Key.sourceName != fixture.SourceID {
				continue
			}
			found = true
			if lane.ProviderPeerID != fixture.PeerID || lane.Key.schema != "NCD.fbs" || lane.TotalRows != fixture.Records || lane.LocalRows != 0 || lane.PinnedRows != 0 || lane.LastPublicationCID != fixture.ManifestCID {
				t.Fatalf("wrong source catalog: %+v", lane)
			}
			t.Logf("%s: %d announced, 0 local, 0 pinned; signed manifest cached after reopen", fixture.SourceID, lane.TotalRows)
		}
		if !found {
			t.Fatalf("missing source %s", fixture.SourceID)
		}
	}
}

func TestSyncRemoteCatalogIsVisibleWithoutLocalRecordsAndDoesNotReplaceAnotherProducer(t *testing.T) {
	store := newConnectorsTestStore(t)
	manifestCID := addRemoteSyncCatalog(t, store, "remote-peer", "remote-provider", "remote-orbits")
	// Existing held records with the same textual provider/source must not
	// acquire a different identity from an unrelated peer's signed claim.
	addRemoteSyncCatalog(t, store, "other-peer", "space-data-network-02", "celestrak-gp")
	mux, _ := newSyncTestMux(t, &AdminMountDeps{Store: store})
	rec, frames := syncFrames(t, mux, http.MethodGet, SyncPath, nil)
	if rec.Code != http.StatusOK || len(frames) != 3 {
		t.Fatalf("status=%d lanes=%d", rec.Code, len(frames))
	}
	remote := findDSS(t, frames, "OMM.fbs", "remote-provider", "remote-orbits")
	if remote.LocalRows() != 0 || remote.PinnedRows() != 0 || remote.TotalRows() != 1 || remote.MissingRows() != 1 || int8(remote.Status()) != DSSStateIdle {
		t.Fatalf("remote replica counters are incorrect")
	}
	if string(remote.ProviderPeerId()) != "remote-peer" || string(remote.LastPublicationCid()) != manifestCID {
		t.Fatalf("remote publication identity missing")
	}
	local := findDSS(t, frames, "OMM.fbs", "space-data-network-02", "celestrak-gp")
	if local.LocalRows() != 2 || string(local.ProviderPeerId()) == "other-peer" {
		t.Fatal("remote metadata replaced local producer")
	}
	addRemoteSyncCatalog(t, store, "conflicting-peer", "remote-provider", "remote-orbits")
	_, frames = syncFrames(t, mux, http.MethodGet, SyncPath, nil)
	if len(frames) != 2 {
		t.Fatalf("ambiguous remote source leaked into Store: %d lanes", len(frames))
	}
}
