package sds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PCF"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PRW"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// TestPCFAndPRWEventsFromPublishedJS is the E2E for the SDS 1.263/1.264
// refresh: records built by the PUBLISHED spacedatastandards.org JavaScript
// bindings (testdata/pcf-prw/build-fixtures.mjs, run against sdn-js's
// installed package) are admitted and decoded by the node's embedded schemas
// and by the Go bindings it links.
//
//   - $PCF 1.0 carrying every table: integrator, forces (space weather,
//     ECOM2), mean-element and segment options, fit policy, three event
//     definitions (one with an expression program), maneuver detection,
//     reentry, launch priors and output.
//   - $PRW execution request whose EVENTS are the same PCFEventDefinition
//     tables, beside the shared PCFIntegratorSettings and PCFForceModel.
//   - $PRW execution result whose EVENTS are located PRWEventRecords.
//
// Each record goes through three independent readers: the node's envelope and
// structure check, the flatc reflection verifier and JSON converter built
// from the EMBEDDED IDL (so a stale embed fails on a slot or type that no
// longer matches), and the linked generated Go accessors.
func TestPCFAndPRWEventsFromPublishedJS(t *testing.T) {
	fixtures := loadPCFPRWFixtures(t)
	v := newConverterValidator(t)
	ctx := context.Background()

	pcfIDL, err := schemasFS.ReadFile(embeddedSchemaPath("PCF.fbs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pcfIDL), "// Version: 1.0.0\n") {
		t.Fatal("the embedded PCF.fbs is not $PCF 1.0.0")
	}

	t.Run("PCF 1.0", func(t *testing.T) {
		raw := fixtures["pcf-1-0.bin"]
		doc := admitAndConvert(ctx, t, v, "PCF.fbs", raw)

		pcfJSONEquals(t, doc, "FFM", "THEORY")
		pcfJSONEquals(t, doc, "FFM-1", "PROFILE")
		pcfJSONEquals(t, doc, "DOPRI853", "INTEGRATOR", "ALGORITHM")
		pcfJSONEquals(t, doc, 250000.0, "INTEGRATOR", "MAXIMUM_STEPS")
		pcfJSONEquals(t, doc, 6, "INTEGRATOR", "ABSOLUTE_TOLERANCES", "#")
		pcfJSONEquals(t, doc, "EGM2008", "FORCES", "GRAVITY_CHOICE")
		pcfJSONEquals(t, doc, "JB2008", "FORCES", "ATMOSPHERE_MODEL")
		pcfJSONEquals(t, doc, 148.25, "FORCES", "WEATHER", "F107")
		pcfJSONEquals(t, doc, "2026-10-24T00:00:00Z", "FORCES", "WEATHER", "EPOCH", "ISO8601")
		pcfJSONEquals(t, doc, 4e-11, "FORCES", "ECOM2", "D2_COS_M_S2")
		pcfJSONEquals(t, doc, "DE440", "FORCES", "EPHEMERIS_SOURCE")
		pcfJSONEquals(t, doc, "DCA_COMMON_MODE", "FORCES", "DENSITY_CORRECTION")
		pcfJSONEquals(t, doc, "HWM14", "FORCES", "NEUTRAL_WIND")
		pcfJSONEquals(t, doc, 18.0, "MEAN_ELEMENT", "ZONAL_MAXIMUM_DEGREE")
		pcfJSONEquals(t, doc, 60.0, "SEGMENTS", "MOON_GRAVITY_MAXIMUM_DEGREE")
		pcfJSONEquals(t, doc, 3, "FIT", "CANDIDATE_SPANS_HOURS", "#")
		pcfJSONEquals(t, doc, 0.5, "FIT", "DRAG_PRIOR", "SIGMA")
		pcfJSONEquals(t, doc, 3, "EVENTS", "#")
		pcfJSONEquals(t, doc, "APSIS", "EVENTS", 0, "KIND")
		pcfJSONEquals(t, doc, 0.8660254037844386, "EVENTS", 1, "PLANE_NORMAL", "Z")
		pcfJSONEquals(t, doc, "STOP", "EVENTS", 2, "ACTION")
		pcfJSONEquals(t, doc, 3, "EVENTS", 2, "EXPRESSION", "TERMS", "#")
		pcfJSONEquals(t, doc, "UNSCENTED", "REENTRY", "WINDOW_METHOD")
		pcfJSONEquals(t, doc, "CORRELATION_TIME", "MANEUVER_DETECTION", "CORRELATION_MODEL")
		pcfJSONEquals(t, doc, 1.5, "LAUNCH", "SEPARATION_SIGMA_M_S")
		pcfJSONEquals(t, doc, "GCRF", "OUTPUT", "FRAME")

		if !PCF.PCFBufferHasIdentifier(raw) {
			t.Fatal("the JS-built buffer does not carry the $PCF identifier")
		}
		cfg := PCF.GetRootAsPCF(raw, 0)
		if string(cfg.CONFIGURATION_ID()) != "sdn-sds-1-264-fixture" || cfg.THEORY() != PCF.EnumValuespcfTheory["FFM"] {
			t.Fatalf("identity: %q theory %v", cfg.CONFIGURATION_ID(), cfg.THEORY())
		}
		integrator := cfg.INTEGRATOR(nil)
		if integrator == nil || integrator.ALGORITHM() != PCF.EnumValuespcfIntegratorAlgorithm["DOPRI853"] ||
			integrator.MAXIMUM_STEPS() != 250000 || integrator.ABSOLUTE_TOLERANCESLength() != 6 ||
			integrator.ABSOLUTE_TOLERANCES(5) != 0.000003 || integrator.RELATIVE_TOLERANCE() != 1e-11 {
			t.Fatal("integrator settings were lost")
		}
		forces := cfg.FORCES(nil)
		if forces == nil || forces.GRAVITY_CHOICE() != PCF.EnumValuespcfGravitySelection["EGM2008"] ||
			!forces.HAS_MAXIMUM_DEGREE() || forces.MAXIMUM_DEGREE() != 70 || forces.MAXIMUM_ORDER() != 60 ||
			forces.THIRD_BODY_IDSLength() != 2 || forces.THIRD_BODY_IDS(1) != 301 ||
			string(forces.EPHEMERIS_SOURCE()) != "DE440" {
			t.Fatal("force model was lost")
		}
		if weather := forces.WEATHER(nil); weather == nil || weather.F107() != 148.25 || weather.AP_INDEX() != 9 {
			t.Fatal("space weather was lost")
		}
		if ecom2 := forces.ECOM2(nil); ecom2 == nil || ecom2.D0_M_S2() != 1e-9 || ecom2.B3_SIN_M_S2() != 2e-12 {
			t.Fatal("ECOM2 was lost")
		}
		fit := cfg.FIT(nil)
		if fit == nil || fit.CANDIDATE_SPANS_HOURSLength() != 3 || fit.CANDIDATE_SPANS_HOURS(2) != 96 ||
			!fit.HAS_HOLDOUT_HOURS() || fit.HOLDOUT_HOURS() != 12 || fit.DRAG_PRIOR(nil).SIGMA() != 0.5 {
			t.Fatal("fit policy was lost")
		}
		if cfg.EVENTSLength() != 3 {
			t.Fatalf("events = %d, want 3", cfg.EVENTSLength())
		}
		stop := new(PCF.PCFEventDefinition)
		if !cfg.EVENTS(stop, 2) || stop.KIND() != PCF.EnumValuespcfEventKind["EXPRESSION"] ||
			stop.ACTION() != PCF.EnumValuespcfEventAction["STOP"] || stop.OCCURRENCE() != 2 ||
			string(stop.SECOND_TRAJECTORY_PORT()) != "second_trajectory" {
			t.Fatal("expression event was lost")
		}
		program := stop.EXPRESSION(nil)
		term := new(PCF.PCFExpressionTerm)
		if program == nil || program.TERMSLength() != 3 || !program.TERMS(term, 1) ||
			term.OPERATION() != PCF.EnumValuespcfExpressionOperation["CONSTANT"] || term.VALUE() != 180000 {
			t.Fatal("expression program was lost")
		}
		if output := cfg.OUTPUT(nil); output == nil || output.FRAME() != PCF.EnumValuespcfOutputFrame["GCRF"] ||
			output.STEP_SECONDS() != 45 || output.MAXIMUM_STATES() != 120000 {
			t.Fatal("output was lost")
		}
	})

	t.Run("PRW request with EVENTS", func(t *testing.T) {
		raw := fixtures["prw-execution-request-events.bin"]
		doc := admitAndConvert(ctx, t, v, "PRW.fbs", raw)

		pcfJSONEquals(t, doc, "DOPRI853", "EXECUTION_REQUEST", "INTEGRATOR", "ALGORITHM")
		pcfJSONEquals(t, doc, "EGM2008", "EXECUTION_REQUEST", "FORCES", "GRAVITY_CHOICE")
		pcfJSONEquals(t, doc, 148.25, "EXECUTION_REQUEST", "FORCES", "WEATHER", "F107")
		pcfJSONEquals(t, doc, 3, "EXECUTION_REQUEST", "EVENTS", "#")
		pcfJSONEquals(t, doc, "PLANE_CROSSING", "EXECUTION_REQUEST", "EVENTS", 1, "KIND")
		pcfJSONEquals(t, doc, "DECREASING", "EXECUTION_REQUEST", "EVENTS", 1, "DIRECTION")
		pcfJSONEquals(t, doc, 3.0, "EXECUTION_REQUEST", "EVENTS", 1, "OCCURRENCE")

		if !PRW.PRWBufferHasIdentifier(raw) {
			t.Fatal("the JS-built buffer does not carry the $PRW identifier")
		}
		request := PRW.GetRootAsPRW(raw, 0).EXECUTION_REQUEST(nil)
		if request == nil || request.INTEGRATOR(nil) == nil || request.FORCES(nil) == nil {
			t.Fatal("execution request lost its shared PCF tables")
		}
		if request.INTEGRATOR(nil).ALGORITHM() != PRW.EnumValuespcfIntegratorAlgorithm["DOPRI853"] ||
			request.INTEGRATOR(nil).MAXIMUM_STEPS() != 250000 {
			t.Fatal("shared integrator settings were lost")
		}
		if request.EVENTSLength() != 3 {
			t.Fatalf("request events = %d, want 3", request.EVENTSLength())
		}
		apsis := new(PRW.PCFEventDefinition)
		if !request.EVENTS(apsis, 0) || apsis.KIND() != PRW.EnumValuespcfEventKind["APSIS"] ||
			apsis.DIRECTION() != PRW.EnumValuespcfEventDirection["INCREASING"] ||
			apsis.ACTION() != PRW.EnumValuespcfEventAction["RECORD"] || apsis.TOLERANCE_SECONDS() != 1e-6 {
			t.Fatal("apsis event definition was lost")
		}
		if initial := request.INITIAL(nil); initial == nil || initial.STATE(nil).POSITION(nil).X() != 6778137 ||
			string(initial.OBJECT_ID()) != "2026-100A" {
			t.Fatal("initial state was lost")
		}
	})

	t.Run("PRW result with EVENTS", func(t *testing.T) {
		raw := fixtures["prw-execution-result-events.bin"]
		doc := admitAndConvert(ctx, t, v, "PRW.fbs", raw)

		pcfJSONEquals(t, doc, 2, "EXECUTION_RESULT", "EVENTS", "#")
		pcfJSONEquals(t, doc, "EXPRESSION", "EXECUTION_RESULT", "EVENTS", 1, "KIND")
		pcfJSONEquals(t, doc, 2.0, "EXECUTION_RESULT", "STOPPED_BY_EVENT")
		pcfJSONEquals(t, doc, 3, "EXECUTION_RESULT", "EVENT_OCCURRENCES", "#")

		result := PRW.GetRootAsPRW(raw, 0).EXECUTION_RESULT(nil)
		if result == nil || result.EVENTSLength() != 2 || result.EVENT_OCCURRENCESLength() != 3 ||
			result.EVENT_OCCURRENCES(0) != 2 || result.EVENT_OCCURRENCES(1) != 0 || result.STOPPED_BY_EVENT() != 2 {
			t.Fatal("event bookkeeping was lost")
		}
		record := new(PRW.PRWEventRecord)
		if !result.EVENTS(record, 0) || record.EVENT_INDEX() != 0 || record.KIND() != PRW.EnumValuespcfEventKind["APSIS"] ||
			record.DIRECTION() != PRW.EnumValuespcfEventDirection["INCREASING"] ||
			record.ELAPSED_SECONDS() != 3723.25 || record.RESIDUAL() != 1.25e-9 || record.ITERATIONS() != 4 ||
			string(record.EPOCH(nil).ISO8601()) != "2026-10-24T13:02:03Z" ||
			record.STATE(nil).STATE(nil).POSITION(nil).X() != 6771000.25 {
			t.Fatal("located event record was lost")
		}
		if final := result.FINAL_SAMPLE(nil); final == nil || final.ACCEPTED_STEPS() != 1440 || final.REJECTED_STEPS() != 3 {
			t.Fatal("final sample was lost")
		}
	})
}

type pcfPRWManifest struct {
	SDSVersion string `json:"sdsVersion"`
	Files      map[string]struct {
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// loadPCFPRWFixtures reads the JS-built records and holds them to their
// provenance: built by the SDS version this module links, byte for byte as
// recorded. A pin bump that leaves them stale fails here and is repaired by
// running testdata/pcf-prw/build-fixtures.mjs.
func loadPCFPRWFixtures(t *testing.T) map[string][]byte {
	t.Helper()
	dir := filepath.Join("testdata", "pcf-prw")
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest pcfPRWManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	linked := regexp.MustCompile(`github\.com/DigitalArsenal/spacedatastandards\.org/lib/go v(\d+\.\d+\.\d+)`).FindSubmatch(goMod)
	if linked == nil {
		t.Fatal("go.mod does not pin spacedatastandards.org/lib/go")
	}
	if manifest.SDSVersion != string(linked[1]) {
		t.Fatalf("fixtures were built by spacedatastandards.org %s but the node links %s: run node sdn-server/internal/sds/testdata/pcf-prw/build-fixtures.mjs",
			manifest.SDSVersion, linked[1])
	}
	fixtures := map[string][]byte{}
	for name, want := range manifest.Files {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if len(raw) != want.Bytes || hex.EncodeToString(sum[:]) != want.SHA256 {
			t.Fatalf("%s does not match fixtures.json: rebuild it with build-fixtures.mjs", name)
		}
		fixtures[name] = raw
	}
	for _, name := range []string{"pcf-1-0.bin", "prw-execution-request-events.bin", "prw-execution-result-events.bin"} {
		if fixtures[name] == nil {
			t.Fatalf("fixtures.json lists no %s", name)
		}
	}
	return fixtures
}

// admitAndConvert runs a record through the node's own readers and returns the
// JSON the embedded IDL's converter renders for it.
func admitAndConvert(ctx context.Context, t *testing.T, v *Validator, schema string, raw []byte) map[string]interface{} {
	t.Helper()
	if form, err := v.DetectEnvelopeForm(schema, raw); err != nil || form != EnvelopeBare {
		t.Fatalf("%s envelope form = %q, %v; want the bare stored form", schema, form, err)
	}
	if err := v.Validate(ctx, schema, raw); err != nil {
		t.Fatalf("%s Validate: %v", schema, err)
	}
	if err := v.VerifyRecord(ctx, schema, raw); err != nil {
		t.Fatalf("%s VerifyRecord (embedded IDL): %v", schema, err)
	}
	out, err := v.FlatBufferToJSON(ctx, schema, raw, wasm.FlatcCompactJSON)
	if err != nil {
		t.Fatalf("%s FlatBufferToJSON (embedded IDL): %v", schema, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(nonFiniteJSONWord.ReplaceAll(out, []byte("${1}null")), &doc); err != nil {
		t.Fatalf("%s JSON: %v", schema, err)
	}
	return doc
}

// flatc prints a non-finite double (an RFM field whose schema default is NaN,
// written explicitly by the JS object API) as the bare word nan, inf or -inf,
// which is not JSON. None of the values this test reads is non-finite.
var nonFiniteJSONWord = regexp.MustCompile(`(:\s*)-?(?:nan|inf)\b`)

// pcfJSONEquals walks doc by object keys and array indexes; a trailing "#"
// asks for the length of the array reached. Numbers compare to a relative
// 1e-12, because the converter prints doubles as text.
func pcfJSONEquals(t *testing.T, doc interface{}, want interface{}, path ...interface{}) {
	t.Helper()
	cur := doc
	for _, step := range path {
		switch s := step.(type) {
		case string:
			if s == "#" {
				array, ok := cur.([]interface{})
				if !ok {
					t.Fatalf("%v: %T is not an array", path, cur)
				}
				cur = float64(len(array))
				continue
			}
			object, ok := cur.(map[string]interface{})
			if !ok {
				t.Fatalf("%v: %T is not an object at %q", path, cur, s)
			}
			if cur, ok = object[s]; !ok {
				t.Fatalf("%v: the converter rendered no %q", path, s)
			}
		case int:
			array, ok := cur.([]interface{})
			if !ok || s >= len(array) {
				t.Fatalf("%v: no element %d", path, s)
			}
			cur = array[s]
		}
	}
	switch w := want.(type) {
	case string:
		if cur != w {
			t.Fatalf("%v = %v, want %q", path, cur, w)
		}
	case int:
		if cur != float64(w) {
			t.Fatalf("%v = %v, want %d", path, cur, w)
		}
	case float64:
		got, ok := cur.(float64)
		if !ok || math.Abs(got-w) > 1e-12*math.Max(1, math.Abs(w)) {
			t.Fatalf("%v = %v, want %v", path, cur, w)
		}
	default:
		t.Fatalf("%v: unsupported expectation %T", path, want)
	}
}
