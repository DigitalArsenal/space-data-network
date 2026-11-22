package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/publicationstate"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate/pst"
)

// publicationMarks reads the marks of d's publication directory: the
// attempts each states, by file name.
func (d *publicationDaemon) publicationMarks(t *testing.T) map[string]int {
	t.Helper()
	dir := filepath.Join(d.root, "dataset-publications", "publication-runs")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	marks := map[string]int{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".fb") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		body, err := publicationstate.Decode(raw, pst.BodyBatchAction)
		if err != nil {
			t.Fatalf("mark %s: %v", entry.Name(), err)
		}
		marks[entry.Name()] = int(body.Value.(*pst.BatchActionT).Attempts)
	}
	return marks
}

// startUntilExit starts d and waits, at most budget, for its process to exit
// by itself; it returns the process's exit status.
func (d *publicationDaemon) startUntilExit(t *testing.T, budget time.Duration) int {
	t.Helper()
	requireIsolatedConfig(t, d.cfg)
	var err error
	d.mu.Lock()
	d.log, err = os.OpenFile(d.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	d.cmd = exec.Command(d.exe, "daemon", "--allow-multi-daemon", "--config", d.cfg)
	d.cmd.Dir, d.cmd.Env, d.cmd.Stdout, d.cmd.Stderr = d.root, d.env, d.log, d.log
	if err := d.cmd.Start(); err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	d.done = make(chan error, 1)
	cmd := d.cmd
	go func() { d.done <- cmd.Wait() }()
	t.Logf("started test daemon pid=%d log=%s; it is expected to exit by itself", cmd.Process.Pid, d.logPath)
	d.mu.Unlock()
	err = d.awaitExit(t, budget)
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("the daemon exited with %v, want an exit status", err)
	}
	return exit.ExitCode()
}

// TestPublicationInterruptedSupersededRealDaemons: a publication that a stop
// interrupted is not published again once its lane has published newer data
// (stack issue 1044).
//
// A batch of 150,000 element sets ("older") is being published when the node
// is stopped, which leaves its mark. The node is started again from a build
// that handles that mark only after 60 s, and meanwhile the lane publishes
// another batch ("newer"); the node is stopped before it handles the mark.
// At the next start (the release build) the node logs that "older" is not
// published again because the lane published "newer" after it, and removes
// the mark: "older" is never read for publication again and never completes,
// and the lane has no alert.
func TestPublicationInterruptedSupersededRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	faults := publicationFollowFaultsBinary(t)
	a := newPublicationDaemon(t, exe, "superseded", "", "", "keep-all")
	a.start(t)

	const source = "lateredo-superseded"
	const n, per = 150_000, 5_000
	for offset := 0; offset < n; offset += per {
		a.ingestOMMCIDs(t, source, "older", 40_000+offset, per)
	}
	a.ingestOMMCIDs(t, source, "newer", 400_000, 2_000)
	const lane = "OMM.fbs publication-e2e/" + source
	started := `publication fingerprint OMM.fbs publication-e2e/` + source + `/older: `

	older := publishInBackground(a, source, "older")
	publicationAwaitLog(t, a, started, 3*time.Minute)
	a.stop(t)
	<-older
	if log := readFileString(a.logPath); strings.Contains(log, `Dataset publication `+lane+` batch "older" completed in `) ||
		!strings.Contains(log, `Dataset publication `+lane+` batch "older" stopped with the node`) {
		t.Fatal("the stop did not interrupt the publication: the batch is too small for this test")
	}
	if marks := a.publicationMarks(t); len(marks) != 1 {
		t.Fatalf("after the interrupted publication the node holds %d marks, want 1: %v", len(marks), marks)
	}

	// The lane publishes newer data while the mark of "older" exists.
	a.exe = faults
	a.start(t)
	publicationAwaitLog(t, a, `test build: the mark of `+lane+` batch "older" is handled after `, time.Minute)
	answer := <-publishInBackground(a, source, "newer")
	if answer.err != nil || answer.status != http.StatusAccepted {
		t.Fatalf("the publication of batch newer answered HTTP %d (%v): %.512s", answer.status, answer.err, answer.body)
	}
	var newer publicationResult
	if err := json.Unmarshal(answer.body, &newer); err != nil {
		t.Fatal(err)
	}
	if newer.RecordCount != 2000 || newer.ManifestCID == "" {
		t.Fatalf("batch newer published %d records, manifest %q; want 2000 and a manifest", newer.RecordCount, newer.ManifestCID)
	}
	a.stop(t)
	if marks := a.publicationMarks(t); len(marks) != 1 {
		t.Fatalf("after batch newer was published the node holds %d marks, want the one of batch older: %v", len(marks), marks)
	}

	a.exe = exe
	a.start(t)
	dropped := `Dataset publication ` + lane + ` batch "older" is NOT published again: the lane published batch "newer" at `
	t.Logf("after the restart: %s%s", dropped, publicationAwaitLog(t, a, dropped, 2*time.Minute))
	log := readFileString(a.logPath)
	if strings.Contains(log, `Dataset publication `+lane+` batch "older" completed in `) {
		t.Fatal("batch older was published over batch newer")
	}
	if runs := strings.Count(log, started); runs != 1 {
		t.Fatalf("batch older was read for publication %d times, want once (the interrupted run)", runs)
	}
	if strings.Contains(log, `Dataset publication FAILED: `+lane+` batch "older"`) {
		t.Fatalf("batch older was reported failed although it is not published again: %s", publicationLogAfter(t, a, `Dataset publication FAILED: `+lane+` batch "older"`))
	}
	if marks := a.publicationMarks(t); len(marks) != 0 {
		t.Fatalf("the mark of batch older is still there: %v", marks)
	}
	if alert := publicationAlert(t, a, lane); alert != "" {
		t.Fatalf("the lane's alert: %s", alert)
	}
}

