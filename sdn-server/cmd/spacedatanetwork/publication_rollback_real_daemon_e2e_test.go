package main

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// TestPublicationRollbackRealDaemons updates a 1.1.9 receiver (the binary
// SDN_E2E_V119_BINARY names) to this build and rolls it back to 1.1.9
// before its converted subscription holds a cursor, as an update whose
// health check fails does with nobody logged in, then updates it again.
//
//  1. Both on 1.1.9: the receiver imports lane rb-ledger through
//     peers.trusted_import (its published batch l1, not the unpublished
//     batch lx before it) and follows lane rb-follow.
//  2. The receiver on this build, the producer on 1.1.9: rb-ledger becomes a
//     subscription waiting for the producer's statement, kept in
//     subscriptions.dss only; follows.dss holds rb-follow alone; the ledger
//     stays.
//  3. Rolled back to 1.1.9: the receiver does not read rb-ledger from its
//     beginning (it never holds lx), imports the producer's next
//     publication through its old path, and rb-follow keeps flowing.
//  4. This build again, then the producer updated: the conversion does not
//     run twice, rb-ledger starts at the producer's current batch (still no
//     lx) and is live, and rb-follow is still followed.
func TestPublicationRollbackRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	old := os.Getenv("SDN_E2E_V119_BINARY")
	if old == "" {
		t.Fatal("SDN_E2E_V119_BINARY names a spacedatanetwork binary of release 1.1.9 (scripts/ci-local.sh publication downloads it)")
	}
	exe := publicationBinary(t)
	if out, err := exec119Version(old); err != nil || !strings.Contains(out, "version=1.1.9") {
		t.Fatalf("SDN_E2E_V119_BINARY is not release 1.1.9: %v %q", err, out)
	}
	legacy := "  trusted_import:\n    new_lanes: deny\n    allow:\n" +
		"      - schema: OMM.fbs\n        provider_id: publication-e2e\n        source_name: rb-ledger\n" +
		// 1.1.9 follows a lane only when its import policy admits it.
		"      - schema: OMM.fbs\n        provider_id: publication-e2e\n        source_name: rb-follow\n"
	p := newPublicationDaemon(t, old, "rollback-producer", "", "", "keep-all")
	p.start(t)
	r := newPublicationDaemon(t, old, "rollback-receiver", p.address, p.id, "keep-all")
	r.appendConfig(t, legacy, "peers:\n")
	r.start(t)
	time.Sleep(3 * time.Second)

	// 1. Both on 1.1.9.
	p.ingestOMMCIDs(t, "rb-ledger", "lx", 820_000, 400)
	p.ingestOMMCIDs(t, "rb-ledger", "l1", 821_000, 1_000)
	p.publish(t, "OMM.fbs", "rb-ledger", "l1")
	l1 := p.scanBatch(t, "OMM.fbs", "rb-ledger", "l1")
	r.awaitRecords(t, "OMM.fbs", "rb-ledger", l1, 6*time.Minute)
	p.ingestOMMCIDs(t, "rb-follow", "f1", 830_000, 300)
	r.follow(t, p, "OMM", "rb-follow", "")
	r.awaitRecords(t, "OMM.fbs", "rb-follow", p.scan(t, "OMM.fbs", "rb-follow"), 3*time.Minute)
	ledger := findFile(t, r.root, "trusted-import-lanes.json")
	t.Log("1.1.9: rb-ledger holds its published batch l1 (1,000; not lx's 400), rb-follow is followed")

	// 2. The receiver on this build, the producer on 1.1.9.
	r.stop(t)
	r.exe = exe
	r.start(t)
	if !strings.Contains(readFileString(r.logPath), "Converted import ledger lane OMM.fbs publication-e2e/rb-ledger to a subscription from "+p.id) {
		t.Fatal("the updated receiver did not convert rb-ledger")
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		sub := r.subscription(t, "rb-ledger")
		if sub.Start == "current" && strings.Contains(sub.Error, "statement") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rb-ledger against the 1.1.9 producer: pending %q, error %q; want waiting for the statement", sub.Start, sub.Error)
		}
		time.Sleep(2 * time.Second)
	}
	// The files are checked after the rollback, so that a build that keeps
	// the waiting subscription where 1.1.9 reads it shows what 1.1.9 does.
	inFollows, inSubs := followFileNames(t, r, "follows.dss"), followFileNames(t, r, "subscriptions.dss")
	ledgerText := readFileString(ledger)
	t.Log("this build: rb-ledger waits for the 1.1.9 producer's statement; the ledger is in place")

	// 3. Rolled back to 1.1.9 before the subscription holds a cursor.
	r.stop(t)
	r.exe = old
	r.start(t)
	p.ingestOMMCIDs(t, "rb-ledger", "l2", 840_000, 300)
	p.publish(t, "OMM.fbs", "rb-ledger", "l2")
	l2 := p.scanBatch(t, "OMM.fbs", "rb-ledger", "l2")
	want := unionCounts(l1, l2)
	deadline = time.Now().Add(6 * time.Minute)
	for !containsCounts(r.scan(t, "OMM.fbs", "rb-ledger"), l2) {
		if time.Now().After(deadline) {
			t.Fatal("rolled back to 1.1.9 the receiver did not import publication l2 through its old path")
		}
		time.Sleep(2 * time.Second)
	}
	p.ingestOMMCIDs(t, "rb-follow", "f1", 850_000, 100)
	r.awaitRecords(t, "OMM.fbs", "rb-follow", p.scan(t, "OMM.fbs", "rb-follow"), 3*time.Minute)
	time.Sleep(30 * time.Second)
	if lx := r.scanBatch(t, "OMM.fbs", "rb-ledger", "lx"); len(lx) != 0 {
		t.Fatalf("rolled back to 1.1.9 the receiver read rb-ledger from its beginning: it holds %d records of the unpublished batch lx", len(lx))
	}
	r.requireRecords(t, "OMM.fbs", "rb-ledger", want)
	if !strings.Contains(ledgerText, "rb-ledger") {
		t.Fatalf("the update did not leave the ledger for a rollback: %q", ledgerText)
	}
	if len(inFollows) != 1 || !inFollows["rb-follow"] {
		t.Fatalf("before the rollback follows.dss held %v, want rb-follow alone", inFollows)
	}
	if len(inSubs) != 2 || !inSubs["rb-follow"] || !inSubs["rb-ledger"] {
		t.Fatalf("before the rollback subscriptions.dss held %v, want rb-ledger and rb-follow", inSubs)
	}
	t.Log("rolled back to 1.1.9: rb-ledger imported publication l2 through the old path and never read lx; rb-follow kept flowing")

	// 4. This build again, then the producer updated.
	r.stop(t)
	r.exe = exe
	r.start(t)
	if n := strings.Count(readFileString(r.logPath), "Converted import ledger lane OMM.fbs publication-e2e/rb-ledger"); n != 1 {
		t.Fatalf("the conversion ran %d times, want once", n)
	}
	p.stop(t)
	p.exe = exe
	p.start(t)
	deadline = time.Now().Add(4 * time.Minute)
	for {
		sub := r.subscription(t, "rb-ledger")
		if sub.Start == "" && sub.StartsAt == "current" && sub.Connected && sub.CurrentBatch == "l2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rb-ledger after both updates: pending %q, starts at %q, connected %v, current batch %q, error %q", sub.Start, sub.StartsAt, sub.Connected, sub.CurrentBatch, sub.Error)
		}
		time.Sleep(2 * time.Second)
	}
	p.ingestOMMCIDs(t, "rb-ledger", "l2", 860_000, 200)
	started := time.Now()
	want = unionCounts(l1, p.scanBatch(t, "OMM.fbs", "rb-ledger", "l2"))
	r.awaitRecordsFast(t, "OMM.fbs", "rb-ledger", want, 30*time.Second)
	t.Logf("200 live records of rb-ledger on the receiver %s after the producer's ingest", time.Since(started).Round(time.Millisecond))
	if lx := r.scanBatch(t, "OMM.fbs", "rb-ledger", "lx"); len(lx) != 0 {
		t.Fatalf("after the second update the receiver holds %d records of lx: the subscription read the lane from its beginning", len(lx))
	}
	if sub := r.subscription(t, "rb-follow"); sub.StartsAt != "beginning" {
		t.Fatalf("rb-follow after the round trip starts at %q, want beginning (the 1.1.9 follow)", sub.StartsAt)
	}
	p.ingestOMMCIDs(t, "rb-follow", "f1", 870_000, 50)
	r.awaitRecords(t, "OMM.fbs", "rb-follow", p.scan(t, "OMM.fbs", "rb-follow"), 2*time.Minute)
	r.requireAlive(t)
	t.Log("this build again: rb-ledger started at the producer's current batch l2, live, and never read lx; rb-follow kept")
}

