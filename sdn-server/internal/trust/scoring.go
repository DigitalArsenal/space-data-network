package trust

// WS11.2 — trust scoring inputs and the pluggable scoring function.
//
// A subject's score is computed FROM AN EVALUATOR'S PERSPECTIVE over:
//   - funds-at-location: the subject's own holdings, weighted by fund TYPE
//     (stablecoin vs BTC/ETH/other — configurable weights);
//   - how many nodes trust the subject: the TOTAL truster count, and the
//     count AMONG the evaluator's own web of trust (transitive trustees);
//   - how much value stands behind those trusters: total type-weighted
//     truster funds, and the amount among the evaluator's web of trust.
//
// The combination is a pluggable ScoreFunc; DefaultScoreFunc is a bounded,
// monotone combination with saturating normalization. WS11.3 layers
// incremental recompute + threshold status flips on top.

import (
	"math"
	"sort"
)

// FundType classifies a holding for weighting purposes.
type FundType string

const (
	FundStablecoin FundType = "stablecoin"
	FundBTC        FundType = "btc"
	FundETH        FundType = "eth"
	FundOther      FundType = "other"
)

// FundHolding is one position a node provably holds at a location
// (chain address, custodian, venue). Amount is USD-normalized.
type FundHolding struct {
	Type     FundType
	Location string
	Amount   float64
}

// FundsProvider supplies a node's holdings. Implementations may back this
// with on-chain attestations; MemoryFundsProvider backs tests and caches.
type FundsProvider interface {
	Funds(nodeID string) []FundHolding
}

// MemoryFundsProvider is a map-backed FundsProvider.
type MemoryFundsProvider map[string][]FundHolding

// Funds implements FundsProvider.
func (m MemoryFundsProvider) Funds(nodeID string) []FundHolding { return m[nodeID] }

// ScoringConfig parameterizes input weighting and the default combiner.
type ScoringConfig struct {
	// FundTypeWeights discounts holdings by type (stablecoin near 1,
	// volatile assets lower). Unlisted types fall back to FundOther's
	// weight, or 0 if that is unset.
	FundTypeWeights map[FundType]float64

	// Saturation scales: the input value at which each component reaches
	// ~63% of its cap (1 - 1/e). Must be > 0.
	FundsSaturation        float64 // subject's own weighted funds
	TrusterCountSaturation float64 // truster counts
	TrusterFundsSaturation float64 // truster weighted funds

	// Component weights for DefaultScoreFunc; normalized internally.
	WeightOwnFunds            float64
	WeightTrusterCount        float64
	WeightTrusterCountTrusted float64
	WeightTrusterFunds        float64
	WeightTrusterFundsTrusted float64
	WeightDirectEdge          float64

	// TrustThreshold is the score at or above which a subject is
	// considered trusted (status flips in WS11.3 use this).
	TrustThreshold float64
}

// DefaultScoringConfig returns the standard weighting: stablecoins count
// full, BTC/ETH discounted, unknown assets heavily discounted; endorsements
// from within the evaluator's own web of trust count more than strangers'.
func DefaultScoringConfig() ScoringConfig {
	return ScoringConfig{
		FundTypeWeights: map[FundType]float64{
			FundStablecoin: 1.0,
			FundBTC:        0.9,
			FundETH:        0.85,
			FundOther:      0.5,
		},
		FundsSaturation:           100_000, // $100k weighted ≈ 63% of the funds component
		TrusterCountSaturation:    10,
		TrusterFundsSaturation:    1_000_000,
		WeightOwnFunds:            0.25,
		WeightTrusterCount:        0.10,
		WeightTrusterCountTrusted: 0.20,
		WeightTrusterFunds:        0.10,
		WeightTrusterFundsTrusted: 0.20,
		WeightDirectEdge:          0.15,
		TrustThreshold:            0.5,
	}
}

// WeightedFunds returns the type-weighted USD value of a holding set.
func (c ScoringConfig) WeightedFunds(holdings []FundHolding) float64 {
	total := 0.0
	for _, h := range holdings {
		if h.Amount <= 0 {
			continue
		}
		w, ok := c.FundTypeWeights[h.Type]
		if !ok {
			w = c.FundTypeWeights[FundOther]
		}
		total += h.Amount * w
	}
	return total
}

// ScoreInputs are the raw signals a ScoreFunc combines, all computed from
// the evaluator's perspective.
type ScoreInputs struct {
	Evaluator string
	Subject   string

	// OwnWeightedFunds: subject's own funds-at-location, type-weighted.
	OwnWeightedFunds float64

	// TrusterCountTotal: how many nodes trust the subject (any node).
	TrusterCountTotal int
	// TrusterCountAmongTrusted: how many of those are inside the
	// evaluator's web of trust (its transitive trustees).
	TrusterCountAmongTrusted int

	// TrusterFundsTotal: type-weighted funds of ALL the subject's trusters.
	TrusterFundsTotal float64
	// TrusterFundsAmongTrusted: type-weighted funds of the subject's
	// trusters that lie inside the evaluator's web of trust.
	TrusterFundsAmongTrusted float64

	// DirectEdgeWeight: the evaluator's own edge weight to the subject
	// (0 when the evaluator has no direct edge).
	DirectEdgeWeight float64
}

