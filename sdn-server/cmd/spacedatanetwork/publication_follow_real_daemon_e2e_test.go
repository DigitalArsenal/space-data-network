package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// TestPublicationFollowRealDaemons follows a lane between two real daemons
// with nothing published: the receiver subscribes to the producer's lane
// (a $DSS Subscribe naming PROVIDER_PEER_ID). The receiver trusts the
// producer, and its rule for the lane is replace-current.
//
//   - A producer the receiver does not trust is refused, and no stream is
//     opened to it.
//   - The producer's stored records arrive over the followed stream, and
//     records stored later arrive within seconds.
//   - A newer batch replaces the older one on the receiver (its rule),
//     while the producer keeps both.
//   - A receiver stopped while the producer stores more, and a producer
//     restarted, both resume: every record once, byte for byte.
//   - Records the producer removes leave the receiver.
//   - A refresh that carries most records over unchanged (the producer
//     tags them to the new batch in place, with no new sequence) leaves the
//     receiver holding exactly the new batch, byte for byte.
//   - One lane followed from two producers: each producer's batch rotation
//     removes only its own superseded records.
//   - A producer (a test host speaking the protocol, trusted by the
//     receiver) that claims another peer's origin is refused; one whose
//     stream is cut in the middle of a page is read again with nothing lost
//     or doubled; one whose new batch is half received (the rest cannot be
//     fetched) leaves the old batch current until all of it is held; one
//     that sends a RECORD_COUNT of 0xFFFFFFFF, an endless run of
//     maximum-size frames, or a header whose root offset points outside it
//     does not crash the receiver or make it read past a page's limits.
//   - Unsubscribing the standard stops every followed lane of it.
func TestPublicationFollowRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "follow-producer", "", "", "keep-all")
	a.start(t)
	fake := newFakeFollowProducer(t)
	untrusted := newFakeFollowProducer(t)
	b := newPublicationDaemon(t, exe, "follow-receiver", a.address, a.id, "replace-current")
	b.trustAlso(t, fake.address)
	b.start(t)
	b.requireTrusted(t, fake.id)
	time.Sleep(3 * time.Second)
	fake.claimOrigin = a.id

	t.Run("untrusted_producer_refused", func(t *testing.T) {
		untrusted.connect(t, b.address)
		raw := b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "untrusted", untrusted.id.String()), http.StatusBadRequest)
		if !strings.Contains(string(raw), "not a trusted peer") {
			t.Fatalf("refusal does not name trust: %q", raw)
		}
		time.Sleep(3 * time.Second)
		if n := untrusted.opened("untrusted"); n != 0 {
			t.Fatalf("the receiver opened %d followed streams to a producer it does not trust", n)
		}
	})

	t.Run("follow_and_live", func(t *testing.T) {
		a.ingestOMMCIDs(t, "follow", "first", 60_000, 10_000)
		want := a.scan(t, "OMM.fbs", "follow")
		if len(want) != 10_000 {
			t.Fatalf("producer holds %d records, want 10000", len(want))
		}
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "follow", a.id), http.StatusAccepted)
		started := time.Now()
		b.awaitRecords(t, "OMM.fbs", "follow", want, 3*time.Minute)
		t.Logf("10,000 stored records followed in %s", time.Since(started).Round(time.Millisecond))
		for round := 0; round < 3; round++ {
			a.ingestOMMCIDs(t, "follow", "first", 70_000+round*500, 500)
			want = a.scan(t, "OMM.fbs", "follow")
			started = time.Now()
			b.awaitRecordsFast(t, "OMM.fbs", "follow", want, 30*time.Second)
			took := time.Since(started)
			t.Logf("live round %d: 500 records on the receiver %s after the producer's ingest returned", round, took.Round(time.Millisecond))
			if took > 15*time.Second {
				t.Fatalf("live records took %s", took)
			}
		}
		sub := b.followRecord(t)
		if string(sub.PROVIDER_PEER_ID()) != a.id || string(sub.SOURCE_NAME()) != "follow" || !sub.SUBSCRIBED() || len(sub.CURSOR()) == 0 {
			t.Fatalf("follows.dss record: producer %q source %q subscribed %v cursor %d bytes", sub.PROVIDER_PEER_ID(), sub.SOURCE_NAME(), sub.SUBSCRIBED(), len(sub.CURSOR()))
		}
	})

	t.Run("retention_replace_current", func(t *testing.T) {
		a.ingestOMMCIDs(t, "follow", "second", 100_000, 2_000)
		want := a.scanBatch(t, "OMM.fbs", "follow", "second")
		if len(want) != 2_000 || len(a.scan(t, "OMM.fbs", "follow")) != 13_500 {
			t.Fatal("the producer does not hold both batches")
		}
		started := time.Now()
		b.awaitRecords(t, "OMM.fbs", "follow", want, 2*time.Minute)
		t.Logf("receiver holds only batch second (2,000 records) %s after the producer stored it; the producer holds 13,500", time.Since(started).Round(time.Millisecond))
	})

	t.Run("receiver_restart", func(t *testing.T) {
		b.stop(t)
		a.ingestOMMCIDs(t, "follow", "second", 110_000, 3_000)
		want := a.scanBatch(t, "OMM.fbs", "follow", "second")
		b.start(t)
		b.awaitRecords(t, "OMM.fbs", "follow", want, 2*time.Minute)
		t.Logf("receiver restart: %d records of the current batch, each once, byte for byte", len(want))
	})

	t.Run("producer_restart", func(t *testing.T) {
		a.stop(t)
		a.start(t)
		a.ingestOMMCIDs(t, "follow", "second", 120_000, 500)
		want := a.scanBatch(t, "OMM.fbs", "follow", "second")
		b.awaitRecords(t, "OMM.fbs", "follow", want, 2*time.Minute)
		t.Logf("producer restart: %d records of the current batch, each once, byte for byte", len(want))
	})

	t.Run("removal_at_the_producer", func(t *testing.T) {
		body := `{"schema":"OMM.fbs","provider_id":"publication-e2e","source_name":"follow","keep_batch":"first"}`
		a.request(t, "POST", "/api/v1/admin/store/supersede", "application/json", []byte(body), http.StatusOK)
		want := a.scan(t, "OMM.fbs", "follow")
		if len(want) != 11_500 {
			t.Fatalf("producer holds %d records after removing batch second, want 11500", len(want))
		}
		started := time.Now()
		b.awaitRecords(t, "OMM.fbs", "follow", want, 3*time.Minute)
		t.Logf("removal: the receiver holds the producer's 11,500 records %s after the producer removed batch second", time.Since(started).Round(time.Millisecond))
	})

	t.Run("replace_current_unchanged_records", func(t *testing.T) {
		// A refresh that carries most records over unchanged: the producer
		// tags them to the new batch in place and never sends them again by
		// sequence; the receiver still holds exactly the new batch.
		// The builder stamps each record's creation time, so the carried-over
		// records are the same frames ingested again.
		o1 := ommFrames(250_000, 1_000)
		a.ingestOMMFrames(t, "overlap", "o1", 250_000, o1)
		first := a.scanBatch(t, "OMM.fbs", "overlap", "o1")
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "overlap", a.id), http.StatusAccepted)
		b.awaitRecords(t, "OMM.fbs", "overlap", first, 2*time.Minute)
		a.ingestOMMFrames(t, "overlap", "o2", 250_000, append(append([][]byte(nil), o1[:900]...), ommFrames(251_000, 100)...))
		want := a.scanBatch(t, "OMM.fbs", "overlap", "o2")
		if len(want) != 1_000 || len(a.scan(t, "OMM.fbs", "overlap")) != 1_100 {
			t.Fatalf("the producer holds %d records of batch o2 and %d of the lane, want 1000 and 1100", len(want), len(a.scan(t, "OMM.fbs", "overlap")))
		}
		started := time.Now()
		b.awaitRecords(t, "OMM.fbs", "overlap", want, 2*time.Minute)
		t.Logf("batch o2 (900 records carried over unchanged, 100 new) is the receiver's whole lane, byte for byte, %s after the producer stored it", time.Since(started).Round(time.Millisecond))
		time.Sleep(15 * time.Second)
		b.requireRecords(t, "OMM.fbs", "overlap", want)
	})

	t.Run("two_producers_one_lane", func(t *testing.T) {
		// One lane followed from two producers: each one's batch rotation
		// removes only its own superseded records.
		a1 := ommFrames(260_000, 1_000)
		a.ingestOMMFrames(t, "two", "two-a1", 260_000, a1)
		fake.connect(t, b.address)
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "two", a.id), http.StatusAccepted)
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "two", fake.id.String()), http.StatusAccepted)
		b.awaitRecords(t, "OMM.fbs", "two", unionCounts(a.scanBatch(t, "OMM.fbs", "two", "two-a1"), fake.want("two")), 2*time.Minute)
		a.ingestOMMFrames(t, "two", "two-a2", 260_000, append(append([][]byte(nil), a1[:500]...), ommFrames(261_000, 500)...))
		want := unionCounts(a.scanBatch(t, "OMM.fbs", "two", "two-a2"), fake.want("two"))
		started := time.Now()
		b.awaitRecords(t, "OMM.fbs", "two", want, 2*time.Minute)
		t.Logf("two producers: the receiver holds producer A's batch two-a2 (1,000) and the test host's 100 records %s after A stored two-a2", time.Since(started).Round(time.Millisecond))
		time.Sleep(40 * time.Second)
		b.requireRecords(t, "OMM.fbs", "two", want)
	})

	t.Run("claimed_origin_refused", func(t *testing.T) {
		fake.connect(t, b.address)
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "claim", fake.id.String()), http.StatusAccepted)
		fake.awaitOpened(t, "claim", 2)
		b.requireAlive(t)
		if got := b.scan(t, "OMM.fbs", "claim"); len(got) != 0 {
			t.Fatalf("the receiver stored %d records whose origin the producer claimed to be %s", len(got), a.id)
		}
		if !strings.Contains(readFileString(b.logPath), "names another peer") {
			t.Fatal("the receiver's log has no refusal of the claimed origin")
		}
	})

	t.Run("stream_cut_mid_page", func(t *testing.T) {
		fake.connect(t, b.address)
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "cut", fake.id.String()), http.StatusAccepted)
		b.awaitRecords(t, "OMM.fbs", "cut", fake.want("cut"), 2*time.Minute)
		if fake.opened("cut") < 2 {
			t.Fatal("the cut stream was not opened again")
		}
	})

	t.Run("half_received_batch_not_current", func(t *testing.T) {
		// The test host states batch pause-2 (200 records: 50 of pause-1
		// carried over, 150 new) but sends 50 of the new ones and refuses
		// the rest; pause-1 stays the receiver's current batch until all
		// 200 are held.
		fake.connect(t, b.address)
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "pause", fake.id.String()), http.StatusAccepted)
		b.awaitRecords(t, "OMM.fbs", "pause", fake.batchWant("pause-1"), 2*time.Minute)
		fake.setPauseStage(1)
		sent := fake.framesWant(fake.batches["pause-2"][50:100])
		deadline := time.Now().Add(time.Minute)
		for !containsCounts(b.scan(t, "OMM.fbs", "pause"), sent) {
			if time.Now().After(deadline) {
				t.Fatal("the receiver does not hold the 50 records of pause-2 sent on the stream")
			}
			time.Sleep(500 * time.Millisecond)
		}
		// Past one list read of the batch (at most every 30 s per lane).
		time.Sleep(40 * time.Second)
		if got := b.scanBatch(t, "OMM.fbs", "pause", "pause-1"); !reflect.DeepEqual(got, fake.batchWant("pause-1")) {
			t.Fatalf("with batch pause-2 half received the receiver holds %d records of batch pause-1, want all 100", len(got))
		}
		fake.awaitFetches(t, "pause", 1, time.Minute)
		if got := b.scan(t, "OMM.fbs", "pause"); len(got) != 150 {
			t.Fatalf("with batch pause-2 half received the receiver holds %d records of the lane, want 150 (pause-1 and the 50 new records sent)", len(got))
		}
		t.Log("half received: the receiver keeps all 100 records of pause-1 while 100 of pause-2's 200 cannot be fetched")
		fake.releasePause()
		started := time.Now()
		b.awaitRecords(t, "OMM.fbs", "pause", fake.batchWant("pause-2"), 2*time.Minute)
		t.Logf("once the rest can be fetched, pause-2 (200) replaces pause-1 %s later", time.Since(started).Round(time.Millisecond))
	})

	t.Run("hostile_record_count", func(t *testing.T) {
		fake.connect(t, b.address)
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "count", fake.id.String()), http.StatusAccepted)
		fake.awaitOpened(t, "count", 2)
		b.requireAlive(t)
		if !strings.Contains(readFileString(b.logPath), "a run of 4294967295 records") {
			t.Fatal("the receiver's log has no refusal of the RECORD_COUNT")
		}
	})

	t.Run("hostile_frame_flood", func(t *testing.T) {
		fake.connect(t, b.address)
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "flood", fake.id.String()), http.StatusAccepted)
		fake.awaitOpened(t, "flood", 2)
		time.Sleep(5 * time.Second)
		b.requireAlive(t)
		if most := fake.mostWritten(); most > 128<<20 {
			t.Fatalf("the receiver read %d MiB of one page from a flooding producer", most>>20)
		} else {
			t.Logf("flood: the most one stream delivered before the receiver refused it: %d MiB", most>>20)
		}
	})

	t.Run("malformed_header", func(t *testing.T) {
		// A header frame whose root offset points outside it: the receiver
		// refuses the stream and raises an alert; it does not crash.
		fake.connect(t, b.address)
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "malformed", fake.id.String()), http.StatusAccepted)
		fake.awaitOpened(t, "malformed", 2)
		time.Sleep(2 * time.Second)
		b.requireAlive(t)
		if !strings.Contains(readFileString(b.logPath), "malformed $QRP frame") {
			t.Fatal("the receiver's log has no refusal of the malformed header")
		}
		alerts := string(b.request(t, "GET", "/api/v1/status/alerts", "", nil, http.StatusOK))
		if !strings.Contains(alerts, "follow_refused") || !strings.Contains(alerts, "publication-e2e/malformed") {
			t.Fatalf("no follow_refused alert for the malformed lane: %.2048s", alerts)
		}
		b.requireRecords(t, "OMM.fbs", "overlap", a.scanBatch(t, "OMM.fbs", "overlap", "o2"))
	})

	t.Run("unsubscribe_standard", func(t *testing.T) {
		b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSAction("OMM", "", "", api.DSSActionUnsubscribe), http.StatusAccepted)
		raw, err := os.ReadFile(findFile(t, b.root, "follows.dss"))
		if err != nil {
			t.Fatal(err)
		}
		if frames, _ := syncwire.SplitFrames(raw); len(frames) != 0 {
			t.Fatalf("follows.dss holds %d lanes after the standard was unsubscribed", len(frames))
		}
		before := fake.opened("flood") + fake.opened("count") + fake.opened("claim")
		time.Sleep(35 * time.Second)
		if after := fake.opened("flood") + fake.opened("count") + fake.opened("claim"); after != before {
			t.Fatalf("%d followed streams opened after the standard was unsubscribed", after-before)
		}
		b.requireAlive(t)
	})
}

