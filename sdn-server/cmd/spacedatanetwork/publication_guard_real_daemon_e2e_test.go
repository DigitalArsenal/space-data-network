package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// hasAlertOn reports an active alert of kind whose subject contains text.
func hasAlertOn(alerts []nodeAlert, kind, text string) bool {
	for _, a := range alerts {
		if a.Kind == kind && strings.Contains(a.Subject, text) {
			return true
		}
	}
	return false
}

// subscribeCurrent is `sync subscribe --start current` of lane (OMM,
// publication-e2e, source) from p, with extra flags.
func (d *publicationDaemon) subscribeCurrent(t *testing.T, p *publicationDaemon, source string, extra ...string) {
	t.Helper()
	args := append([]string{"sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", source, "--from", p.id, "--start", "current"}, extra...)
	if out, err := d.cli(t, args...); err != nil {
		t.Fatalf("sync subscribe %s: %v\n%s", source, err, out)
	}
}

// TestPublicationUnheldBatchImportsPNMRealDaemons: a lane whose followed
// stream is connected but whose current batch this node cannot hold (here
// the producer serves a lane it imported itself, so every record of the batch
// names another peer as its producer and each fetch by CID is refused) gets
// the batch through the lane's signed $PNM publication: until its current
// batch is held the lane does not count as streamed. Once the publication's
// records are held the batch becomes current.
func TestPublicationUnheldBatchImportsPNMRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	c := newPublicationDaemon(t, exe, "unheld-origin", "", "", "keep-all")
	c.start(t)
	c.ingestOMMCIDs(t, "uh", "u1", 970_000, 300)
	// a imports the lane from c: its records name c as their producer.
	a := newPublicationDaemon(t, exe, "unheld-producer", c.address, c.id, "keep-all")
	a.start(t)
	time.Sleep(3 * time.Second)
	a.subscribeCurrent(t, c, "uh")
	want := c.scan(t, "OMM.fbs", "uh")
	if len(want) != 300 {
		t.Fatalf("origin holds %d records of lane uh, want 300", len(want))
	}
	a.awaitRecords(t, "OMM.fbs", "uh", want, 3*time.Minute)
	b := newPublicationDaemon(t, exe, "unheld-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	b.subscribeCurrent(t, a, "uh")
	deadline := time.Now().Add(4 * time.Minute)
	for {
		sub := b.subscription(t, "uh")
		if sub.Connected && strings.Contains(sub.Error, "names another peer") {
			t.Logf("lane uh: stream connected, its current batch not held: %.300s", sub.Error)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lane uh: connected %t, last error %q; want a connected stream whose current batch's records are refused", sub.Connected, sub.Error)
		}
		time.Sleep(2 * time.Second)
	}
	if got := len(b.scan(t, "OMM.fbs", "uh")); got != 0 {
		t.Fatalf("the receiver holds %d records of lane uh before the publication; the stream should have stored none", got)
	}
	a.publish(t, "OMM.fbs", "uh", "u1")
	b.awaitRecords(t, "OMM.fbs", "uh", want, 6*time.Minute)
	t.Log("lane uh: the publication's 300 records arrived through $PNM while the stream was connected")
	deadline = time.Now().Add(3 * time.Minute)
	for {
		sub := b.subscription(t, "uh")
		if sub.CurrentBatch == "u1" && sub.Lag == 0 {
			t.Logf("lane uh: batch u1 is current (received on the stream %d records)", sub.ReceivedRecords)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lane uh: current batch %q, lag %d, last error %q; want u1 held once its records arrived", sub.CurrentBatch, sub.Lag, sub.Error)
		}
		time.Sleep(2 * time.Second)
	}
	b.requireAlive(t)
}

// publicationFollowFaultsBinary is a daemon built with the tag
// sdn_e2e_follow_faults (follow_faults_e2e.go: the stores of a lane named "panic-..."
// panic, a lane named "late-..." starts late): SDN_E2E_PUBLICATION_FOLLOW_FAULTS_BINARY, or built here.
func publicationFollowFaultsBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("SDN_E2E_PUBLICATION_FOLLOW_FAULTS_BINARY"); binary != "" {
		return binary
	}
	exe := filepath.Join(publicationTemp(t, "build-follow-faults"), "spacedatanetwork")
	module, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(module, "..", "scripts", "go-with-wasmedge.sh"), "build", "-o", exe, "-tags", "sdn_e2e_follow_faults", "./cmd/spacedatanetwork")
	cmd.Dir = module
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the follow-faults daemon: %v\n%s", err, tailString(string(out), 20))
	}
	return exe
}

