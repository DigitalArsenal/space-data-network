package format4proof

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// The brief's gates (contract §7). A gate passes only when every check in it
// passes and nothing it needs is missing. A failed check is reported with its
// numbers; nothing here relaxes a bar to make a gate pass.

// Gate statuses.
const (
	StatusPass       = "pass"
	StatusFail       = "FAIL"
	StatusIncomplete = "incomplete" // no check failed, but something the gate needs was not measured
)

// Check is one comparison inside a gate. S must not exceed Bar (or, for
// Higher checks, must not fall below it).
type Check struct {
	Item   string  `json:"item"`
	S      float64 `json:"s"`
	F1     float64 `json:"f1"`
	F2     float64 `json:"f2"`
	Bar    float64 `json:"bar"`
	Unit   string  `json:"unit"`
	Higher bool    `json:"higher,omitempty"` // higher is better (rates)
	Info   bool    `json:"info,omitempty"`   // reported, not part of the gate
	Pass   bool    `json:"pass"`
	Note   string  `json:"note,omitempty"`
}

// MarshalJSON writes an unmeasured number (NaN) as null and an errored one
// (+Inf) as the string "error": JSON carries neither.
func (c Check) MarshalJSON() ([]byte, error) {
	type plain Check
	num := func(v float64) any {
		switch {
		case math.IsNaN(v):
			return nil
		case math.IsInf(v, 0):
			return "error"
		}
		return v
	}
	return json.Marshal(struct {
		plain
		S   any `json:"s"`
		F1  any `json:"f1"`
		F2  any `json:"f2"`
		Bar any `json:"bar"`
	}{plain(c), num(c.S), num(c.F1), num(c.F2), num(c.Bar)})
}

// Gate is one of the brief's gates with its checks.
type Gate struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Status  string   `json:"status"`
	Checks  []Check  `json:"checks"`
	Missing []string `json:"missing,omitempty"`
	Loads   []string `json:"loads,omitempty"` // the load averages of the runs it used
}

// Report is the gate report over one results directory.
type Report struct {
	Generated string `json:"generated"`
	Gates     []Gate `json:"gates"`
}

// finish sets the gate's status from its checks and missing list.
func (g *Gate) finish() {
	g.Status = StatusPass
	for _, c := range g.Checks {
		if !c.Info && !c.Pass {
			g.Status = StatusFail
			return
		}
	}
	if len(g.Missing) > 0 || len(g.Checks) == 0 {
		g.Status = StatusIncomplete
	}
}

func (g *Gate) noteLoads(runs []*Run) {
	seen := map[string]bool{}
	for _, r := range runs {
		for _, l := range []string{r.LoadStart, r.LoadEnd} {
			if l != "" && !seen[l] {
				seen[l] = true
				g.Loads = append(g.Loads, l)
			}
		}
	}
}

// lowerOrEqual reports s <= bar with +Inf for errors; NaN (not measured) never passes.
func lowerOrEqual(s, bar float64) bool {
	if math.IsNaN(s) || math.IsNaN(bar) {
		return false
	}
	return s <= bar
}

// minMeasured is the smallest of the measured (non-NaN) values, NaN for none.
func minMeasured(v ...float64) float64 {
	out := math.NaN()
	for _, x := range v {
		if math.IsNaN(x) {
			continue
		}
		if math.IsNaN(out) || x < out {
			out = x
		}
	}
	return out
}

func extraFloat(r *Run, key string) float64 {
	if r == nil || r.Extra == nil {
		return math.NaN()
	}
	switch v := r.Extra[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	}
	return math.NaN()
}

func runOf(runs []*Run, kind, arm, label string) *Run {
	for _, r := range runs {
		if r.Kind == kind && r.Arm == arm && (label == "" || r.Label == label) {
			return r
		}
	}
	return nil
}

// EvaluateGates computes the three gates from every run in a results directory.
func EvaluateGates(runs []*Run) Report {
	return Report{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Gates:     []Gate{slimmerGate(runs), readsGate(runs), ingestGate(runs), m01Gate(runs), degradeGate(runs)},
	}
}

