package vcard

// Lock for the owner-ruled derivation-path serialization (graph task
// nst-qr-identity-verify): HD derivation paths ride in vCard-3.0-safe EMAIL
// aliases — phones drop X- properties — with the path base64url-encoded in the
// local part and a <kind>.spacedatanetwork.org domain, and they decode back to
// the literal path for UI display.
//
// This lock previously lived in internal/epm as a test of
// nodeDerivationPathEmailAliasLines. That function was removed when alias
// emission moved here and became a projection of the EPM RECORD itself rather
// than of the in-process identity struct; its test was left behind referring to
// a symbol that no longer exists (a build break in internal/epm at c19dbb8e).
// The rule it protected is unchanged, so it is restated here against the
// public API at its new home.

import (
	"encoding/base64"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

const (
	testSigningKeyPath    = "m/44'/0'/7'/0'/0'"
	testEncryptionKeyPath = "m/44'/0'/7'/1'/0'"
)

// createDerivationPathEPM builds a minimal EPM whose signing and encryption
// keys each carry a KEY_ADDRESS derivation path.
func createDerivationPathEPM(t *testing.T) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("Derivation Path Node")
	sigKeyOff := builder.CreateString("0xsigningkey123")
	sigPathOff := builder.CreateString(testSigningKeyPath)
	encKeyOff := builder.CreateString("0xencryptionkey456")
	encPathOff := builder.CreateString(testEncryptionKeyPath)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, sigKeyOff)
	EPM.CryptoKeyAddKEY_ADDRESS(builder, sigPathOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	sigCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, encKeyOff)
	EPM.CryptoKeyAddKEY_ADDRESS(builder, encPathOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
	encCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.EPMStartKEYSVector(builder, 2)
	builder.PrependUOffsetT(encCryptoKey)
	builder.PrependUOffsetT(sigCryptoKey)
	keysVec := builder.EndVector(2)

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddKEYS(builder, keysVec)
	epmOffset := EPM.EPMEnd(builder)

	builder.FinishSizePrefixedWithFileIdentifier(epmOffset, []byte(EPM.EPMIdentifier))
	return builder.FinishedBytes()
}

// TestDerivationPathEmailAliasSerialization locks the alias shape for both
// derivation-path kinds: EMAIL;type=INTERNET;type=<kind>, a base64url local
// part that decodes to the literal HD path, and the kind's own
// spacedatanetwork.org subdomain.
func TestDerivationPathEmailAliasSerialization(t *testing.T) {
	t.Parallel()

	epmBytes := createDerivationPathEPM(t)
	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	lines := AppleIdentityEmailAliasLinesFromEPM(epm, epmBytes, "sign", "encrypt")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (%v)", len(lines), lines)
	}

	for i, want := range []struct{ kind, path string }{
		{"sign", testSigningKeyPath},
		{"encrypt", testEncryptionKeyPath},
	} {
		// Emitted lines are folded at the vCard line-length limit; the
		// address is only whole again after unfolding.
		line := unfoldVCardForTest(lines[i])
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
}

// TestDerivationPathAliasesRideTheCompactQRCard locks that the scannable card
// — what a phone actually keeps — carries both derivation-path aliases, since
// the whole point of the EMAIL-alias encoding is surviving a vCard 3.0 import.
func TestDerivationPathAliasesRideTheCompactQRCard(t *testing.T) {
	t.Parallel()

	card, err := CompactQRVCard(createDerivationPathEPM(t))
	if err != nil {
		t.Fatalf("CompactQRVCard: %v", err)
	}
	unfolded := unfoldVCardForTest(card)

	for kind, path := range map[string]string{
		"sign":    testSigningKeyPath,
		"encrypt": testEncryptionKeyPath,
	} {
		want := "EMAIL;type=INTERNET;type=" + kind + ":" +
			base64.RawURLEncoding.EncodeToString([]byte(path)) + "@" + kind + ".spacedatanetwork.org"
		if !strings.Contains(unfolded, want) {
			t.Fatalf("compact QR card missing %s derivation-path alias %q, got:\n%s", kind, want, card)
		}
	}
}

// TestNoDerivationPathAliasWithoutAKeyAddress locks that an absent KEY_ADDRESS
// produces NO alias — the card never invents a derivation path.
func TestNoDerivationPathAliasWithoutAKeyAddress(t *testing.T) {
	t.Parallel()

	epm := EPM.GetSizePrefixedRootAsEPM(createTestEPM(), 0)
	if lines := AppleIdentityEmailAliasLinesFromEPM(epm, nil, "sign", "encrypt"); len(lines) != 0 {
		t.Fatalf("keys without KEY_ADDRESS produced derivation-path aliases: %v", lines)
	}
}
