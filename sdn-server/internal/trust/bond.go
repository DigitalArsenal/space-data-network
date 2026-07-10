package trust

// WS11.2/C3 — crypto security-bond interrogation.
//
// A "bond" is a cryptocurrency balance a key has attested it controls at a
// chain address, offered as a stake-based signal alongside the web-of-trust
// graph: "this identity has capital at risk under this key." The security
// model is intentionally narrow:
//
//   - WEIGHT, NEVER GATE: a bond can only nudge a score upward. There is no
//     code path where a bond lowers a score, and BondSource errors are never
//     surfaced as evaluation failures.
//   - FAIL-SAFE BY CONSTRUCTION: an unreachable RPC, an unknown address, a
//     rate-limited indexer, or simply "no BondSource configured" are all
//     indistinguishable from "no bond" — every case degrades to a zero
//     bonus, never an error that blocks or lowers trust.
//   - BOUNDED: the bonus a bond can contribute is capped
//     (ScoringConfig.BondBonusCap) and saturates with balance size
//     (ScoringConfig.BondSaturation), so no balance, however large, can
//     dominate the score.
//   - NOT A SUBSTITUTE FOR TRUSTERS: a bond only weights a subject that
//     already has at least one truster in the web-of-trust graph. A key
//     with zero trusters gets a zero bond bonus regardless of balance — see
//     bondBonus below. Capital cannot buy its way past the PGP-style
//     validity rule in internal/peers; it can only make an already-vouched
//     key edge higher among vouched peers.
//
// BondSource is the pluggable seam real chain clients hang off. This file
// ships MemoryBondSource, a map-backed mock for tests and static
// configuration; production wiring plugs in Bitcoin (UTXO sum), Ethereum
// (account balance), Solana (account balance), or other chain clients
// behind the same interface.

// Chain identifies the blockchain a BondSource queries.
type Chain string

const (
	ChainBitcoin  Chain = "bitcoin"
	ChainEthereum Chain = "ethereum"
	ChainSolana   Chain = "solana"
)

// BondSource queries a single chain for the USD-normalized balance backing
// a key's posted bond. Implementations wrap real chain clients (a Bitcoin
// UTXO scan, an Ethereum account balance lookup, a Solana account balance
// lookup, ...). keyID is whatever identifier the deployment uses to map a
// peer key to a chain address (the source owns that mapping).
//
// Bond must treat "no bond" and "can't tell" identically: a key with no
// known address/balance returns (0, nil); an unreachable/broken source
// returns a non-nil error. Callers (BondBalance) collapse both to zero —
// see the package doc comment above for why.
type BondSource interface {
	// Chain identifies which chain this source queries.
	Chain() Chain
	// Bond returns the USD-normalized balance backing keyID's bond on this
	// chain.
	Bond(keyID string) (amount float64, err error)
}

// MemoryBondSource is a map-backed BondSource for tests and static
// configuration. Errs, when set for a key, is returned instead of the
// amount (simulating an unreachable chain / lookup failure) so tests can
// exercise the fail-safe path without a network call.
type MemoryBondSource struct {
	ChainID Chain
	Amounts map[string]float64
	Errs    map[string]error
}

// Chain implements BondSource.
func (m MemoryBondSource) Chain() Chain { return m.ChainID }

// Bond implements BondSource.
func (m MemoryBondSource) Bond(keyID string) (float64, error) {
	if err := m.Errs[keyID]; err != nil {
		return 0, err
	}
	return m.Amounts[keyID], nil
}

// bondWeight resolves the chain discount for weighting bond amounts,
// mirroring ScoringConfig.WeightedFunds' type discount. Unlisted chains (or
// a nil map) default to full weight (1.0) — per-chain weighting is
// optional.
func (c ScoringConfig) bondWeight(chain Chain) float64 {
	if w, ok := c.BondChainWeights[chain]; ok {
		return w
	}
	return 1.0
}

// BondBalance sums the chain-weighted, USD-normalized bond balance for
// keyID across every configured source on ev.Bonds. It is FAIL-SAFE: a
// source that errors, or has no amount for keyID, contributes zero and its
// error is discarded rather than propagated — an unreachable or unknown
// chain must never surface as a fault. A nil/empty ev.Bonds returns 0.
func (ev *Evaluator) BondBalance(keyID string) float64 {
	total := 0.0
	for _, src := range ev.Bonds {
		if src == nil {
			continue
		}
		amt, err := src.Bond(keyID)
		if err != nil || amt <= 0 {
			continue
		}
		total += amt * ev.Config.bondWeight(src.Chain())
	}
	return total
}

// bondBonus converts a chain-weighted bond balance into the bounded,
// saturating score addend DefaultScoreFunc layers on top of the
// web-of-trust score. It never returns a negative value.
//
// It is gated on trusterCount > 0: a bond weights a relationship that
// already exists in the web-of-trust graph, it never creates one. A
// subject with zero trusters always gets a zero bond bonus, however large
// its balance — that is what keeps a bond from being usable as a
// substitute for being vouched for.
func bondBonus(weightedBalance float64, trusterCount int, cfg ScoringConfig) float64 {
	if trusterCount <= 0 || weightedBalance <= 0 || cfg.BondBonusCap <= 0 {
		return 0
	}
	return cfg.BondBonusCap * saturate(weightedBalance, cfg.BondSaturation)
}