// Gate 1: bytes on disk per unique record (and per stored copy) below both engines.
func slimmerGate(runs []*Run) Gate {
	g := Gate{ID: "1", Title: "Slimmer: bytes per record below both engines"}
	used := RunsOf(runs, KindBytes, LabelFixture)
	g.noteLoads(used)
	for _, key := range []struct{ extra, item string }{
		{"bytes_per_record", "bytes on disk per unique record"},
		{"bytes_per_copy", "bytes on disk per stored copy"},
	} {
		s := extraFloat(runOf(used, KindBytes, ArmS, LabelFixture), key.extra)
		f1 := extraFloat(runOf(used, KindBytes, ArmF1, LabelFixture), key.extra)
		f2 := extraFloat(runOf(used, KindBytes, ArmF2, LabelFixture), key.extra)
		if math.IsNaN(s) || math.IsNaN(f1) || math.IsNaN(f2) {
			g.Missing = append(g.Missing, fmt.Sprintf("%s: s %s, f1 %s, f2 %s", key.item, have(s), have(f1), have(f2)))
			continue
		}
		bar := math.Min(f1, f2)
		g.Checks = append(g.Checks, Check{Item: key.item, S: s, F1: f1, F2: f2, Bar: bar, Unit: "B", Pass: s < bar})
	}
	added := RunsOf(runs, KindBytes, LabelGrown)
	s := extraFloat(runOf(added, KindBytes, ArmS, LabelGrown), "bytes_per_added_record")
	f1 := extraFloat(runOf(added, KindBytes, ArmF1, LabelGrown), "bytes_per_added_record")
	f2 := extraFloat(runOf(added, KindBytes, ArmF2, LabelGrown), "bytes_per_added_record")
	if !math.IsNaN(s) {
		bar := minMeasured(f1, f2)
		g.Checks = append(g.Checks, Check{Item: "bytes on disk per added record (+28%)", S: s, F1: f1, F2: f2, Bar: bar,
			Unit: "B", Pass: !math.IsNaN(bar) && s < bar})
	}
	g.finish()
	return g
}

func have(v float64) string {
	if math.IsNaN(v) {
		return "missing"
	}
	return "measured"
}

// readMetrics are the four numbers every read shape is held to.
var readMetrics = []struct {
	name string
	get  func(*ShapeStats) float64
}{
	{"cold p50", func(s *ShapeStats) float64 { return s.ColdP50 }},
	{"cold p99", func(s *ShapeStats) float64 { return s.ColdP99 }},
	{"warm p50", func(s *ShapeStats) float64 { return s.WarmP50 }},
	{"warm p99", func(s *ShapeStats) float64 { return s.WarmP99 }},
}

