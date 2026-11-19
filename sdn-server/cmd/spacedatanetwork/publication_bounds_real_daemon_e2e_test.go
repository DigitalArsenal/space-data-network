package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPublicationCursorRefusedRealDaemons: a producer whose scan generation
// turns (reindex) refuses every resume cursor it issued before. A
// subscription that started at the producer's current batch, and one that
// started at the lane's beginning, both start again at the producer's current
// batch: neither reads the lane from its beginning again, both raise
// follow_cursor_refused, and the one that started at the beginning is moved
// out of follows.dss (the file 1.1.9 reads).
func TestPublicationCursorRefusedRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "cursor-producer", "", "", "keep-all")
	a.start(t)
	for i := 1; i <= 3; i++ {
		a.ingestOMMCIDs(t, "cr-rc", fmt.Sprintf("c%d", i), 700_000+i*10_000, 1_000)
	}
	a.ingestOMMCIDs(t, "cr-ka", "k1", 760_000, 500)
	a.ingestOMMCIDs(t, "cr-ka", "k2", 761_000, 500)
	b := newPublicationDaemon(t, exe, "cursor-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "cr-rc", "--from", a.id, "--start", "current"); err != nil {
		t.Fatalf("sync subscribe cr-rc: %v\n%s", err, out)
	}
	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "cr-ka", "--from", a.id, "--start", "beginning"); err != nil {
		t.Fatalf("sync subscribe cr-ka: %v\n%s", err, out)
	}
	b.awaitRecords(t, "OMM.fbs", "cr-rc", a.scanBatch(t, "OMM.fbs", "cr-rc", "c3"), 3*time.Minute)
	b.awaitRecords(t, "OMM.fbs", "cr-ka", a.scan(t, "OMM.fbs", "cr-ka"), 3*time.Minute)
	if got := b.subscription(t, "cr-rc").ReceivedRecords; got != 1_000 {
		t.Fatalf("cr-rc received %d records for its first read, want the current batch's 1000", got)
	}
	if got := b.subscription(t, "cr-ka").ReceivedRecords; got != 1_000 {
		t.Fatalf("cr-ka received %d records for its first read, want the lane's 1000", got)
	}
	inFollows := followFileNames(t, b, "follows.dss")
	t.Log("cr-rc started at the producer's current batch (1,000 of 3,000), cr-ka at its beginning (1,000)")

	// The producer's scan generation turns: every cursor it issued expires.
	a.stop(t)
	if out, err := a.cli(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	a.start(t)
	a.ingestOMMCIDs(t, "cr-rc", "c3", 790_000, 200)
	a.ingestOMMCIDs(t, "cr-ka", "k2", 795_000, 200)
	b.awaitRecords(t, "OMM.fbs", "cr-rc", a.scanBatch(t, "OMM.fbs", "cr-rc", "c3"), 4*time.Minute)
	b.awaitRecords(t, "OMM.fbs", "cr-ka", a.scan(t, "OMM.fbs", "cr-ka"), 4*time.Minute)
	if log := readFileString(b.logPath); strings.Count(log, "the producer refused the saved cursor") < 2 {
		t.Fatal("the receiver's log does not show both cursors refused: the reindex did not expire them, so this run proves nothing")
	}
	for _, lane := range []string{"cr-rc", "cr-ka"} {
		sub := b.subscription(t, lane)
		if sub.ReceivedRecords > 1_200 {
			t.Fatalf("%s received %d records in all, more than its first read and the 200 stored since (1200): the refused cursor read the lane from its beginning", lane, sub.ReceivedRecords)
		}
		if sub.StartsAt != "current" {
			t.Fatalf("%s starts at %q after the refused cursor, want current", lane, sub.StartsAt)
		}
		t.Logf("%s after the refused cursor: received %d records in all (first read 1000, 200 since)", lane, sub.ReceivedRecords)
	}
	alerts := b.alerts(t)
	for _, lane := range []string{"cr-rc", "cr-ka"} {
		found := false
		for _, al := range alerts {
			if al.Kind == "follow_cursor_refused" && strings.Contains(al.Subject, lane) {
				found = true
				if lane == "cr-ka" && !strings.Contains(al.LastError, "are not read") {
					t.Fatalf("the alert of the lane that started at its beginning does not say what was not read: %q", al.LastError)
				}
			}
		}
		if !found {
			t.Fatalf("no follow_cursor_refused alert for %s: %+v", lane, alerts)
		}
	}
	if !inFollows["cr-ka"] || inFollows["cr-rc"] {
		t.Fatalf("follows.dss held %v before the refusal; want the lane that started at its beginning (cr-ka) and not the bounded one (cr-rc)", inFollows)
	}
	if names := followFileNames(t, b, "follows.dss"); len(names) != 0 {
		t.Fatalf("follows.dss holds %v after both lanes started again at the current batch, want none", names)
	}
	if names := followFileNames(t, b, "subscriptions.dss"); !names["cr-rc"] || !names["cr-ka"] {
		t.Fatalf("subscriptions.dss holds %v, want both lanes", names)
	}
	b.requireAlive(t)
}

