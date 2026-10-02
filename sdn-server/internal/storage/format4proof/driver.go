package format4proof

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// The drivers: the parent side of every measurement. Each runs children
// back to back per class (s, f1, f2 by default), each child on its own fresh
// clone, and removes the clone after.

// Logf is the drivers' progress log.
type Logf func(format string, args ...any)

func (c Config) workPath(parts ...string) string {
	return filepath.Join(append([]string{c.Work}, parts...)...)
}

// GrownStore is where the ingest driver leaves an arm's +28% store.
func (c Config) GrownStore(arm string) string { return c.workPath("grown-" + arm) }

// SourceStore is the store an arm's label measures (never opened directly:
// it is cloned per run).
func (c Config) SourceStore(arm, label string) string {
	switch label {
	case LabelFixture:
		return c.Fixtures[arm]
	case LabelGrown:
		p := c.GrownStore(arm)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	case FixtureH2Copy:
		return c.H2[arm]
	}
	return ""
}

// withClone clones src into the work directory, runs fn on the clone, and
// removes it.
func (c Config) withClone(src, name string, fn func(clone string) error) error {
	if c.IsFixturePath(c.workPath(name)) {
		return fmt.Errorf("the work directory lies inside a fixture")
	}
	clone := c.workPath(name)
	_ = os.RemoveAll(clone)
	if err := CloneStore(src, clone); err != nil {
		return err
	}
	defer os.RemoveAll(clone)
	return fn(clone)
}

// shapeFilter compiles EnvShape (nil when unset).
func shapeFilter(expr string) (*regexp.Regexp, error) {
	if expr == "" {
		return nil, nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvShape, err)
	}
	return re, nil
}

func (c Config) logPath(name string) string {
	return filepath.Join(c.Out, "logs", safeName(name)+".log")
}

// PrepareInputs extracts the inputs from a clone of the format-1 fixture
// (once; an existing inputs.json is kept).
func PrepareInputs(ctx context.Context, c Config, logf Logf) error {
	if _, err := os.Stat(filepath.Join(InputsDir(c.Work), "inputs.json")); err == nil {
		return nil
	}
	src := c.Fixtures[ArmF1]
	if src == "" {
		return fmt.Errorf("%s is required to prepare the inputs", EnvF1Fixture)
	}
	return c.withClone(src, "prepare-f1", func(clone string) error {
		cr, err := RunChild(ctx, ChildSpec{Mode: ModePrepare, Prepare: &PrepareSpec{Store: clone, Work: c.Work}}, c.logPath("prepare"))
		logf("prepare: %s, max RSS %.0f MB", cr.Wall.Round(time.Second), cr.MaxRSSMB)
		return err
	})
}

