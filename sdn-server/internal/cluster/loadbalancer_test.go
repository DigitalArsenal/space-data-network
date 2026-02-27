package cluster

import (
	"math"
	"testing"
)

func TestBackendScoreCalculation(t *testing.T) {
	tests := []struct {
		name     string
		score    BackendScore
		wantLow  float64
		wantHigh float64
	}{
		{
			name:     "idle backend",
			score:    BackendScore{Load: 0.0, LatencyMs: 0, FailureCount: 0},
			wantLow:  0,
			wantHigh: 0.01,
		},
		{
			name:     "fully loaded",
			score:    BackendScore{Load: 1.0, LatencyMs: 0, FailureCount: 0},
			wantLow:  49.99,
			wantHigh: 50.01,
		},
		{
			name:     "max latency",
			score:    BackendScore{Load: 0.0, LatencyMs: 5000, FailureCount: 0},
			wantLow:  29.99,
			wantHigh: 30.01,
		},
		{
			name:     "latency capped beyond max",
			score:    BackendScore{Load: 0.0, LatencyMs: 10000, FailureCount: 0},
			wantLow:  29.99,
			wantHigh: 30.01, // capped at 1.0 normalized
		},
		{
			name:     "max failures",
			score:    BackendScore{Load: 0.0, LatencyMs: 0, FailureCount: 3},
			wantLow:  19.99,
			wantHigh: 20.01,
		},
		{
			name:     "worst case",
			score:    BackendScore{Load: 1.0, LatencyMs: 5000, FailureCount: 3},
			wantLow:  99.99,
			wantHigh: 100.01,
		},
		{
			name:     "realistic mid-range",
			score:    BackendScore{Load: 0.3, LatencyMs: 150, FailureCount: 0},
			wantLow:  15,
			wantHigh: 16, // ~15 (load) + ~0.9 (latency) + 0 (failures)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.score.Score()
			if got < tt.wantLow || got > tt.wantHigh {
				t.Errorf("Score() = %f, want between %f and %f", got, tt.wantLow, tt.wantHigh)
			}
		})
	}
}

func TestBackendScoreOrdering(t *testing.T) {
	// An idle backend should always score lower (better) than a loaded one.
	idle := BackendScore{Load: 0.0, LatencyMs: 50, FailureCount: 0}
	busy := BackendScore{Load: 0.8, LatencyMs: 200, FailureCount: 1}

	if idle.Score() >= busy.Score() {
		t.Fatalf("idle (%f) should score lower than busy (%f)", idle.Score(), busy.Score())
	}
}

func TestLoadBalancerUpdateAndGetBest(t *testing.T) {
	lb := NewLoadBalancer()

	lb.Update(&BackendScore{PeerID: "fast", Load: 0.1, LatencyMs: 10, FailureCount: 0})
	lb.Update(&BackendScore{PeerID: "medium", Load: 0.5, LatencyMs: 100, FailureCount: 0})
	lb.Update(&BackendScore{PeerID: "slow", Load: 0.9, LatencyMs: 500, FailureCount: 1})

	best := lb.GetBestBackends(2)
	if len(best) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(best))
	}

	// First should be the "fast" backend.
	if best[0].PeerID != "fast" {
		t.Fatalf("best backend should be 'fast', got %q", best[0].PeerID)
	}
	// Second should be "medium".
	if best[1].PeerID != "medium" {
		t.Fatalf("second best should be 'medium', got %q", best[1].PeerID)
	}
}

func TestLoadBalancerGetBestRequestMoreThanAvailable(t *testing.T) {
	lb := NewLoadBalancer()
	lb.Update(&BackendScore{PeerID: "only-one", Load: 0.5, LatencyMs: 100})

	best := lb.GetBestBackends(10)
	if len(best) != 1 {
		t.Fatalf("expected 1 backend (all available), got %d", len(best))
	}
}

func TestLoadBalancerGetBestEmpty(t *testing.T) {
	lb := NewLoadBalancer()
	best := lb.GetBestBackends(5)
	if len(best) != 0 {
		t.Fatalf("expected 0 backends, got %d", len(best))
	}
}

