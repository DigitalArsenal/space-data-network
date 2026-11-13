package kubo_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bsmsg "github.com/ipfs/boxo/bitswap/message"
	pb "github.com/ipfs/boxo/bitswap/message/pb"
	"github.com/ipfs/boxo/bitswap/network/bsnet"
	"github.com/ipfs/boxo/blockstore"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/kubo"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

type healthPausedHost struct {
	host.Host
	entered chan chan struct{}
	cleanup <-chan struct{}
}

func (h *healthPausedHost) NewStream(ctx context.Context, p peer.ID, ids ...protocol.ID) (network.Stream, error) {
	for _, id := range ids {
		if id != bsnet.ProtocolBitswap {
			continue
		}
		release := make(chan struct{})
		select {
		case h.entered <- release:
		case <-h.cleanup:
			return nil, context.Canceled
		}
		select {
		case <-release:
		case <-h.cleanup:
			return nil, context.Canceled
		}
		break
	}
	return h.Host.NewStream(ctx, p, ids...)
}

type healthStore struct {
	blockstore.GCBlockstore
	fail   atomic.Bool
	checks atomic.Int64
	// Set before the asynchronous monitor starts; unchanged until it stops.
	hold     <-chan struct{}
	entered  chan struct{}
	finished chan struct{}
}

func (s *healthStore) Has(ctx context.Context, c cid.Cid) (bool, error) {
	s.checks.Add(1)
	if s.hold != nil {
		s.entered <- struct{}{}
		<-s.hold // deliberately ignores cancellation until test cleanup
		defer close(s.finished)
	}
	if s.fail.Load() {
		return false, errors.New("injected local presence failure")
	}
	return s.GCBlockstore.Has(ctx, c)
}

