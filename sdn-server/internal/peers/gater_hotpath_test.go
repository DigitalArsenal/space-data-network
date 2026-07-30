package peers

// Regression gate for `sdn-gater-registry-lock-on-hot-path` (Hermes,
// 2026-07-30) — requirement 4: "a test that fails if a gater callback blocks
// when the store is stalled."
//
// WHAT WENT WRONG IN PRODUCTION. peers.TrustedConnectionGater is installed as
// the libp2p ConnectionGater, so its methods run on every connection in both
// directions. InterceptUpgraded called RecordConnection -> UpdateStats, which
// took the Registry WRITE lock and then performed a synchronous FlatSQL
// round-trip (a WASM call) while still holding it. On host-01, 2026-07-30, one
// engine call stalled inside that round-trip. Because Go's RWMutex blocks new
// readers once a writer is queued, the goroutine dump showed:
//
//	g1966  41 min  InterceptUpgraded, HOLDING the registry write lock
//	53x    40+ min Registry.IsStrictMode via InterceptSecured  (inbound)
//	25x    40+ min Registry.IsStrictMode via InterceptPeerDial (outbound)
//
// The node accepted inbound TCP and never read it, could not dial out, answered
// /p2p/<peerid> with 502, and spaceaware.io/beta served an empty catalogue —
// from ONE stuck query. These tests fail if any of that becomes possible again.

import (
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// blockingPersistence is a store that never returns from Save — the reduced
// form of a stalled FlatSQL engine.
type blockingPersistence struct {
	entered chan struct{} // signalled once Save has been called
	release chan struct{} // closed by the test to let Save finish
}

func newBlockingPersistence() *blockingPersistence {
	return &blockingPersistence{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (b *blockingPersistence) Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return nil
}

func (b *blockingPersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	return make(map[peer.ID]*TrustedPeer), make(map[string]*PeerGroup), nil
}

// mustNotBlock runs fn and fails the test if it has not returned within
// margin. A gater callback is an admission decision on a connection's critical
// path: "slow" and "wedged" are the same outcome, so the assertion is a hard
// time bound rather than an eventual one.
func mustNotBlock(t *testing.T, margin time.Duration, what string, fn func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(margin):
		t.Fatalf("%s did not return within %s while the store was stalled — "+
			"a gater callback must never wait on persistence", what, margin)
		return 0
	}
}

// TestGaterHotPathSurvivesStalledStatsWrite is THE gate.
//
// Sequence: seed the registry with a working store (AddPeer persists
// synchronously, so it must not be pointed at the blocking store yet), swap in
// a store that blocks forever, drive the gater's connection path so the
// BACKGROUND writer is parked inside Save, then assert every gater callback
// still answers promptly.
func TestGaterHotPathSurvivesStalledStatsWrite(t *testing.T) {
	seed := &recordingPersistence{}
	registry := NewRegistry(true /* strict */, seed)
	defer registry.StopStatsWriter()

	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Trusted}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	gater := NewTrustedConnectionGater(registry)

	// Now stall the store and drive the statistics path through it.
	blocking := newBlockingPersistence()
	registry.mu.Lock()
	registry.persistence = blocking
	registry.mu.Unlock()

	registry.RecordConnection(testPeerID1)

	select {
	case <-blocking.entered:
		// The background writer is now parked inside Save.
	case <-time.After(5 * time.Second):
		t.Fatal("background stats writer never reached the store; test cannot prove anything")
	}
	defer close(blocking.release)

	// THE ASSERTIONS. Each of these blocked for 40+ minutes in production.
	if d := mustNotBlock(t, 2*time.Second, "IsStrictMode", func() {
		_ = registry.IsStrictMode()
	}); d > 100*time.Millisecond {
		t.Fatalf("IsStrictMode took %s with the store stalled; it must be a lock-free atomic read", d)
	}

	mustNotBlock(t, 2*time.Second, "InterceptSecured (inbound)", func() {
		_ = gater.InterceptSecured(0, testPeerID1, nil)
	})

	mustNotBlock(t, 2*time.Second, "InterceptPeerDial (outbound)", func() {
		_ = gater.InterceptPeerDial(testPeerID1)
	})

	mustNotBlock(t, 2*time.Second, "InterceptAccept", func() {
		_ = gater.InterceptAccept(nil)
	})

	// RecordConnection is the exact call InterceptUpgraded makes, and the one
	// that held the write lock across the store round-trip.
	mustNotBlock(t, 2*time.Second, "RecordConnection (InterceptUpgraded path)", func() {
		registry.RecordConnection(testPeerID1)
	})

	// A sustained burst must stay bounded — the production failure only became
	// visible under continuous wild inbound traffic (~5 conns/min for hours).
	d := mustNotBlock(t, 5*time.Second, "500 mixed gater decisions", func() {
		for i := 0; i < 500; i++ {
			_ = gater.InterceptSecured(0, testPeerID1, nil)
			_ = gater.InterceptPeerDial(testPeerID2)
			registry.RecordConnection(testPeerID1)
		}
	})
	t.Logf("500 mixed gater decisions with the store permanently stalled: %s", d)

	// And the in-memory effect must still be real: statistics are updated
	// synchronously even though their persistence is not.
	tp, err := registry.GetPeer(testPeerID1)
	if err != nil {
		t.Fatalf("GetPeer after the burst: %v", err)
	}
	if tp.ConnectionCount == 0 {
		t.Fatal("ConnectionCount is 0 — moving persistence off the hot path must not stop the counters from being kept")
	}
}

