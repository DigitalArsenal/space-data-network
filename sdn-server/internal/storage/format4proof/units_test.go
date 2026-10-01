package format4proof

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	IQCfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Unit tests: no fixture, no engine.

func TestPercentileIsNearestRank(t *testing.T) {
	v := []float64{5, 1, 4, 2, 3}
	for _, c := range []struct{ q, want float64 }{{0, 1}, {0.5, 3}, {0.99, 5}, {1, 5}, {0.2, 1}, {0.21, 2}} {
		if got := Percentile(v, c.q); got != c.want {
			t.Errorf("Percentile(%v) = %v, want %v", c.q, got, c.want)
		}
	}
	if !math.IsNaN(Percentile(nil, 0.5)) {
		t.Error("no samples must be NaN")
	}
	big := make([]float64, 64)
	for i := range big {
		big[i] = float64(i + 1)
	}
	if got := Percentile(big, 0.99); got != 64 {
		t.Errorf("p99 of 64 samples = %v, want the largest", got)
	}
	if Median([]float64{1, 2, 3, 4}) != 2.5 || Median([]float64{3, 1, 2}) != 2 {
		t.Error("median")
	}
}

func TestPerDoubling(t *testing.T) {
	// Two points: (v1/v0)^(1/log2(n1/n0)).
	f, ok := PerDoubling([]Point{{100, 10}, {400, 40}})
	if !ok || math.Abs(f-2) > 1e-12 {
		t.Fatalf("x4 records, x4 time: %v per doubling, want 2", f)
	}
	f, _ = PerDoubling([]Point{{1000, 5}, {1280, 5}})
	if math.Abs(f-1) > 1e-12 {
		t.Fatalf("flat: %v", f)
	}
	// Three points on an exact power law t = n^0.5: sqrt(2) per doubling.
	f, _ = PerDoubling([]Point{{1 << 10, 32}, {1 << 12, 64}, {1 << 16, 256}})
	if math.Abs(f-math.Sqrt2) > 1e-9 {
		t.Fatalf("power law: %v, want %v", f, math.Sqrt2)
	}
	if _, ok := PerDoubling([]Point{{100, 1}}); ok {
		t.Fatal("one point has no slope")
	}
	if _, ok := PerDoubling([]Point{{100, 1}, {100, 2}}); ok {
		t.Fatal("one size has no slope")
	}
}

func bytesRun(arm string, perRecord float64) *Run {
	return &Run{Kind: KindBytes, Arm: arm, Label: LabelFixture, Extra: map[string]any{"bytes_per_record": perRecord, "bytes_per_copy": perRecord * 0.9}}
}

func gateByID(rep Report, id string) Gate {
	for _, g := range rep.Gates {
		if g.ID == id {
			return g
		}
	}
	return Gate{}
}

func TestSlimmerGate(t *testing.T) {
	g := gateByID(EvaluateGates([]*Run{bytesRun(ArmS, 785), bytesRun(ArmF1, 2366), bytesRun(ArmF2, 1742)}), "1")
	if g.Status != StatusPass || len(g.Checks) != 2 {
		t.Fatalf("785 against 1742 and 2366: %+v", g)
	}
	g = gateByID(EvaluateGates([]*Run{bytesRun(ArmS, 1800), bytesRun(ArmF1, 2366), bytesRun(ArmF2, 1742)}), "1")
	if g.Status != StatusFail || g.Checks[0].Bar != 1742 {
		t.Fatalf("1800 against 1742 must fail with the bar named: %+v", g)
	}
	g = gateByID(EvaluateGates([]*Run{bytesRun(ArmS, 785)}), "1")
	if g.Status != StatusIncomplete || len(g.Missing) == 0 {
		t.Fatalf("without the baselines the gate is incomplete: %+v", g)
	}
}

// readRun builds a read run: shape -> (cold, warm...) times.
func readRun(arm, label string, records int64, shapes map[string][]float64, errShape string) *Run {
	r := &Run{Kind: KindReads, Arm: arm, Label: label, Records: records}
	for name, v := range shapes {
		for i, ms := range v {
			s := Sample{Class: "R11", Shape: name, Pass: i, Ms: ms}
			if name == errShape && i == 1 {
				s.Err = "boom"
			}
			r.Samples = append(r.Samples, s)
		}
	}
	return r
}

