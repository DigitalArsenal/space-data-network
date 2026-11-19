package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// failBatchPublication requests a publication of the whole batch that fails
// at its announcement (an announcement schema that is not one) and waits
// until the node logged the n-th failure of the batch.
func failBatchPublication(t *testing.T, d *publicationDaemon, source, batch string, n int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": batch, "announcementSchemas": []string{"not a schema"}})
	status, answer := postStatus(t, d, "POST", "/api/v1/admin/dataset-updates/publish", body)
	t.Logf("the failing publication of %q answered HTTP %d: %s", batch, status, answer)
	awaitLogCount(t, d, `Dataset publication FAILED: OMM.fbs publication-e2e/`+source+` batch "`+batch+`", step "`, n, time.Minute)
}

// awaitLogCount waits until marker is in the daemon's log n times.
func awaitLogCount(t *testing.T, d *publicationDaemon, marker string, n int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if strings.Count(readFileString(d.logPath), marker) >= n {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%q is not in %s %d times within %s", marker, d.logPath, n, budget)
}

// TestPublicationAlertGainedAfterFailureRealDaemons stores part one of batch
// "x" (two records) and fails its publication; stores part two (a third
// record) and fails its publication again; then publishes batch "y", which
// holds both records of part one. The third record is in no publication,
// so the alert stays and says one of the three records of "x" is
// unpublished (it cleared here when "x" was counted once, at its first
// failure, and "y" covered what that count found). A publication of the
// whole of "x" clears it.
func TestPublicationAlertGainedAfterFailureRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := newPublicationDaemon(t, publicationBinary(t), "gained-after-failure", "", "", "keep-all")
	d.start(t)
	source := "gained-after-failure"
	lane := "OMM.fbs publication-e2e/" + source
	frames := [][]byte{baselineOMM(0), baselineOMM(1), baselineOMM(2)}

	d.ingestOMMFrames(t, source, "x", 0, frames[:2])
	failBatchPublication(t, d, source, "x", 1)
	t.Logf("alert after the first failure: %s", awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished: 2 of its 2 records are in no completed publication of the lane`, time.Minute))

	d.ingestOMMFrames(t, source, "x", 0, frames[2:])
	failBatchPublication(t, d, source, "x", 2)

	d.ingestOMMFrames(t, source, "y", 0, frames[:2])
	if pub := d.publish(t, "OMM.fbs", source, "y"); pub.RecordCount != 2 {
		t.Fatalf("the publication of \"y\" answered %d records", pub.RecordCount)
	}
	alert := awaitAlertAfter(t, d, lane, `after the publication of batch \"y\", batch \"x\" is unpublished: 1 of its 3 records are in no completed publication of the lane`, time.Minute)
	t.Logf("alert after the publication of \"y\": %s", alert)

	if pub := d.publish(t, "OMM.fbs", source, "x"); pub.RecordCount != 3 {
		t.Fatalf("the publication of the whole of \"x\" answered %d records", pub.RecordCount)
	}
	awaitPublicationAlertCleared(t, d, lane, time.Minute)
	t.Logf("cleared: %s", publicationLogAfter(t, d, `every record of failed batch "x"`))
}

// TestPublicationAlertRestartRealDaemons fails a batch's publication and
// restarts the node: the alert is raised again at start and says the
// batch's two records are unpublished (a restart dropped it here). A
// publication of the whole batch clears it, and the node restarted again
// raises no alert.
func TestPublicationAlertRestartRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := newPublicationDaemon(t, publicationBinary(t), "alert-restart", "", "", "keep-all")
	d.start(t)
	source := "alert-restart"
	lane := "OMM.fbs publication-e2e/" + source
	d.ingestOMMFrames(t, source, "x", 0, [][]byte{baselineOMM(0), baselineOMM(1)})
	failBatchPublication(t, d, source, "x", 1)
	t.Logf("alert before the restart: %s", awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished: 2 of its 2 records are in no completed publication of the lane`, time.Minute))

	d.stop(t)
	d.start(t)
	alert := publicationAlert(t, d, lane)
	t.Logf("alert at the restarted node's start: %s", alert)
	if alert == "" {
		t.Fatalf("the restarted node raised no publication_failed alert of %s", lane)
	}
	t.Logf("alert after the count: %s", awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished: 2 of its 2 records are in no completed publication of the lane`, time.Minute))

	if pub := d.publish(t, "OMM.fbs", source, "x"); pub.RecordCount != 2 {
		t.Fatalf("the publication of the whole of \"x\" answered %d records", pub.RecordCount)
	}
	awaitPublicationAlertCleared(t, d, lane, time.Minute)

	d.stop(t)
	d.start(t)
	if alert := publicationAlert(t, d, lane); alert != "" {
		t.Fatalf("the node restarted after the alert cleared raised it again: %s", alert)
	}
}

