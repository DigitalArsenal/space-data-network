package trust

import (
	"math"
	"reflect"
	"testing"
)

// fixture: eve evaluates subjects over this web.
//
//	eve -> alice (0.9)      alice, bob, carol all trust dave
//	eve -> bob   (0.8)      stranger also trusts dave (outside eve's web)
//	alice -> carol (0.7)    → dave's trusters: alice, bob, carol, stranger
//	                          among eve's web: alice, bob, carol (3 of 4)
func fixtureGraph(t *testing.T) *Graph {
	t.Helper()
	g := NewGraph()
	for _, e := range []Edge{
		{Truster: "eve", Trustee: "alice", Weight: 0.9},
		{Truster: "eve", Trustee: "bob", Weight: 0.8},
		{Truster: "alice", Trustee: "carol", Weight: 0.7},
		{Truster: "alice", Trustee: "dave", Weight: 0.6},
		{Truster: "bob", Trustee: "dave", Weight: 0.5},
		{Truster: "carol", Trustee: "dave", Weight: 0.4},
		{Truster: "stranger", Trustee: "dave", Weight: 1.0},
	} {
		if err := g.SetEdge(e); err != nil {
			t.Fatalf("fixture edge %s->%s: %v", e.Truster, e.Trustee, err)
		}
	}
	return g
}

func fixtureFunds() MemoryFundsProvider {
	return MemoryFundsProvider{
		"dave": {
			{Type: FundStablecoin, Location: "0xdave", Amount: 50_000},
			{Type: FundBTC, Location: "bc1dave", Amount: 10_000},
			{Type: "exotic", Location: "?", Amount: 1_000}, // unknown type → FundOther weight
		},
		"alice":    {{Type: FundStablecoin, Location: "0xalice", Amount: 100_000}},
		"bob":      {{Type: FundETH, Location: "0xbob", Amount: 200_000}},
		"carol":    {{Type: FundBTC, Location: "bc1carol", Amount: 50_000}},
		"stranger": {{Type: FundStablecoin, Location: "0xs", Amount: 1_000_000}},
	}
}

func TestScoreInputsFixture(t *testing.T) {
	g := fixtureGraph(t)
	ev := NewEvaluator(g, fixtureFunds())
	in := ev.Inputs("eve", "dave")

	// Own funds: 50k*1.0 + 10k*0.9 + 1k*0.5 (unknown type → other) = 59_500.
	if math.Abs(in.OwnWeightedFunds-59_500) > 1e-9 {
		t.Fatalf("OwnWeightedFunds = %v, want 59500", in.OwnWeightedFunds)
	}
	if in.TrusterCountTotal != 4 {
		t.Fatalf("TrusterCountTotal = %d, want 4", in.TrusterCountTotal)
	}
	// alice, bob, carol are inside eve's web (carol transitively); stranger is not.
	if in.TrusterCountAmongTrusted != 3 {
		t.Fatalf("TrusterCountAmongTrusted = %d, want 3", in.TrusterCountAmongTrusted)
	}
	// Truster funds: alice 100k*1.0 + bob 200k*0.85 + carol 50k*0.9 + stranger 1M*1.0.
	wantTotal := 100_000.0 + 170_000 + 45_000 + 1_000_000
	if math.Abs(in.TrusterFundsTotal-wantTotal) > 1e-9 {
		t.Fatalf("TrusterFundsTotal = %v, want %v", in.TrusterFundsTotal, wantTotal)
	}
	// Among trusted: without stranger.
	if math.Abs(in.TrusterFundsAmongTrusted-(wantTotal-1_000_000)) > 1e-9 {
		t.Fatalf("TrusterFundsAmongTrusted = %v", in.TrusterFundsAmongTrusted)
	}
	// eve has no direct edge to dave.
	if in.DirectEdgeWeight != 0 {
		t.Fatalf("DirectEdgeWeight = %v, want 0", in.DirectEdgeWeight)
	}
	// …but does to alice.
	if got := ev.Inputs("eve", "alice").DirectEdgeWeight; got != 0.9 {
		t.Fatalf("eve->alice DirectEdgeWeight = %v, want 0.9", got)
	}
}

func TestFundTypeWeighting(t *testing.T) {
	cfg := DefaultScoringConfig()
	stable := cfg.WeightedFunds([]FundHolding{{Type: FundStablecoin, Amount: 10_000}})
	other := cfg.WeightedFunds([]FundHolding{{Type: FundOther, Amount: 10_000}})
	if !(stable > other) {
		t.Fatalf("stablecoin weight (%v) should exceed other (%v)", stable, other)
	}
	// Negative/zero amounts ignored.
	if got := cfg.WeightedFunds([]FundHolding{{Type: FundBTC, Amount: -5}, {Type: FundBTC, Amount: 0}}); got != 0 {
		t.Fatalf("negative/zero holdings scored %v", got)
	}
}

