package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/typedfb/wha"
)

// Startup is pending, so the UI continues polling without displaying an error.
func TestBondHandlerBeforeFirstAttestation(t *testing.T) {
	b := &bondAttestor{}
	rec := httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/bond", nil))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Fatalf("expected pending before first attestation, got %d", rec.Code)
	}
}

// bondFixtureModule loads the embedded module (or SDN_BOND_TEST_WASM) in the
// production WasmEdge host with an `http` capability that answers each free
// service in its own format from fixed balances and quotes: BTC 1 at $60,000,
// ETH 2 at $3,000, 5 USDC at $1, SOL 0. `scenario` breaks one service.
func bondFixtureModule(t *testing.T, scenario string) (*modulert.Module, *[]string) {
	t.Helper()
	wasm := bondAttestationWasm
	if name := os.Getenv("SDN_BOND_TEST_WASM"); name != "" {
		var err error
		wasm, err = os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := []string{}
	reg := modulert.NewCapabilityRegistry()
	reg.Register("http", func(_ *modulert.Module) modulert.CapHandler {
		return func(op string, payload []byte) ([]byte, error) {
			if op != "http.request" {
				t.Fatalf("unexpected operation %s", op)
			}
			// The http capability's request, as the typed http.request
			// hostcall hands it over: a POST body arrives base64-encoded.
			var req struct {
				URL          string `json:"url"`
				Body         string `json:"body"`
				BodyEncoding string `json:"body_encoding"`
			}
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, err
			}
			if req.BodyEncoding == "base64" {
				body, err := base64.StdEncoding.DecodeString(req.Body)
				if err != nil {
					return nil, err
				}
				req.Body = string(body)
			}
			calls = append(calls, req.URL)
			status := 200
			var answer any
			switch {
			case strings.Contains(req.URL, "/address/"):
				funded := any(150000000)
				spent := 50000000
				if scenario == "zero" {
					funded = 0
					spent = 0
				}
				if scenario == "malformed" {
					funded = "oops"
				}
				answer = map[string]any{"chain_stats": map[string]any{"funded_txo_sum": funded, "spent_txo_sum": spent}}
				if scenario == "fallback" && strings.Contains(req.URL, "mempool") {
					status = 429
				}
			case strings.Contains(req.URL, "coinbase"), strings.Contains(req.URL, "coingecko"):
				price := "3000"
				if strings.Contains(req.URL, "BTC") {
					price = "60000"
				}
				answer = map[string]any{"data": map[string]any{"amount": price}}
				if scenario == "price-error" {
					status = 429
				}
			case strings.Contains(req.URL, "blockscout"):
				rate := any("1")
				if scenario == "unpriced" {
					rate = nil
				}
				symbol := "USDC"
				if scenario == "ticker" {
					symbol = "ETH" // an ERC-20 reusing the native ticker
				}
				answer = []any{map[string]any{"value": "5000000", "token": map[string]any{"type": "ERC-20", "symbol": symbol, "decimals": "6", "exchange_rate": rate, "address_hash": "0xUSDC"}}}
			case strings.Contains(req.URL, "dexscreener"):
				answer = []any{map[string]any{"chainId": "solana", "baseToken": map[string]any{"address": "mintA", "symbol": "SPL"}, "priceUsd": "2", "liquidity": map[string]any{"usd": 10000}}, map[string]any{"chainId": "solana", "baseToken": map[string]any{"address": "wrong", "symbol": "SPL"}, "priceUsd": "100", "liquidity": map[string]any{"usd": 999999}}}
			default:
				var rpc struct {
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				_ = json.Unmarshal([]byte(req.Body), &rpc)
				switch rpc.Method {
				case "eth_getBalance":
					answer = map[string]any{"result": "0x1bc16d674ec80000"}
					if scenario == "balance-error" {
						status = 429
					}
				case "getBalance":
					answer = map[string]any{"result": map[string]any{"value": 0}}
				case "getTokenAccountsByOwner":
					entries := []any{}
					if scenario == "spl" && strings.Contains(string(rpc.Params[1]), "Tokenkeg") {
						for _, n := range []int{2, 3} {
							entries = append(entries, map[string]any{"account": map[string]any{"data": map[string]any{"parsed": map[string]any{"info": map[string]any{"owner": "SolTest", "mint": "mintA", "tokenAmount": map[string]any{"uiAmountString": fmt.Sprint(n)}}}}}})
						}
					}
					answer = map[string]any{"result": map[string]any{"value": entries}}
				default:
					return nil, fmt.Errorf("unexpected URL %s", req.URL)
				}
			}
			body, _ := json.Marshal(answer)
			return json.Marshal(map[string]any{"ok": true, "result": map[string]any{"status": status, "body": string(body)}})
		}
	})
	policy, _ := modulert.NewCapabilityPolicyStore("")
	_, _ = policy.Approve(modulert.CapabilityApproval{ModuleHash: modulert.ContentHashHex(wasm), Capability: "http", ApprovedBy: "test"})
	mod, err := modulert.NewModule(wasm, reg, &modulert.NodeContext{CapabilityPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mod.Close() })
	return mod, &calls
}

var bondFixtureAddresses = bondAddresses{Btc: "bc1test", Eth: "0xTest", Sol: "SolTest"}

// bondFixtureAttestation is the module's $WHA for the fixture addresses.
func bondFixtureAttestation(t *testing.T, scenario string, addrs bondAddresses) []byte {
	t.Helper()
	mod, _ := bondFixtureModule(t, scenario)
	raw, err := attestBond(context.Background(), mod, addrs)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Independent fixture arithmetic: 1 BTC * $60,000 + 2 ETH * $3,000 +
// 5 USDC * $1 = $66,005. The $WHA request goes in on "request" and the $WHA
// attestation comes back on "attestation", through the production WasmEdge
// host and its typed hostcalls (http.request, clock.now).
func TestBondModuleFixtures(t *testing.T) {
	for _, scenario := range []string{"complete", "fallback", "zero", "balance-error", "price-error", "malformed", "unpriced", "spl"} {
		t.Run(scenario, func(t *testing.T) {
			mod, calls := bondFixtureModule(t, scenario)
			addrs := bondFixtureAddresses
			if scenario == "zero" || scenario == "malformed" {
				addrs = bondAddresses{Btc: "bc1test"}
			}
			if scenario == "spl" {
				addrs = bondAddresses{Sol: "SolTest"}
			}
			before := time.Now()
			raw, err := attestBond(context.Background(), mod, addrs)
			if err != nil {
				t.Fatal(err)
			}
			w := bondAttestation(raw)
			if w == nil {
				t.Fatalf("not a $WHA: %x", raw)
			}
			want := -1.0
			switch scenario {
			case "complete", "fallback":
				want = 66005
			case "zero":
				want = 0
			case "spl":
				want = 10
			}
			if want < 0 {
				if w.ATTESTED() || w.ERRORSLength() == 0 {
					t.Fatalf("invented value: attested=%v total=%v errors=%d", w.ATTESTED(), w.TOTAL_VALUE(), w.ERRORSLength())
				}
			} else if !w.ATTESTED() || w.TOTAL_VALUE() != want {
				t.Fatalf("want %v: attested=%v total=%v", want, w.ATTESTED(), w.TOTAL_VALUE())
			}
			if string(w.PURPOSE()) != "security-bond" || string(w.CURRENCY()) != "USD" || string(w.ATTESTER_ID()) != "org.spacedatanetwork.bond-attestation" {
				t.Fatalf("attestation header: %q %q %q", w.PURPOSE(), w.CURRENCY(), w.ATTESTER_ID())
			}
			if at := time.UnixMilli(w.AS_OF_MS()); at.Before(before.Add(-time.Second)) || at.After(time.Now().Add(time.Second)) {
				t.Fatalf("AS_OF_MS %d is not the clock hostcall's now", w.AS_OF_MS())
			}
			var c wha.WHAChain
			if scenario == "zero" && len(*calls) != 1 {
				t.Fatalf("zero balance unnecessarily queried prices: %v", *calls)
			}
			if scenario == "complete" && (!w.CHAINS(&c, 1) || string(c.CHAIN_NAME()) != "ETH" || c.TOKEN_COUNT() != 2) {
				t.Fatalf("incorrect token count: %s %d", c.CHAIN_NAME(), c.TOKEN_COUNT())
			}
			if scenario == "spl" && (!w.CHAINS(&c, 0) || c.TOKEN_COUNT() != 1) {
				t.Fatalf("SPL accounts not aggregated: %d", c.TOKEN_COUNT())
			}
		})
	}
}

func TestBondHandlerMethodGate(t *testing.T) {
	b := &bondAttestor{latest: bondFixtureAttestation(t, "complete", bondFixtureAddresses)}
	rec := httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest(http.MethodPut, "/api/v1/trust/bond", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for PUT, got %d", rec.Code)
	}
}

// The handler serves the module's attestation as the dashboard's JSON plus
// the node identity and the attestation timestamp — nothing is synthesized
// host-side.
func TestBondHandlerRendersModuleAttestation(t *testing.T) {
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	b := &bondAttestor{
		latest:     bondFixtureAttestation(t, "complete", bondFixtureAddresses),
		attestedAt: at,
		peerID:     "16UiuTestPeer",
	}
	rec := httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/bond", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Node       string   `json:"node"`
		AttestedAt string   `json:"attested_at"`
		Attested   bool     `json:"attested"`
		BondUSD    *float64 `json:"bond_usd"`
		Holdings   []struct {
			Symbol, Chain, Address, Contract string
			Amount                           float64
			USD                              *float64
		} `json:"holdings"`
		Chains []struct {
			Chain      string   `json:"chain"`
			TokenCount int      `json:"token_count"`
			USD        *float64 `json:"usd"`
			Complete   bool     `json:"complete"`
		} `json:"chains"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body.Node != "16UiuTestPeer" || body.AttestedAt != at.Format(time.RFC3339) {
		t.Fatalf("node identity / attested_at: %q %q", body.Node, body.AttestedAt)
	}
	if !body.Attested || body.BondUSD == nil || *body.BondUSD != 66005 || len(body.Errors) != 0 {
		t.Fatalf("verdict not preserved: %s", rec.Body.String())
	}
	got := map[string]float64{}
	for _, h := range body.Holdings {
		if h.USD == nil {
			t.Fatalf("a priced holding rendered unpriced: %s", rec.Body.String())
		}
		got[h.Chain+":"+h.Symbol+":"+h.Contract] = *h.USD
	}
	if len(got) != 4 || got["BTC:BTC:"] != 60000 || got["ETH:ETH:"] != 6000 || got["ETH:USDC:0xUSDC"] != 5 || got["SOL:SOL:"] != 0 {
		t.Fatalf("holdings: %v", got)
	}
	if len(body.Chains) != 3 || body.Chains[1].Chain != "ETH" || body.Chains[1].TokenCount != 2 || body.Chains[1].USD == nil || *body.Chains[1].USD != 6005 {
		t.Fatalf("chains: %s", rec.Body.String())
	}

	// An unpriced holding stays unpriced: null, never zero.
	b.latest = bondFixtureAttestation(t, "unpriced", bondFixtureAddresses)
	rec = httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/bond", nil))
	if !strings.Contains(rec.Body.String(), `"bond_usd":null`) || !strings.Contains(rec.Body.String(), `"symbol":"USDC","usd":null`) {
		t.Fatalf("unpriced attestation: %s", rec.Body.String())
	}
}

// The embedded module artifact must actually be a wasm binary — an empty or
// truncated embed would otherwise surface only at the first hourly run.
func TestBondModuleArtifactEmbedded(t *testing.T) {
	if len(bondAttestationWasm) < 1024 {
		t.Fatalf("embedded bond-attestation.wasm is suspiciously small: %d bytes", len(bondAttestationWasm))
	}
	if !strings.HasPrefix(string(bondAttestationWasm[:4]), "\x00asm") {
		t.Fatalf("embedded artifact is not a wasm binary")
	}
}

func TestBondPeerCacheAndLookupBounds(t *testing.T) {
	lookups := 0
	cached := bondFixtureAttestation(t, "complete", bondFixtureAddresses)
	b := &bondAttestor{lookupPeer: func(string) (bondAddresses, error) { lookups++; return bondAddresses{}, fmt.Errorf("unknown peer") }, peerCache: map[string]bondPeerResult{"remote": {attestation: cached, at: time.Now()}}}
	rec := httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest("GET", "/api/v1/trust/bond?peer=remote", nil))
	if rec.Code != 200 || lookups != 0 || !strings.Contains(rec.Body.String(), `"node":"remote"`) || !strings.Contains(rec.Body.String(), `"bond_usd":66005`) {
		t.Fatalf("cached peer: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest("GET", "/api/v1/trust/bond?peer=unknown", nil))
	if rec.Code != 404 || lookups != 1 {
		t.Fatalf("unknown peer was queried: %d", rec.Code)
	}
	b.peerMu.Lock()
	defer b.peerMu.Unlock()
	rec = httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest("GET", "/api/v1/trust/bond?peer=remote", nil))
	if rec.Code != 200 {
		t.Fatalf("cached balances blocked by another lookup: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest("GET", "/api/v1/trust/bond?peer=other", nil))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Fatalf("busy lookup must remain pending: %d", rec.Code)
	}
}

// Manual live test (SDN_BOND_LIVE=1): drives the embedded module against the
// real free services with real addresses. Not part of CI — network-dependent.
func TestBondModuleLiveManual(t *testing.T) {
	if os.Getenv("SDN_BOND_LIVE") != "1" {
		t.Skip("set SDN_BOND_LIVE=1 for the live attestation test")
	}
	addrs := bondAddresses{Btc: "bc1qmkz6j54pqnx8f65qupktz35xyw4xqn5scjnqxk"}
	if v := os.Getenv("SDN_BOND_ADDRS"); v != "" { // btc,eth,sol
		parts := append(strings.Split(v, ","), "", "", "")
		addrs = bondAddresses{Btc: parts[0], Eth: parts[1], Sol: parts[2]}
	}
	ctx := context.Background()
	out, err := invokeBondAttestation(ctx, addrs)
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	body, _ := bondJSON(out, "live", time.Now())
	t.Logf("attestation: %s", body)
	if !bondComplete(out) {
		t.Fatalf("live balances are incomplete: %s", body)
	}
	// Repeated GETs must not receive a bodyless 304 from host validator reuse.
	out, err = invokeBondAttestation(ctx, addrs)
	if err != nil {
		t.Fatal(err)
	}
	if !bondComplete(out) {
		t.Fatalf("repeat lookup failed: %x", out)
	}
}