// TestPublicationConversionRecordRealDaemons: the conversion on a data
// folder whose ledger cannot be read converts nothing, writes no record,
// raises subscription_conversion and runs again at the next start; once the
// ledger reads, the record names each removed key the config carries, the
// deny list, and an allow entry naming a peer this node does not trust (not
// subscribed from), and the log and the alert name the removed keys.
func TestPublicationConversionRecordRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	p := newPublicationDaemon(t, exe, "convrec-producer", "", "", "keep-all")
	p.start(t)
	r := newPublicationDaemon(t, exe, "convrec-receiver", p.address, p.id, "keep-all")
	r.start(t)
	r.stop(t)
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	r.appendConfig(t, "  trusted_import:\n    new_lanes: deny\n    allow:\n"+
		"      - peer: "+p.id+"\n        schema: OMM.fbs\n        provider_id: publication-e2e\n        source_name: ce-a\n"+
		"      - peer: "+stranger.String()+"\n        schema: OMM.fbs\n        provider_id: publication-e2e\n        source_name: ce-b\n"+
		"    deny:\n      - schema: CAT.fbs\n        provider_id: publication-e2e\n        source_name: ce-d\n", "peers:\n")
	r.appendConfig(t, "security:\n  require_signed_feed_heads: true\n", "mode: full\n")
	ledger := filepath.Join(r.store, "trusted-import-lanes.json")
	marker := filepath.Join(r.store, "trusted-import-converted")
	if err := os.WriteFile(ledger, []byte("[{\"schema\": \"OMM.fbs\", "), 0o600); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	if !strings.Contains(readFileString(r.logPath), "were not converted to subscriptions (tried again at the next start)") {
		t.Fatal("a ledger that does not parse is not reported")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a ledger that does not parse wrote the conversion record (%v): the conversion would never run again", err)
	}
	if list := r.subscriptions(t); len(list.Subscriptions) != 0 {
		t.Fatalf("a ledger that does not parse converted %d lanes", len(list.Subscriptions))
	}
	if !hasAlert(r.alerts(t), "subscription_conversion", "not converted") {
		t.Fatalf("no subscription_conversion alert for the unreadable ledger: %+v", r.alerts(t))
	}
	t.Log("unreadable ledger: nothing converted, no record, an alert")

	r.stop(t)
	if err := os.WriteFile(ledger, []byte(`[{"schema": "OMM.fbs", "provider_id": "publication-e2e", "source_name": "ce-a"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	record := readFileString(marker)
	for _, want := range []string{
		"removed key peers.trusted_import: ignored", "removed key peers.trusted_import.deny: ignored",
		"removed key security.require_signed_feed_heads: ignored",
		"peers.trusted_import.deny CAT.fbs publication-e2e/ce-d",
		"ce-a from " + p.id + ": subscribed",
		"ce-b from " + stranger.String() + ": not subscribed",
	} {
		if !strings.Contains(record, want) {
			t.Fatalf("the conversion record lacks %q:\n%s", want, record)
		}
	}
	list := r.subscriptions(t)
	if len(list.Subscriptions) != 1 || list.Subscriptions[0].SourceName != "ce-a" || list.Subscriptions[0].Producer != p.id {
		t.Fatalf("subscriptions after the conversion: %+v; want ce-a from the producer alone", list.Subscriptions)
	}
	log := readFileString(r.logPath)
	if !strings.Contains(log, "security.require_signed_feed_heads") || !strings.Contains(log, "peers.trusted_import.deny") {
		t.Fatal("the log does not name the removed keys")
	}
	if !hasAlert(r.alerts(t), "subscription_conversion", "security.require_signed_feed_heads") {
		t.Fatalf("no subscription_conversion alert naming the removed keys: %+v", r.alerts(t))
	}
	t.Logf("conversion record:\n%s", record)
}

func hasAlert(alerts []nodeAlert, kind, text string) bool {
	for _, a := range alerts {
		if a.Kind == kind && strings.Contains(a.LastError, text) {
			return true
		}
	}
	return false
}
