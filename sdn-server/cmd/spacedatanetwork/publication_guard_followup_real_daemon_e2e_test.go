package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// alertOn is the active alert of kind whose subject contains text.
func alertOn(alerts []nodeAlert, kind, text string) (nodeAlert, bool) {
	for _, a := range alerts {
		if a.Kind == kind && strings.Contains(a.Subject, text) {
			return a, true
		}
	}
	return nodeAlert{}, false
}

// TestPublicationMisstatedSizesRealDaemons: a producer built to misstate its
// publications' sizes (sdn_e2e_follow_faults, manifest_faults_e2e.go) signs
// DPMs that state a shard of 3,689,348,814,741,910,324 bytes (times 5 that
// wraps an int64 to 4), half the shard's bytes, or one record fewer than the
// shard holds. The receiver imports these OEM lanes only through their
// publications (the producer refuses their stream to a peer it grants no
// private reads). An honest lane of the same producer imports; the
// overstated one stops at the running guard (follow_stopped naming the
// stated size), and the understated ones are refused with
// publication_misstated naming both figures. None of the misstated lanes'
// records is stored.
func TestPublicationMisstatedSizesRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t) // first: it selects the store format the daemons use
	a := newPublicationDaemon(t, publicationFollowFaultsBinary(t), "misstate-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "misstate-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	lanes := []string{"honest-ms", "overstate-ms", "understate-bytes-ms", "understate-records-ms"}
	want := map[string]map[string]int{}
	for i, source := range lanes {
		b.follow(t, a, "OEM", source, "start=beginning&force=1")
		var body []byte
		for j := 0; j < 3; j++ {
			body = append(body, baselineOEM(300+10*i+j)...)
		}
		a.ingest(t, "OEM.fbs", source, "m1", body, 3)
		want[source] = a.scan(t, "OEM.fbs", source)
		if len(want[source]) != 3 {
			t.Fatalf("producer holds %d OEM records of lane %s, want 3", len(want[source]), source)
		}
	}
	for _, source := range lanes {
		a.publish(t, "OEM.fbs", source, "m1")
	}
	b.awaitRecords(t, "OEM.fbs", "honest-ms", want["honest-ms"], 4*time.Minute)
	t.Log("the honest lane's publication imported: 3 records")

	deadline := time.Now().Add(4 * time.Minute)
	for {
		alerts := b.alerts(t)
		over, overOK := alertOn(alerts, "follow_stopped", "overstate-ms")
		bytesAlert, bytesOK := alertOn(alerts, "publication_misstated", "understate-bytes-ms")
		recAlert, recOK := alertOn(alerts, "publication_misstated", "understate-records-ms")
		for _, source := range lanes[1:] {
			if got := len(b.scan(t, "OEM.fbs", source)); got > 0 {
				t.Fatalf("the receiver stored %d records of lane %s, whose publication misstates its size", got, source)
			}
		}
		if overOK && bytesOK && recOK {
			if !strings.Contains(over.LastError, "3689348814741910324") {
				t.Fatalf("the stop of the overstated lane does not name the stated size: %s", over.LastError)
			}
			if !strings.Contains(bytesAlert.LastError, "states a shard of") || !strings.Contains(bytesAlert.LastError, "bytes") {
				t.Fatalf("the understated-bytes alert does not name both figures: %s", bytesAlert.LastError)
			}
			if !strings.Contains(recAlert.LastError, "states 2 records and its index lists 3") {
				t.Fatalf("the understated-records alert does not name both figures: %s", recAlert.LastError)
			}
			t.Logf("overstated: %.300s", over.LastError)
			t.Logf("understated bytes: %.400s", bytesAlert.LastError)
			t.Logf("understated records: %.400s", recAlert.LastError)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 4 minutes: follow_stopped on overstate-ms %t, publication_misstated on understate-bytes-ms %t, on understate-records-ms %t; alerts %+v", overOK, bytesOK, recOK, alerts)
		}
		time.Sleep(2 * time.Second)
	}
	// The catch-up runs 15 s after the start and every 5 minutes: a restart
	// tries the stored publications again, and they stay refused.
	b.stop(t)
	b.start(t)
	time.Sleep(40 * time.Second)
	for _, source := range lanes[1:] {
		if got := len(b.scan(t, "OEM.fbs", source)); got > 0 {
			t.Fatalf("after a restart the receiver stored %d records of lane %s, whose publication misstates its size", got, source)
		}
	}
	b.requireRecords(t, "OEM.fbs", "honest-ms", want["honest-ms"])
	t.Log("after a restart and a catch-up: no record of the misstated lanes stored")
	b.requireAlive(t)
}

