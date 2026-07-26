package epm

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// TestNodeDerivationPathEmailAliasLines locks the owner-ruled serialization:
// HD derivation paths ride in vCard-3.0-safe EMAIL aliases (phones drop X-
// props), base64url in the local part, <kind>.spacedatanetwork.org domains —
// and decode back to the literal paths for UI display.
func TestNodeDerivationPathEmailAliasLines(t *testing.T) {
	identity := &wasm.DerivedIdentity{
		SigningKeyPath:    "m/44'/0'/7'/0'/0'",
		EncryptionKeyPath: "m/44'/0'/7'/1'/0'",
	}
	lines := nodeDerivationPathEmailAliasLines(identity)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (%v)", len(lines), lines)
	}
	for i, want := range []struct{ kind, path string }{
		{"sign", identity.SigningKeyPath},
		{"encrypt", identity.EncryptionKeyPath},
	} {
		line := lines[i]
		prefix := "EMAIL;type=INTERNET;type=" + want.kind + ":"
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("line %d = %q, want prefix %q", i, line, prefix)
		}
		rest := strings.TrimPrefix(line, prefix)
		local, domain, ok := strings.Cut(rest, "@")
		if !ok || domain != want.kind+".spacedatanetwork.org" {
			t.Fatalf("line %d address = %q, want @%s.spacedatanetwork.org", i, rest, want.kind)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(local)
		if err != nil {
			t.Fatalf("line %d local part not base64url: %v", i, err)
		}
		if string(decoded) != want.path {
			t.Fatalf("line %d decodes to %q, want %q", i, decoded, want.path)
		}
	}

	if got := nodeDerivationPathEmailAliasLines(nil); got != nil {
		t.Fatalf("nil identity should produce no lines, got %v", got)
	}
}