// DriveReads measures every class (or c.Classes) of a label on every arm
// that has a store for it, back to back per class.
func DriveReads(ctx context.Context, c Config, label string, logf Logf) error {
	bs, err := LoadBenchset(c.Benchset)
	if err != nil {
		return err
	}
	in, _, err := LoadInputs(c.Work)
	if err != nil {
		return err
	}
	shapes, err := BuildShapes(bs, in)
	if err != nil {
		return err
	}
	fixture := FixtureT6W
	if label == FixtureH2Copy {
		fixture = FixtureH2Copy
	}
	only, err := shapeFilter(c.Shape)
	if err != nil {
		return err
	}
	classes := c.Classes
	if len(classes) == 0 {
		classes = ClassesOf(shapes)
	}
	var have []string
	for _, cl := range classes {
		if len(ShapesOf(shapes, cl, fixture, only)) > 0 {
			have = append(have, cl)
		}
	}
	if len(have) == 0 {
		return fmt.Errorf("no %s shapes in classes %v match %s %q", fixture, classes, EnvShape, c.Shape)
	}
	classes = have
	var firstErr error
	rounds := c.ColdRounds
	if rounds < 1 {
		rounds = 1
	}
	for _, class := range classes {
		for round := 1; round <= rounds; round++ {
			for _, arm := range c.Arms {
				src := c.SourceStore(arm, label)
				if src == "" {
					logf("reads %s %s %s: no store, skipped", label, arm, class)
					continue
				}
				name := fmt.Sprintf("reads-%s-%s-%s-r%d", label, arm, class, round)
				err := c.withClone(src, name, func(clone string) error {
					spec := ReadSpec{Arm: arm, Label: label, Class: class, Store: clone, Out: c.Out, Warm: c.Warm,
						CallLimit: time.Duration(c.CallLimit) * time.Second, Round: round, Shape: c.Shape}
					if round > 1 {
						spec.Warm = 0 // further rounds add cold samples only
					}
					cr, err := RunChild(ctx, ChildSpec{Mode: ModeReads, Benchset: c.Benchset, Work: c.Work, Read: &spec}, c.logPath(name))
					logf("reads %s %s %s round %d: %s, max RSS %.0f MB, load %s, err %v", label, arm, class, round, cr.Wall.Round(time.Second),
						cr.MaxRSSMB, Load(), err)
					return err
				})
				if err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// IngestPlan is the ingest phases: A and B on one clone (left as the grown
// store), C on another.
type IngestPlan struct{ A, B, C, CWriters, Batch int }

// DefaultIngestPlan is the earlier baselines' +28% (A=100, B=50 per writer,
// 4,096 records a call: 1,228,800 records) plus 8 same-type producers × 12
// calls.
var DefaultIngestPlan = IngestPlan{A: 100, B: 50, C: 12, CWriters: 8, Batch: 4096}

// ParseIngestPlan reads "A=100;B=50;C=12;W=8;batch=4096" over the default.
func ParseIngestPlan(s string) IngestPlan {
	p := DefaultIngestPlan
	for _, kv := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		switch k {
		case "A":
			p.A = n
		case "B":
			p.B = n
		case "C":
			p.C = n
		case "W":
			p.CWriters = n
		case "batch":
			p.Batch = n
		}
	}
	return p
}

// DriveIngest runs the phases per arm: A+B on a clone kept as the arm's grown
// store (bytes per added record measured on it), C on a throwaway clone.
func DriveIngest(ctx context.Context, c Config, plan IngestPlan, logf Logf) error {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, arm := range c.Arms {
		src := c.Fixtures[arm]
		if src == "" {
			logf("ingest %s: no fixture, skipped", arm)
			continue
		}
		grown := c.GrownStore(arm)
		_ = os.RemoveAll(grown)
		if err := CloneStore(src, grown); err != nil {
			return err
		}
		spec := IngestSpec{Arm: arm, Label: LabelFixture, Store: grown, Out: c.Out, Work: c.Work, A: plan.A, B: plan.B, Batch: plan.Batch}
		cr, err := RunChild(ctx, ChildSpec{Mode: ModeIngest, Ingest: &spec}, c.logPath("ingest-AB-"+arm))
		logf("ingest A+B %s: %s, max RSS %.0f MB, err %v", arm, cr.Wall.Round(time.Second), cr.MaxRSSMB, err)
		note(err)
		if err == nil {
			note(writeGrownBytes(c, arm, src, grown))
		}
		if plan.C > 0 && plan.CWriters > 0 {
			name := "ingest-C-" + arm
			note(c.withClone(src, name, func(clone string) error {
				spec := IngestSpec{Arm: arm, Label: LabelFixture, Store: clone, Out: c.Out, Work: c.Work, C: plan.C,
					CWriters: plan.CWriters, Batch: plan.Batch}
				cr, err := RunChild(ctx, ChildSpec{Mode: ModeIngest, Ingest: &spec}, c.logPath(name))
				logf("ingest C %s: %s, max RSS %.0f MB, err %v", arm, cr.Wall.Round(time.Second), cr.MaxRSSMB, err)
				return err
			}))
		}
	}
	return firstErr
}

// The kept pre-migration copies, which serve no read and are not counted.
var notStoreBytes = []string{"pre-format2", "pre-format4"}

// writeGrownBytes records the grown store's bytes and its bytes per added
// record (the A+B run's inserted records).
func writeGrownBytes(c Config, arm, fixture, grown string) error {
	t0, err := DiskTree(fixture, notStoreBytes...)
	if err != nil {
		return err
	}
	t1, err := DiskTree(grown, notStoreBytes...)
	if err != nil {
		return err
	}
	runs, err := ReadRuns(c.Out)
	if err != nil {
		return err
	}
	var added float64
	for _, r := range runs {
		if r.Kind == KindIngest && r.Arm == arm && r.Label == LabelFixture {
			if v, ok := r.Extra["total_records"].(float64); ok && r.Class == "AB" {
				added = v
			}
		}
	}
	r := &Run{Kind: KindBytes, Arm: arm, Format: ArmFormat(arm), Label: LabelGrown, Started: time.Now().UTC().Format(time.RFC3339),
		Machine: ThisMachine(), LoadStart: Load(), LoadEnd: Load(), Extra: map[string]any{
			"store_bytes": float64(t1.Apparent), "allocated_bytes": float64(t1.Allocated), "files": float64(t1.Files),
			"added_records": added}}
	if added > 0 {
		r.Extra["bytes_per_added_record"] = float64(t1.Apparent-t0.Apparent) / added
	}
	_, err = WriteRun(c.Out, r)
	return err
}

// DriveBytes records every arm's fixture bytes on disk (a stat walk of the
// fixture; nothing is opened) per unique record and per stored copy.
func DriveBytes(c Config, uniqueRecords, copies int64) error {
	for _, arm := range c.Arms {
		src := c.Fixtures[arm]
		if src == "" {
			continue
		}
		t, err := DiskTree(src, notStoreBytes...)
		if err != nil {
			return err
		}
		r := &Run{Kind: KindBytes, Arm: arm, Format: ArmFormat(arm), Label: LabelFixture, Records: uniqueRecords,
			Started: time.Now().UTC().Format(time.RFC3339), Machine: ThisMachine(), LoadStart: Load(), LoadEnd: Load(),
			Extra: map[string]any{"store_bytes": float64(t.Apparent), "allocated_bytes": float64(t.Allocated), "files": float64(t.Files),
				"wal_bytes": float64(t.WAL), "unique_records": float64(uniqueRecords), "copies": float64(copies),
				"bytes_per_record": float64(t.Apparent) / float64(uniqueRecords), "bytes_per_copy": float64(t.Apparent) / float64(copies)}}
		if _, err := WriteRun(c.Out, r); err != nil {
			return err
		}
	}
	return nil
}

// DriveM01 runs M01 per arm on a fresh clone.
func DriveM01(ctx context.Context, c Config, idleSeconds, minutes int, logf Logf) error {
	var firstErr error
	for _, arm := range c.Arms {
		src := c.Fixtures[arm]
		if src == "" {
			continue
		}
		name := "m01-" + arm
		err := c.withClone(src, name, func(clone string) error {
			spec := M01Spec{Arm: arm, Store: clone, Out: c.Out, Work: c.Work, IdleSeconds: idleSeconds, Minutes: minutes}
			cr, err := RunChild(ctx, ChildSpec{Mode: ModeM01, Benchset: c.Benchset, M01: &spec}, c.logPath(name))
			logf("m01 %s: %s, max RSS %.0f MB, err %v", arm, cr.Wall.Round(time.Second), cr.MaxRSSMB, err)
			return err
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DriveWrites runs W01–W10 (or ops) per arm, each on a fresh clone.
func DriveWrites(ctx context.Context, c Config, ops []string, logf Logf) error {
	if len(ops) == 0 {
		ops = WriteOps
	}
	var firstErr error
	for _, op := range ops {
		for _, arm := range c.Arms {
			src := c.Fixtures[arm]
			if src == "" {
				continue
			}
			name := "write-" + op + "-" + arm
			err := c.withClone(src, name, func(clone string) error {
				spec := WriteSpec{Arm: arm, Op: op, Store: clone, Out: c.Out, Work: c.Work}
				run := func() error {
					cr, err := RunChild(ctx, ChildSpec{Mode: ModeWrite, Benchset: c.Benchset, Write: &spec}, c.logPath(name))
					logf("write %s %s: %s, max RSS %.0f MB, err %v", op, arm, cr.Wall.Round(time.Second), cr.MaxRSSMB, err)
					return err
				}
				var second string // W07's import target; W10's untouched store (format 4's arrival check)
				switch {
				case op == "W07":
					second = name + "-import"
				case op == "W10" && arm == ArmS:
					second = name + "-before"
				default:
					return run()
				}
				return c.withClone(src, second, func(clone2 string) error {
					spec.Store2 = clone2
					return run()
				})
			})
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// --- equivalence ---------------------------------------------------------

// Format1CopyOracle returns format 1's copy variants of a CID (peer, source
// timestamp, signature of each stored copy), read from a format-1 store
// clone through its own engine.
func Format1CopyOracle(src *storage.MigrationSource) (CopyOracle, error) {
	tables, err := src.ProducerTables()
	if err != nil {
		return nil, err
	}
	return func(schema, cid string) ([]Row, error) {
		var out []Row
		for _, t := range tables {
			if t.Schema != schema {
				continue
			}
			recs, err := src.RecordsByCID(t, []string{cid})
			if err != nil {
				return nil, err
			}
			if r, ok := recs[cid]; ok {
				var sig []byte
				if r.SignatureHex != "" {
					sig, _ = hex.DecodeString(r.SignatureHex)
				}
				out = append(out, Row{{"~peer", r.PeerID}, {"~ts", unixOf(time.Unix(r.Timestamp, 0))}, {"~sig", digest(sig)}})
			}
		}
		return out, nil
	}, nil
}

// EquivalenceReport is every compared shape and write.
type EquivalenceReport struct {
	Generated string         `json:"generated"`
	Label     string         `json:"label"`
	Candidate string         `json:"candidate"` // the arm compared with format 1
	Reads     []Verdict      `json:"reads"`
	Writes    []WriteVerdict `json:"writes"`
	Missing   []string       `json:"missing,omitempty"`
}

// WriteVerdict compares one write's record sets (format 1 and format 4).
type WriteVerdict struct {
	Op     string   `json:"op"`
	Status string   `json:"status"`
	Diffs  []string `json:"diffs,omitempty"`
}

// DriveEquivalence compares a candidate arm's answers (format 4, ArmS; or
// format 2, which checks the harness against a known engine) with format
// 1's for every class answered by both, and the write runs' digests; format
// 1's copies resolve C-12 differences.
func DriveEquivalence(c Config, label, candidate string, logf Logf) (*EquivalenceReport, error) {
	rep := &EquivalenceReport{Generated: time.Now().UTC().Format(time.RFC3339), Label: label, Candidate: candidate}
	paths, err := filepath.Glob(filepath.Join(c.Out, "answers-"+safeName(ArmF1+"-"+label)+"-*.json.gz"))
	if err != nil {
		return nil, err
	}
	var oracle CopyOracle
	if src := c.Fixtures[ArmF1]; src != "" && label == LabelFixture {
		clone := c.workPath("oracle-f1")
		_ = os.RemoveAll(clone)
		if err := CloneStore(src, clone); err != nil {
			return nil, err
		}
		defer os.RemoveAll(clone)
		ms, err := storage.OpenMigrationSource(clone)
		if err != nil {
			return nil, err
		}
		defer ms.Close()
		if oracle, err = Format1CopyOracle(ms); err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		class := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "answers-"+ArmF1+"-"+label+"-"), ".json.gz")
		f1, err := ReadAnswers(c.Out, ArmF1, label, class)
		if err != nil {
			return nil, err
		}
		s, err := ReadAnswers(c.Out, candidate, label, class)
		if err != nil {
			return nil, err
		}
		if s == nil {
			rep.Missing = append(rep.Missing, candidate+" has no answers for "+class)
			continue
		}
		byShape := map[string]*ShapeAnswers{}
		for i := range s.Shapes {
			byShape[s.Shapes[i].Shape] = &s.Shapes[i]
		}
		for i := range f1.Shapes {
			name := f1.Shapes[i].Shape
			if rel, ok := strings.CutSuffix(name, SameQuestionSuffix); ok {
				if candidate != ArmS {
					continue // format 2 answers the relation as format 1 does (C-17)
				}
				// Format 1 answering the relation's own question: format 4's
				// relation must equal it exactly (C-31).
				name = rel
			}
			v := CompareShape(&f1.Shapes[i], byShape[name], oracle)
			rep.Reads = append(rep.Reads, v)
		}
		logf("equivalence %s: %d shapes compared", class, len(f1.Shapes))
	}
	runs, err := ReadRuns(c.Out)
	if err != nil {
		return nil, err
	}
	for _, op := range WriteOps {
		f1, s := writeRun(runs, ArmF1, op), writeRun(runs, candidate, op)
		if f1 == nil || s == nil {
			if f1 != nil || s != nil {
				rep.Missing = append(rep.Missing, op+": run on one arm only")
			}
			continue
		}
		rep.Writes = append(rep.Writes, compareWriteDigests(op, f1, s))
	}
	return rep, nil
}

func writeRun(runs []*Run, arm, op string) *Run {
	for _, r := range runs {
		if r.Kind == KindWrites && r.Arm == arm && r.Class == op {
			return r
		}
	}
	return nil
}

func digestsOf(r *Run) (map[string]TypeDigest, error) {
	v, ok := r.Extra["digest"]
	if !ok {
		return nil, fmt.Errorf("no digest (%v)", r.Extra["digest_error"])
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var d map[string]TypeDigest
	return d, json.Unmarshal(b, &d)
}

// w10Accepted is W10's intended difference: format 4's record sets after
// the quota GC differ from format 1's by design; format 4's own rule is
// checked instead (QuotaByArrival, the run's arrival_violations).
const w10Accepted = "C-32: format 4 deletes each type's oldest records by arrival; format 1 evicts by source timestamp across types"

func compareWriteDigests(op string, f1, s *Run) WriteVerdict {
	v := WriteVerdict{Op: op, Status: EqEqual}
	a, errA := digestsOf(f1)
	b, errB := digestsOf(s)
	if errA != nil || errB != nil {
		v.Status = EqMissing
		v.Diffs = append(v.Diffs, fmt.Sprintf("f1: %v; s: %v", errA, errB))
		return v
	}
	for _, schema := range touched[op] {
		for _, d := range DigestDiff(a[schema], b[schema]) {
			v.Diffs = append(v.Diffs, schema+" "+d)
		}
	}
	if len(v.Diffs) > 0 {
		v.Status = EqDiffer
	}
	if op == "W10" && s.Arm == ArmS {
		probs, ok := s.Extra["arrival_violations"].([]any)
		switch {
		case !ok:
			v.Status = EqMissing
			v.Diffs = append(v.Diffs, "format 4's arrival check did not run")
		case len(probs) > 0:
			v.Status = EqDiffer
			for _, p := range probs {
				v.Diffs = append(v.Diffs, fmt.Sprint(p))
			}
		case len(v.Diffs) > 0:
			v.Status = EqAccepted + " (" + w10Accepted + "; arrival order checked)"
		}
	}
	return v
}

// EquivalenceMarkdown renders the report.
func EquivalenceMarkdown(rep *EquivalenceReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Equivalence of arm %s with format 1 (%s)\n\nGenerated %s. Byte-identical frames, CIDs and provenance; C-10 collapses format 1's repeated rows per record; C-12 accepts the peer, signature and source timestamp of any of format 1's copies; a field format 1 leaves empty and the candidate fills is listed as extra.\n\n", rep.Candidate, rep.Label, rep.Generated)
	counts := map[string]int{}
	for _, v := range rep.Reads {
		counts[v.Status]++
	}
	var st []string
	for k, n := range counts {
		st = append(st, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(st)
	fmt.Fprintf(&b, "Reads: %d shapes: %s.\n\n", len(rep.Reads), strings.Join(st, ", "))
	for _, m := range rep.Missing {
		fmt.Fprintf(&b, "- Missing: %s\n", m)
	}
	b.WriteString("\n| Class | Shape | Status | Rows F1 / S | Notes |\n|---|---|---|---|---|\n")
	for _, v := range rep.Reads {
		var notes []string
		if len(v.Extra) > 0 {
			notes = append(notes, "S adds: "+strings.Join(v.Extra, ", "))
		}
		for _, d := range v.Diffs {
			notes = append(notes, fmt.Sprintf("%s row %d %s: f1 %q, s %q", d.Call, d.Row, d.Field, d.F1, d.S))
		}
		notes = append(notes, v.Notes...)
		if v.Accepted != "" {
			notes = append(notes, "accepted: "+v.Accepted)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d / %d | %s |\n", v.Class, esc(v.Shape), v.Status, v.F1Rows, v.SRows, esc(strings.Join(notes, "; ")))
	}
	if len(rep.Writes) > 0 {
		b.WriteString("\n## Writes: record and tag sets after the operation\n\n| Op | Status | Differences |\n|---|---|---|\n")
		for _, w := range rep.Writes {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", w.Op, w.Status, esc(strings.Join(w.Diffs, "; ")))
		}
	}
	return b.String()
}

// WriteReport evaluates the gates over c.Out and writes gates.json,
// gates.md and the tables.
func WriteReport(c Config) (Report, error) {
	runs, err := ReadRuns(c.Out)
	if err != nil {
		return Report{}, err
	}
	rep := EvaluateGates(runs)
	b, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return rep, err
	}
	files := map[string]string{
		"gates.json":       string(b),
		"gates.md":         GateMarkdown(rep, false),
		"gates-all.md":     GateMarkdown(rep, true),
		"reads-fixture.md": ReadsMarkdown(runs, LabelFixture),
		"reads-grown.md":   ReadsMarkdown(runs, LabelGrown),
		"ingest.md":        IngestMarkdown(runs),
		"bytes.md":         BytesMarkdown(runs),
		"m01.md":           M01Markdown(runs),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(c.Out, name), []byte(body), 0o644); err != nil {
			return rep, err
		}
	}
	return rep, nil
}
