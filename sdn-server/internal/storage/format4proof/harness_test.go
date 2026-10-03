package format4proof

// The harness's entry points. Each skips cleanly unless its environment is
// set (doc.go lists it), so `go test` of this package in CI builds it and
// skips. A typical full run, from sdn-server/:
//
//	export SDN_F1_FIXTURE=… SDN_F2_FIXTURE=… P4_FIXTURE=… P4PROOF_BENCHSET=…/benchset.json \
//	       P4PROOF_WORK=… P4PROOF_OUT=…
//	go test -c -o /tmp/p4proof.test ./internal/storage/format4proof   (through scripts/go-with-wasmedge.sh)
//	/tmp/p4proof.test -test.run 'TestProof(Prepare|Bytes|Ingest|Reads|M01|Writes|Equivalence|Report)$' -test.v -test.timeout 24h
//
// P4PROOF_LABEL=grown re-runs the reads on the stores the ingest left;
// P4PROOF_CLASSES and P4PROOF_SHAPE re-run one shape (gates.md lists the
// shapes within 10% of a bar with the values to set).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func requireEnv(t *testing.T, keys ...string) Config {
	t.Helper()
	for _, k := range keys {
		if os.Getenv(k) == "" {
			t.Skipf("%s not set (the proof harness runs on the fixture; doc.go)", k)
		}
	}
	c := ConfigFromEnv()
	if c.Work != "" && c.IsFixturePath(c.Work) {
		t.Fatalf("%s lies inside a fixture", EnvWork)
	}
	if c.Out != "" && c.IsFixturePath(c.Out) {
		t.Fatalf("%s lies inside a fixture", EnvOut)
	}
	return c
}

func logfOf(t *testing.T) Logf {
	return func(f string, a ...any) { t.Logf(f, a...) }
}

// TestProofChild is the measurement child (EnvChild set by a driver).
func TestProofChild(t *testing.T) {
	spec, err := ChildSpecFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if spec == nil {
		t.Skip("not a measurement child")
	}
	if err := ExecChild(spec); err != nil {
		t.Fatal(err)
	}
}

