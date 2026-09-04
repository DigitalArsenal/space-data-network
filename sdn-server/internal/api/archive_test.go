package api

import (
	"bytes"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const archiveTestNodePeer = "12D3KooWArchiveApiTestNode"

func newArchiveTestDeps(t *testing.T, store *storage.FlatSQLStore, signingKey ed25519.PrivateKey) *AdminMountDeps {
	t.Helper()
	return &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: archiveTestNodePeer, SigningKey: signingKey}
}

func newArchiveTestMux(t *testing.T, deps *AdminMountDeps) (*http.ServeMux, *SyncHandler) {
	t.Helper()
	mux := http.NewServeMux()
	syncHandler := NewSyncHandler(deps)
	syncHandler.RegisterRoutes(mux)
	NewArchiveHandler(deps, syncHandler).RegisterRoutes(mux)
	return mux, syncHandler
}

func archiveFrames(t *testing.T, mux *http.ServeMux, method, target string, body []byte) (*httptest.ResponseRecorder, [][]byte) {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", StreamContentType)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	frames, err := SplitFrames(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("%s %s: body is not a frame stream: %v", method, target, err)
	}
	return rec, frames
}

func decodeDPMFrame(t *testing.T, frame []byte) *dpm.DPM {
	t.Helper()
	if got := FrameIdentifier(frame); got != "$DPM" {
		t.Fatalf("frame identifier = %q, want $DPM", got)
	}
	manifest, err := DecodeDPM(frame)
	if err != nil {
		t.Fatalf("DecodeDPM: %v", err)
	}
	return manifest
}

