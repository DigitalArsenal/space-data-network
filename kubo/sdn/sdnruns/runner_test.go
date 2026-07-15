package sdnruns_test

// End-to-end acceptance test for the supplemental-OMM OD run engine. It drives
// ONE run end-to-end producing a REAL OMM from operator ephemeris via the REAL
// analysis/od WASM module executing under modulert (the OD fit is NOT a Go
// reimplementation). Only the ephemeris SOURCE fetch is stubbed with a canned,
// checked-in real NASA ISS OEM fixture (the data-source pull is firewalled from a
// workstation) — the fit, the OMM production, the RMS, the same-ephemeris
// reference parity and the run recording are all real.
//
// It asserts: a Run is recorded (completed) with a per-object RMS, at least one
// reference comparison populated (CelesTrak SupGP same-ephemeris RMS + beats
// flag), a produced $OMM landed in the store (decoded back to the ISS NORAD), the
// run is listed + searchable by NORAD, and a VCM-format element download comes out.

import (
	"context"
	"os"
	"strings"
	"testing"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/sdnruns"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/sds"
	"github.com/ipfs/kubo/sdn/testsupport"
)

func TestSupplementalOMMRun_RealODFit(t *testing.T) {
	wasmPath := testsupport.SkipIfNoODModuleWasm(t)
	fixturePath := testsupport.SkipIfNoODEphemerisFixture(t)

	// ── Fitter over the REAL analysis/od WASM module (command surface) ─────────
	fitter := sdnruns.NewCommandFitter(func() ([]byte, error) {
		return os.ReadFile(wasmPath)
	}, t.Logf)

	// ── Node record store (OMM schema) + seeded CelesTrak SupGP reference ──────
	store := openOMMStore(t)
	seedCelestrakISSReference(t, store)

	oem, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read ISS OEM fixture: %v", err)
	}
	source := &fixtureSource{eph: []sdnruns.Ephemeris{{
		Provider:   "iss",
		Format:     "oem",
		ObjectName: "ISS",
		ObjectID:   "1998-067-A",
		NoradCatID: 25544,
		DataSource: "ISS-E",
		Bytes:      oem,
	}}}

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

	// ── Drive one run ─────────────────────────────────────────────────────────
	ctx := context.Background()
	run, err := runner.RunProviders(ctx, sdnruns.RunConfig{
		EnabledProviders: []string{"iss"},
		CelestrakSource:  "celestrak-supgp",
		SpacetrackSource: "spacetrack",
		ProducedSource:   "supplemental-omm",
	})
	if err != nil {
		t.Fatalf("RunProviders: %v", err)
	}

	// ── The run is recorded and completed ─────────────────────────────────────
	if run.Status != sdnruns.StatusCompleted {
		t.Fatalf("run status = %q, want completed (error=%q)", run.Status, run.Error)
	}
	if run.ObjectsDone != 1 || len(run.Objects) != 1 {
		t.Fatalf("expected exactly 1 object, got done=%d len=%d", run.ObjectsDone, len(run.Objects))
	}
	obj := run.Objects[0]
	if obj.Error != "" {
		t.Fatalf("object fit error: %s", obj.Error)
	}

	// ── The OD fit is real: it converged, produced a real RMS + ISS-like MM ────
	if !obj.Converged {
		t.Fatalf("expected the ISS OEM fit to converge")
	}
	if obj.RMS <= 0 || obj.RMS > 5.0 {
		t.Fatalf("object RMS = %.4f km, expected a plausible ISS fit RMS (0,5]", obj.RMS)
	}
	if obj.Norad != 25544 {
		t.Fatalf("object NORAD = %d, want 25544", obj.Norad)
	}
	if mm := obj.Elements.MeanMotion; mm < 15.3 || mm > 15.7 {
		t.Fatalf("fitted MEAN_MOTION = %.6f, not ISS-like", mm)
	}

	// ── An OMM came out of the fit and was stored (decode it back) ─────────────
	if obj.OMMCid == "" {
		t.Fatalf("expected a produced $OMM CID")
	}
	recs, err := store.ReadBySourceType(ctx, "supplemental-omm", "OMM")
	if err != nil {
		t.Fatalf("read produced OMM lane: %v", err)
	}
	if len(recs) == 0 {
		t.Fatalf("no produced $OMM records in the store")
	}
	decoded := OMM.GetRootAsOMM(recs[0], 0)
	if decoded.NORAD_CAT_ID() != 25544 {
		t.Fatalf("produced OMM NORAD = %d, want 25544", decoded.NORAD_CAT_ID())
	}
	if mm := decoded.MEAN_MOTION(); mm < 15.3 || mm > 15.7 {
		t.Fatalf("produced OMM MEAN_MOTION = %.6f, not ISS-like", mm)
	}
	if orig := string(decoded.ORIGINATOR()); orig != "SDN-OD" {
		t.Fatalf("produced OMM ORIGINATOR = %q, want SDN-OD (our OD fit)", orig)
	}

	// ── At least one reference comparison is populated ─────────────────────────
	if obj.CelestrakRMS == nil {
		t.Fatalf("expected the CelesTrak SupGP same-ephemeris RMS to be populated")
	}
	if *obj.CelestrakRMS <= 0 {
		t.Fatalf("CelesTrak reference RMS = %.4f, expected > 0", *obj.CelestrakRMS)
	}
	if obj.BeatsCelestrak == nil {
		t.Fatalf("expected the beats-CelesTrak flag to be set")
	}
	t.Logf("ISS fit: ours=%.3f km, CelesTrak same-ephemeris=%.3f km, beats=%v",
		obj.RMS, *obj.CelestrakRMS, *obj.BeatsCelestrak)

	// ── The run is listed and searchable by NORAD ─────────────────────────────
	list := runsStore.List()
	found := false
	for _, s := range list {
		if s.ID == run.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("run %s not present in List()", run.ID)
	}
	hits, err := runsStore.Objects(run.ID, "25544")
	if err != nil {
		t.Fatalf("Objects search: %v", err)
	}
	if len(hits) != 1 || hits[0].Norad != 25544 {
		t.Fatalf("NORAD search returned %d rows, want 1 with NORAD 25544", len(hits))
	}
	if miss, _ := runsStore.Objects(run.ID, "99999"); len(miss) != 0 {
		t.Fatalf("NORAD search for a non-present id returned %d rows, want 0", len(miss))
	}

	// ── Element downloads (VCM-format text) ───────────────────────────────────
	vcm, _, fn, ok := sdnruns.RenderElements(obj, "cdm")
	if !ok {
		t.Fatalf("cdm/VCM render returned not-ok")
	}
	if !strings.Contains(vcm, "VECTOR COVARIANCE") || !strings.Contains(vcm, "25544") || !strings.Contains(vcm, "MEAN MOTION") {
		t.Fatalf("VCM download missing expected element keywords:\n%s", vcm)
	}
	if !strings.HasSuffix(fn, ".vcm") {
		t.Fatalf("VCM filename = %q", fn)
	}
	tle, _, _, ok := sdnruns.RenderElements(obj, "tle")
	if !ok || !strings.Contains(tle, "\n1 ") || !strings.Contains(tle, "\n2 ") {
		t.Fatalf("TLE download not a two-line element set:\n%s", tle)
	}
	ommTxt, _, _, ok := sdnruns.RenderElements(obj, "omm")
	if !ok || !strings.Contains(ommTxt, "CCSDS_OMM_VERS") || !strings.Contains(ommTxt, "MEAN_MOTION") {
		t.Fatalf("OMM download not a CCSDS OMM KVN:\n%s", ommTxt)
	}
	if _, _, _, ok := sdnruns.RenderElements(obj, "bogus"); ok {
		t.Fatalf("unknown format should not render")
	}
}

