package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
)

// TestPublicationSubscribeRealDaemons subscribes real daemons to a
// producer's lanes with the commands an operator runs (sync subscribe,
// sync subscriptions, sync status, sync unsubscribe), each signed in to its
// own daemon with the node's root key.
//
//   - A new subscription to a replace-current lane whose producer holds six
//     batches receives only the current batch (2,000 of the producer's
//     12,000 records, fetched by CID; the receiver's received count is
//     2,000), then the records the producer stores next within seconds.
//   - A keep-all lane starts at its beginning: with the lane (8,000 records)
//     past the receiver's record ceiling (5,000) the subscription is
//     refused, naming both, and nothing is followed; --force subscribes and
//     the whole lane arrives.
//   - sync subscriptions lists the lane with its producer, start time,
//     received records and bytes, current batch and lag; sync status reads
//     through the running daemon; sync unsubscribe stops the lane.
func TestPublicationSubscribeRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "subscribe-producer", "", "", "keep-all")
	a.start(t)

	t.Run("replace_current_starts_at_the_current_batch", func(t *testing.T) {
		for i := 1; i <= 6; i++ {
			a.ingestOMMCIDs(t, "rc", fmt.Sprintf("b%d", i), 300_000+i*10_000, 2_000)
		}
		current := a.scanBatch(t, "OMM.fbs", "rc", "b6")
		if len(current) != 2_000 || len(a.scan(t, "OMM.fbs", "rc")) != 12_000 {
			t.Fatalf("producer holds %d records of batch b6 and %d of the lane, want 2000 and 12000", len(current), len(a.scan(t, "OMM.fbs", "rc")))
		}
		b := newPublicationDaemon(t, exe, "subscribe-rc", a.address, a.id, "replace-current")
		b.start(t)
		time.Sleep(3 * time.Second)
		out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "rc", "--from", a.id)
		if err != nil {
			t.Fatalf("sync subscribe: %v\n%s", err, out)
		}
		for _, want := range []string{"first read:  2000 records", "current batch b6", "lane: 12000 records", "subscribed: OMM.fbs publication-e2e/rc"} {
			if !strings.Contains(out, want) {
				t.Fatalf("sync subscribe output lacks %q:\n%s", want, out)
			}
		}
		started := time.Now()
		b.awaitRecords(t, "OMM.fbs", "rc", current, 3*time.Minute)
		t.Logf("receiver holds exactly batch b6 (2,000 of the producer's 12,000) %s after the subscription", time.Since(started).Round(time.Millisecond))
		sub := b.subscription(t, "rc")
		if sub.ReceivedRecords != 2_000 || sub.Producer != a.id || sub.Since.IsZero() {
			t.Fatalf("subscription after the first read: received %d records, producer %q, since %v; want 2000, %s, set", sub.ReceivedRecords, sub.Producer, sub.Since, a.id)
		}
		// 2,000 element sets of a few hundred bytes each: the batch, not the
		// lane's 12,000.
		if sub.ReceivedBytes < 2_000*200 || sub.ReceivedBytes > 2_000*1_024 {
			t.Fatalf("subscription after the first read received %d bytes, want the bytes of 2,000 element sets", sub.ReceivedBytes)
		}
		t.Logf("records received for the first read: %d (%d bytes)", sub.ReceivedRecords, sub.ReceivedBytes)

		a.ingestOMMCIDs(t, "rc", "b6", 400_000, 500)
		want := a.scanBatch(t, "OMM.fbs", "rc", "b6")
		started = time.Now()
		b.awaitRecordsFast(t, "OMM.fbs", "rc", want, 30*time.Second)
		took := time.Since(started)
		t.Logf("500 live records on the receiver %s after the producer's ingest returned", took.Round(time.Millisecond))
		if took > 15*time.Second {
			t.Fatalf("live records took %s", took)
		}

		deadline := time.Now().Add(time.Minute)
		for {
			sub = b.subscription(t, "rc")
			if sub.CurrentBatch == "b6" && sub.StatedBatch == "b6" && sub.Lag == 0 && sub.Connected {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("sync subscriptions: current batch %q, producer's %q, lag %d, connected %v", sub.CurrentBatch, sub.StatedBatch, sub.Lag, sub.Connected)
			}
			time.Sleep(time.Second)
		}
		var status syncStatusSnapshot
		out, err = b.cli(t, "sync", "status", "--schema", "OMM", "--json")
		if err != nil || json.Unmarshal([]byte(out), &status) != nil {
			t.Fatalf("sync status with the daemon running: %v\n%s", err, out)
		}
		found := false
		for _, row := range status.Results {
			if row.SourceName == "rc" && row.LocalRows == 2_500 {
				found = true
			}
		}
		if !found {
			t.Fatalf("sync status read through the daemon has no lane rc with 2500 rows:\n%s", out)
		}

		out, err = b.cli(t, "sync", "unsubscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "rc", "--from", a.id)
		if err != nil {
			t.Fatalf("sync unsubscribe: %v\n%s", err, out)
		}
		out, err = b.cli(t, "sync", "subscriptions", "--json")
		var list syncSubscriptionList
		if err != nil || json.Unmarshal([]byte(out), &list) != nil || len(list.Subscriptions) != 0 {
			t.Fatalf("sync subscriptions after unsubscribe: %v\n%s", err, out)
		}
		a.ingestOMMCIDs(t, "rc", "b6", 410_000, 100)
		time.Sleep(15 * time.Second)
		b.requireRecords(t, "OMM.fbs", "rc", want)
		t.Log("after sync unsubscribe the producer's next 100 records do not arrive")
	})

	t.Run("keep_all_refused_past_the_ceiling_then_forced", func(t *testing.T) {
		for i := 1; i <= 4; i++ {
			a.ingestOMMCIDs(t, "ka", fmt.Sprintf("k%d", i), 500_000+i*10_000, 2_000)
		}
		want := a.scan(t, "OMM.fbs", "ka")
		if len(want) != 8_000 {
			t.Fatalf("producer holds %d records of lane ka, want 8000", len(want))
		}
		c := newPublicationDaemon(t, exe, "subscribe-ka", a.address, a.id, "keep-all")
		c.appendConfig(t, "  record_ceiling: 5000\n", "subscriptions:\n")
		c.start(t)
		time.Sleep(3 * time.Second)
		out, err := c.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "ka", "--from", a.id)
		if err == nil {
			t.Fatalf("a first read past the record ceiling was not refused:\n%s", out)
		}
		for _, w := range []string{"first read:  8000 records", "the lane's beginning", "ceiling is 5000", "not subscribed"} {
			if !strings.Contains(out, w) {
				t.Fatalf("refusal output lacks %q:\n%s", w, out)
			}
		}
		time.Sleep(5 * time.Second)
		if got := c.scan(t, "OMM.fbs", "ka"); len(got) != 0 {
			t.Fatalf("the refused subscription stored %d records", len(got))
		}
		if list := c.subscriptions(t); len(list.Subscriptions) != 0 {
			t.Fatalf("the refused subscription is listed: %+v", list.Subscriptions)
		}
		t.Log("refused: the keep-all lane's 8,000 records are past this node's ceiling of 5,000")
		out, err = c.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "ka", "--from", a.id, "--force")
		if err != nil || !strings.Contains(out, "subscribed: OMM.fbs publication-e2e/ka") {
			t.Fatalf("sync subscribe --force: %v\n%s", err, out)
		}
		c.awaitRecords(t, "OMM.fbs", "ka", want, 3*time.Minute)
		t.Log("--force: the whole keep-all lane (8,000 records) arrived")
	})
}

