package sdnruns_test

// Proof that the supplemental-OMM run engine, backed by the STORE-backed
// ephemeris source, fits EVERY object each enabled provider ingested — not one.
//
//   - TestStoreBackedSource_MultiObjectCount seeds a fake record store with 50
//     distinct per-object JSON-OEM records under (spacex-starlink, OEM), runs the
//     provider, and asserts ObjectsTotal == 50 with every ObjectResult populated
//     (a deterministic fake Fitter keeps the count assertion fast + hermetic).
//
//   - TestStoreBackedSource_RealODFit_MultiObject drives the REAL analysis/od WASM
//     over MULTIPLE store-backed objects (derived from the checked-in NASA ISS
//     ephemeris) and prints the run JSON with objects_total > 1 and real per-object
//     RMS — the store-backed source feeding the real fitter end-to-end.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/ipfs/kubo/sdn/sdnruns"
	"github.com/ipfs/kubo/sdn/testsupport"
)

func TestStoreBackedSource_MultiObjectCount(t *testing.T) {
	const n = 50
	store := newFakeRecordStore()
	for i := 0; i < n; i++ {
		norad := uint32(70000 + i)
		rec := makeOEMJSONFlat(
			norad,
			fmt.Sprintf("STARLINK-%d", 1000+i),
			fmt.Sprintf("2024-%03dA", i+1),
			"TEME",
			"2026-07-13T12:00:00.000",
			240,
			sampleStates(norad),
		)
		store.seed("spacex-starlink", "OEM", rec)
	}

	source := sdnruns.NewStoreEphemerisSource(store, nil, t.Logf)
	runsStore, err := sdnruns.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("runs store: %v", err)
	}
	runner, err := sdnruns.NewRunner(sdnruns.Config{
		Fitter:  fakeFitter{},
		Source:  source,
		Records: store,
		Runs:    runsStore,
		Log:     t.Logf,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	run, err := runner.RunProviders(context.Background(), sdnruns.RunConfig{
		EnabledProviders: []string{"spacex-starlink"},
		ProducedSource:   "supplemental-omm",
	})
	if err != nil {
		t.Fatalf("RunProviders: %v", err)
	}

	if run.Status != sdnruns.StatusCompleted {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
	if run.ObjectsTotal != n {
		t.Fatalf("ObjectsTotal = %d, want %d (must fit EVERY ingested object, not one)", run.ObjectsTotal, n)
	}
	if run.ObjectsDone != n || len(run.Objects) != n {
		t.Fatalf("ObjectsDone=%d len(Objects)=%d, want %d", run.ObjectsDone, len(run.Objects), n)
	}
	seen := map[uint32]bool{}
	for _, obj := range run.Objects {
		if obj.Error != "" {
			t.Fatalf("object NORAD %d fit error: %s", obj.Norad, obj.Error)
		}
		if obj.Norad < 70000 || obj.Norad >= 70000+n {
			t.Fatalf("unexpected object NORAD %d", obj.Norad)
		}
		if obj.ObjectName == "" || obj.RMS <= 0 || !obj.Converged || obj.OMMCid == "" {
			t.Fatalf("object NORAD %d not fully populated: name=%q rms=%.3f converged=%v cid=%q",
				obj.Norad, obj.ObjectName, obj.RMS, obj.Converged, obj.OMMCid)
		}
		if seen[obj.Norad] {
			t.Fatalf("duplicate object NORAD %d", obj.Norad)
		}
		seen[obj.Norad] = true
	}
	if len(seen) != n {
		t.Fatalf("distinct objects = %d, want %d", len(seen), n)
	}

	// A produced $OMM landed for every object.
	produced, err := store.ReadBySourceType(context.Background(), "supplemental-omm", "OMM")
	if err != nil {
		t.Fatalf("read produced OMM lane: %v", err)
	}
	if len(produced) != n {
		t.Fatalf("produced OMM records = %d, want %d", len(produced), n)
	}
	t.Logf("store-backed run fit %d objects (fake fitter); %d produced OMMs stored", run.ObjectsTotal, len(produced))
}

func TestStoreBackedSource_RealODFit_MultiObject(t *testing.T) {
	wasmPath := testsupport.SkipIfNoODModuleWasm(t)
	fixturePath := testsupport.SkipIfNoODEphemerisFixture(t)

	startISO, stepSec, states := parseKVNStates(t, fixturePath)
	if len(states) < 8 {
		t.Fatalf("fixture yielded too few state vectors (%d)", len(states))
	}

	// Seed the store with THREE distinct objects built from the real ISS ephemeris
	// (same states, distinct identity) as JSON-OEM records — exactly the shape the
	// data-source modules write. The store-backed source converts each to KVN and
	// the REAL od WASM fits it.
	store := newFakeRecordStore()
	objects := []struct {
		norad uint32
		name  string
		objID string
	}{
		{25544, "ISS", "1998-067-A"},
		{90001, "ISS-CLONE-1", "1998-067-B"},
		{90002, "ISS-CLONE-2", "1998-067-C"},
	}
	for _, o := range objects {
		rec := makeOEMJSONFlat(o.norad, o.name, o.objID, "EME2000", startISO, stepSec, states)
		store.seed("spacex-starlink", "OEM", rec)
	}

	source := sdnruns.NewStoreEphemerisSource(store, nil, t.Logf)
	fitter := sdnruns.NewReactorFitter(func() ([]byte, error) { return os.ReadFile(wasmPath) }, len(objects), t.Logf)
	defer fitter.Close()
	runsStore, err := sdnruns.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("runs store: %v", err)
	}
	runner, err := sdnruns.NewRunner(sdnruns.Config{
		Fitter:  fitter,
		Source:  source,
		Records: store,
		Runs:    runsStore,
		Log:     t.Logf,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	run, err := runner.RunProviders(context.Background(), sdnruns.RunConfig{
		EnabledProviders: []string{"spacex-starlink"},
		ProducedSource:   "supplemental-omm",
	})
	if err != nil {
		t.Fatalf("RunProviders: %v", err)
	}

	if run.ObjectsTotal != len(objects) {
		t.Fatalf("ObjectsTotal = %d, want %d", run.ObjectsTotal, len(objects))
	}
	fitted := 0
	for _, obj := range run.Objects {
		if obj.Error != "" {
			t.Fatalf("REAL od fit error for NORAD %d: %s", obj.Norad, obj.Error)
		}
		if !obj.Converged || obj.RMS <= 0 || obj.RMS > 5.0 {
			t.Fatalf("REAL od fit for NORAD %d implausible: converged=%v rms=%.4f", obj.Norad, obj.Converged, obj.RMS)
		}
		if mm := obj.Elements.MeanMotion; mm < 15.3 || mm > 15.7 {
			t.Fatalf("REAL od fit for NORAD %d not ISS-like: MM=%.6f", obj.Norad, mm)
		}
		fitted++
	}
	if fitted != len(objects) {
		t.Fatalf("real fits = %d, want %d", fitted, len(objects))
	}

	blob, _ := json.MarshalIndent(run, "", "  ")
	t.Logf("REAL multi-object store-backed OD run (objects_total=%d):\n%s", run.ObjectsTotal, string(blob))
}

// --- fakes -----------------------------------------------------------------

type fakeRecordStore struct {
	mu   sync.Mutex
	data map[string][][]byte
}

func newFakeRecordStore() *fakeRecordStore {
	return &fakeRecordStore{data: map[string][][]byte{}}
}

func recStoreKey(source, sdsType string) string {
	return source + "|" + strings.ToUpper(strings.TrimSpace(sdsType))
}

func (f *fakeRecordStore) seed(source, sdsType string, rec []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := recStoreKey(source, sdsType)
	cp := make([]byte, len(rec))
	copy(cp, rec)
	f.data[k] = append(f.data[k], cp)
}

func (f *fakeRecordStore) Store(_ context.Context, source, sdsType string, fb []byte) (cid.Cid, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := recStoreKey(source, sdsType)
	cp := make([]byte, len(fb))
	copy(cp, fb)
	f.data[k] = append(f.data[k], cp)
	h, err := mh.Sum(fb, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, h), nil
}

func (f *fakeRecordStore) ReadBySourceType(_ context.Context, source, sdsType string) ([][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.data[recStoreKey(source, sdsType)]
	out := make([][]byte, len(src))
	for i, r := range src {
		cp := make([]byte, len(r))
		copy(cp, r)
		out[i] = cp
	}
	return out, nil
}

// fakeFitter is a deterministic Fitter: it echoes the option identity back with a
// fixed plausible RMS + ISS-like mean motion so the runner's per-object plumbing
// (RMS parse, OMM production + store, result rows) exercises fully without the
// real WASM. It never touches the ephemeris bytes.
type fakeFitter struct{}

func (fakeFitter) Fit(_ context.Context, _ []byte, opts sdnruns.FitOptions) (*sdnruns.FitResult, error) {
	return &sdnruns.FitResult{
		ObjectName:   opts.ObjectName,
		ObjectID:     opts.ObjectID,
		Epoch:        "2026-07-13T12:00:00.000",
		MeanMotion:   15.5,
		Eccentricity: 0.0006,
		Inclination:  51.6,
		NoradCatID:   opts.NoradCatID,
		RMS:          "1.234",
		Converged:    true,
		DataSource:   opts.DataSource,
	}, nil
}

// --- fixtures --------------------------------------------------------------

// makeOEMJSONFlat builds one per-object JSON-OEM record in the exact shape the
// data-source modules' build_oem_record emits: a single EPHEMERIS_DATA_BLOCK with
// a flat row-major EPHEMERIS_DATA (STATE_VECTOR_SIZE per state; epochs implied by
// START_TIME + i*STEP_SIZE).
func makeOEMJSONFlat(norad uint32, name, objID, frame, startISO string, stepSec int, states [][6]float64) []byte {
	flat := make([]float64, 0, len(states)*6)
	for _, s := range states {
		flat = append(flat, s[0], s[1], s[2], s[3], s[4], s[5])
	}
	doc := map[string]interface{}{
		"CCSDS_OEM_VERS": 2.0,
		"CREATION_DATE":  startISO,
		"ORIGINATOR":     "TEST",
		"CLASSIFICATION": "UNCLASSIFIED",
		"EPHEMERIS_DATA_BLOCK": []map[string]interface{}{{
			"OBJECT_NAME":       name,
			"OBJECT_ID":         objID,
			"NORAD_CAT_ID":      norad,
			"CENTER_NAME":       "EARTH",
			"REFERENCE_FRAME":   frame,
			"TIME_SYSTEM":       "UTC",
			"START_TIME":        startISO,
			"STOP_TIME":         startISO,
			"STEP_SIZE":         stepSec,
			"STATE_VECTOR_SIZE": 6,
			"EPHEMERIS_DATA":    flat,
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return b
}

// sampleStates returns a few deterministic (physically-arbitrary) state vectors —
// enough for the JSON-OEM record to render valid KVN for the fake-fitter count
// test (the fake fitter never inspects the bytes).
func sampleStates(seed uint32) [][6]float64 {
	base := float64(seed%1000) + 1
	out := make([][6]float64, 0, 4)
	for i := 0; i < 4; i++ {
		f := base + float64(i)
		out = append(out, [6]float64{
			-4000 + f, 3800 - f, -3800 + f, -6.0 + 0.01*f, -2.1 + 0.01*f, 4.1 - 0.01*f,
		})
	}
	return out
}

// parseKVNStates reads a CCSDS OEM KVN file (the checked-in ISS fixture) and
// returns its first data epoch (ISO), the uniform step in seconds, and the
// [x y z vx vy vz] state vectors — the raw material for JSON-OEM fixtures the
// store-backed source converts back to KVN for the real fit.
func parseKVNStates(t *testing.T, path string) (startISO string, stepSec int, states [][6]float64) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	inData := false
	var epochs []string
	for _, line := range strings.Split(string(raw), "\n") {
		tl := strings.TrimSpace(line)
		if tl == "" || strings.HasPrefix(tl, "COMMENT") {
			continue
		}
		if tl == "META_STOP" {
			inData = true
			continue
		}
		if tl == "META_START" {
			inData = false
			continue
		}
		if !inData || strings.Contains(tl, "=") {
			continue
		}
		fields := strings.Fields(tl)
		if len(fields) < 7 {
			continue
		}
		var s [6]float64
		bad := false
		for i := 0; i < 6; i++ {
			v, perr := strconv.ParseFloat(fields[i+1], 64)
			if perr != nil {
				bad = true
				break
			}
			s[i] = v
		}
		if bad {
			continue
		}
		epochs = append(epochs, fields[0])
		states = append(states, s)
	}
	if len(states) < 2 {
		t.Fatalf("fixture parse yielded %d states", len(states))
	}
	startISO = epochs[0]
	stepSec = kvnStepSeconds(t, epochs[0], epochs[1])
	return startISO, stepSec, states
}

func kvnStepSeconds(t *testing.T, a, b string) int {
	t.Helper()
	const layout = "2006-01-02T15:04:05.000"
	ta, err1 := parseAnyISO(a)
	tb, err2 := parseAnyISO(b)
	if err1 != nil || err2 != nil {
		t.Fatalf("parse epochs %q %q: %v / %v", a, b, err1, err2)
	}
	d := int(tb.Sub(ta).Seconds())
	if d <= 0 {
		t.Fatalf("non-positive step from %q -> %q", a, b)
	}
	_ = layout
	return d
}

func parseAnyISO(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable ISO time %q", s)
}
