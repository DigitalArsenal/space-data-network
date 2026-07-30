package node

// Tests for the node-key-as-publisher trust root (owner ruling 2026-07-30).
//
// The ruling is that the publisher key IS the node key, with trust priced by
// the Adversarial-Security bond on that key's derived chain addresses. The
// mechanical consequence these tests pin down is that the trust root is
// DERIVED FROM THE NODE'S OWN IDENTITY rather than configured by an operator —
// that is what collapses the two divergent trust roots the seal council found
// (sdnapi self-trusting node identity vs sdnruntime trusting only an env list)
// into one. Operator keys remain additive, for OTHER nodes' publisher keys.

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// nodeWithSigningKey builds the minimal Node the policy builder needs: just an
// identity carrying an Ed25519 signing key, which is the publisher key of
// record.
func nodeWithSigningKey(t *testing.T) (*Node, ed25519.PublicKey) {
	t.Helper()
	priv, pub, err := libp2pcrypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	raw, err := pub.Raw()
	if err != nil {
		t.Fatalf("pub.Raw: %v", err)
	}
	return &Node{identity: &wasm.DerivedIdentity{SigningPrivKey: priv}}, ed25519.PublicKey(raw)
}

func hasKey(set []ed25519.PublicKey, want ed25519.PublicKey) bool {
	for _, k := range set {
		if hex.EncodeToString(k) == hex.EncodeToString(want) {
			return true
		}
	}
	return false
}

func TestPolicySelfTrustsTheNodeKeyWithNoOperatorConfig(t *testing.T) {
	t.Setenv(trustedPublisherKeysEnv, "")
	t.Setenv(moduleSignatureEnforceEnv, "")
	n, pub := nodeWithSigningKey(t)

	policy := n.buildModuleSignaturePolicy()
	if policy == nil {
		t.Fatal("buildModuleSignaturePolicy must never return nil — a nil policy is the inert state this wiring exists to end")
	}
	if !hasKey(policy.TrustedSigners, pub) {
		t.Fatal("the node's own signing key must be trusted as publisher without any operator configuration (owner ruling: the publisher key IS the node key)")
	}
	if len(policy.TrustedSigners) != 1 {
		t.Fatalf("len(TrustedSigners) = %d, want exactly the node key", len(policy.TrustedSigners))
	}
}

func TestPolicyDefaultsToReportOnlyAndFlipsOnOneEnvVar(t *testing.T) {
	n, _ := nodeWithSigningKey(t)

	t.Setenv(trustedPublisherKeysEnv, "")
	t.Setenv(moduleSignatureEnforceEnv, "")
	if got := n.buildModuleSignaturePolicy(); !got.ReportOnly {
		t.Fatal("default posture must be REPORT-ONLY: an operator who has not made the owner-gated decision must never get a surprise outage")
	}

	// The enforce flip is deliberately one variable, so it is one guarded,
	// reversible step.
	for _, truthy := range []string{"1", "true", "yes", "on", "enforce", "TRUE"} {
		t.Setenv(moduleSignatureEnforceEnv, truthy)
		if got := n.buildModuleSignaturePolicy(); got.ReportOnly {
			t.Fatalf("%s=%q must enforce (ReportOnly=false)", moduleSignatureEnforceEnv, truthy)
		}
	}
	for _, falsy := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Setenv(moduleSignatureEnforceEnv, falsy)
		if got := n.buildModuleSignaturePolicy(); !got.ReportOnly {
			t.Fatalf("%s=%q must stay report-only", moduleSignatureEnforceEnv, falsy)
		}
	}
}

func TestPolicyAddsOperatorKeysAdditivelyAndDedupesSelf(t *testing.T) {
	n, selfPub := nodeWithSigningKey(t)
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Operator lists BOTH another node's key and (redundantly) this node's own.
	t.Setenv(moduleSignatureEnforceEnv, "")
	t.Setenv(trustedPublisherKeysEnv, hex.EncodeToString(otherPub)+","+hex.EncodeToString(selfPub))

	policy := n.buildModuleSignaturePolicy()
	if !hasKey(policy.TrustedSigners, selfPub) || !hasKey(policy.TrustedSigners, otherPub) {
		t.Fatal("both the node key and the operator-supplied key must be trusted")
	}
	if len(policy.TrustedSigners) != 2 {
		t.Fatalf("len(TrustedSigners) = %d, want 2 — the node key must not be duplicated when an operator also lists it", len(policy.TrustedSigners))
	}
}

// TestMalformedOperatorKnobFailsSafe: a typo in the operator env must not widen
// trust, and must not silently drop the node's own key either.
func TestMalformedOperatorKnobFailsSafe(t *testing.T) {
	n, selfPub := nodeWithSigningKey(t)
	t.Setenv(moduleSignatureEnforceEnv, "")
	t.Setenv(trustedPublisherKeysEnv, "not-hex-at-all")

	policy := n.buildModuleSignaturePolicy()
	if len(policy.TrustedSigners) != 1 || !hasKey(policy.TrustedSigners, selfPub) {
		t.Fatalf("a malformed %s must leave exactly the node key trusted, got %d signer(s)", trustedPublisherKeysEnv, len(policy.TrustedSigners))
	}
}

// TestPolicyWithoutNodeIdentityStillAttaches covers an edge node with no HD
// identity: the policy must still be attached (so the observe log works) rather
// than falling back to the inert nil state.
func TestPolicyWithoutNodeIdentityStillAttaches(t *testing.T) {
	t.Setenv(trustedPublisherKeysEnv, "")
	t.Setenv(moduleSignatureEnforceEnv, "")
	n := &Node{}

	policy := n.buildModuleSignaturePolicy()
	if policy == nil {
		t.Fatal("policy must be attached even without a node identity")
	}
	if len(policy.TrustedSigners) != 0 {
		t.Fatalf("no identity and no operator keys must mean no trusted signers, got %d", len(policy.TrustedSigners))
	}
	if !policy.ReportOnly {
		t.Fatal("an identity-less node must not silently enforce with an empty trust set")
	}
}

// TestParseTrustedPublisherKeysMatchesKuboSyntax pins the shared operator-knob
// syntax. Both binaries on host-01 read SDN_TRUSTED_PUBLISHER_KEYS; if the
// parsers disagree the operator cannot give them one value and get one trust
// set, which is the divergence the council flagged.
func TestParseTrustedPublisherKeysMatchesKuboSyntax(t *testing.T) {
	a, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	b, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	hexA, hexB := hex.EncodeToString(a), hex.EncodeToString(b)

	// Comma, semicolon, whitespace and newline separated; mixed case; duplicated.
	got, err := parseTrustedPublisherKeys("  " + hexA + "," + hexB + ";\n" + hexA + "\t" + hexB + "  ")
	if err != nil {
		t.Fatalf("parseTrustedPublisherKeys: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (duplicates collapsed)", len(got))
	}

	if _, err := parseTrustedPublisherKeys(hexA[:len(hexA)-2]); err == nil {
		t.Fatal("a short key must be rejected, not truncated into trust")
	}
	if empty, err := parseTrustedPublisherKeys(""); err != nil || len(empty) != 0 {
		t.Fatalf("empty input must yield no keys and no error, got %d keys err=%v", len(empty), err)
	}
}
