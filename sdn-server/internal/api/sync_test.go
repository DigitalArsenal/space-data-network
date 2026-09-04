package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
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

func TestSyncOneDSSPerLaneWithChannelAndOrigin(t *testing.T) {
	store := newConnectorsTestStore(t)
	deps := &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForSyncTest", Channels: NewChannelHandler(store)}
	mux, _ := newSyncTestMux(t, deps)

	rec, frames := syncFrames(t, mux, http.MethodGet, SyncPath, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/sync status = %d body = %q", rec.Code, rec.Body.String())
	}
	if len(frames) != 2 || rec.Header().Get(StreamRecordCountHeader) != "2" {
		t.Fatalf("frames = %d (header %q), want 2 lanes", len(frames), rec.Header().Get(StreamRecordCountHeader))
	}
	if got := rec.Header().Get(StreamSchemaHeader); got != SyncSchemaName {
		t.Fatalf("X-SDN-Schema = %q", got)
	}

	gp := findDSS(t, frames, "OMM.fbs", "space-data-network-02", "celestrak-gp")
	if gp.LocalRows() != 2 {
		t.Fatalf("OMM LOCAL_ROWS = %d, want 2", gp.LocalRows())
	}
	wantChannel, err := channels.FormatChannelID(channels.ChannelIDInput{
		SourceID:     datasetPublicationSourceID("space-data-network-02", "celestrak-gp"),
		StandardCode: "OMM",
	})
	if err != nil {
		t.Fatalf("FormatChannelID: %v", err)
	}
	if got := string(gp.ChannelId()); got != wantChannel {
		t.Fatalf("CHANNEL_ID = %q, want %q", got, wantChannel)
	}
	if got := string(gp.TOPIC()); got != channels.DiscoveryTopic("OMM") {
		t.Fatalf("TOPIC = %q", got)
	}
	if string(gp.OriginId()) != "celestrak.org" || string(gp.ConnectorId()) != "space-data-network-02/celestrak-gp" || string(gp.DatasetId()) != "gp-full-catalog" {
		t.Fatalf("origin triple = %q / %q / %q", string(gp.OriginId()), string(gp.ConnectorId()), string(gp.DatasetId()))
	}
	if string(gp.ProviderPeerId()) != connectorsTestProducer {
		t.Fatalf("PROVIDER_PEER_ID = %q, want the seeded producer", string(gp.ProviderPeerId()))
	}
	if gp.SUBSCRIBED() {
		t.Fatalf("SUBSCRIBED = true before any subscribe")
	}
	if int8(gp.PinPolicy()) != DSSPinNone {
		t.Fatalf("PIN_POLICY = %d, want None", gp.PinPolicy())
	}
	if string(gp.SyncProtocol()) == "" || string(gp.VISIBILITY()) != "public" || string(gp.EncryptionState()) != "none" || string(gp.GrantState()) != "not-required" {
		t.Fatalf("channel words = %q / %q / %q / %q", string(gp.SyncProtocol()), string(gp.VISIBILITY()), string(gp.EncryptionState()), string(gp.GrantState()))
	}
	if int8(gp.STATUS()) != DSSStateSynced || int8(gp.RequestedAction()) != DSSActionNone {
		t.Fatalf("STATUS/REQUESTED_ACTION = %d/%d", gp.STATUS(), gp.RequestedAction())
	}

	spw := findDSS(t, frames, "SPW.fbs", "space-data-network-02", "celestrak-space-weather")
	if spw.LocalRows() != 1 || string(spw.DatasetId()) != "sw-all" {
		t.Fatalf("SPW lane = %d rows / %q", spw.LocalRows(), string(spw.DatasetId()))
	}

	// Filters narrow the set; the item route addresses one lane.
	_, filtered := syncFrames(t, mux, http.MethodGet, SyncPath+"?schema=SPW", nil)
	if len(filtered) != 1 {
		t.Fatalf("schema filter = %d frames, want 1", len(filtered))
	}
	_, byOrigin := syncFrames(t, mux, http.MethodGet, SyncPath+"?origin=celestrak.org", nil)
	if len(byOrigin) != 2 {
		t.Fatalf("origin filter = %d frames, want 2", len(byOrigin))
	}
	itemRec, item := syncFrames(t, mux, http.MethodGet, SyncPath+"/OMM/space-data-network-02/celestrak-gp", nil)
	if itemRec.Code != http.StatusOK || len(item) != 1 {
		t.Fatalf("item route status = %d frames = %d", itemRec.Code, len(item))
	}
	if got := string(decodeDSSFrame(t, item[0]).SCHEMA_NAME()); got != "OMM.fbs" {
		t.Fatalf("item SCHEMA_NAME = %q", got)
	}

	// An archive-plane ledger row on the lane flips PIN_POLICY to Archive.
	if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID: "bafyarchiveshardforsynctest", SchemaName: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp",
		BatchID: "archive-omm-1", QueryProfile: storage.ArchiveQueryProfile, Role: storage.PinLedgerRoleArchive,
		SnapshotID: "bafyarchivemanifest", VerificationState: "verified", VerifiedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertPinLedgerEntry: %v", err)
	}
	_, after := syncFrames(t, mux, http.MethodGet, SyncPath+"?schema=OMM", nil)
	if got := int8(findDSS(t, after, "OMM.fbs", "space-data-network-02", "celestrak-gp").PinPolicy()); got != DSSPinArchive {
		t.Fatalf("PIN_POLICY after archive row = %d, want Archive", got)
	}
}

