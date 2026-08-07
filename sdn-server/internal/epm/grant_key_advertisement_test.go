package epm

// The licensing GRANT VERIFIER key on the node EPM.
//
// OWNER RULING 2026-08-07 (graph/tasks/sdn-grant-verifier-key-domain-separation.md):
// the grant signer is a derived child of the node identity and the update root is
// isolated. Two consequences meet here:
//
//   - the child's PUBLIC key MUST be advertised in the EPM, because a client that
//     resolves this node's provider descriptor harvests every ed25519 Signing key
//     from it into `trustedGrantVerifierPublicKeys` and refuses any grant whose
//     GRANT_VERIFIER_PUBKEY is absent from that set;
//   - it MUST NOT reach the vCard or the QR (owner law: QR = xpub + sign/encrypt +
//     epmsig EXACTLY, ≤1200B, official props only).

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func grantAdvertisementService(t *testing.T) (*Service, string, string) {
	t.Helper()
	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity: %v", err)
	}
	svc := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "", t.TempDir())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	grantRaw, err := identity.GrantSigningPubKey.Raw()
	if err != nil {
		t.Fatalf("GrantSigningPubKey.Raw: %v", err)
	}
	signingRaw, err := identity.SigningPubKey.Raw()
	if err != nil {
		t.Fatalf("SigningPubKey.Raw: %v", err)
	}
	if hex.EncodeToString(grantRaw) == hex.EncodeToString(signingRaw) {
		t.Fatal("test fixture has the grant key equal to the identity signing key")
	}
	return svc, hex.EncodeToString(grantRaw), hex.EncodeToString(signingRaw)
}

// TestNodeEPMAdvertisesTheGrantVerifierKey — without this the split turns every
// grant failure from "invalid signature" into "invalid verifier".
func TestNodeEPMAdvertisesTheGrantVerifierKey(t *testing.T) {
	t.Parallel()

	svc, grantHex, signingHex := grantAdvertisementService(t)

	record := svc.DirectoryRecordJSON()
	keysAny, ok := record["keys"].([]map[string]any)
	if !ok {
		t.Fatalf("EPM directory record has no keys vector: %#v", record["keys"])
	}

	var sawGrant, sawSigning bool
	for _, key := range keysAny {
		pub, _ := key["public_key"].(string)
		switch strings.ToLower(strings.TrimSpace(pub)) {
		case grantHex:
			sawGrant = true
			if got, _ := key["key_type"].(string); got != "signing" {
				t.Fatalf("grant verifier key_type = %q, want \"signing\" (that is what clients harvest)", got)
			}
			if got, _ := key["address_type"].(string); got != "ed25519" {
				t.Fatalf("grant verifier address_type = %q, want \"ed25519\"", got)
			}
			if got, _ := key["key_address"].(string); got != "m/44'/0'/0'/2'/0'" {
				t.Fatalf("grant verifier key_address = %q, want the grant derivation path", got)
			}
			// XPUB on a CryptoKey ASSERTS public CKDpub-derivability, which is
			// false for an all-hardened SLIP-10 Ed25519 path. Its absence is also
			// what keeps this key off the vCard.
			if xpub, _ := key["xpub"].(string); strings.TrimSpace(xpub) != "" {
				t.Fatalf("grant verifier key carries an XPUB (%q) — that asserts a derivation that cannot exist, and it would put the key on the vCard", xpub)
			}
		case signingHex:
			sawSigning = true
		}
	}
	if !sawGrant {
		t.Fatal("the node EPM does not advertise the licensing grant verifier key — clients resolving the provider descriptor will refuse every grant")
	}
	if !sawSigning {
		t.Fatal("the node EPM no longer advertises the identity signing key")
	}
}

// TestGrantVerifierKeyStaysOffTheVCardAndQR — owner law on the card surface.
func TestGrantVerifierKeyStaysOffTheVCardAndQR(t *testing.T) {
	t.Parallel()

	svc, grantHex, _ := grantAdvertisementService(t)

	card, err := svc.GetNodeVCard()
	if err != nil {
		t.Fatalf("GetNodeVCard: %v", err)
	}
	if strings.Contains(strings.ToLower(card), grantHex) {
		t.Fatal("the grant verifier key's bytes are on the full vCard")
	}
	// The grant path must not appear as a derivation-path alias either: the alias
	// asserts "derive this key from xpub + path", which is unresolvable for an
	// all-hardened Ed25519 path and was the sdn-vcf-duplicate-sign-alias defect.
	if strings.Contains(card, "m/44'/0'/0'/2'/0'") {
		t.Fatal("the grant derivation path is projected onto the vCard")
	}
}
