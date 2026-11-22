package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestPublicationAlertOtherBatchNoCreditRealDaemons fails the publication of
// batch "x" (two records), then publishes batch "y", which holds every
// record of "x". A publication of another batch never clears a failed
// batch: the alert stays, saying no publication of the whole of "x"
// completed (it cleared here when every record of "x" was in a completed
// publication of any batch). A publication of the whole of "x" clears it.
func TestPublicationAlertOtherBatchNoCreditRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := newPublicationDaemon(t, publicationBinary(t), "other-batch", "", "", "keep-all")
	d.start(t)
	source := "other-batch"
	lane := "OMM.fbs publication-e2e/" + source
	frames := [][]byte{baselineOMM(0), baselineOMM(1)}
	d.ingestOMMFrames(t, source, "x", 0, frames)
	failBatchPublication(t, d, source, "x", 1)
	t.Logf("alert after the failure: %s", awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished`, time.Minute))

	d.ingestOMMFrames(t, source, "y", 0, frames)
	if pub := d.publish(t, "OMM.fbs", source, "y"); pub.RecordCount != 2 {
		t.Fatalf("the publication of \"y\" answered %d records", pub.RecordCount)
	}
	alert := alertStays(t, d, lane, `batch \"x\" is unpublished`, 15*time.Second)
	t.Logf("alert after the publication of \"y\": %s", alert)
	if want := `batch \"x\" is unpublished: no publication of the whole batch completed; it holds 2 records`; !strings.Contains(alert, want) {
		t.Fatalf("the alert does not say %q: %s", want, alert)
	}

	if pub := d.publish(t, "OMM.fbs", source, "x"); pub.RecordCount != 2 {
		t.Fatalf("the publication of the whole of \"x\" answered %d records", pub.RecordCount)
	}
	awaitPublicationAlertCleared(t, d, lane, time.Minute)
	t.Logf("checked: %s", publicationAwaitLog(t, d, `Dataset publication `+lane+`: batch "x" is published as it is now`, time.Minute))
}

// TestPublicationAlertRecordsDuringPublicationRealDaemons fails the
// publication of batch "x" (two records), then publishes the whole of "x" on
// a node that holds that publication for 15 s after it exported its set and
// before it records its completion (SDN_E2E_PUBLICATION_COMPLETION_HOLD).
// A third record of "x" is stored during the hold. The completed publication
// removes the scope of "x" when it is reported; the check of "x" after it
// finds "x" holds a record that publication did not export, records the
// scope again and raises the alert again, saying so (it stayed clear here).
// A publication of the whole of "x" as it is now clears it, and the check
// after that one finds "x" published.
func TestPublicationAlertRecordsDuringPublicationRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	const hold = 15 * time.Second
	d := newPublicationDaemon(t, publicationBinary(t), "during-publication", "", "", "keep-all")
	d.env = append(d.env, "SDN_E2E_PUBLICATION_COMPLETION_HOLD="+hold.String())
	d.start(t)
	source := "during-publication"
	lane := "OMM.fbs publication-e2e/" + source
	frames := [][]byte{baselineOMM(0), baselineOMM(1), baselineOMM(2)}
	d.ingestOMMFrames(t, source, "x", 0, frames[:2])
	failBatchPublication(t, d, source, "x", 1)
	t.Logf("alert after the failure: %s", awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished`, time.Minute))

	answered := make(chan string, 1)
	go func() {
		body, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "batchId": "x"})
		client := http.Client{Timeout: 5 * time.Minute}
		res, err := client.Post(d.base+"/api/v1/admin/dataset-updates/publish", "application/json", strings.NewReader(string(body)))
		if err != nil {
			answered <- err.Error()
			return
		}
		res.Body.Close()
		answered <- res.Status
	}()
	publicationAwaitLog(t, d, `SDN_E2E_PUBLICATION_COMPLETION_HOLD: the publication of `+lane+` batch "x" is held`, time.Minute)
	d.ingestOMMFrames(t, source, "x", 0, frames[2:])
	t.Logf("a third record of \"x\" is stored during the hold")
	select {
	case status := <-answered:
		t.Logf("the publication of the whole of \"x\" answered: %s", status)
		if !strings.HasPrefix(status, "202") {
			t.Fatalf("the publication of the whole of \"x\" did not complete: %s", status)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("the publication of the whole of \"x\" was not answered within 2 minutes")
	}
	t.Logf("removed when reported: %s", publicationLogAfter(t, d, `failed scope "publish batch" of batch "x" is published`))
	alert := awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished: 1 of its 3 records arrived after its last completed publication of the whole batch and are in no publication of it`, time.Minute)
	t.Logf("alert after the check: %s", alert)
	t.Logf("recorded again: %s", publicationLogAfter(t, d, `Dataset publication `+lane+`: batch "x" holds 3 records`))

	if pub := d.publish(t, "OMM.fbs", source, "x"); pub.RecordCount != 3 {
		t.Fatalf("the publication of the whole of \"x\" as it is now answered %d records", pub.RecordCount)
	}
	awaitPublicationAlertCleared(t, d, lane, time.Minute)
	t.Logf("checked: %s", publicationAwaitLog(t, d, `Dataset publication `+lane+`: batch "x" is published as it is now (3 records)`, time.Minute))
	if alert := publicationAlert(t, d, lane); alert != "" {
		t.Fatalf("the alert is raised again after the publication of \"x\" as it is now: %s", alert)
	}
}

