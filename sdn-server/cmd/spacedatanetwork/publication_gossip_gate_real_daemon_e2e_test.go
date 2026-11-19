package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// TestPublicationGossipGateRealDaemons: a record gossiped on a schema topic
// names no lane, so no subscription admits it, and a node stores none: not
// from a peer it trusts and subscribes to, and not with
// network.accept_push_from: any. The receiver's log shows each record was
// delivered and refused.
func TestPublicationGossipGateRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "gossip-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "gossip-receiver", a.address, a.id, "keep-all")
	b.appendConfig(t, "  accept_push_from: any\n", "network:\n")
	b.start(t)
	time.Sleep(3 * time.Second)
	a.ingestOMMCIDs(t, "gg", "g1", 940_000, 10)
	b.follow(t, a, "OMM", "gg", "")
	b.awaitRecords(t, "OMM.fbs", "gg", a.scan(t, "OMM.fbs", "gg"), 2*time.Minute)

	sent := map[string]bool{}
	deadline := time.Now().Add(3 * time.Minute)
	for i := 0; ; i++ {
		frame := baselineOMM(950_000 + i)
		// The record with and without its size prefix: either form the
		// listing returns counts.
		for _, form := range [][]byte{frame, frame[4:]} {
			sum := sha256.Sum256(form)
			sent[hex.EncodeToString(sum[:])] = true
		}
		a.request(t, "POST", "/api/v1/pubsub/publish", publicationStreamType, frame, http.StatusOK)
		time.Sleep(2 * time.Second)
		if held := b.gossipHeld(t, sent); held > 0 {
			t.Fatalf("the receiver stored %d records gossiped on the OMM topic: a node imports only the lanes it subscribes to", held)
		}
		if strings.Contains(readFileString(b.logPath), "record gossiped by "+shortID(a.id)) {
			t.Logf("the receiver refused %d gossiped records (trusted producer, a subscription from it, accept_push_from: any)", len(sent)/2)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no gossiped record reached the receiver in 3 minutes (%d sent): this run proves nothing", len(sent)/2)
		}
	}
	time.Sleep(5 * time.Second)
	if held := b.gossipHeld(t, sent); held > 0 {
		t.Fatalf("the receiver stored %d gossiped records", held)
	}
	b.requireAlive(t)
}

// TestPublicationPNMOnlyLaneRealDaemons: a subscribed lane of a standard the
// producer serves only to peers it grants private reads (OEM, as host-02
// serves host-01's CNP and IQC lanes) imports through the lane's signed
// publications: the stream is refused, nothing arrives on it, and the
// receiver's records equal the producer's. A lane whose stream is connected
// gets its records on the stream and does not fetch its publication's shard
// a second time.
func TestPublicationPNMOnlyLaneRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "pnmonly-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "pnmonly-receiver", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)
	b.follow(t, a, "OEM", "po", "start=beginning&force=1")
	b.follow(t, a, "OMM", "ps", "")

	for i := 0; i < 6; i += 3 {
		var body []byte
		for j := i; j < i+3; j++ {
			body = append(body, baselineOEM(100+j)...)
		}
		a.ingest(t, "OEM.fbs", "po", "p1", body, 3)
	}
	want := a.scan(t, "OEM.fbs", "po")
	if len(want) != 6 {
		t.Fatalf("producer holds %d OEM records, want 6", len(want))
	}
	a.publish(t, "OEM.fbs", "po", "p1")
	b.awaitRecords(t, "OEM.fbs", "po", want, 6*time.Minute)
	sub := b.subscription(t, "po")
	if sub.ReceivedRecords != 0 || !strings.Contains(sub.Error, "private reads") {
		t.Fatalf("lane po: received %d records on its stream, last error %q; want none (the producer refuses the stream)", sub.ReceivedRecords, sub.Error)
	}
	t.Log("lane po (OEM): the stream is refused; the 6 records arrived through the lane's signed publication, byte for byte")

	a.ingestOMMCIDs(t, "ps", "s1", 960_000, 500)
	b.awaitRecords(t, "OMM.fbs", "ps", a.scan(t, "OMM.fbs", "ps"), 2*time.Minute)
	pub := a.publish(t, "OMM.fbs", "ps", "s1")
	b.requirePublication(t, "ps", pub)
	time.Sleep(20 * time.Second)
	log := readFileString(b.logPath)
	if !strings.Contains(log, "Not fetching the publications of OMM.fbs publication-e2e/ps") {
		t.Fatal("the receiver's log does not say lane ps's publication was left to its stream")
	}
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "Materialized trusted dataset update") && strings.Contains(line, "on OMM.fbs") {
			t.Fatalf("the receiver imported lane ps's shard although its stream delivered the records: %s", line)
		}
	}
	b.requireRecords(t, "OMM.fbs", "ps", a.scan(t, "OMM.fbs", "ps"))
	t.Log("lane ps (OMM): the records came on the stream; the publication's shard was not fetched again")
}

// gossipHeld is how many of the records (by SHA-256 of the frame) the
// daemon stores under OMM.fbs with no lane, as /api/v1/pubsub/messages lists
// them.
func (d *publicationDaemon) gossipHeld(t *testing.T, sent map[string]bool) int {
	t.Helper()
	raw := d.request(t, "GET", "/api/v1/pubsub/messages?"+url.Values{"schema": {"OMM.fbs"}, "limit": {"1000"}}.Encode(), "", nil, http.StatusOK)
	frames, err := syncwire.SplitFrames(raw)
	if err != nil {
		t.Fatal(err)
	}
	held := 0
	for _, f := range frames[min(1, len(frames)):] {
		sum := sha256.Sum256(f)
		if sent[hex.EncodeToString(sum[:])] {
			held++
		} else if len(f) > 4 {
			if sum = sha256.Sum256(f[4:]); sent[hex.EncodeToString(sum[:])] {
				held++
			}
		}
	}
	return held
}

// shortID is the form peer.ID.ShortString logs: the last six characters.
func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return fmt.Sprintf("<peer.ID %s*%s>", id[:2], id[len(id)-6:])
}
