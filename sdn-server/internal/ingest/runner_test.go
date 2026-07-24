package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestRunnerConfigHasNoDirectPublicCatalogEndpointOrSchedule(t *testing.T) {
	forbiddenSource := strings.Join([]string{"celes", "trak"}, "")
	typ := reflect.TypeOf(Config{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if strings.Contains(strings.ToLower(field.Name), forbiddenSource) {
			t.Fatalf("runner config exposes forbidden direct source field %s", field.Name)
		}
	}
}

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

func TestNewRunnerDefaultsCredentialedSourceCadences(t *testing.T) {
	runner := newTestRunner(t)

	if got, want := runner.cfg.SpaceTrackPollInterval, 30*time.Minute; got != want {
		t.Fatalf("SpaceTrackPollInterval = %s, want %s", got, want)
	}
	if got, want := runner.cfg.UDLPollInterval, 30*time.Minute; got != want {
		t.Fatalf("UDLPollInterval = %s, want %s", got, want)
	}
}

func TestRunCycleIsOfflineWhenCredentialedSourcesAreDisabled(t *testing.T) {
	runner := newTestRunner(t)
	if err := runner.runCycle(context.Background()); err != nil {
		t.Fatalf("runCycle with all credentialed sources disabled failed: %v", err)
	}
}

func TestRunnerDiskGuardAppliesToGenericSourceSync(t *testing.T) {
	runner := newTestRunner(t)

	oldDiskAvailableBytes := diskAvailableBytes
	diskAvailableBytes = func(string) (uint64, error) {
		return 1024, nil
	}
	t.Cleanup(func() {
		diskAvailableBytes = oldDiskAvailableBytes
	})

	err := runner.requireFreeDisk("source sync")
	if err == nil {
		t.Fatal("source sync succeeded despite low free disk")
	}
	if !strings.Contains(err.Error(), "free disk") {
		t.Fatalf("error = %v, want free disk guardrail", err)
	}
}

func TestIngestGPDataStoresCanonicalOMMAndMPERecords(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/gp-sample.csv")
	if err != nil {
		t.Fatalf("read GP fixture: %v", err)
	}

	tags := sourceTags("space-track", "gp-history", "https://fixture.test/gp.csv", fixture)
	countOMM, countMPE, normalizedHash, err := runner.ingestGPData(fixture, "source:spacetrack", tags)
	if err != nil {
		t.Fatalf("ingestGPData failed: %v", err)
	}
	if countOMM != 2 || countMPE != 2 {
		t.Fatalf("counts OMM/MPE = %d/%d, want 2/2", countOMM, countMPE)
	}
	if normalizedHash == "" {
		t.Fatal("normalized hash is empty")
	}

	for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
		records, err := runner.store.QuerySourceTaggedRecords(storage.SourceTagQuery{
			SchemaName: schema,
			ProviderID: "space-track",
			SourceName: "gp-history",
			BatchID:    tags.BatchID,
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("QuerySourceTaggedRecords(%s): %v", schema, err)
		}
		if len(records) != 2 {
			t.Fatalf("%s records = %d, want 2", schema, len(records))
		}
	}
}

func TestIngestGPDataRejectsMalformedEpoch(t *testing.T) {
	runner := newTestRunner(t)
	fixture := []byte("NORAD_CAT_ID,EPOCH,OBJECT_NAME\n25544,not-an-epoch,TEST\n")
	countOMM, countMPE, _, err := runner.ingestGPData(fixture, "source:spacetrack")
	if err == nil {
		t.Fatalf("ingestGPData returned nil error with OMM=%d MPE=%d", countOMM, countMPE)
	}
	if !strings.Contains(err.Error(), "malformed EPOCH") {
		t.Fatalf("error = %v, want malformed EPOCH", err)
	}
}

func TestGPOriginatorUsesCredentialedSourceIdentity(t *testing.T) {
	if got, want := gpOriginatorForSource("source:spacetrack"), "SPACE-TRACK"; got != want {
		t.Fatalf("Space-Track originator = %q, want %q", got, want)
	}
	if got, want := gpOriginatorForSource("source:provider-a"), "PROVIDER-A"; got != want {
		t.Fatalf("generic originator = %q, want %q", got, want)
	}
}

func TestOfflineSatcatParsersStoreCanonicalCATRecords(t *testing.T) {
	for _, fixtureName := range []string{"catalog-sample.txt", "catalog-sample.csv"} {
		t.Run(fixtureName, func(t *testing.T) {
			runner := newTestRunner(t)
			fixture, err := os.ReadFile(filepath.Join("testdata", fixtureName))
			if err != nil {
				t.Fatalf("read CAT fixture: %v", err)
			}
			count, normalizedHash, err := runner.ingestSatcatData(fixture, "source:fixture")
			if err != nil {
				t.Fatalf("ingestSatcatData failed: %v", err)
			}
			if count != 2 {
				t.Fatalf("CAT count = %d, want 2", count)
			}
			if normalizedHash == "" {
				t.Fatal("normalized hash is empty")
			}
		})
	}
}

func TestOfflineSpaceWeatherParserStoresCanonicalSPWRecords(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/space-weather-sample.csv")
	if err != nil {
		t.Fatalf("read SPW fixture: %v", err)
	}

	count, normalizedHash, err := runner.ingestSpaceWeatherData(fixture, "source:fixture")
	if err != nil {
		t.Fatalf("ingestSpaceWeatherData failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("SPW count = %d, want 2", count)
	}
	if normalizedHash == "" {
		t.Fatal("normalized hash is empty")
	}
}

func TestOfflineParsersRejectSchemaMismatch(t *testing.T) {
	runner := newTestRunner(t)
	if count, _, err := runner.ingestSpaceWeatherData([]byte("NOT_DATE,AP_AVG\n2026-01-01,4\n"), "source:fixture"); err == nil {
		t.Fatalf("ingestSpaceWeatherData returned nil error with SPW=%d", count)
	}
	if count, _, err := runner.ingestSatcatData([]byte("OBJECT_NAME,OBJECT_ID\nTEST,1998-001A\n"), "source:fixture"); err == nil {
		t.Fatalf("ingestSatcatData returned nil error with CAT=%d", count)
	}
}

func TestSyncSpaceTrackGapFillRecordsBatchProvenance(t *testing.T) {
	fixture, err := os.ReadFile("testdata/gp-sample.csv")
	if err != nil {
		t.Fatalf("read GP fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Method != http.MethodPost {
				t.Errorf("login method = %s, want POST", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		case "/gp":
			w.Header().Set("ETag", `"fixture-gp"`)
			w.Header().Set("Content-Type", "text/csv")
			_, _ = w.Write(fixture)
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
	defer runner.Close()

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
		ParserVersion    string         `json:"parser_version"`
		NormalizedSHA256 string         `json:"normalized_sha256"`
		SchemaCounts     map[string]int `json:"schema_counts"`
	}
	if err := json.Unmarshal(payload, &provenance); err != nil {
		t.Fatalf("unmarshal provenance: %v", err)
	}
	if provenance.ParserVersion != parserVersionSpaceTrackGP {
		t.Fatalf("parser version = %q, want %q", provenance.ParserVersion, parserVersionSpaceTrackGP)
	}
	if provenance.NormalizedSHA256 == "" {
		t.Fatal("normalized hash is empty")
	}
	if provenance.SchemaCounts["OMM.fbs"] != 2 || provenance.SchemaCounts["MPE.fbs"] != 2 {
		t.Fatalf("schema counts = %#v, want OMM=2 MPE=2", provenance.SchemaCounts)
	}
}
