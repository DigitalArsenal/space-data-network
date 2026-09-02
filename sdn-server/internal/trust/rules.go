package trust

// Rule evaluation — one `$TRP` policy against one subject's facts.
//
// The facts are what the node can currently prove: the subject's chain
// holdings as the bond-attestation module reports them (value in the policy's
// currency, held-since when the chain-history lane provides it) and the
// subject's trusters from the `$TRE` graph. A predicate that needs a fact the
// node does not have FAILS with the reason in EVIDENCE_TEXT — never passes on
// an assumption.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Holding is one balance the bond lane observed for a subject address.
type Holding struct {
	ChainID       string
	Address       string
	TokenAddress  string
	Symbol        string
	Amount        float64 // native units of the asset
	Decimals      uint32
	USD           float64 // normalized value when the lane priced it
	HeldSinceMs   int64   // 0 = the chain-history lane did not answer
	ObservedAtMs  int64
	BlockRef      string
	SourceQuery   string
	PricedInUSD   bool
	NormalizedRef string
}

// SubjectFacts are the inputs one evaluation reads.
type SubjectFacts struct {
	Holdings []Holding
	// Trusters maps truster id → edge weight for every live edge into the subject.
	Trusters map[string]float64
	// HoldingsErr is a lane refusal; predicates that need holdings fail with it.
	HoldingsErr string
}

// BondEvidence mirrors `$TRV.TRVBondEvidence`.
type BondEvidence struct {
	ChainID         string  `json:"CHAIN_ID"`
	Address         string  `json:"ADDRESS"`
	TokenAddress    string  `json:"TOKEN_ADDRESS,omitempty"`
	Balance         uint64  `json:"BALANCE"`
	Decimals        uint32  `json:"DECIMALS"`
	ValueCurrency   string  `json:"VALUE_CURRENCY"`
	NormalizedValue float64 `json:"NORMALIZED_VALUE"`
	HeldSinceMs     int64   `json:"HELD_SINCE"`
	ObservedAtMs    int64   `json:"OBSERVED_AT"`
	BlockReference  string  `json:"BLOCK_REFERENCE,omitempty"`
	SourceQuery     string  `json:"SOURCE_QUERY,omitempty"`
}

// PredicateResult mirrors `$TRV.TRVPredicateResult`.
type PredicateResult struct {
	PredicateID       string         `json:"PREDICATE_ID"`
	Kind              PredicateKind  `json:"KIND"`
	Passed            bool           `json:"PASSED"`
	MeasuredValue     float64        `json:"MEASURED_VALUE"`
	RequiredValue     float64        `json:"REQUIRED_VALUE"`
	BondEvidence      []BondEvidence `json:"BOND_EVIDENCE,omitempty"`
	TrusterIDsMatched []string       `json:"TRUSTER_IDS_MATCHED,omitempty"`
	EvidenceText      string         `json:"EVIDENCE_TEXT"`
}

// Outcome is what evaluating a policy yields before it becomes a verdict.
type Outcome struct {
	Passed  bool
	Score   float64 // fraction of leaf predicates that passed
	Results []PredicateResult
}

// currencyIsFiat is true for the value-normalized lane (USD cents).
func currencyIsFiat(c string) bool {
	c = strings.ToUpper(strings.TrimSpace(c))
	return c == "" || c == "USD"
}

// assetMatches says whether a holding is one of the predicate's ASSETS
// (an empty ASSETS list matches every holding).
func assetMatches(h Holding, assets []Asset) bool {
	if len(assets) == 0 {
		return true
	}
	for _, a := range assets {
		if !strings.EqualFold(strings.TrimSpace(a.ChainID), h.ChainID) {
			continue
		}
		if a.TokenAddress != "" && !strings.EqualFold(a.TokenAddress, h.TokenAddress) {
			continue
		}
		if a.TokenSymbol != "" && !strings.EqualFold(a.TokenSymbol, h.Symbol) {
			continue
		}
		return true
	}
	return false
}

// valueOf is a holding's value in the predicate's currency, in the smallest
// unit the predicate's MIN_VALUE is stated in: cents for fiat, 10^DECIMALS
// for a token. A holding the lane could not price contributes nothing and
// says so.
func valueOf(h Holding, p Predicate) (float64, bool) {
	if currencyIsFiat(p.ValueCurrency) {
		if !h.PricedInUSD {
			return 0, false
		}
		return h.USD * 100, true
	}
	if !strings.EqualFold(p.ValueCurrency, h.Symbol) {
		return 0, false
	}
	decimals := h.Decimals
	for _, a := range p.Assets {
		if strings.EqualFold(a.TokenSymbol, h.Symbol) && a.Decimals > 0 {
			decimals = a.Decimals
		}
	}
	return h.Amount * math.Pow10(int(decimals)), true
}

