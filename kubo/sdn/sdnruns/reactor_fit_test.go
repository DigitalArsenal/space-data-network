package sdnruns_test

// Proof for the resident-reactor OD conversion (SDN Flow Platform Phase 3):
//
//   - TestReactorCommandFitParity proves the RESIDENT REACTOR fit
//     (dist/isomorphic/module.wasm, driven via plugin_invoke_stream on a live
//     instance) returns the SAME fit result — RMS, converged, and every mean
//     element, value-for-value — as the legacy COMMAND fit (module.command.wasm,
//     driven via _start). Parity is sacred: the reactor is a pure plumbing change
//     (same C++ fit, byte-identical $PIV request), so the result must be identical.
//
//   - TestReactorParallelSpeedup fits a fleet of objects the REAL OD reactor,
//     sequentially (pool size 1) vs in parallel (pool size runtime.NumCPU()), and
//     asserts (a) every object is fitted in both, (b) each object's RMS is
//     IDENTICAL between the sequential and parallel runs (parallelism preserves
//     parity), and (c) the parallel wall-clock is a real speedup over sequential.

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/sdnruns"
	"github.com/ipfs/kubo/sdn/testsupport"
)

func TestReactorCommandFitParity(t *testing.T) {
	reactorWasm := testsupport.SkipIfNoODModuleWasm(t)        // module.wasm (reactor)
	commandWasm := testsupport.SkipIfNoODCommandModuleWasm(t) // module.command.wasm
	fixturePath := testsupport.SkipIfNoODEphemerisFixture(t)

	oem, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read ISS OEM fixture: %v", err)
	}

	// The SAME ephemeris + the SAME options fed to both surfaces.
	opts := sdnruns.FitOptions{
		InputFormat: "oem",
		DataSource:  "ISS-E",
		ObjectName:  "ISS",
		ObjectID:    "1998-067-A",
		NoradCatID:  25544,
	}

	reactor := sdnruns.NewReactorFitter(func() ([]byte, error) { return os.ReadFile(reactorWasm) }, 1, t.Logf)
	defer reactor.Close()
	command := sdnruns.NewCommandFitter(func() ([]byte, error) { return os.ReadFile(commandWasm) }, t.Logf)

	ctx := context.Background()
	reactorRes, err := reactor.Fit(ctx, oem, opts)
	if err != nil {
		t.Fatalf("reactor fit: %v", err)
	}
	commandRes, err := command.Fit(ctx, oem, opts)
	if err != nil {
		t.Fatalf("command fit: %v", err)
	}

	// Value-for-value parity across the ENTIRE fit result: RMS (the module emits
	// it as a fixed-precision string, compared byte-for-byte here), converged, and
	// every mean element. reflect.DeepEqual requires exact float equality — which
	// must hold because both surfaces run the identical fit over the identical
	// request. Do NOT weaken this to an approximate compare.
	if !reflect.DeepEqual(reactorRes, commandRes) {
		t.Fatalf("reactor fit != command fit (parity broken):\n reactor=%+v\n command=%+v", reactorRes, commandRes)
	}

	rms, err := reactorRes.RMSKm()
	if err != nil {
		t.Fatalf("parse RMS: %v", err)
	}
	if !reactorRes.Converged || rms <= 0 || rms > 5.0 {
		t.Fatalf("implausible ISS fit: converged=%v rms=%.4f", reactorRes.Converged, rms)
	}
	t.Logf("reactor==command parity holds: RMS=%q km (%.3f), converged=%v, MEAN_MOTION=%.6f",
		reactorRes.RMS, rms, reactorRes.Converged, reactorRes.MeanMotion)
}

