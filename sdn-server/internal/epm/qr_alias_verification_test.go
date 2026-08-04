package epm

import (
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// parseAliasEmails extracts <kind> -> local-part values from a vCard's
// EMAIL;...:<local>@<kind>.spacedatanetwork.org lines — exactly what a
// phone-side importer sees after contact import (X-* properties dropped).
func parseAliasEmails(t *testing.T, card string) map[string][]string {
	t.Helper()
	unfolded := strings.ReplaceAll(strings.ReplaceAll(card, "\r\n ", ""), "\n ", "")
	aliases := map[string][]string{}
	for _, line := range strings.Split(unfolded, "\r\n") {
		if !strings.HasPrefix(line, "EMAIL") {
			continue
		}
		_, addr, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		at := strings.LastIndex(addr, "@")
		if at <= 0 || !strings.HasSuffix(addr[at+1:], ".spacedatanetwork.org") {
			continue
		}
		kind := strings.TrimSuffix(addr[at+1:], ".spacedatanetwork.org")
		aliases[kind] = append(aliases[kind], addr[:at])
	}
	return aliases
}

// TestQRAliasChainProvesEPMSignature is the owner-ruled end-to-end proof:
// the email aliases in the SCANNED card alone, plus the downloadable
// serialized EPM, are sufficient for a third party to verify the record —
// no X-* properties (which phones drop) required. The chain:
//
//  1. epmcid alias == CID of the fetched serialized EPM  (integrity of fetch)
//  2. epmsig/epmts aliases == the record's embedded signature + timestamp
//  3. xpub alias + sign-path alias  ->  derive the secp256k1 signing public
//     key; it MUST appear among the record's signing KEYS (binds the signed
//     record to the xpub holder)
//  4. VerifyEPMSignature passes over the record
func TestQRAliasChainProvesEPMSignature(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}
	const xpub = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"
	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// The FULL downloadable card carries the complete verification chain.
	// (The scannable QR card carries only xpub + sign/encrypt + epmsig —
	// owner ruling 2026-08-04 dropped epmts/epmcid for scan density — so
	// the chain-proof walk below runs against the full card.)
	card, err := service.GetNodeVCard()
	if err != nil {
		t.Fatalf("GetNodeVCard failed: %v", err)
	}
	aliases := parseAliasEmails(t, card)
	epmBytes := service.GetNodeEPM()

	// 1. Content addressing: the epmcid alias identifies the exact record.
	wantCid, err := ComputeEPMCID(epmBytes)
	if err != nil {
		t.Fatalf("ComputeEPMCID failed: %v", err)
	}
	if len(aliases["epmcid"]) != 1 || aliases["epmcid"][0] != wantCid {
		t.Fatalf("epmcid alias = %v, want [%s]", aliases["epmcid"], wantCid)
	}

	// 2. Signature + timestamp aliases mirror the record's embedded values.
	record := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	wantSig, err := hex.DecodeString(strings.TrimSpace(string(record.SIGNATURE())))
	if err != nil || len(wantSig) == 0 {
		t.Fatalf("record signature not decodable: %v", err)
	}
	if len(aliases["epmsig"]) != 1 {
		t.Fatalf("epmsig aliases = %v, want exactly 1", aliases["epmsig"])
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(aliases["epmsig"][0])
	if err != nil {
		t.Fatalf("epmsig alias not base64url: %v", err)
	}
	if hex.EncodeToString(gotSig) != hex.EncodeToString(wantSig) {
		t.Fatal("epmsig alias does not match the record's embedded signature")
	}
	if len(aliases["epmts"]) != 1 ||
		aliases["epmts"][0] != strconv.FormatInt(record.SIGNATURE_TIMESTAMP(), 10) {
		t.Fatalf("epmts alias = %v, want [%d]", aliases["epmts"], record.SIGNATURE_TIMESTAMP())
	}

	// 3. Derive the signing key from the scanned xpub at the scanned sign
	// path; it must be one of the record's signing keys.
	if len(aliases["xpub"]) != 1 || aliases["xpub"][0] != xpub {
		t.Fatalf("xpub alias = %v, want [%s]", aliases["xpub"], xpub)
	}
	derived, ok := PublicIdentityKeysFromXPub(xpub, identity.Account)
	if !ok {
		t.Fatal("PublicIdentityKeysFromXPub failed")
	}
	// OWNER RULE, task sdn-vcf-duplicate-sign-alias (2026-07-29): EXACTLY ONE
	// sign alias, and it is the xpub-derivable path.
	//
	// This assertion used to scan the sign aliases for *a* derivable one and
	// accept extras alongside it. That tolerance is what let the live card ship
	// two indistinguishable sign@ rows — the second being the Ed25519 key's
	// all-hardened SLIP-10 path, which no extended public key can derive. A
	// consumer scanning the QR has no way to tell two rows of the same alias
	// kind apart, so "at least one is usable" is not a contract; "exactly one,
	// and it resolves" is.
	if len(aliases["sign"]) != 1 {
		t.Fatalf("sign aliases = %v, want exactly 1 (the xpub-derivable path)", aliases["sign"])
	}
	signedPath, err := base64.RawURLEncoding.DecodeString(aliases["sign"][0])
	if err != nil {
		t.Fatalf("sign alias %q not base64url: %v", aliases["sign"][0], err)
	}
	if string(signedPath) != derived.SigningKeyPath {
		t.Fatalf("sign alias decodes to %q, want the xpub-derivable path %q",
			signedPath, derived.SigningKeyPath)
	}
	// Same rule on the encryption side (already owner-ruled: ONE ENCRYPTION
	// PATH), asserted here so the two halves cannot drift apart again.
	if len(aliases["encrypt"]) != 1 {
		t.Fatalf("encrypt aliases = %v, want exactly 1", aliases["encrypt"])
	}
	keyInRecord := false
	key := new(EPM.CryptoKey)
	for i := 0; i < record.KEYSLength(); i++ {
		if record.KEYS(key, i) && key.KEY_TYPE() == EPM.KeyTypeSigning &&
			strings.EqualFold(strings.TrimSpace(string(key.PUBLIC_KEY())), derived.SigningPublicKey) {
			keyInRecord = true
			break
		}
	}
	if !keyInRecord {
		t.Fatalf("xpub-derived signing key %s not present in the record's KEYS", derived.SigningPublicKey)
	}

	// 4. The record's embedded signature verifies.
	if err := VerifyEPMSignature(epmBytes); err != nil {
		t.Fatalf("VerifyEPMSignature failed: %v", err)
	}
}

// TestEveryXPubBearingKeyReallyDerives locks the CryptoKey.XPUB contract, which
// is the invariant the whole card paradigm rests on and the one that broke:
//
//	XPUB set on a KEYS entry ASSERTS that PUBLIC_KEY is BIP-32 CKDpub-derivable
//	from XPUB at KEY_ADDRESS.
//
// The vCard alias block is a projection of that assertion — it publishes a
// derivation-path alias precisely for the entries that make it — so a false
// assertion becomes a published, unresolvable alias. That is exactly what the
// owner scanned on 2026-07-29: the node stamped its secp256k1 account xpub onto
// the Ed25519 signing key, whose all-hardened SLIP-10 path no extended public
// key can reach, and the card grew a second indistinguishable sign@ row.
//
// Asserting the invariant here rather than only counting aliases on the card
// puts the check in the layer that owns derivation, and it fails on the CAUSE
// instead of the symptom.
func TestEveryXPubBearingKeyReallyDerives(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}
	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, validTestXPub, t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	record := EPM.GetSizePrefixedRootAsEPM(service.GetNodeEPM(), 0)
	key := new(EPM.CryptoKey)
	checked := 0
	for i := 0; i < record.KEYSLength(); i++ {
		if !record.KEYS(key, i) {
			continue
		}
		keyXPub := strings.TrimSpace(string(key.XPUB()))
		if keyXPub == "" {
			// Asserts no derivation, so there is nothing to hold it to. Its
			// bytes still ride the record for anyone who fetches it.
			continue
		}
		path := strings.TrimSpace(string(key.KEY_ADDRESS()))
		pub := strings.TrimSpace(string(key.PUBLIC_KEY()))
		addressType := strings.TrimSpace(string(key.ADDRESS_TYPE()))
		if addressType != "secp256k1" {
			t.Fatalf("key %d claims XPUB derivation but is %q — only secp256k1 has BIP-32 public derivation (path %q)",
				i, addressType, path)
		}
		derivedPub, err := deriveXPubPublicKeyAtPath(keyXPub, path)
		if err != nil {
			t.Fatalf("key %d claims XPUB derivation at %q but it does not derive: %v", i, path, err)
		}
		if !strings.EqualFold(derivedPub, pub) {
			t.Fatalf("key %d at %q derives to %s, but the record publishes %s", i, path, derivedPub, pub)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no XPUB-bearing keys in the node EPM — the invariant was not exercised")
	}
}
