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

	card, err := service.GetNodeQRVCard()
	if err != nil {
		t.Fatalf("GetNodeQRVCard failed: %v", err)
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
	foundDerivablePath := false
	for _, encoded := range aliases["sign"] {
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("sign alias %q not base64url: %v", encoded, err)
		}
		if string(raw) == derived.SigningKeyPath {
			foundDerivablePath = true
		}
	}
	if !foundDerivablePath {
		t.Fatalf("no sign alias decodes to the xpub-derivable path %q (aliases=%v)",
			derived.SigningKeyPath, aliases["sign"])
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