func TestReadsGateHoldsEveryNumber(t *testing.T) {
	base := map[string][]float64{"a": {10, 5, 5, 5}, "b": {20, 8, 9, 10}}
	f1 := readRun(ArmF1, LabelFixture, 100, base, "")
	f2 := readRun(ArmF2, LabelFixture, 100, map[string][]float64{"a": {12, 4, 4, 4}, "b": {30, 9, 9, 9}}, "")
	s := readRun(ArmS, LabelFixture, 100, map[string][]float64{"a": {9, 4, 4, 4}, "b": {19, 8, 8, 9}}, "")
	g := gateByID(EvaluateGates([]*Run{f1, f2, s}), "2a")
	if g.Status != StatusPass || len(g.Checks) != 8 {
		t.Fatalf("faster on every number: %s, %d checks", g.Status, len(g.Checks))
	}
	// Warm p99 of "a" 4.5 > the better baseline's 4: one failed check.
	s = readRun(ArmS, LabelFixture, 100, map[string][]float64{"a": {9, 4, 4, 4.5}, "b": {19, 8, 8, 9}}, "")
	g = gateByID(EvaluateGates([]*Run{f1, f2, s}), "2a")
	var failed []string
	for _, c := range g.Checks {
		if !c.Pass {
			failed = append(failed, c.Item)
		}
	}
	if g.Status != StatusFail || len(failed) != 1 || failed[0] != "R11 a warm p99" {
		t.Fatalf("a slower warm p99 must fail exactly that check: %s %v", g.Status, failed)
	}
	// An error is infinitely slow.
	s = readRun(ArmS, LabelFixture, 100, map[string][]float64{"a": {9, 4, 4, 4}, "b": {19, 8, 8, 9}}, "b")
	g = gateByID(EvaluateGates([]*Run{f1, f2, s}), "2a")
	if g.Status != StatusFail {
		t.Fatalf("an errored call must fail: %s", g.Status)
	}
}

func TestIngestGateAgainstFormat2(t *testing.T) {
	mk := func(arm string, p99 float64, rate float64) *Run {
		r := &Run{Kind: KindIngest, Arm: arm, Label: LabelFixture, Extra: map[string]any{PhaseOneWriter: map[string]any{"rec_per_s": rate}}}
		for i := 0; i < 98; i++ {
			r.Samples = append(r.Samples, Sample{Class: PhaseOneWriter, Pass: 1, Ms: 10})
		}
		r.Samples = append(r.Samples, Sample{Class: PhaseOneWriter, Pass: 1, Ms: p99}, Sample{Class: PhaseOneWriter, Pass: 1, Ms: p99})
		return r
	}
	g := gateByID(EvaluateGates([]*Run{mk(ArmS, 300, 80000), mk(ArmF2, 306, 17000)}), "2b")
	if len(g.Checks) == 0 || !g.Checks[0].Pass {
		t.Fatalf("300 <= 306 ms: %+v", g)
	}
	if g.Status != StatusIncomplete {
		t.Fatalf("four-writer and same-type phases missing: %s", g.Status)
	}
	g = gateByID(EvaluateGates([]*Run{mk(ArmS, 600, 80000), mk(ArmF2, 306, 17000)}), "2b")
	if g.Status != StatusFail {
		t.Fatalf("600 > 306 ms must fail: %s", g.Status)
	}
	// A+B and C runs of one arm are merged.
	c := &Run{Kind: KindIngest, Arm: ArmS, Label: LabelFixture, Class: "C", Samples: []Sample{{Class: PhaseSameType, Pass: 1, Ms: 5}}}
	cf2 := &Run{Kind: KindIngest, Arm: ArmF2, Label: LabelFixture, Class: "C", Samples: []Sample{{Class: PhaseSameType, Pass: 1, Ms: 50}}}
	g = gateByID(EvaluateGates([]*Run{mk(ArmS, 300, 80000), mk(ArmF2, 306, 17000), c, cf2}), "2b")
	found := false
	for _, ck := range g.Checks {
		if strings.HasPrefix(ck.Item, "same-type") && ck.Pass {
			found = true
		}
	}
	if !found {
		t.Fatalf("the C run was not merged: %+v", g.Checks)
	}
}

