package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// TestPublicationUpgradeRealDaemons updates real daemons from 1.1.9 (the
// binary SDN_E2E_V119_BINARY names: a build of tag v1.1.9) to this build,
// one at a time, the way the production hosts update.
//
//  1. Both on 1.1.9: the receiver imports two lanes of the producer through
//     the old gate (peers.trusted_import, an allow list naming one lane and
//     a provider-wide selector); its lane ledger lists both. A second
//     1.1.9 receiver follows a third lane over the followed stream.
//  2. The receiver alone updated, on its 1.1.9 data folder and config: it
//     starts, converts the ledger and the allow list to two subscriptions
//     (the provider-wide selector is reported, not converted), renames the
//     ledger, and loses no record. Its producer still runs 1.1.9, which
//     states no lane: the converted subscriptions wait, naming why; a new
//     subscription is refused for the same reason, and --force --start
//     beginning follows the 1.1.9 producer anyway. The old path is gone: a
//     lane the provider-wide selector admitted on 1.1.9 (a 1.1.9 control
//     receiver imports it through feed heads) is not imported.
//  3. The producer updated too: the waiting subscriptions start at the
//     producer's current batch (records already held are not sent again:
//     the received count is the live records only), every lane the receiver
//     imported keeps flowing, each record once, byte for byte; the 1.1.9
//     receiver's followed lane keeps flowing from the updated producer.
func TestPublicationUpgradeRealDaemons(t *testing.T) {
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
		"      - schema: OMM.fbs\n        provider_id: publication-e2e\n        source_name: upg-allow\n" +
		"      - schema: OMM.fbs\n        provider_id: publication-e2e\n"

	p := newPublicationDaemon(t, old, "upgrade-producer", "", "", "keep-all")
	p.start(t)
	r := newPublicationDaemon(t, old, "upgrade-receiver", p.address, p.id, "keep-all")
	r.appendConfig(t, legacy, "peers:\n")
	r.start(t)
	r0 := newPublicationDaemon(t, old, "upgrade-control", p.address, p.id, "keep-all")
	r0.appendConfig(t, legacy, "peers:\n")
	r0.start(t)
	r2 := newPublicationDaemon(t, old, "upgrade-follower", p.address, p.id, "keep-all")
	r2.appendConfig(t, legacy, "peers:\n")
	r2.start(t)
	time.Sleep(3 * time.Second)

	// 1. Both on 1.1.9.
	p.ingestOMMCIDs(t, "upg-allow", "a1", 600_000, 1_000)
	p.ingestOMMCIDs(t, "upg-ledger", "l1", 610_000, 1_000)
	p.ingestOMMCIDs(t, "upg-follow", "f1", 620_000, 1_000)
	p.publish(t, "OMM.fbs", "upg-allow", "a1")
	p.publish(t, "OMM.fbs", "upg-ledger", "l1")
	r.awaitRecords(t, "OMM.fbs", "upg-allow", p.scan(t, "OMM.fbs", "upg-allow"), 6*time.Minute)
	r.awaitRecords(t, "OMM.fbs", "upg-ledger", p.scan(t, "OMM.fbs", "upg-ledger"), 6*time.Minute)
	ledger := findFile(t, r.root, "trusted-import-lanes.json")
	if text := readFileString(ledger); !strings.Contains(text, "upg-allow") || !strings.Contains(text, "upg-ledger") {
		t.Fatalf("the 1.1.9 ledger does not list both lanes: %s", text)
	}
	r2.follow(t, p, "OMM", "upg-follow", "")
	r2.awaitRecords(t, "OMM.fbs", "upg-follow", p.scan(t, "OMM.fbs", "upg-follow"), 3*time.Minute)
	t.Log("1.1.9: the receiver imported two lanes through peers.trusted_import and its ledger; a 1.1.9 follower follows a third")

	// 2. The receiver updated; the producer still on 1.1.9.
	r.stop(t)
	r.exe = exe
	r.start(t)
	log := readFileString(r.logPath)
	for _, want := range []string{"Converted import ledger lane OMM.fbs publication-e2e/upg-allow to a subscription from " + p.id,
		"Converted import ledger lane OMM.fbs publication-e2e/upg-ledger to a subscription from " + p.id,
		"an allow selector that does not name one lane"} {
		if !strings.Contains(log, want) {
			t.Fatalf("the updated receiver's log lacks %q", want)
		}
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Fatalf("the ledger %s is still in place: %v", ledger, err)
	}
	if _, err := os.Stat(ledger + ".converted"); err != nil {
		t.Fatalf("the ledger was not renamed: %v", err)
	}
	record := readFileString(filepath.Join(filepath.Dir(ledger), "trusted-import-converted"))
	if !strings.Contains(record, "upg-allow from "+p.id+": subscribed") || !strings.Contains(record, "upg-ledger from "+p.id+": subscribed") {
		t.Fatalf("conversion record: %s", record)
	}
	for _, d := range r.followRecords(t) {
		if string(d.PROVIDER_PEER_ID()) != p.id || !d.SUBSCRIBED() {
			t.Fatalf("converted follows.dss record: producer %q subscribed %v", d.PROVIDER_PEER_ID(), d.SUBSCRIBED())
		}
	}
	r.requireRecords(t, "OMM.fbs", "upg-allow", p.scan(t, "OMM.fbs", "upg-allow"))
	r.requireRecords(t, "OMM.fbs", "upg-ledger", p.scan(t, "OMM.fbs", "upg-ledger"))
	deadline := time.Now().Add(2 * time.Minute)
	for {
		list := r.subscriptions(t)
		waiting := 0
		for _, s := range list.Subscriptions {
			if s.Start == "current" && strings.Contains(s.Error, "statement") {
				waiting++
			}
		}
		if len(list.Subscriptions) == 2 && waiting == 2 {
			t.Logf("mixed versions: both converted subscriptions wait for the 1.1.9 producer's statement: %q", list.Subscriptions[0].Error)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("converted subscriptions against a 1.1.9 producer: %+v", list.Subscriptions)
		}
		time.Sleep(2 * time.Second)
	}
	p.ingestOMMCIDs(t, "upg-x", "x1", 630_000, 500)
	out, err := r.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "upg-x", "--from", p.id)
	if err == nil || !strings.Contains(out, "did not state the lane") {
		t.Fatalf("a new subscription to a 1.1.9 producer: %v\n%s", err, out)
	}
	out, err = r.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e", "--source-name", "upg-x", "--from", p.id, "--force", "--start", "beginning")
	if err != nil {
		t.Fatalf("sync subscribe --force --start beginning from a 1.1.9 producer: %v\n%s", err, out)
	}
	r.awaitRecords(t, "OMM.fbs", "upg-x", p.scan(t, "OMM.fbs", "upg-x"), 3*time.Minute)
	t.Log("mixed versions: a new subscription to the 1.1.9 producer is refused (no statement); --force --start beginning follows it")

	p.ingestOMMCIDs(t, "upg-new", "n1", 640_000, 200)
	newPub := p.publish(t, "OMM.fbs", "upg-new", "n1")
	r0.awaitRecords(t, "OMM.fbs", "upg-new", p.scan(t, "OMM.fbs", "upg-new"), 6*time.Minute)
	r0.requirePublication(t, "upg-new", newPub)
	time.Sleep(20 * time.Second)
	r.requireRecords(t, "OMM.fbs", "upg-new", map[string]int{})
	if !strings.Contains(readFileString(r.logPath), "does not subscribe to that lane") {
		t.Fatal("the updated receiver's log has no refusal of the unsubscribed lane")
	}
	t.Log("old path gone: the 1.1.9 control imports lane upg-new through its feed head; the updated receiver, not subscribed, imports nothing")

	// 3. The producer updated too.
	p.stop(t)
	p.exe = exe
	p.start(t)
	p.ingestOMMCIDs(t, "upg-allow", "a1", 650_000, 300)
	p.ingestOMMCIDs(t, "upg-ledger", "l1", 660_000, 300)
	for _, lane := range []string{"upg-allow", "upg-ledger"} {
		r.awaitRecords(t, "OMM.fbs", lane, p.scan(t, "OMM.fbs", lane), 3*time.Minute)
		sub := r.subscription(t, lane)
		if sub.Start != "" || sub.ReceivedRecords != 300 {
			t.Fatalf("lane %s after the producer's update: pending start %q, received %d records, want none pending and 300 (the live records only)", lane, sub.Start, sub.ReceivedRecords)
		}
	}
	t.Log("both updated: the converted subscriptions started at the producer's current batch; each lane equals the producer's, byte for byte, and only the 300 live records were sent")
	started := time.Now()
	p.ingestOMMCIDs(t, "upg-allow", "a1", 670_000, 200)
	r.awaitRecordsFast(t, "OMM.fbs", "upg-allow", p.scan(t, "OMM.fbs", "upg-allow"), 30*time.Second)
	if took := time.Since(started); took > 15*time.Second {
		t.Fatalf("live records took %s", took)
	}
	p.ingestOMMCIDs(t, "upg-follow", "f1", 680_000, 300)
	r2.awaitRecords(t, "OMM.fbs", "upg-follow", p.scan(t, "OMM.fbs", "upg-follow"), 2*time.Minute)
	t.Log("the 1.1.9 follower's followed lane keeps flowing from the updated producer")
	r.requireRecords(t, "OMM.fbs", "upg-new", map[string]int{})
	r.requireAlive(t)
}

// followRecords is every $DSS record of d's follows.dss.
func (d *publicationDaemon) followRecords(t *testing.T) []*DSS.DSS {
	t.Helper()
	raw, err := os.ReadFile(findFile(t, d.root, "follows.dss"))
	if err != nil {
		t.Fatal(err)
	}
	frames, err := syncwire.SplitFrames(raw)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]*DSS.DSS, 0, len(frames))
	for _, f := range frames {
		d, err := api.DecodeDSS(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func exec119Version(binary string) (string, error) {
	out, err := exec.Command(binary, "version").CombinedOutput()
	return string(out), err
}