// TestPublicationAlertSupersededBatchRealDaemons fails a batch's publication
// on a replace-current node, stores a batch of other records (a pull whose
// element sets replaced the failed batch's), supersedes the lane by that
// batch (the store's replace-current eviction, POST
// /api/v1/admin/store/supersede) and publishes it: the failed batch holds no
// records any more, and the alert clears, saying so (it stayed here: none of
// the failed batch's records was in the published batch).
func TestPublicationAlertSupersededBatchRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := newPublicationDaemon(t, publicationBinary(t), "alert-superseded", "", "", "replace-current")
	d.start(t)
	source := "alert-superseded"
	lane := "OMM.fbs publication-e2e/" + source
	d.ingestOMMFrames(t, source, "x", 0, [][]byte{baselineOMM(0), baselineOMM(1)})
	failBatchPublication(t, d, source, "x", 1)
	t.Logf("alert after the failure: %s", awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished: 2 of its 2 records are in no completed publication of the lane`, time.Minute))

	d.ingestOMMFrames(t, source, "y", 0, [][]byte{baselineOMM(2), baselineOMM(3)})
	body, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "provider_id": "publication-e2e", "source_name": source, "keep_batch": "y"})
	raw := d.request(t, "POST", "/api/v1/admin/store/supersede", "application/json", body, http.StatusOK)
	t.Logf("supersede by \"y\": %s", raw)
	if left := d.scanBatch(t, "OMM.fbs", source, "x"); len(left) != 0 {
		t.Fatalf("batch \"x\" holds %d records after the supersede", len(left))
	}

	if pub := d.publish(t, "OMM.fbs", source, "y"); pub.RecordCount != 2 {
		t.Fatalf("the publication of \"y\" answered %d records", pub.RecordCount)
	}
	awaitPublicationAlertCleared(t, d, lane, time.Minute)
	t.Logf("cleared: %s", publicationLogAfter(t, d, `failed batch "x" holds no records any more`))
}

// TestPublicationAlertFailureDuringClearRealDaemons fails batch "x", then
// publishes batch "y" holding its record, on a node that holds the alert's
// clearing for 20 s after it is decided
// (SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD). While the clearing is held, the
// publication of batch "w" fails. After the hold the alert is active and
// names "w": the clear of "x" did not take the failure of "w" with it.
func TestPublicationAlertFailureDuringClearRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	const hold = 20 * time.Second
	d := newPublicationDaemon(t, publicationBinary(t), "failure-during-clear", "", "", "keep-all")
	d.env = append(d.env, "SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD="+hold.String())
	d.start(t)
	source := "failure-during-clear"
	lane := "OMM.fbs publication-e2e/" + source
	d.ingestOMMFrames(t, source, "x", 0, [][]byte{baselineOMM(0)})
	failBatchPublication(t, d, source, "x", 1)
	awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished: 1 of its 1 records are in no completed publication of the lane`, time.Minute)

	d.ingestOMMFrames(t, source, "y", 0, [][]byte{baselineOMM(0)})
	d.ingestOMMFrames(t, source, "w", 0, [][]byte{baselineOMM(1)})
	if pub := d.publish(t, "OMM.fbs", source, "y"); pub.RecordCount != 1 {
		t.Fatalf("the publication of \"y\" answered %d records", pub.RecordCount)
	}
	publicationAwaitLog(t, d, "SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD: the clearing of the publication_failed alert of "+lane+" is held", time.Minute)
	held := time.Now()
	failBatchPublication(t, d, source, "w", 1)
	t.Logf("the publication of \"w\" failed %s after the clearing was held", time.Since(held).Round(time.Millisecond))
	for time.Since(held) < hold+5*time.Second {
		time.Sleep(time.Second)
	}
	alert := publicationAlert(t, d, lane)
	t.Logf("alert after the hold: %s", alert)
	if !strings.Contains(alert, `batch \"w\"`) {
		t.Fatalf("the publication_failed alert of %s does not name the failed batch \"w\" after the clear of \"x\": %q", lane, alert)
	}
	if strings.Contains(alert, `batch \"x\"`) {
		t.Fatalf("the alert still names batch \"x\", whose record is published: %s", alert)
	}
}
