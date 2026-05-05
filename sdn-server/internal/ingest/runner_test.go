package ingest

import (
	"os"
	"path/filepath"
	"testing"

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

func TestIngestGPDataStoresOMMAndMPEFlatBuffers(t *testing.T) {
	runner := newTestRunner(t)
	fixture, err := os.ReadFile("testdata/celestrak-gp-omm.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	countOMM, countMPE, err := runner.ingestGPData(fixture, "source:celestrak")
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

	count, err := runner.ingestSatcatData(fixture, "source:celestrak")
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

	count, err := runner.ingestSatcatData(fixture, "source:celestrak")
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