// cli runs the daemon's binary as an operator would, against d's config and
// environment; it returns stdout and stderr together.
func (d *publicationDaemon) cli(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(d.exe, append(args, "--config", d.cfg)...)
	cmd.Dir = d.root
	cmd.Env = d.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// subscriptions is `sync subscriptions --json`.
func (d *publicationDaemon) subscriptions(t *testing.T) syncSubscriptionList {
	t.Helper()
	out, err := d.cli(t, "sync", "subscriptions", "--json")
	var list syncSubscriptionList
	if err != nil || json.Unmarshal([]byte(out), &list) != nil {
		t.Fatalf("sync subscriptions: %v\n%s", err, out)
	}
	return list
}

// subscription is the listed subscription of lane (OMM, publication-e2e,
// source).
func (d *publicationDaemon) subscription(t *testing.T, source string) api.LaneFollow {
	t.Helper()
	for _, s := range d.subscriptions(t).Subscriptions {
		if s.SourceName == source {
			return s
		}
	}
	t.Fatalf("no subscription of lane %s", source)
	return api.LaneFollow{}
}

// appendConfig inserts text after the first line equal to after in d's
// config file (before the daemon starts).
func (d *publicationDaemon) appendConfig(t *testing.T, text, after string) {
	t.Helper()
	raw, err := os.ReadFile(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(raw)
	i := strings.Index(cfg, after)
	if i < 0 {
		t.Fatalf("config has no %q", after)
	}
	cfg = cfg[:i+len(after)] + text + cfg[i+len(after):]
	if err := os.WriteFile(d.cfg, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
}