// TestPublicationInterruptedCrashBoundRealDaemons: a publication that ends
// the process is tried again at most three times, across starts, and then
// reported failed (stack issue 1044).
//
// The node is a build whose publications of a "crash-" lane end the process
// (status 3) as soon as they start. A batch publication ends it and leaves a
// mark. At each of the next three starts the node, once ready, counts the
// attempt in the mark (1, 2, 3) and starts it, and the process ends again. At
// the fourth start the node publishes nothing: it reports the publication
// FAILED at the step "publish again after the node's stop", given up after 3
// attempts, raises the lane's publication_failed alert and removes the mark.
// A fifth start finds no mark and reports nothing again.
func TestPublicationInterruptedCrashBoundRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	a := newPublicationDaemon(t, publicationFollowFaultsBinary(t), "crash-bound", "", "", "keep-all")
	a.start(t)

	const source = "crash-bound"
	a.ingestOMMCIDs(t, source, "first", 40_000, 2_000)
	const lane = "OMM.fbs publication-e2e/" + source
	crash := `test build: the publication of ` + lane + ` batch "first" ends the process`
	interrupted := `Dataset publication FAILED: ` + lane + ` batch "first", step "interrupted by the node's stop"`
	givenUp := `Dataset publication FAILED: ` + lane + ` batch "first", step "publish again after the node's stop"`

	if status, err := publishWithClientDeadline(a, source, "first", time.Minute); err == nil {
		t.Fatalf("the publication answered HTTP %d; the test build ends the process when it starts", status)
	}
	if err := a.awaitExit(t, 2*time.Minute); err == nil {
		t.Fatal("the daemon exited cleanly; the test build ends it with status 3")
	}
	if marks := a.publicationMarks(t); len(marks) != 1 {
		t.Fatalf("after the publication ended the process the node holds %d marks, want 1: %v", len(marks), marks)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		if status := a.startUntilExit(t, 10*time.Minute); status != 3 {
			t.Fatalf("start %d: the daemon exited with status %d, want 3 (the publication ended it)", attempt, status)
		}
		marks := a.publicationMarks(t)
		if len(marks) != 1 {
			t.Fatalf("after start %d the node holds %d marks, want 1: %v", attempt, len(marks), marks)
		}
		for name, attempts := range marks {
			if attempts != attempt {
				t.Fatalf("after start %d mark %s states %d attempts, want %d", attempt, name, attempts, attempt)
			}
		}
		if count := strings.Count(readFileString(a.logPath), crash); count != attempt+1 {
			t.Fatalf("after start %d the publication ended the process %d times, want %d", attempt, count, attempt+1)
		}
	}

	a.start(t)
	got := publicationAwaitLog(t, a, givenUp, 2*time.Minute)
	if !strings.Contains(got, "given up after 3 attempts to publish it again") {
		t.Fatalf("the give-up report: %s%s", givenUp, got)
	}
	log := readFileString(a.logPath)
	if count := strings.Count(log, crash); count != 4 {
		t.Fatalf("the publication ended the process %d times, want 4 (the run and 3 attempts)", count)
	}
	if count := strings.Count(log, interrupted); count != 3 {
		t.Fatalf("the interrupted publication was reported %d times, want 3 (once before each attempt)", count)
	}
	if marks := a.publicationMarks(t); len(marks) != 0 {
		t.Fatalf("the mark is still there after the node gave up: %v", marks)
	}
	if alert := publicationAlert(t, a, lane); !strings.Contains(alert, "given up after 3 attempts") {
		t.Fatalf("the lane's alert after the node gave up: %q, want the give-up report", alert)
	}

	a.stop(t)
	before := len(readFileString(a.logPath))
	a.start(t)
	after := readFileString(a.logPath)[before:]
	if strings.Contains(after, "did not complete before the node stopped") || strings.Contains(after, givenUp) || strings.Contains(after, crash) {
		t.Fatalf("a further start handled the publication again:\n%s", tailString(after, 20))
	}
}