func TestM01Gate(t *testing.T) {
	mk := func(arm string, maxMs, errs, wal float64) *Run {
		return &Run{Kind: KindM01, Arm: arm, Label: LabelFixture, Extra: map[string]any{"busy_read_max_ms": maxMs, "read_errors": errs, "wal_max_bytes": wal}}
	}
	if g := gateByID(EvaluateGates([]*Run{mk(ArmS, 12, 0, 1<<28)}), "2c"); g.Status != StatusPass {
		t.Fatalf("12 ms, no error, 256 MiB WAL: %+v", g)
	}
	if g := gateByID(EvaluateGates([]*Run{mk(ArmS, 51, 0, 1<<28)}), "2c"); g.Status != StatusFail {
		t.Fatalf("51 ms must fail: %s", g.Status)
	}
	if g := gateByID(EvaluateGates([]*Run{mk(ArmS, 12, 0, 2<<30)}), "2c"); g.Status != StatusFail {
		t.Fatalf("a 2 GiB WAL must fail: %s", g.Status)
	}
}

func TestDegradeGateComparesSlopes(t *testing.T) {
	// +28%: f2 grows 1.5x, s grows 1.1x on the same shape.
	runs := []*Run{
		readRun(ArmS, LabelFixture, 1000, map[string][]float64{"a": {10, 5, 5}}, ""),
		readRun(ArmS, LabelGrown, 1280, map[string][]float64{"a": {11, 5.5, 5.5}}, ""),
		readRun(ArmF2, LabelFixture, 1000, map[string][]float64{"a": {10, 5, 5}}, ""),
		readRun(ArmF2, LabelGrown, 1280, map[string][]float64{"a": {15, 7.5, 7.5}}, ""),
		{Kind: KindGrowth, Arm: ArmS, Label: "G0", Records: 1 << 20, Samples: []Sample{{Class: "probe", Shape: "get_hit", Pass: 1, Ms: 1}}},
		{Kind: KindGrowth, Arm: ArmS, Label: "G1", Records: 1 << 22, Samples: []Sample{{Class: "probe", Shape: "get_hit", Pass: 1, Ms: 1.2}}},
		{Kind: KindGrowth, Arm: ArmF2, Label: "G0", Records: 1 << 20, Samples: []Sample{{Class: "probe", Shape: "get_hit", Pass: 1, Ms: 1}}},
		{Kind: KindGrowth, Arm: ArmF2, Label: "G1", Records: 1 << 22, Samples: []Sample{{Class: "probe", Shape: "get_hit", Pass: 1, Ms: 4}}},
	}
	g := gateByID(EvaluateGates(runs), "3")
	if g.Status != StatusIncomplete && g.Status != StatusPass {
		t.Fatalf("s flatter everywhere: %s %+v", g.Status, g.Checks)
	}
	for _, c := range g.Checks {
		if !c.Pass {
			t.Fatalf("check failed: %+v", c)
		}
	}
	growth := 0
	for _, c := range g.Checks {
		if strings.HasPrefix(c.Item, "growth ") {
			growth++
			if math.Abs(c.F2-2) > 1e-9 {
				t.Fatalf("f2 4x over 2 doublings is 2 per doubling: %v", c.F2)
			}
		}
	}
	if growth == 0 {
		t.Fatal("no growth checks")
	}
	// s steeper than f2: fail.
	runs[1] = readRun(ArmS, LabelGrown, 1280, map[string][]float64{"a": {20, 10, 10}}, "")
	if g := gateByID(EvaluateGates(runs), "3"); g.Status != StatusFail {
		t.Fatalf("s steeper must fail: %s", g.Status)
	}
}

func row(kv ...string) Row { return ValueRow(kv...) }