// recordingPersistence is a store that works, and remembers that it was used.
type recordingPersistence struct {
	saves int
}

func (r *recordingPersistence) Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error {
	r.saves++
	return nil
}

func (r *recordingPersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	return make(map[peer.ID]*TrustedPeer), make(map[string]*PeerGroup), nil
}

// TestRecordConnectionDoesNotPersistSynchronously pins the mechanism rather
// than the symptom: the gater's connection path must not call the store at all
// on the caller's goroutine.
func TestRecordConnectionDoesNotPersistSynchronously(t *testing.T) {
	blocking := newBlockingPersistence()
	registry := NewRegistry(false, nil) // no store while we seed
	defer registry.StopStatsWriter()

	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Trusted}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	registry.mu.Lock()
	registry.persistence = blocking
	registry.mu.Unlock()

	// If RecordConnection still persisted inline, this would never return.
	d := mustNotBlock(t, 2*time.Second, "RecordConnection with a blocking store", func() {
		registry.RecordConnection(testPeerID1)
	})
	if d > 200*time.Millisecond {
		t.Fatalf("RecordConnection took %s; it must not touch the store on the caller's goroutine", d)
	}
	close(blocking.release)
}

// TestStatsPersistenceDoesNotHoldTheRegistryLock proves the other half of
// requirement 3: while the store is being written, the Registry must remain
// readable AND writable, because the writer snapshots under the lock and calls
// Save outside it.
func TestStatsPersistenceDoesNotHoldTheRegistryLock(t *testing.T) {
	registry := NewRegistry(false, nil)
	defer registry.StopStatsWriter()
	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Trusted}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	blocking := newBlockingPersistence()
	registry.mu.Lock()
	registry.persistence = blocking
	registry.mu.Unlock()

	registry.RecordConnection(testPeerID1)
	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stats writer never reached the store")
	}
	defer close(blocking.release)

	// A reader must not be blocked by the in-flight store write.
	mustNotBlock(t, 2*time.Second, "GetPeer during a store write", func() {
		if _, err := registry.GetPeer(testPeerID1); err != nil && !errors.Is(err, ErrPeerNotFound) {
			t.Errorf("GetPeer: %v", err)
		}
	})

	// And so must a writer of unrelated in-memory state.
	mustNotBlock(t, 2*time.Second, "SetTrustLevel during a store write", func() {
		registry.strictMode.Store(true)
		registry.strictMode.Store(false)
	})
}
