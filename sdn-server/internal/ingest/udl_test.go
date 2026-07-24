package ingest

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	OMMFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	SPWFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/SPW"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	testUDLUsername = "udl-user"
	testUDLPassword = "udl-pass"
)

func newUDLTestRunner(t *testing.T, baseURL string, maxResults int) *Runner {
	t.Helper()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:            filepath.Join(dir, "store"),
		RawPath:                filepath.Join(dir, "raw"),
		SpaceTrackPollInterval: time.Hour,
		UDLEnabled:             true,
		UDLUsername:            testUDLUsername,
		UDLPassword:            testUDLPassword,
		UDLBaseURL:             baseURL,
		UDLStartDay:            time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"),
		UDLBatchDays:           3,
		UDLBatchSleep:          time.Millisecond,
		UDLMaxResults:          maxResults,
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

func requireUDLBasicAuth(t *testing.T, r *http.Request) {
	t.Helper()
	user, pass, ok := r.BasicAuth()
	if !ok {
		t.Errorf("UDL request %s is missing basic auth header", r.URL)
		return
	}
	if user != testUDLUsername || pass != testUDLPassword {
		t.Errorf("UDL basic auth = %q/%q, want %q/%q", user, pass, testUDLUsername, testUDLPassword)
	}
}

func loadUDLFixtureRecords(t *testing.T, name string) []map[string]any {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var records []map[string]any
	if err := json.Unmarshal(payload, &records); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", name, err)
	}
	return records
}

