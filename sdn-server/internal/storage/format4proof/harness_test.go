package format4proof

// The harness's entry points. Each skips cleanly unless its environment is
// set (doc.go lists it), so `go test` of this package in CI runs only the
// unit tests. A typical full run, from sdn-server/:
//
//	export SDN_F1_FIXTURE=… SDN_F2_FIXTURE=… P4_FIXTURE=… P4PROOF_BENCHSET=…/benchset.json \
//	       P4PROOF_WORK=… P4PROOF_OUT=…
//	go test -c -o /tmp/p4proof.test ./internal/storage/format4proof   (through scripts/go-with-wasmedge.sh)
//	/tmp/p4proof.test -test.run 'TestProof(Prepare|Bytes|Ingest|Reads|M01|Writes|Equivalence|Report)$' -test.v -test.timeout 24h
//
// P4PROOF_LABEL=grown re-runs the reads on the stores the ingest left.

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
	c := requireEnv(t, EnvBenchset, EnvOut)
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
	if err := DriveM01(context.Background(), c, envInt("P4PROOF_M01_IDLE_S", 30), envInt("P4PROOF_M01_MINUTES", 0), logfOf(t)); err != nil {
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
// P4PROOF_CRASH_ARMS (default s), P4PROOF_CRASH_ROUNDS per scenario (25).
func TestProofCrash(t *testing.T) {
	c := requireEnv(t, "P4PROOF_CRASH", EnvWork, EnvOut)
	arms := splitList(os.Getenv("P4PROOF_CRASH_ARMS"))
	if len(arms) == 0 {
		arms = []string{ArmS}
	}
	rounds := envInt("P4PROOF_CRASH_ROUNDS", 25)
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
					Out: c.Out, Rounds: rounds, Batch: 1024}, logfOf(t))
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

func TestProofEquivalence(t *testing.T) {
	c := requireEnv(t, EnvOut)
	label := os.Getenv("P4PROOF_LABEL")
	if label == "" {
		label = LabelFixture
	}
	rep, err := DriveEquivalence(c, label, logfOf(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(c.Out, "equivalence-"+label+".json"), rep); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Out, "equivalence-"+label+".md"), []byte(EquivalenceMarkdown(rep)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range rep.Reads {
		if !v.Passed() && v.Status != EqAccepted {
			t.Errorf("%s %s: %s", v.Class, v.Shape, v.Status)
		}
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