// subscriptionFrame is lane source's record in the subscriptions file at
// path, as stored.
func subscriptionFrame(t *testing.T, path, source string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := syncwire.SplitFrames(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		if string(DSS.GetSizePrefixedRootAsDSS(f, 0).SOURCE_NAME()) == source {
			return f
		}
	}
	t.Fatalf("%s holds no subscription of lane %s", path, source)
	return nil
}

// setSubscriptionRecord rewrites lane source's record in the subscriptions
// file at path with SYNC_FILTER filter and every field a node writes set
// (status, last synced time, error, rates and times), every other record as
// it was. It returns the record written.
func setSubscriptionRecord(t *testing.T, path, source, filter string) []byte {
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
	var written []byte
	for _, f := range frames {
		d := DSS.GetSizePrefixedRootAsDSS(f, 0)
		if string(d.SOURCE_NAME()) != source {
			out.Write(f)
			continue
		}
		b := flatbuffers.NewBuilder(1024)
		str := func(s []byte) flatbuffers.UOffsetT {
			if len(s) == 0 {
				return 0
			}
			return b.CreateByteString(s)
		}
		schema, provider, src, producer := str(d.SCHEMA_NAME()), str(d.PROVIDER_ID()), str(d.SOURCE_NAME()), str(d.PROVIDER_PEER_ID())
		cursor, protocol, filterOff := str(d.CURSOR()), str(d.SYNC_PROTOCOL()), str([]byte(filter))
		lastAt := b.CreateString("2026-11-01T02:03:04.123456789Z")
		errText := b.CreateString("stored error: the producer was unreachable at 2026-11-01T02:03:04Z")
		DSS.DSSStart(b)
		DSS.DSSAddSTATUS(b, DSS.EnumValuesdssSyncState["SYNCING"])
		for _, field := range []struct {
			off flatbuffers.UOffsetT
			add func(*flatbuffers.Builder, flatbuffers.UOffsetT)
		}{{schema, DSS.DSSAddSCHEMA_NAME}, {provider, DSS.DSSAddPROVIDER_ID}, {src, DSS.DSSAddSOURCE_NAME}, {producer, DSS.DSSAddPROVIDER_PEER_ID},
			{cursor, DSS.DSSAddCURSOR}, {protocol, DSS.DSSAddSYNC_PROTOCOL}, {filterOff, DSS.DSSAddSYNC_FILTER},
			{lastAt, DSS.DSSAddLAST_SYNCED_AT}, {errText, DSS.DSSAddERROR}} {
			if field.off != 0 {
				field.add(b, field.off)
			}
		}
		DSS.DSSAddLAST_SYNC_STARTED_AT(b, d.LAST_SYNC_STARTED_AT())
		DSS.DSSAddSYNCED_ROWS(b, d.SYNCED_ROWS())
		DSS.DSSAddLOCAL_ROWS(b, d.LOCAL_ROWS())
		DSS.DSSAddDOWNLOADED_BYTES(b, d.DOWNLOADED_BYTES())
		DSS.DSSAddDOWNLOAD_SPEED_BYTES_PER_SECOND(b, 12345)
		DSS.DSSAddNETWORK_TRANSFER_MS(b, 55)
		DSS.DSSAddVERIFICATION_MS(b, 66)
		DSS.DSSAddFLATSQL_MATERIALIZATION_MS(b, 77)
		DSS.DSSAddSUBSCRIBED(b, d.SUBSCRIBED())
		DSS.FinishSizePrefixedDSSBuffer(b, DSS.DSSEnd(b))
		written = append([]byte(nil), b.FinishedBytes()...)
		out.Write(written)
	}
	if written == nil {
		t.Fatalf("%s holds no subscription of lane %s", path, source)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return written
}

// refusedSubscriptionRun subscribes receiver b to lanes refused and other
// of producer a, stops b, rewrites refused's record with filter (and every
// field set), and starts b again. It returns the record written.
func refusedSubscriptionRun(t *testing.T, a, b *publicationDaemon, refused, other, filter string) []byte {
	t.Helper()
	a.ingestOMMCIDs(t, refused, "a1", 989_000, 50)
	a.ingestOMMCIDs(t, other, "b1", 990_000, 50)
	b.start(t)
	time.Sleep(3 * time.Second)
	b.subscribeCurrent(t, a, refused)
	b.subscribeCurrent(t, a, other)
	b.awaitRecords(t, "OMM.fbs", refused, a.scan(t, "OMM.fbs", refused), 3*time.Minute)
	b.awaitRecords(t, "OMM.fbs", other, a.scan(t, "OMM.fbs", other), 3*time.Minute)
	b.stop(t)
	written := setSubscriptionRecord(t, filepath.Join(filepath.Dir(b.store), "subscriptions.dss"), refused, filter)
	b.start(t)
	return written
}

// TestPublicationSubscriptionFilterValueRealDaemons: a SYNC_FILTER whose
// known field holds a value this build does not read, beside a field it
// does not know ("start=<a later start>&x=1", as a later version could
// write before a rollback), refuses only that subscription
// (subscription_refused naming the lane): the file is not treated as
// unreadable, the other lane keeps importing, and the refused lane imports
// nothing.
func TestPublicationSubscriptionFilterValueRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "fvalue-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "fvalue-receiver", a.address, a.id, "keep-all")
	const later = "start=from-the-next-batch&x=1"
	refusedSubscriptionRun(t, a, b, "fv-a", "fv-b", later)
	alerts := b.alerts(t)
	if hasAlertOn(alerts, "subscriptions_unreadable", "subscriptions.dss") {
		t.Fatalf("one subscription whose filter value this build does not read made the whole file unreadable: %+v", alerts)
	}
	al, ok := alertOn(alerts, "subscription_refused", "fv-a")
	if !ok || !strings.Contains(al.LastError, "from-the-next-batch") {
		t.Fatalf("no subscription_refused alert on lane fv-a naming its filter: %+v", alerts)
	}
	if _, ok := alertOn(alerts, "subscription_refused", "fv-b"); ok {
		t.Fatal("lane fv-b is refused too")
	}
	t.Logf("lane fv-a refused: %.300s", al.LastError)
	a.ingestOMMCIDs(t, "fv-b", "b1", 991_000, 20)
	a.ingestOMMCIDs(t, "fv-a", "a1", 992_000, 20)
	b.awaitRecords(t, "OMM.fbs", "fv-b", a.scan(t, "OMM.fbs", "fv-b"), 3*time.Minute)
	time.Sleep(20 * time.Second)
	if got := len(b.scan(t, "OMM.fbs", "fv-a")); got != 50 {
		t.Fatalf("the refused lane fv-a holds %d records, want the 50 it held before", got)
	}
	var kept string
	for _, r := range b.dssFile(t, "subscriptions.dss") {
		if string(r.SOURCE_NAME()) == "fv-a" {
			kept = string(r.SYNC_FILTER())
		}
	}
	if kept != later {
		t.Fatalf("subscriptions.dss holds fv-a's SYNC_FILTER as %q, want %q", kept, later)
	}
	t.Log("lane fv-a refused, its record kept; lane fv-b received its 20 new records")
	b.requireAlive(t)
}