func TestSyncUDLElsetsIngestsOMMWithAuthPagingAndCheckpoint(t *testing.T) {
	fixtureRecords := loadUDLFixtureRecords(t, "udl-elset.json")

	var (
		mu       sync.Mutex
		requests int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/udl/elset" {
			http.NotFound(w, r)
			return
		}
		requireUDLBasicAuth(t, r)

		epochRange := r.URL.Query().Get("epoch")
		if !strings.Contains(epochRange, "..") {
			t.Errorf("epoch query = %q, want start..end range", epochRange)
		}
		maxResults, err := strconv.Atoi(r.URL.Query().Get("maxResults"))
		if err != nil || maxResults != 2 {
			t.Errorf("maxResults query = %q, want 2", r.URL.Query().Get("maxResults"))
		}
		first := 0
		if raw := r.URL.Query().Get("firstResult"); raw != "" {
			first, err = strconv.Atoi(raw)
			if err != nil {
				t.Errorf("firstResult query = %q, want integer", raw)
			}
		}

		mu.Lock()
		requests++
		mu.Unlock()

		end := first + maxResults
		if end > len(fixtureRecords) {
			end = len(fixtureRecords)
		}
		page := []map[string]any{}
		if first < len(fixtureRecords) {
			page = fixtureRecords[first:end]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(page); err != nil {
			t.Errorf("encode page response: %v", err)
		}
	}))
	defer server.Close()

	runner := newUDLTestRunner(t, server.URL, 2)

	if err := runner.syncUDLFeed(context.Background(), runner.udlElsetFeed()); err != nil {
		t.Fatalf("syncUDLFeed elset failed: %v", err)
	}

	mu.Lock()
	gotRequests := requests
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("UDL elset page requests = %d, want 2 (paging via firstResult)", gotRequests)
	}

	stored, err := runner.store.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll OMM failed: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("stored OMM records = %d, want 3", len(stored))
	}

	byNorad := make(map[uint32]*OMMFB.OMM, len(stored))
	for _, record := range stored {
		omm := OMMFB.GetSizePrefixedRootAsOMM(record, 0)
		byNorad[omm.NORAD_CAT_ID()] = omm
	}

	iss := byNorad[25544]
	if iss == nil {
		t.Fatalf("missing OMM for NORAD 25544")
	}
	if got, want := string(iss.OBJECT_NAME()), "ISS (ZARYA)"; got != want {
		t.Fatalf("OBJECT_NAME = %q, want %q", got, want)
	}
	if got, want := string(iss.OBJECT_ID()), "1998-067A"; got != want {
		t.Fatalf("OBJECT_ID = %q, want %q", got, want)
	}
	if got, want := string(iss.EPOCH()), "2026-06-01T12:00:00Z"; got != want {
		t.Fatalf("EPOCH = %q, want %q", got, want)
	}
	if got, want := iss.MEAN_MOTION(), 15.4951; got != want {
		t.Fatalf("MEAN_MOTION = %f, want %f", got, want)
	}
	if got, want := iss.ECCENTRICITY(), 0.0004567; got != want {
		t.Fatalf("ECCENTRICITY = %f, want %f", got, want)
	}
	if got, want := iss.INCLINATION(), 51.6405; got != want {
		t.Fatalf("INCLINATION = %f, want %f", got, want)
	}
	if got, want := iss.RA_OF_ASC_NODE(), 247.4627; got != want {
		t.Fatalf("RA_OF_ASC_NODE = %f, want %f", got, want)
	}
	if got, want := iss.ARG_OF_PERICENTER(), 130.536; got != want {
		t.Fatalf("ARG_OF_PERICENTER = %f, want %f", got, want)
	}
	if got, want := iss.MEAN_ANOMALY(), 325.0288; got != want {
		t.Fatalf("MEAN_ANOMALY = %f, want %f", got, want)
	}
	// CLASSIFICATION_MARKING from the UDL record is passed through to
	// CLASSIFICATION_TYPE in the OMM FlatBuffer via WithClassificationType.
	if got, want := string(iss.CLASSIFICATION_TYPE()), "U"; got != want {
		t.Fatalf("CLASSIFICATION_TYPE = %q, want %q (from UDL CLASSIFICATION_MARKING)", got, want)
	}

	// camelCase UDL record without MEAN_MOTION derives it from SEMI_MAJOR_AXIS.
	derived := byNorad[40909]
	if derived == nil {
		t.Fatalf("missing OMM for NORAD 40909 (camelCase UDL keys)")
	}
	if mm := derived.MEAN_MOTION(); mm < 15.0 || mm > 16.0 {
		t.Fatalf("derived MEAN_MOTION from SEMI_MAJOR_AXIS = %f, want ~15.5 rev/day", mm)
	}
	if got, want := string(derived.OBJECT_ID()), "2015-049A"; got != want {
		t.Fatalf("OBJECT_ID from idOnOrbit = %q, want %q", got, want)
	}

	// camelCase record has classificationMarking "U//FOUO" — verify it is
	// written to the OMM CLASSIFICATION_TYPE field, not silently discarded.
	if got, want := string(derived.CLASSIFICATION_TYPE()), "U//FOUO"; got != want {
		t.Fatalf("derived CLASSIFICATION_TYPE = %q, want %q (from UDL classificationMarking)", got, want)
	}

	wantDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if got := runner.checkpoints.getString(udlElsetCheckpointKey); got != wantDay {
		t.Fatalf("checkpoint %s = %q, want %q", udlElsetCheckpointKey, got, wantDay)
	}

	matches, err := filepath.Glob(filepath.Join(runner.cfg.RawPath, "provenance", "udl-elset", "*.json"))
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
		SourceURL              string         `json:"source_url"`
		ParserVersion          string         `json:"parser_version"`
		NormalizedSHA256       string         `json:"normalized_sha256"`
		SchemaCounts           map[string]int `json:"schema_counts"`
		ClassificationMarkings map[string]int `json:"classification_markings"`
	}
	if err := json.Unmarshal(payload, &provenance); err != nil {
		t.Fatalf("unmarshal provenance: %v\n%s", err, payload)
	}
	if got, want := provenance.ParserVersion, parserVersionUDLElset; got != want {
		t.Fatalf("parser_version = %q, want %q", got, want)
	}
	if !strings.HasPrefix(provenance.SourceURL, server.URL+"/udl/elset?") {
		t.Fatalf("source_url = %q, want UDL elset query URL", provenance.SourceURL)
	}
	if provenance.NormalizedSHA256 == "" {
		t.Fatalf("normalized_sha256 is empty")
	}
	if got, want := provenance.SchemaCounts["OMM.fbs"], 3; got != want {
		t.Fatalf("OMM.fbs schema count = %d, want %d", got, want)
	}
	if got, want := provenance.ClassificationMarkings["U"], 2; got != want {
		t.Fatalf("classification_markings[U] = %d, want %d", got, want)
	}
	if got, want := provenance.ClassificationMarkings["U//FOUO"], 1; got != want {
		t.Fatalf("classification_markings[U//FOUO] = %d, want %d", got, want)
	}

	tagged, err := runner.store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: "OMM.fbs",
		ProviderID: udlProviderID,
		SourceName: "udl-elset",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QuerySourceTaggedRecords failed: %v", err)
	}
	if len(tagged) != 3 {
		t.Fatalf("tagged OMM records = %d, want 3", len(tagged))
	}
	gotTags, err := runner.store.GetSourceTags("OMM.fbs", tagged[0].CID)
	if err != nil {
		t.Fatalf("GetSourceTags failed: %v", err)
	}
	if gotTags.ProviderID != udlProviderID {
		t.Fatalf("tag ProviderID = %q, want %q", gotTags.ProviderID, udlProviderID)
	}
	if gotTags.SourceName != "udl-elset" {
		t.Fatalf("tag SourceName = %q, want udl-elset", gotTags.SourceName)
	}
	if gotTags.ContentKeyID != publicContentKeyID {
		t.Fatalf("tag ContentKeyID = %q, want %q", gotTags.ContentKeyID, publicContentKeyID)
	}
}

