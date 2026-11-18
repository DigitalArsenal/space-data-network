package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func newSyncTestMux(t *testing.T, deps *AdminMountDeps) (*http.ServeMux, *SyncHandler) {
	t.Helper()
	h := NewSyncHandler(deps)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, h
}

func syncFrames(t *testing.T, mux *http.ServeMux, method, target string, body []byte) (*httptest.ResponseRecorder, [][]byte) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
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
	if got := rec.Header().Get("Content-Type"); got != StreamContentType {
		t.Fatalf("%s %s: Content-Type = %q", method, target, got)
	}
	return rec, frames
}

func decodeDSSFrame(t *testing.T, frame []byte) *DSS.DSS {
	t.Helper()
	if got := FrameIdentifier(frame); got != "$DSS" {
		t.Fatalf("frame identifier = %q, want $DSS", got)
	}
	dss, err := DecodeDSS(frame)
	if err != nil {
		t.Fatalf("DecodeDSS: %v", err)
	}
	return dss
}

func findDSS(t *testing.T, frames [][]byte, schema, provider, source string) *DSS.DSS {
	t.Helper()
	for _, frame := range frames {
		dss := decodeDSSFrame(t, frame)
		if string(dss.SCHEMA_NAME()) == schema && string(dss.PROVIDER_ID()) == provider && string(dss.SOURCE_NAME()) == source {
			return dss
		}
	}
	t.Fatalf("no $DSS for %s/%s/%s among %d frames", schema, provider, source, len(frames))
	return nil
}

func TestSyncDiscoversPublishedDatasetsBeforeDownload(t *testing.T) {
	store := newConnectorsTestStore(t)
	for _, schema := range []string{"CAT.fbs", "MPE.fbs"} {
		if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
			SchemaName: schema, ProviderID: "space-data-network-02", SourceName: "catalog-publication",
			BatchID: "batch-1", QueryProfile: storage.DatasetPublicationQueryProfile, Offset: 100, Limit: 100,
			RecordCount: 50, ByteCount: 1000, ShardCID: "bafyshard", IndexCID: "bafyindex",
			ManifestCID: "bafymanifest", PNMCID: "bafypnm", PublishedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	deps := &AdminMountDeps{Store: store, Config: &config.Config{}, Channels: NewChannelHandler(store)}
	mux, _ := newSyncTestMux(t, deps)
	_, frames := syncFrames(t, mux, http.MethodGet, SyncPath, nil)
	for _, schema := range []string{"CAT.fbs", "MPE.fbs"} {
		lane := findDSS(t, frames, schema, "space-data-network-02", "catalog-publication")
		if lane.LocalRows() != 0 || lane.TotalRows() != 150 || lane.MissingRows() != 150 || int8(lane.STATUS()) != DSSStateIdle {
			t.Fatalf("%s: publication must be available without claiming local rows", schema)
		}
		if string(lane.ProviderPeerId()) != connectorsTestProducer || string(lane.LastPublicationCid()) != "bafymanifest" {
			t.Fatalf("%s: missing provider or publication identity", schema)
		}
	}
	_, filtered := syncFrames(t, mux, http.MethodGet, SyncPath+"?schema=CAT", nil)
	if len(filtered) != 1 {
		t.Fatalf("CAT filter returned %d lanes", len(filtered))
	}
	_, exact := syncFrames(t, mux, http.MethodGet, SyncPath+"/OMM/space-data-network-02/celestrak-gp", nil)
	if len(exact) != 1 {
		t.Fatalf("exact source returned %d lanes", len(exact))
	}
}

func TestSyncRejectsABodyThatIsNotOneDSSFrame(t *testing.T) {
	store := newConnectorsTestStore(t)
	mux, _ := newSyncTestMux(t, &AdminMountDeps{Store: store, Config: &config.Config{}})

	rec, frames := syncFrames(t, mux, http.MethodPost, SyncPath, BuildQRP(QRPFields{Kind: QRPKindRequest, SchemaName: "OMM.fbs"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST with a $QRP body status = %d, want 400", rec.Code)
	}
	if len(frames) != 1 || FrameIdentifier(frames[0]) != "$QRP" {
		t.Fatalf("error body = %d frames (%q), want one $QRP", len(frames), FrameIdentifier(frames[0]))
	}
	q, err := ParseQRP(frames[0])
	if err != nil {
		t.Fatalf("ParseQRP: %v", err)
	}
	if int8(q.KIND()) != QRPKindError || int8(q.STATUS()) != QRPStatusError || string(q.MESSAGE()) == "" {
		t.Fatalf("error frame = kind %d status %d message %q", q.KIND(), q.STATUS(), string(q.MESSAGE()))
	}

	// Two frames are refused the same way.
	body := append(append([]byte{}, EncodeDSSAction("OMM.fbs", "a", "b", DSSActionSync)...), EncodeDSSAction("OMM.fbs", "a", "b", DSSActionSync)...)
	rec, _ = syncFrames(t, mux, http.MethodPost, SyncPath, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST with two frames status = %d, want 400", rec.Code)
	}
}

func TestSyncWithoutASyncPrimitiveReportsErrorInPlainWords(t *testing.T) {
	store := newConnectorsTestStore(t)
	mux, _ := newSyncTestMux(t, &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForSyncTest"})

	// A lane is imported by following it: Sync is refused, naming the
	// command that subscribes.
	rec, frames := syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM.fbs", "space-data-network-02", "celestrak-gp", DSSActionSync))
	if rec.Code != http.StatusBadRequest || len(frames) != 1 {
		t.Fatalf("POST Sync status = %d, frames = %d; want 400 and one error", rec.Code, len(frames))
	}
	if refusal, err := ParseQRP(frames[0]); err != nil || !strings.Contains(string(refusal.MESSAGE()), "sync subscribe") {
		t.Fatalf("Sync refusal %v: %q", err, refusal.MESSAGE())
	}

	// Pin actions belong to the node pin policy and return explicit guidance here.
	rec, frames = syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM.fbs", "space-data-network-02", "celestrak-gp", DSSActionPin))
	if rec.Code != http.StatusBadRequest || len(frames) != 1 {
		t.Fatalf("POST Pin status = %d, frames = %d; want 400 and one error", rec.Code, len(frames))
	}
	refusal, err := ParseQRP(frames[0])
	if err != nil {
		t.Fatal(err)
	}
	if int8(refusal.KIND()) != QRPKindError || string(refusal.ERROR_CODE()) != "bad_request" || !strings.Contains(string(refusal.MESSAGE()), "/api/node/pins") {
		t.Fatalf("pin refusal kind=%v code=%q message=%q", refusal.KIND(), refusal.ERROR_CODE(), refusal.MESSAGE())
	}
}
