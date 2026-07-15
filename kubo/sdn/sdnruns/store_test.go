package sdnruns_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/sdnruns"
)

// TestStorePersistLifecycleAndSearch covers the run store lifecycle (start ->
// append -> finish), the live snapshot, the newest-first list, NORAD search, and
// durable reload — all without the OD WASM module so it runs everywhere.
func TestStorePersistLifecycleAndSearch(t *testing.T) {
	dir := t.TempDir()
	store, err := sdnruns.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	started := time.Now().UTC()
	run := &sdnruns.Run{
		ID:             sdnruns.NewRunID(started),
		Started:        started,
		Providers:      []string{"iss", "starlink"},
		ObjectsTotal:   2,
		EphemerisFiles: 2,
	}
	if err := store.StartRun(run); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// A live run is reported while it executes.
	if live, ok := store.Live(); !ok || live.ID != run.ID || live.ObjectsRemaining != 2 {
		t.Fatalf("Live() = %+v ok=%v, want the running run with 2 remaining", live, ok)
	}

	if err := store.AppendObject(run.ID, sdnruns.ObjectResult{
		Norad: 25544, ObjectName: "ISS", Provider: "iss", RMS: 0.071, Converged: true,
		CelestrakRMS: ptr(0.111), BeatsCelestrak: bptr(true),
	}); err != nil {
		t.Fatalf("AppendObject 1: %v", err)
	}
	if err := store.AppendObject(run.ID, sdnruns.ObjectResult{
		Norad: 44713, ObjectName: "STARLINK-1007", Provider: "starlink", RMS: 0.201, Converged: true,
	}); err != nil {
		t.Fatalf("AppendObject 2: %v", err)
	}

	if live, ok := store.Live(); !ok || live.ObjectsDone != 2 || live.ObjectsRemaining != 0 {
		t.Fatalf("Live() after appends = %+v, want done=2 remaining=0", live)
	}

	if err := store.FinishRun(run.ID, sdnruns.StatusCompleted, ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if _, ok := store.Live(); ok {
		t.Fatalf("Live() should be empty after finish")
	}

	got, err := store.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != sdnruns.StatusCompleted || got.ObjectsDone != 2 || got.BeatCount != 1 {
		t.Fatalf("run aggregates: status=%q done=%d beats=%d", got.Status, got.ObjectsDone, got.BeatCount)
	}
	// avg over converged objects = (0.071+0.201)/2 = 0.136
	if got.AvgRMS < 0.135 || got.AvgRMS > 0.137 {
		t.Fatalf("avg_rms = %.4f, want ~0.136", got.AvgRMS)
	}

	// Search by NORAD substring.
	hits, err := store.Objects(run.ID, "25544")
	if err != nil || len(hits) != 1 || hits[0].Norad != 25544 {
		t.Fatalf("search 25544: hits=%d err=%v", len(hits), err)
	}
	// Search by object name.
	byName, _ := store.Objects(run.ID, "starlink")
	if len(byName) != 1 || byName[0].Norad != 44713 {
		t.Fatalf("search by name: %d rows", len(byName))
	}

	// Durable reload with a fresh store over the same dir.
	reopened, err := sdnruns.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if list := reopened.List(); len(list) != 1 || list[0].ID != run.ID {
		t.Fatalf("reopened list = %+v", list)
	}
	rl, err := reopened.Get(run.ID)
	if err != nil || len(rl.Objects) != 2 {
		t.Fatalf("reopened run objects = %d err=%v", len(rl.Objects), err)
	}
}

// TestRenderElementsFormats checks all three element downloads render coherent,
// format-specific text from a fitted element set.
func TestRenderElementsFormats(t *testing.T) {
	obj := sdnruns.ObjectResult{
		Norad: 25544, ObjectName: "ISS", ObjectID: "1998-067-A", RMS: 0.071,
		CelestrakRMS: ptr(0.111),
		Elements: sdnruns.Elements{
			ObjectName: "ISS", ObjectID: "1998-067-A", Epoch: "2026-07-13T12:00:00.000000Z",
			NoradCatID: 25544, MeanMotion: 15.48986033, Eccentricity: 0.0006726,
			Inclination: 51.6300, RaOfAscNode: 169.8722, ArgOfPericenter: 293.0452,
			MeanAnomaly: 20.1755, Bstar: 0.00092967, MeanMotionDot: 0.00051371,
			ElementSetNo: 999, RevAtEpoch: 12345, RMSKm: 0.071, Converged: true,
		},
	}

	tle, ct, fn, ok := sdnruns.RenderElements(obj, "tle")
	if !ok {
		t.Fatal("tle not ok")
	}
	lines := strings.Split(strings.TrimSpace(tle), "\n")
	if len(lines) != 3 {
		t.Fatalf("TLE should be name + 2 lines, got %d:\n%s", len(lines), tle)
	}
	if !strings.HasPrefix(lines[1], "1 25544") || !strings.HasPrefix(lines[2], "2 25544") {
		t.Fatalf("TLE line prefixes wrong:\n%s", tle)
	}
	if !strings.HasSuffix(fn, ".tle") || !strings.Contains(ct, "text/plain") {
		t.Fatalf("tle meta: fn=%q ct=%q", fn, ct)
	}

	omm, _, _, ok := sdnruns.RenderElements(obj, "omm")
	if !ok || !strings.Contains(omm, "CCSDS_OMM_VERS") || !strings.Contains(omm, "NORAD_CAT_ID = 25544") {
		t.Fatalf("omm render:\n%s", omm)
	}

	vcm, _, _, ok := sdnruns.RenderElements(obj, "cdm")
	if !ok || !strings.Contains(vcm, "VECTOR COVARIANCE") || !strings.Contains(vcm, "CELESTRAK RMS") {
		t.Fatalf("vcm render:\n%s", vcm)
	}

	if _, _, _, ok := sdnruns.RenderElements(obj, "nope"); ok {
		t.Fatal("unknown format should be not-ok")
	}
}

func ptr(v float64) *float64 { return &v }
func bptr(v bool) *bool      { return &v }
