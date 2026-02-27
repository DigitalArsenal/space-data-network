package cluster

import (
	"testing"
	"time"
)

func TestHealthTrackerUpdateAndGet(t *testing.T) {
	ht := NewHealthTracker()

	status := &HealthStatus{
		PeerID:    "peer-a",
		OnionHost: "test.onion",
		Role:      RolePrimary,
		Load:      0.5,
		Timestamp: time.Now(),
	}

	ht.Update(status)

	got := ht.Get("peer-a")
	if got == nil {
		t.Fatal("expected non-nil status for peer-a")
	}
	if got.PeerID != "peer-a" {
		t.Fatalf("PeerID = %q, want %q", got.PeerID, "peer-a")
	}
	if got.Load != 0.5 {
		t.Fatalf("Load = %f, want 0.5", got.Load)
	}
}

func TestHealthTrackerGetUnknownPeer(t *testing.T) {
	ht := NewHealthTracker()
	if got := ht.Get("nonexistent"); got != nil {
		t.Fatalf("expected nil for unknown peer, got %+v", got)
	}
}

func TestHealthTrackerIsAlive(t *testing.T) {
	ht := NewHealthTracker()
	now := time.Now()

	// Fresh heartbeat — alive.
	ht.Update(&HealthStatus{PeerID: "alive", Timestamp: now.Add(-5 * time.Second)})
	if !ht.IsAlive("alive", now) {
		t.Fatal("expected peer to be alive (5s ago)")
	}

	// Stale heartbeat — not alive.
	ht.Update(&HealthStatus{PeerID: "stale", Timestamp: now.Add(-60 * time.Second)})
	if ht.IsAlive("stale", now) {
		t.Fatal("expected peer to be stale (60s ago)")
	}

	// Unknown peer — not alive.
	if ht.IsAlive("unknown", now) {
		t.Fatal("expected unknown peer to be not alive")
	}

	// Exactly at the boundary.
	ht.Update(&HealthStatus{PeerID: "boundary", Timestamp: now.Add(-StaleTimeout)})
	if ht.IsAlive("boundary", now) {
		t.Fatal("expected peer at exactly StaleTimeout to be not alive")
	}

	// Just inside the boundary.
	ht.Update(&HealthStatus{PeerID: "barely", Timestamp: now.Add(-StaleTimeout + time.Millisecond)})
	if !ht.IsAlive("barely", now) {
		t.Fatal("expected peer just inside StaleTimeout to be alive")
	}
}

func TestHealthTrackerLivePeers(t *testing.T) {
	ht := NewHealthTracker()
	now := time.Now()

	ht.Update(&HealthStatus{PeerID: "a", Timestamp: now.Add(-5 * time.Second)})
	ht.Update(&HealthStatus{PeerID: "b", Timestamp: now.Add(-10 * time.Second)})
	ht.Update(&HealthStatus{PeerID: "c", Timestamp: now.Add(-60 * time.Second)}) // stale

	live := ht.LivePeers(now)
	if len(live) != 2 {
		t.Fatalf("expected 2 live peers, got %d", len(live))
	}

	ids := map[string]bool{}
	for _, p := range live {
		ids[p.PeerID] = true
	}
	if !ids["a"] || !ids["b"] {
		t.Fatalf("expected peers a and b, got %v", ids)
	}
	if ids["c"] {
		t.Fatal("stale peer c should not be in live list")
	}
}

func TestHealthTrackerRemove(t *testing.T) {
	ht := NewHealthTracker()
	ht.Update(&HealthStatus{PeerID: "x", Timestamp: time.Now()})

	if ht.PeerCount() != 1 {
		t.Fatalf("expected 1 peer, got %d", ht.PeerCount())
	}

	ht.Remove("x")
	if ht.PeerCount() != 0 {
		t.Fatalf("expected 0 peers after remove, got %d", ht.PeerCount())
	}
	if ht.Get("x") != nil {
		t.Fatal("expected nil after remove")
	}
}

func TestHealthTrackerPeerCount(t *testing.T) {
	ht := NewHealthTracker()
	if ht.PeerCount() != 0 {
		t.Fatalf("expected 0, got %d", ht.PeerCount())
	}

	ht.Update(&HealthStatus{PeerID: "a", Timestamp: time.Now()})
	ht.Update(&HealthStatus{PeerID: "b", Timestamp: time.Now()})
	if ht.PeerCount() != 2 {
		t.Fatalf("expected 2, got %d", ht.PeerCount())
	}

	// Updating same peer doesn't increase count.
	ht.Update(&HealthStatus{PeerID: "a", Timestamp: time.Now()})
	if ht.PeerCount() != 2 {
		t.Fatalf("expected 2 after duplicate update, got %d", ht.PeerCount())
	}
}

func TestHealthTrackerUpdateOverwrites(t *testing.T) {
	ht := NewHealthTracker()

	ht.Update(&HealthStatus{PeerID: "x", Load: 0.1, Timestamp: time.Now()})
	ht.Update(&HealthStatus{PeerID: "x", Load: 0.9, Timestamp: time.Now()})

	got := ht.Get("x")
	if got.Load != 0.9 {
		t.Fatalf("expected updated load 0.9, got %f", got.Load)
	}
}

func TestRoleConstants(t *testing.T) {
	if RolePrimary != "primary" {
		t.Fatalf("RolePrimary = %q", RolePrimary)
	}
	if RoleReplica != "replica" {
		t.Fatalf("RoleReplica = %q", RoleReplica)
	}
	if RoleAuto != "auto" {
		t.Fatalf("RoleAuto = %q", RoleAuto)
	}
}