// followRecord is the receiver's one followed lane in follows.dss.
func (d *publicationDaemon) followRecord(t *testing.T) *DSS.DSS {
	t.Helper()
	raw, err := os.ReadFile(findFile(t, d.root, "follows.dss"))
	if err != nil {
		t.Fatal(err)
	}
	frames, err := syncwire.SplitFrames(raw)
	if err != nil || len(frames) != 1 || !DSS.SizePrefixedDSSBufferHasIdentifier(frames[0]) {
		t.Fatalf("follows.dss holds %d frames (%v), want one $DSS record", len(frames), err)
	}
	return DSS.GetSizePrefixedRootAsDSS(frames[0], 0)
}

// trustAlso adds a trusted peer address to the daemon's configuration.
func (d *publicationDaemon) trustAlso(t *testing.T, address string) {
	t.Helper()
	raw, err := os.ReadFile(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "  trusted_peers: [", "  trusted_peers: [\""+address+"\", ", 1)
	if text == string(raw) {
		t.Fatal("no trusted_peers list to extend")
	}
	if err := os.WriteFile(d.cfg, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func (d *publicationDaemon) requireTrusted(t *testing.T, id peer.ID) {
	t.Helper()
	raw := d.request(t, "GET", "/api/v1/admin/peers/"+id.String(), "", nil, http.StatusOK)
	var status struct {
		Trust peers.TrustLevel `json:"trust_level"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.Trust < peers.Trusted {
		t.Fatalf("peer %s is not trusted by the receiver: %s", id, status.Trust)
	}
}

// requireAlive fails when the daemon process has exited or its API does not
// answer.
func (d *publicationDaemon) requireAlive(t *testing.T) {
	t.Helper()
	select {
	case err := <-d.done:
		d.done <- err
		t.Fatalf("the daemon exited: %v; %s", err, tailString(readFileString(d.logPath), 30))
	default:
	}
	c := http.Client{Timeout: 10 * time.Second}
	res, err := c.Get(d.base + "/api/v1/id")
	if err != nil {
		t.Fatalf("the daemon does not answer: %v; %s", err, tailString(readFileString(d.logPath), 30))
	}
	res.Body.Close()
}

// awaitRecordsFast is awaitRecords polling every 100 ms.
func (d *publicationDaemon) awaitRecordsFast(t *testing.T, schema, source string, want map[string]int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if reflect.DeepEqual(d.scan(t, schema, source), want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.requireRecords(t, schema, source, want)
}

func findFile(t *testing.T, root, name string) string {
	t.Helper()
	found := ""
	_ = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no %s under %s", name, root)
	}
	return found
}

// ommFrames is n element sets from offset.
func ommFrames(offset, n int) [][]byte {
	frames := make([][]byte, n)
	for i := range frames {
		frames[i] = baselineOMM(offset + i)
	}
	return frames
}

// unionCounts is two lanes' records as the scan helper counts them.
func unionCounts(a, b map[string]int) map[string]int {
	out := make(map[string]int, len(a)+len(b))
	for k, v := range a {
		out[k] += v
	}
	for k, v := range b {
		out[k] += v
	}
	return out
}

// fakeFollowProducer is a libp2p host that answers followed read-chunk
// requests the way a broken, hostile or slow producer would, chosen by the
// lane's source name: "claim" (valid records whose header names claimOrigin
// as their origin), "cut" (the first stream reset in the middle of a page,
// later ones whole), "count" (a run header with RECORD_COUNT 0xFFFFFFFF),
// "flood" (a run header, then maximum-size frames without end), "malformed"
// (a $QRP header whose root offset points outside it), "two" (an ordinary
// producer of one batch) and "pause" (batch pause-1, then batch pause-2 of
// which it sends 50 new records and refuses to fetch the rest until
// released). Every followed page's end header states the lane's current
// batch. An unfollowed read-chunk answers a batch's list of CIDs
// (PROJECTION), its records by CID, or else the batch's TOTAL_COUNT.
type fakeFollowProducer struct {
	host        libp2phost.Host
	id          peer.ID
	address     string
	claimOrigin string
	mu          sync.Mutex
	opens       map[string]int
	fetches     map[string]int
	written     int64
	records     map[string][][]byte
	batches     map[string][][]byte
	pauseStage  int
	pauseSent   bool
	released    bool
}

func newFakeFollowProducer(t *testing.T) *fakeFollowProducer {
	t.Helper()
	key, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.Identity(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	f := &fakeFollowProducer{host: h, id: h.ID(), opens: map[string]int{}, fetches: map[string]int{}, records: map[string][][]byte{},
		batches: map[string][][]byte{}}
	for _, addr := range h.Addrs() {
		if strings.HasPrefix(addr.String(), "/ip4/127.0.0.1/tcp/") {
			f.address = addr.String() + "/p2p/" + h.ID().String()
		}
	}
	for i := 0; i < 100; i++ {
		f.records["claim"] = append(f.records["claim"], baselineOMM(200_000+i))
	}
	for i := 0; i < 200; i++ {
		f.records["cut"] = append(f.records["cut"], baselineOMM(210_000+i))
	}
	for i := 0; i < 100; i++ {
		f.records["two"] = append(f.records["two"], baselineOMM(230_000+i))
	}
	for i := 0; i < 100; i++ {
		f.batches["pause-1"] = append(f.batches["pause-1"], baselineOMM(220_000+i))
	}
	f.batches["pause-2"] = append(f.batches["pause-2"], f.batches["pause-1"][:50]...)
	for i := 0; i < 150; i++ {
		f.batches["pause-2"] = append(f.batches["pause-2"], baselineOMM(221_000+i))
	}
	for source, recs := range f.records {
		f.batches[source+"-batch"] = recs
	}
	h.SetStreamHandler(libp2pprotocol.ID(syncwire.ProtocolID(syncwire.OpReadChunk)), f.serve)
	return f
}

func (f *fakeFollowProducer) connect(t *testing.T, address string) {
	t.Helper()
	info, err := peer.AddrInfoFromP2pAddr(multiaddr.StringCast(address))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.host.Connect(ctx, *info); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeFollowProducer) opened(source string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[source]
}

func (f *fakeFollowProducer) awaitOpened(t *testing.T, source string, n int) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for f.opened(source) < n {
		if time.Now().After(deadline) {
			t.Fatalf("the receiver opened %d followed streams of %s, want at least %d", f.opened(source), source, n)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// awaitFetches waits for the receiver's fetches by CID of a lane.
func (f *fakeFollowProducer) awaitFetches(t *testing.T, source string, n int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		f.mu.Lock()
		got := f.fetches[source]
		f.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the receiver fetched by CID %d times from lane %s, want at least %d", got, source, n)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (f *fakeFollowProducer) setPauseStage(stage int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauseStage = stage
}

func (f *fakeFollowProducer) releasePause() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = true
}

func (f *fakeFollowProducer) mostWritten() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written
}

// want is a lane's records as the scan helper counts them.
func (f *fakeFollowProducer) want(source string) map[string]int {
	return f.batchWant(source + "-batch")
}

// batchWant is a batch's records as the scan helper counts them.
func (f *fakeFollowProducer) batchWant(batch string) map[string]int {
	return f.framesWant(f.batches[batch])
}

// framesWant is frames as the scan helper counts them.
func (f *fakeFollowProducer) framesWant(frames [][]byte) map[string]int {
	out := map[string]int{}
	for _, frame := range frames {
		sum := sha256.Sum256(frame)
		out[hex.EncodeToString(sum[:])]++
	}
	return out
}

// containsCounts reports every record of want held in got.
func containsCounts(got, want map[string]int) bool {
	for k := range want {
		if got[k] == 0 {
			return false
		}
	}
	return true
}

// current is the lane's statement: its current batch.
func (f *fakeFollowProducer) current(source string) string {
	if source != "pause" {
		return source + "-batch"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprintf("pause-%d", f.pauseStage+1)
}

func (f *fakeFollowProducer) header(source, batch, origin string, count uint32, resume string) []byte {
	return syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindPage, Status: syncwire.QRPStatusOk, SchemaName: "OMM.fbs",
		ProviderID: "publication-e2e", SourceName: source, BatchID: batch, OriginID: origin, Limit: syncwire.FollowMaxRecords,
		RecordCount: count, TotalCount: uint64(len(f.batches[batch])), ResumeCursor: resume})
}

// end is a followed page's end header, stating the lane's current batch.
func (f *fakeFollowProducer) end(source, resume string) []byte {
	return f.header(source, f.current(source), "", 0, resume)
}

func (f *fakeFollowProducer) serve(s network.Stream) {
	defer s.Close()
	q, err := syncwire.ReadRequest(s)
	if err != nil {
		_ = s.Reset()
		return
	}
	source := string(q.SOURCE_NAME())
	batch := string(q.BATCH_ID())
	writeRun := func(batch, origin string, frames [][]byte) bool {
		if _, err := s.Write(f.header(source, batch, origin, uint32(len(frames)), "")); err != nil {
			return false
		}
		for _, frame := range frames {
			if _, err := s.Write(frame); err != nil {
				return false
			}
		}
		return true
	}
	if !q.FOLLOW() {
		if _, ok := syncwire.Filter(q, syncwire.FilterProjection, syncwire.QRPFilterEq); ok {
			cids := make([]string, 0, len(f.batches[batch]))
			for _, frame := range f.batches[batch] {
				cids = append(cids, storage.ComputeCID(frame[syncwire.FramePrefixLength:]))
			}
			_, _ = s.Write(syncwire.CIDPage(syncwire.QRPFields{SchemaName: "OMM.fbs", ProviderID: "publication-e2e", SourceName: source, BatchID: batch}, cids))
			return
		}
		if named, ok := syncwire.Filter(q, syncwire.FilterCID, syncwire.QRPFilterIn); ok {
			f.mu.Lock()
			f.fetches[source]++
			held := source == "pause" && !f.released
			f.mu.Unlock()
			if held {
				_, _ = s.Write(syncwire.ErrorFrame(syncwire.QRPStatusBusy, "busy", "the batch's records are not ready", http.StatusTooManyRequests, time.Second))
				return
			}
			want := map[string]bool{}
			for _, c := range strings.Split(named, ",") {
				want[c] = true
			}
			var frames [][]byte
			for _, frame := range f.batches[batch] {
				if want[storage.ComputeCID(frame[syncwire.FramePrefixLength:])] {
					frames = append(frames, frame)
				}
			}
			origin := f.id.String()
			if source == "claim" {
				origin = f.claimOrigin
			}
			if len(frames) == 0 || writeRun(batch, origin, frames) {
				_, _ = s.Write(f.header(source, "", "", 0, ""))
			}
			return
		}
		_, _ = s.Write(f.header(source, batch, "", 0, ""))
		return
	}
	f.mu.Lock()
	f.opens[source]++
	n := f.opens[source]
	f.mu.Unlock()
	quiet := func() {
		// Every grant: an empty page after a pause, as a caught-up producer;
		// lane pause sends 50 new records of pause-2 once at stage 1.
		for {
			if _, err := syncwire.ReadRequest(s); err != nil {
				return
			}
			time.Sleep(2 * time.Second)
			if source == "pause" {
				f.mu.Lock()
				send := f.pauseStage == 1 && !f.pauseSent
				f.pauseSent = f.pauseSent || send
				f.mu.Unlock()
				if send && !writeRun("pause-2", f.id.String(), f.batches["pause-2"][50:100]) {
					return
				}
			}
			if _, err := s.Write(f.end(source, source+"-end")); err != nil {
				return
			}
		}
	}
	switch source {
	case "claim":
		if writeRun("claim-batch", f.claimOrigin, f.records["claim"]) {
			_, _ = s.Write(f.end(source, "claim-end"))
			quiet()
		}
	case "two":
		if writeRun("two-batch", f.id.String(), f.records["two"]) {
			_, _ = s.Write(f.end(source, "two-end"))
			quiet()
		}
	case "pause":
		if writeRun("pause-1", f.id.String(), f.batches["pause-1"]) {
			_, _ = s.Write(f.end(source, "pause-end"))
			quiet()
		}
	case "cut":
		recs := f.records["cut"]
		if n == 1 {
			writeRun("cut-batch", f.id.String(), recs[:100])
			_, _ = s.Write(f.header(source, "cut-batch", f.id.String(), 100, ""))
			for _, frame := range recs[100:150] {
				_, _ = s.Write(frame)
			}
			_ = s.Reset()
			return
		}
		if writeRun("cut-batch", f.id.String(), recs[:100]) && writeRun("cut-batch", f.id.String(), recs[100:]) {
			_, _ = s.Write(f.end(source, "cut-end"))
			quiet()
		}
	case "count":
		_, _ = s.Write(f.header(source, "count-batch", "", 0xFFFFFFFF, ""))
		_, _ = io.Copy(io.Discard, s)
	case "malformed":
		// A size-prefixed $QRP frame of 20 bytes whose root offset points
		// 2 GiB past its end.
		frame := make([]byte, 24)
		binary.LittleEndian.PutUint32(frame[0:4], 20)
		binary.LittleEndian.PutUint32(frame[4:8], 0x7FFFFFF0)
		copy(frame[8:12], "$QRP")
		_, _ = s.Write(frame)
		_, _ = io.Copy(io.Discard, s)
	case "flood":
		if _, err := s.Write(f.header(source, "flood-batch", "", syncwire.FollowMaxRecords, "")); err != nil {
			return
		}
		frame := make([]byte, syncwire.FramePrefixLength+syncwire.MaxFrameBytes)
		frame[0], frame[1], frame[2], frame[3] = 0, 0, 0, byte(syncwire.MaxFrameBytes>>24)
		var written int64
		for written < 512<<20 {
			_ = s.SetWriteDeadline(time.Now().Add(20 * time.Second))
			if _, err := s.Write(frame); err != nil {
				break
			}
			written += int64(len(frame))
			f.mu.Lock()
			f.written = max(f.written, written)
			f.mu.Unlock()
		}
		_, _ = io.Copy(io.Discard, s)
	default:
		_ = s.Reset()
	}
}
