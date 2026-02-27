package cluster

import (
	"testing"
	"time"
)

func newTestElection(localPeerID string, peerIDs ...string) (*ElectionState, *HealthTracker) {
	ht := NewHealthTracker()
	now := time.Now()
	for _, id := range peerIDs {
		ht.Update(&HealthStatus{
			PeerID:    id,
			Timestamp: now,
		})
	}
	return NewElectionState(localPeerID, ht), ht
}

func TestElectLowestPeerIDWins(t *testing.T) {
	es, _ := newTestElection("peer-c", "peer-a", "peer-b", "peer-c")
	now := time.Now()

	leader := es.Elect(now)
	if leader != "peer-a" {
		t.Fatalf("expected peer-a (lowest), got %q", leader)
	}
	if es.CurrentLeader() != "peer-a" {
		t.Fatalf("CurrentLeader() = %q, want peer-a", es.CurrentLeader())
	}
}

func TestElectSingleNode(t *testing.T) {
	es, _ := newTestElection("only-node", "only-node")
	now := time.Now()

	leader := es.Elect(now)
	if leader != "only-node" {
		t.Fatalf("single node should elect itself, got %q", leader)
	}
	if !es.IsLeader() {
		t.Fatal("single node should report IsLeader() = true")
	}
}

func TestElectNoLivePeers(t *testing.T) {
	ht := NewHealthTracker()
	// Add a peer that's already stale.
	ht.Update(&HealthStatus{
		PeerID:    "ghost",
		Timestamp: time.Now().Add(-60 * time.Second),
	})
	es := NewElectionState("local", ht)

	leader := es.Elect(time.Now())
	if leader != "" {
		t.Fatalf("expected empty leader with no live peers, got %q", leader)
	}
}

func TestElectExcludesStalePeers(t *testing.T) {
	ht := NewHealthTracker()
	now := time.Now()

	// peer-a is the lowest but stale; peer-b is alive.
	ht.Update(&HealthStatus{PeerID: "peer-a", Timestamp: now.Add(-60 * time.Second)})
	ht.Update(&HealthStatus{PeerID: "peer-b", Timestamp: now.Add(-5 * time.Second)})

	es := NewElectionState("peer-b", ht)
	leader := es.Elect(now)
	if leader != "peer-b" {
		t.Fatalf("expected peer-b (only live peer), got %q", leader)
	}
}

func TestIsLeader(t *testing.T) {
	es, _ := newTestElection("peer-a", "peer-a", "peer-b")
	now := time.Now()

	es.Elect(now)
	if !es.IsLeader() {
		t.Fatal("peer-a is lowest, should be leader")
	}

	es2, _ := newTestElection("peer-z", "peer-a", "peer-z")
	es2.Elect(now)
	if es2.IsLeader() {
		t.Fatal("peer-z is not lowest, should not be leader")
	}
}

func TestRecordHeartbeatResetsCounter(t *testing.T) {
	es, _ := newTestElection("local", "local", "peer-b")

	// Simulate some missed beats.
	es.RecordMiss("peer-b")
	es.RecordMiss("peer-b")

	// Heartbeat resets.
	es.RecordHeartbeat("peer-b")

	// Next miss should be 1, not 3.
	exceeded := es.RecordMiss("peer-b")
	if exceeded {
		t.Fatal("should not exceed threshold after heartbeat reset")
	}
}

func TestRecordMissThreshold(t *testing.T) {
	es, _ := newTestElection("local", "local", "leader")

	for i := 0; i < MissedBeatsThreshold-1; i++ {
		if es.RecordMiss("leader") {
			t.Fatalf("should not exceed threshold at miss %d", i+1)
		}
	}

	if !es.RecordMiss("leader") {
		t.Fatal("should exceed threshold at MissedBeatsThreshold")
	}
}

func TestShouldReelect(t *testing.T) {
	es, _ := newTestElection("local", "local", "leader")
	es.Elect(time.Now())

	// Initially the leader is "leader" (lowest alphabetically).
	// Wait — "leader" < "local", so "leader" wins.
	if es.CurrentLeader() != "leader" {
		t.Fatalf("expected leader, got %q", es.CurrentLeader())
	}

	// No missed beats yet.
	if es.ShouldReelect() {
		t.Fatal("should not re-elect when leader has 0 missed beats")
	}

	// Miss threshold beats.
	for i := 0; i < MissedBeatsThreshold; i++ {
		es.RecordMiss("leader")
	}

	if !es.ShouldReelect() {
		t.Fatal("should re-elect when leader exceeded missed beats threshold")
	}
}

func TestShouldReelectNoLeader(t *testing.T) {
	ht := NewHealthTracker()
	es := NewElectionState("local", ht)
	// No election has happened.
	if !es.ShouldReelect() {
		t.Fatal("should re-elect when no leader is set")
	}
}

func TestResolveSplitBrain(t *testing.T) {
	tests := []struct {
		name       string
		claimants  []string
		wantLeader string
	}{
		{"empty", []string{}, ""},
		{"single", []string{"peer-a"}, "peer-a"},
		{"two", []string{"peer-z", "peer-a"}, "peer-a"},
		{"three", []string{"peer-c", "peer-a", "peer-b"}, "peer-a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveSplitBrain(tt.claimants)
			if got != tt.wantLeader {
				t.Errorf("ResolveSplitBrain(%v) = %q, want %q", tt.claimants, got, tt.wantLeader)
			}
		})
	}
}

func TestElectionReelectionAfterLeaderFailure(t *testing.T) {
	ht := NewHealthTracker()
	now := time.Now()

	// Three-node cluster.
	ht.Update(&HealthStatus{PeerID: "node-a", Timestamp: now})
	ht.Update(&HealthStatus{PeerID: "node-b", Timestamp: now})
	ht.Update(&HealthStatus{PeerID: "node-c", Timestamp: now})

	es := NewElectionState("node-b", ht)

	// First election: node-a wins (lowest).
	leader := es.Elect(now)
	if leader != "node-a" {
		t.Fatalf("first election: expected node-a, got %q", leader)
	}

	// Simulate node-a going down — its timestamp goes stale.
	ht.Update(&HealthStatus{PeerID: "node-a", Timestamp: now.Add(-60 * time.Second)})

	// Re-election: node-b wins (next lowest among live).
	newNow := now.Add(1 * time.Second)
	leader = es.Elect(newNow)
	if leader != "node-b" {
		t.Fatalf("re-election: expected node-b, got %q", leader)
	}
	if !es.IsLeader() {
		t.Fatal("node-b should be the leader now")
	}
}