// fixtureSource is the stubbed ephemeris source: it returns canned real ephemeris
// (the OD fit downstream is real). This stands in for the firewalled data-source
// module pull.
type fixtureSource struct{ eph []sdnruns.Ephemeris }

func (f *fixtureSource) Pull(_ context.Context, provider string) ([]sdnruns.Ephemeris, error) {
	var out []sdnruns.Ephemeris
	for _, e := range f.eph {
		if e.Provider == provider {
			out = append(out, e)
		}
	}
	return out, nil
}

// seedCelestrakISSReference stores the real same-day CelesTrak SupGP ISS
// [Segment 01] element set (NORAD 25544, EPOCH 2026-07-13T12:00:00) into the store
// under the reference lane, so the run scores it over the same ephemeris. These
// are the actual CelesTrak values from
// analysis/od/tests/data/supgp-reference/iss/celestrak_supgp_iss-e_2026-07-13.csv.
func seedCelestrakISSReference(t *testing.T, store *sdnstore.Store) {
	t.Helper()
	sized := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS [Segment 01]").
		WithObjectID("1998-067A").
		WithEpoch("2026-07-13T12:00:00.000000").
		WithMeanMotion(15.48986033).
		WithEccentricity(0.0006726).
		WithInclination(51.6300).
		WithRaOfAscNode(169.8722).
		WithArgOfPericenter(293.0452).
		WithMeanAnomaly(20.1755).
		WithBStar(0.00092967).
		WithMeanMotionDot(0.00051371).
		WithOriginator("celestrak-supgp").
		Build()
	if _, err := store.Store(context.Background(), "celestrak-supgp", "OMM", sized[4:]); err != nil {
		t.Fatalf("seed CelesTrak reference: %v", err)
	}
}

