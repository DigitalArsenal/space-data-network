package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/ICN"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/SPW"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	connectorsTestProducer = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"
	connectorsTestGPURL    = "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv"
	connectorsTestSPWURL   = "https://celestrak.org/SpaceData/SW-All.csv"
)

func newConnectorsTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "sdn-connectors-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for i, norad := range []uint32{25544, 40909} {
		payload := sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectName("OBJECT-" + string(rune('A'+i))).
			WithEpoch("2026-09-03T12:00:00Z").
			Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "source:celestrak", nil, storage.SourceTags{
			ProviderID:     "space-data-network-02",
			SourceName:     "celestrak-gp",
			SourceURL:      connectorsTestGPURL,
			BatchID:        "gp-batch-1",
			ProducerPeerID: connectorsTestProducer,
		}); err != nil {
			t.Fatalf("store OMM %d: %v", norad, err)
		}
	}
	b := flatbuffers.NewBuilder(256)
	date := b.CreateString("2026-09-03")
	SPW.SPWStart(b)
	SPW.SPWAddDATE(b, date)
	SPW.SPWAddBSRN(b, 2600)
	SPW.FinishSizePrefixedSPWBuffer(b, SPW.SPWEnd(b))
	if _, err := store.StoreWithSourceTags("SPW.fbs", b.FinishedBytes(), "source:celestrak", nil, storage.SourceTags{
		ProviderID:     "space-data-network-02",
		SourceName:     "celestrak-space-weather",
		SourceURL:      connectorsTestSPWURL,
		BatchID:        "spw-batch-1",
		ProducerPeerID: connectorsTestProducer,
	}); err != nil {
		t.Fatalf("store SPW: %v", err)
	}
	return store
}

func decodeICNFrame(t *testing.T, frame []byte) *ICN.ICN {
	t.Helper()
	if got := FrameIdentifier(frame); got != "$ICN" {
		t.Fatalf("frame identifier = %q, want $ICN", got)
	}
	icn, err := DecodeICN(frame)
	if err != nil {
		t.Fatalf("DecodeICN: %v", err)
	}
	return icn
}

func emittedSchemas(icn *ICN.ICN) []string {
	out := make([]string, 0, icn.EmitsSchemasLength())
	for i := 0; i < icn.EmitsSchemasLength(); i++ {
		out = append(out, string(icn.EmitsSchemas(i)))
	}
	return out
}

