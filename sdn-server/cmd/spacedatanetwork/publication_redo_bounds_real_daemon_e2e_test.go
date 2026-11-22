package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/publicationstate"
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
		mark, err := publicationstate.DecodeMark(raw)
		if err != nil {
			t.Fatalf("mark %s: %v", entry.Name(), err)
		}
		marks[entry.Name()] = int(mark.Attempts)
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

// publicationGates sets the folder of the test build's gates
// (dataset_publication_faults_e2e.go) for d's next starts and returns it; a
// gate is opened with openPublicationGate.
func (d *publicationDaemon) publicationGates(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(d.root, "gates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d.env = append(d.env, "SDN_E2E_PUBLICATION_GATES="+dir)
	return dir
}

func openPublicationGate(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshotLane declares the lane (OMM, publication-e2e, source) of d a
// snapshot lane (publishing.retention.lanes[].snapshot_batches), keeping
// every series, for d's next starts.
func (d *publicationDaemon) snapshotLane(t *testing.T, source string) {
	t.Helper()
	raw, err := os.ReadFile(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	const publishing = "publishing:\n  enabled: true\n"
	if strings.Count(string(raw), publishing) != 1 {
		t.Fatalf("%s has no single %q block", d.cfg, publishing)
	}
	cfg := strings.Replace(string(raw), publishing, publishing+
		"  retention:\n    lanes:\n      - schema: OMM.fbs\n        provider_id: publication-e2e\n        source_name: "+source+
		"\n        keep_series: 0\n        snapshot_batches: true\n", 1)
	if err := os.WriteFile(d.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	requireIsolatedConfig(t, d.cfg)
}

// TestPublicationInterruptedSupersededRealDaemons: on a snapshot lane, a
// publication that a stop interrupted is not published again once its lane
// has published newer data, also when that newer data is published while the
// redo waits for the lane (stack issue 1044).
//
// First, the check before the redo: a batch of 150,000 element sets ("older")
// is being published when the node is stopped, which leaves its mark. The
// node is started again from a build that handles that mark only once the
// test opens its gate, and meanwhile the lane publishes another batch
// ("newer"); the node is stopped before it handles the mark. At the next
// start (the release build) the node logs that "older" is not published
// again because the lane published "newer" after it, and removes the mark:
// "older" is never read for publication again and never completes, and the
// lane has no alert.
//
// Then the check once the redo holds the lane: a third batch of 150,000
// ("third") is interrupted the same way. At the next start (the test build),
// the publication of a fourth batch ("heldlane-fourth") holds the lane, and
// waits there for its gate, before the test opens the redo's gate. The
// redo's first check passes (the fourth batch has no series yet), so the
// node reports "third" interrupted and starts publishing it again; then the
// test opens the fourth batch's gate, and the fourth batch completes while
// the redo waits for the lane. Once the redo holds the lane it finds the
// fourth batch's series, newer than its request: "third" is NOT published
// again, its report is cleared, it is read for publication only once, its
// mark is gone and the lane has no alert.
func TestPublicationInterruptedSupersededRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	faults := publicationFollowFaultsBinary(t)
	a := newPublicationDaemon(t, exe, "superseded", "", "", "keep-all")
	const source = "lateredo-superseded"
	a.snapshotLane(t, source)
	gates := a.publicationGates(t)
	a.start(t)

	const n, per = 150_000, 5_000
	for offset := 0; offset < n; offset += per {
		a.ingestOMMCIDs(t, source, "older", 40_000+offset, per)
		a.ingestOMMCIDs(t, source, "third", 600_000+offset, per)
	}
	a.ingestOMMCIDs(t, source, "newer", 400_000, 2_000)
	a.ingestOMMCIDs(t, source, "heldlane-fourth", 900_000, 2_000)
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

	// The lane publishes newer data while the mark of "older" exists; its
	// gate is never opened.
	a.exe = faults
	a.start(t)
	publicationAwaitLog(t, a, `test build: the mark of `+lane+` batch "older" is handled until gate "redo-older" is open`, time.Minute)
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

	// The check once the redo holds the lane.
	thirdStarted := `publication fingerprint OMM.fbs publication-e2e/` + source + `/third: `
	third := publishInBackground(a, source, "third")
	publicationAwaitLog(t, a, thirdStarted, 3*time.Minute)
	a.stop(t)
	<-third
	if log := readFileString(a.logPath); strings.Contains(log, `Dataset publication `+lane+` batch "third" completed in `) ||
		!strings.Contains(log, `Dataset publication `+lane+` batch "third" stopped with the node`) {
		t.Fatal("the stop did not interrupt the publication of batch third: the batch is too small for this test")
	}
	if marks := a.publicationMarks(t); len(marks) != 1 {
		t.Fatalf("after the interrupted publication of batch third the node holds %d marks, want 1: %v", len(marks), marks)
	}

	a.exe = faults
	a.start(t)
	publicationAwaitLog(t, a, `test build: the mark of `+lane+` batch "third" is handled until gate "redo-third" is open`, time.Minute)
	fourth := publishInBackground(a, source, "heldlane-fourth")
	publicationAwaitLog(t, a, `test build: the publication of `+lane+` batch "heldlane-fourth" holds its lane until gate "lane-heldlane-fourth" is open`, time.Minute)
	openPublicationGate(t, gates, "redo-third")
	// The redo's first check passed while the fourth batch held the lane.
	reported := `Dataset publication FAILED: ` + lane + ` batch "third", step "interrupted by the node's stop"`
	publicationAwaitLog(t, a, reported, time.Minute)
	openPublicationGate(t, gates, "lane-heldlane-fourth")
	answer = <-fourth
	if answer.err != nil || answer.status != http.StatusAccepted {
		t.Fatalf("the publication of batch heldlane-fourth answered HTTP %d (%v): %.512s", answer.status, answer.err, answer.body)
	}
	var fourthResult publicationResult
	if err := json.Unmarshal(answer.body, &fourthResult); err != nil {
		t.Fatal(err)
	}
	if fourthResult.RecordCount != 2000 || fourthResult.ManifestCID == "" {
		t.Fatalf("batch heldlane-fourth published %d records, manifest %q; want 2000 and a manifest", fourthResult.RecordCount, fourthResult.ManifestCID)
	}
	droppedHeld := `Dataset publication ` + lane + ` batch "third" is NOT published again: the lane published batch "heldlane-fourth" at `
	rest := publicationAwaitLog(t, a, droppedHeld, 2*time.Minute)
	t.Logf("once the redo held the lane: %s%s", droppedHeld, rest)
	if !strings.HasSuffix(rest, "Its mark is removed; its report as interrupted is cleared") {
		t.Fatalf("the drop of batch third: %s%s; want its report cleared", droppedHeld, rest)
	}
	log = readFileString(a.logPath)
	if strings.Contains(log, `Dataset publication `+lane+` batch "third" completed in `) {
		t.Fatal("batch third was published over batch heldlane-fourth")
	}
	if runs := strings.Count(log, thirdStarted); runs != 1 {
		t.Fatalf("batch third was read for publication %d times, want once (the interrupted run)", runs)
	}
	if count := strings.Count(log, reported); count != 1 {
		t.Fatalf("batch third was reported interrupted %d times, want once", count)
	}
	if marks := a.publicationMarks(t); len(marks) != 0 {
		t.Fatalf("the mark of batch third is still there: %v", marks)
	}
	if alert := publicationAlert(t, a, lane); alert != "" {
		t.Fatalf("the lane's alert after batch third was dropped: %s", alert)
	}
}

// TestPublicationInterruptedPartsRealDaemons: on a lane whose batches are
// parts (no snapshot_batches), every part a stop interrupted is published
// again, oldest first, each once (stack issue 1044).
//
// Three parts of one lane are requested in turn: "part-1" (150,000 element
// sets) runs, and "part-2" and "part-3" (2,000 each) wait for the lane behind
// it. The node is stopped, which leaves the three marks. At the next start
// every part is reported interrupted and published again, in the order they
// were requested, each once and each whole; no mark is left and the lane has
// no alert. Before this, the newest part was published again and the two
// older ones were dropped as superseded by it, and never published.
func TestPublicationInterruptedPartsRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	a := newPublicationDaemon(t, publicationBinary(t), "parts", "", "", "keep-all")
	a.start(t)

	const source = "parts"
	const n, per = 150_000, 5_000
	for offset := 0; offset < n; offset += per {
		a.ingestOMMCIDs(t, source, "part-1", 40_000+offset, per)
	}
	a.ingestOMMCIDs(t, source, "part-2", 400_000, 2_000)
	a.ingestOMMCIDs(t, source, "part-3", 500_000, 2_000)
	const lane = "OMM.fbs publication-e2e/" + source
	parts := []string{"part-1", "part-2", "part-3"}
	records := map[string]int{"part-1": n, "part-2": 2_000, "part-3": 2_000}

	answers := []<-chan publicationInFlight{publishInBackground(a, source, "part-1")}
	publicationAwaitLog(t, a, `publication fingerprint OMM.fbs publication-e2e/`+source+`/part-1: `, 3*time.Minute)
	for i, part := range parts[1:] {
		answers = append(answers, publishInBackground(a, source, part))
		// Each part's run has started, and written its mark, before the next
		// is requested: the marks state the order of the requests.
		deadline := time.Now().Add(time.Minute)
		for len(a.publicationMarks(t)) != i+2 {
			if time.Now().After(deadline) {
				t.Fatalf("after %s was requested the node holds %d marks, want %d", part, len(a.publicationMarks(t)), i+2)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	a.stop(t)
	for _, answer := range answers {
		<-answer
	}
	log := readFileString(a.logPath)
	for _, part := range parts {
		if strings.Contains(log, `Dataset publication `+lane+` batch "`+part+`" completed in `) ||
			!strings.Contains(log, `Dataset publication `+lane+` batch "`+part+`" stopped with the node`) {
			t.Fatalf("the stop did not interrupt the publication of %s", part)
		}
	}
	if marks := a.publicationMarks(t); len(marks) != 3 {
		t.Fatalf("after the stop the node holds %d marks, want 3: %v", len(marks), marks)
	}

	before := len(readFileString(a.logPath))
	a.start(t)
	for _, part := range parts {
		publicationAwaitLog(t, a, `Dataset publication `+lane+` batch "`+part+`" completed in `, 3*time.Minute)
	}
	after := readFileString(a.logPath)[before:]
	last := -1
	for _, part := range parts {
		completed := `Dataset publication ` + lane + ` batch "` + part + `" completed in `
		if count := strings.Count(after, completed); count != 1 {
			t.Fatalf("%s was published %d times after the restart, want once", part, count)
		}
		want := fmt.Sprintf(": %d records, manifest ", records[part])
		if rest := publicationLogAfter(t, a, completed); !strings.Contains(rest, want) {
			t.Fatalf("%s: %s%s; want %d records", part, completed, rest, records[part])
		}
		reported := `Dataset publication FAILED: ` + lane + ` batch "` + part + `", step "interrupted by the node's stop"`
		if count := strings.Count(after, reported); count != 1 {
			t.Fatalf("%s was reported interrupted %d times after the restart, want once", part, count)
		}
		at := strings.Index(after, completed)
		if at < last {
			t.Fatalf("%s was published before the part requested before it; want the parts in the order they were requested", part)
		}
		last = at
	}
	if strings.Contains(after, "is NOT published again") {
		t.Fatalf("a part was dropped: %s", publicationLogAfter(t, a, "is NOT published again"))
	}
	if marks := a.publicationMarks(t); len(marks) != 0 {
		t.Fatalf("marks are left after every part was published: %v", marks)
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
	// The store format and the engine cache of every publication daemon.
	publicationBinary(t)
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