func evidence(h Holding, p Predicate, value float64) BondEvidence {
	return BondEvidence{
		ChainID: h.ChainID, Address: h.Address, TokenAddress: h.TokenAddress,
		Balance: uint64(math.Max(0, math.Round(h.Amount*math.Pow10(int(h.Decimals))))), Decimals: h.Decimals,
		ValueCurrency: strings.ToUpper(firstNonEmpty(p.ValueCurrency, "USD")), NormalizedValue: value,
		HeldSinceMs: h.HeldSinceMs, ObservedAtMs: h.ObservedAtMs, BlockReference: h.BlockRef, SourceQuery: h.SourceQuery,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// EvaluatePredicate applies one predicate to the facts.
func EvaluatePredicate(p Predicate, f SubjectFacts, nowMs int64) PredicateResult {
	r := PredicateResult{PredicateID: p.ID, Kind: p.Kind}
	switch p.Kind {
	case PredicateMinValueLocked, PredicateValueForDuration:
		r.RequiredValue = float64(p.MinValue)
		if f.HoldingsErr != "" {
			r.EvidenceText = "The bond lane did not answer: " + f.HoldingsErr
			return r
		}
		total := 0.0
		unpriced := 0
		unknownHeld := 0
		for _, h := range f.Holdings {
			if !assetMatches(h, p.Assets) || h.Amount <= 0 {
				continue
			}
			v, ok := valueOf(h, p)
			if !ok {
				unpriced++
				continue
			}
			if p.Kind == PredicateValueForDuration {
				if h.HeldSinceMs <= 0 {
					unknownHeld++
					r.BondEvidence = append(r.BondEvidence, evidence(h, p, v))
					continue
				}
				if nowMs-h.HeldSinceMs < int64(p.MinHeldSeconds)*1000 {
					r.BondEvidence = append(r.BondEvidence, evidence(h, p, v))
					continue
				}
			}
			total += v
			r.BondEvidence = append(r.BondEvidence, evidence(h, p, v))
		}
		r.MeasuredValue = total
		r.Passed = total >= float64(p.MinValue)
		switch {
		case p.Kind == PredicateValueForDuration && unknownHeld > 0 && !r.Passed:
			r.EvidenceText = fmt.Sprintf("%d holding(s) carry no held-since from the chain-history lane; only balances with a proven hold count.", unknownHeld)
		case r.Passed:
			r.EvidenceText = fmt.Sprintf("Value locked %s meets the required %s.", fmtUnits(total, p), fmtUnits(float64(p.MinValue), p))
		case unpriced > 0 && total == 0:
			r.EvidenceText = fmt.Sprintf("%d holding(s) could not be valued in %s.", unpriced, strings.ToUpper(firstNonEmpty(p.ValueCurrency, "USD")))
		default:
			r.EvidenceText = fmt.Sprintf("Value locked %s is below the required %s.", fmtUnits(total, p), fmtUnits(float64(p.MinValue), p))
		}
	case PredicateAllowedTokens:
		r.RequiredValue = 1
		if f.HoldingsErr != "" {
			r.EvidenceText = "The bond lane did not answer: " + f.HoldingsErr
			return r
		}
		held := 0
		for _, h := range f.Holdings {
			if h.Amount > 0 && assetMatches(h, p.Assets) {
				held++
				r.BondEvidence = append(r.BondEvidence, evidence(h, p, h.USD*100))
			}
		}
		r.MeasuredValue = float64(held)
		r.Passed = held > 0
		if r.Passed {
			r.EvidenceText = fmt.Sprintf("%d listed asset(s) held with a positive balance.", held)
		} else {
			r.EvidenceText = "None of the listed assets is held with a positive balance."
		}
	case PredicateTrustedConnections:
		r.RequiredValue = float64(p.RequiredCount)
		allowed := map[string]bool{}
		for _, id := range p.TrusterIDs {
			allowed[strings.TrimSpace(id)] = true
		}
		for truster, w := range f.Trusters {
			if len(allowed) > 0 && !allowed[truster] {
				continue
			}
			if w < p.MinEdgeWeight {
				continue
			}
			r.TrusterIDsMatched = append(r.TrusterIDsMatched, truster)
		}
		sort.Strings(r.TrusterIDsMatched)
		r.MeasuredValue = float64(len(r.TrusterIDsMatched))
		r.Passed = len(r.TrusterIDsMatched) >= int(p.RequiredCount)
		pool := "any truster"
		if len(allowed) > 0 {
			pool = fmt.Sprintf("the %d named trusters", len(allowed))
		}
		r.EvidenceText = fmt.Sprintf("%d of %s trust this subject with weight ≥ %.2f; %d required.", len(r.TrusterIDsMatched), pool, p.MinEdgeWeight, p.RequiredCount)
	default:
		r.EvidenceText = "Unknown predicate kind."
	}
	return r
}

func fmtUnits(v float64, p Predicate) string {
	if currencyIsFiat(p.ValueCurrency) {
		return fmt.Sprintf("%.2f USD", v/100)
	}
	return fmt.Sprintf("%.0f %s units", v, strings.ToUpper(p.ValueCurrency))
}

func evaluateGroup(g Group, f SubjectFacts, nowMs int64, results *[]PredicateResult) (bool, int, int) {
	passed := 0
	total := 0
	var verdicts []bool
	for _, p := range g.Predicates {
		r := EvaluatePredicate(p, f, nowMs)
		*results = append(*results, r)
		verdicts = append(verdicts, r.Passed)
		total++
		if r.Passed {
			passed++
		}
	}
	for _, sub := range g.Groups {
		ok, p, t := evaluateGroup(sub, f, nowMs, results)
		verdicts = append(verdicts, ok)
		passed += p
		total += t
	}
	if len(verdicts) == 0 {
		return false, passed, total
	}
	if g.Combinator == CombinatorAny {
		for _, v := range verdicts {
			if v {
				return true, passed, total
			}
		}
		return false, passed, total
	}
	for _, v := range verdicts {
		if !v {
			return false, passed, total
		}
	}
	return true, passed, total
}

// EvaluatePolicy applies the whole rule tree.
func EvaluatePolicy(p Policy, f SubjectFacts, nowMs int64) Outcome {
	var results []PredicateResult
	ok, passed, total := evaluateGroup(p.Root, f, nowMs, &results)
	score := 0.0
	if total > 0 {
		score = float64(passed) / float64(total)
	}
	return Outcome{Passed: ok, Score: score, Results: results}
}