// TestPublicationAlertRetractedLaneRealDaemons fails the publication of
// batch "x" and of the lane's newest records (no batch), then retracts the
// lane (POST /api/v1/admin/dataset-updates/retract?apply=true). A retracted
// lane is never published again, so both failed scopes go at the retraction
// and the alert is clear when it answers (both stayed here: no publication
// could clear them). It stays clear, and the restarted node raises nothing.
func TestPublicationAlertRetractedLaneRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	d := newPublicationDaemon(t, publicationBinary(t), "retracted-lane", "", "", "keep-all")
	d.start(t)
	source := "retracted-lane"
	lane := "OMM.fbs publication-e2e/" + source
	d.ingestOMMFrames(t, source, "x", 0, [][]byte{baselineOMM(0), baselineOMM(1)})
	failBatchPublication(t, d, source, "x", 1)
	head, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "publication-e2e", "sourceName": source, "announcementSchemas": []string{"not a schema"}})
	status, answer := postStatus(t, d, "POST", "/api/v1/admin/dataset-updates/publish", head)
	t.Logf("the failing publication of the lane's newest records answered HTTP %d: %s", status, answer)
	alert := awaitPublicationAlert(t, d, lane, `a publication of the lane's newest records failed`, time.Minute)
	if !strings.Contains(alert, `batch \"x\" is unpublished`) {
		t.Fatalf("the alert does not name the failed batch \"x\": %s", alert)
	}
	t.Logf("alert before the retraction: %s", alert)

	raw := d.request(t, "POST", "/api/v1/admin/dataset-updates/retract?schema=OMM.fbs&providerId=publication-e2e&sourceName="+source+"&apply=true", "", nil, http.StatusOK)
	t.Logf("the retraction answered %d bytes", len(raw))
	if alert := publicationAlert(t, d, lane); alert != "" {
		t.Fatalf("the alert of the retracted lane is active when the retraction answered: %s", alert)
	}
	t.Logf("removed: %s", publicationLogAfter(t, d, `Dataset publication `+lane+`: the lane is retracted; its `))
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if alert := publicationAlert(t, d, lane); alert != "" {
			t.Fatalf("the alert of the retracted lane is raised again: %s", alert)
		}
	}
	d.stop(t)
	d.start(t)
	if alert := publicationAlert(t, d, lane); alert != "" {
		t.Fatalf("the restarted node raised the alert of the retracted lane: %s", alert)
	}
}

// TestPublicationAlertCheckCostRealDaemons publishes 50 batches of a lane,
// each holding record R and one of its own, then fails the publication of
// batch "x", which holds R and a record of its own. The check of "x"
// fingerprints "x" alone: the node's log names no fingerprint of another
// batch (the recount fingerprinted every one of the 50 here, each a batch
// with a completed publication that shares R with "x").
func TestPublicationAlertCheckCostRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for isolated real daemons")
	}
	const sharing = 50
	d := newPublicationDaemon(t, publicationBinary(t), "check-cost", "", "", "keep-all")
	d.start(t)
	source := "check-cost"
	lane := "OMM.fbs publication-e2e/" + source
	frames := make([][]byte, sharing+2)
	for i := range frames {
		frames[i] = baselineOMM(i)
	}
	started := time.Now()
	for i := 1; i <= sharing; i++ {
		batch := fmt.Sprintf("b%02d", i)
		d.ingestOMMFrames(t, source, batch, 0, [][]byte{frames[0], frames[i]})
		if pub := d.publish(t, "OMM.fbs", source, batch); pub.RecordCount != 2 {
			t.Fatalf("the publication of %q answered %d records", batch, pub.RecordCount)
		}
	}
	t.Logf("%d batches sharing record R published in %s", sharing, time.Since(started).Round(time.Second))

	d.ingestOMMFrames(t, source, "x", 0, [][]byte{frames[0], frames[sharing+1]})
	failBatchPublication(t, d, source, "x", 1)
	// The check's end: the alert restated with its result.
	t.Logf("after the check: %s", publicationAwaitLog(t, d, `Dataset publication `+lane+`: 1 failed scope(s) are still unpublished: `, 2*time.Minute))
	marker := `Failed batch check ` + lane + `: fingerprinted batch "`
	fingerprinted := map[string]int{}
	for _, m := range regexp.MustCompile(regexp.QuoteMeta(marker)+`([^"]*)"`).FindAllStringSubmatch(readFileString(d.logPath), -1) {
		fingerprinted[m[1]]++
	}
	t.Logf("batches the checks fingerprinted: %v", fingerprinted)
	for batch, n := range fingerprinted {
		if batch != "x" {
			t.Errorf("the check of \"x\" fingerprinted batch %q (%d times)", batch, n)
		}
	}
	if fingerprinted["x"] != 1 {
		t.Errorf("batch \"x\" was fingerprinted %d times by its check, want 1", fingerprinted["x"])
	}
	t.Logf("alert: %s", awaitPublicationAlert(t, d, lane, `batch \"x\" is unpublished: no publication of the whole batch completed; it holds 2 records`, time.Minute))
}
