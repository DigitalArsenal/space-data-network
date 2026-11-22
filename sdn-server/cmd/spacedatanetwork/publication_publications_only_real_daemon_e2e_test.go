package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// followerReads are the producer's log lines of the reads a follower asked
// of lane (OMM, publication-e2e, source): its statement, pages of a batch's
// list, records by CID, followed streams opened.
func followerReads(text, source string) []string {
	re := regexp.MustCompile(`flatsql-sync: (statement|batch list page|records by CID|followed stream opened) of OMM\.fbs publication-e2e/` +
		regexp.QuoteMeta(source) + `( batch \S+)? for .*`)
	return re.FindAllString(text, -1)
}

// requireNoFollowerReads fails when the producer's log since mark holds a
// read a follower asked of lane source.
func requireNoFollowerReads(t *testing.T, p *publicationDaemon, mark int, source, when string) {
	t.Helper()
	if reads := followerReads(readFileString(p.logPath)[mark:], source); len(reads) > 0 {
		t.Fatalf("%s: the producer served %d reads of lane %s, want none:\n%s", when, len(reads), source, strings.Join(reads, "\n"))
	}
}

// awaitFollowerReads waits until the producer's log since mark holds a read
// of each kind of lane source.
func awaitFollowerReads(t *testing.T, p *publicationDaemon, mark int, source string, kinds ...string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		reads := followerReads(readFileString(p.logPath)[mark:], source)
		missing := ""
		for _, k := range kinds {
			found := false
			for _, r := range reads {
				if strings.Contains(r, ": "+k+" of ") {
					found = true
					break
				}
			}
			if !found {
				missing = k
				break
			}
		}
		if missing == "" {
			return reads
		}
		if time.Now().After(deadline) {
			t.Fatalf("the producer's log shows no %s of lane %s; its reads of the lane:\n%s", missing, source, strings.Join(reads, "\n"))
		}
		time.Sleep(time.Second)
	}
}

// subscriptionFilter is the SYNC_FILTER of lane source in d's
// subscriptions.dss, and its cursor.
func subscriptionFilter(t *testing.T, d *publicationDaemon, source string) (string, string) {
	t.Helper()
	for _, r := range d.dssFile(t, "subscriptions.dss") {
		if string(r.SOURCE_NAME()) == source {
			return string(r.SYNC_FILTER()), string(r.CURSOR())
		}
	}
	t.Fatalf("subscriptions.dss holds no subscription of lane %s", source)
	return "", ""
}

