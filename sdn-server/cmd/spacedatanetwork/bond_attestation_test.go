package main

import (
	"context"
	"encoding/json"
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

// TestBuildBondInvokePayloadPerKey pins the per-key request shape (owner
// ruling 2026-08-07): the ROOT's addresses stay in the legacy flat fields the
// shipped module reads, and every managed key with bondable addresses gets a
// triple under keys[<purpose>]. Empty triples never ship.
func TestBuildBondInvokePayloadPerKey(t *testing.T) {
	root := bondAddresses{Btc: "bc1root", Eth: "0xroot"}
	payload := buildBondInvokePayload(root, map[string]map[string]string{
		"identity-signing": {"bitcoin": "bc1root", "ethereum": "0xroot"},
		"licensing-grant":  {"ethereum": "0xgrant"},
		"empty-key":        {"bitcoin": ""},
	})

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Btc  string                   `json:"btc"`
		Eth  string                   `json:"eth"`
		Keys map[string]bondAddresses `json:"keys"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Btc != "bc1root" || decoded.Eth != "0xroot" {
		t.Fatalf("legacy flat fields must stay the root's: %s", raw)
	}
	if decoded.Keys["identity-signing"].Btc != "bc1root" {
		t.Fatalf("identity-signing triple wrong: %s", raw)
	}
	if decoded.Keys["licensing-grant"].Eth != "0xgrant" {
		t.Fatalf("licensing-grant triple wrong: %s", raw)
	}
	if _, present := decoded.Keys["empty-key"]; present {
		t.Fatalf("an all-empty triple must not ship: %s", raw)
	}

	// No per-key addresses at all -> no keys field on the wire (an old module
	// sees a byte-identical request).
	legacy, _ := json.Marshal(buildBondInvokePayload(root, nil))
	if strings.Contains(string(legacy), "\"keys\"") {
		t.Fatalf("keys field must be absent when there are no per-key addresses: %s", legacy)
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
}