func TestCompareShape(t *testing.T) {
	sa := func(policy Policy, calls ...Answer) *ShapeAnswers {
		return &ShapeAnswers{Class: "R01", Shape: "x", Schema: "IQC.fbs", Policy: policy, Calls: calls}
	}
	rec := func(cid, data, peer string) Row {
		return row("cid", cid, "data", data, "rowid", "0", "~peer", peer)
	}
	// Equal.
	v := CompareShape(sa(Policy{}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "p")}}),
		sa(Policy{}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "p")}}), nil)
	if v.Status != EqEqual || !v.Passed() {
		t.Fatalf("equal: %+v", v)
	}
	// Data differs.
	v = CompareShape(sa(Policy{}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "p")}}),
		sa(Policy{}, Answer{Call: "c", Rows: []Row{rec("b1", "d2", "p")}}), nil)
	if v.Status != EqDiffer || len(v.Diffs) != 1 || v.Diffs[0].Field != "data" {
		t.Fatalf("data differs: %+v", v)
	}
	// C-12: another copy's peer, resolved by the oracle.
	oracle := func(schema, cid string) ([]Row, error) {
		return []Row{row("~peer", "p"), row("~peer", "q")}, nil
	}
	f1 := sa(Policy{}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "p")}})
	s := sa(Policy{}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "q")}})
	if v = CompareShape(f1, s, oracle); v.Status != EqEqualC12 {
		t.Fatalf("C-12 copy: %+v", v)
	}
	s = sa(Policy{}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "z")}})
	if v = CompareShape(f1, s, oracle); v.Status != EqDiffer {
		t.Fatalf("a peer of no copy: %+v", v)
	}
	// S fills an optional field format 1 leaves empty.
	s = sa(Policy{}, Answer{Call: "c", Rows: []Row{row("cid", "b1", "data", "d1", "rowid", "77", "~peer", "p")}})
	if v = CompareShape(f1, s, nil); v.Status != EqExtra || len(v.Extra) != 1 || v.Extra[0] != "rowid" {
		t.Fatalf("extra rowid: %+v", v)
	}
	// A scalar zero is not optional.
	v = CompareShape(sa(Policy{}, Answer{Call: "n", Rows: []Row{row("n", "0")}}), sa(Policy{}, Answer{Call: "n", Rows: []Row{row("n", "5")}}), nil)
	if v.Status != EqDiffer {
		t.Fatalf("count 0 vs 5: %+v", v)
	}
	// C-10: format 1 repeats a record per tag row.
	f1 = sa(Policy{Collapse: true}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "p"), rec("b1", "d1", "p"), rec("b2", "d2", "p")}})
	s = sa(Policy{Collapse: true}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "p"), rec("b2", "d2", "p")}})
	if v = CompareShape(f1, s, nil); v.Status != EqEqual {
		t.Fatalf("C-10 collapse: %+v", v)
	}
	// Unordered.
	f1 = sa(Policy{Unordered: true}, Answer{Call: "c", Rows: []Row{rec("b1", "d1", "p"), rec("b2", "d2", "p")}})
	s = sa(Policy{Unordered: true}, Answer{Call: "c", Rows: []Row{rec("b2", "d2", "p"), rec("b1", "d1", "p")}})
	if v = CompareShape(f1, s, nil); v.Status != EqEqual {
		t.Fatalf("unordered: %+v", v)
	}
	// Ordered, same set: differs, with the order note.
	f1.Policy, s.Policy = Policy{}, Policy{}
	if v = CompareShape(f1, s, nil); v.Status != EqDiffer || !strings.Contains(strings.Join(v.Notes, ";"), "order") {
		t.Fatalf("order only: %+v", v)
	}
	// Errors.
	if v = CompareShape(sa(Policy{}, Answer{Call: "c", Rows: []Row{row("n", "1")}}), sa(Policy{}, Answer{Call: "c", Err: "x"}), nil); v.Status != EqSError {
		t.Fatalf("s error: %+v", v)
	}
	// An accepted difference is reported as such, never as equal.
	v = CompareShape(sa(Policy{Accepted: "C-10"}, Answer{Call: "n", Rows: []Row{row("n", "2")}}), sa(Policy{}, Answer{Call: "n", Rows: []Row{row("n", "1")}}), nil)
	if v.Status != EqAccepted || v.Passed() {
		t.Fatalf("accepted: %+v", v)
	}
}

func TestFrameRows(t *testing.T) {
	b := []byte{3, 0, 0, 0, 'a', 'b', 'c', 0, 0, 0, 0, 1, 0, 0, 0, 'z'}
	rows, err := FrameRows(b)
	if err != nil || len(rows) != 3 || rows[0].Get("len") != "3" || rows[1].Get("len") != "0" {
		t.Fatalf("frames: %v %v", rows, err)
	}
	if _, err := FrameRows([]byte{9, 0, 0, 0, 1}); err == nil {
		t.Fatal("a frame past the end must be an error")
	}
}

func TestJSONRows(t *testing.T) {
	rows, err := JSONRows([]byte(`[{"b":2,"a":"x"},{"a":"y","b":3}]`))
	if err != nil || len(rows) != 2 || rows[0][0].N != "a" || rows[1].Get("b") != "3" {
		t.Fatalf("%v %v", rows, err)
	}
}