// TestPublicationPublicationsOnlyRealDaemons: a lane subscribed with
// `sync subscribe --publications only` is imported from the producer's
// signed publications alone, and the producer serves no read of it.
//
//   - `--start` is refused with it. The subscription is listed
//     "publications only, no stream", kept with stream=none in its
//     SYNC_FILTER and not in follows.dss (the file 1.1.9 follows).
//   - A publication the receiver passed over before it subscribed is
//     imported by the stored-$PNM catch-up within a minute of the
//     subscription.
//   - Each publication of the lane is imported within a minute of the
//     producer publishing it (a stream lane that has not settled waits up
//     to 5 minutes), and the producer's log shows no statement, batch list
//     page, records by CID or followed stream of the lane.
//   - The daemons run with their DHT off: the producer is peered in the
//     receiver's Kubo, so Bitswap asks it for the publications' blocks.
//   - After a restart the lane is still publications only, opens no
//     stream, and imports the next publication within a minute.
//   - `--publications stream` starts the stream at the producer's current
//     batch (its statement, the stream opened, new records on it);
//     `--publications only` again stops it, keeps the cursor, and the
//     producer serves no read of the lane afterwards.
//   - Control: a lane subscribed with `--publications keep` costs the
//     producer its statement, its batch list and a followed stream.
func TestPublicationPublicationsOnlyRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "pubonly-producer", "", "", "keep-all")
	a.start(t)
	for i := 1; i <= 3; i++ {
		a.ingestOMMCIDs(t, "only", fmt.Sprintf("o%d", i), 700_000+i*10_000, 1_000)
	}
	b := newPublicationDaemon(t, exe, "pubonly-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	lane := []string{"sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "only", "--from", a.id}

	// A publication the receiver passes over while it does not subscribe to
	// the lane is stored, and imported once the lane is subscribed.
	want := a.scanBatch(t, "OMM.fbs", "only", "o2")
	a.publish(t, "OMM.fbs", "only", "o2")
	deadline := time.Now().Add(2 * time.Minute)
	for !strings.Contains(readFileString(b.logPath), "Not importing OMM.fbs publication-e2e/only from") {
		if time.Now().After(deadline) {
			t.Fatal("the receiver's log does not show the publication of batch o2 passed over")
		}
		time.Sleep(time.Second)
	}

	out, err := b.cli(t, append(lane, "--publications", "only", "--start", "beginning")...)
	if err == nil || !strings.Contains(out, "takes no --start") {
		t.Fatalf("--publications only --start beginning was not refused: %v\n%s", err, out)
	}
	out, err = b.cli(t, append(lane, "--publications", "only")...)
	if err != nil || !strings.Contains(out, "subscribed: OMM.fbs publication-e2e/only from "+a.id+" (publications only, no stream)") {
		t.Fatalf("sync subscribe --publications only: %v\n%s", err, out)
	}
	subscribed := time.Now()
	sub := b.subscription(t, "only")
	if !sub.PublicationsOnly || sub.Connected || sub.Start != "" {
		t.Fatalf("publications-only subscription listed: publications_only %t, connected %t, pending start %q", sub.PublicationsOnly, sub.Connected, sub.Start)
	}
	if out, err = b.cli(t, "sync", "subscriptions"); err != nil || !strings.Contains(out, "publications only, no stream") {
		t.Fatalf("sync subscriptions does not show the lane as publications only: %v\n%s", err, out)
	}
	if filter, _ := subscriptionFilter(t, b, "only"); !strings.Contains(filter, "stream=none") || strings.Contains(filter, "publications=") {
		t.Fatalf("SYNC_FILTER of the publications-only lane is %q, want stream=none and no publications key", filter)
	}
	if followFileNames(t, b, "follows.dss")["only"] {
		t.Fatal("follows.dss lists the publications-only lane: 1.1.9 would follow it as a stream")
	}
	b.awaitRecordsFast(t, "OMM.fbs", "only", want, 3*time.Minute)
	took := time.Since(subscribed)
	t.Logf("the passed-over publication of batch o2 (1,000 records) imported %s after the subscription", took.Round(time.Millisecond))
	if took > time.Minute {
		t.Fatalf("the passed-over publication took %s to import after the subscription, want under a minute", took)
	}

	want = unionCounts(want, a.scanBatch(t, "OMM.fbs", "only", "o3"))
	a.publish(t, "OMM.fbs", "only", "o3")
	published := time.Now()
	b.awaitRecordsFast(t, "OMM.fbs", "only", want, 3*time.Minute)
	took = time.Since(published)
	t.Logf("publication of batch o3 (1,000 records) imported %s after the producer published it", took.Round(time.Millisecond))
	if took > time.Minute {
		t.Fatalf("the publication took %s to import, want under a minute", took)
	}
	if !kuboPeered(t, b.kubo, a.id) {
		t.Fatalf("the receiver (DHT off) imports a lane from %s but has not peered it in Kubo", a.id)
	}
	requireNoFollowerReads(t, a, 0, "only", "after the first publication")
	t.Log("the producer is peered in the receiver's Kubo and served no read of the lane")

	a.ingestOMMCIDs(t, "only", "o4", 750_000, 1_000)
	want = unionCounts(want, a.scanBatch(t, "OMM.fbs", "only", "o4"))
	a.publish(t, "OMM.fbs", "only", "o4")
	published = time.Now()
	b.awaitRecordsFast(t, "OMM.fbs", "only", want, 3*time.Minute)
	if took = time.Since(published); took > time.Minute {
		t.Fatalf("the next publication took %s to import, want under a minute", took)
	}
	t.Logf("publication of batch o4 imported %s after it was published", took.Round(time.Millisecond))

	b.stop(t)
	b.start(t)
	time.Sleep(20 * time.Second)
	if sub := b.subscription(t, "only"); !sub.PublicationsOnly || sub.Connected {
		t.Fatalf("after the restart: publications_only %t, connected %t", sub.PublicationsOnly, sub.Connected)
	}
	a.ingestOMMCIDs(t, "only", "o5", 760_000, 1_000)
	want = unionCounts(want, a.scanBatch(t, "OMM.fbs", "only", "o5"))
	a.publish(t, "OMM.fbs", "only", "o5")
	published = time.Now()
	b.awaitRecordsFast(t, "OMM.fbs", "only", want, 3*time.Minute)
	if took = time.Since(published); took > time.Minute {
		t.Fatalf("after the restart the publication took %s to import, want under a minute", took)
	}
	t.Logf("after the restart: still publications only; publication of batch o5 imported %s after it was published", took.Round(time.Millisecond))
	// The command repeated without --publications changes nothing and asks
	// the producer nothing: it is refused before the plan's statement read.
	if out, err = b.cli(t, lane...); err == nil || !strings.Contains(out+err.Error(), "is subscribed publications only: nothing is changed and the producer is not asked") {
		t.Fatalf("sync subscribe without --publications on a publications-only lane: %v\n%s", err, out)
	}
	if sub := b.subscription(t, "only"); !sub.PublicationsOnly || sub.Connected {
		t.Fatalf("after the repeated command: publications_only %t, connected %t", sub.PublicationsOnly, sub.Connected)
	}
	requireNoFollowerReads(t, a, 0, "only", "after the restart and the repeated command")

	since := b.subscription(t, "only").Since
	mark := len(readFileString(a.logPath))
	if out, err = b.cli(t, append(lane, "--publications", "stream")...); err != nil {
		t.Fatalf("sync subscribe --publications stream: %v\n%s", err, out)
	}
	reads := awaitFollowerReads(t, a, mark, "only", "statement", "followed stream opened")
	t.Logf("--publications stream: the producer served %d reads of the lane: %s", len(reads), strings.Join(reads, "; "))
	if sub := b.subscription(t, "only"); sub.PublicationsOnly || !sub.Since.Equal(since) {
		t.Fatalf("after --publications stream: publications_only %t, since %v (was %v); want a stream, the same subscription", sub.PublicationsOnly, sub.Since, since)
	}
	deadline = time.Now().Add(2 * time.Minute)
	for !b.subscription(t, "only").Connected {
		if time.Now().After(deadline) {
			t.Fatalf("the stream of the lane did not connect: %+v", b.subscription(t, "only"))
		}
		time.Sleep(time.Second)
	}
	want = b.scan(t, "OMM.fbs", "only")
	a.ingestOMMCIDs(t, "only", "o5", 770_000, 200)
	for digest, n := range a.scanBatch(t, "OMM.fbs", "only", "o5") {
		want[digest] = max(want[digest], n)
	}
	b.awaitRecordsFast(t, "OMM.fbs", "only", want, time.Minute)
	if sub := b.subscription(t, "only"); sub.ReceivedRecords < 200 {
		t.Fatalf("after --publications stream the stream received %d records, want the 200 new ones", sub.ReceivedRecords)
	}
	if followFileNames(t, b, "follows.dss")["only"] {
		t.Fatal("follows.dss lists the lane, which the stream started at the producer's current batch")
	}
	t.Log("the stream started at the producer's current batch and carried its 200 new records")

	if out, err = b.cli(t, append(lane, "--publications", "only")...); err != nil {
		t.Fatalf("sync subscribe --publications only on the followed lane: %v\n%s", err, out)
	}
	if filter, cursor := subscriptionFilter(t, b, "only"); !strings.Contains(filter, "stream=none") || cursor == "" {
		t.Fatalf("after --publications only: SYNC_FILTER %q, cursor %q; want stream=none and the cursor kept", filter, cursor)
	}
	time.Sleep(3 * time.Second)
	mark = len(readFileString(a.logPath))
	a.ingestOMMCIDs(t, "only", "o6", 780_000, 1_000)
	want = unionCounts(b.scan(t, "OMM.fbs", "only"), a.scanBatch(t, "OMM.fbs", "only", "o6"))
	a.publish(t, "OMM.fbs", "only", "o6")
	b.awaitRecordsFast(t, "OMM.fbs", "only", want, 3*time.Minute)
	requireNoFollowerReads(t, a, mark, "only", "after --publications only again")
	t.Log("--publications only again: the stream stopped, the cursor is kept, publication o6 imported, and the producer served no read of the lane")

	// Control: a kept lane costs the producer its reads.
	a.ingestOMMCIDs(t, "kept", "k1", 800_000, 1_000)
	mark = len(readFileString(a.logPath))
	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "kept", "--from", a.id,
		"--start", "current", "--publications", "keep"); err != nil {
		t.Fatalf("sync subscribe kept: %v\n%s", err, out)
	}
	reads = awaitFollowerReads(t, a, mark, "kept", "statement", "followed stream opened", "batch list page")
	t.Logf("control: the producer served %d reads of the kept lane", len(reads))
	b.requireAlive(t)
}

