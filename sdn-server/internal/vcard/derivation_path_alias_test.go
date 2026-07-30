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
	// NON-hardened tails below the account, because that is the only shape a
	// path alias may legally have: the alias exists so a holder of the entry's
	// XPUB can re-derive the key with BIP-32 CKDpub. An all-hardened path (the
	// shape this fixture used until task sdn-vcf-duplicate-sign-alias) is not
	// publicly derivable and therefore never earns an alias.
	testSigningKeyPath    = "m/44'/0'/7'/0/0"
	testEncryptionKeyPath = "m/44'/0'/7'/1/0"
	testAliasXPub         = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"
)

// createDerivationPathEPM builds a minimal EPM whose signing and encryption
// keys each carry a KEY_ADDRESS derivation path AND the XPUB that path resolves
// against. Both halves are required: a path alias is only emitted for an entry
// that actually asserts a derivation (see appleIdentityEntriesFromEPM).
func createDerivationPathEPM(t *testing.T) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("Derivation Path Node")
	xpubOff := builder.CreateString(testAliasXPub)
	sigKeyOff := builder.CreateString("0xsigningkey123")
	sigPathOff := builder.CreateString(testSigningKeyPath)
	encKeyOff := builder.CreateString("0xencryptionkey456")
	encPathOff := builder.CreateString(testEncryptionKeyPath)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, sigKeyOff)
	EPM.CryptoKeyAddXPUB(builder, xpubOff)
	EPM.CryptoKeyAddKEY_ADDRESS(builder, sigPathOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	sigCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, encKeyOff)
	EPM.CryptoKeyAddXPUB(builder, xpubOff)
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

// createTwoSigningKeyEPM reproduces the exact shape the owner scanned on
// 2026-07-29: TWO KeyTypeSigning entries, the first xpub-derivable
// (secp256k1, non-hardened tail) and the second not (Ed25519 at an
// all-hardened SLIP-10 path, which no extended public key can produce).
func createTwoSigningKeyEPM(t *testing.T, secondKeyXPub string) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("Two Signing Keys Node")
	xpubOff := builder.CreateString(testAliasXPub)
	derivableKeyOff := builder.CreateString("0xderivablesigningkey")
	derivablePathOff := builder.CreateString(testSigningKeyPath)
	hardenedKeyOff := builder.CreateString("0xhardeneded25519key")
	hardenedPathOff := builder.CreateString("m/44'/0'/7'/0'/0'")
	secp256k1Off := builder.CreateString("secp256k1")
	ed25519Off := builder.CreateString("ed25519")

	var secondXPubOff flatbuffers.UOffsetT
	if secondKeyXPub != "" {
		secondXPubOff = builder.CreateString(secondKeyXPub)
	}

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, derivableKeyOff)
	EPM.CryptoKeyAddXPUB(builder, xpubOff)
	EPM.CryptoKeyAddADDRESS_TYPE(builder, secp256k1Off)
	EPM.CryptoKeyAddKEY_ADDRESS(builder, derivablePathOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	derivableKey := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, hardenedKeyOff)
	if secondXPubOff != 0 {
		EPM.CryptoKeyAddXPUB(builder, secondXPubOff)
	}
	EPM.CryptoKeyAddADDRESS_TYPE(builder, ed25519Off)
	EPM.CryptoKeyAddKEY_ADDRESS(builder, hardenedPathOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	hardenedKey := EPM.CryptoKeyEnd(builder)

	EPM.EPMStartKEYSVector(builder, 2)
	builder.PrependUOffsetT(hardenedKey)
	builder.PrependUOffsetT(derivableKey)
	keysVec := builder.EndVector(2)

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddKEYS(builder, keysVec)
	epmOffset := EPM.EPMEnd(builder)

	builder.FinishSizePrefixedWithFileIdentifier(epmOffset, []byte(EPM.EPMIdentifier))
	return builder.FinishedBytes()
}