func TestLoadBalancerMarkFailed(t *testing.T) {
	lb := NewLoadBalancer()
	lb.Update(&BackendScore{PeerID: "flaky", Load: 0.1, LatencyMs: 10, FailureCount: 0})

	// First two failures should not remove.
	for i := 0; i < maxFailures-1; i++ {
		removed := lb.MarkFailed("flaky")
		if removed {
			t.Fatalf("should not remove at failure %d", i+1)
		}
	}

	if lb.BackendCount() != 1 {
		t.Fatal("backend should still be tracked")
	}

	// Third failure removes it.
	removed := lb.MarkFailed("flaky")
	if !removed {
		t.Fatal("should remove at maxFailures")
	}
	if lb.BackendCount() != 0 {
		t.Fatal("backend should be removed")
	}
}

func TestLoadBalancerMarkFailedUnknown(t *testing.T) {
	lb := NewLoadBalancer()
	if lb.MarkFailed("nobody") {
		t.Fatal("MarkFailed on unknown peer should return false")
	}
}

func TestLoadBalancerMarkSuccess(t *testing.T) {
	lb := NewLoadBalancer()
	lb.Update(&BackendScore{PeerID: "recovering", Load: 0.1, LatencyMs: 10, FailureCount: 0})

	lb.MarkFailed("recovering")
	lb.MarkFailed("recovering")
	// failure count = 2

	lb.MarkSuccess("recovering")
	// failure count should be reset to 0

	// One more failure should not trigger removal.
	if lb.MarkFailed("recovering") {
		t.Fatal("should not remove after success reset")
	}
}

func TestLoadBalancerRemove(t *testing.T) {
	lb := NewLoadBalancer()
	lb.Update(&BackendScore{PeerID: "doomed"})

	lb.Remove("doomed")
	if lb.BackendCount() != 0 {
		t.Fatal("expected 0 after remove")
	}

	// Removing nonexistent is a no-op.
	lb.Remove("ghost")
}

func TestLoadBalancerBackendCount(t *testing.T) {
	lb := NewLoadBalancer()
	if lb.BackendCount() != 0 {
		t.Fatalf("expected 0, got %d", lb.BackendCount())
	}

	lb.Update(&BackendScore{PeerID: "a"})
	lb.Update(&BackendScore{PeerID: "b"})
	if lb.BackendCount() != 2 {
		t.Fatalf("expected 2, got %d", lb.BackendCount())
	}
}

func TestLoadBalancerSortStability(t *testing.T) {
	// When scores are very close, verify GetBestBackends still returns
	// a deterministic number of results.
	lb := NewLoadBalancer()
	for i := 0; i < 10; i++ {
		lb.Update(&BackendScore{
			PeerID:    string(rune('a' + i)),
			Load:      0.5,
			LatencyMs: 100,
		})
	}

	best := lb.GetBestBackends(3)
	if len(best) != 3 {
		t.Fatalf("expected 3 backends, got %d", len(best))
	}
}

func TestBackendScoreLatencyNormalization(t *testing.T) {
	// Verify that latency beyond maxLatencyMs is capped.
	lowLat := BackendScore{LatencyMs: 100}
	highLat := BackendScore{LatencyMs: 50000}

	// Both should have load and failure contributions of 0.
	// lowLat: 100/5000 * 30 = 0.6
	// highLat: capped at 1.0 * 30 = 30.0
	if lowLat.Score() >= highLat.Score() {
		t.Fatal("low latency should score lower than high latency")
	}

	// Verify the cap: 50000ms should score the same as 5000ms.
	maxLat := BackendScore{LatencyMs: maxLatencyMs}
	if math.Abs(highLat.Score()-maxLat.Score()) > 0.001 {
		t.Fatalf("scores beyond maxLatencyMs should be capped: %f vs %f",
			highLat.Score(), maxLat.Score())
	}
}

func TestLoadBalancerFailureIncrementsScore(t *testing.T) {
	lb := NewLoadBalancer()
	lb.Update(&BackendScore{PeerID: "target", Load: 0.1, LatencyMs: 10, FailureCount: 0})

	before := lb.GetBestBackends(1)[0].Score()

	lb.MarkFailed("target")
	after := lb.GetBestBackends(1)[0].Score()

	if after <= before {
		t.Fatalf("score should increase after failure: before=%f, after=%f", before, after)
	}
}