func TestBitswapSendStalledHealth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	var paused *healthPausedHost
	key, _ := newKey(t)
	a := kubo.StartHealthFixture(t, kubo.Config{
		RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: key, DHT: kubo.DHTOff,
		HostOptions: []libp2p.Option{libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0")},
	}, func(h host.Host) host.Host {
		paused = &healthPausedHost{Host: h, entered: make(chan chan struct{}, 16), cleanup: release}
		return paused
	})
	store := &healthStore{GCBlockstore: a.IPFS().Blockstore}
	a.IPFS().Blockstore = store // only the sampler; Bitswap retains the real store
	alerts := ops.NewRegistry()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	alerts.SetClock(func() time.Time { return now })
	probe := kubo.HealthProbeForTest(a, alerts)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", api.HealthHandler(alerts))
	server := httptest.NewServer(mux)
	defer server.Close()
	assertHealth := func(active bool) {
		t.Helper()
		resp, err := http.Get(server.URL + "/api/v1/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Status string
			Alerts ops.Counts
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		status, warnings := ops.StatusOK, 0
		if active {
			status, warnings = ops.StatusDegraded, 1
		}
		if resp.StatusCode != http.StatusOK || body.Status != status || body.Alerts.Warning != warnings || body.Alerts.Error != 0 {
			t.Fatalf("health = HTTP %d %+v, want %s with %d warning", resp.StatusCode, body, status, warnings)
		}
		if active {
			entries := alerts.Active()
			if len(entries) != 1 || entries[0].Kind != ops.KindBitswapSendStalled {
				t.Fatalf("wrong alert: %+v", entries)
			}
		}
	}
	step := func(d time.Duration) (bool, bool, uint64) { now = now.Add(d); return probe(ctx, now) }
	for i := 0; i < 20; i++ {
		step(30 * time.Second)
	}
	assertHealth(false) // an idle node does not stall, however old its last send
	info := peer.AddrInfo{ID: a.Host().ID(), Addrs: a.Host().Addrs()}
	newPeer := func() *kubo.Node {
		key, _ := newKey(t)
		n := start(t, filepath.Join(t.TempDir(), "kubo"), key, nil, "")
		if err := n.Host().Connect(ctx, info); err != nil {
			t.Fatal(err)
		}
		return n
	}
	want := func(n *kubo.Node, entries []cid.Cid, cancelWant bool) {
		t.Helper()
		m := bsmsg.New(false)
		for _, c := range entries {
			if cancelWant {
				m.Cancel(c)
			} else {
				m.AddEntry(c, 1, pb.Message_Wantlist_Block, false)
			}
		}
		s, err := n.Host().NewStream(ctx, info.ID, bsnet.ProtocolBitswap)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.ToNetV1(s); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	waitWant := func(n *kubo.Node, c cid.Cid, present bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			found := false
			for _, k := range a.IPFS().Bitswap.WantlistForPeer(n.Host().ID()) {
				found = found || k.Equals(c)
			}
			if found == present {
				return
			}
			select {
			case <-deadline:
				t.Fatal("remote wantlist did not reach the expected state")
			case <-time.After(time.Millisecond):
			}
		}
	}
	put := func(label string) blocks.Block {
		b := blocks.NewBlock([]byte(label))
		if err := a.IPFS().Blockstore.Put(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	nextSend := func() chan struct{} {
		select {
		case ch := <-paused.entered:
			return ch
		case <-time.After(5 * time.Second):
			t.Fatal("send worker did not reach barrier")
			return nil
		}
	}
	b := newPeer()
	missing := blocks.NewBlock([]byte("not held")).Cid()
	want(b, []cid.Cid{missing}, false)
	waitWant(b, missing, true)
	for i := 0; i < 20; i++ {
		step(30 * time.Second)
	}
	assertHealth(false) // wants for absent blocks cannot start the timer
	var sends []chan struct{}
	for i := 0; i < 8; i++ {
		n := newPeer()
		held := put(fmt.Sprintf("held for blocked send %d", i))
		want(n, []cid.Cid{held.Cid()}, false)
		sends = append(sends, nextSend())
	}
	fresh := put("fresh demand queued behind eight sends")
	want(b, []cid.Cid{fresh.Cid()}, false)
	waitWant(b, fresh.Cid(), true)
	step(time.Hour)
	assertHealth(false) // timer starts at first demand, not last send
	for i := 0; i < 8; i++ {
		step(30 * time.Second)
		assertHealth(false)
	}
	store.fail.Store(true)
	step(30 * time.Second)
	step(10 * time.Minute)
	assertHealth(false)
	store.fail.Store(false)
	// Resume after an unknown gap: five new minutes must be observed.
	for {
		known, demand, _ := step(30 * time.Second)
		if known && demand {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("no complete demand sample")
		}
	}
	for i := 0; i < 9; i++ {
		step(30 * time.Second)
		assertHealth(false)
	}
	step(30 * time.Second)
	assertHealth(true)
	store.fail.Store(true)
	step(time.Minute)
	assertHealth(true)
	store.fail.Store(false)
	before, err := a.IPFS().Bitswap.Stat()
	if err != nil {
		t.Fatal(err)
	}
	close(sends[0]) // one genuine network send completes; the next queued send pauses
	_ = nextSend()
	progressCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	for {
		stats, err := a.IPFS().Bitswap.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if stats.BlocksSent > before.BlocksSent {
			break
		}
		select {
		case <-progressCtx.Done():
			t.Fatal("real send made no BlocksSent progress")
		case <-time.After(time.Millisecond):
		}
	}
	more := put("demand remains after real progress")
	want(b, []cid.Cid{more.Cid()}, false)
	waitWant(b, more.Cid(), true)
	for {
		known, demand, _ := step(30 * time.Second)
		if known && demand {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("no complete sample after send")
		}
	}
	assertHealth(false) // genuine progress clears even with fresh satisfiable demand
	for i := 0; i < 10; i++ {
		step(30 * time.Second)
	}
	assertHealth(true)
	want(b, []cid.Cid{more.Cid()}, true)
	waitWant(b, more.Cid(), false)
	for i := 0; i < 2; i++ {
		step(30 * time.Second)
	}
	assertHealth(false) // a complete no-demand sample also clears

	// A large absent wantlist is bounded and unknown, never a healthy sample.
	var many []cid.Cid
	for i := 0; i < 600; i++ {
		many = append(many, blocks.NewBlock([]byte(fmt.Sprintf("absent %d", i))).Cid())
	}
	want(b, many, false)
	waitWant(b, many[len(many)-1], true)
	checks := store.checks.Load()
	known, _, _ := step(30 * time.Second)
	if known || store.checks.Load()-checks > 512 {
		t.Fatalf("partial scan known=%v, Has calls=%d", known, store.checks.Load()-checks)
	}
	assertHealth(false)
	canceled, cancelSample := context.WithCancel(ctx)
	cancelSample()
	probe(canceled, now.Add(time.Hour))
	assertHealth(false)

	// Even a store that ignores cancellation gets only one outstanding sample.
	hold := make(chan struct{})
	var releaseHold sync.Once
	finishHold := func() { releaseHold.Do(func() { close(hold) }) }
	defer finishHold()
	store.hold = hold
	store.entered = make(chan struct{}, 1)
	store.finished = make(chan struct{})
	runCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() { kubo.RunHealthMonitorForTest(runCtx, a, alerts, ticks); close(done) }()
	checks = store.checks.Load()
	ticks <- now
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sample never entered Has")
	}
	ticks <- now.Add(30 * time.Second)
	ticks <- now.Add(time.Minute)
	if got := store.checks.Load() - checks; got != 1 {
		t.Fatalf("concurrent Has calls: %d", got)
	}
	stopMonitor()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not stop with node lifetime")
	}
	finishHold()
	select {
	case <-store.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("held sample did not finish during cleanup")
	}
	t.Log("health warning: fresh demand, five-minute threshold, unknown gaps, genuine send recovery, idle/absent wants, bounded scan and one-flight cancellation verified")
}