// TestExactlyOneSignAliasForTheDerivableKey is the owner-reported defect,
// pinned (task sdn-vcf-duplicate-sign-alias, owner 2026-07-29: "the vcf when
// scanning the qr ... has the sign.spacedatanetwork.org path twice").
//
// A record holding a second signing key that asserts NO derivation must yield
// exactly ONE sign alias — the derivable one. Two rows of the same alias kind
// are indistinguishable to every consumer that scans the card, and the extra
// row was one a verifier could never resolve.
func TestExactlyOneSignAliasForTheDerivableKey(t *testing.T) {
	t.Parallel()

	epmBytes := createTwoSigningKeyEPM(t, "")
	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	lines := AppleIdentityEmailAliasLinesFromEPM(epm, epmBytes, "sign")
	if len(lines) != 1 {
		t.Fatalf("sign aliases = %d, want exactly 1: %v", len(lines), lines)
	}
	want := "EMAIL;type=INTERNET;type=sign:" +
		base64.RawURLEncoding.EncodeToString([]byte(testSigningKeyPath)) +
		"@sign.spacedatanetwork.org"
	if got := unfoldVCardForTest(lines[0]); got != want {
		t.Fatalf("sign alias = %q, want %q", got, want)
	}
	// And the underivable path must not appear anywhere on the card, under
	// any alias kind.
	card, err := CompactQRVCard(epmBytes)
	if err != nil {
		t.Fatalf("CompactQRVCard: %v", err)
	}
	hardenedAlias := base64.RawURLEncoding.EncodeToString([]byte("m/44'/0'/7'/0'/0'"))
	if strings.Contains(unfoldVCardForTest(card), hardenedAlias) {
		t.Fatalf("compact QR card still advertises the underivable hardened path:\n%s", card)
	}
}

// TestSecondSignAliasStillSuppressedWhenXPubIsFalselyStamped is the belt to
// the braces above. The LIVE defect was not merely a missing xpub: the node
// stamped its secp256k1 account xpub onto the Ed25519 entry
// (epm/service.go, fixed in the same task), so the record positively asserted
// a derivation that SLIP-10 Ed25519 cannot perform. If that ever regresses in
// the builder, the card must still refuse to advertise a hardened path.
func TestSecondSignAliasStillSuppressedWhenXPubIsFalselyStamped(t *testing.T) {
	t.Parallel()

	epmBytes := createTwoSigningKeyEPM(t, testAliasXPub)
	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	lines := AppleIdentityEmailAliasLinesFromEPM(epm, epmBytes, "sign")
	if len(lines) != 1 {
		t.Fatalf("sign aliases = %d, want exactly 1 even with a falsely stamped XPUB: %v", len(lines), lines)
	}
}

// TestScannedQRCarriesExactlyOneSignAlias proves the fix on the owner's ACTUAL
// repro path: not the served .vcf text, but the bytes that come back out of a
// scanned QR code. Encoding to a real QR and decoding it again is the only test
// that covers the pipeline the owner used ("the vcf when scanning the qr ...
// has the sign.spacedatanetwork.org path twice").
func TestScannedQRCarriesExactlyOneSignAlias(t *testing.T) {
	t.Parallel()

	epmBytes := createTwoSigningKeyEPM(t, testAliasXPub)

	pngData, err := EPMToQR(epmBytes, 512)
	if err != nil {
		t.Fatalf("EPMToQR failed: %v", err)
	}
	scanned, err := QRToVCard(pngData)
	if err != nil {
		t.Fatalf("QRToVCard failed: %v", err)
	}
	unfolded := unfoldVCardForTest(scanned)

	if got := strings.Count(unfolded, "@sign.spacedatanetwork.org"); got != 1 {
		t.Fatalf("SCANNED QR carries %d sign aliases, want exactly 1:\n%s", got, scanned)
	}
	wantPath := base64.RawURLEncoding.EncodeToString([]byte(testSigningKeyPath))
	if !strings.Contains(unfolded, wantPath+"@sign.spacedatanetwork.org") {
		t.Fatalf("SCANNED QR is missing the derivable sign alias:\n%s", scanned)
	}
	if hardened := base64.RawURLEncoding.EncodeToString([]byte("m/44'/0'/7'/0'/0'")); strings.Contains(unfolded, hardened) {
		t.Fatalf("SCANNED QR still advertises the underivable hardened path:\n%s", scanned)
	}
}