func TestConnectorsReplicaLanesResolveFromTheCompiledRegistry(t *testing.T) {
	store := newConnectorsTestStore(t)
	deps := &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForConnectorsTest"}

	connectors, err := BuildConnectorFrames(deps)
	if err != nil {
		t.Fatalf("BuildConnectorFrames: %v", err)
	}
	if len(connectors) != 2 {
		t.Fatalf("connectors = %d, want 2", len(connectors))
	}
	// Sorted by ORIGIN_ID then SOURCE_NAME.
	if connectors[0].SourceName != "celestrak-gp" || connectors[1].SourceName != "celestrak-space-weather" {
		t.Fatalf("order = %s, %s", connectors[0].SourceName, connectors[1].SourceName)
	}

	gp := decodeICNFrame(t, connectors[0].Frame)
	if got := string(gp.ConnectorId()); got != "space-data-network-02/celestrak-gp" {
		t.Fatalf("CONNECTOR_ID = %q", got)
	}
	if string(gp.OriginId()) != "celestrak.org" || string(gp.OriginName()) != "CelesTrak" {
		t.Fatalf("origin = %q / %q", string(gp.OriginId()), string(gp.OriginName()))
	}
	if string(gp.DatasetId()) != "gp-full-catalog" {
		t.Fatalf("DATASET_ID = %q", string(gp.DatasetId()))
	}
	if int8(gp.STATUS()) != ICNStatusValidated {
		t.Fatalf("STATUS = %d, want Validated (no ledger row)", gp.STATUS())
	}
	if got := emittedSchemas(gp); len(got) != 1 || got[0] != "OMM.fbs" {
		t.Fatalf("EMITS_SCHEMAS = %v", got)
	}
	if string(gp.TargetSchema()) != "OMM.fbs" {
		t.Fatalf("TARGET_SCHEMA = %q", string(gp.TargetSchema()))
	}
	if string(gp.ProviderPeerId()) != connectorsTestProducer {
		t.Fatalf("PROVIDER_PEER_ID = %q, want the seeded producer", string(gp.ProviderPeerId()))
	}
	if string(gp.ProviderId()) != "space-data-network-02" || string(gp.SourceName()) != "celestrak-gp" {
		t.Fatalf("provenance pair = %q / %q", string(gp.ProviderId()), string(gp.SourceName()))
	}
	// Endpoint from the record's own source tag: a replica still names where
	// the bytes came from, so KIND is HttpsPull.
	if string(gp.EndpointUrl()) != connectorsTestGPURL {
		t.Fatalf("ENDPOINT_URL = %q", string(gp.EndpointUrl()))
	}
	if int8(gp.KIND()) != ICNKindHttpsPull || string(gp.HttpMethod()) != "GET" {
		t.Fatalf("KIND/HTTP_METHOD = %d/%q", gp.KIND(), string(gp.HttpMethod()))
	}
	if gp.MinFetchIntervalMs() != DefaultMinFetchIntervalMs || DefaultMinFetchIntervalMs != 10800000 {
		t.Fatalf("MIN_FETCH_INTERVAL_MS = %d", gp.MinFetchIntervalMs())
	}
	if gp.PollIntervalMs() != 3*3600*1000 {
		t.Fatalf("POLL_INTERVAL_MS = %d, want the registry cadence", gp.PollIntervalMs())
	}
	if gp.NextEligibleAt() != 0 {
		t.Fatalf("NEXT_ELIGIBLE_AT = %d, want 0 (never fetched here)", gp.NextEligibleAt())
	}
	if gp.License() != nil && len(gp.License()) != 0 {
		t.Fatalf("LICENSE = %q, want empty (never invented)", string(gp.License()))
	}
	if gp.UpdatedAt() == 0 || gp.CreatedAt() == 0 {
		t.Fatalf("CREATED_AT/UPDATED_AT = %d/%d", gp.CreatedAt(), gp.UpdatedAt())
	}

	spw := decodeICNFrame(t, connectors[1].Frame)
	if string(spw.DatasetId()) != "sw-all" || string(spw.OriginId()) != "celestrak.org" {
		t.Fatalf("SPW lane = %q / %q", string(spw.DatasetId()), string(spw.OriginId()))
	}
	if got := emittedSchemas(spw); len(got) != 1 || got[0] != "SPW.fbs" {
		t.Fatalf("SPW EMITS_SCHEMAS = %v", got)
	}
	if int8(spw.STATUS()) != ICNStatusValidated {
		t.Fatalf("SPW STATUS = %d", spw.STATUS())
	}
}