// TestPublicationRefusedSubscriptionKeptRealDaemons: a refused subscription
// (a field this build does not know) is written back byte for byte when
// the file is written again (here by the other lane's saved cursor): its
// status, last synced time, error, rates and times as the file held them,
// and its stored ERROR is not replaced by the refusal, which is in the
// alert and the listing.
func TestPublicationRefusedSubscriptionKeptRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "fkept-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "fkept-receiver", a.address, a.id, "keep-all")
	subsPath := filepath.Join(filepath.Dir(b.store), "subscriptions.dss")
	written := refusedSubscriptionRun(t, a, b, "fk-a", "fk-b", "later=1&start=current")
	if !hasAlertOn(b.alerts(t), "subscription_refused", "fk-a") {
		t.Fatalf("no subscription_refused alert on lane fk-a: %+v", b.alerts(t))
	}
	if sub := b.subscription(t, "fk-a"); !strings.Contains(sub.Error, `"later"`) {
		t.Fatalf("sync subscriptions does not show why fk-a is not run: %q", sub.Error)
	}
	cursorBefore := string(DSS.GetSizePrefixedRootAsDSS(subscriptionFrame(t, subsPath, "fk-b"), 0).CURSOR())
	a.ingestOMMCIDs(t, "fk-b", "b1", 993_000, 20)
	b.awaitRecords(t, "OMM.fbs", "fk-b", a.scan(t, "OMM.fbs", "fk-b"), 3*time.Minute)
	deadline := time.Now().Add(time.Minute)
	for string(DSS.GetSizePrefixedRootAsDSS(subscriptionFrame(t, subsPath, "fk-b"), 0).CURSOR()) == cursorBefore {
		if time.Now().After(deadline) {
			t.Fatal("subscriptions.dss was not written again after lane fk-b stored 20 records (its cursor is the same): this run proves nothing")
		}
		time.Sleep(time.Second)
	}
	t.Log("subscriptions.dss was written again (lane fk-b's cursor moved)")
	b.stop(t)
	got := subscriptionFrame(t, subsPath, "fk-a")
	if !bytes.Equal(got, written) {
		d := DSS.GetSizePrefixedRootAsDSS(got, 0)
		t.Fatalf("the refused subscription's record changed when the file was written again (%d bytes, was %d): ERROR %q, LAST_SYNCED_AT %q, STATUS %d, rate %d, transfer %d ms, verify %d ms, store %d ms, SYNC_FILTER %q",
			len(got), len(written), d.ERROR(), d.LAST_SYNCED_AT(), d.STATUS(), d.DOWNLOAD_SPEED_BYTES_PER_SECOND(), d.NETWORK_TRANSFER_MS(), d.VERIFICATION_MS(), d.FLATSQL_MATERIALIZATION_MS(), d.SYNC_FILTER())
	}
	t.Log("the refused subscription's record is in subscriptions.dss byte for byte as it was, its stored ERROR included")
}

