package trust

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

const nowMs = int64(1_780_000_000_000)

func usdHolding(chain, addr, symbol string, amount, usd float64, heldSinceMs int64) Holding {
	return Holding{ChainID: chain, Address: addr, Symbol: symbol, Amount: amount, Decimals: 9, USD: usd, PricedInUSD: true, HeldSinceMs: heldSinceMs, ObservedAtMs: nowMs, BlockRef: "block-1", SourceQuery: "test"}
}

func TestMinValueLockedBoundary(t *testing.T) {
	p := Predicate{ID: "v", Kind: PredicateMinValueLocked, MinValue: 100_000, ValueCurrency: "USD"} // $1,000.00
	exactly := SubjectFacts{Holdings: []Holding{usdHolding("eip155:1", "0xa", "ETH", 0.5, 1000, 0)}}
	if r := EvaluatePredicate(p, exactly, nowMs); !r.Passed || r.MeasuredValue != 100_000 {
		t.Fatalf("exact value must pass: %+v", r)
	}
	below := SubjectFacts{Holdings: []Holding{usdHolding("eip155:1", "0xa", "ETH", 0.5, 999.99, 0)}}
	if r := EvaluatePredicate(p, below, nowMs); r.Passed {
		t.Fatalf("one cent below must fail: %+v", r)
	}
	split := SubjectFacts{Holdings: []Holding{usdHolding("eip155:1", "0xa", "ETH", 0.2, 400, 0), usdHolding("solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp", "So1", "SOL", 4, 600, 0)}}
	if r := EvaluatePredicate(p, split, nowMs); !r.Passed || len(r.BondEvidence) != 2 {
		t.Fatalf("value sums across chains: %+v", r)
	}
	refused := SubjectFacts{HoldingsErr: "module timeout"}
	if r := EvaluatePredicate(p, refused, nowMs); r.Passed || !strings.Contains(r.EvidenceText, "did not answer") {
		t.Fatalf("a refused lane fails with the reason: %+v", r)
	}
}

func TestValueForDurationNeedsProvenHold(t *testing.T) {
	p := Predicate{ID: "d", Kind: PredicateValueForDuration, MinValue: 50_000, ValueCurrency: "USD", MinHeldSeconds: 3600}
	held := usdHolding("eip155:1", "0xa", "ETH", 1, 500, nowMs-3600*1000)
	if r := EvaluatePredicate(p, SubjectFacts{Holdings: []Holding{held}}, nowMs); !r.Passed {
		t.Fatalf("held exactly MIN_HELD_SECONDS passes: %+v", r)
	}
	short := usdHolding("eip155:1", "0xa", "ETH", 1, 500, nowMs-3599*1000)
	if r := EvaluatePredicate(p, SubjectFacts{Holdings: []Holding{short}}, nowMs); r.Passed {
		t.Fatalf("one second short fails: %+v", r)
	}
	unknown := usdHolding("eip155:1", "0xa", "ETH", 1, 500, 0)
	r := EvaluatePredicate(p, SubjectFacts{Holdings: []Holding{unknown}}, nowMs)
	if r.Passed || !strings.Contains(r.EvidenceText, "held-since") {
		t.Fatalf("an unproven hold never passes and says why: %+v", r)
	}
}

