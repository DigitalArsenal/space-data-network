package api

import (
	"bytes"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
