package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// postStatus sends a JSON request and answers its HTTP status and body,
// without judging them.
func postStatus(t *testing.T, d *publicationDaemon, method, path string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, d.base+path, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: 5 * time.Minute}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

// TestPublicationAlertBatchGainedRecordsRealDaemons publishes batch "y"
// (two records), stores a third record in it, and fails its republication
// at the announcement of its third window (SDN_E2E_PUBLICATION_FAULT): the
// third record is in no completed publication. Batch "x" holds that third
// record too and its publication fails. A completed series of "y" exists,
// but it exported two records and "y" holds three now, so:
//   - neither "y" nor "x" is published: the alert says 1 of the 3 records
//     of "y" arrived after its last completed publication, and that no
//     publication of the whole of "x" (1 record) completed;
//   - an announcement of the existing publications of "y" announces the
//     window of part one's completed publication only, not the windows the
//     failed republication wrote;
//   - the publication of an unrelated batch "z" completes and the alert
//     stays, saying the same (it cleared here when any completed series of
//     a batch counted, whatever the batch held since);
//   - the publication of batch "w", holding the two records of part one,
//     completes, and the alert stays, saying the same: another batch's
//     publication counts for nothing;
//   - a publication of the whole of "y" as it is now removes "y", and the
//     alert stays naming "x", whose record "y" holds (another batch);
//   - a publication of the whole of "x" clears it.
func TestPublicationAlertBatchGainedRecordsRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := newPublicationDaemon(t, publicationBinary(t), "gained-records", "", "", "keep-all")
	d.env = append(d.env, "SDN_E2E_PUBLICATION_FAULT=announce@3")
	d.start(t)
	source := "gained-records"
	lane := "OMM.fbs publication-e2e/" + source
	frames := [][]byte{baselineOMM(0), baselineOMM(1), baselineOMM(2), baselineOMM(3)}

	// "y", part one, published.
	d.ingestOMMFrames(t, source, "y", 0, frames[:2])
	if pub := d.publish(t, "OMM.fbs", source, "y"); pub.RecordCount != 2 {
		t.Fatalf("the publication of part one of \"y\" answered %d records", pub.RecordCount)
	}
	// "y", part two, and its failed republication.
	d.ingestOMMFrames(t, source, "y", 0, frames[2:3])
	body, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": "y", "chunkSize": 1})
	status, answer := postStatus(t, d, "POST", "/api/v1/admin/dataset-updates/publish", body)
	t.Logf("the republication of \"y\" answered HTTP %d: %s", status, answer)
	if failed := publicationAwaitLog(t, d, `Dataset publication FAILED: `+lane+` batch "y", step "`, time.Minute); !strings.HasPrefix(failed, `announce"`) {
		t.Fatalf("the republication of \"y\" did not fail at its announcement: %s", failed)
	}
	t.Logf("check of \"y\": %s", publicationAwaitLog(t, d, `Failed batch check `+lane+`: fingerprinted batch "y"`, time.Minute))

	// "x", holding the record part two added to "y", fails.
	d.ingestOMMFrames(t, source, "x", 0, frames[2:3])
	bad, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": "x", "announcementSchemas": []string{"not a schema"}})
	status, answer = postStatus(t, d, "POST", "/api/v1/admin/dataset-updates/publish", bad)
	t.Logf("the publication of \"x\" answered HTTP %d: %s", status, answer)
	publicationAwaitLog(t, d, `Dataset publication FAILED: `+lane+` batch "x", step "`, time.Minute)
	t.Logf("check of \"x\": %s", publicationAwaitLog(t, d, `Failed batch check `+lane+`: fingerprinted batch "x"`, time.Minute))

	// The existing publications of "y": the window of part one's completed
	// publication (2 records) and the three windows the failed
	// republication wrote (1 record each).
	announce, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": "y", "announceExisting": true})
	status, answer = postStatus(t, d, "POST", "/api/v1/admin/dataset-updates/publish", announce)
	t.Logf("the announcement of the existing publications of \"y\" answered HTTP %d: %s", status, answer)
	var announced publicationResult
	if err := json.Unmarshal([]byte(answer), &announced); status != http.StatusAccepted || err != nil {
		t.Errorf("the announcement of the existing publications of \"y\" failed: HTTP %d %v", status, err)
	} else if announced.RecordCount != 2 {
		t.Errorf("the announcement of the existing publications of \"y\" announced %d records, want the 2 of part one's completed publication: the windows of the failed republication were announced again", announced.RecordCount)
	}

	// An unrelated batch.
	d.ingestOMMFrames(t, source, "z", 0, frames[3:])
	if pub := d.publish(t, "OMM.fbs", source, "z"); pub.RecordCount != 1 {
		t.Fatalf("the publication of \"z\" answered %d records", pub.RecordCount)
	}
	both := `batch \"x\" is unpublished: no publication of the whole batch completed; it holds 1 record; batch \"y\" is unpublished: 1 of its 3 records arrived after its last completed publication of the whole batch and are in no publication of it`
	t.Logf("alert after the publication of \"z\": %s", alertStays(t, d, lane, both, 10*time.Second))

	// A batch sharing the records of part one with "y": the alert stays as
	// it was.
	d.ingestOMMFrames(t, source, "w", 0, frames[:2])
	if pub := d.publish(t, "OMM.fbs", source, "w"); pub.RecordCount != 2 {
		t.Fatalf("the publication of \"w\" answered %d records", pub.RecordCount)
	}
	t.Logf("alert after the publication of \"w\": %s", alertStays(t, d, lane, both, 10*time.Second))

	// The whole of "y" as it is now: "y" goes; "x" stays, though "y" holds
	// its record.
	if pub := d.publish(t, "OMM.fbs", source, "y"); pub.RecordCount != 3 {
		t.Fatalf("the publication of the whole of \"y\" answered %d records", pub.RecordCount)
	}
	alert := alertStays(t, d, lane, `batch \"x\" is unpublished: no publication of the whole batch completed; it holds 1 record`, 10*time.Second)
	t.Logf("alert after the publication of the whole of \"y\": %s", alert)
	if strings.Contains(alert, `batch \"y\" is unpublished`) {
		t.Fatalf("the alert still names \"y\" after the publication of the whole of it: %s", alert)
	}

	// The whole of "x".
	if pub := d.publish(t, "OMM.fbs", source, "x"); pub.RecordCount != 1 {
		t.Fatalf("the publication of the whole of \"x\" answered %d records", pub.RecordCount)
	}
	awaitPublicationAlertCleared(t, d, lane, time.Minute)
	t.Logf("cleared: %s", publicationLogAfter(t, d, `failed scope "publish batch" of batch "x" is published`))
}

