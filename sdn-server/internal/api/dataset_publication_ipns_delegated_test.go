package api

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/boxo/ipns"
	"github.com/ipfs/boxo/path"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// dataset_publication_ipns_delegated_test.go — stack #980. On host-02 Kubo's
// name/publish never answered (its DHT put outlasted the 3-minute deadline on
// every run from 2026-11-19 05:28 UTC), so the CelesTrak IPNS name stood still
// until its record expired. These tests drive the publication service against
// a real FlatSQL store and a loopback Kubo whose name/publish signs a real
// record, stores it, and then hangs like that DHT put.

// ipnsTestSigner is the IPNS half of the fake Kubo: one key, real signatures.
type ipnsTestSigner struct {
	t       *testing.T
	key     string
	sk      ic.PrivKey
	name    ipns.Name
	hang    bool
	release chan struct{}

	mu  sync.Mutex
	seq uint64
	raw []byte
}

func enableIPNSSigning(t *testing.T, k *hygieneKubo, key string, hang bool) *ipnsTestSigner {
	t.Helper()
	sk, _, err := ic.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	s := &ipnsTestSigner{t: t, key: key, sk: sk, name: ipns.NameFromPeer(pid), hang: hang, release: make(chan struct{})}
	// Registered after the fake Kubo's server.Close, so it runs first and
	// frees the hanging handlers.
	t.Cleanup(func() { close(s.release) })
	k.mu.Lock()
	k.signer = s
	k.mu.Unlock()
	return s
}

