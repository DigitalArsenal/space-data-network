package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// awaitPublicationAlert waits for the lane's publication_failed alert to
// contain want (the record counts are restated after the publication is
// reported, off its path) and returns it.
func awaitPublicationAlert(t *testing.T, d *publicationDaemon, lane, want string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	alert := ""
	for time.Now().Before(deadline) {
		if alert = publicationAlert(t, d, lane); strings.Contains(alert, want) {
			return alert
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the publication_failed alert of %s does not say %q within %s: %q", lane, want, budget, alert)
	return ""
}

// awaitPublicationAlertCleared waits for the lane's publication_failed alert
// to clear.
func awaitPublicationAlertCleared(t *testing.T, d *publicationDaemon, lane string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	alert := ""
	for time.Now().Before(deadline) {
		if alert = publicationAlert(t, d, lane); alert == "" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the publication_failed alert of %s is still active after %s: %s", lane, budget, alert)
}

// TestPublicationAlertPartialFailureRealDaemons fails a batch's publication
// after it wrote publication rows (SDN_E2E_PUBLICATION_FAULT): at the
// announcement of its second window (the first window is published and
// announced, the second written), and at the recording of its completion
// (every window written and announced). Neither publication completed, so
// none of the batch's records is in a completed publication:
//   - the alert says no publication of the whole batch completed;
//   - a publication of another batch carrying one of them completes; the
//     rows the failed publication left are not adopted as a series of their
//     own (retention lists no "legacy:" series of the batch), and the alert
//     stays as it was (it cleared here when such rows counted as a
//     publication of the batch);
//   - another batch with the same three records whose publication fails at
//     its first announcement is unpublished too;
//   - a publication of a batch carrying all three completes, and both
//     failed batches stay (another batch's publication counts for nothing;
//     it cleared the alert here when it did);
//   - a publication of each failed batch whole clears it.
func TestPublicationAlertPartialFailureRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	exe := publicationBinary(t)
	for _, c := range []struct{ name, fault, step string }{
		{"announce", "announce@2", "announce"},
		{"record-completion", "record completion", "record completion"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newPublicationDaemon(t, exe, "partial-"+c.name, "", "", "keep-all")
			d.env = append(d.env, "SDN_E2E_PUBLICATION_FAULT="+c.fault)
			d.start(t)
			source := "partial-" + c.name
			lane := "OMM.fbs publication-e2e/" + source

			// The failed batch: three records, one per window.
			frames := [][]byte{baselineOMM(0), baselineOMM(1), baselineOMM(2)}
			failedCIDs := d.ingestOMMFrames(t, source, "failed", 0, frames)
			body, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": "failed", "chunkSize": 1})
			answer := d.request(t, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", body, http.StatusBadRequest)
			t.Logf("the failed publication answered: %s", answer)
			failed := publicationLogAfter(t, d, `Dataset publication FAILED: `+lane+` batch "failed", step "`)
			if !strings.HasPrefix(failed, c.step+`"`) {
				t.Fatalf("the publication did not fail at step %q: %s", c.step, failed)
			}
			failedText := `batch \"failed\" is unpublished: no publication of the whole batch completed; it holds 3 records`
			alert := awaitPublicationAlert(t, d, lane, failedText, time.Minute)
			t.Logf("alert after the failure: %s", alert)

			// Another batch carrying one of the three records.
			if one := d.ingestOMMFrames(t, source, "one", 0, frames[:1]); one[0] != failedCIDs[0] {
				t.Fatalf("the record of batch \"one\" is %s, not the failed batch's %s", one[0], failedCIDs[0])
			}
			if pub := d.publish(t, "OMM.fbs", source, "one"); pub.RecordCount != 1 {
				t.Fatalf("the publication of the batch carrying one record answered %d records", pub.RecordCount)
			}
			alert = alertStays(t, d, lane, failedText, 10*time.Second)
			t.Logf("alert after the publication of one record: %s", alert)

			// Another failed batch with the same three records, failing at
			// its first window's announcement (an announcement schema that
			// is not one): the rows the first failed publication left are no
			// publication of it.
			d.ingestOMMFrames(t, source, "again", 0, frames)
			again, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": "again", "chunkSize": 1, "announcementSchemas": []string{"not a schema"}})
			answer = d.request(t, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", again, http.StatusBadRequest)
			t.Logf("the second failed publication answered: %s", answer)
			if failed := publicationLogAfter(t, d, `Dataset publication FAILED: `+lane+` batch "again", step "`); !strings.HasPrefix(failed, `announce"`) {
				t.Fatalf("the second publication did not fail at its announcement: %s", failed)
			}
			againText := `batch \"again\" is unpublished: no publication of the whole batch completed; it holds 3 records`
			alert = awaitPublicationAlert(t, d, lane, againText, time.Minute)
			t.Logf("alert after the second failure: %s", alert)

			// No series is made of the rows the failed publications left.
			keep := 10
			retention, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "keepSeries": keep})
			raw := d.request(t, "POST", "/api/v1/admin/dataset-updates/retention", "application/json", retention, http.StatusOK)
			var plan struct {
				KeptSeries    []string `json:"keptSeries"`
				RetiredSeries []string `json:"retiredSeries"`
			}
			if err := json.Unmarshal(raw, &plan); err != nil {
				t.Fatal(err)
			}
			t.Logf("retention dry run: kept %v, retired %v", plan.KeptSeries, plan.RetiredSeries)
			for _, series := range append(plan.KeptSeries, plan.RetiredSeries...) {
				if strings.HasPrefix(series, "legacy:") {
					t.Fatalf("the rows of a failed publication are a series of their own: %s", raw)
				}
			}

			// A batch carrying all three: another batch, so both failed
			// batches stay.
			d.ingestOMMFrames(t, source, "all", 0, frames)
			if pub := d.publish(t, "OMM.fbs", source, "all"); pub.RecordCount != 3 {
				t.Fatalf("the publication of the batch carrying all three records answered %d records", pub.RecordCount)
			}
			alert = alertStays(t, d, lane, failedText, 10*time.Second)
			if !strings.Contains(alert, againText) {
				t.Fatalf("the alert does not say %q after the publication of \"all\": %s", againText, alert)
			}
			t.Logf("alert after the publication of all three records: %s", alert)

			// Each failed batch whole.
			for _, batch := range []string{"failed", "again"} {
				if pub := d.publish(t, "OMM.fbs", source, batch); pub.RecordCount != 3 {
					t.Fatalf("the publication of the whole of %q answered %d records", batch, pub.RecordCount)
				}
			}
			awaitPublicationAlertCleared(t, d, lane, time.Minute)
			t.Logf("cleared: %s", publicationLogAfter(t, d, `failed scope "publish batch" of batch "again" is published`))
		})
	}
}

// TestPublicationManualRunStopsWithNodeRealDaemons stops a node while a
// run-now request waits for a manual run whose GP origin answers only after
// two minutes. The node cancels the run and answers the request before its
// HTTP servers shut down: the request is answered within seconds of the
// stop, saying the node is stopping, and the admin server's shutdown is not
// held to its bound. Before that, a non-forced manual run cancelled by the
// operator leaves the provider's minimum interval free: the next non-forced
// manual run starts.
func TestPublicationManualRunStopsWithNodeRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	const originDelay = 2 * time.Minute
	d := gpFlowDaemonServing(t, "manual-stop", "  first_fire_when_due: false\n", func(fixture []byte) http.HandlerFunc {
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

	// A non-forced run, cancelled: the next non-forced run is not deferred.
	out, err, _ := appsCLI(t, d, "apps", "run", gpFlowAppID, gpFlowTrigger, "--detach")
	t.Logf("apps run --detach (%v):\n%s", err, out)
	if err != nil || !strings.Contains(out, ": started ") {
		t.Fatalf("the first manual run did not start (%v):\n%s", err, out)
	}
	out, err, _ = appsCLI(t, d, "apps", "cancel", gpFlowAppID, gpFlowTrigger)
	t.Logf("apps cancel (%v):\n%s", err, out)
	if end := awaitManualRunEnd(t, d, 1, time.Minute); !strings.Contains(end, "cancelled by the operator") {
		t.Fatalf("the cancelled run's end: status=%s", end)
	}
	out, err, _ = appsCLI(t, d, "apps", "run", gpFlowAppID, gpFlowTrigger, "--detach")
	t.Logf("apps run --detach after the cancel (%v):\n%s", err, out)
	if err != nil || !strings.Contains(out, ": started ") || !strings.Contains(out, "runs on the node") {
		t.Fatalf("the manual run after a cancelled one did not start (%v):\n%s", err, out)
	}
	out, _, _ = appsCLI(t, d, "apps", "status", gpFlowAppID, gpFlowTrigger)
	if !strings.Contains(out, ": running since ") {
		t.Fatalf("the manual run after a cancelled one is not running (the provider's interval deferred it?):\n%s", out)
	}

	// A run-now request waiting for that run, and the node's stop.
	type answer struct {
		status int
		body   string
		err    error
		at     time.Time
	}
	answered := make(chan answer, 1)
	go func() {
		client := http.Client{Timeout: 5 * time.Minute}
		response, err := client.Post(d.base+appRunPath(gpFlowAppID, gpFlowTrigger), "application/json", strings.NewReader(`{"force":true}`))
		if err != nil {
			answered <- answer{err: err, at: time.Now()}
			return
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		answered <- answer{status: response.StatusCode, body: string(raw), at: time.Now()}
	}()
	// The request joins the running run.
	publicationAwaitLog(t, d, `is already running (started `, time.Minute)
	time.Sleep(2 * time.Second)
	stopping := time.Now()
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var got answer
	select {
	case got = <-answered:
	case <-time.After(3 * time.Minute):
		t.Fatal("the run-now request was not answered within 3 minutes of the stop")
	}
	select {
	case <-d.done:
	case <-time.After(3 * time.Minute):
		t.Fatal("the node did not exit within 3 minutes of the stop")
	}
	d.cmd = nil
	took := got.at.Sub(stopping)
	t.Logf("the run-now request was answered %s after the stop: HTTP %d %v %s", took.Round(time.Millisecond), got.status, got.err, got.body)
	if got.err != nil || !strings.Contains(got.body, "the node is stopping") {
		t.Fatalf("the run-now request was not answered that the node is stopping (HTTP %d, %v): %s", got.status, got.err, got.body)
	}
	if took >= 10*time.Second {
		t.Fatalf("the run-now request was answered %s after the stop, not before the HTTP shutdown", took)
	}
	if strings.Contains(readFileString(d.logPath), "admin server graceful shutdown exceeded") {
		t.Fatalf("the admin server's shutdown was held to its bound:\n%s", publicationLogAfter(t, d, "admin server graceful shutdown exceeded"))
	}
	t.Logf("manual runs at the stop: %s", publicationLogAfter(t, d, "manual run(s) "))
}

// TestPublicationManualRunBoundRealDaemons runs the GP flow by hand on a
// node whose service has execution_timeout 6s and whose GP origin answers
// after 30 s: the manual run ends at its bound, saying so.
func TestPublicationManualRunBoundRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := gpFlowDaemonServing(t, "manual-bound", "  first_fire_when_due: false\n", func(fixture []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(30 * time.Second):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "text/csv")
			w.Write(fixture)
		}
	})
	cfg := readFileString(d.cfg)
	if !strings.Contains(cfg, "    - flow: ") {
		t.Fatalf("the GP service is not in the config:\n%s", cfg)
	}
	cfg = strings.Replace(cfg, "    - flow: ", "    - execution_timeout: 6s\n      flow: ", 1)
	if err := os.WriteFile(d.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	d.start(t)
	started := time.Now()
	out, err, _ := appsCLI(t, d, "apps", "run", gpFlowAppID, gpFlowTrigger, "--timeout", "2m")
	took := time.Since(started)
	t.Logf("apps run (%s, %v):\n%s", took.Round(time.Millisecond), err, out)
	if err == nil || !strings.Contains(out, "the manual run's bound of 6s passed") {
		t.Fatalf("the manual run did not end at its bound of 6s (%v):\n%s", err, out)
	}
	if took >= 30*time.Second {
		t.Fatalf("the manual run took %s, as long as the origin", took)
	}
}

// TestPublicationManualRunCancelByIDRealDaemons starts a manual run, cancels
// it, and starts another, on a node whose GP origin answers after two
// minutes. A cancel that names the first run (as `apps run
// --cancel-on-interrupt` names the run it follows) is answered 404 and
// leaves the second run running (it cancelled the second run here); a
// cancel that names the second run, and a cancel naming no run, cancel it.
func TestPublicationManualRunCancelByIDRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := gpFlowDaemonServing(t, "manual-cancel-id", "  first_fire_when_due: false\n", func(fixture []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(2 * time.Minute):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "text/csv")
			w.Write(fixture)
		}
	})
	d.start(t)
	path := appRunPath(gpFlowAppID, gpFlowTrigger)
	start := func() string {
		t.Helper()
		var run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		raw := d.request(t, "POST", path+"?detach=1", "application/json", nil, http.StatusAccepted)
		if err := json.Unmarshal(raw, &run); err != nil || run.ID == "" || run.Status != "running" {
			t.Fatalf("the manual run did not start (%v): %s", err, raw)
		}
		return run.ID
	}
	state := func(id string) string {
		t.Helper()
		var run struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		raw := d.request(t, "GET", path+"?id="+id, "", nil, http.StatusOK)
		if err := json.Unmarshal(raw, &run); err != nil {
			t.Fatal(err)
		}
		return run.Status + " " + string(raw)
	}

	first := start()
	d.request(t, "DELETE", path+"?id="+first, "", nil, http.StatusOK)
	if end := awaitManualRunEnd(t, d, 1, time.Minute); !strings.Contains(end, "cancelled by the operator") {
		t.Fatalf("the first run's end: status=%s", end)
	}
	second := start()
	t.Logf("first run %s (cancelled), second run %s", first, second)
	if now := state(second); !strings.HasPrefix(now, "running") {
		t.Fatalf("the second run is not running before any cancel names a run: %s", now)
	}

	status, body := postStatus(t, d, "DELETE", path+"?id="+first, nil)
	t.Logf("cancel naming the first run: HTTP %d %s", status, strings.TrimSpace(body))
	if now := state(second); !strings.HasPrefix(now, "running") {
		t.Fatalf("a cancel naming the ended first run ended the second run: %s", now)
	}
	if status != http.StatusNotFound {
		t.Fatalf("a cancel naming the ended first run answered HTTP %d, want 404: %s", status, body)
	}

	d.request(t, "DELETE", path+"?id="+second, "", nil, http.StatusOK)
	if end := awaitManualRunEnd(t, d, 2, time.Minute); !strings.Contains(end, "cancelled by the operator") {
		t.Fatalf("the second run's end: status=%s", end)
	}
	third := start()
	out, err, _ := appsCLI(t, d, "apps", "cancel", gpFlowAppID, gpFlowTrigger)
	t.Logf("apps cancel (%v):\n%s", err, out)
	if err != nil {
		t.Fatalf("apps cancel failed: %v", err)
	}
	if end := awaitManualRunEnd(t, d, 3, time.Minute); !strings.Contains(end, "cancelled by the operator") {
		t.Fatalf("the third run (%s) after apps cancel: status=%s", third, end)
	}
}
