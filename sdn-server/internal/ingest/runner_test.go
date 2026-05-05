package ingest

import (
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
	if got, want := iss.Maneuverable(), true; got != want {
		t.Fatalf("MANEUVERABLE = %t, want %t", got, want)
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
}