func TestSyncUDLElsetsSkipsMalformedRecords(t *testing.T) {
	payload := []byte(`[
		{"CLASSIFICATION_MARKING":"U","NORAD_CAT_ID":25544,"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","EPOCH":"2026-06-01T12:00:00.000000Z","MEAN_MOTION":15.4951,"ECCENTRICITY":0.0004567,"INCLINATION":51.6405,"RA_OF_ASC_NODE":247.4627,"ARG_OF_PERICENTER":130.536,"MEAN_ANOMALY":325.0288,"SEMI_MAJOR_AXIS":6796.0},
		{"CLASSIFICATION_MARKING":"U","OBJECT_NAME":"NO-NORAD","EPOCH":"2026-06-01T12:00:00.000000Z","MEAN_MOTION":15.0},
		{"CLASSIFICATION_MARKING":"U","NORAD_CAT_ID":40909,"OBJECT_NAME":"BAD-EPOCH","EPOCH":"not-a-date","MEAN_MOTION":15.0}
	]`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireUDLBasicAuth(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	runner := newUDLTestRunner(t, server.URL, 50)

	if err := runner.syncUDLFeed(context.Background(), runner.udlElsetFeed()); err != nil {
		t.Fatalf("syncUDLFeed elset failed: %v", err)
	}

	stored, err := runner.store.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll OMM failed: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored OMM records = %d, want 1 (malformed records skipped)", len(stored))
	}
	omm := OMMFB.GetSizePrefixedRootAsOMM(stored[0], 0)
	if got, want := omm.NORAD_CAT_ID(), uint32(25544); got != want {
		t.Fatalf("NORAD_CAT_ID = %d, want %d", got, want)
	}

	wantDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if got := runner.checkpoints.getString(udlElsetCheckpointKey); got != wantDay {
		t.Fatalf("checkpoint %s = %q, want %q", udlElsetCheckpointKey, got, wantDay)
	}

	matches, err := filepath.Glob(filepath.Join(runner.cfg.RawPath, "provenance", "udl-elset", "*.json"))
	if err != nil {
		t.Fatalf("glob provenance: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d provenance files, want 1: %v", len(matches), matches)
	}
	provenancePayload, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	var provenance struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(provenancePayload, &provenance); err != nil {
		t.Fatalf("unmarshal provenance: %v\n%s", err, provenancePayload)
	}
	found := false
	for _, warning := range provenance.Warnings {
		if strings.Contains(warning, "skipped 2 malformed UDL elset record(s)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want malformed skip warning", provenance.Warnings)
	}
}

func TestSyncUDLSGIIngestsSPWWithCheckpoint(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "udl-sgi.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/udl/sgi" {
			http.NotFound(w, r)
			return
		}
		requireUDLBasicAuth(t, r)
		if rangeParam := r.URL.Query().Get("sgiDate"); !strings.Contains(rangeParam, "..") {
			t.Errorf("sgiDate query = %q, want start..end range", rangeParam)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	runner := newUDLTestRunner(t, server.URL, 50)

	if err := runner.syncUDLFeed(context.Background(), runner.udlSGIFeed()); err != nil {
		t.Fatalf("syncUDLFeed sgi failed: %v", err)
	}

	stored, err := runner.store.QueryAll("SPW.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll SPW failed: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored SPW records = %d, want 2", len(stored))
	}

	byDate := make(map[string]*SPWFB.SPW, len(stored))
	for _, record := range stored {
		spw := SPWFB.GetSizePrefixedRootAsSPW(record, 0)
		byDate[string(spw.Date())] = spw
	}

	first := byDate["2026-06-01"]
	if first == nil {
		t.Fatalf("missing SPW record for 2026-06-01")
	}
	if got, want := first.F107Obs(), float32(152.5); got != want {
		t.Fatalf("F107_OBS = %f, want %f", got, want)
	}
	if got, want := first.F107ObsCenter81(), float32(148.2); got != want {
		t.Fatalf("F107_OBS_CENTER81 = %f, want %f", got, want)
	}
	if got, want := first.ApAvg(), int32(12); got != want {
		t.Fatalf("AP_AVG = %d, want %d", got, want)
	}
	if got, want := first.KpSum(), int32(33); got != want {
		t.Fatalf("KP_SUM = %d tenths, want %d tenths", got, want)
	}

	second := byDate["2026-06-02"]
	if second == nil {
		t.Fatalf("missing SPW record for 2026-06-02")
	}
	if got, want := second.KpSum(), int32(27); got != want {
		t.Fatalf("KP_SUM = %d tenths, want %d tenths", got, want)
	}

	wantDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if got := runner.checkpoints.getString(udlSGICheckpointKey); got != wantDay {
		t.Fatalf("checkpoint %s = %q, want %q", udlSGICheckpointKey, got, wantDay)
	}

	matches, err := filepath.Glob(filepath.Join(runner.cfg.RawPath, "provenance", "udl-sgi", "*.json"))
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
		ParserVersion string         `json:"parser_version"`
		SchemaCounts  map[string]int `json:"schema_counts"`
	}
	if err := json.Unmarshal(payload, &provenance); err != nil {
		t.Fatalf("unmarshal provenance: %v\n%s", err, payload)
	}
	if got, want := provenance.ParserVersion, parserVersionUDLSGI; got != want {
		t.Fatalf("parser_version = %q, want %q", got, want)
	}
	if got, want := provenance.SchemaCounts["SPW.fbs"], 2; got != want {
		t.Fatalf("SPW.fbs schema count = %d, want %d", got, want)
	}
}

