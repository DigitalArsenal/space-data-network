package protocol

// Store reads served to followers (flatsql_sync_work.go), end to end: a
// real format 5 store behind the read-chunk handler on a libp2p host, asked
// by other libp2p hosts over TCP and yamux. A lane whose source name starts
// with "slow-" has a statement that reads until its context ends (or the test
// lets it finish): followWorkFault, as the sdn_e2e_follow_faults build sets
// it for the real-daemon tests.

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// workProducer is a producer host serving a format 5 store that holds lane
// publication-e2e/work (batch w1, 3 records), with slow lanes.
type workProducer struct {
	host    host.Host
	handler *FlatSQLSyncHandler
	// started names each slow statement once its read began.
	started chan string
	mu      sync.Mutex
	finish  map[string]chan struct{}
	ended   map[string]error
}

func newWorkProducer(t *testing.T) *workProducer {
	t.Helper()
	t.Setenv("SDN_STORE_FORMAT", "4")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if !store.Format4() {
		t.Fatal("SDN_STORE_FORMAT=4 did not open a format 5 store")
	}
	tags := storage.SourceTags{ProviderID: "publication-e2e", SourceName: "work", BatchID: "w1"}
	for i := 0; i < 3; i++ {
		record := sds.NewOMMBuilder().WithNoradCatID(uint32(700_000 + i)).WithObjectName("work").Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "producer", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	p := &workProducer{host: h, handler: NewFlatSQLSyncHandler(store), started: make(chan string, 32), finish: map[string]chan struct{}{},
		ended: map[string]error{}}
	p.handler.Register(h)
	prev := followWorkFault
	followWorkFault = func(ctx context.Context, source string) error {
		if !strings.HasPrefix(source, "slow-") {
			return nil
		}
		p.mu.Lock()
		finish := p.finish[source]
		if finish == nil {
			finish = make(chan struct{})
			p.finish[source] = finish
		}
		p.mu.Unlock()
		p.started <- source
		var err error
		select {
		case <-finish:
		case <-ctx.Done():
			err = ctx.Err()
		}
		p.mu.Lock()
		p.ended[source] = err
		p.mu.Unlock()
		return err
	}
	t.Cleanup(func() { followWorkFault = prev })
	return p
}

// let lets the slow statement of source finish.
func (p *workProducer) let(source string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finish[source] == nil {
		p.finish[source] = make(chan struct{})
	}
	close(p.finish[source])
}

// awaitStarted waits for the slow statements of sources to begin.
func (p *workProducer) awaitStarted(t *testing.T, sources ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, s := range sources {
		want[s] = true
	}
	deadline := time.After(10 * time.Second)
	for len(want) > 0 {
		select {
		case s := <-p.started:
			delete(want, s)
		case <-deadline:
			t.Fatalf("slow statements not begun within 10 s: %v", want)
		}
	}
}

// awaitRunning waits until n reads run at the producer, or fails after d.
func (p *workProducer) awaitRunning(t *testing.T, n int, d time.Duration) time.Duration {
	t.Helper()
	began := time.Now()
	for p.handler.followWork.running() != n {
		if time.Since(began) > d {
			t.Fatalf("%d reads run at the producer after %s, want %d", p.handler.followWork.running(), d, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(began)
}

// follower is a libp2p host connected to the producer.
func (p *workProducer) follower(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Connect(ctx, peer.AddrInfo{ID: p.host.ID(), Addrs: p.host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	return h
}

// askStatement sends one statement request of the lane named by source,
// closing its side after the request when halfClose (as sdn-js and
// syncwire.Call do), and returns the open stream.
func (p *workProducer) askStatement(t *testing.T, from host.Host, source string, halfClose bool) network.Stream {
	t.Helper()
	s, err := from.NewStream(context.Background(), p.host.ID(), libprotocol.ID(syncwire.ProtocolID(syncwire.OpReadChunk)))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetDeadline(time.Now().Add(3 * time.Minute))
	if err := syncwire.Request(s, syncwire.QRPFields{SchemaName: "OMM.fbs", ProviderID: "publication-e2e", SourceName: source, Limit: 1,
		Filters: []syncwire.QRPFilterField{{Field: syncwire.FilterProjection, Op: syncwire.QRPFilterEq, Value: syncwire.ProjectionStatement}}}); err != nil {
		t.Fatal(err)
	}
	if halfClose {
		_ = s.CloseWrite()
	}
	return s
}

// statement asks for a statement and reads its answer.
func (p *workProducer) statement(t *testing.T, from host.Host, source string) (syncwire.LaneStatement, time.Duration, error) {
	began := time.Now()
	s := p.askStatement(t, from, source, false)
	defer s.Close()
	st, err := syncwire.ReadStatementPage(s)
	return st, time.Since(began), err
}

func isBusy(err error) bool {
	var remote *syncwire.RemoteError
	return errors.As(err, &remote) && strings.Contains(remote.Code+" "+remote.Message, "busy")
}

// TestFollowWorkTrustedPeerServedPastStrangers: two peers without a trust
// relation hold the stranger slots with reads that do not end; a third such
// peer waits for a slot and is answered busy, while a peer the producer
// trusts is answered at once from the slot kept for trusted peers.
func TestFollowWorkTrustedPeerServedPastStrangers(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("a 1-core host has 1 stranger slot; this test holds 2")
	}
	p := newWorkProducer(t)
	trusted := p.follower(t)
	p.handler.SetFollowTrust(func(id peer.ID) bool { return id == trusted.ID() })
	prevWait := followStatementSlotWait
	followStatementSlotWait = 3 * time.Second
	t.Cleanup(func() { followStatementSlotWait = prevWait })

	s1, s2, s3 := p.follower(t), p.follower(t), p.follower(t)
	held1 := p.askStatement(t, s1, "slow-1", false)
	held2 := p.askStatement(t, s2, "slow-2", false)
	p.awaitStarted(t, "slow-1", "slow-2")
	p.awaitRunning(t, 2, 5*time.Second)

	type answer struct {
		took time.Duration
		err  error
	}
	third := make(chan answer, 1)
	go func() {
		_, took, err := p.statement(t, s3, "work")
		third <- answer{took, err}
	}()
	st, took, err := p.statement(t, trusted, "work")
	t.Logf("trusted peer's statement while two strangers hold their slots: %s, batch %q %d records, error %v", took.Round(time.Millisecond),
		st.BatchID, st.Records, err)
	if err != nil || st.BatchID != "w1" || st.Records != 3 {
		t.Fatalf("trusted peer's statement while two strangers hold their slots: batch %q %d records, error %v; want w1, 3, answered", st.BatchID,
			st.Records, err)
	}
	if took > 2*time.Second {
		t.Fatalf("trusted peer's statement took %s; want it served at once from the trusted slot", took)
	}
	a := <-third
	t.Logf("third stranger's statement: %s, %v", a.took.Round(time.Millisecond), a.err)
	if !isBusy(a.err) || a.took < 2500*time.Millisecond || a.took > 10*time.Second {
		t.Fatalf("third stranger's statement: %v after %s; want busy after its 3 s wait for a slot", a.err, a.took)
	}
	if n := p.handler.followWork.running(); n != 2 {
		t.Fatalf("%d reads run after the trusted one, want the two strangers' 2", n)
	}
	_ = held1.Reset()
	_ = held2.Reset()
	p.awaitRunning(t, 0, 5*time.Second)
}

// TestFollowWorkStatementWaitsForOwnPeer: a peer whose one slot is taken by
// its own slow read asks for a statement; the statement waits for the slot
// and is answered once the read ends, instead of busy at once.
func TestFollowWorkStatementWaitsForOwnPeer(t *testing.T) {
	p := newWorkProducer(t)
	f := p.follower(t)
	held := p.askStatement(t, f, "slow-own", false)
	defer held.Close()
	p.awaitStarted(t, "slow-own")
	const hold = 2 * time.Second
	time.AfterFunc(hold, func() { p.let("slow-own") })
	st, took, err := p.statement(t, f, "work")
	t.Logf("statement while the same peer's read runs for %s more: %s, batch %q, error %v", hold, took.Round(time.Millisecond), st.BatchID, err)
	if err != nil || st.BatchID != "w1" {
		t.Fatalf("statement while the same peer's read runs: batch %q, error %v; want w1, answered", st.BatchID, err)
	}
	if took < hold-200*time.Millisecond || took > followStatementSlotWait {
		t.Fatalf("statement answered after %s; want after the other read's %s and within the %s wait for a slot", took, hold,
			followStatementSlotWait)
	}
	if _, err := syncwire.ReadStatementPage(held); err != nil {
		t.Fatalf("the slow statement that was let finish: %v", err)
	}
}

// TestFollowWorkHalfClosedStreamEndsWithConnection: a peer that closes its
// side after its request still reads the answer, so its read goes on; once
// its connection closes the read stops and its slot is free, and the
// stream's watch has ended.
func TestFollowWorkHalfClosedStreamEndsWithConnection(t *testing.T) {
	p := newWorkProducer(t)
	f := p.follower(t)
	s := p.askStatement(t, f, "slow-half", true)
	p.awaitStarted(t, "slow-half")
	time.Sleep(2 * time.Second)
	if n := p.handler.followWork.running(); n != 1 {
		t.Fatalf("2 s after the peer closed its side, %d reads run; want its read going on", n)
	}
	if err := s.Conn().Close(); err != nil {
		t.Fatal(err)
	}
	stopped := p.awaitRunning(t, 0, 5*time.Second)
	t.Logf("the half-closed stream's read stopped %s after its connection closed", stopped.Round(time.Millisecond))
	p.mu.Lock()
	ended := p.ended["slow-half"]
	p.mu.Unlock()
	if !errors.Is(ended, context.Canceled) {
		t.Fatalf("the slow read ended with %v, want its context canceled", ended)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		if c := strings.Count(string(buf[:n]), "protocol.watchStream.func1"); c == 0 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("%d stream watches still run 5 s after the handler ended", c)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