// Gate 2a: every read shape at least equal to both engines, at p50 and p99,
// cold and warm. The bar is the better of F1 and F2 for that number (an arm
// that errors is infinitely slow; an arm that did not run the shape is left
// out of the bar and named in the check's note). The EPOCH shapes over every
// object (AllObjects; not in the benchset) are reported beside the bar, as
// the owner asked (p50 and p99), not held to it.
func readsGate(runs []*Run) Gate {
	g := Gate{ID: "2a", Title: "Faster: every read shape at least equal to both engines (p50 and p99, cold and warm)"}
	used := RunsOf(runs, KindReads, LabelFixture)
	g.noteLoads(used)
	st := SummarizeShapes(used)
	s, f1, f2 := st[ArmS], st[ArmF1], st[ArmF2]
	if len(s) == 0 {
		g.Missing = append(g.Missing, "arm s has no read runs")
	}
	if len(f1) == 0 && len(f2) == 0 {
		g.Missing = append(g.Missing, "no baseline read runs (f1, f2)")
	}
	keys := map[ShapeKey]bool{}
	for _, m := range []map[ShapeKey]*ShapeStats{s, f1, f2} {
		for k := range m {
			keys[k] = true
		}
	}
	for _, k := range sortedKeys(keys) {
		ss, a, b := s[k], f1[k], f2[k]
		if a == nil && b == nil {
			continue // no baseline answers this shape; reported in the shape table
		}
		for _, m := range readMetrics {
			c := Check{Item: k.Class + " " + k.Shape + " " + m.name, Unit: "ms", S: math.NaN(), F1: math.NaN(), F2: math.NaN()}
			if a != nil {
				c.F1 = m.get(a)
			}
			if b != nil {
				c.F2 = m.get(b)
			}
			c.Bar = minMeasured(c.F1, c.F2)
			var notes []string
			if a == nil {
				notes = append(notes, "f1 not run")
			}
			if b == nil {
				notes = append(notes, "f2 not run")
			}
			if ss == nil {
				notes = append(notes, "s did not run it")
			} else {
				c.S = m.get(ss)
				if ss.Errors > 0 {
					notes = append(notes, fmt.Sprintf("s errors %d", ss.Errors))
				}
				if a != nil && a.Rows != ss.Rows {
					notes = append(notes, fmt.Sprintf("rows s %d, f1 %d", ss.Rows, a.Rows)) // C-31 shapes answer more
				}
			}
			c.Info = strings.HasSuffix(k.Shape, " "+AllObjects)
			c.Pass = lowerOrEqual(c.S, c.Bar)
			c.Note = strings.Join(notes, "; ")
			g.Checks = append(g.Checks, c)
		}
	}
	g.finish()
	return g
}

func sortedKeys(m map[ShapeKey]bool) []ShapeKey {
	out := make([]ShapeKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Shape < out[j].Shape
	})
	return out
}

// Ingest phases (ingest runs' sample classes).
const (
	PhaseOneWriter   = "ingest_A" // one writer, OMM, 4,096-record calls
	PhaseFourWriters = "ingest_B" // four writers, one per type
	PhaseSameType    = "ingest_C" // several producers of one type at once
)

var phaseTitles = map[string]string{
	PhaseOneWriter:   "one writer",
	PhaseFourWriters: "four writers (one per type)",
	PhaseSameType:    "same-type producers",
}

// IngestPhase is one phase's numbers inside an ingest run.
type IngestPhase struct {
	Records    int64   `json:"records"`
	FrameBytes int64   `json:"frame_bytes"`
	Seconds    float64 `json:"seconds"`
	RecPerS    float64 `json:"rec_per_s"`
	DiskWMB    float64 `json:"disk_written_mb"`
	WriteAmp   float64 `json:"write_amplification"`
}

// mergedRun joins an arm's runs (ingest A+B and C run separately) into one:
// their samples, and their Extra keys (first run wins). nil when none.
func mergedRun(runs []*Run, arm string) *Run {
	var out *Run
	for _, r := range runs {
		if r.Arm != arm {
			continue
		}
		if out == nil {
			c := *r
			c.Samples = append([]Sample(nil), r.Samples...)
			c.Extra = map[string]any{}
			for k, v := range r.Extra {
				c.Extra[k] = v
			}
			out = &c
			continue
		}
		out.Samples = append(out.Samples, r.Samples...)
		for k, v := range r.Extra {
			if _, ok := out.Extra[k]; !ok {
				out.Extra[k] = v
			}
		}
	}
	return out
}

func phaseP99(r *Run, phase string) float64 {
	if r == nil {
		return math.NaN()
	}
	var v []float64
	for _, s := range r.Samples {
		if s.Class != phase {
			continue
		}
		if s.Err != "" {
			v = append(v, math.Inf(1))
			continue
		}
		v = append(v, s.Ms)
	}
	return Percentile(v, 0.99)
}

func phaseRate(r *Run, phase string) float64 {
	if r == nil || r.Extra == nil {
		return math.NaN()
	}
	m, ok := r.Extra[phase].(map[string]any)
	if !ok {
		return math.NaN()
	}
	if v, ok := m["rec_per_s"].(float64); ok {
		return v
	}
	return math.NaN()
}