func TestMultisetIsOrderIndependent(t *testing.T) {
	var a, b Multiset
	a.Add("x", "1")
	a.Add("y", "2")
	b.Add("y", "2")
	b.Add("x", "1")
	if a.String() != b.String() {
		t.Fatal("order changed the digest")
	}
	b.Add("x", "1")
	if a.String() == b.String() {
		t.Fatal("a repeat must change the digest")
	}
	d := DigestDiff(TypeDigest{Records: 2, Partitions: map[string]PartitionCount{"p": {1, 2}}}, TypeDigest{Records: 3, Partitions: map[string]PartitionCount{"p": {1, 2}}})
	if len(d) != 1 || !strings.HasPrefix(d[0], "records") {
		t.Fatalf("diff: %v", d)
	}
}

func TestBuildShapesCoversEveryRead(t *testing.T) {
	bs, err := LoadBenchset(filepath.Join("testdata", "benchset-mini.json"))
	if err != nil {
		t.Fatal(err)
	}
	shapes, err := BuildShapes(bs, &Inputs{B052CIDs: []string{"bafkreib052"}})
	if err != nil {
		t.Fatal(err)
	}
	byClass := map[string]int{}
	names := map[string]bool{}
	for _, s := range shapes {
		byClass[s.Class]++
		key := s.Class + "\x00" + s.Name
		if names[key] {
			t.Errorf("duplicate shape %s %s", s.Class, s.Name)
		}
		names[key] = true
		if len(s.Calls) == 0 {
			t.Errorf("%s %s has no calls", s.Class, s.Name)
		}
		calls := map[string]bool{}
		for _, c := range s.Calls {
			if calls[c.Name] && s.Class != "R01" && s.Class != "R02" {
				t.Errorf("%s %s: duplicate call %s", s.Class, s.Name, c.Name)
			}
			calls[c.Name] = true
		}
	}
	for _, r := range bs.Reads {
		if byClass[r.ID] == 0 {
			t.Errorf("%s has no shapes", r.ID)
		}
	}
	for _, s := range ShapesOf(shapes, "R24", FixtureH2Copy) {
		if s.Schema != "PNM.fbs" {
			t.Errorf("R24 is PNM on h2copy: %+v", s.Schema)
		}
	}
	if len(ShapesOf(shapes, "R22", FixtureT6W)) != 0 {
		t.Error("R22 runs on h2copy only")
	}
	if got := len(ShapesOf(shapes, "R12", FixtureT6W)); got < 12 {
		t.Errorf("R12 must carry the earlier baselines' type windows: %d shapes", got)
	}
}

func TestFrameFilesRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.bin")
	in := [][]byte{[]byte("a"), {}, []byte("hello")}
	if err := WriteFrames(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFrames(p)
	if err != nil || len(out) != 3 || string(out[2]) != "hello" || len(out[1]) != 0 {
		t.Fatalf("%q %v", out, err)
	}
}

func buildIQC(t *testing.T) []byte {
	b := flatbuffers.NewBuilder(256)
	ret, created, updated := b.CreateString("2026-09-30T10:00:00Z"), b.CreateString("2026-09-30T10:00:01Z"), b.CreateString("2026-09-30T10:00:02Z")
	start := b.CreateString("2026-09-01T00:00:00Z")
	IQCfb.IQCStart(b)
	IQCfb.IQCAddRETRIEVED_AT(b, ret)
	IQCfb.IQCAddCREATED_AT(b, created)
	IQCfb.IQCAddUPDATED_AT(b, updated)
	IQCfb.IQCAddCAPTURE_START(b, start)
	IQCfb.FinishIQCBuffer(b, IQCfb.IQCEnd(b))
	return b.FinishedBytes()
}

