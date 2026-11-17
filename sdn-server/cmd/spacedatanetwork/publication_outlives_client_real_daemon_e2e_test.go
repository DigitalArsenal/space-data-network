package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// publicationClientDeadline is far shorter than the publication of the
// test's batches takes.
const publicationClientDeadline = 300 * time.Millisecond

// TestPublicationOutlivesClientRealDaemons publishes a 150,000-record batch
// for a client that gives up after 300 ms, as a flow's HTTP request gives up
// at its deadline. The publication completes anyway and the receiver holds
// the batch. A second request for a batch whose publication is running waits
// for that publication's result instead of starting another. Shard and index
// files no publication names (what a publication that never completed left)
// are removed once a publication completes; the published ones stay.
func TestPublicationOutlivesClientRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "outlives-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "outlives-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)

	const n, per = 150_000, 5_000
	for offset := 0; offset < n; offset += per {
		a.ingestOMMCIDs(t, "outlives", "first", 40_000+offset, per)
	}
	want := a.scan(t, "OMM.fbs", "outlives")
	if len(want) != n {
		t.Fatalf("producer holds %d records, want %d", len(want), n)
	}

	// Files of a publication that never completed.
	omm := filepath.Join(a.root, "dataset-publications", "OMM")
	orphans := []string{
		filepath.Join(omm, "shards", "OMM-0-50000-00000000000000aa.fbshard"),
		filepath.Join(omm, "indexes", "OMM-0-50000-00000000000000bb.index.fb"),
	}
	for _, orphan := range orphans {
		if err := os.MkdirAll(filepath.Dir(orphan), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(orphan, []byte("left by a publication that did not complete"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(3 * time.Second)

	if status, err := publishWithClientDeadline(a, "outlives", "first", publicationClientDeadline); err == nil {
		t.Fatalf("the publication answered HTTP %d within %s: the batch is too small for this test", status, publicationClientDeadline)
	}
	t.Logf("the client gave up after %s", publicationClientDeadline)
	b.awaitRecords(t, "OMM.fbs", "outlives", want, 6*time.Minute)
	completed := publicationLogAfter(t, a, `Dataset publication OMM.fbs publication-e2e/outlives batch "first" completed in `)
	t.Logf("producer: completed in %s", completed)
	for _, orphan := range orphans {
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatalf("%s is still there after the publication completed (%v)", orphan, err)
		}
	}
	shards, _ := filepath.Glob(filepath.Join(omm, "shards", "*.fbshard"))
	if len(shards) != 3 {
		t.Fatalf("%d shard files after the publication, want its 3: %v", len(shards), shards)
	}

	// A second request while the publication runs waits for it.
	for offset := 0; offset < n; offset += per {
		a.ingestOMMCIDs(t, "outlives", "second", 240_000+offset, per)
	}
	if status, err := publishWithClientDeadline(a, "outlives", "second", publicationClientDeadline); err == nil {
		t.Fatalf("the publication answered HTTP %d within %s: the batch is too small for this test", status, publicationClientDeadline)
	}
	raw := a.request(t, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", batchPublicationRequest("outlives", "second"), http.StatusAccepted)
	var pub publicationResult
	if err := json.Unmarshal(raw, &pub); err != nil {
		t.Fatal(err)
	}
	if pub.ManifestCID == "" || pub.RecordCount != n {
		t.Fatalf("the second request answered %d records, manifest %q; want %d and a manifest", pub.RecordCount, pub.ManifestCID, n)
	}
	if runs := strings.Count(readFileString(a.logPath), "publication fingerprint OMM.fbs publication-e2e/outlives/second: "); runs != 1 {
		t.Fatalf("batch second was read for publication %d times, want once", runs)
	}
	if !strings.Contains(readFileString(a.logPath), `Dataset publication OMM.fbs publication-e2e/outlives batch "second" is already running`) {
		t.Fatal("the second request did not wait for the running publication")
	}
	b.awaitRecords(t, "OMM.fbs", "outlives", a.scan(t, "OMM.fbs", "outlives"), 6*time.Minute)
}

// batchPublicationRequest is the request a flow's IngestResult makes: one
// stored batch, every other field at its default.
func batchPublicationRequest(source, batch string) []byte {
	body, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": batch})
	return body
}

// publishWithClientDeadline sends a batch publication with a client that
// gives up after deadline.
func publishWithClientDeadline(d *publicationDaemon, source, batch string, deadline time.Duration) (int, error) {
	client := http.Client{Timeout: deadline}
	response, err := client.Post(d.base+"/api/v1/admin/dataset-updates/publish", "application/json", bytes.NewReader(batchPublicationRequest(source, batch)))
	if err != nil {
		return 0, err
	}
	response.Body.Close()
	return response.StatusCode, nil
}

// publicationLogAfter is the text after the last occurrence of marker in the
// daemon's log, to the end of its line.
func publicationLogAfter(t *testing.T, d *publicationDaemon, marker string) string {
	t.Helper()
	text := readFileString(d.logPath)
	i := strings.LastIndex(text, marker)
	if i < 0 {
		t.Fatalf("no %q in %s", marker, d.logPath)
	}
	rest := text[i+len(marker):]
	if end := strings.IndexByte(rest, '\n'); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// publicationAwaitLog waits for marker in the daemon's log and returns the
// rest of its line.
func publicationAwaitLog(t *testing.T, d *publicationDaemon, marker string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if strings.Contains(readFileString(d.logPath), marker) {
			return publicationLogAfter(t, d, marker)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("no %q in %s within %s", marker, d.logPath, budget)
	return ""
}

// publicationAlert is the active publication_failed alert of a lane, as
// JSON, or "".
func publicationAlert(t *testing.T, d *publicationDaemon, lane string) string {
	t.Helper()
	var snapshot struct {
		Alerts []map[string]any `json:"alerts"`
	}
	if err := json.Unmarshal(d.request(t, "GET", "/api/v1/status/alerts", "", nil, http.StatusOK), &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, alert := range snapshot.Alerts {
		if alert["kind"] == "publication_failed" && alert["subject"] == lane {
			raw, _ := json.Marshal(alert)
			return string(raw)
		}
	}
	return ""
}
