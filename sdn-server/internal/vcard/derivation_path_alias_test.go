package vcard

// Lock for the §21 literal-key alias serialization (owner ruling 2026-08-19):
// the sign/encrypt EMAIL aliases carry b64url(literal PUBLIC KEY BYTES), not
// a derivation path. xpub and KEY_PATH are private; the published card's
// aliases ARE the verification material. The local part decodes to the raw
// key bytes (hex-decoded from the record's CryptoKey.PUBLIC_KEY, then
// base64url-encoded for email safety).
//
// This file previously locked the §17/§18 derivation-PATH alias format. §21
// reversed that: the local part is now key bytes, not a path. The tests are
// rewritten to assert the new shape; the one-row invariant (exactly ONE sign
// alias) and the "no second indistinguishable row" guard survive unchanged.

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

const (
	// Valid hex public keys — 32-byte ed25519-sized keys for the fixture.
	// Under §21 the alias local part is b64url(hex-decode(key)) of these.
	testSigningPubKeyHex    = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	testEncryptionPubKeyHex = "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"
	testAliasXPub           = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"
)

// b64urlOfHex decodes a hex string to raw bytes and re-encodes as base64url —
// the exact transform hexPublicKeyToAlias applies in production.
func b64urlOfHex(t *testing.T, hexKey string) string {
	t.Helper()
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		t.Fatalf("bad hex fixture: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// createLiteralKeyEPM builds a minimal EPM whose signing and encryption keys
// each carry a PUBLIC_KEY (hex) — the §21 published-identity shape. XPUB and
// KEY_ADDRESS are absent (private), which is what a published record now looks
// like.
func createLiteralKeyEPM(t *testing.T) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("Literal Key Node")
	sigKeyOff := builder.CreateString(testSigningPubKeyHex)
	encKeyOff := builder.CreateString(testEncryptionPubKeyHex)
	signatureOff := builder.CreateString("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, sigKeyOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	sigCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, encKeyOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
	encCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.EPMStartKEYSVector(builder, 2)
	builder.PrependUOffsetT(encCryptoKey)
	builder.PrependUOffsetT(sigCryptoKey)
	keysVec := builder.EndVector(2)

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddKEYS(builder, keysVec)
	EPM.EPMAddSIGNATURE(builder, signatureOff)
	EPM.EPMAddSIGNATURE_TIMESTAMP(builder, 1700000000)
	epmOffset := EPM.EPMEnd(builder)

	builder.FinishSizePrefixedWithFileIdentifier(epmOffset, []byte(EPM.EPMIdentifier))
	return builder.FinishedBytes()
}

// TestLiteralKeyEmailAliasSerialization locks the §21 alias shape for both
// kinds: EMAIL;type=INTERNET;type=<kind>, a base64url local part that decodes
// to the literal PUBLIC KEY BYTES (not a derivation path), and the kind's own
// spacedatanetwork.org subdomain.
func TestLiteralKeyEmailAliasSerialization(t *testing.T) {
	t.Parallel()

	epmBytes := createLiteralKeyEPM(t)
	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	lines := AppleIdentityEmailAliasLinesFromEPM(epm, epmBytes, "sign", "encrypt")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (%v)", len(lines), lines)
	}

	for i, want := range []struct{ kind, hexKey string }{
		{"sign", testSigningPubKeyHex},
		{"encrypt", testEncryptionPubKeyHex},
	} {
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
		// The local part is b64url(hex-decode(key)) — decode it back and
		// compare against the raw key bytes, not the path.
		decoded, err := base64.RawURLEncoding.DecodeString(local)
		if err != nil {
			t.Fatalf("line %d local part not base64url: %v", i, err)
		}
		rawKey, _ := hex.DecodeString(want.hexKey)
		if string(decoded) != string(rawKey) {
			t.Fatalf("line %d decodes to %x, want key bytes %x", i, decoded, rawKey)
		}
	}
}

// TestLiteralKeyAliasesRideTheCompactQRCard locks that the scannable card
// carries both literal-key aliases — the whole point of the EMAIL-alias
// encoding is surviving a vCard 3.0 import.
func TestLiteralKeyAliasesRideTheCompactQRCard(t *testing.T) {
	t.Parallel()

	card, err := CompactQRVCard(createLiteralKeyEPM(t))
	if err != nil {
		t.Fatalf("CompactQRVCard: %v", err)
	}
	unfolded := unfoldVCardForTest(card)

	for kind, hexKey := range map[string]string{
		"sign":    testSigningPubKeyHex,
		"encrypt": testEncryptionPubKeyHex,
	} {
		want := "EMAIL;type=INTERNET;type=" + kind + ":" +
			b64urlOfHex(t, hexKey) + "@" + kind + ".spacedatanetwork.org"
		if !strings.Contains(unfolded, want) {
			t.Fatalf("compact QR card missing %s literal-key alias %q, got:\n%s", kind, want, card)
		}
	}
}

// TestNoAliasWithoutAPublicKey locks that an absent PUBLIC_KEY produces NO
// alias — the card never invents a key.
func TestNoAliasWithoutAPublicKey(t *testing.T) {
	t.Parallel()

	epm := EPM.GetSizePrefixedRootAsEPM(createTestEPM(), 0)
	if lines := AppleIdentityEmailAliasLinesFromEPM(epm, nil, "sign", "encrypt"); len(lines) != 0 {
		t.Fatalf("keys without PUBLIC_KEY produced aliases: %v", lines)
	}
}

// createTwoSigningKeyEPM reproduces a record with TWO KeyTypeSigning entries.
// Under §21 both carry PUBLIC_KEY bytes; the one-row invariant means the card
// emits exactly ONE sign alias (the first signing key — the one that produced
// SIGNATURE). A second signing key's alias would be an indistinguishable
// duplicate row (sdn-vcf-duplicate-sign-alias precedent).
func createTwoSigningKeyEPM(t *testing.T, secondKeyXPub string) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("Two Signing Keys Node")
	firstKeyOff := builder.CreateString(testSigningPubKeyHex)
	secondKeyOff := builder.CreateString("c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4")

	var secondXPubOff flatbuffers.UOffsetT
	if secondKeyXPub != "" {
		secondXPubOff = builder.CreateString(secondKeyXPub)
	}

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, firstKeyOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	firstKey := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, secondKeyOff)
	if secondXPubOff != 0 {
		EPM.CryptoKeyAddXPUB(builder, secondXPubOff)
	}
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	secondKey := EPM.CryptoKeyEnd(builder)

	EPM.EPMStartKEYSVector(builder, 2)
	builder.PrependUOffsetT(secondKey)
	builder.PrependUOffsetT(firstKey)
	keysVec := builder.EndVector(2)

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddKEYS(builder, keysVec)
	epmOffset := EPM.EPMEnd(builder)

	builder.FinishSizePrefixedWithFileIdentifier(epmOffset, []byte(EPM.EPMIdentifier))
	return builder.FinishedBytes()
}

