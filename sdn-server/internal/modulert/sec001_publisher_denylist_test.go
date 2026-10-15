package modulert

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

// SEC-001 (verifier r2): a module signed by a denylisted publisher key is
// refused even when the trusted-signer list names that key (as
// SDN_TRUSTED_PUBLISHER_KEYS or the node's own key would), and enforcement
// refuses to load it. A throwaway key stands in for the leaked one.
func TestModuleSignedByADenylistedPublisherIsNeverTrusted(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	var raw map[string]any
	if err := json.Unmarshal(trustanchors.EmbeddedJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	deny := raw["denylist"].(map[string]any)
	deny["ed25519PublicKeys"] = append(deny["ed25519PublicKeys"].([]any), hex.EncodeToString(pub))
	data, _ := json.Marshal(raw)
	anchors, err := trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	artifact := buildSignedModuleArtifact(t, wasmHeader, priv, "denied-key")

	// Before the key is denylisted the artifact verifies under it...
	if _, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{pub}); err != nil || !status.Verified {
		t.Fatalf("baseline: status=%+v err=%v", status, err)
	}

	restore := trustanchors.Use(anchors)
	defer restore()
	_, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{pub})
	if !errors.Is(err, trustanchors.ErrDenied) || status.Verified || status.Reason != "denylisted_signer" {
		t.Fatalf("denylisted signer: status=%+v err=%v", status, err)
	}
	policy := &ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}, AllowUnsignedByContentHash: map[string]bool{}}
	if _, _, err := EnforceModuleSignaturePolicy(policy, artifact); err == nil {
		t.Fatal("enforcement admitted a module signed by a denylisted publisher key")
	}
}

// Verifier r3 BLOCKER: a BUNDLE-scope signature ("sha256-sdn-module-bundle-v1")
// was dispatched to verifyWholeBundleSignature before the denylist check. The
// SDK-built bundle-scope vector, signed by a key that is then denylisted, must
// be refused on both the verify and the enforce paths.
func TestBundleScopeSignatureByADenylistedPublisherIsNeverTrusted(t *testing.T) {
	v, _ := loadStatementDomainVectors(t)
	var vec *statementDomainArtifactVector
	for i := range v.Artifacts {
		if v.Artifacts[i].Name == "bundle-scope-signed-ok" {
			vec = &v.Artifacts[i]
		}
	}
	if vec == nil || vec.SDKArtifactHex == "" {
		t.Fatal("the bundle-scope-signed-ok vector is missing")
	}
	artifact := mustHex(t, vec.SDKArtifactHex)
	var trusted []ed25519.PublicKey
	for _, k := range vec.TrustedPublicKeysHex {
		trusted = append(trusted, ed25519.PublicKey(mustHex(t, k)))
	}
	if _, status, err := verifyPublicationSignature(artifact, trusted); err != nil || !status.Verified || status.SignatureScope != signatureScopeBundle {
		t.Fatalf("baseline: status=%+v err=%v", status, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(trustanchors.EmbeddedJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	deny := raw["denylist"].(map[string]any)
	deny["ed25519PublicKeys"] = append(deny["ed25519PublicKeys"].([]any), v.SignerPubHex)
	data, _ := json.Marshal(raw)
	anchors, err := trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	restore := trustanchors.Use(anchors)
	defer restore()

	_, status, err := verifyPublicationSignature(artifact, trusted)
	if !errors.Is(err, trustanchors.ErrDenied) || status.Verified || status.Reason != "denylisted_signer" {
		t.Fatalf("bundle scope, denylisted signer: status=%+v err=%v", status, err)
	}
	policy := &ModuleSignaturePolicy{TrustedSigners: trusted, AllowUnsignedByContentHash: map[string]bool{}}
	if _, _, err := EnforceModuleSignaturePolicy(policy, artifact); err == nil {
		t.Fatal("enforcement admitted a bundle-scope module signed by a denylisted publisher key")
	}
	// Defense in depth: even with the by-name check bypassed, the filtered
	// trusted list holds no denylisted key.
	if got := withoutDeniedKeys(trusted); len(got) != 0 {
		t.Fatalf("withoutDeniedKeys kept %d denylisted key(s)", len(got))
	}
}