// Gate 2b: ingest call p99 at most format 2's, one writer, four writers and
// same-type producers. Rates are reported beside it.
func ingestGate(runs []*Run) Gate {
	g := Gate{ID: "2b", Title: "Faster: ingest call p99 at most format 2's (rates reported)"}
	used := RunsOf(runs, KindIngest, LabelFixture)
	g.noteLoads(used)
	rs, r1, r2 := mergedRun(used, ArmS), mergedRun(used, ArmF1), mergedRun(used, ArmF2)
	for _, ph := range []string{PhaseOneWriter, PhaseFourWriters, PhaseSameType} {
		s, f1, f2 := phaseP99(rs, ph), phaseP99(r1, ph), phaseP99(r2, ph)
		if math.IsNaN(s) || math.IsNaN(f2) {
			g.Missing = append(g.Missing, fmt.Sprintf("%s call p99: s %s, f2 %s", phaseTitles[ph], have(s), have(f2)))
		} else {
			g.Checks = append(g.Checks, Check{Item: phaseTitles[ph] + ": call p99", S: s, F1: f1, F2: f2, Bar: f2, Unit: "ms",
				Pass: lowerOrEqual(s, f2)})
		}
		rS, rF1, rF2 := phaseRate(rs, ph), phaseRate(r1, ph), phaseRate(r2, ph)
		if !math.IsNaN(rS) {
			bar := math.NaN()
			for _, v := range []float64{rF1, rF2} {
				if !math.IsNaN(v) && (math.IsNaN(bar) || v > bar) {
					bar = v
				}
			}
			g.Checks = append(g.Checks, Check{Item: phaseTitles[ph] + ": records/s", S: rS, F1: rF1, F2: rF2, Bar: bar,
				Unit: "rec/s", Higher: true, Info: true, Pass: !math.IsNaN(bar) && rS >= bar})
		}
	}
	g.finish()
	return g
}

// M01 limits (benchset M01; contract §6 host G4).
const (
	M01BlockedMs       = 50.0
	M01WALBoundedBytes = 1 << 30 // the engine's instance WAL total (config tag 32 default)
)

// Gate 2c: reads never wait (M01): no read over 50 ms during the writes, no
// error, the WAL bounded. Baselines are reported beside it.
func m01Gate(runs []*Run) Gate {
	g := Gate{ID: "2c", Title: "Faster: reads never wait during writes (M01), WAL bounded"}
	used := RunsOf(runs, KindM01, "")
	g.noteLoads(used)
	rs := runOf(used, KindM01, ArmS, "")
	r1, r2 := runOf(used, KindM01, ArmF1, ""), runOf(used, KindM01, ArmF2, "")
	if rs == nil {
		g.Missing = append(g.Missing, "arm s has no M01 run")
		g.finish()
		return g
	}
	maxBusy := func(r *Run) float64 {
		if r == nil {
			return math.NaN()
		}
		return extraFloat(r, "busy_read_max_ms")
	}
	errs := func(r *Run) float64 {
		if r == nil {
			return math.NaN()
		}
		return extraFloat(r, "read_errors")
	}
	wal := func(r *Run) float64 {
		if r == nil {
			return math.NaN()
		}
		return extraFloat(r, "wal_max_bytes")
	}
	g.Checks = append(g.Checks,
		Check{Item: "max read during writes", S: maxBusy(rs), F1: maxBusy(r1), F2: maxBusy(r2), Bar: M01BlockedMs, Unit: "ms",
			Pass: lowerOrEqual(maxBusy(rs), M01BlockedMs)},
		Check{Item: "read errors", S: errs(rs), F1: errs(r1), F2: errs(r2), Bar: 0, Unit: "",
			Pass: lowerOrEqual(errs(rs), 0)},
		Check{Item: "max WAL bytes", S: wal(rs), F1: wal(r1), F2: wal(r2), Bar: M01WALBoundedBytes, Unit: "B",
			Pass: lowerOrEqual(wal(rs), M01WALBoundedBytes)},
	)
	g.finish()
	return g
}

// degradeFloorMs drops shapes whose fixture time is under 20 µs from the
// slope comparison (timer resolution; the verify step's rule).
const degradeFloorMs = 0.02