// TestExactlyOneSignAlias locks the one-row invariant: a record with two
// signing keys yields exactly ONE sign alias. Two rows of the same alias kind
// are indistinguishable to every consumer that scans the card.
func TestExactlyOneSignAlias(t *testing.T) {
	t.Parallel()

	epmBytes := createTwoSigningKeyEPM(t, "")
	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	lines := AppleIdentityEmailAliasLinesFromEPM(epm, epmBytes, "sign")
	if len(lines) != 1 {
		t.Fatalf("sign aliases = %d, want exactly 1: %v", len(lines), lines)
	}
	want := "EMAIL;type=INTERNET;type=sign:" +
		b64urlOfHex(t, testSigningPubKeyHex) +
		"@sign.spacedatanetwork.org"
	if got := unfoldVCardForTest(lines[0]); got != want {
		t.Fatalf("sign alias = %q, want %q", got, want)
	}
}

// TestScannedQRCarriesExactlyOneSignAlias proves the one-row invariant on the
// owner's actual repro path: the bytes that come back out of a scanned QR.
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
	wantKey := b64urlOfHex(t, testSigningPubKeyHex)
	if !strings.Contains(unfolded, wantKey+"@sign.spacedatanetwork.org") {
		t.Fatalf("SCANNED QR is missing the sign alias:\n%s", scanned)
	}
}
