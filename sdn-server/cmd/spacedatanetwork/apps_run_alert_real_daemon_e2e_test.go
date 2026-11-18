package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// gpFlowAppID is the CelesTrak GP ingest flow's app id; gpFlowTrigger its
// timer.
const (
	gpFlowAppID   = "com.digitalarsenal.flows.celestrak-gp-ingest"
	gpFlowTrigger = "timer-gp"
)

// manualRunEnd is the log marker of a manual run's end, followed by its
// status.
const manualRunEnd = `Plugin "` + gpFlowAppID + `" manual run "` + gpFlowTrigger + `" end status=`

// awaitManualRunEnd waits for the n-th manual run end in the daemon's log
// and returns its status line.
func awaitManualRunEnd(t *testing.T, d *publicationDaemon, n int, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if strings.Count(readFileString(d.logPath), manualRunEnd) >= n {
			return publicationLogAfter(t, d, manualRunEnd)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("manual run %d did not end within %s (%s)", n, budget, d.logPath)
	return ""
}

// appsCLI runs the spacedatanetwork CLI against the daemon and answers its
// output, its error and how long it ran.
func appsCLI(t *testing.T, d *publicationDaemon, args ...string) (string, error, time.Duration) {
	t.Helper()
	cmd := exec.Command(d.exe, append([]string{"--config", d.cfg}, args...)...)
	cmd.Env = appsCLIEnv(t, d)
	started := time.Now()
	out, err := cmd.CombinedOutput()
	return string(out), err, time.Since(started)
}

// appsCLIEnv is the daemon's environment for a CLI command, which signs in
// with the node's root key through the HD-wallet module: the copy the binary
// embeds, from this repository.
func appsCLIEnv(t *testing.T, d *publicationDaemon) []string {
	t.Helper()
	wallet, err := filepath.Abs(filepath.Join("..", "..", "internal", "wasm", "hd-wallet-wasi.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]string(nil), d.env...), "HD_WALLET_WASM_PATH="+wallet)
}

// lockedBuffer is a bytes.Buffer two goroutines may use.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPublicationManualRunOutlivesClientRealDaemons runs the CelesTrak GP ingest flow by
// hand on a node whose GP origin answers only after originDelay, so every run
// lasts longer than its client waits (host-01, 2026-11-17: the command gave
// up after about 65 s and the node cancelled the run). Each run ends ok on
// the node after its client stopped waiting:
//   - `apps run --timeout` stops waiting, says the run continues and how to
//     see its result, and exits non-zero; `apps status` then shows the run ok;
//   - `apps run` of a running run joins it, and Ctrl-C (SIGINT) stops waiting
//     and says the run continues;
//   - a raw run-now request whose client gives up (the production shape);
//
// and a running run, joined by `apps run --detach` (which returns at once),
// is cancelled on purpose with `apps cancel`, which ends it as cancelled by
// the operator. A second manual run inside the provider's minimum interval
// is deferred, so the runs after the first are forced.
func TestPublicationManualRunOutlivesClientRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	const originDelay = 20 * time.Second
	d := gpFlowDaemonServing(t, "apps-run", "  first_fire_when_due: false\n", func(fixture []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(originDelay):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "text/csv")
			w.Write(fixture)
		}
	})
	d.start(t)

	// 1. --timeout: the command stops waiting; the run ends ok on the node.
	out, err, took := appsCLI(t, d, "apps", "run", gpFlowAppID, gpFlowTrigger, "--timeout", "4s")
	t.Logf("apps run --timeout 4s (%s, %v):\n%s", took.Round(time.Millisecond), err, out)
	if err == nil {
		t.Fatal("apps run --timeout exited 0 although the run had not ended")
	}
	if took >= originDelay {
		t.Fatalf("apps run --timeout 4s waited %s, as long as the run", took)
	}
	if !strings.Contains(out, "this command stopped waiting (--timeout 4s) and the run continues") ||
		!strings.Contains(out, "spacedatanetwork apps status "+gpFlowAppID+" "+gpFlowTrigger) {
		t.Fatalf("apps run --timeout did not say the run continues and how to see it:\n%s", out)
	}
	if end := awaitManualRunEnd(t, d, 1, 3*time.Minute); !strings.HasPrefix(end, "ok ") {
		t.Fatalf("the run its client stopped waiting for did not end ok: status=%s", end)
	}
	out, err, _ = appsCLI(t, d, "apps", "status", gpFlowAppID, gpFlowTrigger)
	t.Logf("apps status:\n%s", out)
	if err != nil || !strings.Contains(out, gpFlowAppID+" "+gpFlowTrigger+": ok ") {
		t.Fatalf("apps status does not show the run ok (%v):\n%s", err, out)
	}

	// The provider's minimum interval (3 h for a flow loaded by path) defers
	// a second manual run; the runs below are forced ({"force":true}), and
	// the CLI joins them.
	forceStart := func() {
		t.Helper()
		response, err := http.Post(d.base+appRunPath(gpFlowAppID, gpFlowTrigger)+"?detach=1", "application/json", strings.NewReader(`{"force":true}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("a detached forced run answered HTTP %d", response.StatusCode)
		}
	}

	// 2. Ctrl-C: the command stops waiting; the run ends ok on the node.
	forceStart()
	var output lockedBuffer
	cmd := exec.Command(d.exe, "--config", d.cfg, "apps", "run", gpFlowAppID, gpFlowTrigger)
	cmd.Env = appsCLIEnv(t, d)
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for !strings.Contains(output.String(), ": already running since ") && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(output.String(), ": already running since ") {
		_ = cmd.Process.Kill()
		t.Fatalf("apps run did not join the running run:\n%s", output.String())
	}
	time.Sleep(2 * time.Second)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	t.Logf("apps run, interrupted (%v):\n%s", err, output.String())
	if err == nil || !strings.Contains(output.String(), "interrupted; this command stopped waiting and the run continues on the node") {
		t.Fatalf("an interrupted apps run did not say the run continues:\n%s", output.String())
	}
	if end := awaitManualRunEnd(t, d, 2, 3*time.Minute); !strings.HasPrefix(end, "ok ") {
		t.Fatalf("the run whose command was interrupted did not end ok: status=%s", end)
	}

	// 3. A raw run-now request whose client gives up.
	client := http.Client{Timeout: 4 * time.Second}
	started := time.Now()
	response, err := client.Post(d.base+appRunPath(gpFlowAppID, gpFlowTrigger), "application/json", strings.NewReader(`{"force":true}`))
	if err == nil {
		response.Body.Close()
		t.Fatalf("the run-now request answered HTTP %d after %s, before the run could end", response.StatusCode, time.Since(started))
	}
	t.Logf("run-now request with a 4 s client: %v", err)
	if end := awaitManualRunEnd(t, d, 3, 3*time.Minute); !strings.HasPrefix(end, "ok ") {
		t.Fatalf("the run whose client gave up did not end ok: status=%s", end)
	}

	// 4. A run, cancelled on purpose.
	forceStart()
	out, err, took = appsCLI(t, d, "apps", "run", gpFlowAppID, gpFlowTrigger, "--detach")
	t.Logf("apps run --detach (%s, %v):\n%s", took.Round(time.Millisecond), err, out)
	if err != nil || took >= originDelay || !strings.Contains(out, "runs on the node") {
		t.Fatalf("apps run --detach did not return at once with the run running (%v, %s):\n%s", err, took, out)
	}
	out, err, _ = appsCLI(t, d, "apps", "cancel", gpFlowAppID, gpFlowTrigger)
	t.Logf("apps cancel (%v):\n%s", err, out)
	if err != nil || !strings.Contains(out, ": error ") || !strings.Contains(out, "cancelled by the operator") {
		t.Fatalf("apps cancel did not end the run as cancelled (%v):\n%s", err, out)
	}
	if end := awaitManualRunEnd(t, d, 4, time.Minute); !strings.HasPrefix(end, "error ") || !strings.Contains(end, "cancelled by the operator") {
		t.Fatalf("the cancelled run's end: status=%s", end)
	}
}

// TestPublicationAlertClearsWhenLaterRunsPublishItRealDaemons runs the
// CelesTrak GP ingest flow three times, as its schedule does (host-02,
// 2026-11-18: a run's publication failed, later runs ended ok, and the alert
// stayed until a restart). The first run's publication fails and raises the
// lane's publication_failed alert. The second run's pull returns one of the
// first batch's two records: its batch is published, the other record is in
// no publication, and the alert stays, saying so. The third run's pull
// returns both records again (in another order, so another batch): every
// record of the failed batch is now in a completed publication, and the
// alert clears.
func TestPublicationAlertClearsWhenLaterRunsPublishItRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	var phase atomic.Int32
	d := gpFlowDaemonServing(t, "alert-clear", "", func(fixture []byte) http.HandlerFunc {
		lines := strings.Split(strings.TrimRight(string(fixture), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("the GP fixture has %d lines, want a header and two records", len(lines))
		}
		payloads := []string{
			strings.Join(lines, "\n") + "\n",
			lines[0] + "\n" + lines[1] + "\n",
			lines[0] + "\n" + lines[2] + "\n" + lines[1] + "\n",
		}
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/csv")
			w.Write([]byte(payloads[phase.Load()]))
		}
	})
	// The injected failure: a file where the export's shard directory must
	// be (TestPublicationFailureRealDaemons).
	omm := d.root + "/dataset-publications/OMM"
	if err := os.MkdirAll(omm, 0o700); err != nil {
		t.Fatal(err)
	}
	blocker := omm + "/shards"
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.start(t)

	run := publicationAwaitLog(t, d, `Flow service "`+gpFlowAppID+`" first-fire trigger "`+gpFlowTrigger+`" failed: `, 4*time.Minute)
	t.Logf("first run: %s", run)
	failed := publicationLogAfter(t, d, `Dataset publication FAILED: `+gpFlowLane+` batch "`)
	match := regexp.MustCompile(`^([^"]+)", step "export", after `).FindStringSubmatch(failed)
	if match == nil {
		t.Fatalf("the failure names no batch and export step: %s", failed)
	}
	batch := match[1]
	alert := publicationAlert(t, d, gpFlowLane)
	t.Logf("alert after the failed run: %s", alert)
	if !strings.Contains(alert, `batch \"`+batch+`\" is unpublished: 2 of its 2 records are in no completed publication of the lane`) {
		t.Fatalf("the alert does not say the failed batch's two records are unpublished: %s", alert)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}

	manualRun := func(n int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", d.base+appRunPath(gpFlowAppID, gpFlowTrigger), strings.NewReader(`{"force":true}`))
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Status, Message string
		}
		_ = json.NewDecoder(response.Body).Decode(&result)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || result.Status != "ok" {
			t.Fatalf("run %d: HTTP %d, status %q: %s", n, response.StatusCode, result.Status, result.Message)
		}
		done := publicationLogAfter(t, d, `Dataset publication `+gpFlowLane+` batch "`)
		if !regexp.MustCompile(`^[0-9a-f]+" completed in `).MatchString(done) || strings.HasPrefix(done, batch+`"`) {
			t.Fatalf("run %d did not publish a batch of its own: %s", n, done)
		}
		t.Logf("run %d: published batch %s", n, done)
	}

	// The second pull returns one of the failed batch's records.
	phase.Store(1)
	manualRun(2)
	alert = publicationAlert(t, d, gpFlowLane)
	t.Logf("alert after the second run: %s", alert)
	if !strings.Contains(alert, `batch \"`+batch+`\" is unpublished: 1 of its 2 records are in no completed publication of the lane`) {
		t.Fatalf("after a publication that carries one of the failed batch's two records, the alert is not the one record still unpublished: %s", alert)
	}

	// The third pull returns both.
	phase.Store(2)
	manualRun(3)
	if alert := publicationAlert(t, d, gpFlowLane); alert != "" {
		t.Fatalf("every record of the failed batch is published, and the alert is still active: %s", alert)
	}
	cleared := publicationLogAfter(t, d, `every record of failed batch "`+batch+`"`)
	t.Logf("cleared: every record of failed batch %q%s", batch, cleared)
}