// TestPublicationStreamSlotsRealDaemons: a producer serves a subscriber it
// has no trust relation with at most eight followed streams; the ninth is
// refused busy. Once the producer lists the subscriber as trusted it serves
// all nine, outside the anonymous pool.
func TestPublicationStreamSlotsRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "slots-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "slots-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	const lanes = 9
	for i := 1; i <= lanes; i++ {
		a.ingestOMMCIDs(t, fmt.Sprintf("sl-%d", i), "s1", 900_000+i*1_000, 50)
	}
	for i := 1; i <= lanes; i++ {
		b.follow(t, a, "OMM", fmt.Sprintf("sl-%d", i), "")
	}
	connected := func() (int, []string) {
		n := 0
		var errs []string
		for _, s := range b.subscriptions(t).Subscriptions {
			if s.Connected {
				n++
			} else if s.Error != "" {
				errs = append(errs, s.SourceName+": "+s.Error)
			}
		}
		return n, errs
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		n, errs := connected()
		busy := 0
		for _, e := range errs {
			if strings.Contains(e, "busy") || strings.Contains(e, "limit") {
				busy++
			}
		}
		if n == lanes {
			t.Fatalf("all %d followed streams were served to a subscriber the producer does not trust; at most 8 are", lanes)
		}
		if n == 8 && busy == 1 {
			t.Logf("untrusted subscriber: 8 streams served, the ninth refused: %v", errs)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("untrusted subscriber: %d streams connected, refusals %v; want 8 and one refused busy", n, errs)
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(20 * time.Second)
	if n, _ := connected(); n != 8 {
		t.Fatalf("untrusted subscriber: %d streams connected after 20 s, want 8", n)
	}

	// The producer lists the subscriber as trusted.
	a.stop(t)
	a.appendConfig(t, fmt.Sprintf("  trusted_peers: [%q]\n", b.address), "peers:\n")
	a.start(t)
	deadline = time.Now().Add(3 * time.Minute)
	for {
		n, errs := connected()
		if n == lanes {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trusted subscriber: %d of %d streams connected, refusals %v", n, lanes, errs)
		}
		time.Sleep(2 * time.Second)
	}
	for i := 1; i <= lanes; i++ {
		lane := fmt.Sprintf("sl-%d", i)
		b.awaitRecords(t, "OMM.fbs", lane, a.scan(t, "OMM.fbs", lane), 2*time.Minute)
	}
	t.Logf("trusted subscriber: all %d streams served and every lane held", lanes)
}

// nodeAlert is one entry of /api/v1/status/alerts.
type nodeAlert struct {
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Severity  string `json:"severity"`
	LastError string `json:"last_error"`
}

// alerts is the daemon's active alerts.
func (d *publicationDaemon) alerts(t *testing.T) []nodeAlert {
	t.Helper()
	raw := d.request(t, "GET", "/api/v1/status/alerts", "", nil, http.StatusOK)
	var body struct {
		Alerts []nodeAlert `json:"alerts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Alerts
}

// followFileNames is the source names of the subscriptions in one of d's
// subscription files (none when the file is absent).
func followFileNames(t *testing.T, d *publicationDaemon, name string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, r := range d.dssFile(t, name) {
		out[string(r.SOURCE_NAME())] = true
	}
	return out
}