func TestConnectorsLedgerRowMakesTheLaneActiveWithValidators(t *testing.T) {
	store := newConnectorsTestStore(t)
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })

	ledger.RecordFetch(sourcemetrics.Fetch{URL: connectorsTestGPURL, Status: 200, Bytes: 4096, DurationMs: 321, ETag: `"gp-v1"`, LastModified: "Wed, 03 Sep 2026 11:00:00 GMT"})
	ledger.RecordIngest(sourcemetrics.Ingest{
		AppID: "celestrak-gp-ingest", ProviderID: "space-data-network-02", SourceName: "celestrak-gp",
		SourceURL: connectorsTestGPURL, Schema: "OMM.fbs", BatchID: "gp-batch-1", Records: 2, Inserted: 2,
	})
	row := ledgerRow(t, ledger, "celestrak-gp")

	deps := &AdminMountDeps{
		Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForConnectorsTest",
		SourceMetrics: ledger,
		FlowServices: func() []FlowServiceInfo {
			return []FlowServiceInfo{{ProgramID: "celestrak-gp-ingest", Running: true, TimerIntervalMs: 6 * 3600 * 1000}}
		},
	}
	connectors, err := BuildConnectorFrames(deps)
	if err != nil {
		t.Fatalf("BuildConnectorFrames: %v", err)
	}
	gp := decodeICNFrame(t, connectors[0].Frame)
	if int8(gp.STATUS()) != ICNStatusActive {
		t.Fatalf("STATUS = %d (%s), want Active", gp.STATUS(), string(gp.StatusMessage()))
	}
	if string(gp.LastSourceEtag()) != `"gp-v1"` || string(gp.LastSourceLastModified()) == "" {
		t.Fatalf("validators = %q / %q", string(gp.LastSourceEtag()), string(gp.LastSourceLastModified()))
	}
	if gp.LastHttpStatus() != 200 || gp.FetchCount() != 1 || gp.LastDurationMs() != 321 {
		t.Fatalf("fetch facts = %d/%d/%d", gp.LastHttpStatus(), gp.FetchCount(), gp.LastDurationMs())
	}
	if gp.LastRecordCount() != 2 || gp.LastInsertedCount() != 2 || gp.IngestCount() != 1 {
		t.Fatalf("ingest facts = %d/%d/%d", gp.LastRecordCount(), gp.LastInsertedCount(), gp.IngestCount())
	}
	if string(gp.LastBatchId()) != "gp-batch-1" {
		t.Fatalf("LAST_BATCH_ID = %q", string(gp.LastBatchId()))
	}
	if gp.PollIntervalMs() != 6*3600*1000 {
		t.Fatalf("POLL_INTERVAL_MS = %d, want the owning flow's timer", gp.PollIntervalMs())
	}
	wantIngestAt := uint64(row.LastRetrievedAt.UnixMilli())
	if gp.LastIngestAt() != wantIngestAt {
		t.Fatalf("LAST_INGEST_AT = %d, want %d", gp.LastIngestAt(), wantIngestAt)
	}
	if want := wantIngestAt + 3*3600*1000; gp.NextEligibleAt() != want {
		t.Fatalf("NEXT_ELIGIBLE_AT = %d, want last + 3 h = %d", gp.NextEligibleAt(), want)
	}
	// An attempt stamp (the retrieval gate) moves the window from the attempt.
	ledger.RecordAttempt("celestrak-gp-ingest")
	last, _ := ledger.AttemptState("celestrak-gp-ingest")
	connectors, _ = BuildConnectorFrames(deps)
	gp = decodeICNFrame(t, connectors[0].Frame)
	// An attempt counts as a failure until an ingest proves otherwise, so the
	// window is the gate's own EffectiveDebounceHoursFrom(base, failures).
	_, failures := ledger.AttemptState("celestrak-gp-ingest")
	window := time.Duration(sourcemetrics.EffectiveDebounceHoursFrom(sourcemetrics.DefaultDebounceHours, failures) * float64(time.Hour))
	if want := uint64(last.Add(window).UnixMilli()); gp.NextEligibleAt() < want-1000 || gp.NextEligibleAt() > want+1000 {
		t.Fatalf("NEXT_ELIGIBLE_AT after attempt = %d, want ~%d (%d failure(s), %s window)", gp.NextEligibleAt(), want, failures, window)
	}

	// No running owner: Paused. A failed last fetch: Error.
	deps.FlowServices = func() []FlowServiceInfo { return nil }
	connectors, _ = BuildConnectorFrames(deps)
	if gp = decodeICNFrame(t, connectors[0].Frame); int8(gp.STATUS()) != ICNStatusPaused {
		t.Fatalf("STATUS without owner = %d, want Paused", gp.STATUS())
	}
	ledger.RecordFetch(sourcemetrics.Fetch{URL: connectorsTestGPURL, Status: 403, Err: "forbidden"})
	connectors, _ = BuildConnectorFrames(deps)
	gp = decodeICNFrame(t, connectors[0].Frame)
	if int8(gp.STATUS()) != ICNStatusError || gp.LastHttpStatus() != 403 || string(gp.LastError()) == "" {
		t.Fatalf("STATUS after 403 = %d/%d/%q, want Error", gp.STATUS(), gp.LastHttpStatus(), string(gp.LastError()))
	}
	// The 403 must not have blanked the validators from the 200.
	if string(gp.LastSourceEtag()) != `"gp-v1"` {
		t.Fatalf("403 blanked LAST_SOURCE_ETAG: %q", string(gp.LastSourceEtag()))
	}
}

func TestConnectorsConfigOriginOutranksTheRegistry(t *testing.T) {
	store := newConnectorsTestStore(t)
	cfg := &config.Config{}
	cfg.Connectors.Origins = []config.ConnectorOriginConfig{{
		ProviderID: "space-data-network-02", SourceName: "celestrak-gp",
		OriginID: "celestrak.org", OriginName: "CelesTrak (operator)", DatasetID: "gp-full-catalog",
		License: "CelesTrak terms", LicenseURL: "https://celestrak.org/webmaster.php",
	}}
	deps := &AdminMountDeps{Store: store, Config: cfg, NodePeerID: "16Uiu2HAmLocalNodeForConnectorsTest"}
	connectors, err := BuildConnectorFrames(deps)
	if err != nil {
		t.Fatalf("BuildConnectorFrames: %v", err)
	}
	gp := decodeICNFrame(t, connectors[0].Frame)
	if string(gp.OriginName()) != "CelesTrak (operator)" {
		t.Fatalf("ORIGIN_NAME = %q, want the config value", string(gp.OriginName()))
	}
	if string(gp.License()) != "CelesTrak terms" || string(gp.LicenseUrl()) != "https://celestrak.org/webmaster.php" {
		t.Fatalf("licence = %q / %q", string(gp.License()), string(gp.LicenseUrl()))
	}
	// The registry still fills what config left empty.
	if gp.PollIntervalMs() != 3*3600*1000 {
		t.Fatalf("POLL_INTERVAL_MS = %d, want the registry cadence", gp.PollIntervalMs())
	}
	// The untouched lane keeps the registry name.
	spw := decodeICNFrame(t, connectors[1].Frame)
	if string(spw.OriginName()) != "CelesTrak" {
		t.Fatalf("SPW ORIGIN_NAME = %q", string(spw.OriginName()))
	}
}