func TestArchiveCreateListsAndReimportsWithTheOriginalProducer(t *testing.T) {
	store := newExportTestStore(t)
	storeDataAPITestOMMInto(t, store, 25544, "ISS (ZARYA)", "2026-09-01")
	storeDataAPITestOMMInto(t, store, 40909, "OBJECT-B", "2026-09-02")
	storeDataAPITestOMMInto(t, store, 43013, "OBJECT-C", "2026-09-03")
	_, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x71}, 128)))
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	deps := newArchiveTestDeps(t, store, signingKey)
	mux, _ := newArchiveTestMux(t, deps)

	request := BuildQRP(QRPFields{
		Kind: QRPKindRequest, SchemaName: "OMM", ProviderID: "space-data-network-02", SourceName: "catalogfixture-gp",
		ArchiveID: "archive-omm-api-1",
	})
	rec, frames := archiveFrames(t, mux, http.MethodPost, ArchivePath, request)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/archive status = %d body = %q", rec.Code, rec.Body.String())
	}
	if len(frames) != 1 || rec.Header().Get(StreamSchemaHeader) != ArchiveSchemaName {
		t.Fatalf("archive response = %d frames, schema %q", len(frames), rec.Header().Get(StreamSchemaHeader))
	}
	manifest := decodeDPMFrame(t, frames[0])
	if string(manifest.DATASET_ID()) != "archive-omm-api-1" {
		t.Fatalf("DATASET_ID = %q, want the requested archive id", string(manifest.DATASET_ID()))
	}
	if string(manifest.PROVIDER_PEER_ID()) != archiveTestNodePeer {
		t.Fatalf("PROVIDER_PEER_ID = %q, want this node", string(manifest.PROVIDER_PEER_ID()))
	}
	if string(manifest.SIGNATURE_TYPE()) != "Ed25519" {
		t.Fatalf("SIGNATURE_TYPE = %q", string(manifest.SIGNATURE_TYPE()))
	}
	manifestCID := storage.ComputeCID(storage.BareDPMBytes(frames[0]))

	entries, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{Role: storage.PinLedgerRoleArchive, BatchID: "archive-omm-api-1"})
	if err != nil {
		t.Fatalf("ListPinLedgerEntries: %v", err)
	}
	if len(entries) < 3 {
		t.Fatalf("archive ledger rows = %d, want shard, index and manifest", len(entries))
	}
	laneRows, _ := store.ListPinLedgerEntries(storage.PinLedgerQuery{Role: storage.PinLedgerRoleArchive, ProviderID: "space-data-network-02", SourceName: "catalogfixture-gp"})
	if len(laneRows) == 0 {
		t.Fatalf("no lane-scoped archive rows; the lane's $DSS could not report PIN_POLICY Archive")
	}
	matches, _ := filepath.Glob(filepath.Join(store.ArchiveOutputDir(), "manifests", "archive-omm-api-1-*.dpm"))
	if len(matches) != 1 {
		t.Fatalf("manifest files = %v, want one under the archive plane", matches)
	}

	// The lane's $DSS reads PIN_POLICY Archive now.
	_, laneFrames := archiveFrames(t, mux, http.MethodGet, SyncPath+"/OMM/space-data-network-02/catalogfixture-gp", nil)
	if got := int8(decodeDSSFrame(t, laneFrames[0]).PinPolicy()); got != DSSPinArchive {
		t.Fatalf("lane PIN_POLICY = %d, want Archive", got)
	}

	// GET /api/v1/archives lists it; the asset route serves its shard.
	rec, listed := archiveFrames(t, mux, http.MethodGet, ArchivesPath, nil)
	if rec.Code != http.StatusOK || len(listed) != 1 || rec.Header().Get(StreamRecordCountHeader) != "1" {
		t.Fatalf("GET /api/v1/archives status = %d frames = %d", rec.Code, len(listed))
	}
	if string(decodeDPMFrame(t, listed[0]).DATASET_ID()) != "archive-omm-api-1" {
		t.Fatalf("listed archive is not the one created")
	}
	if _, filtered := archiveFrames(t, mux, http.MethodGet, ArchivesPath+"?schema=CAT", nil); len(filtered) != 0 {
		t.Fatalf("schema filter listed %d archives, want 0", len(filtered))
	}
	var shard dpm.DPMAsset
	shardCID := ""
	for i := 0; i < manifest.ASSETSLength(); i++ {
		if manifest.ASSETS(&shard, i) && shard.ASSET_KIND().String() == "DATA_SHARD" {
			shardCID = string(shard.CID())
		}
	}
	if shardCID == "" {
		t.Fatalf("manifest has no DATA_SHARD asset")
	}
	assetRec := httptest.NewRecorder()
	mux.ServeHTTP(assetRec, httptest.NewRequest(http.MethodGet, ArchivesPath+"/"+manifestCID+"/asset/"+shardCID, nil))
	if assetRec.Code != http.StatusOK || storage.ComputeCID(assetRec.Body.Bytes()) != shardCID {
		t.Fatalf("asset route status = %d, body CID matches = %v", assetRec.Code, storage.ComputeCID(assetRec.Body.Bytes()) == shardCID)
	}
	if !strings.HasPrefix(assetRec.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("asset Content-Disposition = %q", assetRec.Header().Get("Content-Disposition"))
	}

	// Re-import into a second node: the archive plane is copied over (as a
	// download or a disk would carry it), the manifest is addressed by id,
	// and the records land under the ORIGINAL producer.
	target := newExportTestStore(t)
	if err := os.CopyFS(target.ArchiveOutputDir(), os.DirFS(store.ArchiveOutputDir())); err != nil {
		t.Fatalf("copy archive plane: %v", err)
	}
	targetDeps := &AdminMountDeps{Store: target, Config: &config.Config{}, NodePeerID: "12D3KooWArchiveApiImportingNode"}
	targetMux, _ := newArchiveTestMux(t, targetDeps)
	// Without the producer's key on the importing node the signature cannot
	// be verified: refused, nothing imported.
	rec, frames = archiveFrames(t, targetMux, http.MethodPost, ArchiveImportPath, BuildQRP(QRPFields{Kind: QRPKindRequest, ArchiveID: "archive-omm-api-1"}))
	if rec.Code != http.StatusForbidden || FrameIdentifier(frames[0]) != "$QRP" {
		t.Fatalf("import without a verifiable key: status %d frame %q", rec.Code, FrameIdentifier(frames[0]))
	}
	// The importing node IS the producer (same signing key, same peer id):
	// the key resolves locally and the import runs on the lane.
	targetDeps.NodePeerID, targetDeps.SigningKey = archiveTestNodePeer, signingKey
	rec, frames = archiveFrames(t, targetMux, http.MethodPost, ArchiveImportPath, BuildQRP(QRPFields{Kind: QRPKindRequest, CID: manifestCID}))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/archive/import status = %d body = %q", rec.Code, rec.Body.String())
	}
	dss := decodeDSSFrame(t, frames[0])
	if string(dss.SCHEMA_NAME()) != "OMM.fbs" || string(dss.PROVIDER_ID()) != "space-data-network-02" || string(dss.SOURCE_NAME()) != "catalogfixture-gp" {
		t.Fatalf("import lane = %s/%s/%s", string(dss.SCHEMA_NAME()), string(dss.PROVIDER_ID()), string(dss.SOURCE_NAME()))
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, laneFrames = archiveFrames(t, targetMux, http.MethodGet, SyncPath+"/OMM.fbs/space-data-network-02/catalogfixture-gp", nil)
		dss = decodeDSSFrame(t, laneFrames[0])
		if int8(dss.STATUS()) != DSSStateSyncing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("import never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if msg := string(dss.ERROR()); msg != "" {
		t.Fatalf("import ERROR = %q", msg)
	}
	if dss.SyncedRows() != 3 || dss.LocalRows() != 3 {
		t.Fatalf("after import: SYNCED_ROWS=%d LOCAL_ROWS=%d, want 3/3", dss.SyncedRows(), dss.LocalRows())
	}
	if got := string(dss.ProviderPeerId()); got != archiveTestNodePeer {
		t.Fatalf("imported producer = %q, want the archive's provider %q", got, archiveTestNodePeer)
	}
	summary, err := target.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	if summary.TotalRecords != 3 {
		t.Fatalf("imported records = %d, want 3", summary.TotalRecords)
	}
	if _, listed := archiveFrames(t, targetMux, http.MethodGet, ArchivesPath, nil); len(listed) != 1 {
		t.Fatalf("importing node lists %d archives, want 1", len(listed))
	}
}