func TestProofPrepare(t *testing.T) {
	c := requireEnv(t, EnvF1Fixture, EnvWork)
	if err := PrepareInputs(context.Background(), c, logfOf(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProofBytes(t *testing.T) {
	c := requireEnv(t, EnvBenchset, EnvWork, EnvOut)
	bs, err := LoadBenchset(c.Benchset)
	if err != nil {
		t.Fatal(err)
	}
	fx := bs.Fixtures[FixtureT6W]
	if fx.UniqueRecords == 0 || fx.RecordCopies == 0 {
		t.Fatal("the benchset names no t6w record counts")
	}
	if err := DriveBytes(c, fx.UniqueRecords, fx.RecordCopies); err != nil {
		t.Fatal(err)
	}
}

func TestProofIngest(t *testing.T) {
	c := requireEnv(t, EnvWork, EnvOut)
	if err := DriveIngest(context.Background(), c, ParseIngestPlan(os.Getenv("P4PROOF_INGEST")), logfOf(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProofReads(t *testing.T) {
	c := requireEnv(t, EnvBenchset, EnvWork, EnvOut)
	label := os.Getenv("P4PROOF_LABEL")
	if label == "" {
		label = LabelFixture
	}
	if err := DriveReads(context.Background(), c, label, logfOf(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProofM01(t *testing.T) {
	c := requireEnv(t, EnvBenchset, EnvWork, EnvOut)
	if err := DriveM01(context.Background(), c, envInt("P4PROOF_M01_IDLE_S", 30), envInt("P4PROOF_M01_MINUTES", 10), logfOf(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProofWrites(t *testing.T) {
	c := requireEnv(t, EnvBenchset, EnvWork, EnvOut)
	if err := DriveWrites(context.Background(), c, splitList(os.Getenv("P4PROOF_WRITES")), logfOf(t)); err != nil {
		t.Fatal(err)
	}
}

// TestProofCrash runs the kill -9 loops (P4PROOF_CRASH=1) on fresh stores:
// P4PROOF_CRASH_ARMS (default s), P4PROOF_CRASH_ROUNDS per scenario (100;
// owner, 2026-10-01), P4PROOF_CRASH_SOURCE the feed's source (default
// proof-crash; any string, feednames.go).
func TestProofCrash(t *testing.T) {
	c := requireEnv(t, "P4PROOF_CRASH", EnvWork, EnvOut)
	arms := splitList(os.Getenv("P4PROOF_CRASH_ARMS"))
	if len(arms) == 0 {
		arms = []string{ArmS}
	}
	rounds := envInt("P4PROOF_CRASH_ROUNDS", 100)
	for _, arm := range arms {
		for _, sc := range []string{ScenarioIngest, ScenarioSupersede} {
			arm, sc := arm, sc
			t.Run(arm+"-"+sc, func(t *testing.T) {
				store := filepath.Join(c.Work, fmt.Sprintf("crash-%s-%s-%d", arm, sc, time.Now().Unix()))
				if err := os.MkdirAll(store, 0o755); err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(store)
				res, err := CrashLoop(context.Background(), CrashLoopSpec{Arm: arm, Scenario: sc, Store: store, Work: c.Work,
					Out: c.Out, Rounds: rounds, Batch: 1024, Source: os.Getenv("P4PROOF_CRASH_SOURCE")}, logfOf(t))
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("CRASH %s %s: %d rounds, %d killed mid-run, %d with violations", arm, sc, res.Rounds, res.Killed, res.Violating)
				if res.Violating > 0 {
					t.Errorf("%s %s: %s", arm, sc, res.FirstViolation)
				}
			})
		}
	}
}

// TestProofFeedNames is the feed-name proof (feednames.go, contract C-37
// (1)) on fresh stores: the file set, the reads against format 1, a reopen,
// store-migrate --to 4 (P4PROOF_SDN_BIN), the quota, and
// P4PROOF_FEEDNAMES_CRASH_ROUNDS kill -9 rounds per scenario (30).
func TestProofFeedNames(t *testing.T) {
	c := requireEnv(t, EnvWork, EnvOut)
	r, err := DriveFeedNames(context.Background(), c, envInt("P4PROOF_FEEDNAMES_CRASH_ROUNDS", 30), logfOf(t))
	if r != nil {
		t.Logf("FEEDNAMES: %d answer rows, quota %v, files %v", r.Extra["rows"], r.Extra["quota"], r.Extra["files_reopened"])
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestProofMigrate migrates the format-1 fixture with store-migrate --to 4
// (P4PROOF_SDN_BIN): a clean run kept as the format-4 fixture, checked
// against format 1, then a run under kill -9 (P4PROOF_MIGRATE_KILLS, 5)
// resumed to the same record and tag sets.
func TestProofMigrate(t *testing.T) {
	c := requireEnv(t, EnvSDNBin, EnvF1Fixture, EnvWork, EnvOut)
	r, err := MigrateCrashLoop(context.Background(), MigrateLoopSpec{Bin: c.SDNBin, Source: c.Fixtures[ArmF1], Work: c.Work, Out: c.Out,
		Kills: envInt("P4PROOF_MIGRATE_KILLS", 5)}, logfOf(t))
	if r != nil {
		t.Logf("MIGRATE: reference %v s, max RSS %v MB, %v kills", r.Extra["reference_seconds"], r.Extra["reference_max_rss_mb"], r.Extra["kills"])
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestProofLayout checks the format-4 fixture (P4_FIXTURE) against the
// format-1 fixture it was migrated from (contract C-37): one feed file per
// source feed x standard, no provider or source column in any feed file.
// It writes layout-s-fixture.json; the migrate phase runs the same check on
// its reference.
func TestProofLayout(t *testing.T) {
	c := requireEnv(t, EnvF1Fixture, EnvWork, EnvOut)
	p4 := c.Fixtures[ArmS]
	if p4 == "" {
		t.Skip("P4_FIXTURE not set")
	}
	f1 := c.workPath("layout-f1")
	_ = os.RemoveAll(f1)
	if err := CloneStore(c.Fixtures[ArmF1], f1); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(f1)
	lay, err := CheckFeedLayout(f1, p4, c.Work)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(c.Out, "layout-s-fixture.json"), lay); err != nil {
		t.Fatal(err)
	}
	for typ, files := range lay.Files {
		t.Logf("LAYOUT %s: %v", typ, files)
	}
	for table, cols := range lay.Columns {
		t.Logf("LAYOUT table %s: %v", table, cols)
	}
	for _, p := range lay.Problems {
		t.Error(p)
	}
}

// TestProofCoverage runs the coverage classes (coverage.go: every
// recordBackend method and parameter axis the benchset leaves out; COVERAGE.md)
// on every arm, untimed; P4PROOF_CLASSES narrows them. Their answers are
// compared by TestProofEquivalence with the reads'.
func TestProofCoverage(t *testing.T) {
	c := requireEnv(t, EnvBenchset, EnvWork, EnvOut)
	if err := DriveCoverage(context.Background(), c, logfOf(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProofEquivalence(t *testing.T) {
	c := requireEnv(t, EnvOut)
	label := os.Getenv("P4PROOF_LABEL")
	if label == "" {
		label = LabelFixture
	}
	candidate := os.Getenv("P4PROOF_EQ_ARM") // default s; f2 checks the harness against a known engine
	if candidate == "" {
		candidate = ArmS
	}
	rep, err := DriveEquivalence(c, label, candidate, logfOf(t))
	if err != nil {
		t.Fatal(err)
	}
	name := "equivalence-" + candidate + "-" + label
	if err := writeJSON(filepath.Join(c.Out, name+".json"), rep); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Out, name+".md"), []byte(EquivalenceMarkdown(rep)), 0o644); err != nil {
		t.Fatal(err)
	}
	var prov Provenance
	for _, v := range rep.Reads {
		prov.add(v.Prov)
		if !v.Passed() && v.Status != EqAccepted {
			t.Errorf("%s %s: %s", v.Class, v.Shape, v.Status)
		}
	}
	t.Logf("PROVENANCE: %d cells; blank on format 1 %d, on %s %d; blank on %s where format 1 has a value %d",
		prov.Cells, prov.F1Blank, candidate, prov.SBlank, candidate, prov.SBlankF1Set)
	if prov.SBlankF1Set > 0 {
		t.Errorf("provenance: %d cells blank on %s where format 1 has a value", prov.SBlankF1Set, candidate)
	}
	for _, w := range rep.Writes {
		if w.Status == EqDiffer {
			t.Errorf("%s: %v", w.Op, w.Diffs)
		}
	}
}

// TestProofReport writes the gate report; a failed gate fails the test.
func TestProofReport(t *testing.T) {
	c := requireEnv(t, EnvOut)
	rep, err := WriteReport(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range rep.Gates {
		t.Logf("GATE %s %s: %s", g.ID, g.Title, g.Status)
		if g.Status == StatusFail {
			t.Errorf("gate %s failed (see %s)", g.ID, filepath.Join(c.Out, "gates.md"))
		}
	}
}
