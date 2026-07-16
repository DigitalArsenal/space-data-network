package sdnruns

// Internal unit tests for the JSON-OEM -> CCSDS OEM KVN reconciliation and the
// provider -> store-lane mapping the store-backed ephemeris source relies on.

import (
	"strings"
	"testing"
)

func TestLaneFor(t *testing.T) {
	cases := map[string]string{
		"starlink":        "spacex-starlink",
		"spacex-starlink": "spacex-starlink",
		"STARLINK":        "spacex-starlink",
		"iss":             "iss",
		"oneweb":          "oneweb",
		"glonass":         "glonass",
		"intelsat":        "intelsat",
		"cpf":             "cpf",
		"gps":             "gps",
		"already-a-lane":  "already-a-lane", // tolerant passthrough
	}
	for in, want := range cases {
		if got := laneFor(in); got != want {
			t.Errorf("laneFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEphemerisFromOEMJSON_FlatStates(t *testing.T) {
	// A flat-EPHEMERIS_DATA record (starlink shape): 2 states, 6 values each,
	// epochs reconstructed from START_TIME + i*STEP_SIZE.
	rec := []byte(`{
      "CCSDS_OEM_VERS":2.0,
      "CREATION_DATE":"2026-07-13T12:00:00.000",
      "ORIGINATOR":"SpaceX",
      "EPHEMERIS_DATA_BLOCK":[{
        "OBJECT_NAME":"STARLINK-1007",
        "OBJECT_ID":"2019-074A",
        "NORAD_CAT_ID":44713,
        "CENTER_NAME":"EARTH",
        "REFERENCE_FRAME":"TEME",
        "TIME_SYSTEM":"UTC",
        "START_TIME":"2026-07-13T12:00:00.000",
        "STOP_TIME":"2026-07-13T12:04:00.000",
        "STEP_SIZE":240,
        "STATE_VECTOR_SIZE":6,
        "EPHEMERIS_DATA":[
          -4024.5,3872.2,-3874.9,-6.07,-2.18,4.12,
          -5317.8,3214.1,-2755.8,-4.64,-3.26,5.14
        ]
      }]
    }`)

	eph, ok := ephemerisFromOEMJSON("starlink", "SpaceX-E", rec)
	if !ok {
		t.Fatalf("ephemerisFromOEMJSON returned not-ok")
	}
	if eph.Format != "oem" {
		t.Fatalf("Format = %q, want oem", eph.Format)
	}
	if eph.NoradCatID != 44713 || eph.ObjectName != "STARLINK-1007" || eph.ObjectID != "2019-074A" {
		t.Fatalf("identity mismatch: %+v", eph)
	}
	if eph.DataSource != "SpaceX-E" {
		t.Fatalf("DataSource = %q, want SpaceX-E", eph.DataSource)
	}
	kvn := string(eph.Bytes)
	for _, want := range []string{
		"CCSDS_OEM_VERS = 2.0",
		"META_START",
		"OBJECT_NAME = STARLINK-1007",
		"REF_FRAME = TEME",       // KVN key (JSON carries REFERENCE_FRAME)
		"CENTER_NAME = EARTH",
		"TIME_SYSTEM = UTC",
		"META_STOP",
		"2026-07-13T12:00:00.000 -4024.5 3872.2 -3874.9 -6.07 -2.18 4.12",
		"2026-07-13T12:04:00.000 -5317.8 3214.1 -2755.8 -4.64 -3.26 5.14", // +240s
	} {
		if !strings.Contains(kvn, want) {
			t.Fatalf("KVN missing %q in:\n%s", want, kvn)
		}
	}
}

func TestEphemerisFromOEMJSON_PositionOnlyLines(t *testing.T) {
	// CPF shape: explicit EPHEMERIS_DATA_LINES with their own EPOCH, position-only.
	rec := []byte(`{
      "CCSDS_OEM_VERS":2.0,
      "ORIGINATOR":"ILRS",
      "EPHEMERIS_DATA_BLOCK":[{
        "OBJECT_NAME":"LAGEOS-1",
        "OBJECT_ID":"1976-039A",
        "NORAD_CAT_ID":8820,
        "CENTER_NAME":"EARTH",
        "REFERENCE_FRAME":"ITRF",
        "TIME_SYSTEM":"UTC",
        "START_TIME":"2026-07-13T00:00:00.000",
        "STEP_SIZE":120,
        "STATE_VECTOR_SIZE":3,
        "EPHEMERIS_DATA_LINES":[
          {"EPOCH":"2026-07-13T00:00:00.000","X":1234.5,"Y":-6789.0,"Z":8500.25},
          {"EPOCH":"2026-07-13T00:02:00.000","X":1300.0,"Y":-6700.0,"Z":8600.0}
        ]
      }]
    }`)

	eph, ok := ephemerisFromOEMJSON("cpf", "CPF", rec)
	if !ok {
		t.Fatalf("ephemerisFromOEMJSON returned not-ok")
	}
	kvn := string(eph.Bytes)
	for _, want := range []string{
		"REF_FRAME = ITRF",
		"2026-07-13T00:00:00.000 1234.5 -6789 8500.25",
		"2026-07-13T00:02:00.000 1300 -6700 8600",
	} {
		if !strings.Contains(kvn, want) {
			t.Fatalf("KVN missing %q in:\n%s", want, kvn)
		}
	}
	// Position-only lines carry no velocity tokens (4 fields per data line).
	for _, line := range strings.Split(kvn, "\n") {
		if strings.HasPrefix(line, "2026-07-13T00:0") {
			if got := len(strings.Fields(line)); got != 4 {
				t.Fatalf("position-only data line has %d fields, want 4: %q", got, line)
			}
		}
	}
}

func TestEphemerisFromOEMJSON_RejectsEmpty(t *testing.T) {
	// No block / no states -> not usable.
	if _, ok := ephemerisFromOEMJSON("iss", "ISS-E", []byte(`{"CCSDS_OEM_VERS":2.0}`)); ok {
		t.Fatalf("expected not-ok for a record with no EPHEMERIS_DATA_BLOCK")
	}
	if _, ok := ephemerisFromOEMJSON("iss", "ISS-E", []byte(`not json`)); ok {
		t.Fatalf("expected not-ok for non-JSON bytes")
	}
}
