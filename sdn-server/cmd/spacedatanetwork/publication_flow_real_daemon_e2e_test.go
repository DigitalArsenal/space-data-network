package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// gpFlowLane is the lane the CelesTrak GP ingest flow stores its $OMM batch in
// on a publication test daemon.
const gpFlowLane = "OMM.fbs publication-e2e/celestrak-gp"

// gpFlowDaemon is a producer daemon running the real CelesTrak GP ingest flow
// against a local copy of the parser's fixture. The flow stores a batch and
// POSTs it to publishURL(d), the node's own publish route or a stand-in for
// it. It is not started.
func gpFlowDaemon(t *testing.T, name string, publishURL func(d *publicationDaemon) string) *publicationDaemon {
	t.Helper()
	modules := os.Getenv("SDN_E2E_MODULES")
	required := modules != ""
	if !required {
		modules = filepath.Join("..", "..", "..", "..", "space-data-network-modules")
	}
	missing := func(format string, args ...any) {
		t.Helper()
		if required {
			t.Fatalf(format, args...)
		}
		t.Skipf(format+" (set SDN_E2E_MODULES)", args...)
	}
	dist, err := filepath.Abs(filepath.Join(modules, "flows", "celestrak-ingest", "dist", "gp"))
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(dist, "runtime.wasm"))
	if err != nil {
		missing("CelesTrak GP ingest flow not found under %s: %v", modules, err)
	}
	fixture, err := os.ReadFile(filepath.Join(modules, "data-source", "celestrak-parser", "tests", "fixtures", "celestrak-gp-omm.csv"))
	if err != nil {
		missing("CelesTrak parser fixture not found under %s: %v", modules, err)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.Write(fixture)
	}))
	t.Cleanup(origin.Close)

	d := newPublicationDaemon(t, publicationBinary(t), name, "", "", "keep-all")
	// The operator's approval of the flow's capabilities.
	hash := modulert.ContentHashHex(modulert.StripPublicationTrailer(wasm))
	policy, _ := json.Marshal(map[string]any{"version": 1, "approvals": []map[string]string{
		{"module_hash": hash, "capability": "http"},
		{"module_hash": hash, "capability": "storage_ingest"},
	}})
	policyPath := filepath.Join(d.root, "capability_policy.json")
	if err := os.WriteFile(policyPath, policy, 0o600); err != nil {
		t.Fatal(err)
	}
	d.env = append(d.env, "SDN_MODULE_CAPABILITY_POLICY_PATH="+policyPath)
	cfg := readFileString(d.cfg) + fmt.Sprintf("flows:\n  services:\n    - flow: %q\n      config:\n        celestrak_gp_url: %q\n        celestrak_provider_id: publication-e2e\n        celestrak_http_timeout_ms: 30000\n        celestrak_publish_url: %q\n",
		dist, origin.URL+"/NORAD/elements/gp.php", publishURL(d))
	if err := os.WriteFile(d.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func ownPublishURL(d *publicationDaemon) string {
	return d.base + "/api/v1/admin/dataset-updates/publish"
}

// TestPublicationFailureRealDaemons runs the CelesTrak GP ingest flow, which
// stores a batch and asks the node to publish it, on a node whose
// publication export cannot write. The failure is logged as an error naming
// the lane, the batch and the step, raises the lane's publication_failed
// alert, and the flow's run ends with an error. A publication of another
// batch of the lane and an announcement of the lane's existing publications
// complete without clearing the alert: neither publishes the failed batch.
// Once the export can write, a publication of the failed batch completes and
// clears it.
func TestPublicationFailureRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	d := gpFlowDaemon(t, "failure-producer", ownPublishURL)
	// The injected failure: a file where the export's shard directory must
	// be, which stops its creation for any user, root included.
	omm := filepath.Join(d.root, "dataset-publications", "OMM")
	if err := os.MkdirAll(omm, 0o700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(omm, "shards")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.start(t)

	run := publicationAwaitLog(t, d, `Flow service "com.digitalarsenal.flows.celestrak-gp-ingest" first-fire trigger "timer-gp" failed: `, 4*time.Minute)
	if !strings.Contains(run, "dataset publication request to "+ownPublishURL(d)+" answered HTTP 400") {
		t.Fatalf("the flow run failed, but not for its publication: %s", run)
	}
	t.Logf("flow run: %s", run)
	failed := publicationLogAfter(t, d, `Dataset publication FAILED: `+gpFlowLane+` batch "`)
	match := regexp.MustCompile(`^([^"]+)", step "export", after `).FindStringSubmatch(failed)
	if match == nil {
		t.Fatalf("the failure names no batch and export step: %s", failed)
	}
	batch := match[1]
	t.Logf("producer: FAILED: batch %s", failed)
	alert := publicationAlert(t, d, gpFlowLane)
	if alert == "" {
		t.Fatalf("no publication_failed alert for %s", gpFlowLane)
	}
	t.Logf("alert: %s", alert)

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	// Another batch of the lane, and an announcement of the lane's existing
	// publications: both complete, neither publishes the failed batch.
	d.ingestOMMCIDs(t, "celestrak-gp", "another", 900_000, 20)
	raw := d.request(t, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", batchPublicationRequest("celestrak-gp", "another"), http.StatusAccepted)
	var other publicationResult
	if err := json.Unmarshal(raw, &other); err != nil {
		t.Fatal(err)
	}
	if other.ManifestCID == "" || other.RecordCount != 20 {
		t.Fatalf("the publication of another batch answered %d records, manifest %q", other.RecordCount, other.ManifestCID)
	}
	if publicationAlert(t, d, gpFlowLane) == "" {
		t.Fatal("a publication of another batch cleared the alert of the failed batch")
	}
	announce, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": "celestrak-gp", "announceExisting": true})
	d.request(t, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", announce, http.StatusAccepted)
	publicationLogAfter(t, d, `Dataset publication `+gpFlowLane+` batch "" completed in `)
	if publicationAlert(t, d, gpFlowLane) == "" {
		t.Fatal("an announcement of existing publications cleared the alert of the failed batch")
	}

	raw = d.request(t, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", batchPublicationRequest("celestrak-gp", batch), http.StatusAccepted)
	var pub publicationResult
	if err := json.Unmarshal(raw, &pub); err != nil {
		t.Fatal(err)
	}
	if pub.ManifestCID == "" || pub.RecordCount == 0 {
		t.Fatalf("the publication after the fix answered %d records, manifest %q", pub.RecordCount, pub.ManifestCID)
	}
	if alert := publicationAlert(t, d, gpFlowLane); alert != "" {
		t.Fatalf("the alert is still active after the failed batch was published: %s", alert)
	}
}

// slowPublicationAnswer is longer than the http-request capability's cap on
// any other request (five minutes).
const slowPublicationAnswer = 5*time.Minute + 20*time.Second

// TestPublicationSlowFlowRealDaemons runs the CelesTrak GP ingest flow on a
// node whose publication answers its first request after
// slowPublicationAnswer: the flow's publish URL is a loopback stand-in for the
// node's publish route that forwards the request and holds the node's answer
// that long. The flow waits for the real answer: its run ends without error,
// and the publication of the batch is complete.
func TestPublicationSlowFlowRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	var node atomic.Pointer[publicationDaemon]
	var first atomic.Bool
	var answered atomic.Value
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived := time.Now()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		forward, err := http.NewRequest(r.Method, ownPublishURL(node.Load()), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		forward.Header = r.Header.Clone()
		response, err := http.DefaultClient.Do(forward)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		answer, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if first.CompareAndSwap(false, true) {
			answered.Store(fmt.Sprintf("HTTP %d after %s: %s", response.StatusCode, time.Since(arrived).Round(time.Millisecond), answer))
			time.Sleep(time.Until(arrived.Add(slowPublicationAnswer)))
		}
		for k, v := range response.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(response.StatusCode)
		w.Write(answer)
	}))
	defer slow.Close()
	d := gpFlowDaemon(t, "slow-producer", func(*publicationDaemon) string {
		return slow.URL + "/api/v1/admin/dataset-updates/publish"
	})
	node.Store(d)
	d.start(t)

	ended := time.Now()
	completed := `Flow service "com.digitalarsenal.flows.celestrak-gp-ingest" first-fire trigger "timer-gp" completed`
	failedRun := `Flow service "com.digitalarsenal.flows.celestrak-gp-ingest" first-fire trigger "timer-gp" failed: `
	deadline := time.Now().Add(slowPublicationAnswer + 5*time.Minute)
	for {
		text := readFileString(d.logPath)
		if strings.Contains(text, failedRun) {
			t.Fatalf("the flow run failed: %s", publicationLogAfter(t, d, failedRun))
		}
		if strings.Contains(text, completed) {
			ended = time.Now()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the flow run did not end within %s", slowPublicationAnswer+5*time.Minute)
		}
		time.Sleep(time.Second)
	}
	held, _ := answered.Load().(string)
	if held == "" {
		t.Fatal("the flow made no publication request")
	}
	t.Logf("the node answered the first publication request: %s; the flow received it after %s; the run ended ok at %s",
		held, slowPublicationAnswer, ended.Format(time.RFC3339))
	if !strings.HasPrefix(held, "HTTP 202 ") {
		t.Fatalf("the node's answer was not a completed publication: %s", held)
	}
	done := publicationLogAfter(t, d, `Dataset publication `+gpFlowLane+` batch "`)
	if !strings.Contains(done, `" completed in `) {
		t.Fatalf("the lane's publication did not complete: %s", done)
	}
	t.Logf("producer: %s", done)
	if alert := publicationAlert(t, d, gpFlowLane); alert != "" {
		t.Fatalf("a publication_failed alert after the run: %s", alert)
	}
}