// openOMMStore builds a durable sdnstore over in-memory blockstore + datastore
// with the OMM FlatSQL schema registered (mirrors the sdnstore suite).
func openOMMStore(t *testing.T) *sdnstore.Store {
	t.Helper()
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)
	st, err := sdnstore.Open(sdnstore.Config{
		Blockstore:     bs,
		Datastore:      mds,
		Schemas:        ommSchemas(),
		RuntimeOptions: []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
	})
	if err != nil {
		t.Fatalf("open OMM store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func ommSchemas() sdnstore.SchemaProvider {
	return sdnstore.SchemaProviderFunc(func(ty string) (schema, fileID, tableName string, ok bool) {
		if ty == "OMM" {
			return ommSchema, "$OMM", "OMM", true
		}
		return "", "", "", false
	})
}

func sharedAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return base + "/sdn-flatsqlrt-test-aot"
}

const ommSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    CENTER_NAME:string;
    REFERENCE_FRAME:RFM;
    REFERENCE_FRAME_EPOCH:string;
    TIME_SYSTEM:timingStandard = UTC;
    MEAN_ELEMENT_THEORY:meanElementSource = SGP4;
    COMMENT:string;
    EPOCH:string;
    SEMI_MAJOR_AXIS:double;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    RA_OF_ASC_NODE:double;
    ARG_OF_PERICENTER:double;
    MEAN_ANOMALY:double;
    GM:double;
    MASS:double;
    SOLAR_RAD_AREA:double;
    SOLAR_RAD_COEFF:double;
    DRAG_AREA:double;
    DRAG_COEFF:double;
    EPHEMERIS_TYPE:ephemerisFormat = SGP4;
    CLASSIFICATION_TYPE:string;
    NORAD_CAT_ID:uint32;
    ELEMENT_SET_NO:uint32;
    REV_AT_EPOCH:double;
    BSTAR:double;
    MEAN_MOTION_DOT:double;
    MEAN_MOTION_DDOT:double;
    COV_REFERENCE_FRAME:RFM;
    COVARIANCE:[double];
    USER_DEFINED_BIP_0044_TYPE:uint;
    USER_DEFINED_OBJECT_DESIGNATOR:string;
    USER_DEFINED_EARTH_MODEL:string;
    USER_DEFINED_EPOCH_TIMESTAMP: double;
    USER_DEFINED_MICROSECONDS: double;
  }
  root_type OMM;
  file_identifier "$OMM";
`
