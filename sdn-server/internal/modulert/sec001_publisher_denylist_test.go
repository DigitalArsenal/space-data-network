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
