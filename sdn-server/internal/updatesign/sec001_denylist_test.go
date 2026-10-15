package updatesign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

// SEC-001 (verifier r2): the node-side update signer — behind the
// sign-manifest endpoint, the Updater page's approve button and the signal
// signer — refuses a denylisted key. The real leaked private key is never
// available to a test, so a throwaway key is put on a test denylist.
func TestNewSignerRefusesADenylistedKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(trustanchors.EmbeddedJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	deny := raw["denylist"].(map[string]any)
	deny["ed25519PublicKeys"] = append(deny["ed25519PublicKeys"].([]any), hexString(pub))
	data, _ := json.Marshal(raw)
	anchors, err := trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	restore := trustanchors.Use(anchors)
	defer restore()

	audit := NewAuditLog(filepath.Join(t.TempDir(), "audit.jsonl"))
	if _, err := NewSigner(priv, audit); !errors.Is(err, trustanchors.ErrDenied) {
		t.Fatalf("NewSigner(denylisted key) = %v, want ErrDenied", err)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSigner(other, audit); err != nil {
		t.Fatalf("NewSigner(ordinary key) = %v", err)
	}
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