// publish signs value under the key (sequence +1), stores it where name/get
// offline reads it, and then, like an online Kubo on a host whose DHT put
// does not finish, does not answer until the caller gives up.
func (s *ipnsTestSigner) publish(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("key") != s.key {
		http.Error(w, "no key named "+q.Get("key"), http.StatusInternalServerError)
		return
	}
	value, err := path.NewPath(q.Get("arg"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lifetime := 48 * time.Hour
	if v := q.Get("lifetime"); v != "" {
		if lifetime, err = time.ParseDuration(v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	s.seq++
	rec, err := ipns.NewRecord(s.sk, value, s.seq, time.Now().Add(lifetime), 5*time.Minute)
	if err == nil {
		s.raw, err = ipns.MarshalRecord(rec)
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.hang {
		select {
		case <-r.Context().Done():
		case <-s.release:
		}
		return
	}
	_, _ = fmt.Fprintf(w, `{"Name":"%s","Value":"%s"}`+"\n", s.name.String(), value.String())
}

func (s *ipnsTestSigner) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v0/key/list":
		_, _ = fmt.Fprintf(w, `{"Keys":[{"Name":"self","Id":"k51-other"},{"Name":"%s","Id":"%s"}]}`+"\n", s.key, s.name.String())
	case "/api/v0/name/get":
		if r.URL.Query().Get("offline") != "true" {
			// The point of the fix: the node never waits on a network lookup.
			http.Error(w, "name/get without offline=true would query the DHT", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("arg") != s.name.String() {
			http.Error(w, "routing: not found", http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		raw := append([]byte(nil), s.raw...)
		s.mu.Unlock()
		if len(raw) == 0 {
			http.Error(w, "routing: not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.ipfs.ipns-record")
		_, _ = w.Write(raw)
	}
}

// fakeIPNSPublisher is a Routing V1 HTTP publisher (PUT /routing/v1/ipns/<name>).
type fakeIPNSPublisher struct {
	mu      sync.Mutex
	status  int
	records map[string][][]byte
	server  *httptest.Server
}

func newFakeIPNSPublisher(t *testing.T, status int) *fakeIPNSPublisher {
	t.Helper()
	p := &fakeIPNSPublisher{status: status, records: map[string][][]byte{}}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, "/routing/v1/ipns/")
		if r.Method != http.MethodPut || !ok || r.Header.Get("Content-Type") != "application/vnd.ipfs.ipns-record" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.status != http.StatusOK {
			http.Error(w, "publisher down", p.status)
			return
		}
		p.records[name] = append(p.records[name], body)
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeIPNSPublisher) received(name string) [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.records[name]...)
}

// verifiedRecord decodes a record the publisher received and checks it
// against the name, as delegated-ipfs.dev and every browser reader do.
func verifiedRecord(t *testing.T, name ipns.Name, raw []byte) (value string, seq uint64, validity time.Time) {
	t.Helper()
	rec, err := ipns.UnmarshalRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ipns.ValidateWithName(rec, name); err != nil {
		t.Fatalf("record does not verify for %s: %v", name, err)
	}
	p, err := rec.Value()
	if err != nil {
		t.Fatal(err)
	}
	if seq, err = rec.Sequence(); err != nil {
		t.Fatal(err)
	}
	if validity, err = rec.Validity(); err != nil {
		t.Fatal(err)
	}
	return p.String(), seq, validity
}

// TestIPNSPointerReachesDelegatedPublishersWhileTheDHTPutHangs: with
// delegated_publishers set, each new series' DPM reaches the publisher as a
// record signed by Kubo's key, with the configured lifetime and a rising
// sequence, although Kubo's name/publish never answers.
func TestIPNSPointerReachesDelegatedPublishersWhileTheDHTPutHangs(t *testing.T) {
	f := newHygieneFixture(t)
	signer := enableIPNSSigning(t, f.kubo, "celestrak-ipns", true)
	publisher := newFakeIPNSPublisher(t, http.StatusOK)
	t.Cleanup(f.service.hygiene.ipns.stop)
	f.service.SetPublicationPolicy(config.PublicationRetentionConfig{KeepSeries: 2}, []config.IPNSPointerConfig{
		{Schema: "IQC", SourceName: hygieneSource, Key: "celestrak-ipns", Lifetime: 168 * time.Hour, DelegatedPublishers: []string{publisher.server.URL}},
	})
	for round := 1; round <= 2; round++ {
		batch := "edition-" + strconv.Itoa(round)
		hygieneIngest(t, f.store, batch, round*30, round*30+30, "2026-11-21T03:00:00Z")
		result := f.publishBatch(t, batch, 0)
		started := time.Now()
		f.service.hygiene.ipns.wait()
		if took := time.Since(started); took > 30*time.Second {
			t.Fatalf("round %d: the pointer took %s: it waited on the hanging DHT put", round, took)
		}
		status, ok := f.service.hygiene.ipns.lastStatus("celestrak-ipns")
		if !ok || status.Err != nil || status.Name != signer.name.String() {
			t.Fatalf("round %d: pointer status %+v", round, status)
		}
		got := publisher.received(signer.name.String())
		if len(got) != round {
			t.Fatalf("round %d: publisher received %d records", round, len(got))
		}
		value, seq, validity := verifiedRecord(t, signer.name, got[round-1])
		if value != "/ipfs/"+result.ManifestCID || seq != uint64(round) {
			t.Fatalf("round %d: record names %s sequence %d, want /ipfs/%s sequence %d", round, value, seq, result.ManifestCID, round)
		}
		if until := time.Until(validity); until < 167*time.Hour || until > 169*time.Hour {
			t.Fatalf("round %d: record valid for %s, want the configured 168h", round, until)
		}
	}
}

// TestIPNSPointerFailsWhenNoDelegatedPublisherAccepts: a publisher that
// refuses the record leaves the pointer failed and says so in its status.
func TestIPNSPointerFailsWhenNoDelegatedPublisherAccepts(t *testing.T) {
	f := newHygieneFixture(t)
	enableIPNSSigning(t, f.kubo, "celestrak-ipns", true)
	publisher := newFakeIPNSPublisher(t, http.StatusBadGateway)
	t.Cleanup(f.service.hygiene.ipns.stop)
	f.service.SetPublicationPolicy(config.PublicationRetentionConfig{KeepSeries: 2}, []config.IPNSPointerConfig{
		{Schema: "IQC", SourceName: hygieneSource, Key: "celestrak-ipns", DelegatedPublishers: []string{publisher.server.URL}},
	})
	hygieneIngest(t, f.store, "b1", 0, 20, "2026-11-21T03:00:00Z")
	f.publishBatch(t, "b1", 0)
	f.service.hygiene.ipns.wait()
	status, ok := f.service.hygiene.ipns.lastStatus("celestrak-ipns")
	if !ok || status.Err == nil || !strings.Contains(status.Err.Error(), "no delegated publisher accepted sequence 1") || !strings.Contains(status.Err.Error(), "status 502") {
		t.Fatalf("pointer status %+v, want the refusal", status)
	}
}

// TestIPNSPointerResumesAfterRestart: after a restart (a fresh pointer
// worker) with no new series, ResumeIPNSPointers re-sends the pointer the key
// holds in Kubo, re-signed with a higher sequence, and republish_every goes on
// from there.
func TestIPNSPointerResumesAfterRestart(t *testing.T) {
	f := newHygieneFixture(t)
	signer := enableIPNSSigning(t, f.kubo, "celestrak-ipns", true)
	publisher := newFakeIPNSPublisher(t, http.StatusOK)
	f.service.SetPublicationPolicy(config.PublicationRetentionConfig{KeepSeries: 2}, []config.IPNSPointerConfig{
		{Schema: "IQC", SourceName: hygieneSource, Key: "celestrak-ipns", RepublishEvery: time.Hour, DelegatedPublishers: []string{publisher.server.URL}},
	})
	hygieneIngest(t, f.store, "b1", 0, 20, "2026-11-21T03:00:00Z")
	result := f.publishBatch(t, "b1", 0)
	f.service.hygiene.ipns.wait()
	if got := len(publisher.received(signer.name.String())); got != 1 {
		t.Fatalf("publisher received %d records before the restart", got)
	}

	// The restart: the old worker's timers die with the process.
	f.service.hygiene.ipns.stop()
	f.service.hygiene.ipns = newIPNSPointerWorker()
	t.Cleanup(f.service.hygiene.ipns.stop)
	f.service.ResumeIPNSPointers(10 * time.Millisecond)
	deadline := time.Now().Add(20 * time.Second)
	for len(publisher.received(signer.name.String())) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the pointer was not resumed within 20 s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.service.hygiene.ipns.wait()
	value, seq, _ := verifiedRecord(t, signer.name, publisher.received(signer.name.String())[1])
	if value != "/ipfs/"+result.ManifestCID || seq != 2 {
		t.Fatalf("resumed record names %s sequence %d, want /ipfs/%s sequence 2", value, seq, result.ManifestCID)
	}
	status, ok := f.service.hygiene.ipns.lastStatus("celestrak-ipns")
	if !ok || status.Err != nil || status.Path != "/ipfs/"+result.ManifestCID {
		t.Fatalf("resumed pointer status %+v", status)
	}
}

// TestIPNSPointerRepublishesWhileNoNewSeriesArrives: republish_every re-signs
// the current pointer with a new sequence and sends it again, with no new
// series (an upstream outage), and stops once a newer series replaces it.
func TestIPNSPointerRepublishesWhileNoNewSeriesArrives(t *testing.T) {
	f := newHygieneFixture(t)
	signer := enableIPNSSigning(t, f.kubo, "celestrak-ipns", false)
	publisher := newFakeIPNSPublisher(t, http.StatusOK)
	t.Cleanup(f.service.hygiene.ipns.stop)
	f.service.SetPublicationPolicy(config.PublicationRetentionConfig{KeepSeries: 2}, []config.IPNSPointerConfig{
		{Schema: "IQC", SourceName: hygieneSource, Key: "celestrak-ipns", RepublishEvery: 300 * time.Millisecond, DelegatedPublishers: []string{publisher.server.URL}},
	})
	hygieneIngest(t, f.store, "b1", 0, 20, "2026-11-21T03:00:00Z")
	first := f.publishBatch(t, "b1", 0)
	deadline := time.Now().Add(20 * time.Second)
	for len(publisher.received(signer.name.String())) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("publisher received %d records in 20 s, want the first and two republishes", len(publisher.received(signer.name.String())))
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := publisher.received(signer.name.String())
	for i, raw := range got[:3] {
		value, seq, _ := verifiedRecord(t, signer.name, raw)
		if value != "/ipfs/"+first.ManifestCID || seq != uint64(i+1) {
			t.Fatalf("record %d names %s sequence %d, want /ipfs/%s sequence %d", i, value, seq, first.ManifestCID, i+1)
		}
	}
	// A newer series replaces the pointer; from then on only it is republished.
	hygieneIngest(t, f.store, "b2", 100, 120, "2026-11-21T06:00:00Z")
	second := f.publishBatch(t, "b2", 0)
	time.Sleep(1200 * time.Millisecond)
	f.service.hygiene.ipns.stop()
	f.service.hygiene.ipns.wait()
	got = publisher.received(signer.name.String())
	sawSecond := false
	var last uint64
	for _, raw := range got {
		value, seq, _ := verifiedRecord(t, signer.name, raw)
		if seq <= last {
			t.Fatalf("sequence %d after %d", seq, last)
		}
		last = seq
		if value == "/ipfs/"+second.ManifestCID {
			sawSecond = true
		} else if sawSecond {
			t.Fatalf("the replaced pointer %s was republished after the newer one", value)
		}
	}
	if !sawSecond {
		t.Fatal("the newer series' pointer never reached the publisher")
	}
}
