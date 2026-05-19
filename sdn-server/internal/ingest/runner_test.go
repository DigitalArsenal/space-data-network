package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	CATFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	OMMFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	SPWFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/SPW"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func newTestRunner(t *testing.T) *Runner {
	t.Helper()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath: filepath.Join(dir, "store"),
		RawPath:     filepath.Join(dir, "raw"),
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})
	return runner
}

func TestNewRunnerEnforcesCelesTrakMinimumCadence(t *testing.T) {
	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:          filepath.Join(dir, "store"),
		RawPath:              filepath.Join(dir, "raw"),
		CelestrakInterval:    time.Minute,
		SatcatInterval:       time.Minute,
		SpaceWeatherInterval: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	if got, want := runner.cfg.CelestrakInterval, minCelestrakFetchInterval; got != want {
		t.Fatalf("CelestrakInterval = %s, want %s", got, want)
	}
	if got, want := runner.cfg.SatcatInterval, minCelestrakFetchInterval; got != want {
		t.Fatalf("SatcatInterval = %s, want %s", got, want)
	}
	if got, want := runner.cfg.SpaceWeatherInterval, minCelestrakFetchInterval; got != want {
		t.Fatalf("SpaceWeatherInterval = %s, want %s", got, want)
	}
}

func TestNewRunnerDefaultsToValidCelestrakSatcatCSVQuery(t *testing.T) {
	runner := newTestRunner(t)

	if !strings.Contains(runner.cfg.CelestrakSatcatCSVURL, "GROUP=active") {
		t.Fatalf("CelestrakSatcatCSVURL = %q, want GROUP=active query", runner.cfg.CelestrakSatcatCSVURL)
	}
	if !strings.Contains(runner.cfg.CelestrakSatcatCSVURL, "FORMAT=CSV") {
		t.Fatalf("CelestrakSatcatCSVURL = %q, want FORMAT=CSV query", runner.cfg.CelestrakSatcatCSVURL)
	}
}

func TestRunnerRefusesCelesTrakSyncWhenStorageDiskBelowGuardrail(t *testing.T) {
	runner := newTestRunner(t)

	oldDiskAvailableBytes := diskAvailableBytes
	diskAvailableBytes = func(string) (uint64, error) {
		return 1024, nil
	}
	t.Cleanup(func() {
		diskAvailableBytes = oldDiskAvailableBytes
	})

	err := runner.syncCelestrakSatcat(context.Background())
	if err == nil {
		t.Fatal("syncCelestrakSatcat succeeded despite low free disk")
	}
	if !strings.Contains(err.Error(), "free disk") {
		t.Fatalf("error = %v, want free disk guardrail", err)
	}
}