// holdFailure is a receiver's log line of a failed hold of lane hb's batch,
// with its time.
var holdFailure = regexp.MustCompile(`(?m)^(\S+)\s+WARN\s+\S+\s+\S+\s+Followed lane OMM\.fbs publication-e2e/hb from \S+: "hold the records of batch`)

// TestPublicationHoldBackoffRealDaemons: a followed lane whose producer's
// current batch it cannot hold (the producer serves a lane it imported
// itself, so every record names another peer as its producer and each fetch
// by CID is refused) asks for the batch again under the stream's backoff,
// with its stream connected. Before 1.1.13 it asked again at the first quiet
// page 30 s after each failure, for as long as the hold failed: on a large
// keep-all lane each ask costs the producer a walk of the lane per page of
// the batch's list. Here a wait of 50 s or more between two failed holds
// appears within 8 minutes.
func TestPublicationHoldBackoffRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	c := newPublicationDaemon(t, exe, "holdbackoff-origin", "", "", "keep-all")
	c.start(t)
	c.ingestOMMCIDs(t, "hb", "h1", 990_000, 300)
	a := newPublicationDaemon(t, exe, "holdbackoff-producer", c.address, c.id, "keep-all")
	a.start(t)
	time.Sleep(3 * time.Second)
	a.subscribeCurrent(t, c, "hb")
	a.awaitRecords(t, "OMM.fbs", "hb", c.scan(t, "OMM.fbs", "hb"), 3*time.Minute)
	b := newPublicationDaemon(t, exe, "holdbackoff-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	b.subscribeCurrent(t, a, "hb")
	deadline := time.Now().Add(8 * time.Minute)
	for {
		var at []time.Time
		for _, m := range holdFailure.FindAllStringSubmatch(readFileString(b.logPath), -1) {
			if ts, err := time.Parse("2006-01-02T15:04:05.000Z0700", m[1]); err == nil {
				at = append(at, ts)
			}
		}
		var gaps []string
		var longest time.Duration
		for i := 1; i < len(at); i++ {
			gap := at[i].Sub(at[i-1])
			longest = max(longest, gap)
			gaps = append(gaps, gap.Round(time.Second).String())
		}
		if longest >= 50*time.Second {
			t.Logf("failed holds: %d; waits between them: %s", len(at), strings.Join(gaps, ", "))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("8 minutes after the subscription: %d failed holds, waits %s; want one of 50 s or more", len(at), strings.Join(gaps, ", "))
		}
		time.Sleep(2 * time.Second)
	}
	sub := b.subscription(t, "hb")
	if !sub.Connected || !strings.Contains(sub.Error, "asked for again in") {
		t.Fatalf("lane hb: connected %t, last error %q; want the stream connected and the hold's next ask named", sub.Connected, sub.Error)
	}
	t.Logf("lane hb: stream connected; last error %.300s", sub.Error)
	t.Logf("the producer served %d pages of the batch's list", strings.Count(readFileString(a.logPath), "batch list page of OMM.fbs publication-e2e/hb"))
	b.requireAlive(t)
}
