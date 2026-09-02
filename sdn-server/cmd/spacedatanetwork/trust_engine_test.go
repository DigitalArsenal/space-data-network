package main

import (
	"path/filepath"
	"testing"
)

func TestParseBondHoldingsMapsSymbolsToAttestedAddresses(t *testing.T) {
	out := []byte(`{"attested":true,"bond_usd":12.5,"holdings":[{"symbol":"BTC","amount":0.001,"usd":12.5},{"symbol":"SOL","amount":2},{"symbol":"XYZ","amount":9,"usd":1}]}`)
	byChain := map[string]string{"bitcoin": "bc1qexample", "solana": "So1anaExample"}
	holdings, err := parseBondHoldings(out, byChain, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(holdings) != 2 {
		t.Fatalf("unsupported symbols are dropped: got %d holdings", len(holdings))
	}
	btc := holdings[0]
	if btc.ChainID != "bitcoin" || btc.Address != "bc1qexample" || btc.Decimals != 8 || !btc.PricedInUSD || btc.USD != 12.5 || btc.ObservedAtMs != 1_000 {
		t.Fatalf("btc holding: %+v", btc)
	}
	sol := holdings[1]
	if sol.ChainID != "solana" || sol.Address != "So1anaExample" || sol.Decimals != 9 || sol.PricedInUSD {
		t.Fatalf("an unpriced holding stays unpriced: %+v", sol)
	}
	if _, err := parseBondHoldings([]byte(`{"holdings":[]}`), byChain, 0); err == nil {
		t.Fatal("an answer without the attested verdict is refused")
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