func TestDefaultScoreMonotoneAndBounded(t *testing.T) {
	g := fixtureGraph(t)
	funds := fixtureFunds()
	ev := NewEvaluator(g, funds)

	base := ev.Score("eve", "dave")
	if base <= 0 || base >= 1 {
		t.Fatalf("score %v outside (0,1)", base)
	}

	// More stablecoin funds → strictly higher score.
	richer := fixtureFunds()
	richer["dave"] = append(richer["dave"], FundHolding{Type: FundStablecoin, Location: "0xdave2", Amount: 500_000})
	ev2 := NewEvaluator(g, richer)
	if !(ev2.Score("eve", "dave") > base) {
		t.Fatalf("richer subject did not score higher: %v vs %v", ev2.Score("eve", "dave"), base)
	}

	// An additional truster INSIDE eve's web → strictly higher score.
	g3 := fixtureGraph(t)
	if err := g3.SetEdge(Edge{Truster: "eve", Trustee: "frank", Weight: 0.9}); err != nil {
		t.Fatal(err)
	}
	if err := g3.SetEdge(Edge{Truster: "frank", Trustee: "dave", Weight: 0.9}); err != nil {
		t.Fatal(err)
	}
	ev3 := NewEvaluator(g3, funds)
	if !(ev3.Score("eve", "dave") > base) {
		t.Fatalf("extra in-web truster did not raise score")
	}

	// An in-web endorsement moves the score more than a stranger endorsement
	// (the among-trusted components weight higher than the total-only ones).
	g4 := fixtureGraph(t)
	if err := g4.SetEdge(Edge{Truster: "stranger2", Trustee: "dave", Weight: 0.9}); err != nil {
		t.Fatal(err)
	}
	ev4 := NewEvaluator(g4, funds)
	inWebGain := ev3.Score("eve", "dave") - base
	strangerGain := ev4.Score("eve", "dave") - base
	if !(inWebGain > strangerGain) {
		t.Fatalf("in-web endorsement gain (%v) should exceed stranger gain (%v)", inWebGain, strangerGain)
	}
}

func TestPluggableScoreFunc(t *testing.T) {
	g := fixtureGraph(t)
	ev := NewEvaluator(g, fixtureFunds())
	// Replace the model: score = 1 iff any in-web truster exists.
	ev.ScoreFn = func(in ScoreInputs, _ ScoringConfig) float64 {
		if in.TrusterCountAmongTrusted > 0 {
			return 1
		}
		return 0
	}
	if got := ev.Score("eve", "dave"); got != 1 {
		t.Fatalf("custom fn: dave = %v, want 1", got)
	}
	// alice has no trusters other than eve... eve trusts alice directly;
	// alice's trusters = {eve}; eve IS in its own web → 1.
	if got := ev.Score("eve", "alice"); got != 1 {
		t.Fatalf("custom fn: alice = %v, want 1", got)
	}
	// stranger has no trusters at all → 0.
	if got := ev.Score("eve", "stranger"); got != 0 {
		t.Fatalf("custom fn: stranger = %v, want 0", got)
	}
}

func TestThresholdAndRanking(t *testing.T) {
	g := fixtureGraph(t)
	ev := NewEvaluator(g, fixtureFunds())

	// dave (rich + 4 trusters, 3 in-web) must outscore stranger (rich but
	// zero trusters); the threshold between them separates their status.
	daveScore := ev.Score("eve", "dave")
	strangerScore := ev.Score("eve", "stranger")
	if !(daveScore > strangerScore) {
		t.Fatalf("dave (%v) should outscore stranger (%v)", daveScore, strangerScore)
	}
	ev.Config.TrustThreshold = (daveScore + strangerScore) / 2
	if !ev.Trusted("eve", "dave") {
		t.Fatalf("dave (score %v) should clear threshold %v", daveScore, ev.Config.TrustThreshold)
	}
	if ev.Trusted("eve", "stranger") {
		t.Fatalf("stranger (score %v) should not clear threshold %v", strangerScore, ev.Config.TrustThreshold)
	}

	ranked := ev.RankedSubjects("eve")
	if len(ranked) != 5 { // alice bob carol dave stranger
		t.Fatalf("ranked %d subjects, want 5", len(ranked))
	}
	scores := ev.ScoreAll("eve")
	for i := 1; i < len(ranked); i++ {
		if scores[ranked[i-1]] < scores[ranked[i]] {
			t.Fatalf("ranking not descending at %d: %v", i, ranked)
		}
	}
	// Deterministic.
	if again := ev.RankedSubjects("eve"); !reflect.DeepEqual(again, ranked) {
		t.Fatalf("ranking not deterministic: %v vs %v", again, ranked)
	}
}