// plan is GET /api/v1/sync/plan of lane (OMM, publication-e2e, source) from p.
func (d *publicationDaemon) plan(t *testing.T, p *publicationDaemon, source string) api.LanePlan {
	t.Helper()
	q := url.Values{"schema": {"OMM"}, "provider_id": {"publication-e2e"}, "source": {source}, "from": {p.id}}
	var plan api.LanePlan
	if err := json.Unmarshal(d.request(t, "GET", api.SyncPlanPath+"?"+q.Encode(), "", nil, http.StatusOK), &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestPublicationStorePanicReleasesReservationRealDaemons: a followed store
// that panics (a receiver built so the stores of its "panic-" lanes panic)
// releases the running guard's reservation, on a followed page and on a fetch
// by CID of the current batch. The reservation is shared by every lane, so a
// panic that kept it would, retried at each reconnect, stop every lane at the
// record ceiling: here a few leaked retries leave less than the other lane's
// next 3,000 records under the ceiling of 8,000, and that lane keeps storing
// only because none is leaked.
func TestPublicationStorePanicReleasesReservationRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "panic-producer", "", "", "keep-all")
	a.start(t)
	a.ingestOMMCIDs(t, "sp-ok", "o1", 940_000, 100)
	a.ingestOMMCIDs(t, "panic-seq", "q1", 942_000, 2_000)
	a.ingestOMMCIDs(t, "panic-cid", "c1", 945_000, 2_000)
	b := newPublicationDaemon(t, publicationFollowFaultsBinary(t), "panic-receiver", a.address, a.id, "keep-all")
	b.appendConfig(t, "  record_ceiling: 8000\n", "subscriptions:\n")
	b.start(t)
	time.Sleep(3 * time.Second)
	b.subscribeCurrent(t, a, "sp-ok")
	b.awaitRecords(t, "OMM.fbs", "sp-ok", a.scan(t, "OMM.fbs", "sp-ok"), 3*time.Minute)
	// panic-seq's records come on followed pages (from its beginning),
	// panic-cid's by CID (its current batch).
	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "panic-seq", "--from", a.id, "--start", "beginning"); err != nil {
		t.Fatalf("sync subscribe panic-seq: %v\n%s", err, out)
	}
	b.subscribeCurrent(t, a, "panic-cid")
	deadline := time.Now().Add(4 * time.Minute)
	for {
		log := readFileString(b.logPath)
		seq, cid := strings.Count(log, "the store of lane panic-seq panics"), strings.Count(log, "the store of lane panic-cid panics")
		if seq >= 2 && cid >= 2 {
			t.Logf("the stores panicked: %d times on panic-seq's pages, %d times on panic-cid's fetches by CID", seq, cid)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stores panicked %d times on panic-seq, %d on panic-cid; want at least 2 each", seq, cid)
		}
		time.Sleep(2 * time.Second)
	}
	// Nothing is reserved between the stores (a store in progress holds its
	// reservation for an instant).
	deadline = time.Now().Add(30 * time.Second)
	for {
		plan := b.plan(t, a, "sp-ok")
		if plan.ReservedRecords == 0 && plan.ReservedBytes == 0 {
			t.Logf("after the panics nothing is reserved (held %d records, ceiling %d)", plan.HeldRecords, plan.RecordCeiling)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the panicked stores the followed lanes still reserve %d records, %d store bytes: the panics kept their reservations", plan.ReservedRecords, plan.ReservedBytes)
		}
		time.Sleep(time.Second)
	}
	a.ingestOMMCIDs(t, "sp-ok", "o1", 950_000, 3_000)
	b.awaitRecords(t, "OMM.fbs", "sp-ok", a.scan(t, "OMM.fbs", "sp-ok"), 2*time.Minute)
	for _, al := range b.alerts(t) {
		if al.Kind == "follow_stopped" {
			t.Fatalf("a lane stopped at the running guard after the panicked stores: %s: %s", al.Subject, al.LastError)
		}
	}
	if sub := b.subscription(t, "sp-ok"); sub.Stopped != "" {
		t.Fatalf("lane sp-ok stopped: %s", sub.Stopped)
	}
	t.Log("lane sp-ok kept storing (3,100 records) while the panic lanes retried")
	b.requireAlive(t)
}