func TestRestampIQCChangesOnlyTheVolatileStrings(t *testing.T) {
	seed := buildIQC(t)
	out, changed := RestampIQC(seed, 3)
	if !changed || len(out) != len(seed) || string(out) == string(seed) {
		t.Fatal("restamp must change bytes in place")
	}
	a, b := IQCfb.GetRootAsIQC(seed, 0), IQCfb.GetRootAsIQC(out, 0)
	if string(a.CAPTURE_START()) != string(b.CAPTURE_START()) {
		t.Fatal("CAPTURE_START is not volatile")
	}
	if string(a.RETRIEVED_AT()) == string(b.RETRIEVED_AT()) || string(a.UPDATED_AT()) == string(b.UPDATED_AT()) {
		t.Fatal("the volatile stamps did not change")
	}
	diff := 0
	for i := range seed {
		if seed[i] != out[i] {
			diff++
		}
	}
	if diff != 3 {
		t.Fatalf("%d bytes changed, want one digit per volatile field", diff)
	}
}

func TestClonesMakeNewCIDs(t *testing.T) {
	recs := CrashRecords(1, 0, 4)
	if string(CloneOf("OMM", recs[0], 1, nil)) == string(recs[0]) {
		t.Fatal("an OMM clone must shift the epoch")
	}
	if string(CloneOf("OMM", recs[0], 1, nil)) == string(CloneOf("OMM", recs[0], 2, nil)) {
		t.Fatal("two clones must differ")
	}
}

func TestCrashRecordsAreDeterministicAndDistinct(t *testing.T) {
	a, b := CrashRecords(3, 7, 32), CrashRecords(3, 7, 32)
	seen := map[string]bool{}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			t.Fatal("not deterministic")
		}
		cid := storage.ComputeCID(a[i])
		if !strings.HasPrefix(cid, "bafkrei") || seen[cid] {
			t.Fatalf("cid %s", cid)
		}
		seen[cid] = true
	}
	for _, r := range CrashRecords(3, 8, 32) {
		if seen[storage.ComputeCID(r)] {
			t.Fatal("two calls share a record")
		}
	}
}

func TestCrashLogParsing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(ackPath(dir), []byte("START 1\nACK 1 0 1024\nACK 1 1 1024\nERR 1 2 boom\nACK 2 0 10\nSUPDONE 2 5 5\nACK 2 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	cid := "bafkrei" + strings.Repeat("a", 52)
	if err := os.WriteFile(followPath(dir), []byte("SEQ 5 "+cid+"\nSEQ 6 bad\nSEQ 7 "+cid[:30]), 0o644); err != nil {
		t.Fatal(err)
	}
	cl, err := readCrashLogs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cl.acked[1]) != 2 || len(cl.acked[2]) != 1 || !cl.supDone[2] || len(cl.errs) != 1 {
		t.Fatalf("acks: %+v", cl)
	}
	if len(cl.seen) != 1 || cl.seen[5] != cid {
		t.Fatalf("a torn follower line must be skipped: %v", cl.seen)
	}
	writeFloor(dir, 41)
	writeFloor(dir, 40)
	if readFloor(dir) != 41 {
		t.Fatal("floor")
	}
}

func TestReportRendersFromAResultsDirectory(t *testing.T) {
	out := t.TempDir()
	for _, r := range []*Run{bytesRun(ArmS, 785), bytesRun(ArmF1, 2366), bytesRun(ArmF2, 1742),
		readRun(ArmS, LabelFixture, 100, map[string][]float64{"a": {9, 4}}, ""),
		readRun(ArmF2, LabelFixture, 100, map[string][]float64{"a": {12, 4}}, "")} {
		r.Extra = finiteMap(r.Extra)
		if _, err := WriteRun(out, r); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := WriteReport(Config{Out: out})
	if err != nil {
		t.Fatal(err)
	}
	if gateByID(rep, "1").Status != StatusPass {
		t.Fatalf("gate 1: %+v", gateByID(rep, "1"))
	}
	md, err := os.ReadFile(filepath.Join(out, "gates.md"))
	if err != nil || !strings.Contains(string(md), "| 1 Slimmer") {
		t.Fatalf("gates.md: %v\n%s", err, md)
	}
	reads, _ := os.ReadFile(filepath.Join(out, "reads-fixture.md"))
	if !strings.Contains(string(reads), "| R11 | a |") || !strings.Contains(string(reads), "yes") {
		t.Fatalf("reads table:\n%s", reads)
	}
}

func TestFormatMs(t *testing.T) {
	for in, want := range map[float64]string{0.812: "812 µs", 4.62: "4.6 ms", 52.4: "52 ms", 3170: "3.17 s", math.Inf(1): "error"} {
		if got := FormatMs(in); got != want {
			t.Errorf("FormatMs(%v) = %q, want %q", in, got, want)
		}
	}
}