func TestConnectorsPersistThroughTheEngine(t *testing.T) {
	store := newConnectorsTestStore(t)
	deps := &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForConnectorsTest"}
	n, err := PersistConnectors(deps)
	if err != nil {
		t.Fatalf("PersistConnectors: %v", err)
	}
	if n != 2 {
		t.Fatalf("persisted %d rows, want 2", n)
	}
	icnCount, err := store.Count(ConnectorsSchemaName)
	if err != nil {
		t.Fatalf("Count(ICN.fbs): %v", err)
	}
	if icnCount < 2 {
		t.Fatalf("ICN.fbs rows after persist = %d, want >= 2", icnCount)
	}
	// Persisting again with identical facts changes UPDATED_AT only, so new
	// rows appear (frames differ) and the store keeps every version.
	if _, err := PersistConnectors(deps); err != nil {
		t.Fatalf("PersistConnectors again: %v", err)
	}
	if again, _ := store.Count(ConnectorsSchemaName); again < icnCount {
		t.Fatalf("ICN.fbs rows shrank: %d -> %d", icnCount, again)
	}
}

func TestConnectorsRoutesServeFramesAndRefuseRunWithoutAFetch(t *testing.T) {
	store := newConnectorsTestStore(t)
	deps := &AdminMountDeps{Store: store, Config: &config.Config{}, NodePeerID: "16Uiu2HAmLocalNodeForConnectorsTest"}
	mux := http.NewServeMux()
	NewConnectorsHandler(deps).RegisterRoutes(mux)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get(ConnectorsPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != StreamContentType {
		t.Fatalf("Content-Type = %q", ct)
	}
	if f := rec.Header().Get(StreamFormatHeader); f != StreamFormat {
		t.Fatalf("stream format = %q", f)
	}
	if s := rec.Header().Get(StreamSchemaHeader); s != ConnectorsSchemaName {
		t.Fatalf("schema header = %q", s)
	}
	frames, err := SplitFrames(rec.Body.Bytes())
	if err != nil || len(frames) != 2 {
		t.Fatalf("frames = %d err=%v", len(frames), err)
	}
	for _, frame := range frames {
		decodeICNFrame(t, frame)
	}
	if rec.Header().Get(StreamRecordCountHeader) != "2" {
		t.Fatalf("record count header = %q", rec.Header().Get(StreamRecordCountHeader))
	}

	// Filters.
	if frames, _ = SplitFrames(get(ConnectorsPath + "?origin=celestrak.org").Body.Bytes()); len(frames) != 2 {
		t.Fatalf("origin filter = %d frames", len(frames))
	}
	if frames, _ = SplitFrames(get(ConnectorsPath + "?origin=nowhere.example").Body.Bytes()); len(frames) != 0 {
		t.Fatalf("unknown origin = %d frames", len(frames))
	}
	if frames, _ = SplitFrames(get(ConnectorsPath + "?schema=SPW").Body.Bytes()); len(frames) != 1 {
		t.Fatalf("schema filter = %d frames", len(frames))
	}
	if frames, _ = SplitFrames(get(ConnectorsPath + "?schema=omm.fbs").Body.Bytes()); len(frames) != 1 {
		t.Fatalf("schema filter (store form) = %d frames", len(frames))
	}

	// Single connector.
	rec = get(ConnectorsPath + "/space-data-network-02%2Fcelestrak-gp")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET one status = %d", rec.Code)
	}
	frames, _ = SplitFrames(rec.Body.Bytes())
	if len(frames) != 1 || string(decodeICNFrame(t, frames[0]).SourceName()) != "celestrak-gp" {
		t.Fatalf("GET one returned %d frames", len(frames))
	}
	rec = get(ConnectorsPath + "/space-data-network-02/celestrak-space-weather")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET one (literal slash) status = %d", rec.Code)
	}
	rec = get(ConnectorsPath + "/nobody%2Fnothing")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connector status = %d", rec.Code)
	}
	frames, _ = SplitFrames(rec.Body.Bytes())
	if len(frames) != 1 {
		t.Fatalf("404 body frames = %d", len(frames))
	}
	if q, err := ParseQRP(frames[0]); err != nil || int8(q.KIND()) != QRPKindError {
		t.Fatalf("404 body is not a $QRP error: %v", err)
	}

	// Run without a fetch owner on this node: 404 + one $QRP.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, ConnectorsPath+"/space-data-network-02%2Fcelestrak-gp/run", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("run status = %d body=%q", rec.Code, rec.Body.String())
	}
	frames, err = SplitFrames(rec.Body.Bytes())
	if err != nil || len(frames) != 1 {
		t.Fatalf("run body frames = %d err=%v", len(frames), err)
	}
	q, err := ParseQRP(frames[0])
	if err != nil {
		t.Fatalf("run body: %v", err)
	}
	if int8(q.KIND()) != QRPKindError || string(q.MESSAGE()) != "This node does not run a fetch for this source." {
		t.Fatalf("run $QRP = kind %d message %q", q.KIND(), string(q.MESSAGE()))
	}

	// Debounced run: 429 + Retry-After + "Next eligible at <UTC ISO>".
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	ledger.RecordFetch(sourcemetrics.Fetch{URL: connectorsTestGPURL, Status: 200})
	ledger.RecordIngest(sourcemetrics.Ingest{
		AppID: "celestrak-gp-ingest", ProviderID: "space-data-network-02", SourceName: "celestrak-gp",
		SourceURL: connectorsTestGPURL, Schema: "OMM.fbs", BatchID: "gp-batch-1", Records: 2, Inserted: 2,
	})
	deps.SourceMetrics = ledger
	deps.FlowServices = func() []FlowServiceInfo {
		return []FlowServiceInfo{{ProgramID: "celestrak-gp-ingest", Running: true, TimerIntervalMs: 3 * 3600 * 1000}}
	}
	deps.RunFlowNow = func(_ context.Context, programID string) (bool, string, error) {
		return true, "last attempt 1m ago is inside the 3h0m0s debounce window", nil
	}
	mux = http.NewServeMux()
	NewConnectorsHandler(deps).RegisterRoutes(mux)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, ConnectorsPath+"/space-data-network-02%2Fcelestrak-gp/run", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("debounced run status = %d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("debounced run has no Retry-After")
	}
	frames, _ = SplitFrames(rec.Body.Bytes())
	q, _ = ParseQRP(frames[0])
	if string(q.ErrorCode()) != "debounce" || !strings.HasPrefix(string(q.MESSAGE()), "Next eligible at ") || q.RetryAfterMs() == 0 {
		t.Fatalf("debounce $QRP = %q %q retry=%d", string(q.ErrorCode()), string(q.MESSAGE()), q.RetryAfterMs())
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimPrefix(string(q.MESSAGE()), "Next eligible at ")); err != nil {
		t.Fatalf("debounce message is not a UTC ISO time: %q", string(q.MESSAGE()))
	}

	// Admitted run: 202 + the connector's $ICN.
	deps.RunFlowNow = func(_ context.Context, programID string) (bool, string, error) { return false, "", nil }
	mux = http.NewServeMux()
	NewConnectorsHandler(deps).RegisterRoutes(mux)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ConnectorsPath+"/space-data-network-02%2Fcelestrak-gp/run", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("admitted run status = %d body=%q", rec.Code, rec.Body.String())
	}
	frames, _ = SplitFrames(rec.Body.Bytes())
	if len(frames) != 1 || string(decodeICNFrame(t, frames[0]).ConnectorId()) != "space-data-network-02/celestrak-gp" {
		t.Fatalf("admitted run body = %d frames", len(frames))
	}
}

func ledgerRow(t *testing.T, ledger *sourcemetrics.Store, sourceName string) sourcemetrics.Source {
	t.Helper()
	rows, err := ledger.Sources()
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	for _, row := range rows {
		if row.SourceName == sourceName {
			return row
		}
	}
	t.Fatalf("no ledger row for %q", sourceName)
	return sourcemetrics.Source{}
}