// importedPublicationOf reports a line of log that imported a publication of
// standard schema ("Materialized trusted dataset update ... on <schema>").
func importedPublicationOf(log, schema string) string {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "Materialized trusted dataset update") && strings.Contains(line, "on "+schema) {
			return line
		}
	}
	return ""
}

// TestPublicationRestartStreamedPNMRealDaemons: a publication of a lane whose
// followed stream held the batch is left to the stream, and it is not
// imported after a restart either, although the stored-$PNM catch-up (15 s
// after the start) runs before the stream holds the batch again (the
// receiver is built so the lane's first stream opens 40 s after it starts,
// as a producer answering late would). The publication waits for the stream;
// once the stream holds the batch the catch-up runs again and leaves it to
// the stream.
func TestPublicationRestartStreamedPNMRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "late-producer", "", "", "keep-all")
	a.start(t)
	a.ingestOMMCIDs(t, "late-rs", "r1", 930_000, 300)
	b := newPublicationDaemon(t, publicationFollowFaultsBinary(t), "late-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	b.subscribeCurrent(t, a, "late-rs")
	want := a.scan(t, "OMM.fbs", "late-rs")
	b.awaitRecords(t, "OMM.fbs", "late-rs", want, 4*time.Minute)
	held := func(when string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for {
			sub := b.subscription(t, "late-rs")
			if sub.Connected && sub.CurrentBatch == "r1" && sub.Lag == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: lane late-rs connected %t, current batch %q, lag %d; want r1 held", when, sub.Connected, sub.CurrentBatch, sub.Lag)
			}
			time.Sleep(2 * time.Second)
		}
	}
	held("after the subscription")
	a.publish(t, "OMM.fbs", "late-rs", "r1")
	deadline := time.Now().Add(3 * time.Minute)
	for !strings.Contains(readFileString(b.logPath), "Not fetching the publications of OMM.fbs publication-e2e/late-rs") {
		if time.Now().After(deadline) {
			t.Fatal("the receiver's log does not say lane late-rs's publication was left to its stream")
		}
		time.Sleep(2 * time.Second)
	}
	if line := importedPublicationOf(readFileString(b.logPath), "OMM.fbs"); line != "" {
		t.Fatalf("the receiver imported lane late-rs's publication although its stream held the batch: %s", line)
	}
	t.Log("lane late-rs: its publication was left to the stream, which held batch r1")

	b.stop(t)
	mark := len(readFileString(b.logPath))
	b.start(t)
	deadline = time.Now().Add(4 * time.Minute)
	for {
		since := readFileString(b.logPath)[mark:]
		if line := importedPublicationOf(since, "OMM.fbs"); line != "" {
			t.Fatalf("after the restart the receiver imported lane late-rs's publication a second time: %s", line)
		}
		if strings.Contains(since, "Not fetching the publications of OMM.fbs publication-e2e/late-rs") {
			if !strings.Contains(since, "waits for the lane's followed stream") {
				t.Fatal("after the restart the publication was left to the stream, but the log shows no catch-up that waited for the stream: the stream held the batch before the first catch-up, so this run proves nothing")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the restart no catch-up left lane late-rs's publication to its stream; log since the restart:\n%s", tailString(since, 30))
		}
		time.Sleep(2 * time.Second)
	}
	held("after the restart")
	b.requireRecords(t, "OMM.fbs", "late-rs", want)
	t.Log("after the restart the publication waited for the stream and was not imported again; the stream holds batch r1")
	b.requireAlive(t)
}

