package main

import (
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A followed lane keeps the batch its producer states as current.
//
//   - TestPublicationFollowRestartKeepsCurrentRealDaemons: a receiver whose
//     publication ledger holds an older batch of a subscribed lane (here a
//     publication the receiver made of the lane itself; on a node that ran
//     1.1.9, the publications its feed-head path imported) keeps the lane's
//     current records across a restart, for a lane followed by a stream and
//     for one imported from its publications alone. The start's retention
//     pass judged the lane from the ledger: the older batch's shard files
//     are still there although its records are gone, so it kept that batch
//     and evicted the current one at every start. The subscription's lag is
//     what the store holds of the producer's batch now, not what it held
//     when the batch was last made current.
//   - TestPublicationFollowOwnBytesRealDaemons: a producer's batch holds
//     records whose bytes the producer first received from the follower
//     itself (it once followed a lane of the follower). The follower holds
//     the batch whole; it refused all of it while the producer named the
//     follower as those records' origin.
//   - TestPublicationFollowBatchGrowsWhileListedRealDaemons: a batch that
//     grows while the follower lists it (records carried over from the
//     previous batch are tagged in place, so the stream is quiet and states
//     the batch while it grows) is held whole, with no refusal and no alert.

// sweepAfter waits until d's log after mark shows the start's retention
// sweep.
func sweepAfter(t *testing.T, d *publicationDaemon, mark int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for !strings.Contains(readFileString(d.logPath)[mark:], "Retention sweep:") {
		if time.Now().After(deadline) {
			t.Fatal("the receiver's log shows no retention sweep after its start")
		}
		time.Sleep(time.Second)
	}
}

// holdsThroughout fails when d's lane source lacks a record of want at any
// read during budget.
func holdsThroughout(t *testing.T, d *publicationDaemon, source string, want map[string]int, budget time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		got := d.scan(t, "OMM.fbs", source)
		held := 0
		for k := range want {
			if got[k] > 0 {
				held++
			}
		}
		if held != len(want) {
			t.Fatalf("%s: the receiver holds %d of the %d records of the lane's current batch", what, held, len(want))
		}
		time.Sleep(time.Second)
	}
}

