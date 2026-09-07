package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
)

// Before the first successful module run there is NO bond — the endpoint
// 404s rather than inventing a zero (the dashboard renders absence).
func TestBondHandlerBeforeFirstAttestation(t *testing.T) {
	b := &bondAttestor{}
	rec := httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/bond", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 before first attestation, got %d", rec.Code)
	}
}

// Independent fixture arithmetic: 1 BTC * $60,000 + 2 ETH * $3,000 +
// 5 USDC * $1 = $66,005. Run the actual WASM in the production WasmEdge host.
func TestBondModuleFixtures(t *testing.T) {
	wasm := bondAttestationWasm
	if name := os.Getenv("SDN_BOND_TEST_WASM"); name != "" {
		var err error
		wasm, err = os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, scenario := range []string{"complete", "fallback", "zero", "balance-error", "price-error", "malformed", "unpriced", "spl"} {
		t.Run(scenario, func(t *testing.T) {
			calls := []string{}
			reg := modulert.NewCapabilityRegistry()
			reg.Register("http", func(_ *modulert.Module) modulert.CapHandler {
				return func(op string, payload []byte) ([]byte, error) {
					if op != "http.request" {
						t.Fatalf("unexpected operation %s", op)
					}
					var req struct {
						URL  string `json:"url"`
						Body string `json:"body"`
					}
					if err := json.Unmarshal(payload, &req); err != nil {
						return nil, err
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
						answer = []any{map[string]any{"value": "5000000", "token": map[string]any{"type": "ERC-20", "symbol": "USDC", "decimals": "6", "exchange_rate": rate, "address_hash": "0xUSDC"}}}
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
			defer mod.Close()
			args := `{"btc":"bc1test","eth":"0xTest","sol":"SolTest"}`
			if scenario == "zero" || scenario == "malformed" {
				args = `{"btc":"bc1test"}`
			}
			if scenario == "spl" {
				args = `{"sol":"SolTest"}`
			}
			raw, err := mod.InvokeCron(context.Background(), "attest", []byte(args))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Attested bool     `json:"attested"`
				USD      *float64 `json:"bond_usd"`
				Chains   []struct {
					Count int `json:"token_count"`
				} `json:"chains"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
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
				if got.Attested || got.USD != nil {
					t.Fatalf("invented value: %s", raw)
				}
			} else if !got.Attested || got.USD == nil || *got.USD != want {
				t.Fatalf("want %v: %s", want, raw)
			}
			if scenario == "zero" && len(calls) != 1 {
				t.Fatalf("zero balance unnecessarily queried prices: %v", calls)
			}
			if scenario == "complete" && got.Chains[1].Count != 2 {
				t.Fatalf("incorrect token count: %s", raw)
			}
			if scenario == "spl" && got.Chains[0].Count != 1 {
				t.Fatalf("SPL accounts not aggregated: %s", raw)
			}
		})
	}
}

func TestBondHandlerMethodGate(t *testing.T) {
	b := &bondAttestor{latest: json.RawMessage(`{"attested":false}`)}
	rec := httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest(http.MethodPut, "/api/v1/trust/bond", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for PUT, got %d", rec.Code)
	}
}

// The handler serves the module's answer VERBATIM plus the node identity and
// the attestation timestamp — nothing else is synthesized host-side.
func TestBondHandlerWrapsModuleAnswer(t *testing.T) {
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	b := &bondAttestor{
		latest:     json.RawMessage(`{"attested":true,"bond_usd":12.5,"holdings":[{"symbol":"BTC","amount":0.001,"usd":12.5}]}`),
		attestedAt: at,
		peerID:     "16UiuTestPeer",
	}
	rec := httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/bond", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body["node"] != "16UiuTestPeer" {
		t.Fatalf("node identity missing: %v", body["node"])
	}
	if body["attested_at"] != at.Format(time.RFC3339) {
		t.Fatalf("attested_at wrong: %v", body["attested_at"])
	}
	if body["bond_usd"] != 12.5 {
		t.Fatalf("module answer not preserved: %v", body["bond_usd"])
	}
	if body["attested"] != true {
		t.Fatalf("attested flag not preserved: %v", body["attested"])
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
	b := &bondAttestor{lookupPeer: func(string) (bondAddresses, error) { lookups++; return bondAddresses{}, fmt.Errorf("unknown peer") }, peerCache: map[string]bondPeerResult{"remote": {body: json.RawMessage(`{"node":"remote","bond_usd":12}`), at: time.Now()}}}
	rec := httptest.NewRecorder()
	b.handleBond(rec, httptest.NewRequest("GET", "/api/v1/trust/bond?peer=remote", nil))
	if rec.Code != 200 || lookups != 0 || !strings.Contains(rec.Body.String(), `"remote"`) {
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
	if rec.Code != 429 {
		t.Fatalf("unbounded simultaneous lookup: %d", rec.Code)
	}
}

// Manual live test (SDN_BOND_LIVE=1): drives the embedded module against the
// real free services with real addresses. Not part of CI — network-dependent.
func TestBondModuleLiveManual(t *testing.T) {
	if os.Getenv("SDN_BOND_LIVE") != "1" {
		t.Skip("set SDN_BOND_LIVE=1 for the live attestation test")
	}
	capReg := modulert.NewCapabilityRegistry()
	capReg.Register("http", caps.NewHTTPCapFactory())
	mod, err := modulert.NewModule(bondAttestationWasm, capReg, bondModuleNodeContext())
	if err != nil {
		t.Fatalf("module load: %v", err)
	}
	defer mod.Close()
	payload := []byte(os.Getenv("SDN_BOND_ADDRS"))
	if len(payload) == 0 {
		payload = []byte(`{"btc":"bc1qmkz6j54pqnx8f65qupktz35xyw4xqn5scjnqxk"}`)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := mod.InvokeCron(ctx, "attest", payload)
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	t.Logf("attestation: %s", out)
	var probe struct {
		Attested *bool `json:"attested"`
	}
	if err := json.Unmarshal(out, &probe); err != nil || probe.Attested == nil {
		t.Fatalf("non-attestation answer: %s", out)
	}
	if !*probe.Attested {
		t.Fatalf("live balances are incomplete: %s", out)
	}
	// Repeated GETs must not receive a bodyless 304 from host validator reuse.
	out, err = mod.InvokeCron(ctx, "attest", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &probe); err != nil || probe.Attested == nil || !*probe.Attested {
		t.Fatalf("repeat lookup failed: %s", out)
	}
}