func TestReactorParallelSpeedup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping heavy real-OD parallel speedup benchmark in -short mode")
	}
	wasmPath := testsupport.SkipIfNoODModuleWasm(t)
	fixturePath := testsupport.SkipIfNoODEphemerisFixture(t)

	cpus := runtime.NumCPU()
	if cpus < 2 {
		t.Skip("single-CPU host: no parallel speedup to demonstrate")
	}
	// Fleet size: enough to fill the pool a couple of times so the parallel run
	// shows a clear speedup, while keeping the sequential baseline bounded (each
	// REAL OD fit is ~2s). 2*NumCPU, capped at 24.
	numObjects := 2 * cpus
	if numObjects > 24 {
		numObjects = 24
	}

	// Build numObjects distinct objects from the REAL checked-in ISS ephemeris
	// (same state vectors, distinct identity) as JSON-OEM records — the shape the
	// data-source modules write. The store-backed source converts each to KVN and
	// the REAL od reactor fits it.
	startISO, stepSec, states := parseKVNStates(t, fixturePath)
	if len(states) < 8 {
		t.Fatalf("fixture yielded too few state vectors (%d)", len(states))
	}

	seedFleet := func() *fakeRecordStore {
		store := newFakeRecordStore()
		for i := 0; i < numObjects; i++ {
			norad := uint32(90000 + i)
			rec := makeOEMJSONFlat(norad, fmt.Sprintf("ISS-CLONE-%d", i),
				fmt.Sprintf("1998-067-%03d", i), "EME2000", startISO, stepSec, states)
			store.seed("spacex-starlink", "OEM", rec)
		}
		return store
	}

	// One run over the fleet with a given pool size; returns per-NORAD RMS + wall.
	runFleet := func(pool int) (map[uint32]float64, time.Duration) {
		store := seedFleet()
		fitter := sdnruns.NewReactorFitter(func() ([]byte, error) { return os.ReadFile(wasmPath) }, pool, t.Logf)
		defer fitter.Close()
		source := sdnruns.NewStoreEphemerisSource(store, nil, t.Logf)
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
		start := time.Now()
		run, err := runner.RunProviders(context.Background(), sdnruns.RunConfig{
			EnabledProviders: []string{"spacex-starlink"},
			ProducedSource:   "supplemental-omm",
		})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("RunProviders(pool=%d): %v", pool, err)
		}
		if run.ObjectsTotal != numObjects || run.ObjectsDone != numObjects || len(run.Objects) != numObjects {
			t.Fatalf("pool=%d fitted total=%d done=%d len=%d, want %d",
				pool, run.ObjectsTotal, run.ObjectsDone, len(run.Objects), numObjects)
		}
		rms := make(map[uint32]float64, numObjects)
		for _, obj := range run.Objects {
			if obj.Error != "" {
				t.Fatalf("pool=%d object %d fit error: %s", pool, obj.Norad, obj.Error)
			}
			if !obj.Converged || obj.RMS <= 0 || obj.RMS > 5.0 {
				t.Fatalf("pool=%d object %d implausible: converged=%v rms=%.4f", pool, obj.Norad, obj.Converged, obj.RMS)
			}
			rms[obj.Norad] = obj.RMS
		}
		return rms, elapsed
	}

	// Sequential baseline (pool=1 → runner runs the object loop serially).
	seqRMS, seqWall := runFleet(1)
	// Parallel (pool=NumCPU → runner fans out to NumCPU workers, each with its own
	// resident OD reactor instance).
	parRMS, parWall := runFleet(cpus)

	// Parity: every object's RMS is IDENTICAL between the sequential and parallel
	// runs — parallelism must not perturb any fit.
	if len(seqRMS) != numObjects || len(parRMS) != numObjects {
		t.Fatalf("object-count mismatch: seq=%d par=%d want=%d", len(seqRMS), len(parRMS), numObjects)
	}
	for norad, s := range seqRMS {
		p, ok := parRMS[norad]
		if !ok {
			t.Fatalf("object %d fitted sequentially but missing from the parallel run", norad)
		}
		if p != s {
			t.Fatalf("object %d RMS parity broken: sequential=%.6f parallel=%.6f", norad, s, p)
		}
	}

	speedup := seqWall.Seconds() / parWall.Seconds()
	t.Logf("REAL OD parallel fan-out: objects=%d N=%d  sequential=%s  parallel=%s  speedup=%.2fx  (per-object RMS identical)",
		numObjects, cpus, seqWall.Round(time.Millisecond), parWall.Round(time.Millisecond), speedup)

	// A real wall-clock speedup. The parallel run must beat the sequential one;
	// with N>=2 fully-CPU-bound ~2s fits the margin is large, so a strict "faster"
	// gate is not flaky.
	if parWall >= seqWall {
		t.Fatalf("no parallel speedup: sequential=%s parallel=%s (N=%d)", seqWall, parWall, cpus)
	}
}