// TestPublicationDeferredWaitBoundRealDaemons: the publications of a lane
// whose followed stream never connects (a receiver built so lane "never-..."
// opens no stream) wait at most 5 minutes after the lane starts. A nudge of
// the stored-$PNM catch-up from another lane (here "late270-...", whose
// stream connects 270 s after it starts and whose publication waited) runs
// the catch-up and resets its timer to its 5-minute interval; the waiting
// lane's publication is imported when its 5 minutes end all the same, not
// 5 minutes after the nudge.
func TestPublicationDeferredWaitBoundRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	a := newPublicationDaemon(t, publicationBinary(t), "wait-producer", "", "", "keep-all")
	a.start(t)
	a.ingestOMMCIDs(t, "never-w", "n1", 994_000, 50)
	a.ingestOMMCIDs(t, "late270-w", "l1", 995_000, 50)
	b := newPublicationDaemon(t, publicationFollowFaultsBinary(t), "wait-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	b.subscribeCurrent(t, a, "late270-w")
	b.subscribeCurrent(t, a, "never-w")
	started := time.Now()
	a.publish(t, "OMM.fbs", "late270-w", "l1")
	a.publish(t, "OMM.fbs", "never-w", "n1")
	want := a.scan(t, "OMM.fbs", "never-w")
	const bound = 5*time.Minute + 50*time.Second
	for {
		if len(b.scan(t, "OMM.fbs", "never-w")) == len(want) {
			break
		}
		if time.Since(started) > bound {
			settled := false
			for _, line := range strings.Split(readFileString(b.logPath), "\n") {
				if strings.Contains(line, "publication-e2e/late270-w from") && strings.Contains(line, "batch l1 is current") {
					settled = true
				}
			}
			t.Fatalf("lane never-w's publication was not imported %s after the lane started (the bound is 5 minutes); lane late270-w's stream held its batch (its nudge ran the catch-up): %t",
				bound, settled)
		}
		time.Sleep(2 * time.Second)
	}
	took := time.Since(started).Round(time.Second)
	b.requireRecords(t, "OMM.fbs", "never-w", want)
	waited := map[string]bool{}
	for _, line := range strings.Split(readFileString(b.logPath), "\n") {
		if strings.Contains(line, "waits for the lane's followed stream") {
			for _, source := range []string{"never-w", "late270-w"} {
				if strings.Contains(line, "publication-e2e/"+source+" ") {
					waited[source] = true
				}
			}
		}
	}
	if !waited["never-w"] || !waited["late270-w"] {
		t.Fatalf("the receiver's log shows the publications waiting for their streams: never-w %t, late270-w %t; both must, or this run proves nothing", waited["never-w"], waited["late270-w"])
	}
	if took < 4*time.Minute {
		t.Fatalf("lane never-w's publication was imported %s after the lane started, before its 5-minute wait: this run proves nothing", took)
	}
	t.Logf("lane never-w's publication imported %s after the lane started (its stream never connected)", took)
	b.requireAlive(t)
}