func TestIngestSpaceWeatherDataStoresSPWFlatBuffers(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/celestrak-sw-all.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	count, _, err := runner.ingestSpaceWeatherData(fixture, "source:celestrak")
	if err != nil {
		t.Fatalf("ingestSpaceWeatherData failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("ingestSpaceWeatherData stored %d records, want 2", count)
	}

	stored, err := runner.store.QueryAll("SPW.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll SPW failed: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("QueryAll returned %d SPW records, want 2", len(stored))
	}

	byDate := make(map[string]*SPWFB.SPW, len(stored))
	for _, record := range stored {
		spw := SPWFB.GetSizePrefixedRootAsSPW(record, 0)
		byDate[string(spw.Date())] = spw
	}

	latest := byDate["2026-01-02"]
	if latest == nil {
		t.Fatalf("missing SPW record for 2026-01-02")
	}
	if got, want := string(latest.Date()), "2026-01-02"; got != want {
		t.Fatalf("latest DATE = %q, want %q", got, want)
	}
	if got, want := latest.Kp1(), int32(17); got != want {
		t.Fatalf("decimal Kp1 = %d, want %d tenths", got, want)
	}
	if got, want := latest.F107DataType(), SPWFB.F107DataTypeINT; got != want {
		t.Fatalf("F107 data type = %v, want %v", got, want)
	}

	older := byDate["2026-01-01"]
	if older == nil {
		t.Fatalf("missing SPW record for 2026-01-01")
	}
	if got, want := older.Kp1(), int32(10); got != want {
		t.Fatalf("integer Kp1 = %d, want %d tenths", got, want)
	}
	if got, want := older.Ap8(), int32(8); got != want {
		t.Fatalf("AP8 = %d, want %d", got, want)
	}
	if got, want := older.F107Obs(), float32(150.5); got != want {
		t.Fatalf("F107_OBS = %f, want %f", got, want)
	}
}

func TestIngestSpaceWeatherDataRejectsSchemaMismatch(t *testing.T) {
	runner := newTestRunner(t)
	fixture := []byte("NORAD_CAT_ID,OBJECT_NAME,OBJECT_ID\n25544,ISS (ZARYA),1998-067A\n")

	count, _, err := runner.ingestSpaceWeatherData(fixture, "source:celestrak")
	if err == nil {
		t.Fatalf("ingestSpaceWeatherData returned nil error with SPW=%d", count)
	}
	if !strings.Contains(err.Error(), "schema mismatch") {
		t.Fatalf("error = %v, want schema mismatch", err)
	}
}

func TestIngestGPDataRejectsMalformedEpoch(t *testing.T) {
	runner := newTestRunner(t)
	fixture := []byte("NORAD_CAT_ID,OBJECT_NAME,EPOCH,MEAN_MOTION\n25544,ISS (ZARYA),not-a-date,15.5\n")

	countOMM, countMPE, _, err := runner.ingestGPData(fixture, "source:celestrak")
	if err == nil {
		t.Fatalf("ingestGPData returned nil error with OMM=%d MPE=%d", countOMM, countMPE)
	}
	if !strings.Contains(err.Error(), "malformed EPOCH") {
		t.Fatalf("error = %v, want malformed EPOCH", err)
	}
}

func TestSyncCelestrakSpaceWeatherStopsAndAlertsOnStaleSourceTimestamp(t *testing.T) {
	stalePayload := []byte("DATE,BSRN,ND,KP1\n2026-01-01,2600,1,10\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Tue, 05 May 2026 00:00:00 GMT")
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(stalePayload); err != nil {
			t.Fatalf("write stale response: %v", err)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:              filepath.Join(dir, "store"),
		RawPath:                  filepath.Join(dir, "raw"),
		CelestrakSpaceWeatherURL: server.URL + "/SW-All.csv",
		SpaceTrackPollInterval:   time.Hour,
		CelestrakInterval:        minCelestrakFetchInterval,
		SatcatInterval:           minCelestrakFetchInterval,
		SpaceWeatherInterval:     minCelestrakFetchInterval,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	err = runner.syncCelestrakSpaceWeather(context.Background())
	if err == nil {
		t.Fatalf("syncCelestrakSpaceWeather returned nil error for stale payload")
	}
	if !strings.Contains(err.Error(), "stale source timestamp") {
		t.Fatalf("error = %v, want stale source timestamp", err)
	}
	if got := runner.checkpoints.getString("ingest_human_review_required_celestrak_space_weather"); got == "" {
		t.Fatalf("ingest human review checkpoint is empty")
	}
}

func TestSyncCelestrakSpaceWeatherRecordsBatchProvenance(t *testing.T) {
	fixture, err := os.ReadFile("testdata/celestrak-sw-all.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"fixture-spw"`)
		w.Header().Set("Last-Modified", "Fri, 02 Jan 2026 03:04:05 GMT")
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(fixture); err != nil {
			t.Fatalf("write fixture response: %v", err)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:              filepath.Join(dir, "store"),
		RawPath:                  filepath.Join(dir, "raw"),
		CelestrakSpaceWeatherURL: server.URL + "/SW-All.csv",
		SpaceTrackPollInterval:   time.Hour,
		CelestrakInterval:        minCelestrakFetchInterval,
		SatcatInterval:           minCelestrakFetchInterval,
		SpaceWeatherInterval:     minCelestrakFetchInterval,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	if err := runner.syncCelestrakSpaceWeather(context.Background()); err != nil {
		t.Fatalf("syncCelestrakSpaceWeather failed: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "raw", "provenance", "celestrak-space-weather", "*.json"))
	if err != nil {
		t.Fatalf("glob provenance: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d provenance files, want 1: %v", len(matches), matches)
	}

	payload, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}

	var provenance struct {
		SourceURL        string            `json:"source_url"`
		HTTPStatus       int               `json:"http_status"`
		ETag             string            `json:"etag"`
		LastModified     string            `json:"last_modified"`
		RetrievedAt      string            `json:"retrieved_at"`
		ParserVersion    string            `json:"parser_version"`
		SourceSHA256     string            `json:"source_sha256"`
		NormalizedSHA256 string            `json:"normalized_sha256"`
		NormalizedCount  int               `json:"normalized_count"`
		SchemaCounts     map[string]int    `json:"schema_counts"`
		SchemaHashes     map[string]string `json:"schema_hashes"`
		Warnings         []string          `json:"warnings"`
	}
	if err := json.Unmarshal(payload, &provenance); err != nil {
		t.Fatalf("unmarshal provenance: %v\n%s", err, payload)
	}

	sum := sha256.Sum256(fixture)
	if got, want := provenance.SourceURL, server.URL+"/SW-All.csv"; got != want {
		t.Fatalf("source_url = %q, want %q", got, want)
	}
	if got, want := provenance.HTTPStatus, http.StatusOK; got != want {
		t.Fatalf("http_status = %d, want %d", got, want)
	}
	if got, want := provenance.ETag, `"fixture-spw"`; got != want {
		t.Fatalf("etag = %q, want %q", got, want)
	}
	if got, want := provenance.LastModified, "Fri, 02 Jan 2026 03:04:05 GMT"; got != want {
		t.Fatalf("last_modified = %q, want %q", got, want)
	}
	if _, err := time.Parse(time.RFC3339Nano, provenance.RetrievedAt); err != nil {
		t.Fatalf("retrieved_at is not RFC3339Nano: %q", provenance.RetrievedAt)
	}
	if !strings.HasPrefix(provenance.ParserVersion, "celestrak-space-weather/") {
		t.Fatalf("parser_version = %q, want celestrak-space-weather/*", provenance.ParserVersion)
	}
	if got, want := provenance.SourceSHA256, hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("source_sha256 = %q, want %q", got, want)
	}
	if provenance.NormalizedSHA256 == "" {
		t.Fatalf("normalized_sha256 is empty")
	}
	if got, want := provenance.NormalizedCount, 2; got != want {
		t.Fatalf("normalized_count = %d, want %d", got, want)
	}
	if got, want := provenance.SchemaCounts["SPW.fbs"], 2; got != want {
		t.Fatalf("SPW.fbs schema count = %d, want %d", got, want)
	}
	if provenance.SchemaHashes["SPW.fbs"] == "" {
		t.Fatalf("SPW.fbs schema hash is empty")
	}
	if len(provenance.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", provenance.Warnings)
	}

	tagged, err := runner.store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: "SPW.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-space-weather",
		BatchID:    provenance.SourceSHA256,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QuerySourceTaggedRecords failed: %v", err)
	}
	if len(tagged) != 2 {
		t.Fatalf("tagged SPW records = %d, want 2", len(tagged))
	}
	gotTags, err := runner.store.GetSourceTags("SPW.fbs", tagged[0].CID)
	if err != nil {
		t.Fatalf("GetSourceTags failed: %v", err)
	}
	if gotTags.SourceURL != server.URL+"/SW-All.csv" {
		t.Fatalf("tag SourceURL = %q, want %q", gotTags.SourceURL, server.URL+"/SW-All.csv")
	}
	if gotTags.ContentKeyID != "public" {
		t.Fatalf("tag ContentKeyID = %q, want public", gotTags.ContentKeyID)
	}
}

func TestSyncCelestrakSpaceWeatherRequestsDatasetPublication(t *testing.T) {
	fixture, err := os.ReadFile("testdata/celestrak-sw-all.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Fri, 02 Jan 2026 03:04:05 GMT")
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(fixture); err != nil {
			t.Fatalf("write fixture response: %v", err)
		}
	}))
	defer sourceServer.Close()

	publicationRequests := make(chan map[string]any, 1)
	publicationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Fatalf("publication method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/api/v1/admin/dataset-updates/publish"; got != want {
			t.Fatalf("publication path = %s, want %s", got, want)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode publication request: %v", err)
		}
		publicationRequests <- payload
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"schema":"SPW.fbs","recordCount":2,"manifestCid":"bafymanifest"}`))
	}))
	defer publicationServer.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:              filepath.Join(dir, "store"),
		RawPath:                  filepath.Join(dir, "raw"),
		CelestrakSpaceWeatherURL: sourceServer.URL + "/SW-All.csv",
		DatasetPublishURL:        publicationServer.URL + "/api/v1/admin/dataset-updates/publish",
		SpaceTrackPollInterval:   time.Hour,
		CelestrakInterval:        minCelestrakFetchInterval,
		SatcatInterval:           minCelestrakFetchInterval,
		SpaceWeatherInterval:     minCelestrakFetchInterval,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	if err := runner.syncCelestrakSpaceWeather(context.Background()); err != nil {
		t.Fatalf("syncCelestrakSpaceWeather failed: %v", err)
	}

	select {
	case payload := <-publicationRequests:
		if got, want := payload["schema"], "SPW.fbs"; got != want {
			t.Fatalf("schema = %v, want %s", got, want)
		}
		if got, want := payload["providerId"], celestrakProviderID; got != want {
			t.Fatalf("providerId = %v, want %s", got, want)
		}
		if got, want := payload["sourceName"], "celestrak-space-weather"; got != want {
			t.Fatalf("sourceName = %v, want %s", got, want)
		}
		if got, want := payload["combinedCelesTrak"], true; got != want {
			t.Fatalf("combinedCelesTrak = %v, want %v", got, want)
		}
		if got, want := payload["fullCatalog"], true; got != want {
			t.Fatalf("fullCatalog = %v, want %v", got, want)
		}
		if got, want := payload["chunkSize"], float64(50000); got != want {
			t.Fatalf("chunkSize = %v, want %v", got, want)
		}
		if got, want := payload["batchId"], sourceSHA256(fixture); got != want {
			t.Fatalf("batchId = %v, want %s", got, want)
		}
		if _, ok := payload["limit"]; ok {
			t.Fatalf("limit should be omitted for batch dataset publication: %#v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dataset publication request")
	}
}

func TestSyncCelestrakGPPublishesCurrentBatchDeltas(t *testing.T) {
	fixture, err := os.ReadFile("testdata/celestrak-gp-omm.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"fixture-gp"`)
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(fixture); err != nil {
			t.Fatalf("write fixture response: %v", err)
		}
	}))
	defer sourceServer.Close()

	publicationRequests := make(chan map[string]any, 2)
	publicationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode publication request: %v", err)
		}
		publicationRequests <- payload
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"schema":"OMM.fbs","recordCount":2,"manifestCid":"bafymanifest"}`))
	}))
	defer publicationServer.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:            filepath.Join(dir, "store"),
		RawPath:                filepath.Join(dir, "raw"),
		CelestrakCatalogURL:    sourceServer.URL + "/gp.csv",
		DatasetPublishURL:      publicationServer.URL + "/api/v1/admin/dataset-updates/publish",
		SpaceTrackPollInterval: time.Hour,
		CelestrakInterval:      minCelestrakFetchInterval,
		SatcatInterval:         minCelestrakFetchInterval,
		SpaceWeatherInterval:   minCelestrakFetchInterval,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	if err := runner.syncCelestrakGP(context.Background()); err != nil {
		t.Fatalf("syncCelestrakGP failed: %v", err)
	}

	wantBatchID := sourceSHA256(fixture)
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case payload := <-publicationRequests:
			schema, _ := payload["schema"].(string)
			seen[schema] = true
			if got, want := payload["providerId"], celestrakProviderID; got != want {
				t.Fatalf("%s providerId = %v, want %s", schema, got, want)
			}
			if got, want := payload["sourceName"], "celestrak-gp"; got != want {
				t.Fatalf("%s sourceName = %v, want %s", schema, got, want)
			}
			if got := payload["batchId"]; got != wantBatchID {
				t.Fatalf("%s batchId = %v, want %s", schema, got, wantBatchID)
			}
			if got, want := payload["fullCatalog"], true; got != want {
				t.Fatalf("%s fullCatalog = %v, want %v", schema, got, want)
			}
			if got, want := payload["chunkSize"], float64(50000); got != want {
				t.Fatalf("%s chunkSize = %v, want %v", schema, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for GP publication requests")
		}
	}
	if !seen["OMM.fbs"] || !seen["MPE.fbs"] {
		t.Fatalf("publication schemas = %#v, want OMM.fbs and MPE.fbs", seen)
	}
}

func TestSyncCelestrakGPReconcilesDuplicateOMMBatchBeforePublication(t *testing.T) {
	fixture, err := os.ReadFile("testdata/celestrak-gp-omm.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"fixture-gp"`)
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(fixture); err != nil {
			t.Fatalf("write fixture response: %v", err)
		}
	}))
	defer sourceServer.Close()

	publicationRequests := make(chan map[string]any, 2)
	publicationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode publication request: %v", err)
		}
		publicationRequests <- payload
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"schema":"OMM.fbs","recordCount":2,"manifestCid":"bafymanifest"}`))
	}))
	defer publicationServer.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:            filepath.Join(dir, "store"),
		RawPath:                filepath.Join(dir, "raw"),
		CelestrakCatalogURL:    sourceServer.URL + "/gp.csv",
		DatasetPublishURL:      publicationServer.URL + "/api/v1/admin/dataset-updates/publish",
		SpaceTrackPollInterval: time.Hour,
		CelestrakInterval:      minCelestrakFetchInterval,
		SatcatInterval:         minCelestrakFetchInterval,
		SpaceWeatherInterval:   minCelestrakFetchInterval,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	tags := sourceTags(celestrakProviderID, "celestrak-gp", sourceServer.URL+"/gp.csv", fixture)
	oldDuplicate := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithEpoch("2026-01-01T00:00:00.000000").
		WithCreationDate("2025-12-31T23:59:59Z").
		Build()
	if _, err := runner.store.StoreWithSourceTags("OMM.fbs", oldDuplicate, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store old duplicate OMM failed: %v", err)
	}

	if err := runner.syncCelestrakGP(context.Background()); err != nil {
		t.Fatalf("syncCelestrakGP failed: %v", err)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-publicationRequests:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for GP publication requests")
		}
	}

	ommCount, err := runner.store.CountRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: celestrakProviderID,
		SourceName: "celestrak-gp",
		BatchID:    tags.BatchID,
	})
	if err != nil {
		t.Fatalf("CountRawRecords OMM failed: %v", err)
	}
	if ommCount != 2 {
		t.Fatalf("OMM source batch count = %d, want 2 after duplicate reconciliation", ommCount)
	}
}

func TestIngestGPDataIsIdempotentForSameSourceBatch(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/celestrak-gp-omm.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	tags := sourceTags(celestrakProviderID, "celestrak-gp", "https://celestrak.example/gp.csv", fixture)

	countOMM, countMPE, firstHash, err := runner.ingestGPData(fixture, "source:celestrak", tags)
	if err != nil {
		t.Fatalf("first ingestGPData failed: %v", err)
	}
	if countOMM != 2 || countMPE != 2 {
		t.Fatalf("first ingest stored OMM=%d MPE=%d, want 2 each", countOMM, countMPE)
	}
	time.Sleep(1100 * time.Millisecond)
	countOMM, countMPE, secondHash, err := runner.ingestGPData(fixture, "source:celestrak", tags)
	if err != nil {
		t.Fatalf("second ingestGPData failed: %v", err)
	}
	if countOMM != 2 || countMPE != 2 {
		t.Fatalf("second ingest parsed OMM=%d MPE=%d, want 2 each", countOMM, countMPE)
	}
	if firstHash != secondHash {
		t.Fatalf("normalized ingest hash changed across identical batch: first=%s second=%s", firstHash, secondHash)
	}

	ommCount, err := runner.store.CountRawRecords(storage.RawRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: celestrakProviderID,
		SourceName: "celestrak-gp",
		BatchID:    tags.BatchID,
	})
	if err != nil {
		t.Fatalf("CountRawRecords OMM failed: %v", err)
	}
	if ommCount != 2 {
		t.Fatalf("OMM source batch count = %d, want 2 after replaying identical batch", ommCount)
	}
	mpeCount, err := runner.store.CountRawRecords(storage.RawRecordQuery{
		SchemaName: "MPE.fbs",
		ProviderID: celestrakProviderID,
		SourceName: "celestrak-gp",
		BatchID:    tags.BatchID,
	})
	if err != nil {
		t.Fatalf("CountRawRecords MPE failed: %v", err)
	}
	if mpeCount != 2 {
		t.Fatalf("MPE source batch count = %d, want 2 after replaying identical batch", mpeCount)
	}
}

func TestSyncCelestrakSatcatPublishesCurrentBatchDeltas(t *testing.T) {
	legacyFixture, err := os.ReadFile("testdata/celestrak-satcat.txt")
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}
	csvFixture, err := os.ReadFile("testdata/celestrak-satcat.csv")
	if err != nil {
		t.Fatalf("read csv fixture: %v", err)
	}

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/satcat.txt":
			w.Header().Set("ETag", `"fixture-satcat-legacy"`)
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(legacyFixture)
		case "/satcat.csv":
			w.Header().Set("ETag", `"fixture-satcat-csv"`)
			w.Header().Set("Content-Type", "text/csv")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(csvFixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer sourceServer.Close()

	publicationRequests := make(chan map[string]any, 2)
	publicationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode publication request: %v", err)
		}
		publicationRequests <- payload
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"schema":"CAT.fbs","recordCount":2,"manifestCid":"bafymanifest"}`))
	}))
	defer publicationServer.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:            filepath.Join(dir, "store"),
		RawPath:                filepath.Join(dir, "raw"),
		CelestrakSatcatURL:     sourceServer.URL + "/satcat.txt",
		CelestrakSatcatCSVURL:  sourceServer.URL + "/satcat.csv",
		DatasetPublishURL:      publicationServer.URL + "/api/v1/admin/dataset-updates/publish",
		SpaceTrackPollInterval: time.Hour,
		CelestrakInterval:      minCelestrakFetchInterval,
		SatcatInterval:         minCelestrakFetchInterval,
		SpaceWeatherInterval:   minCelestrakFetchInterval,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	if err := runner.syncCelestrakSatcat(context.Background()); err != nil {
		t.Fatalf("syncCelestrakSatcat failed: %v", err)
	}

	wantBatchBySource := map[string]string{
		"celestrak-satcat":     sourceSHA256(legacyFixture),
		"celestrak-satcat-csv": sourceSHA256(csvFixture),
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case payload := <-publicationRequests:
			sourceName, _ := payload["sourceName"].(string)
			seen[sourceName] = true
			if got, want := payload["schema"], "CAT.fbs"; got != want {
				t.Fatalf("%s schema = %v, want %s", sourceName, got, want)
			}
			if got, want := payload["providerId"], celestrakProviderID; got != want {
				t.Fatalf("%s providerId = %v, want %s", sourceName, got, want)
			}
			if got, want := payload["batchId"], wantBatchBySource[sourceName]; got != want {
				t.Fatalf("%s batchId = %v, want %s", sourceName, got, want)
			}
			if got, want := payload["fullCatalog"], true; got != want {
				t.Fatalf("%s fullCatalog = %v, want %v", sourceName, got, want)
			}
			if got, want := payload["chunkSize"], float64(50000); got != want {
				t.Fatalf("%s chunkSize = %v, want %v", sourceName, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for SATCAT publication requests")
		}
	}
	if !seen["celestrak-satcat"] || !seen["celestrak-satcat-csv"] {
		t.Fatalf("publication sources = %#v, want both SATCAT feeds", seen)
	}
}