// Gate 3: every metric's slope as the store grows flatter than both engines.
// Per class and metric: the median over shapes of the per-doubling factor of
// the +28% step, and the class p99's factor; plus every count-scaled growth
// probe's per-doubling factor.
func degradeGate(runs []*Run) Gate {
	g := Gate{ID: "3", Title: "Degrades slower: every slope flatter than both engines (+28% step and count-scaled growth)"}
	fix, grown := RunsOf(runs, KindReads, LabelFixture), RunsOf(runs, KindReads, LabelGrown)
	g.noteLoads(append(append([]*Run{}, fix...), grown...))
	slopes := map[string]map[string]map[string]float64{} // arm -> class -> metric -> factor
	for _, arm := range Arms {
		sf, sg := filterArm(fix, arm), filterArm(grown, arm)
		if len(sf) == 0 || len(sg) == 0 {
			continue
		}
		n0, n1 := recordsOf(sf), recordsOf(sg)
		if n0 <= 0 || n1 <= n0 {
			g.Missing = append(g.Missing, fmt.Sprintf("%s: record counts of the +28%% pair unknown (%d -> %d)", arm, n0, n1))
			continue
		}
		slopes[arm] = classSlopes(sf, sg, arm, n0, n1)
	}
	if slopes[ArmS] == nil {
		g.Missing = append(g.Missing, "arm s has no fixture and grown read runs")
	}
	if slopes[ArmF2] == nil {
		g.Missing = append(g.Missing, "arm f2 has no fixture and grown read runs")
	}
	if slopes[ArmS] != nil {
		var classes []string
		for c := range slopes[ArmS] {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		for _, c := range classes {
			var metrics []string
			for m := range slopes[ArmS][c] {
				metrics = append(metrics, m)
			}
			sort.Strings(metrics)
			for _, m := range metrics {
				s := slopes[ArmS][c][m]
				f1, f2 := lookupSlope(slopes, ArmF1, c, m), lookupSlope(slopes, ArmF2, c, m)
				bar := minMeasured(f1, f2)
				ck := Check{Item: "+28% " + c + " " + m + " per doubling", S: s, F1: f1, F2: f2, Bar: bar, Unit: "x",
					Pass: !math.IsNaN(bar) && s < bar}
				if math.IsNaN(f1) {
					ck.Note = "f1 not run at both sizes"
				}
				if math.IsNaN(bar) {
					ck.Note = "no baseline slope"
				}
				g.Checks = append(g.Checks, ck)
			}
		}
	}
	growthChecks(&g, RunsOf(runs, KindGrowth, ""))
	g.finish()
	return g
}

func filterArm(runs []*Run, arm string) []*Run {
	var out []*Run
	for _, r := range runs {
		if r.Arm == arm {
			out = append(out, r)
		}
	}
	return out
}

// recordsOf is the store size the runs measured (the largest count any of
// them recorded: every run of a label measured the same store).
func recordsOf(runs []*Run) int64 {
	var n int64
	for _, r := range runs {
		if r.Records > n {
			n = r.Records
		}
	}
	return n
}

func lookupSlope(m map[string]map[string]map[string]float64, arm, class, metric string) float64 {
	if m[arm] == nil || m[arm][class] == nil {
		return math.NaN()
	}
	v, ok := m[arm][class][metric]
	if !ok {
		return math.NaN()
	}
	return v
}

// classSlopes: per class, for each read metric, the median per-doubling
// factor over shapes ("<metric> shape median") and the factor of the p99 over
// every sample of the class, cold and warm ("class cold p99").
func classSlopes(fix, grown []*Run, arm string, n0, n1 int64) map[string]map[string]float64 {
	a, b := SummarizeShapes(fix)[arm], SummarizeShapes(grown)[arm]
	doublings := math.Log2(float64(n1) / float64(n0))
	out := map[string]map[string]float64{}
	per := map[string]map[string][]float64{}
	for k, s0 := range a {
		s1 := b[k]
		if s1 == nil {
			continue
		}
		for _, m := range readMetrics {
			v0, v1 := m.get(s0), m.get(s1)
			if math.IsNaN(v0) || math.IsNaN(v1) || math.IsInf(v0, 0) || v0 < degradeFloorMs {
				continue
			}
			if per[k.Class] == nil {
				per[k.Class] = map[string][]float64{}
			}
			per[k.Class][m.name] = append(per[k.Class][m.name], math.Pow(v1/v0, 1/doublings))
		}
	}
	for c, ms := range per {
		out[c] = map[string]float64{}
		for m, v := range ms {
			out[c][m+" shape median"] = Median(v)
		}
	}
	pool := func(runs []*Run, class string, cold bool) float64 {
		var v []float64
		for _, r := range runs {
			for _, s := range r.Samples {
				if s.Class != class || (s.Pass == 0) != cold {
					continue
				}
				if s.Err != "" {
					v = append(v, math.Inf(1))
				} else {
					v = append(v, s.Ms)
				}
			}
		}
		return Percentile(v, 0.99)
	}
	for c := range out {
		for _, cold := range []bool{true, false} {
			v0, v1 := pool(fix, c, cold), pool(grown, c, cold)
			if math.IsNaN(v0) || math.IsNaN(v1) || v0 < degradeFloorMs || math.IsInf(v0, 0) {
				continue
			}
			name := "class warm p99"
			if cold {
				name = "class cold p99"
			}
			out[c][name] = math.Pow(v1/v0, 1/doublings)
		}
	}
	return out
}

// growthChecks: the count-scaled growth tier (KindGrowth runs, one per arm
// and step, Records = the store's records at the step). Every probe shape's
// p50 and p99 per-doubling factor over the steps an arm ran, s against each
// baseline that ran at least two steps.
func growthChecks(g *Gate, runs []*Run) {
	if len(runs) == 0 {
		g.Missing = append(g.Missing, "count-scaled growth step (sds-tb-gen) not run")
		return
	}
	type key struct{ shape, metric string }
	points := map[string]map[key][]Point{}
	for _, r := range runs {
		if r.Records <= 0 {
			continue
		}
		byShape := map[string][]float64{}
		for _, s := range r.Samples {
			if s.Err != "" {
				byShape[s.Class+" "+s.Shape] = append(byShape[s.Class+" "+s.Shape], math.Inf(1))
				continue
			}
			byShape[s.Class+" "+s.Shape] = append(byShape[s.Class+" "+s.Shape], s.Ms)
		}
		if points[r.Arm] == nil {
			points[r.Arm] = map[key][]Point{}
		}
		for shape, v := range byShape {
			for _, q := range []struct {
				name string
				q    float64
			}{{"p50", 0.5}, {"p99", 0.99}} {
				k := key{shape, q.name}
				points[r.Arm][k] = append(points[r.Arm][k], Point{Records: float64(r.Records), Value: Percentile(v, q.q)})
			}
		}
	}
	if points[ArmS] == nil {
		g.Missing = append(g.Missing, "count-scaled growth: arm s not run")
		return
	}
	var keys []key
	for k := range points[ArmS] {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].shape != keys[j].shape {
			return keys[i].shape < keys[j].shape
		}
		return keys[i].metric < keys[j].metric
	})
	for _, k := range keys {
		s, ok := PerDoubling(points[ArmS][k])
		if !ok {
			continue
		}
		f1, ok1 := PerDoubling(points[ArmF1][k])
		f2, ok2 := PerDoubling(points[ArmF2][k])
		if !ok1 {
			f1 = math.NaN()
		}
		if !ok2 {
			f2 = math.NaN()
		}
		bar := minMeasured(f1, f2)
		ck := Check{Item: "growth " + k.shape + " " + k.metric + " per doubling", S: s, F1: f1, F2: f2, Bar: bar, Unit: "x",
			Pass: !math.IsNaN(bar) && s < bar}
		if math.IsNaN(bar) {
			ck.Note = "no baseline ran two growth steps"
		}
		g.Checks = append(g.Checks, ck)
	}
}
