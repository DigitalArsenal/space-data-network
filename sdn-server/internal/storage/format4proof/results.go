package format4proof

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// The arms, as benchset.json's protocol names them.
const (
	ArmF1 = "f1" // store format 1, as deployed
	ArmF2 = "f2" // store format 2 (store-migrate --to 2 of the same fixture)
	ArmS  = "s"  // store format 4 (store-migrate --to 4 of the same fixture)
)

// Arms is the order every table lists them in.
var Arms = []string{ArmS, ArmF1, ArmF2}

// Run kinds.
const (
	KindReads  = "reads"
	KindIngest = "ingest"
	KindM01    = "m01"
	KindWrites = "writes"
	KindBytes  = "bytes"
	KindCrash  = "crash"
	KindGrowth = "growth"
)

// Labels of the store a run measured.
const (
	LabelFixture = "fixture" // the host-02-sized fixture
	LabelGrown   = "grown"   // the fixture + 28% (ingest phases A and B)
)

// Sample is one timed call.
type Sample struct {
	Class string  `json:"class"` // benchset id ("R01", "W01") or a phase ("ingest_A")
	Shape string  `json:"shape"` // the parameter set, stable across arms
	Pass  int     `json:"pass"`  // 0 = cold (first call after open, fresh process, fresh clone); >0 warm
	Ms    float64 `json:"ms"`
	Rows  int64   `json:"rows"`
	Bytes int64   `json:"bytes,omitempty"`
	Err   string  `json:"err,omitempty"`
	AtS   float64 `json:"at_s,omitempty"` // seconds since the run started (ingest, M01)
}

// MemPoint is a resource snapshot of the measuring process.
type MemPoint struct {
	At         string  `json:"at"`
	ElapsedS   float64 `json:"elapsed_s"`
	RSSMB      float64 `json:"rss_mb"`
	MaxRSSMB   float64 `json:"maxrss_mb"`
	GoHeapMB   float64 `json:"go_heap_mb"`
	DiskWMB    float64 `json:"disk_written_mb,omitempty"`
	DiskRMB    float64 `json:"disk_read_mb,omitempty"`
	FDs        int     `json:"fds"`
	WALBytes   int64   `json:"wal_bytes,omitempty"`
	StoreBytes int64   `json:"store_bytes,omitempty"`
	Records    int64   `json:"records,omitempty"`
	Load       string  `json:"load"`
}

// Machine names where a run ran.
type Machine struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	CPUs int    `json:"cpus"`
}

// ThisMachine describes the running process's machine.
func ThisMachine() Machine {
	return Machine{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU()}
}

// Run is one measurement process: one arm, one kind, one store, usually one
// class. Every number in it is paired with the load at its start and end.
type Run struct {
	Kind      string         `json:"kind"`
	Arm       string         `json:"arm"`
	Format    string         `json:"format"` // the store format the arm opened
	Label     string         `json:"label"`  // LabelFixture, LabelGrown, a growth step id
	Class     string         `json:"class,omitempty"`
	Records   int64          `json:"records,omitempty"` // unique live records of the store (growth axis)
	Started   string         `json:"started"`
	Machine   Machine        `json:"machine"`
	LoadStart string         `json:"load_start"`
	LoadEnd   string         `json:"load_end"`
	OpenMs    float64        `json:"open_ms,omitempty"`
	Samples   []Sample       `json:"samples,omitempty"`
	Mem       []MemPoint     `json:"mem,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// FileName is where a run is written in the results directory.
func (r *Run) FileName() string {
	parts := []string{r.Kind, r.Arm, r.Label}
	if r.Class != "" {
		parts = append(parts, r.Class)
	}
	return safeName(strings.Join(parts, "-")) + ".json"
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '+':
			return r
		}
		return '_'
	}, s)
}

// WriteRun writes r into dir (created when missing) and returns the path.
func WriteRun(dir string, r *Run) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	r.Extra = finiteMap(r.Extra)
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return "", fmt.Errorf("format4proof: encode run %s: %w", r.FileName(), err)
	}
	p := filepath.Join(dir, r.FileName())
	return p, os.WriteFile(p, b, 0o644)
}

// ReadRuns loads every run in dir (files that are not runs are skipped),
// sorted by file name.
func ReadRuns(dir string) ([]*Run, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []*Run
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var r Run
		if json.Unmarshal(b, &r) != nil || r.Kind == "" || r.Arm == "" {
			continue
		}
		out = append(out, &r)
	}
	return out, nil
}

// finiteMap replaces NaN and ±Inf (which JSON cannot carry) by nil, recursively.
func finiteMap(m map[string]any) map[string]any {
	for k, v := range m {
		m[k] = finiteValue(v)
	}
	return m
}

func finiteValue(v any) any {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
	case map[string]any:
		return finiteMap(x)
	case []any:
		for i := range x {
			x[i] = finiteValue(x[i])
		}
	}
	return v
}

// ShapeStats is one (arm, class, shape) over its samples. An errored call
// counts as infinitely slow (benchset protocol), so one error makes the
// shape's percentiles +Inf.
type ShapeStats struct {
	Arm, Class, Shape string
	ColdN, WarmN      int
	ColdP50, ColdP99  float64
	ColdMax           float64
	WarmP50, WarmP99  float64
	WarmMax           float64
	Errors            int
	Rows              int64
}

// ShapeKey identifies a shape across arms.
type ShapeKey struct{ Class, Shape string }

// SummarizeShapes groups the samples of runs (all of one kind) by arm and shape.
func SummarizeShapes(runs []*Run) map[string]map[ShapeKey]*ShapeStats {
	type acc struct {
		cold, warm []float64
		errs       int
		rows       int64
		seenRows   bool
	}
	accs := map[string]map[ShapeKey]*acc{}
	for _, r := range runs {
		m := accs[r.Arm]
		if m == nil {
			m = map[ShapeKey]*acc{}
			accs[r.Arm] = m
		}
		for _, s := range r.Samples {
			k := ShapeKey{s.Class, s.Shape}
			a := m[k]
			if a == nil {
				a = &acc{}
				m[k] = a
			}
			ms := s.Ms
			if s.Err != "" {
				a.errs++
				ms = math.Inf(1)
			} else if !a.seenRows {
				a.rows, a.seenRows = s.Rows, true
			}
			if s.Pass == 0 {
				a.cold = append(a.cold, ms)
			} else {
				a.warm = append(a.warm, ms)
			}
		}
	}
	out := map[string]map[ShapeKey]*ShapeStats{}
	for arm, m := range accs {
		o := map[ShapeKey]*ShapeStats{}
		for k, a := range m {
			o[k] = &ShapeStats{Arm: arm, Class: k.Class, Shape: k.Shape,
				ColdN: len(a.cold), ColdP50: Percentile(a.cold, 0.5), ColdP99: Percentile(a.cold, 0.99), ColdMax: Percentile(a.cold, 1),
				WarmN: len(a.warm), WarmP50: Percentile(a.warm, 0.5), WarmP99: Percentile(a.warm, 0.99), WarmMax: Percentile(a.warm, 1),
				Errors: a.errs, Rows: a.rows}
		}
		out[arm] = o
	}
	return out
}

// RunsOf filters runs by kind and label ("" = any label).
func RunsOf(runs []*Run, kind, label string) []*Run {
	var out []*Run
	for _, r := range runs {
		if r.Kind == kind && (label == "" || r.Label == label) {
			out = append(out, r)
		}
	}
	return out
}

// writeJSON writes v indented.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
