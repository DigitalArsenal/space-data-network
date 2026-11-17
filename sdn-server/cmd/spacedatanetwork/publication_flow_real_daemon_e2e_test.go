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
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// gpFlowLane is the lane the CelesTrak GP ingest flow stores its $OMM batch in
// on a publication test daemon.
const gpFlowLane = "OMM.fbs publication-e2e/celestrak-gp"

// gpFlowDaemon is a producer daemon running the real CelesTrak GP ingest flow
// against a local copy of the parser's fixture. The flow stores a batch and
// POSTs it to the node's own publish route. flows is more of the config's
// flows section ("" for none). It is not started.
func gpFlowDaemon(t *testing.T, name, flows string) *publicationDaemon {
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
	cfg := readFileString(d.cfg) + "flows:\n" + flows + fmt.Sprintf("  services:\n    - flow: %q\n      config:\n        celestrak_gp_url: %q\n        celestrak_provider_id: publication-e2e\n        celestrak_http_timeout_ms: 30000\n        celestrak_publish_url: %q\n",
		dist, origin.URL+"/NORAD/elements/gp.php", ownPublishURL(d))
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
	d := gpFlowDaemon(t, "failure-producer", "")
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

// slowFlowWriteTimeout and slowFlowRequestLimit are the admin server's write
// timeout and the http-request capability's limit on the slow-flow node, far
// shorter than the publication its flow waits for (SDN_ADMIN_WRITE_TIMEOUT,
// SDN_HTTP_REQUEST_MAX_TIMEOUT; 10 and 5 minutes otherwise).
const (
	slowFlowWriteTimeout = 3 * time.Second
	slowFlowRequestLimit = 2 * time.Second
)

// TestPublicationSlowFlowRealDaemons runs the CelesTrak GP ingest flow while
// a 300,000-record publication of the same lane runs, so the node answers
// the flow's publication request only after that publication: later than the
// admin server's write timeout and the capability's limit on any other
// request. The flow waits for the node's answer: its run ends without error,
// and its batch's publication is complete.
func TestPublicationSlowFlowRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	// The flow runs when the test asks (manual run), not at start.
	d := gpFlowDaemon(t, "slow-producer", "  first_fire_when_due: false\n")
	d.env = append(d.env, "SDN_ADMIN_WRITE_TIMEOUT="+slowFlowWriteTimeout.String(), "SDN_HTTP_REQUEST_MAX_TIMEOUT="+slowFlowRequestLimit.String())
	d.start(t)

	const n, per = 300_000, 5_000
	for offset := 0; offset < n; offset += per {
		d.ingestOMMCIDs(t, "celestrak-gp", "big", 2_000_000+offset, per)
	}
	// The big publication holds the lane; its own answer also comes after the
	// write timeout, and is read in the background.
	bigDone := make(chan string, 1)
	go func() {
		client := http.Client{}
		response, err := client.Post(ownPublishURL(d), "application/json", bytes.NewReader(batchPublicationRequest("celestrak-gp", "big")))
		if err != nil {
			bigDone <- err.Error()
			return
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		bigDone <- fmt.Sprintf("HTTP %d: %.200s", response.StatusCode, body)
	}()
	publicationAwaitLog(t, d, "publication fingerprint OMM.fbs publication-e2e/celestrak-gp/big: ", 2*time.Minute)

	runDone := make(chan string, 1)
	go func() {
		client := http.Client{}
		response, err := client.Post(d.base+"/api/v1/modules/runtime/com.digitalarsenal.flows.celestrak-gp-ingest/schedules/timer-gp/run", "application/json", nil)
		if err != nil {
			runDone <- err.Error()
			return
		}
		response.Body.Close()
		runDone <- fmt.Sprintf("HTTP %d", response.StatusCode)
	}()
	end := `Plugin "com.digitalarsenal.flows.celestrak-gp-ingest" manual run "timer-gp" end status=`
	run := publicationAwaitLog(t, d, end, 5*time.Minute)
	t.Logf("flow run: status=%s", run)
	if !strings.HasPrefix(run, "ok ") {
		t.Fatalf("the flow run did not end ok: status=%s", run)
	}
	done := publicationLogAfter(t, d, `Dataset publication `+gpFlowLane+` batch "`)
	match := regexp.MustCompile(`^([0-9a-f]+)" completed in ([0-9.]+[a-zµ]+):`).FindStringSubmatch(done)
	if match == nil || match[1] == "big" {
		t.Fatalf("the flow's batch publication did not complete last: %s", done)
	}
	took, err := time.ParseDuration(match[2])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("producer: the flow's batch %s completed in %s (write timeout %s, request limit %s)", match[1], took, slowFlowWriteTimeout, slowFlowRequestLimit)
	if took <= slowFlowWriteTimeout || took <= slowFlowRequestLimit {
		t.Fatalf("the node answered the flow after %s, within the write timeout %s or the request limit %s: the test proves nothing", took, slowFlowWriteTimeout, slowFlowRequestLimit)
	}
	if alert := publicationAlert(t, d, gpFlowLane); alert != "" {
		t.Fatalf("a publication_failed alert after the run: %s", alert)
	}
	select {
	case big := <-bigDone:
		t.Logf("the big publication's own answer: %s", big)
	case <-time.After(2 * time.Minute):
		t.Fatal("the big publication did not answer")
	}
	<-runDone
}