func TestRunCycleStopsAfterContextCancellation(t *testing.T) {
	legacyFixture, err := os.ReadFile("testdata/celestrak-satcat.txt")
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}
	csvFixture, err := os.ReadFile("testdata/celestrak-satcat.csv")
	if err != nil {
		t.Fatalf("read csv fixture: %v", err)
	}

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "raw", "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "celestrak-satcat.txt"), legacyFixture, 0644); err != nil {
		t.Fatalf("write legacy cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "celestrak-satcat.csv"), csvFixture, 0644); err != nil {
		t.Fatalf("write csv cache: %v", err)
	}

	runner, err := NewRunner(Config{
		StoragePath:            filepath.Join(dir, "store"),
		RawPath:                filepath.Join(dir, "raw"),
		CelestrakCatalogURL:    "http://127.0.0.1:1/gp.csv",
		CelestrakSatcatURL:     "http://127.0.0.1:1/satcat.txt",
		CelestrakSatcatCSVURL:  "http://127.0.0.1:1/satcat.csv",
		SpaceTrackPollInterval: time.Hour,
		CelestrakInterval:      minCelestrakFetchInterval,
		SatcatInterval:         minCelestrakFetchInterval,
		SpaceWeatherInterval:   minCelestrakFetchInterval,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = runner.runCycle(ctx)
	if err == nil {
		t.Fatal("runCycle returned nil for canceled context")
	}
	if got := runner.checkpoints.getString("celestrak_satcat_last_success"); got != "" {
		t.Fatalf("SATCAT checkpoint advanced after cancellation: %q", got)
	}
	catCount, countErr := runner.store.CountRawRecords(storage.RawRecordQuery{SchemaName: "CAT.fbs"})
	if countErr != nil {
		t.Fatalf("CountRawRecords CAT failed: %v", countErr)
	}
	if catCount != 0 {
		t.Fatalf("CAT rows = %d, want 0 after canceled run cycle", catCount)
	}
}

func TestSyncCelestrakSpaceWeatherFailsWhenConfiguredPublicationFails(t *testing.T) {
	fixture, err := os.ReadFile("testdata/celestrak-sw-all.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Fri, 02 Jan 2026 03:04:05 GMT")
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture)
	}))
	defer sourceServer.Close()

	publicationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "pinning unavailable", http.StatusServiceUnavailable)
	}))
	defer publicationServer.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:              filepath.Join(dir, "store"),
		RawPath:                  filepath.Join(dir, "raw"),
		CelestrakSpaceWeatherURL: sourceServer.URL + "/SW-All.csv",
		DatasetPublishURL:        publicationServer.URL + "/api/v1/admin/dataset-updates/publish",
		SpaceTrackPollInterval:   time.Hour,
		CelestrakInterval:        minCelestrakFetchInterval,
		SatcatInterval:           minCelestrakFetchInterval,
		SpaceWeatherInterval:     minCelestrakFetchInterval,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	err = runner.syncCelestrakSpaceWeather(context.Background())
	if err == nil {
		t.Fatal("syncCelestrakSpaceWeather succeeded despite publication failure")
	}
	if !strings.Contains(err.Error(), "publish celestrak SPW dataset update") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := runner.checkpoints.getString("celestrak_space_weather_last_success"); got != "" {
		t.Fatalf("success checkpoint was advanced despite publication failure: %q", got)
	}
}

