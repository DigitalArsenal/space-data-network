package geoip

import (
	"math"
	"path/filepath"
	"testing"
)

const testMMDB = "testdata/GeoLite2-City-Test.mmdb"

func TestLookupResolvesKnownIP(t *testing.T) {
	r := Open(testMMDB)
	defer r.Close()

	loc := r.Lookup("81.2.69.142")
	if math.Abs(float64(loc.Lat)-51.5142) > 0.01 {
		t.Errorf("Lat = %v, want ~51.5142", loc.Lat)
	}
	if math.Abs(float64(loc.Lon)-(-0.0931)) > 0.01 {
		t.Errorf("Lon = %v, want ~-0.0931", loc.Lon)
	}
	if loc.City != "London" {
		t.Errorf("City = %q, want London", loc.City)
	}
	if loc.Country != "United Kingdom" {
		t.Errorf("Country = %q, want United Kingdom", loc.Country)
	}
}

func TestLookupMissReturnsEmpty(t *testing.T) {
	r := Open(testMMDB)
	defer r.Close()

	// An address outside the single inserted network resolves to nothing.
	loc := r.Lookup("8.8.8.8")
	if loc != (Location{}) {
		t.Errorf("expected empty Location for miss, got %+v", loc)
	}
}

func TestFailOpenMissingDatabase(t *testing.T) {
	r := Open(filepath.Join(t.TempDir(), "does-not-exist.mmdb"))
	defer r.Close()

	if loc := r.Lookup("81.2.69.142"); loc != (Location{}) {
		t.Errorf("missing db must fail open to empty Location, got %+v", loc)
	}
}

func TestFailOpenEmptyPath(t *testing.T) {
	r := Open("")
	defer r.Close()

	if loc := r.Lookup("81.2.69.142"); loc != (Location{}) {
		t.Errorf("empty path must fail open to empty Location, got %+v", loc)
	}
}

func TestFailOpenNilReader(t *testing.T) {
	var r *Reader
	if loc := r.Lookup("81.2.69.142"); loc != (Location{}) {
		t.Errorf("nil reader must fail open to empty Location, got %+v", loc)
	}
	if err := r.Close(); err != nil {
		t.Errorf("nil reader Close = %v, want nil", err)
	}
}

func TestLookupInvalidIP(t *testing.T) {
	r := Open(testMMDB)
	defer r.Close()

	if loc := r.Lookup("not-an-ip"); loc != (Location{}) {
		t.Errorf("invalid IP must return empty Location, got %+v", loc)
	}
}
