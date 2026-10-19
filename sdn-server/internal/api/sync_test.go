package api

import (
	"bytes"
	"context"
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

	rec, frames := syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM.fbs", "space-data-network-02", "celestrak-gp", DSSActionSync))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST Sync status = %d body = %q", rec.Code, rec.Body.String())
	}
	dss := decodeDSSFrame(t, frames[0])
	if int8(dss.STATUS()) != DSSStateError {
		t.Fatalf("STATUS = %d, want ERROR when the node cannot sync", dss.STATUS())
	}
	if msg := string(dss.ERROR()); !strings.Contains(msg, "cannot sync") {
		t.Fatalf("ERROR = %q, want a plain sentence", msg)
	}

	// Pin without Kubo and without a publication reads the same way.
	rec, frames = syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM.fbs", "space-data-network-02", "celestrak-gp", DSSActionPin))
	if rec.Code != http.StatusAccepted || int8(decodeDSSFrame(t, frames[0]).STATUS()) != DSSStateError {
		t.Fatalf("POST Pin status = %d STATUS = %d", rec.Code, decodeDSSFrame(t, frames[0]).STATUS())
	}
}

func TestSyncRunsTheLanePrimitiveAndReportsSyncedRows(t *testing.T) {
	store := newConnectorsTestStore(t)
	release := make(chan struct{})
	deps := &AdminMountDeps{
		Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForSyncTest",
		SyncLane: func(_ context.Context, schema, providerID, sourceName string) (int, error) {
			<-release
			if schema != "OMM.fbs" || providerID != "space-data-network-02" || sourceName != "celestrak-gp" {
				t.Errorf("SyncLane lane = %s/%s/%s", schema, providerID, sourceName)
			}
			return 7, nil
		},
	}
	mux, _ := newSyncTestMux(t, deps)

	rec, frames := syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM.fbs", "space-data-network-02", "celestrak-gp", DSSActionSync))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST Sync status = %d", rec.Code)
	}
	if got := int8(decodeDSSFrame(t, frames[0]).STATUS()); got != DSSStateSyncing {
		t.Fatalf("STATUS while running = %d, want SYNCING", got)
	}
	// A second action on a busy lane is refused with 409.
	rec, _ = syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM.fbs", "space-data-network-02", "celestrak-gp", DSSActionHydrate))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second action on a busy lane status = %d, want 409", rec.Code)
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, frames = syncFrames(t, mux, http.MethodGet, SyncPath+"/OMM.fbs/space-data-network-02/celestrak-gp", nil)
		dss := decodeDSSFrame(t, frames[0])
		if int8(dss.STATUS()) != DSSStateSyncing {
			if dss.SyncedRows() != 7 || string(dss.ERROR()) != "" || dss.LastSyncStartedAt() == 0 {
				t.Fatalf("after sync: SYNCED_ROWS=%d ERROR=%q LAST_SYNC_STARTED_AT=%d", dss.SyncedRows(), string(dss.ERROR()), dss.LastSyncStartedAt())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
