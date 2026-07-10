package trust

import (
	"errors"
	"math"
	"testing"
)

// Reuses fixtureGraph/fixtureFunds from scoring_test.go: eve evaluates dave,
// who has trusters alice, bob, carol (in eve's web) and stranger (not).

func TestBondedKeyScoresHigherThanUnbonded(t *testing.T) {
	g := fixtureGraph(t)
	funds := fixtureFunds()

	unbonded := NewEvaluator(g, funds)
	bonded := NewEvaluator(g, funds, MemoryBondSource{
		ChainID: ChainBitcoin,
		Amounts: map[string]float64{"dave": 20_000},
	})

	base := unbonded.Score("eve", "dave")
	got := bonded.Score("eve", "dave")
	if !(got > base) {
		t.Fatalf("bonded score (%v) should exceed unbonded score (%v)", got, base)
	}
}

func TestUnreachableChainDegradesGracefully(t *testing.T) {
	g := fixtureGraph(t)
	funds := fixtureFunds()

	unbonded := NewEvaluator(g, funds)
	unreachable := NewEvaluator(g, funds, MemoryBondSource{
		ChainID: ChainEthereum,
		Errs:    map[string]error{"dave": errors.New("rpc: connection refused")},
	})

	want := unbonded.Score("eve", "dave")
	got := unreachable.Score("eve", "dave")
	if got != want {
		t.Fatalf("unreachable-source score = %v, want exactly unbonded score %v", got, want)
	}

	in := unreachable.Inputs("eve", "dave")
	if in.BondWeightedBalance != 0 || in.BondBonus != 0 {
		t.Fatalf("unreachable source leaked a balance/bonus: %+v", in)
	}
}

func TestBondBonusIsBounded(t *testing.T) {
	g := fixtureGraph(t)
	funds := fixtureFunds()
	cfg := DefaultScoringConfig()

	huge := NewEvaluator(g, funds, MemoryBondSource{
		ChainID: ChainBitcoin,
		Amounts: map[string]float64{"dave": 1e18}, // absurd balance
	})

	in := huge.Inputs("eve", "dave")
	if in.BondBonus > cfg.BondBonusCap+1e-9 {
		t.Fatalf("bond bonus %v exceeds documented cap %v", in.BondBonus, cfg.BondBonusCap)
	}
	if got := huge.Score("eve", "dave"); got > 1+1e-9 {
		t.Fatalf("score %v exceeds 1 despite absurd balance", got)
	}

	// A merely large balance should already be close to, but not exceed,
	// the cap (saturation), and a bigger balance never scores less.
	moderate := NewEvaluator(g, funds, MemoryBondSource{
		ChainID: ChainBitcoin,
		Amounts: map[string]float64{"dave": 5_000_000},
	})
	if got := moderate.Inputs("eve", "dave").BondBonus; got > cfg.BondBonusCap+1e-9 {
		t.Fatalf("moderate bond bonus %v exceeds cap %v", got, cfg.BondBonusCap)
	}
}

func TestBondAloneDoesNotCrossValidityThreshold(t *testing.T) {
	g := fixtureGraph(t)
	if err := g.AddNode("ghost"); err != nil {
		t.Fatal(err)
	}
	funds := fixtureFunds()

	// ghost has zero trusters, zero funds, and no direct edge from eve — an
	// entirely unvouched key — but an enormous bond balance.
	ev := NewEvaluator(g, funds, MemoryBondSource{
		ChainID: ChainBitcoin,
		Amounts: map[string]float64{"ghost": 50_000_000},
	})

	in := ev.Inputs("eve", "ghost")
	if in.BondWeightedBalance <= 0 {
		t.Fatalf("expected a nonzero weighted balance for ghost, got %v", in.BondWeightedBalance)
	}
	if in.BondBonus != 0 {
		t.Fatalf("bond bonus for a zero-truster subject = %v, want 0 (bond must not substitute for trusters)", in.BondBonus)
	}
	if got := ev.Score("eve", "ghost"); got != 0 {
		t.Fatalf("ghost score = %v, want exactly 0 (bond had zero effect)", got)
	}
	if ev.Trusted("eve", "ghost") {
		t.Fatalf("ghost should not be trusted: a bond cannot promote a key past the web-of-trust rule on its own")
	}
}

