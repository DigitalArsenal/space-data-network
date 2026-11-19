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

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
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