func TestAllowedTokensAndTrustedConnections(t *testing.T) {
	tokens := Predicate{ID: "t", Kind: PredicateAllowedTokens, Assets: []Asset{{ChainID: "eip155:1", TokenSymbol: "USDC"}}}
	facts := SubjectFacts{Holdings: []Holding{usdHolding("eip155:1", "0xa", "ETH", 1, 500, 0)}}
	if r := EvaluatePredicate(tokens, facts, nowMs); r.Passed {
		t.Fatalf("an unlisted asset contributes nothing: %+v", r)
	}
	facts.Holdings = append(facts.Holdings, usdHolding("eip155:1", "0xa", "USDC", 10, 10, 0))
	if r := EvaluatePredicate(tokens, facts, nowMs); !r.Passed || r.MeasuredValue != 1 {
		t.Fatalf("a listed asset held passes: %+v", r)
	}
	conn := Predicate{ID: "c", Kind: PredicateTrustedConnections, RequiredCount: 2, TrusterIDs: []string{"a", "b", "c"}, MinEdgeWeight: 0.5}
	one := SubjectFacts{Trusters: map[string]float64{"a": 0.9, "b": 0.4, "z": 1}}
	if r := EvaluatePredicate(conn, one, nowMs); r.Passed || r.MeasuredValue != 1 {
		t.Fatalf("REQUIRED_COUNT-1 fails, low weight and unnamed trusters do not count: %+v", r)
	}
	two := SubjectFacts{Trusters: map[string]float64{"a": 0.9, "b": 0.5, "z": 1}}
	if r := EvaluatePredicate(conn, two, nowMs); !r.Passed || len(r.TrusterIDsMatched) != 2 {
		t.Fatalf("exactly REQUIRED_COUNT passes: %+v", r)
	}
}

func nestedPolicy() Policy {
	return Policy{
		ID: "p1", Name: "Nested", Active: true, EvaluationIntervalMs: 10000, EventSources: []string{"trust-edge"},
		Root: Group{ID: "root", Combinator: CombinatorAll, Predicates: []Predicate{{ID: "v", Kind: PredicateMinValueLocked, MinValue: 100_000, ValueCurrency: "USD"}},
			Groups: []Group{{ID: "any", Combinator: CombinatorAny, Predicates: []Predicate{
				{ID: "t", Kind: PredicateAllowedTokens, Assets: []Asset{{ChainID: "eip155:1", TokenSymbol: "USDC", Decimals: 6}}},
				{ID: "c", Kind: PredicateTrustedConnections, RequiredCount: 1, MinEdgeWeight: 0.5},
			}}}},
	}
}

func TestNestedGroupsTruthTable(t *testing.T) {
	p := nestedPolicy()
	value := usdHolding("eip155:1", "0xa", "ETH", 1, 2000, 0)
	usdc := usdHolding("eip155:1", "0xa", "USDC", 5, 5, 0)
	cases := []struct {
		name   string
		facts  SubjectFacts
		passed bool
	}{
		{"value + token", SubjectFacts{Holdings: []Holding{value, usdc}}, true},
		{"value + connection", SubjectFacts{Holdings: []Holding{value}, Trusters: map[string]float64{"a": 0.9}}, true},
		{"value only", SubjectFacts{Holdings: []Holding{value}}, false},
		{"token + connection, no value", SubjectFacts{Holdings: []Holding{usdc}, Trusters: map[string]float64{"a": 0.9}}, false},
	}
	for _, c := range cases {
		out := EvaluatePolicy(p, c.facts, nowMs)
		if out.Passed != c.passed {
			t.Fatalf("%s: passed=%v want %v (%+v)", c.name, out.Passed, c.passed, out.Results)
		}
		if len(out.Results) != 3 {
			t.Fatalf("%s: every leaf predicate reports: %d", c.name, len(out.Results))
		}
	}
}

func TestPolicyRoundTripAndDualSignature(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	p := nestedPolicy()
	p.CreatedAtMs, p.UpdatedAtMs, p.EvaluatorPeerID = nowMs, nowMs, "peer-self"
	fb, doc, err := SignPolicy(&p, key)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodePolicy(fb)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != p.ID || back.Root.Groups[0].Predicates[1].RequiredCount != 1 || back.Root.Groups[0].Predicates[0].Assets[0].Decimals != 6 || back.EventSources[0] != "trust-edge" || back.EvaluationIntervalMs != 10000 || !back.Active {
		t.Fatalf("round trip lost fields: %+v", back)
	}
	if err := VerifyPolicyBytes(fb, pub); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPolicyJSON(doc, pub); err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), fb...)
	tampered[len(tampered)/2] ^= 0x01
	if err := VerifyPolicyBytes(tampered, pub); err == nil {
		t.Fatal("a tampered FlatBuffer must not verify")
	}
	badDoc := []byte(strings.Replace(string(doc), `"NAME":"Nested"`, `"NAME":"Nested!"`, 1))
	if err := VerifyPolicyJSON(badDoc, pub); err == nil {
		t.Fatal("a tampered JSON document must not verify")
	}
	if err := (Policy{ID: "x", Root: Group{ID: "r", Combinator: CombinatorAll}}).Validate(); err == nil {
		t.Fatal("an empty rule set is refused")
	}
}

