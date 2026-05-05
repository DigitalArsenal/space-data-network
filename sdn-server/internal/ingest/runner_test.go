package ingest

import (
	"os"
	"path/filepath"
	"testing"

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

func TestIngestSpaceWeatherDataStoresSPWFlatBuffers(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/celestrak-sw-all.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	count, err := runner.ingestSpaceWeatherData(fixture, "source:celestrak")
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
