package main

import (
	"path/filepath"
	"testing"
)

// The engine credits each attested address's native coin from the module's
// $WHA: BTC priced, SOL (a zero balance) priced at zero, the USDC token and
// the ETH address the subject never attested left out.
func TestParseBondHoldingsMapsSymbolsToAttestedAddresses(t *testing.T) {
	out := bondFixtureAttestation(t, "complete", bondFixtureAddresses)
	byChain := map[string]string{"bitcoin": "bc1test", "solana": "SolTest"}
	holdings, err := parseBondHoldings(out, byChain, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(holdings) != 2 {
		t.Fatalf("only attested native holdings count: got %+v", holdings)
	}
	btc := holdings[0]
	if btc.ChainID != "bitcoin" || btc.Address != "bc1test" || btc.Symbol != "BTC" || btc.Amount != 1 || btc.Decimals != 8 || !btc.PricedInUSD || btc.USD != 60000 || btc.ObservedAtMs != 1_000 {
		t.Fatalf("btc holding: %+v", btc)
	}
	sol := holdings[1]
	if sol.ChainID != "solana" || sol.Address != "SolTest" || sol.Decimals != 9 || sol.Amount != 0 {
		t.Fatalf("sol holding: %+v", sol)
	}
	unpriced, err := parseBondHoldings(bondFixtureAttestation(t, "price-error", bondAddresses{Btc: "bc1test"}), byChain, 0)
	if err != nil || len(unpriced) != 1 || unpriced[0].PricedInUSD {
		t.Fatalf("an unpriced holding stays unpriced: %+v %v", unpriced, err)
	}
	if _, err := parseBondHoldings([]byte(`{"attested":true,"holdings":[]}`), byChain, 0); err == nil {
		t.Fatal("an answer that is not a $WHA is refused")
	}
}

func TestTrustSettingsRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "storage")
	if loadTrustSettings(dir).EvaluationIntervalMs != 0 {
		t.Fatal("absent settings read as zero")
	}
	if err := saveTrustSettings(dir, 2500); err == nil {
		t.Fatal("saving into a missing directory fails loudly")
	}
	if err := saveTrustSettings(t.TempDir(), 2500); err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	if err := saveTrustSettings(dir, 2500); err != nil {
		t.Fatal(err)
	}
	if got := loadTrustSettings(dir).EvaluationIntervalMs; got != 2500 {
		t.Fatalf("persisted interval: %d", got)
	}
}

// An ERC-20 named "ETH" is a token, not ether, and a wallet the subject did
// not attest is not the subject's.
func TestParseBondHoldingsRejectsTickerImpersonation(t *testing.T) {
	out := bondFixtureAttestation(t, "ticker", bondAddresses{Eth: "0xTest"})
	holdings, err := parseBondHoldings(out, map[string]string{"ethereum": "0xTest"}, 1000)
	if err != nil || len(holdings) != 1 || holdings[0].Amount != 2 || holdings[0].USD != 6000 {
		t.Fatalf("ticker impersonation accepted: %+v %v", holdings, err)
	}
	if holdings, err = parseBondHoldings(out, map[string]string{"ethereum": "another-wallet"}, 1000); err != nil || len(holdings) != 0 {
		t.Fatalf("another wallet credited: %+v %v", holdings, err)
	}
}