func TestSyncSubscribeFlipsTheLaneAndPersistsTheList(t *testing.T) {
	store := newConnectorsTestStore(t)
	deps := &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForSyncTest", Channels: NewChannelHandler(store)}
	mux, h := newSyncTestMux(t, deps)

	rec, frames := syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM.fbs", "space-data-network-02", "celestrak-gp", DSSActionSubscribe))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST Subscribe status = %d body = %q", rec.Code, rec.Body.String())
	}
	if len(frames) != 1 {
		t.Fatalf("POST Subscribe frames = %d, want the lane's $DSS", len(frames))
	}
	dss := decodeDSSFrame(t, frames[0])
	if !dss.SUBSCRIBED() {
		t.Fatalf("SUBSCRIBED = false after Subscribe")
	}
	if int8(dss.STATUS()) == DSSStateError {
		t.Fatalf("STATUS = ERROR after Subscribe: %q", string(dss.ERROR()))
	}

	raw, err := os.ReadFile(h.SubscriptionFilePath())
	if err != nil {
		t.Fatalf("subscription file %s: %v", h.SubscriptionFilePath(), err)
	}
	var listed []string
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("subscription file is not a JSON list: %v", err)
	}
	if len(listed) != 1 || listed[0] != string(dss.ChannelId()) {
		t.Fatalf("subscription file = %v, want [%s]", listed, string(dss.ChannelId()))
	}

	// A fresh handler over the same store reloads the list.
	fresh := &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForSyncTest", Channels: NewChannelHandler(store)}
	freshMux, _ := newSyncTestMux(t, fresh)
	_, reloaded := syncFrames(t, freshMux, http.MethodGet, SyncPath+"?schema=OMM", nil)
	if !findDSS(t, reloaded, "OMM.fbs", "space-data-network-02", "celestrak-gp").SUBSCRIBED() {
		t.Fatalf("subscription did not survive a handler restart")
	}

	rec, frames = syncFrames(t, mux, http.MethodPost, SyncPath, EncodeDSSAction("OMM", "space-data-network-02", "celestrak-gp", DSSActionUnsubscribe))
	if rec.Code != http.StatusAccepted || decodeDSSFrame(t, frames[0]).SUBSCRIBED() {
		t.Fatalf("Unsubscribe status = %d subscribed = %v", rec.Code, decodeDSSFrame(t, frames[0]).SUBSCRIBED())
	}
	raw, _ = os.ReadFile(h.SubscriptionFilePath())
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("subscription file after Unsubscribe = %q, want []", raw)
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