func TestSyncUDLSkipsWhenCredentialsMissing(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:            filepath.Join(dir, "store"),
		RawPath:                filepath.Join(dir, "raw"),
		SpaceTrackPollInterval: time.Hour,
		UDLEnabled:             true,
		UDLBaseURL:             server.URL,
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	defer func() {
		if err := runner.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	if err := runner.syncUDL(context.Background()); err != nil {
		t.Fatalf("syncUDL failed: %v", err)
	}
	if requests != 0 {
		t.Fatalf("UDL requests = %d, want 0 when credentials are missing", requests)
	}
	if got := runner.checkpoints.getString(udlElsetCheckpointKey); got != "" {
		t.Fatalf("elset checkpoint advanced without credentials: %q", got)
	}
}

func TestSyncUDLResumesFromCheckpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireUDLBasicAuth(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	runner := newUDLTestRunner(t, server.URL, 50)

	// Checkpoint at yesterday means there is no new day window to pull.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	runner.checkpoints.setString(udlElsetCheckpointKey, yesterday)

	if err := runner.syncUDLFeed(context.Background(), runner.udlElsetFeed()); err != nil {
		t.Fatalf("syncUDLFeed elset failed: %v", err)
	}
	if got := runner.checkpoints.getString(udlElsetCheckpointKey); got != yesterday {
		t.Fatalf("checkpoint %s = %q, want unchanged %q", udlElsetCheckpointKey, got, yesterday)
	}
}

func TestMeanMotionFromSemiMajorAxis(t *testing.T) {
	// GEO altitude semi-major axis should be ~1 rev/day.
	got, ok := meanMotionFromSemiMajorAxis(42164.0)
	if !ok {
		t.Fatalf("meanMotionFromSemiMajorAxis returned !ok for GEO")
	}
	if math.Abs(got-1.0) > 0.01 {
		t.Fatalf("GEO mean motion = %f rev/day, want ~1.0", got)
	}
	if _, ok := meanMotionFromSemiMajorAxis(0); ok {
		t.Fatalf("meanMotionFromSemiMajorAxis(0) returned ok")
	}
}