// ScoreFunc combines raw inputs into a score in [0,1]. Pluggable so
// deployments can bring their own model.
type ScoreFunc func(in ScoreInputs, cfg ScoringConfig) float64

// saturate maps [0,∞) to [0,1) with ~63% at scale.
func saturate(v, scale float64) float64 {
	if v <= 0 || scale <= 0 {
		return 0
	}
	return 1 - math.Exp(-v/scale)
}

// DefaultScoreFunc: normalized weighted sum of saturated components.
// Monotone in every input; always in [0,1].
func DefaultScoreFunc(in ScoreInputs, cfg ScoringConfig) float64 {
	wsum := cfg.WeightOwnFunds + cfg.WeightTrusterCount + cfg.WeightTrusterCountTrusted +
		cfg.WeightTrusterFunds + cfg.WeightTrusterFundsTrusted + cfg.WeightDirectEdge
	if wsum <= 0 {
		return 0
	}
	s := cfg.WeightOwnFunds*saturate(in.OwnWeightedFunds, cfg.FundsSaturation) +
		cfg.WeightTrusterCount*saturate(float64(in.TrusterCountTotal), cfg.TrusterCountSaturation) +
		cfg.WeightTrusterCountTrusted*saturate(float64(in.TrusterCountAmongTrusted), cfg.TrusterCountSaturation) +
		cfg.WeightTrusterFunds*saturate(in.TrusterFundsTotal, cfg.TrusterFundsSaturation) +
		cfg.WeightTrusterFundsTrusted*saturate(in.TrusterFundsAmongTrusted, cfg.TrusterFundsSaturation) +
		cfg.WeightDirectEdge*math.Max(0, math.Min(1, in.DirectEdgeWeight))
	return s / wsum
}

// Evaluator computes scores over a Graph + FundsProvider with a config and
// a pluggable ScoreFunc.
type Evaluator struct {
	Graph   *Graph
	Funds   FundsProvider
	Config  ScoringConfig
	ScoreFn ScoreFunc
}

// NewEvaluator wires the default config + score function.
func NewEvaluator(g *Graph, funds FundsProvider) *Evaluator {
	return &Evaluator{Graph: g, Funds: funds, Config: DefaultScoringConfig(), ScoreFn: DefaultScoreFunc}
}

// Inputs assembles the raw scoring signals for subject as seen by evaluator.
func (ev *Evaluator) Inputs(evaluator, subject string) ScoreInputs {
	in := ScoreInputs{Evaluator: evaluator, Subject: subject}
	in.OwnWeightedFunds = ev.Config.WeightedFunds(ev.Funds.Funds(subject))

	trusters := ev.Graph.Trusters(subject)
	in.TrusterCountTotal = len(trusters)

	// The evaluator's web of trust: itself + everyone it transitively trusts.
	web := map[string]struct{}{evaluator: {}}
	for _, id := range ev.Graph.TransitiveTrustees(evaluator, 0) {
		web[id] = struct{}{}
	}
	for _, t := range trusters {
		funds := ev.Config.WeightedFunds(ev.Funds.Funds(t))
		in.TrusterFundsTotal += funds
		if _, ok := web[t]; ok {
			in.TrusterCountAmongTrusted++
			in.TrusterFundsAmongTrusted += funds
		}
	}
	if e, ok := ev.Graph.Edge(evaluator, subject); ok {
		in.DirectEdgeWeight = e.Weight
	}
	return in
}

// Score computes the subject's score in [0,1] from the evaluator's view.
func (ev *Evaluator) Score(evaluator, subject string) float64 {
	fn := ev.ScoreFn
	if fn == nil {
		fn = DefaultScoreFunc
	}
	return fn(ev.Inputs(evaluator, subject), ev.Config)
}

// Trusted reports whether the subject clears the configured threshold.
func (ev *Evaluator) Trusted(evaluator, subject string) bool {
	return ev.Score(evaluator, subject) >= ev.Config.TrustThreshold
}

// ScoreAll scores every node in the graph from the evaluator's perspective,
// returned sorted by subject id (deterministic).
func (ev *Evaluator) ScoreAll(evaluator string) map[string]float64 {
	out := map[string]float64{}
	for _, id := range ev.Graph.Nodes() {
		if id == evaluator {
			continue
		}
		out[id] = ev.Score(evaluator, id)
	}
	return out
}

// RankedSubjects returns subjects sorted by descending score (ties by id).
func (ev *Evaluator) RankedSubjects(evaluator string) []string {
	scores := ev.ScoreAll(evaluator)
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}