func TestArchiveRefusesWhatItCannotSignOrSelect(t *testing.T) {
	store := newExportTestStore(t)
	storeDataAPITestOMMInto(t, store, 25544, "ISS (ZARYA)", "2026-09-01")
	_, signingKey, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x72}, 128)))

	// No signing key: 503 with a plain sentence.
	mux, _ := newArchiveTestMux(t, newArchiveTestDeps(t, store, nil))
	rec, frames := archiveFrames(t, mux, http.MethodPost, ArchivePath, BuildQRP(QRPFields{Kind: QRPKindRequest, SchemaName: "OMM"}))
	if rec.Code != http.StatusServiceUnavailable || FrameIdentifier(frames[0]) != "$QRP" {
		t.Fatalf("archive without a key: status %d frame %q", rec.Code, FrameIdentifier(frames[0]))
	}
	q, _ := ParseQRP(frames[0])
	if !strings.Contains(string(q.MESSAGE()), "signing key") {
		t.Fatalf("message = %q", string(q.MESSAGE()))
	}

	mux, _ = newArchiveTestMux(t, newArchiveTestDeps(t, store, signingKey))
	// An empty selection is a 400.
	rec, frames = archiveFrames(t, mux, http.MethodPost, ArchivePath, BuildQRP(QRPFields{Kind: QRPKindRequest, SchemaName: "OMM", SourceName: "no-such-source"}))
	if rec.Code != http.StatusBadRequest || FrameIdentifier(frames[0]) != "$QRP" {
		t.Fatalf("empty selection: status %d frame %q", rec.Code, FrameIdentifier(frames[0]))
	}
	// A body that is not a $QRP frame is a 400 too.
	rec, _ = archiveFrames(t, mux, http.MethodPost, ArchivePath, EncodeDSSAction("OMM", "a", "b", DSSActionSync))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-QRP body: status %d", rec.Code)
	}
	// An unknown archive cannot be imported.
	rec, frames = archiveFrames(t, mux, http.MethodPost, ArchiveImportPath, BuildQRP(QRPFields{Kind: QRPKindRequest, ArchiveID: "never-made"}))
	if rec.Code != http.StatusNotFound || FrameIdentifier(frames[0]) != "$QRP" {
		t.Fatalf("unknown archive import: status %d", rec.Code)
	}
	// Nothing to list yet.
	rec, frames = archiveFrames(t, mux, http.MethodGet, ArchivesPath, nil)
	if rec.Code != http.StatusOK || len(frames) != 0 {
		t.Fatalf("empty archive list: status %d frames %d", rec.Code, len(frames))
	}
}