func TestMultipleBondSourcesCombine(t *testing.T) {
	g := fixtureGraph(t)
	funds := fixtureFunds()
	cfg := DefaultScoringConfig()

	btc := MemoryBondSource{ChainID: ChainBitcoin, Amounts: map[string]float64{"dave": 10_000}}
	eth := MemoryBondSource{ChainID: ChainEthereum, Amounts: map[string]float64{"dave": 20_000}}

	btcOnly := NewEvaluator(g, funds, btc)
	combined := NewEvaluator(g, funds, btc, eth)

	wantBalance := 10_000*cfg.BondChainWeights[ChainBitcoin] + 20_000*cfg.BondChainWeights[ChainEthereum]
	got := combined.Inputs("eve", "dave").BondWeightedBalance
	if math.Abs(got-wantBalance) > 1e-9 {
		t.Fatalf("combined weighted balance = %v, want %v (sources should sum)", got, wantBalance)
	}

	// Combining sources should never score lower than either alone.
	if !(combined.Score("eve", "dave") > btcOnly.Score("eve", "dave")) {
		t.Fatalf("combined sources (%v) did not outscore btc-only (%v)",
			combined.Score("eve", "dave"), btcOnly.Score("eve", "dave"))
	}
}

func TestBondBalanceIgnoresNonPositiveAndNilSources(t *testing.T) {
	g := fixtureGraph(t)
	funds := fixtureFunds()

	tests := []struct {
		name  string
		bonds []BondSource
	}{
		{"zero amount", []BondSource{MemoryBondSource{ChainID: ChainBitcoin, Amounts: map[string]float64{"dave": 0}}}},
		{"negative amount", []BondSource{MemoryBondSource{ChainID: ChainBitcoin, Amounts: map[string]float64{"dave": -100}}}},
		{"no amount for key", []BondSource{MemoryBondSource{ChainID: ChainBitcoin, Amounts: map[string]float64{"someone-else": 5_000}}}},
		{"nil source in slice", []BondSource{nil, MemoryBondSource{ChainID: ChainBitcoin, Amounts: map[string]float64{"dave": 0}}}},
		{"no sources", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := NewEvaluator(g, funds, tc.bonds...)
			if got := ev.BondBalance("dave"); got != 0 {
				t.Fatalf("BondBalance = %v, want 0", got)
			}
		})
	}
}

func TestBondBonusCapZeroDisablesWeighting(t *testing.T) {
	g := fixtureGraph(t)
	funds := fixtureFunds()
	ev := NewEvaluator(g, funds, MemoryBondSource{
		ChainID: ChainBitcoin,
		Amounts: map[string]float64{"dave": 1_000_000},
	})
	ev.Config.BondBonusCap = 0
	if got := ev.Inputs("eve", "dave").BondBonus; got != 0 {
		t.Fatalf("BondBonusCap=0 should disable weighting entirely, got bonus %v", got)
	}
}

func TestBondChainWeightDefaultsToOneForUnlistedChain(t *testing.T) {
	cfg := DefaultScoringConfig()
	const custom Chain = "dogecoin" // not in DefaultScoringConfig's BondChainWeights
	if w := cfg.bondWeight(custom); w != 1.0 {
		t.Fatalf("unlisted chain weight = %v, want 1.0", w)
	}
	cfg.BondChainWeights = nil
	if w := cfg.bondWeight(ChainBitcoin); w != 1.0 {
		t.Fatalf("nil BondChainWeights should default every chain to 1.0, got %v", w)
	}
}