func TestVerdictRoundTripAndDualSignature(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	out := EvaluatePolicy(nestedPolicy(), SubjectFacts{Holdings: []Holding{usdHolding("eip155:1", "0xa", "ETH", 1, 2000, nowMs-5000)}, Trusters: map[string]float64{"a": 0.9}}, nowMs)
	v := Verdict{ID: "p1:s:1", PolicyID: "p1", SubjectID: "s", Passed: out.Passed, Score: out.Score, Results: out.Results, Trigger: "interval", EvaluatedAtMs: nowMs, EvaluatorPeerID: "peer-self"}
	fb, doc, err := SignVerdict(&v, key)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeVerdict(fb)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Passed || len(back.Results) != 3 || back.Results[0].BondEvidence[0].BlockReference != "block-1" || back.Results[2].TrusterIDsMatched[0] != "a" || back.Results[0].Kind != PredicateMinValueLocked {
		t.Fatalf("round trip lost fields: %+v", back)
	}
	if err := VerifyVerdictBytes(fb, pub); err != nil {
		t.Fatal(err)
	}
	if err := VerifyVerdictJSON(doc, pub); err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), fb...)
	tampered[len(tampered)/3] ^= 0x01
	if err := VerifyVerdictBytes(tampered, pub); err == nil {
		t.Fatal("a tampered verdict must not verify")
	}
	if !strings.Contains(string(doc), `"EVALUATOR_SIGNATURE":"`) {
		t.Fatal("the JSON form carries its own signature")
	}
}

/* ── engine ──────────────────────────────────────────────────────────── */

type fakeVerdicts struct{ stored []Verdict }