// alertStays checks, for budget, that the lane's publication_failed alert
// stays active and says want, failing at once, naming it, when it clears or
// says otherwise. It returns the alert.
func alertStays(t *testing.T, d *publicationDaemon, lane, want string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	alert := ""
	for {
		alert = publicationAlert(t, d, lane)
		if alert == "" {
			t.Fatalf("the publication_failed alert of %s cleared; want it to stay saying %q", lane, want)
		}
		if !strings.Contains(alert, want) {
			t.Fatalf("the publication_failed alert of %s does not say %q: %s", lane, want, alert)
		}
		if time.Now().After(deadline) {
			return alert
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestPublicationAlertHeadPublicationRealDaemons fails the publication of a
// batch of three records, then publishes a head of that batch (limit 1) and
// a head of the lane (no batch): both complete, and neither exported every
// record of the failed batch, so the alert stays (each cleared it here).
// A publication of the whole batch clears it.
func TestPublicationAlertHeadPublicationRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := newPublicationDaemon(t, publicationBinary(t), "head-publication", "", "", "keep-all")
	d.start(t)
	source := "head-publication"
	lane := "OMM.fbs publication-e2e/" + source
	d.ingestOMMFrames(t, source, "failed", 0, [][]byte{baselineOMM(0), baselineOMM(1), baselineOMM(2)})
	bad, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": "failed", "announcementSchemas": []string{"not a schema"}})
	status, answer := postStatus(t, d, "POST", "/api/v1/admin/dataset-updates/publish", bad)
	t.Logf("the failed publication answered HTTP %d: %s", status, answer)
	alert := awaitPublicationAlert(t, d, lane, `batch \"failed\" is unpublished: no publication of the whole batch completed; it holds 3 records`, time.Minute)
	t.Logf("alert after the failure: %s", alert)

	for _, head := range []struct {
		name string
		req  map[string]any
	}{
		{"a head of the failed batch (limit 1)", map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": "failed", "limit": 1}},
		{"a head of the lane (no batch)", map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source}},
	} {
		body, _ := json.Marshal(head.req)
		raw := d.request(t, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", body, http.StatusAccepted)
		var pub publicationResult
		if err := json.Unmarshal(raw, &pub); err != nil {
			t.Fatal(err)
		}
		// The publication's report, which clears or keeps the alert, comes
		// before its callers are answered.
		t.Logf("the publication of %s completed: %d records", head.name, pub.RecordCount)
		if alert := publicationAlert(t, d, lane); alert == "" {
			t.Fatalf("the publication of %s cleared the alert of the failed batch", head.name)
		} else {
			t.Logf("alert after the publication of %s: %s", head.name, alert)
		}
	}

	if pub := d.publish(t, "OMM.fbs", source, "failed"); pub.RecordCount != 3 {
		t.Fatalf("the publication of the whole failed batch answered %d records", pub.RecordCount)
	}
	awaitPublicationAlertCleared(t, d, lane, time.Minute)
}
