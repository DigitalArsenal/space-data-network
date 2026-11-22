package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// publicationInFlight is the answer to a publication request sent in the
// background.
type publicationInFlight struct {
	status int
	body   []byte
	err    error
}

// publishInBackground sends the batch publication a flow's IngestResult
// makes and delivers its answer on the returned channel.
func publishInBackground(d *publicationDaemon, source, batch string) <-chan publicationInFlight {
	answer := make(chan publicationInFlight, 1)
	go func() {
		client := http.Client{Timeout: 20 * time.Minute}
		response, err := client.Post(d.base+"/api/v1/admin/dataset-updates/publish", "application/json", bytes.NewReader(batchPublicationRequest(source, batch)))
		if err != nil {
			answer <- publicationInFlight{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		answer <- publicationInFlight{status: response.StatusCode, body: body, err: err}
	}()
	return answer
}

// inBundle runs d from a bundle (bin/spacedatanetwork and manifest.json under
// one root), so it serves the update shutdown handshake. It returns the
// bundle's update paths.
func (d *publicationDaemon) inBundle(t *testing.T) update.Paths {
	t.Helper()
	root := filepath.Join(d.root, "bundle")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "bin", "spacedatanetwork")
	if err := os.Link(d.exe, exe); err != nil {
		raw, readErr := os.ReadFile(d.exe)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(exe, raw, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"schema":"sdn-bundle/v1","version":"0.0.0-e2e","channel":"release"}`
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	// The daemon names its bundle by its executable's resolved path.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	d.exe = filepath.Join(resolved, "bin", "spacedatanetwork")
	return update.PathsFor(resolved)
}

// updateShutdown makes the update helper's shutdown handshake with d (a
// one-time control token, then POST update.ControlShutdownPath) and returns
// the daemon's answer.
func (d *publicationDaemon) updateShutdown(t *testing.T, paths update.Paths) map[string]any {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	if err := update.WriteControlToken(paths, token); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token, "bundleRoot": paths.Root})
	raw := d.request(t, "POST", update.ControlShutdownPath, "application/json", body, http.StatusAccepted)
	var answer map[string]any
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("update shutdown answer %q: %v", raw, err)
	}
	return answer
}

// awaitExit waits for d's process to exit by itself.
func (d *publicationDaemon) awaitExit(t *testing.T, budget time.Duration) error {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd == nil {
		t.Fatal("no daemon process to wait for")
	}
	select {
	case err := <-d.done:
		d.log.Close()
		d.cmd = nil
		return err
	case <-time.After(budget):
		t.Fatalf("the daemon did not exit within %s; %s", budget, tailString(readFileString(d.logPath), 20))
		return nil
	}
}

// TestPublicationUpdateRestartWaitsRealDaemons: an update's restart waits for
// the dataset publication that runs when it arrives (stack issue 1044).
//
// A node in a bundle publishes a batch of 150,000 element sets. While the
// publication runs, the update helper's shutdown handshake arrives. The node
// answers that it waits up to its bound for the one running publication,
// `status` says so, and a new publication requested during the wait is
// refused as retryable (HTTP 503) and kept for after the restart. The running
// publication completes and answers its client; only then does the node
// shut down. Started again, the node publishes the refused batch by itself,
// and the batch that completed is not published again.
func TestPublicationUpdateRestartWaitsRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "update-wait", "", "", "keep-all")
	paths := a.inBundle(t)
	a.start(t)

	const n, per = 150_000, 5_000
	for offset := 0; offset < n; offset += per {
		a.ingestOMMCIDs(t, "updwait", "first", 40_000+offset, per)
	}
	a.ingestOMMCIDs(t, "updwait", "second", 400_000, 2_000)
	const lane = "OMM.fbs publication-e2e/updwait"
	started := `publication fingerprint OMM.fbs publication-e2e/updwait/first: `

	first := publishInBackground(a, "updwait", "first")
	publicationAwaitLog(t, a, started, 3*time.Minute)
	if strings.Contains(readFileString(a.logPath), `Dataset publication `+lane+` batch "first" completed in `) {
		t.Fatal("the publication completed before the update arrived: the batch is too small for this test")
	}

	answer := a.updateShutdown(t, paths)
	t.Logf("update shutdown answer: %v", answer)
	status, statusErr := a.cli(t, "status")
	second, secondErr := publishWithClientDeadline(a, "updwait", "second", 30*time.Second)

	// The running publication completes and answers its client.
	var done publicationInFlight
	select {
	case done = <-first:
	case <-time.After(15 * time.Minute):
		t.Fatal("the running publication did not answer within 15 minutes")
	}
	log := readFileString(a.logPath)
	if strings.Contains(log, `Dataset publication `+lane+` batch "first" stopped with the node`) {
		t.Fatalf("the update's restart stopped the running publication: %s", publicationLogAfter(t, a, `Dataset publication `+lane+` batch "first" stopped with the node`))
	}
	if done.err != nil || done.status != http.StatusAccepted {
		t.Fatalf("the running publication answered HTTP %d (%v): %.512s", done.status, done.err, done.body)
	}
	var pub publicationResult
	if err := json.Unmarshal(done.body, &pub); err != nil {
		t.Fatal(err)
	}
	if pub.RecordCount != n || pub.ManifestCID == "" {
		t.Fatalf("the running publication answered %d records, manifest %q; want %d and a manifest", pub.RecordCount, pub.ManifestCID, n)
	}

	// The node said why it waited, and how long it waits at most.
	if got, ok := answer["waitBeforeStopSeconds"].(float64); !ok || got != 1800 {
		t.Fatalf("the update shutdown answer states waitBeforeStopSeconds %v, want 1800 (the 30 minute bound): %v", answer["waitBeforeStopSeconds"], answer)
	}
	if got, ok := answer["runningPublications"].(float64); !ok || got != 1 {
		t.Fatalf("the update shutdown answer states runningPublications %v, want 1: %v", answer["runningPublications"], answer)
	}
	if statusErr != nil && !strings.Contains(status, "update_stop_wait=") {
		t.Fatalf("status during the wait: %v\n%s", statusErr, status)
	}
	if !strings.Contains(status, "update_stop_wait=dataset-publications running=1 ") {
		t.Fatalf("status during the wait does not say the update waits for the running publication:\n%s", status)
	}

	// A new publication during the wait is refused, retryable, and kept.
	if secondErr != nil || second != http.StatusServiceUnavailable {
		t.Fatalf("a new publication requested during the wait answered HTTP %d (%v), want 503", second, secondErr)
	}
	refused := `Dataset publication ` + lane + ` batch "second" refused: the node is restarting for an update`
	if !strings.Contains(log, refused) {
		t.Fatalf("the log has no %q", refused)
	}

	// The node shut down only after the publication completed.
	if err := a.awaitExit(t, 3*time.Minute); err != nil {
		t.Fatalf("the daemon exited with %v after the update shutdown", err)
	}
	log = readFileString(a.logPath)
	completed := strings.Index(log, `Dataset publication `+lane+` batch "first" completed in `)
	shutdown := strings.LastIndex(log, "Shutting down...")
	if completed < 0 || shutdown < 0 || completed > shutdown {
		t.Fatalf("the publication did not complete before the shutdown (completed at byte %d, shutting down at byte %d)", completed, shutdown)
	}
	if !strings.Contains(log, "Update shutdown: waiting up to 30m0s for 1 running dataset publication(s)") ||
		!strings.Contains(log, "Update shutdown: every running dataset publication finished after ") {
		t.Fatal("the log does not record the wait for the running publication")
	}

	// The update's restart: the node publishes the refused batch by itself.
	a.start(t)
	publicationAwaitLog(t, a, `Dataset publication `+lane+` batch "second" completed in `, 6*time.Minute)
	if got := publicationLogAfter(t, a, `Dataset publication `+lane+` batch "second" completed in `); !strings.Contains(got, ": 2000 records, manifest ") {
		t.Fatalf("the refused batch after the restart: %s", got)
	}
	if runs := strings.Count(readFileString(a.logPath), started); runs != 1 {
		t.Fatalf("batch first was read for publication %d times, want once: it completed before the restart", runs)
	}
	if alert := publicationAlert(t, a, lane); alert != "" {
		t.Fatalf("the lane's alert after the refused batch was published: %s", alert)
	}
	out, _ := a.cli(t, "status")
	if strings.Contains(out, "update_stop_wait=") {
		t.Fatalf("status after the restart still reports a wait:\n%s", out)
	}
}

// TestPublicationInterruptedRedoneRealDaemons: a publication that a stop of
// the node interrupted is published again when the node starts (stack issue
// 1044).
//
// A batch of 150,000 element sets is being published when the node is
// stopped (SIGINT, as an operator's restart). At its next start the node
// logs the publication as failed at the step "interrupted by the node's
// stop", raising the lane's publication_failed alert, publishes the batch
// again, and the completed publication clears the alert. A later start
// publishes nothing again.
func TestPublicationInterruptedRedoneRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "interrupted", "", "", "keep-all")
	a.start(t)

	const n, per = 150_000, 5_000
	for offset := 0; offset < n; offset += per {
		a.ingestOMMCIDs(t, "interrupted", "first", 40_000+offset, per)
	}
	const lane = "OMM.fbs publication-e2e/interrupted"
	started := `publication fingerprint OMM.fbs publication-e2e/interrupted/first: `
	completed := `Dataset publication ` + lane + ` batch "first" completed in `
	// A publication reads its first window once; the change check before a
	// redo reads the fingerprint only.
	read := `Dataset publication window OMM.fbs publication-e2e/interrupted/first offset=0 `

	first := publishInBackground(a, "interrupted", "first")
	publicationAwaitLog(t, a, started, 3*time.Minute)
	a.stop(t)
	<-first
	log := readFileString(a.logPath)
	if strings.Contains(log, completed) || !strings.Contains(log, `Dataset publication `+lane+` batch "first" stopped with the node`) {
		t.Fatal("the stop did not interrupt the publication: the batch is too small for this test")
	}

	a.start(t)
	interrupted := `Dataset publication FAILED: ` + lane + ` batch "first", step "interrupted by the node's stop"`
	publicationAwaitLog(t, a, interrupted, 2*time.Minute)
	t.Logf("after the restart: %s%s", interrupted, publicationLogAfter(t, a, interrupted))
	got := publicationAwaitLog(t, a, completed, 6*time.Minute)
	if !strings.Contains(got, ": 150000 records, manifest ") {
		t.Fatalf("the publication published again after the restart: %s", got)
	}
	if runs := strings.Count(readFileString(a.logPath), read); runs != 2 {
		t.Fatalf("batch first was published %d times, want twice (the interrupted run and its redo)", runs)
	}
	if alert := publicationAlert(t, a, lane); alert != "" {
		t.Fatalf("the lane's alert after the publication completed: %s", alert)
	}

	// The completed publication is not redone at the next start.
	a.stop(t)
	a.start(t)
	time.Sleep(20 * time.Second)
	log = readFileString(a.logPath)
	if runs := strings.Count(log, read); runs != 2 {
		t.Fatalf("batch first was published %d times after a further restart, want twice", runs)
	}
	if count := strings.Count(log, interrupted); count != 1 {
		t.Fatalf("the interrupted publication was reported %d times, want once", count)
	}
}