func TestPublicationFollowRestartKeepsCurrentRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "keepcur-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "keepcur-receiver", a.address, a.id, "replace-current")
	b.start(t)
	time.Sleep(3 * time.Second)

	// The stream lane: batch s1 held, published by the receiver itself (a
	// row of s1 in its ledger, its shard files kept), then replaced by s2.
	a.ingestOMMCIDs(t, "kc-stream", "s1", 400_000, 1_000)
	b.follow(t, a, "OMM", "kc-stream", "")
	b.awaitRecords(t, "OMM.fbs", "kc-stream", a.scanBatch(t, "OMM.fbs", "kc-stream", "s1"), 3*time.Minute)
	b.publish(t, "OMM.fbs", "kc-stream", "s1")
	a.ingestOMMCIDs(t, "kc-stream", "s2", 410_000, 1_000)
	streamWant := a.scanBatch(t, "OMM.fbs", "kc-stream", "s2")
	b.awaitRecords(t, "OMM.fbs", "kc-stream", streamWant, 3*time.Minute)

	// The publications-only lane: p1 imported and published by the receiver
	// itself, then p2 imported.
	a.ingestOMMCIDs(t, "kc-only", "p1", 420_000, 500)
	a.publish(t, "OMM.fbs", "kc-only", "p1")
	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "kc-only", "--from", a.id, "--publications", "only"); err != nil {
		t.Fatalf("sync subscribe --publications only: %v\n%s", err, out)
	}
	p1 := a.scanBatch(t, "OMM.fbs", "kc-only", "p1")
	deadline := time.Now().Add(3 * time.Minute)
	for !containsCounts(b.scan(t, "OMM.fbs", "kc-only"), p1) {
		if time.Now().After(deadline) {
			t.Fatal("the receiver did not import publication p1")
		}
		time.Sleep(2 * time.Second)
	}
	b.publish(t, "OMM.fbs", "kc-only", "p1")
	a.ingestOMMCIDs(t, "kc-only", "p2", 430_000, 500)
	a.publish(t, "OMM.fbs", "kc-only", "p2")
	onlyWant := a.scanBatch(t, "OMM.fbs", "kc-only", "p2")
	deadline = time.Now().Add(3 * time.Minute)
	for !containsCounts(b.scan(t, "OMM.fbs", "kc-only"), onlyWant) {
		if time.Now().After(deadline) {
			t.Fatal("the receiver did not import publication p2")
		}
		time.Sleep(2 * time.Second)
	}

	mark := len(readFileString(b.logPath))
	b.stop(t)
	b.start(t)
	sweepAfter(t, b, mark)
	t.Run("stream_lane_kept", func(t *testing.T) {
		holdsThroughout(t, b, "kc-stream", streamWant, 90*time.Second, "after the restart")
		b.requireRecords(t, "OMM.fbs", "kc-stream", streamWant)
	})
	t.Run("publications_only_lane_kept", func(t *testing.T) {
		holdsThroughout(t, b, "kc-only", onlyWant, 30*time.Second, "after the restart")
	})
	if text := readFileString(b.logPath)[mark:]; strings.Contains(text, "keeping batch s1 as the current set") || strings.Contains(text, "keeping batch p1 as the current set") {
		t.Fatal("the receiver's start kept the batch of its own older publication as a subscribed lane's current set")
	}

	t.Run("lag_is_what_the_store_holds", func(t *testing.T) {
		a.ingestOMMCIDs(t, "kc-lag", "l1", 440_000, 500)
		want := a.scanBatch(t, "OMM.fbs", "kc-lag", "l1")
		b.follow(t, a, "OMM", "kc-lag", "")
		b.awaitRecords(t, "OMM.fbs", "kc-lag", want, 3*time.Minute)
		deadline := time.Now().Add(time.Minute)
		for sub := b.subscription(t, "kc-lag"); sub.CurrentBatch != "l1" || sub.Lag != 0; sub = b.subscription(t, "kc-lag") {
			if time.Now().After(deadline) {
				t.Fatalf("subscription of kc-lag: current batch %q, lag %d; want l1, 0", sub.CurrentBatch, sub.Lag)
			}
			time.Sleep(time.Second)
		}
		// The receiver's operator removes the batch's records: one record
		// of the receiver's own in the lane is the batch kept.
		b.ingestOMMCIDs(t, "kc-lag", "local", 450_000, 1)
		body := `{"schema":"OMM.fbs","provider_id":"publication-e2e","source_name":"kc-lag","keep_batch":"local"}`
		b.request(t, "POST", "/api/v1/admin/store/supersede", "application/json", []byte(body), http.StatusOK)
		sub := b.subscription(t, "kc-lag")
		held := len(b.scanBatch(t, "OMM.fbs", "kc-lag", "l1"))
		if held >= len(want) {
			t.Fatalf("the supersede left %d records of batch l1", held)
		}
		if sub.CurrentBatch == "l1" && sub.Lag != int64(len(want)-held) {
			t.Fatalf("the subscription states batch l1 current with lag %d while the receiver holds %d of its %d records", sub.Lag, held, len(want))
		}
		t.Logf("after the supersede: %d of %d records held, lag %d", held, len(want), sub.Lag)
		deadline = time.Now().Add(3 * time.Minute)
		for !reflect.DeepEqual(b.scanBatch(t, "OMM.fbs", "kc-lag", "l1"), want) {
			if time.Now().After(deadline) {
				t.Fatal("the receiver did not hold batch l1 again")
			}
			time.Sleep(2 * time.Second)
		}
		deadline = time.Now().Add(time.Minute)
		for sub := b.subscription(t, "kc-lag"); sub.Lag != 0; sub = b.subscription(t, "kc-lag") {
			if time.Now().After(deadline) {
				t.Fatalf("lag %d with batch l1 held whole again", sub.Lag)
			}
			time.Sleep(time.Second)
		}
	})
}

func TestPublicationFollowOwnBytesRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	// b is the follower; its own lane ob-origin first holds the three
	// records.
	b := newPublicationDaemon(t, exe, "ownbytes-follower", "", "", "replace-current")
	b.start(t)
	own := ommFrames(460_000, 3)
	b.ingestOMMFrames(t, "ob-origin", "o1", 460_000, own)
	b.ingestOMMCIDs(t, "ob-origin", "o2", 461_000, 10)
	// a, the producer, follows ob-origin from b: it stores the three
	// records with b as the peer they came from.
	a := newPublicationDaemon(t, exe, "ownbytes-producer", b.address, b.id, "keep-all")
	a.start(t)
	time.Sleep(3 * time.Second)
	a.follow(t, b, "OMM", "ob-origin", "start=beginning")
	a.awaitRecords(t, "OMM.fbs", "ob-origin", b.scan(t, "OMM.fbs", "ob-origin"), 3*time.Minute)
	// b no longer holds them.
	body := `{"schema":"OMM.fbs","provider_id":"publication-e2e","source_name":"ob-origin","keep_batch":"o2"}`
	b.request(t, "POST", "/api/v1/admin/store/supersede", "application/json", []byte(body), http.StatusOK)
	if got := b.scanBatch(t, "OMM.fbs", "ob-origin", "o1"); len(got) != 0 {
		t.Fatalf("the follower still holds %d records of batch o1", len(got))
	}
	// a's own lane: batch c1 holds the three records' bytes and 100 more.
	a.ingestOMMFrames(t, "ob-carry", "c1", 460_000, append(append([][]byte(nil), own...), ommFrames(462_000, 100)...))
	want := a.scanBatch(t, "OMM.fbs", "ob-carry", "c1")
	if len(want) != 103 {
		t.Fatalf("the producer holds %d records of batch c1, want 103", len(want))
	}
	// b bootstraps from a, trusts it and follows ob-carry.
	b.stop(t)
	raw, err := os.ReadFile(b.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(raw)
	begin := strings.Index(cfg, "  bootstrap:\n") + len("  bootstrap:\n")
	end := strings.Index(cfg[begin:], "\n") + begin
	if err := os.WriteFile(b.cfg, []byte(cfg[:begin]+fmt.Sprintf("    - %q", a.address)+cfg[end:]), 0600); err != nil {
		t.Fatal(err)
	}
	b.appendConfig(t, fmt.Sprintf("  trusted_peers: [%q]\n", a.address), "peers:\n")
	b.start(t)
	time.Sleep(3 * time.Second)
	mark := len(readFileString(b.logPath))
	b.follow(t, a, "OMM", "ob-carry", "")
	started := time.Now()
	b.awaitRecords(t, "OMM.fbs", "ob-carry", want, 3*time.Minute)
	t.Logf("the follower holds batch c1 (103 records, 3 whose bytes it first stored itself) %s after it subscribed", time.Since(started).Round(time.Millisecond))
	if strings.Contains(readFileString(b.logPath)[mark:], "names another peer") {
		t.Fatal("the follower refused records of the producer's batch as another peer's")
	}
	for _, alert := range b.alerts(t) {
		if alert.Kind == "follow_refused" {
			t.Fatalf("follow_refused alert: %s", alert.LastError)
		}
	}
}

func TestPublicationFollowBatchGrowsWhileListedRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	publicationBinary(t) // first: it selects the store format the daemons use
	// The producer's list pages of a "growlist-" lane wait at a gate
	// (sdn_e2e_follow_faults, storage/follow_faults_e2e.go).
	exe := publicationFollowFaultsBinary(t)
	a := newPublicationDaemon(t, exe, "grow-producer", "", "", "keep-all")
	gates := a.publicationGates(t)
	a.start(t)
	b := newPublicationDaemon(t, exe, "grow-receiver", a.address, a.id, "replace-current")
	b.start(t)
	time.Sleep(3 * time.Second)
	const source = "growlist-g"
	g1 := ommFrames(470_000, 10_000)
	a.ingestOMMFrames(t, source, "g1", 470_000, g1)
	b.follow(t, a, "OMM", source, "")
	b.awaitRecords(t, "OMM.fbs", source, a.scanBatch(t, "OMM.fbs", source, "g1"), 3*time.Minute)

	// g2 carries 9,000 of g1's records over: the stream stays quiet and
	// states g2 with 9,000 records. The receiver's list of g2 waits after
	// its first page while g2 grows to 19,000.
	openPublicationGate(t, gates, source+"-hold")
	mark := len(readFileString(a.logPath))
	bmark := len(readFileString(b.logPath))
	a.ingestOMMFrames(t, source, "g2", 470_000, g1[:9_000])
	deadline := time.Now().Add(3 * time.Minute)
	for !strings.Contains(readFileString(a.logPath)[mark:], "follow fault: a list page of "+source+" waits for its gate") {
		if time.Now().After(deadline) {
			t.Fatal("the receiver did not list batch g2")
		}
		time.Sleep(500 * time.Millisecond)
	}
	a.ingestOMMFrames(t, source, "g2", 479_000, append(append([][]byte(nil), g1[9_000:]...), ommFrames(480_000, 9_000)...))
	want := a.scanBatch(t, "OMM.fbs", source, "g2")
	if len(want) != 19_000 {
		t.Fatalf("the producer holds %d records of batch g2, want 19000", len(want))
	}
	openPublicationGate(t, gates, source+"-go")
	started := time.Now()
	b.awaitRecords(t, "OMM.fbs", source, want, 3*time.Minute)
	t.Logf("batch g2 (stated 9,000 when its list began, 19,000 when it ended) held whole %s after its list went on", time.Since(started).Round(time.Millisecond))
	if text := readFileString(b.logPath)[bmark:]; strings.Contains(text, "lists more than") {
		t.Fatal("the receiver refused the list of a batch that grew while it was listed")
	}
	for _, alert := range b.alerts(t) {
		if alert.Kind == "follow_refused" {
			t.Fatalf("follow_refused alert: %s", alert.LastError)
		}
	}
}