func newTestEngine(t *testing.T, policies []Policy, balances BalanceSource, published *[]string) (*Engine, *fakeVerdicts, *Service) {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	g := NewGraph()
	_ = g.AddNode("self")
	_ = g.AddNode("s1")
	_ = g.SetEdge(Edge{Truster: "self", Trustee: "s1", Weight: 0.9})
	svc := NewService(g, nil)
	verdicts := &fakeVerdicts{}
	eng, err := NewEngine(EngineConfig{
		Service: svc, Balances: balances,
		Resolve: func(peerID string) ([]ChainAddress, error) {
			return []ChainAddress{{ChainID: "eip155:1", Address: "0x" + peerID}}, nil
		},
		Publish: func(topic string, data []byte) error {
			*published = append(*published, topic)
			return nil
		},
		Key: key, PeerID: "self", NowMs: func() int64 { return time.Now().UnixMilli() },
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.policiesList = func() ([]Policy, error) { return policies, nil }
	eng.verdictPut = func(v Verdict, fb []byte) error { verdicts.stored = append(verdicts.stored, v); return nil }
	return eng, verdicts, svc
}

func TestEngineEvaluatesOnIntervalAndCoalescesTriggers(t *testing.T) {
	var published []string
	balances := BalanceSourceFunc(func(ctx context.Context, addrs []ChainAddress) ([]Holding, error) {
		return []Holding{usdHolding("eip155:1", addrs[0].Address, "ETH", 1, 2000, 0)}, nil
	})
	p := Policy{ID: "p", Active: true, EvaluationIntervalMs: 250, Root: Group{ID: "r", Combinator: CombinatorAll, Predicates: []Predicate{{ID: "v", Kind: PredicateMinValueLocked, MinValue: 100_000, ValueCurrency: "USD"}}}}
	eng, verdicts, _ := newTestEngine(t, []Policy{p}, balances, &published)
	ctx, cancel := context.WithTimeout(context.Background(), 1100*time.Millisecond)
	defer cancel()
	eng.Run(ctx)
	if eng.Runs() < 2 {
		t.Fatalf("a 250 ms policy must evaluate several times in ~1 s: runs=%d", eng.Runs())
	}
	// Only the first evaluation is a flip (no previous verdict) → one stored verdict, published to the subject's and the evaluator's topics.
	if len(verdicts.stored) != 1 || verdicts.stored[0].SubjectID != "s1" || !verdicts.stored[0].Passed {
		t.Fatalf("one stored verdict for the first pass: %+v", verdicts.stored)
	}
	if len(published) != 2 || published[0] != TrustTopic("s1") || published[1] != TrustTopic("self") {
		t.Fatalf("the flip fans out as $TRV bytes on both topics: %v", published)
	}
	latest := eng.Latest("p", "")
	if len(latest) != 1 || latest[0].Trigger != "interval" {
		t.Fatalf("latest verdict kept: %+v", latest)
	}
	// Twenty triggers while nothing runs coalesce to exactly one pending run.
	before := eng.Runs()
	for i := 0; i < 20; i++ {
		eng.Trigger("trust-edge")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel2()
	eng.SetIntervalOverride(60_000) // long interval: only the trigger fires
	eng.Run(ctx2)
	if got := eng.Runs() - before; got != 1 && got != 2 {
		t.Fatalf("triggers coalesce: extra runs=%d", got)
	}
}

func TestEngineIntervalOverrideTakesEffectWithoutRestart(t *testing.T) {
	var published []string
	p := Policy{ID: "p", Active: true, EvaluationIntervalMs: 60_000, Root: Group{ID: "r", Combinator: CombinatorAll, Predicates: []Predicate{{ID: "c", Kind: PredicateTrustedConnections, RequiredCount: 1}}}}
	eng, _, _ := newTestEngine(t, []Policy{p}, nil, &published)
	eng.SetIntervalOverride(250)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	eng.Run(ctx)
	if eng.Runs() < 2 {
		t.Fatalf("the runtime override drives the cadence: runs=%d", eng.Runs())
	}
	if eng.IntervalOverride() != 250 {
		t.Fatalf("override reported: %d", eng.IntervalOverride())
	}
}

func TestEngineNeverLeaksRPCSecretsIntoVerdicts(t *testing.T) {
	var published []string
	balances := BalanceSourceFunc(func(ctx context.Context, addrs []ChainAddress) ([]Holding, error) {
		return nil, context.DeadlineExceeded
	})
	p := Policy{ID: "p", Active: true, Root: Group{ID: "r", Combinator: CombinatorAll, Predicates: []Predicate{{ID: "v", Kind: PredicateMinValueLocked, MinValue: 1, ValueCurrency: "USD"}}}}
	eng, verdicts, _ := newTestEngine(t, []Policy{p}, balances, &published)
	eng.RunOnce(context.Background(), "test")
	if len(verdicts.stored) != 1 || verdicts.stored[0].Passed {
		t.Fatalf("a refused lane yields a failing verdict, never a pass: %+v", verdicts.stored)
	}
	if text := verdicts.stored[0].Results[0].EvidenceText; strings.Contains(text, "secret") || !strings.Contains(text, "did not answer") {
		t.Fatalf("evidence states the refusal without secrets: %q", text)
	}
}

func TestEngineEvaluatesDirectorySubjectsWithoutTrustEdges(t *testing.T) {
	var published []string
	p := Policy{ID: "p", Active: true, EvaluationIntervalMs: 60_000, Root: Group{ID: "r", Combinator: CombinatorAll, Predicates: []Predicate{{ID: "c", Kind: PredicateTrustedConnections, RequiredCount: 1}}}}
	eng, _, _ := newTestEngine(t, []Policy{p}, nil, &published)
	eng.extraSubjects = func() []string { return []string{"peer-known-only", eng.peerID, ""} }
	eng.RunOnce(context.Background(), "test")
	got := eng.Latest("p", "peer-known-only")
	if len(got) != 1 || got[0].Passed {
		t.Fatalf("a directory-known peer with no trust edge must get a FAIL verdict: %+v", got)
	}
	if got[0].Results[0].EvidenceText == "" {
		t.Fatal("the failing verdict must say why")
	}
	if len(eng.Latest("p", eng.peerID)) != 0 {
		t.Fatal("the evaluator never evaluates itself")
	}
}