func TestRequestDatasetPublicationUsesLongTimeoutForFullCatalog(t *testing.T) {
	publicationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer publicationServer.Close()

	runner := &Runner{
		cfg: Config{
			DatasetPublishURL: publicationServer.URL + "/api/v1/admin/dataset-updates/publish",
		},
		httpClient: &http.Client{
			Timeout: time.Nanosecond,
		},
	}

	err := runner.requestDatasetPublication(context.Background(), datasetPublicationRequest{
		Schema:      "OMM.fbs",
		ProviderID:  celestrakProviderID,
		SourceName:  "celestrak-gp",
		FullCatalog: true,
		ChunkSize:   datasetPublicationChunkSize,
	})
	if err != nil {
		t.Fatalf("requestDatasetPublication failed: %v", err)
	}
}

func TestSyncSpaceTrackGapFillRecordsBatchProvenance(t *testing.T) {
	fixture, err := os.ReadFile("testdata/celestrak-gp-omm.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Method != http.MethodPost {
				t.Fatalf("login method = %s, want POST", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		case "/gp":
			w.Header().Set("ETag", `"fixture-gp"`)
			w.Header().Set("Content-Type", "text/csv")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(fixture); err != nil {
				t.Fatalf("write fixture response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	startDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	runner, err := NewRunner(Config{
		StoragePath:          filepath.Join(dir, "store"),
		RawPath:              filepath.Join(dir, "raw"),
		SpaceTrackEnabled:    true,
		SpaceTrackIdentity:   "test-user",
		SpaceTrackPassword:   "test-password",
		SpaceTrackLoginURL:   server.URL + "/login",
		SpaceTrackQueryTmpl:  server.URL + "/gp?start=%s&end=%s",
		SpaceTrackStartDay:   startDay,
		SpaceTrackBatchDays:  1,
		SpaceTrackBatchSleep: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	if err := runner.syncSpaceTrackGapFill(context.Background()); err != nil {
		t.Fatalf("syncSpaceTrackGapFill failed: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "raw", "provenance", "spacetrack-gp-history", "*.json"))
	if err != nil {
		t.Fatalf("glob provenance: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d provenance files, want 1: %v", len(matches), matches)
	}

	payload, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	var provenance struct {
		SourceURL        string            `json:"source_url"`
		HTTPStatus       int               `json:"http_status"`
		ETag             string            `json:"etag"`
		ParserVersion    string            `json:"parser_version"`
		NormalizedSHA256 string            `json:"normalized_sha256"`
		NormalizedCount  int               `json:"normalized_count"`
		SchemaCounts     map[string]int    `json:"schema_counts"`
		SchemaHashes     map[string]string `json:"schema_hashes"`
	}
	if err := json.Unmarshal(payload, &provenance); err != nil {
		t.Fatalf("unmarshal provenance: %v\n%s", err, payload)
	}
	if !strings.HasPrefix(provenance.SourceURL, server.URL+"/gp?start=") {
		t.Fatalf("source_url = %q, want Space-Track GP test URL", provenance.SourceURL)
	}
	if got, want := provenance.HTTPStatus, http.StatusOK; got != want {
		t.Fatalf("http_status = %d, want %d", got, want)
	}
	if got, want := provenance.ETag, `"fixture-gp"`; got != want {
		t.Fatalf("etag = %q, want %q", got, want)
	}
	if got, want := provenance.ParserVersion, "spacetrack-gp-history/v1"; got != want {
		t.Fatalf("parser_version = %q, want %q", got, want)
	}
	if provenance.NormalizedSHA256 == "" {
		t.Fatalf("normalized_sha256 is empty")
	}
	if got, want := provenance.NormalizedCount, 4; got != want {
		t.Fatalf("normalized_count = %d, want %d", got, want)
	}
	if got, want := provenance.SchemaCounts["OMM.fbs"], 2; got != want {
		t.Fatalf("OMM.fbs schema count = %d, want %d", got, want)
	}
	if got, want := provenance.SchemaCounts["MPE.fbs"], 2; got != want {
		t.Fatalf("MPE.fbs schema count = %d, want %d", got, want)
	}
	if provenance.SchemaHashes["OMM.fbs"] == "" {
		t.Fatalf("OMM.fbs schema hash is empty")
	}
	if provenance.SchemaHashes["MPE.fbs"] == "" {
		t.Fatalf("MPE.fbs schema hash is empty")
	}
}

func TestSyncCelestrakSatcatFetchesLegacyAndCSV(t *testing.T) {
	legacyFixture, err := os.ReadFile("testdata/celestrak-satcat.txt")
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}
	csvFixture, err := os.ReadFile("testdata/celestrak-satcat.csv")
	if err != nil {
		t.Fatalf("read CSV fixture: %v", err)
	}

	var legacyRequests, csvRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pub/satcat.txt":
			legacyRequests++
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(legacyFixture); err != nil {
				t.Fatalf("write legacy fixture response: %v", err)
			}
		case "/satcat/records.php":
			csvRequests++
			if got, want := r.URL.Query().Get("GROUP"), "active"; got != want {
				t.Fatalf("GROUP query = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("FORMAT"), "CSV"; got != want {
				t.Fatalf("FORMAT query = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "text/csv")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(csvFixture); err != nil {
				t.Fatalf("write CSV fixture response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:            filepath.Join(dir, "store"),
		RawPath:                filepath.Join(dir, "raw"),
		CelestrakSatcatURL:     server.URL + "/pub/satcat.txt",
		CelestrakSatcatCSVURL:  server.URL + "/satcat/records.php?GROUP=active&FORMAT=CSV",
		SatcatInterval:         minCelestrakFetchInterval,
		SpaceTrackPollInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	if err := runner.syncCelestrakSatcat(context.Background()); err != nil {
		t.Fatalf("syncCelestrakSatcat failed: %v", err)
	}
	if legacyRequests != 1 {
		t.Fatalf("legacyRequests = %d, want 1", legacyRequests)
	}
	if csvRequests != 1 {
		t.Fatalf("csvRequests = %d, want 1", csvRequests)
	}

	stored, err := runner.store.QueryAll("CAT.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll CAT failed: %v", err)
	}
	if len(stored) != 4 {
		t.Fatalf("stored CAT records = %d, want 4", len(stored))
	}

	matches, err := filepath.Glob(filepath.Join(dir, "raw", "provenance", "celestrak-satcat-csv", "*.json"))
	if err != nil {
		t.Fatalf("glob CSV provenance: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("CSV provenance files = %d, want 1: %v", len(matches), matches)
	}
}

func TestFetchWithCacheHardStopsOnForbiddenInsteadOfUsingStaleCache(t *testing.T) {
	runner := newTestRunner(t)
	cachePath := filepath.Join(runner.cfg.RawPath, "cache", "celestrak-gp.csv")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("stale payload"), 0644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	old := time.Now().Add(-2 * minCelestrakFetchInterval)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatalf("age cache: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	data, metadata, err := runner.fetchWithCache(context.Background(), server.URL+"/gp.csv", "celestrak-gp.csv", minCelestrakFetchInterval)
	if err == nil {
		t.Fatalf("fetchWithCache returned nil error with data=%q metadata=%+v", data, metadata)
	}
	if metadata.FromCache {
		t.Fatalf("metadata.FromCache = true, want false for 403 hard stop")
	}
	if metadata.HTTPStatus != http.StatusForbidden {
		t.Fatalf("metadata.HTTPStatus = %d, want %d", metadata.HTTPStatus, http.StatusForbidden)
	}
}

func TestFetchWithCacheHardStopsOnRedirectInsteadOfUsingStaleCache(t *testing.T) {
	runner := newTestRunner(t)
	cachePath := filepath.Join(runner.cfg.RawPath, "cache", "celestrak-gp.csv")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("stale payload"), 0644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	old := time.Now().Add(-2 * minCelestrakFetchInterval)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatalf("age cache: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer server.Close()

	data, metadata, err := runner.fetchWithCache(context.Background(), server.URL+"/gp.csv", "celestrak-gp.csv", minCelestrakFetchInterval)
	if err == nil {
		t.Fatalf("fetchWithCache returned nil error with data=%q metadata=%+v", data, metadata)
	}
	if metadata.FromCache {
		t.Fatalf("metadata.FromCache = true, want false for redirect hard stop")
	}
	if metadata.HTTPStatus != http.StatusFound {
		t.Fatalf("metadata.HTTPStatus = %d, want %d", metadata.HTTPStatus, http.StatusFound)
	}
}

func TestFetchWithCacheHardStopsOnNotFoundInsteadOfUsingStaleCache(t *testing.T) {
	runner := newTestRunner(t)
	cachePath := filepath.Join(runner.cfg.RawPath, "cache", "celestrak-satcat.txt")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("stale payload"), 0644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	old := time.Now().Add(-2 * minCelestrakFetchInterval)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatalf("age cache: %v", err)
	}

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	data, metadata, err := runner.fetchWithCache(context.Background(), server.URL+"/satcat.txt", "celestrak-satcat.txt", minCelestrakFetchInterval)
	if err == nil {
		t.Fatalf("fetchWithCache returned nil error with data=%q metadata=%+v", data, metadata)
	}
	if metadata.FromCache {
		t.Fatalf("metadata.FromCache = true, want false for 404 hard stop")
	}
	if metadata.HTTPStatus != http.StatusNotFound {
		t.Fatalf("metadata.HTTPStatus = %d, want %d", metadata.HTTPStatus, http.StatusNotFound)
	}
}

func TestFetchWithCacheUsesConditionalValidatorsForStaleCache(t *testing.T) {
	runner := newTestRunner(t)
	cacheName := "celestrak-space-weather.csv"
	cachePath := filepath.Join(runner.cfg.RawPath, "cache", cacheName)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	cachedPayload := []byte("cached payload")
	if err := os.WriteFile(cachePath, cachedPayload, 0644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	old := time.Now().Add(-2 * minCelestrakFetchInterval)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatalf("age cache: %v", err)
	}
	cacheMetadata := fetchMetadata{
		SourceURL:    "https://celestrak.example/SW-All.csv",
		HTTPStatus:   http.StatusOK,
		ETag:         `"fixture-etag"`,
		LastModified: "Fri, 02 Jan 2026 03:04:05 GMT",
		RetrievedAt:  old,
	}
	metadataBytes, err := json.Marshal(cacheMetadata)
	if err != nil {
		t.Fatalf("marshal cache metadata: %v", err)
	}
	if err := os.WriteFile(cachePath+".metadata.json", metadataBytes, 0644); err != nil {
		t.Fatalf("write cache metadata: %v", err)
	}

	sawConditionalHeaders := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"fixture-etag"` &&
			r.Header.Get("If-Modified-Since") == "Fri, 02 Jan 2026 03:04:05 GMT" {
			sawConditionalHeaders = true
			w.WriteHeader(http.StatusNotModified)
			return
		}
		http.Error(w, "missing conditional headers", http.StatusBadRequest)
	}))
	defer server.Close()

	data, metadata, err := runner.fetchWithCache(context.Background(), server.URL+"/SW-All.csv", cacheName, minCelestrakFetchInterval)
	if err != nil {
		t.Fatalf("fetchWithCache failed: %v", err)
	}
	if !sawConditionalHeaders {
		t.Fatalf("server did not receive If-None-Match and If-Modified-Since")
	}
	if !bytes.Equal(data, cachedPayload) {
		t.Fatalf("data = %q, want cached payload %q", data, cachedPayload)
	}
	if !metadata.FromCache {
		t.Fatalf("metadata.FromCache = false, want true for 304")
	}
	if metadata.HTTPStatus != http.StatusNotModified {
		t.Fatalf("metadata.HTTPStatus = %d, want %d", metadata.HTTPStatus, http.StatusNotModified)
	}
	if metadata.ETag != `"fixture-etag"` {
		t.Fatalf("metadata.ETag = %q, want fixture etag", metadata.ETag)
	}
}

func TestFetchWithCacheRetriesTransientServerFailures(t *testing.T) {
	runner := newTestRunner(t)
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "temporary upstream error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", `"retry-success"`)
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("fresh payload")); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer server.Close()

	data, metadata, err := runner.fetchWithCache(context.Background(), server.URL+"/gp.csv", "celestrak-gp.csv", minCelestrakFetchInterval)
	if err != nil {
		t.Fatalf("fetchWithCache failed: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if got, want := string(data), "fresh payload"; got != want {
		t.Fatalf("data = %q, want %q", got, want)
	}
	if metadata.HTTPStatus != http.StatusOK {
		t.Fatalf("metadata.HTTPStatus = %d, want 200", metadata.HTTPStatus)
	}
	if failureCount := runner.checkpoints.getString("fetch_failure_count_celestrak_gp_csv"); failureCount != "" {
		t.Fatalf("failure checkpoint = %q, want cleared", failureCount)
	}
}

func TestFetchWithCacheMarksHumanReviewAfterRetryBudgetExhausted(t *testing.T) {
	runner := newTestRunner(t)
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "temporary upstream error", http.StatusInternalServerError)
	}))
	defer server.Close()

	data, metadata, err := runner.fetchWithCache(context.Background(), server.URL+"/gp.csv", "celestrak-gp.csv", minCelestrakFetchInterval)
	if err == nil {
		t.Fatalf("fetchWithCache returned nil error with data=%q metadata=%+v", data, metadata)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if got, want := runner.checkpoints.getString("fetch_failure_count_celestrak_gp_csv"), "3"; got != want {
		t.Fatalf("failure count checkpoint = %q, want %q", got, want)
	}
	if got := runner.checkpoints.getString("fetch_human_review_required_celestrak_gp_csv"); got == "" {
		t.Fatalf("human review checkpoint is empty")
	}
}

func TestIngestGPDataStoresOMMAndMPEFlatBuffers(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/celestrak-gp-omm.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	countOMM, countMPE, _, err := runner.ingestGPData(fixture, "source:celestrak")
	if err != nil {
		t.Fatalf("ingestGPData failed: %v", err)
	}
	if countOMM != 2 || countMPE != 2 {
		t.Fatalf("ingestGPData stored OMM=%d MPE=%d, want 2 each", countOMM, countMPE)
	}

	ommRecords, err := runner.store.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll OMM failed: %v", err)
	}
	if len(ommRecords) != 2 {
		t.Fatalf("QueryAll OMM returned %d records, want 2", len(ommRecords))
	}
	mpeRecords, err := runner.store.QueryAll("MPE.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll MPE failed: %v", err)
	}
	if len(mpeRecords) != 2 {
		t.Fatalf("QueryAll MPE returned %d records, want 2", len(mpeRecords))
	}

	byNorad := make(map[uint32]*OMMFB.OMM, len(ommRecords))
	for _, record := range ommRecords {
		omm := OMMFB.GetSizePrefixedRootAsOMM(record, 0)
		byNorad[omm.NoradCatId()] = omm
	}
	iss := byNorad[25544]
	if iss == nil {
		t.Fatalf("missing OMM record for NORAD 25544")
	}
	if got, want := string(iss.ObjectName()), "ISS (ZARYA)"; got != want {
		t.Fatalf("OBJECT_NAME = %q, want %q", got, want)
	}
	if got, want := string(iss.ObjectId()), "1998-067A"; got != want {
		t.Fatalf("OBJECT_ID = %q, want %q", got, want)
	}
	if got, want := iss.MeanMotion(), 15.48962367; got != want {
		t.Fatalf("MEAN_MOTION = %.8f, want %.8f", got, want)
	}
	if got, want := iss.Eccentricity(), 0.0006703; got != want {
		t.Fatalf("ECCENTRICITY = %.7f, want %.7f", got, want)
	}
}

func TestIngestGPDataRejectsParseCountAnomaly(t *testing.T) {
	runner := newTestRunner(t)
	fixture := []byte("NORAD_CAT_ID,OBJECT_NAME,EPOCH\n,NO ID,2026-01-01T00:00:00Z\n")

	countOMM, countMPE, _, err := runner.ingestGPData(fixture, "source:celestrak")
	if err == nil {
		t.Fatalf("ingestGPData returned nil error with OMM=%d MPE=%d", countOMM, countMPE)
	}
	if !strings.Contains(err.Error(), "no OMM rows parsed") {
		t.Fatalf("error = %v, want no OMM rows parsed", err)
	}
}

func TestIngestSatcatDataStoresCATFlatBuffers(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/celestrak-satcat.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	count, _, err := runner.ingestSatcatData(fixture, "source:celestrak")
	if err != nil {
		t.Fatalf("ingestSatcatData failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("ingestSatcatData stored %d records, want 2", count)
	}

	stored, err := runner.store.QueryAll("CAT.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll CAT failed: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("QueryAll CAT returned %d records, want 2", len(stored))
	}

	byNorad := make(map[uint32]*CATFB.CAT, len(stored))
	for _, record := range stored {
		cat := CATFB.GetSizePrefixedRootAsCAT(record, 0)
		byNorad[cat.NoradCatId()] = cat
	}
	iss := byNorad[25544]
	if iss == nil {
		t.Fatalf("missing CAT record for NORAD 25544")
	}
	if got, want := string(iss.ObjectName()), "ISS (ZARYA)"; got != want {
		t.Fatalf("OBJECT_NAME = %q, want %q", got, want)
	}
	if got, want := string(iss.ObjectId()), "1998-067A"; got != want {
		t.Fatalf("OBJECT_ID = %q, want %q", got, want)
	}
	if got, want := string(iss.LaunchDate()), "1998-11-20"; got != want {
		t.Fatalf("LAUNCH_DATE = %q, want %q", got, want)
	}
	if got, want := iss.Period(), 92.68; got != want {
		t.Fatalf("PERIOD = %.2f, want %.2f", got, want)
	}
	if got, want := iss.Maneuverable(), false; got != want {
		t.Fatalf("MANEUVERABLE = %t, want %t", got, want)
	}

	starlink := byNorad[40909]
	if starlink == nil {
		t.Fatalf("missing CAT record for NORAD 40909")
	}
	if got, want := starlink.Maneuverable(), false; got != want {
		t.Fatalf("legacy fixed-width MANEUVERABLE = %t, want %t", got, want)
	}
	if got, want := starlink.Mass(), 0.0; got != want {
		t.Fatalf("legacy fixed-width MASS = %.1f, want %.1f", got, want)
	}
	if got, want := starlink.Size(), 0.0; got != want {
		t.Fatalf("legacy fixed-width SIZE = %.1f, want %.1f", got, want)
	}
	if got, want := starlink.Rcs(), 10.5; got != want {
		t.Fatalf("legacy fixed-width RCS = %.1f, want %.1f", got, want)
	}
}

func TestIngestSatcatDataRejectsParseCountAnomaly(t *testing.T) {
	runner := newTestRunner(t)
	fixture := []byte("NORAD_CAT_ID,OBJECT_NAME,OBJECT_ID\n,NO ID,1998-067A\n")

	count, _, err := runner.ingestSatcatData(fixture, "source:celestrak")
	if err == nil {
		t.Fatalf("ingestSatcatData returned nil error with CAT=%d", count)
	}
	if !strings.Contains(err.Error(), "no CAT rows parsed") {
		t.Fatalf("error = %v, want no CAT rows parsed", err)
	}
}

func TestIngestSatcatCSVDataStoresCATFlatBuffers(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/celestrak-satcat.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	count, _, err := runner.ingestSatcatData(fixture, "source:celestrak")
	if err != nil {
		t.Fatalf("ingestSatcatData failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("ingestSatcatData stored %d records, want 2", count)
	}

	stored, err := runner.store.QueryAll("CAT.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll CAT failed: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("QueryAll CAT returned %d records, want 2", len(stored))
	}

	byNorad := make(map[uint32]*CATFB.CAT, len(stored))
	for _, record := range stored {
		cat := CATFB.GetSizePrefixedRootAsCAT(record, 0)
		byNorad[cat.NoradCatId()] = cat
	}
	starlink := byNorad[40909]
	if starlink == nil {
		t.Fatalf("missing CAT record for NORAD 40909")
	}
	if got, want := string(starlink.ObjectName()), "STARLINK-1001"; got != want {
		t.Fatalf("OBJECT_NAME = %q, want %q", got, want)
	}
	if got, want := string(starlink.ObjectId()), "2015-049A"; got != want {
		t.Fatalf("OBJECT_ID = %q, want %q", got, want)
	}
	if got, want := starlink.Mass(), 260.5; got != want {
		t.Fatalf("MASS = %.1f, want %.1f", got, want)
	}
	if got, want := starlink.Maneuverable(), false; got != want {
		t.Fatalf("MANEUVERABLE = %t, want %t", got, want)
	}
	if got, want := starlink.ObjectType().String(), "PAYLOAD"; got != want {
		t.Fatalf("OBJECT_TYPE = %q, want %q", got, want)
	}
	if got, want := starlink.OpsStatusCode().String(), "OPERATIONAL"; got != want {
		t.Fatalf("OPS_STATUS_CODE = %q, want %q", got, want)
	}
}

func TestIngestSatcatCSVDataDoesNotInventPhysicalFields(t *testing.T) {
	runner := newTestRunner(t)
	fixture := []byte(strings.Join([]string{
		"OBJECT_NAME,OBJECT_ID,NORAD_CAT_ID,OBJECT_TYPE,OPS_STATUS_CODE,OWNER,LAUNCH_DATE,LAUNCH_SITE,DECAY_DATE,PERIOD,INCLINATION,APOGEE,PERIGEE,RCS,DATA_STATUS_CODE,ORBIT_CENTER,ORBIT_TYPE",
		"COSMOS 2251 DEB,1993-036AAB,33757,DEB,D,CIS,1993-06-16,PKM,,94.69,74.0,807,770,0.01,,EA,ORB",
		"",
	}, "\n"))

	count, _, err := runner.ingestSatcatData(fixture, "source:celestrak")
	if err != nil {
		t.Fatalf("ingestSatcatData failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("ingestSatcatData stored %d records, want 1", count)
	}

	stored, err := runner.store.QueryAll("CAT.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll CAT failed: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("QueryAll CAT returned %d records, want 1", len(stored))
	}

	cat := CATFB.GetSizePrefixedRootAsCAT(stored[0], 0)
	if got, want := cat.ObjectType().String(), "DEBRIS"; got != want {
		t.Fatalf("OBJECT_TYPE = %q, want %q", got, want)
	}
	if got, want := cat.OpsStatusCode().String(), "DECAYED"; got != want {
		t.Fatalf("OPS_STATUS_CODE = %q, want %q", got, want)
	}
	if got, want := cat.Maneuverable(), false; got != want {
		t.Fatalf("MANEUVERABLE = %t, want %t", got, want)
	}
	if got, want := cat.Mass(), 0.0; got != want {
		t.Fatalf("MASS = %.1f, want %.1f", got, want)
	}
	if got, want := cat.Size(), 0.0; got != want {
		t.Fatalf("SIZE = %.1f, want %.1f", got, want)
	}
	if got, want := cat.Rcs(), 0.01; got != want {
		t.Fatalf("RCS = %.2f, want %.2f", got, want)
	}
}
