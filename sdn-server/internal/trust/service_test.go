package trust

import (
	"math"
	"testing"
)

// assertCacheMatchesFullRecompute is the gold check for the incremental
// engine: after any mutation, every tracked evaluator's cached statuses must
// equal a from-scratch recompute. If the affected-set derivation ever misses
// a dependency, this catches it.
func assertCacheMatchesFullRecompute(t *testing.T, s *Service, evaluators ...string) {
	t.Helper()
	for _, evaluator := range evaluators {
		fresh := s.Evaluator().ScoreAll(evaluator)
		cached := s.Statuses(evaluator)
		if len(fresh) != len(cached) {
			t.Fatalf("evaluator %s: cache has %d subjects, full recompute %d", evaluator, len(cached), len(fresh))
		}
		for subject, want := range fresh {
			got, ok := cached[subject]
			if !ok {
				t.Fatalf("evaluator %s: cache missing subject %s", evaluator, subject)
			}
			if math.Abs(got.Score-want) > 1e-12 {
				t.Fatalf("evaluator %s subject %s: cached %v, fresh %v", evaluator, subject, got.Score, want)
			}
			if got.Trusted != (want >= s.Evaluator().Config.TrustThreshold) {
				t.Fatalf("evaluator %s subject %s: trusted flag stale", evaluator, subject)
			}
		}
	}
}

func newFixtureService(t *testing.T) *Service {
	t.Helper()
	s := NewService(fixtureGraph(t), fixtureFunds())
	s.nowMs = func() int64 { return 12345 }
	s.TrackEvaluator("eve")
	return s
}

func TestIncrementalMatchesFullRecompute(t *testing.T) {
	s := newFixtureService(t)
	assertCacheMatchesFullRecompute(t, s, "eve")

	// Funds change on a truster (alice) — hits dave/carol via truster-funds.
	s.UpdateFunds("alice", []FundHolding{{Type: FundStablecoin, Location: "0xalice", Amount: 900_000}})
	assertCacheMatchesFullRecompute(t, s, "eve")

	// Funds change on a leaf subject.
	s.UpdateFunds("dave", []FundHolding{{Type: FundBTC, Location: "bc1dave", Amount: 5_000}})
	assertCacheMatchesFullRecompute(t, s, "eve")

	// New edge that EXTENDS eve's web (eve->frank->dave).
	if _, err := s.SetEdge(Edge{Truster: "eve", Trustee: "frank", Weight: 0.9}); err != nil {
		t.Fatal(err)
	}
	assertCacheMatchesFullRecompute(t, s, "eve")
	if _, err := s.SetEdge(Edge{Truster: "frank", Trustee: "dave", Weight: 0.9}); err != nil {
		t.Fatal(err)
	}
	assertCacheMatchesFullRecompute(t, s, "eve")

	// Edge whose truster is OUTSIDE eve's web.
	if _, err := s.SetEdge(Edge{Truster: "stranger", Trustee: "carol", Weight: 0.3}); err != nil {
		t.Fatal(err)
	}
	assertCacheMatchesFullRecompute(t, s, "eve")

	// Remove an edge that SHRINKS eve's web (alice->carol: carol leaves the
	// web, and carol's contribution to dave's among-trusted splits moves).
	if _, err := s.RemoveEdge("alice", "carol"); err != nil {
		t.Fatal(err)
	}
	assertCacheMatchesFullRecompute(t, s, "eve")

	// Remove a whole node.
	if _, err := s.RemoveNode("bob"); err != nil {
		t.Fatal(err)
	}
	assertCacheMatchesFullRecompute(t, s, "eve")
}

func TestStatusFlipEventsOnThreshold(t *testing.T) {
	s := newFixtureService(t)
	// Pin the threshold just above dave's current score so the next boost
	// flips him to trusted.
	st, ok := s.Status("eve", "dave")
	if !ok {
		t.Fatal("no baseline status for dave")
	}
	s.Evaluator().Config.TrustThreshold = st.Score + 0.01
	// Rebuild baseline under the new threshold (re-track).
	s2 := NewService(fixtureGraph(t), fixtureFunds())
	s2.nowMs = func() int64 { return 777 }
	s2.Evaluator().Config.TrustThreshold = st.Score + 0.01
	s2.TrackEvaluator("eve")

	if got, _ := s2.Status("eve", "dave"); got.Trusted {
		t.Fatal("dave should start untrusted under the pinned threshold")
	}

	// Massive stablecoin boost on dave → own-funds component rises → flip.
	changes := s2.UpdateFunds("dave", []FundHolding{{Type: FundStablecoin, Location: "0xdave", Amount: 10_000_000}})
	var flip *StatusChange
	for i := range changes {
		if changes[i].Subject == "dave" && changes[i].Evaluator == "eve" {
			flip = &changes[i]
		}
	}
	if flip == nil {
		t.Fatalf("no flip emitted for dave; changes = %+v", changes)
	}
	if flip.OldTrusted || !flip.NewTrusted {
		t.Fatalf("flip direction wrong: %+v", flip)
	}
	if flip.AtMs != 777 {
		t.Fatalf("flip timestamp = %d", flip.AtMs)
	}
	if got, _ := s2.Status("eve", "dave"); !got.Trusted {
		t.Fatal("cache not updated to trusted")
	}

	// A no-op funds refresh emits NO events.
	if again := s2.UpdateFunds("dave", []FundHolding{{Type: FundStablecoin, Location: "0xdave", Amount: 10_000_000}}); len(again) != 0 {
		t.Fatalf("no-op update emitted %d events", len(again))
	}

	// Dropping the funds flips dave back off.
	back := s2.UpdateFunds("dave", nil)
	found := false
	for _, c := range back {
		if c.Subject == "dave" && c.OldTrusted && !c.NewTrusted {
			found = true
		}
	}
	if !found {
		t.Fatalf("no downward flip emitted: %+v", back)
	}
}

func TestNewSubjectAppearsInCache(t *testing.T) {
	s := newFixtureService(t)
	// A brand-new node enters via an edge — cache rows must appear.
	if _, err := s.SetEdge(Edge{Truster: "eve", Trustee: "newcomer", Weight: 1.0}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Status("eve", "newcomer"); !ok {
		t.Fatal("newcomer missing from cache")
	}
	assertCacheMatchesFullRecompute(t, s, "eve")
}

func TestNeighborhoodExposedForEvents(t *testing.T) {
	s := newFixtureService(t)
	// dave's neighborhood should include his trusters and (transitively) eve.
	hood := s.NeighborhoodOf("dave", 0)
	want := map[string]bool{"alice": true, "bob": true, "carol": true, "eve": true, "stranger": true}
	for _, id := range hood {
		if !want[id] {
			t.Fatalf("unexpected neighborhood member %s", id)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("neighborhood missing %v", want)
	}
}

func TestCycleStillRejectedThroughService(t *testing.T) {
	s := newFixtureService(t)
	if _, err := s.SetEdge(Edge{Truster: "dave", Trustee: "eve", Weight: 0.5}); err == nil {
		t.Fatal("cycle accepted through service")
	}
}
