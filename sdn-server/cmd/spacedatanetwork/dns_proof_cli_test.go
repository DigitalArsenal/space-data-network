package main

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/dnsproof"
)

// TestDNSProofCommandIsRegistered guards the init()-registration choice. The
// command registers itself instead of editing main.go; if that wiring is ever
// dropped, the CLI silently loses the subcommand and the only symptom is an
// operator being told "unknown command" while holding a DNS console open.
func TestDNSProofCommandIsRegistered(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "dns-proof" {
			if c.Flags().Lookup("domain") == nil {
				t.Fatal("dns-proof must expose --domain")
			}
			if c.Flags().Lookup("json") == nil {
				t.Fatal("dns-proof must expose --json; the dashboard menu consumes it")
			}
			return
		}
	}
	t.Fatal("dns-proof is not registered on rootCmd")
}

// TestBuildDNSProofOutputProducesAVerifiableRecord is the end-to-end contract:
// the exact string an operator pastes must parse and verify through the same
// code path the browser runs.
func TestBuildDNSProofOutputProducesAVerifiableRecord(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	issued := time.Unix(1785400000, 0).UTC()

	out, err := buildDNSProofOutput(dnsProofRequest{
		Domain:    "sdn.spaceaware.io",
		PublicKey: pub,
		PeerID:    "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45",
		KeyPath:   "m/44'/0'/0'/0'/0'",
		IssuedAt:  issued,
		ValidFor:  365 * 24 * time.Hour,
		Sign:      func(statement []byte) ([]byte, error) { return ed25519.Sign(priv, statement), nil },
	})
	if err != nil {
		t.Fatalf("buildDNSProofOutput: %v", err)
	}

	if out.OwnerName != "_sdnkey.sdn.spaceaware.io" {
		t.Errorf("owner name = %q", out.OwnerName)
	}
	if out.RecordType != "TXT" {
		t.Errorf("record type = %q", out.RecordType)
	}
	if !out.VerifiedLocal {
		t.Error("the generator must verify its own output before handing it over")
	}
	if out.MultiString {
		t.Errorf("a live-shaped record must fit one DNS character-string, got %d bytes", out.RecordBytes)
	}
	if out.ExpiresAt != issued.Add(365*24*time.Hour).Unix() {
		t.Errorf("expires-at = %d", out.ExpiresAt)
	}

	proof, err := dnsproof.ParseRecord("sdn.spaceaware.io", out.RecordValue)
	if err != nil {
		t.Fatalf("the emitted record must parse: %v", err)
	}
	if err := dnsproof.Verify(proof, issued); err != nil {
		t.Fatalf("the emitted record must verify: %v", err)
	}
	if proof.KeyFingerprint() != out.Fingerprint {
		t.Errorf("fingerprint mismatch: %q vs %q", proof.KeyFingerprint(), out.Fingerprint)
	}

	// The reported statement must be the bytes that were actually signed, not a
	// re-rendering: an operator comparing them by eye is a real review step.
	statement, err := dnsproof.CanonicalStatement(proof)
	if err != nil {
		t.Fatalf("CanonicalStatement: %v", err)
	}
	if string(statement) != out.Statement {
		t.Errorf("reported statement is not the signed statement:\n got %q\nwant %q", out.Statement, statement)
	}
}

// TestDNSProofJSONKeysAreLowercase: these are API-synthesized fields, not SDS
// record keys. SDS keys match IDL capitalization exactly; synthesized fields
// stay lowercase (standing stack rule), and the dashboard consumes this shape.
func TestDNSProofJSONKeysAreLowercase(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	out, err := buildDNSProofOutput(dnsProofRequest{
		Domain:    "example.org",
		PublicKey: pub,
		IssuedAt:  time.Unix(1785400000, 0),
		Sign:      func(s []byte) ([]byte, error) { return ed25519.Sign(priv, s), nil },
	})
	if err != nil {
		t.Fatalf("buildDNSProofOutput: %v", err)
	}
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for key := range decoded {
		if key != strings.ToLower(key) {
			t.Errorf("synthesized JSON key %q must be lowercase", key)
		}
	}
	for _, required := range []string{"owner_name", "record_value", "canonical_statement", "key_fingerprint"} {
		if _, ok := decoded[required]; !ok {
			t.Errorf("missing %q: the dashboard menu and the copy-paste flow need it", required)
		}
	}
}

// TestBuildDNSProofOutputRejectsBadInputBeforeSigning: the domain is validated
// first so a typo never costs a keystore decryption, and a signer that returns
// garbage never produces a record.
func TestBuildDNSProofOutputRejectsBadInput(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer := func(s []byte) ([]byte, error) { return ed25519.Sign(priv, s), nil }

	if _, err := buildDNSProofOutput(dnsProofRequest{
		Domain: "localhost", PublicKey: pub, IssuedAt: time.Unix(1, 0), Sign: signer,
	}); err == nil {
		t.Error("a non-FQDN must be refused")
	}
	if _, err := buildDNSProofOutput(dnsProofRequest{
		Domain: "example.org", Selector: "a.b", PublicKey: pub, IssuedAt: time.Unix(1, 0), Sign: signer,
	}); err == nil {
		t.Error("a multi-label selector must be refused")
	}
	if _, err := buildDNSProofOutput(dnsProofRequest{
		Domain: "example.org", PublicKey: pub, IssuedAt: time.Unix(1785400000, 0),
		Sign: func([]byte) ([]byte, error) { return make([]byte, 64), nil },
	}); err == nil {
		t.Error("a signer returning an invalid signature must not yield a record")
	}
}

// TestNoExpiryIsRepresentedAsZero: --valid-for 0 means "never", and the
// canonical statement must say so explicitly rather than omitting the line.
func TestNoExpiryIsRepresentedAsZero(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	out, err := buildDNSProofOutput(dnsProofRequest{
		Domain:    "example.org",
		PublicKey: pub,
		IssuedAt:  time.Unix(1785400000, 0),
		ValidFor:  0,
		Sign:      func(s []byte) ([]byte, error) { return ed25519.Sign(priv, s), nil },
	})
	if err != nil {
		t.Fatalf("buildDNSProofOutput: %v", err)
	}
	if out.ExpiresAt != 0 {
		t.Errorf("expires-at = %d, want 0", out.ExpiresAt)
	}
	if !strings.Contains(out.Statement, "expires=0\n") {
		t.Errorf("statement must carry expires=0 explicitly: %q", out.Statement)
	}
	if strings.Contains(out.RecordValue, "xp=") {
		t.Errorf("a never-expiring record must omit xp=: %q", out.RecordValue)
	}
}