// TestPublicationPNMGuardRealDaemons: a lane's signed publications are
// imported under the running guard of the followed lanes. A receiver whose
// record ceiling (3) the publication's records would pass does not import it
// (follow_stopped, the lane stopped); with the ceiling raised the stored-$PNM
// catch-up imports it and the alert clears.
func TestPublicationPNMGuardRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "pnmguard-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "pnmguard-receiver", a.address, a.id, "keep-all")
	b.appendConfig(t, "  record_ceiling: 3\n", "subscriptions:\n")
	b.start(t)
	time.Sleep(3 * time.Second)
	// OEM: the producer refuses the stream to a peer it grants no private
	// reads, so the lane imports only through its publications.
	b.follow(t, a, "OEM", "pg", "start=beginning&force=1")
	var body []byte
	for i := 0; i < 6; i++ {
		body = append(body, baselineOEM(200+i)...)
	}
	a.ingest(t, "OEM.fbs", "pg", "g1", body, 6)
	want := a.scan(t, "OEM.fbs", "pg")
	if len(want) != 6 {
		t.Fatalf("producer holds %d OEM records, want 6", len(want))
	}
	a.publish(t, "OEM.fbs", "pg", "g1")
	deadline := time.Now().Add(4 * time.Minute)
	for !hasAlertOn(b.alerts(t), "follow_stopped", "pg") {
		if got := len(b.scan(t, "OEM.fbs", "pg")); got > 0 {
			t.Fatalf("the receiver imported %d records of lane pg past its record ceiling of 3", got)
		}
		if time.Now().After(deadline) {
			t.Fatal("no follow_stopped alert for lane pg after its publication")
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(5 * time.Second)
	if got := len(b.scan(t, "OEM.fbs", "pg")); got > 0 {
		t.Fatalf("the receiver imported %d records of lane pg past its record ceiling of 3", got)
	}
	var stopped api.LaneFollow
	for _, s := range b.subscriptions(t).Subscriptions {
		if s.SourceName == "pg" {
			stopped = s
		}
	}
	if stopped.Stopped == "" {
		t.Fatal("sync subscriptions does not show lane pg stopped")
	}
	t.Logf("lane pg stopped at the guard: %.300s", stopped.Stopped)

	b.stop(t)
	raw, err := os.ReadFile(b.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.cfg, []byte(strings.Replace(string(raw), "  record_ceiling: 3\n", "  record_ceiling: 1000000\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	b.start(t)
	b.awaitRecords(t, "OEM.fbs", "pg", want, 4*time.Minute)
	deadline = time.Now().Add(time.Minute)
	for hasAlertOn(b.alerts(t), "follow_stopped", "pg") {
		if time.Now().After(deadline) {
			t.Fatal("follow_stopped stays on lane pg after its publication was imported")
		}
		time.Sleep(2 * time.Second)
	}
	t.Log("with the ceiling raised the catch-up imported the 6 records and the alert cleared")
	b.requireAlive(t)
}

// TestPublicationSubscriptionsUnreadableRealDaemons: a subscriptions.dss
// that does not read at start is moved aside, not overwritten, and raises
// subscriptions_unreadable; no subscription changes while it is not loaded,
// a restart without a readable file stays so, and a readable copy put back
// loads the subscriptions again.
func TestPublicationSubscriptionsUnreadableRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "unreadable-producer", "", "", "keep-all")
	a.start(t)
	a.ingestOMMCIDs(t, "ur", "r1", 975_000, 100)
	b := newPublicationDaemon(t, exe, "unreadable-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	b.subscribeCurrent(t, a, "ur")
	b.awaitRecords(t, "OMM.fbs", "ur", a.scan(t, "OMM.fbs", "ur"), 3*time.Minute)
	b.stop(t)
	subsPath := filepath.Join(filepath.Dir(b.store), "subscriptions.dss")
	good, err := os.ReadFile(subsPath)
	if err != nil || len(good) == 0 {
		t.Fatalf("subscriptions.dss after the subscription: %v (%d bytes)", err, len(good))
	}
	broken := []byte("this is not a subscriptions file")
	if err := os.WriteFile(subsPath, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	b.start(t)
	aside := func() []string {
		t.Helper()
		names, err := filepath.Glob(subsPath + ".unreadable-*")
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	if !hasAlertOn(b.alerts(t), "subscriptions_unreadable", "subscriptions.dss") {
		t.Fatalf("no subscriptions_unreadable alert after a start on an unreadable subscriptions.dss: %+v", b.alerts(t))
	}
	moved := aside()
	if len(moved) != 1 {
		t.Fatalf("subscriptions.dss moved aside to %v, want one file", moved)
	}
	if raw, err := os.ReadFile(moved[0]); err != nil || !bytes.Equal(raw, broken) {
		t.Fatalf("the file moved aside does not hold the unreadable bytes: %v", err)
	}
	if _, err := os.Stat(subsPath); !os.IsNotExist(err) {
		t.Fatalf("subscriptions.dss exists after it was moved aside (%v): it was written again", err)
	}
	if len(b.subscriptions(t).Subscriptions) != 0 {
		t.Fatal("subscriptions are listed although their file did not load")
	}
	a.ingestOMMCIDs(t, "ur2", "r1", 976_000, 10)
	out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "ur2", "--from", a.id, "--start", "current")
	if err == nil || !strings.Contains(out, "did not load") {
		t.Fatalf("a new subscription was taken while the subscriptions file did not load: %v\n%s", err, out)
	}
	if _, err := os.Stat(subsPath); !os.IsNotExist(err) {
		t.Fatalf("subscriptions.dss was written while it did not load (%v)", err)
	}
	t.Log("unreadable subscriptions.dss: moved aside, alert raised, no subscription changed, nothing written")

	// A restart with the file still missing is not "nothing subscribed".
	b.stop(t)
	b.start(t)
	if !hasAlertOn(b.alerts(t), "subscriptions_unreadable", "subscriptions.dss") {
		t.Fatal("after a restart with subscriptions.dss still moved aside: no subscriptions_unreadable alert")
	}
	if _, err := os.Stat(subsPath); !os.IsNotExist(err) {
		t.Fatalf("subscriptions.dss was written at the restart (%v)", err)
	}
	// A readable copy put back loads the subscription again.
	b.stop(t)
	if err := os.WriteFile(subsPath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	b.start(t)
	if hasAlertOn(b.alerts(t), "subscriptions_unreadable", "subscriptions.dss") {
		t.Fatal("subscriptions_unreadable stays after a readable subscriptions.dss was put back")
	}
	if names := followFileNames(t, b, "subscriptions.dss"); !names["ur"] {
		t.Fatalf("subscriptions.dss holds %v after the readable copy was put back, want ur", names)
	}
	a.ingestOMMCIDs(t, "ur", "r1", 977_000, 20)
	b.awaitRecords(t, "OMM.fbs", "ur", a.scan(t, "OMM.fbs", "ur"), 3*time.Minute)
	t.Log("with the readable copy back the subscription loaded and the lane received the 20 new records")

	// A read that fails (here: no permission) is not a corrupt file: the
	// node stays blocked, naming the error, without moving the file, and
	// loads it by itself at the next start once it reads.
	if os.Geteuid() == 0 {
		t.Log("running as root, which reads a file without permission: the failed-read check is not run")
		b.requireAlive(t)
		return
	}
	setAside := len(aside())
	b.stop(t)
	if err := os.Chmod(subsPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(subsPath, 0o600) })
	b.start(t)
	found := false
	for _, al := range b.alerts(t) {
		if al.Kind == "subscriptions_unreadable" && strings.Contains(al.Subject, "subscriptions.dss") {
			found = true
			if !strings.Contains(al.LastError, "permission denied") || !strings.Contains(al.LastError, "left in place") {
				t.Fatalf("the alert of a subscriptions.dss without read permission does not name the error and that the file stays: %q", al.LastError)
			}
		}
	}
	if !found {
		t.Fatal("no subscriptions_unreadable alert on a start whose subscriptions.dss cannot be read")
	}
	if _, err := os.Stat(subsPath); err != nil {
		t.Fatalf("subscriptions.dss whose read failed is not in place: %v", err)
	}
	if got := len(aside()); got != setAside {
		t.Fatalf("subscriptions.dss whose read failed was moved aside: %d files aside, was %d", got, setAside)
	}
	if len(b.subscriptions(t).Subscriptions) != 0 {
		t.Fatal("subscriptions are listed although their file did not read")
	}
	b.stop(t)
	if err := os.Chmod(subsPath, 0o600); err != nil {
		t.Fatal(err)
	}
	b.start(t)
	if hasAlertOn(b.alerts(t), "subscriptions_unreadable", "subscriptions.dss") {
		t.Fatal("subscriptions_unreadable stays after subscriptions.dss reads again")
	}
	a.ingestOMMCIDs(t, "ur", "r1", 977_500, 20)
	b.awaitRecords(t, "OMM.fbs", "ur", a.scan(t, "OMM.fbs", "ur"), 3*time.Minute)
	t.Log("a subscriptions.dss without read permission: blocked, left in place, loaded by itself once it read again")
	b.requireAlive(t)
}

// setSubscriptionFilter rewrites the subscriptions file at path with lane
// source's SYNC_FILTER set to filter (every other field, and every other
// record, as they were).
func setSubscriptionFilter(t *testing.T, path, source, filter string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := syncwire.SplitFrames(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	found := false
	for _, f := range frames {
		d := DSS.GetSizePrefixedRootAsDSS(f, 0)
		syncFilter := string(d.SYNC_FILTER())
		if string(d.SOURCE_NAME()) == source {
			syncFilter, found = filter, true
		}
		b := flatbuffers.NewBuilder(512)
		str := func(s []byte) flatbuffers.UOffsetT {
			if len(s) == 0 {
				return 0
			}
			return b.CreateByteString(s)
		}
		schema, provider, src, producer := str(d.SCHEMA_NAME()), str(d.PROVIDER_ID()), str(d.SOURCE_NAME()), str(d.PROVIDER_PEER_ID())
		cursor, protocol, filterOff := str(d.CURSOR()), str(d.SYNC_PROTOCOL()), str([]byte(syncFilter))
		DSS.DSSStart(b)
		for _, field := range []struct {
			off flatbuffers.UOffsetT
			add func(*flatbuffers.Builder, flatbuffers.UOffsetT)
		}{{schema, DSS.DSSAddSCHEMA_NAME}, {provider, DSS.DSSAddPROVIDER_ID}, {src, DSS.DSSAddSOURCE_NAME}, {producer, DSS.DSSAddPROVIDER_PEER_ID},
			{cursor, DSS.DSSAddCURSOR}, {protocol, DSS.DSSAddSYNC_PROTOCOL}, {filterOff, DSS.DSSAddSYNC_FILTER}} {
			if field.off != 0 {
				field.add(b, field.off)
			}
		}
		DSS.DSSAddLAST_SYNC_STARTED_AT(b, d.LAST_SYNC_STARTED_AT())
		DSS.DSSAddSYNCED_ROWS(b, d.SYNCED_ROWS())
		DSS.DSSAddLOCAL_ROWS(b, d.LOCAL_ROWS())
		DSS.DSSAddDOWNLOADED_BYTES(b, d.DOWNLOADED_BYTES())
		DSS.DSSAddSUBSCRIBED(b, d.SUBSCRIBED())
		DSS.FinishSizePrefixedDSSBuffer(b, DSS.DSSEnd(b))
		out.Write(b.FinishedBytes())
	}
	if !found {
		t.Fatalf("%s holds no subscription of lane %s", path, source)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPublicationSubscriptionUnknownFieldRealDaemons: a subscription whose
// SYNC_FILTER names a field this build does not know (as a later version
// could write before a rollback) refuses only that subscription
// (subscription_refused naming the lane and the field): the file is not
// treated as unreadable, the other lane keeps importing, the refused lane
// imports nothing, and its record is kept as it was. Unsubscribing it clears
// its alert.
func TestPublicationSubscriptionUnknownFieldRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "unknown-producer", "", "", "keep-all")
	a.start(t)
	a.ingestOMMCIDs(t, "uf-a", "a1", 985_000, 50)
	a.ingestOMMCIDs(t, "uf-b", "b1", 986_000, 50)
	b := newPublicationDaemon(t, exe, "unknown-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	b.subscribeCurrent(t, a, "uf-a")
	b.subscribeCurrent(t, a, "uf-b")
	b.awaitRecords(t, "OMM.fbs", "uf-a", a.scan(t, "OMM.fbs", "uf-a"), 3*time.Minute)
	b.awaitRecords(t, "OMM.fbs", "uf-b", a.scan(t, "OMM.fbs", "uf-b"), 3*time.Minute)
	b.stop(t)
	subsPath := filepath.Join(filepath.Dir(b.store), "subscriptions.dss")
	const later = "later=1&start=current"
	setSubscriptionFilter(t, subsPath, "uf-a", later)
	b.start(t)
	alerts := b.alerts(t)
	if hasAlertOn(alerts, "subscriptions_unreadable", "subscriptions.dss") {
		t.Fatalf("one subscription with an unknown field made the whole file unreadable: %+v", alerts)
	}
	refused := false
	for _, al := range alerts {
		if al.Kind == "subscription_refused" && strings.Contains(al.Subject, "uf-a") && strings.Contains(al.LastError, `"later"`) {
			refused = true
		}
		if al.Kind == "subscription_refused" && strings.Contains(al.Subject, "uf-b") {
			t.Fatalf("lane uf-b is refused too: %s", al.LastError)
		}
	}
	if !refused {
		t.Fatalf("no subscription_refused alert naming lane uf-a and the field \"later\": %+v", alerts)
	}
	if sub := b.subscription(t, "uf-a"); !strings.Contains(sub.Error, `"later"`) {
		t.Fatalf("sync subscriptions does not show why uf-a is not run: %q", sub.Error)
	}
	a.ingestOMMCIDs(t, "uf-b", "b1", 987_000, 20)
	a.ingestOMMCIDs(t, "uf-a", "a1", 988_000, 20)
	b.awaitRecords(t, "OMM.fbs", "uf-b", a.scan(t, "OMM.fbs", "uf-b"), 3*time.Minute)
	time.Sleep(20 * time.Second)
	if got := len(b.scan(t, "OMM.fbs", "uf-a")); got != 50 {
		t.Fatalf("the refused lane uf-a holds %d records, want the 50 it held before", got)
	}
	var kept string
	for _, r := range b.dssFile(t, "subscriptions.dss") {
		if string(r.SOURCE_NAME()) == "uf-a" {
			kept = string(r.SYNC_FILTER())
		}
	}
	if kept != later {
		t.Fatalf("subscriptions.dss holds uf-a's SYNC_FILTER as %q, want it kept as %q", kept, later)
	}
	t.Log("lane uf-a refused (unknown field \"later\"), its record kept; lane uf-b received its 20 new records")
	if out, err := b.cli(t, "sync", "unsubscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "uf-a", "--from", a.id); err != nil {
		t.Fatalf("sync unsubscribe uf-a: %v\n%s", err, out)
	}
	if hasAlertOn(b.alerts(t), "subscription_refused", "uf-a") {
		t.Fatal("subscription_refused stays on lane uf-a after it was unsubscribed")
	}
	b.requireAlive(t)
}

// TestPublicationSubscriptionFileOrderRealDaemons: the subscription files
// are written follows.dss first, then subscriptions.dss. When the first
// write fails, an unsubscribe leaves both files as they were: a lane gone
// from subscriptions.dss but still in follows.dss would come back at the
// next start as a subscription from the beginning.
func TestPublicationSubscriptionFileOrderRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "order-producer", "", "", "keep-all")
	a.start(t)
	a.ingestOMMCIDs(t, "fo", "f1", 978_000, 50)
	b := newPublicationDaemon(t, exe, "order-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "fo", "--from", a.id, "--start", "beginning"); err != nil {
		t.Fatalf("sync subscribe fo: %v\n%s", err, out)
	}
	b.awaitRecords(t, "OMM.fbs", "fo", a.scan(t, "OMM.fbs", "fo"), 3*time.Minute)
	for _, name := range []string{"follows.dss", "subscriptions.dss"} {
		if !followFileNames(t, b, name)["fo"] {
			t.Fatalf("%s does not hold lane fo after its subscription from the beginning", name)
		}
	}
	// follows.dss cannot be written: its temporary file's name is taken by
	// a directory.
	blocker := filepath.Join(filepath.Dir(b.store), "follows.dss.tmp")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := b.cli(t, "sync", "unsubscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "fo", "--from", a.id)
	if err == nil {
		t.Fatalf("the unsubscribe reported success although follows.dss could not be written:\n%s", out)
	}
	subs, follows := followFileNames(t, b, "subscriptions.dss"), followFileNames(t, b, "follows.dss")
	if !subs["fo"] || !follows["fo"] {
		t.Fatalf("after an unsubscribe whose follows.dss write failed: subscriptions.dss holds %v, follows.dss %v; want both to hold fo as before (written first, follows.dss failed before subscriptions.dss was touched)", subs, follows)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	t.Log("an unsubscribe whose first write failed left both files as they were")
	if out, err := b.cli(t, "sync", "unsubscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "fo", "--from", a.id); err != nil {
		t.Fatalf("sync unsubscribe fo: %v\n%s", err, out)
	}
	b.stop(t)
	b.start(t)
	if names := followFileNames(t, b, "subscriptions.dss"); names["fo"] {
		t.Fatalf("lane fo came back after its unsubscribe and a restart: subscriptions.dss holds %v", names)
	}
	if len(b.subscriptions(t).Subscriptions) != 0 {
		t.Fatal("a subscription is listed after the only one was unsubscribed")
	}
	b.requireAlive(t)
}

// TestPublicationDiskFloorRealDaemons: the running guard reserves the bytes
// a fetch by CID of the current batch can store before it reads them, so a
// receiver whose free disk is just above the 1 GiB reserve stops the lane
// (follow_stopped) instead of fetching the batch past the reserve. The
// receiver's store is on a disk image of its own (macOS hdiutil); elsewhere
// the test is skipped.
func TestPublicationDiskFloorRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the receiver's store needs a small file system of its own; this test makes one with macOS hdiutil")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "floor-producer", "", "", "keep-all")
	a.start(t)
	for i := 0; i < 4; i++ {
		a.ingestOMMCIDs(t, "df", "d1", 980_000+i*5_000, 5_000)
	}
	b := newPublicationDaemon(t, exe, "floor-receiver", a.address, a.id, "keep-all")
	image := filepath.Join(publicationTemp(t, "floor-image"), "store.sparseimage")
	mount := publicationTemp(t, "floor-mount")
	if out, err := exec.Command("hdiutil", "create", "-size", "3g", "-type", "SPARSE", "-fs", "HFS+", "-volname", "sdnfloor", image).CombinedOutput(); err != nil {
		t.Fatalf("hdiutil create: %v %s", err, out)
	}
	if out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-noautoopen", "-mountpoint", mount, image).CombinedOutput(); err != nil {
		t.Fatalf("hdiutil attach: %v %s", err, out)
	}
	t.Cleanup(func() {
		b.stop(t)
		if out, err := exec.Command("hdiutil", "detach", mount).CombinedOutput(); err != nil {
			t.Errorf("hdiutil detach %s: %v %s", mount, err, out)
		}
	})
	// The receiver's storage path, and the directory holding it (its
	// subscription files), are on the image.
	onImage := filepath.Join(mount, "node", "store")
	if err := os.MkdirAll(onImage, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(b.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), b.store) {
		t.Fatal("the receiver's config does not name its storage path")
	}
	if err := os.WriteFile(b.cfg, []byte(strings.ReplaceAll(string(raw), b.store, onImage)), 0o600); err != nil {
		t.Fatal(err)
	}
	b.store = onImage
	b.start(t)
	time.Sleep(5 * time.Second)
	// Fill the image until 16 MiB above the reserve is free.
	free := func() int64 {
		t.Helper()
		f, err := storage.FreeDiskBytes(onImage)
		if err != nil {
			t.Fatal(err)
		}
		return int64(f)
	}
	const reserve, margin = int64(1) << 30, int64(16) << 20
	fill := free() - reserve - margin
	if fill <= 0 {
		t.Fatalf("the image has %d bytes free, not more than the reserve and margin", free())
	}
	filler, err := os.Create(filepath.Join(mount, "filler"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte{0x5a}, 8<<20)
	for left := fill; left > 0; left -= int64(len(chunk)) {
		if _, err := filler.Write(chunk[:min(int64(len(chunk)), left)]); err != nil {
			t.Fatal(err)
		}
	}
	if err := filler.Sync(); err != nil {
		t.Fatal(err)
	}
	filler.Close()
	before := free()
	t.Logf("receiver's store: %d bytes free, %d above the 1 GiB reserve", before, before-reserve)
	// Forced past the size check (the batch does not fit), so only the
	// running guard stands between the fetch and the reserve.
	b.subscribeCurrent(t, a, "df", "--force")
	deadline := time.Now().Add(4 * time.Minute)
	for {
		sub := b.subscription(t, "df")
		if sub.Stopped != "" {
			t.Logf("lane df stopped: %.400s", sub.Stopped)
			break
		}
		if time.Now().After(deadline) || sub.CurrentBatch == "d1" {
			t.Fatalf("lane df was not stopped by the running guard: current batch %q, received %d records, free disk %d bytes (%d above the reserve); last error %q",
				sub.CurrentBatch, sub.ReceivedRecords, free(), free()-reserve, sub.Error)
		}
		time.Sleep(2 * time.Second)
	}
	if !hasAlertOn(b.alerts(t), "follow_stopped", "df") {
		t.Fatal("no follow_stopped alert for lane df")
	}
	if after := free(); after < reserve {
		t.Fatalf("the free disk under the store fell to %d bytes, below the 1 GiB reserve", after)
	}
	t.Logf("free disk after the stop: %d bytes (%d above the reserve)", free(), free()-reserve)
	b.requireAlive(t)
}
