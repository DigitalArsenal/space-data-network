package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
//   - A producer (a test host speaking the protocol, trusted by the
//     receiver) that claims another peer's origin is refused; one whose
//     stream is cut in the middle of a page is read again with nothing lost
//     or doubled; one that sends a RECORD_COUNT of 0xFFFFFFFF, or an endless
//     run of maximum-size frames, does not crash the receiver or make it
//     read past a page's limits.
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

// fakeFollowProducer is a libp2p host that answers followed read-chunk
// requests the way a broken or hostile producer would, chosen by the lane's
// source name: "claim" (valid records whose header names claimOrigin as
// their origin), "cut" (the first stream reset in the middle of a page,
// later ones whole), "count" (a run header with RECORD_COUNT 0xFFFFFFFF),
// "flood" (a run header, then maximum-size frames without end). An
// unfollowed read-chunk is answered with the lane's TOTAL_COUNT.
type fakeFollowProducer struct {
	host        libp2phost.Host
	id          peer.ID
	address     string
	claimOrigin string
	mu          sync.Mutex
	opens       map[string]int
	written     int64
	records     map[string][][]byte
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
	f := &fakeFollowProducer{host: h, id: h.ID(), opens: map[string]int{}, records: map[string][][]byte{}}
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

func (f *fakeFollowProducer) mostWritten() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written
}

// want is a lane's records as the scan helper counts them.
func (f *fakeFollowProducer) want(source string) map[string]int {
	out := map[string]int{}
	for _, frame := range f.records[source] {
		sum := sha256.Sum256(frame)
		out[hex.EncodeToString(sum[:])]++
	}
	return out
}

func (f *fakeFollowProducer) header(source, origin string, count uint32, resume string) []byte {
	return syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindPage, Status: syncwire.QRPStatusOk, SchemaName: "OMM.fbs",
		ProviderID: "publication-e2e", SourceName: source, BatchID: source + "-batch", OriginID: origin, Limit: syncwire.FollowMaxRecords,
		RecordCount: count, TotalCount: uint64(len(f.records[source])), ResumeCursor: resume})
}

func (f *fakeFollowProducer) serve(s network.Stream) {
	defer s.Close()
	q, err := syncwire.ReadRequest(s)
	if err != nil {
		_ = s.Reset()
		return
	}
	source := string(q.SOURCE_NAME())
	if !q.FOLLOW() {
		_, _ = s.Write(f.header(source, "", 0, ""))
		return
	}
	f.mu.Lock()
	f.opens[source]++
	n := f.opens[source]
	f.mu.Unlock()
	writeRun := func(origin string, frames [][]byte) bool {
		if _, err := s.Write(f.header(source, origin, uint32(len(frames)), "")); err != nil {
			return false
		}
		for _, frame := range frames {
			if _, err := s.Write(frame); err != nil {
				return false
			}
		}
		return true
	}
	quiet := func() {
		// Every grant: an empty page after a pause, as a caught-up producer.
		for {
			if _, err := syncwire.ReadRequest(s); err != nil {
				return
			}
			time.Sleep(2 * time.Second)
			if _, err := s.Write(f.header(source, "", 0, source+"-end")); err != nil {
				return
			}
		}
	}
	switch source {
	case "claim":
		if writeRun(f.claimOrigin, f.records["claim"]) {
			_, _ = s.Write(f.header(source, "", 0, "claim-end"))
			quiet()
		}
	case "cut":
		recs := f.records["cut"]
		if n == 1 {
			writeRun(f.id.String(), recs[:100])
			_, _ = s.Write(f.header(source, f.id.String(), 100, ""))
			for _, frame := range recs[100:150] {
				_, _ = s.Write(frame)
			}
			_ = s.Reset()
			return
		}
		if writeRun(f.id.String(), recs[:100]) && writeRun(f.id.String(), recs[100:]) {
			_, _ = s.Write(f.header(source, "", 0, "cut-end"))
			quiet()
		}
	case "count":
		_, _ = s.Write(f.header(source, "", 0xFFFFFFFF, ""))
		_, _ = io.Copy(io.Discard, s)
	case "flood":
		if _, err := s.Write(f.header(source, "", syncwire.FollowMaxRecords, "")); err != nil {
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
