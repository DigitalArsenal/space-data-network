package epm

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// validTestXPub parses successfully, putting the service on the HD
// (xpub-derived) identity branch.
const validTestXPub = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"

// TestNodeEPMSelfSignatureRoundTripBothIdentityBranches pins the core
// signer/verifier contract: the EPM the service publishes must pass the
// repo's own VerifyEPMSignature for both identity branches (HD/xpub-derived
// and direct-key). The HD branch is the production regression: signer and
// verifier canonicalized different content, so HD self-signatures never
// verified.
func TestNodeEPMSelfSignatureRoundTripBothIdentityBranches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		xpub string
	}{
		// Valid xpub -> derivePublicIdentityKeysFromXPub succeeds -> HD branch.
		{name: "hd-xpub-derived", xpub: validTestXPub},
		// Unparseable xpub -> direct-key branch (matches nodes without an
		// account-level xpub).
		{name: "direct-key", xpub: "xpub-test"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			identity, err := testDerivedIdentity()
			if err != nil {
				t.Fatalf("testDerivedIdentity failed: %v", err)
			}
			service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, tc.xpub, t.TempDir())
			if err := service.Init(); err != nil {
				t.Fatalf("Init failed: %v", err)
			}

			epmBytes := service.GetNodeEPM()
			if len(epmBytes) == 0 {
				t.Fatal("GetNodeEPM returned no bytes")
			}
			if err := VerifyEPMSignature(epmBytes); err != nil {
				t.Fatalf("VerifyEPMSignature failed for %s branch: %v", tc.name, err)
			}

			// The signature must verify against material carried ON THE WIRE
			// only: the Ed25519 signing key from the EPM KEYS vector and the
			// payload recomputed from wire fields. No service-side state may
			// be required (that is what remote verifiers have).
			root := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
			wirePub := wireEd25519SigningKey(t, root)
			payload, err := EPMSigningPayload(epmBytes)
			if err != nil {
				t.Fatalf("EPMSigningPayload failed: %v", err)
			}
			signature, err := decodeHexString(string(root.SIGNATURE()))
			if err != nil {
				t.Fatalf("decode SIGNATURE failed: %v", err)
			}
			if !ed25519.Verify(wirePub, payload, signature) {
				t.Fatal("signature does not verify against wire-derived payload and wire Ed25519 key")
			}

			identityPub, err := identity.SigningPubKey.Raw()
			if err != nil {
				t.Fatalf("SigningPubKey.Raw failed: %v", err)
			}
			if hex.EncodeToString(wirePub) != hex.EncodeToString(identityPub) {
				t.Fatalf("wire Ed25519 signing key = %x, want identity signing key %x", wirePub, identityPub)
			}
		})
	}
}

// TestNodeEPMSignatureCoversWireFieldsBothBranches pins that the canonical
// signing content derives from the wire EPM: mutating a signed wire field
// after signing must fail verification in both identity branches.
func TestNodeEPMSignatureCoversWireFieldsBothBranches(t *testing.T) {
	t.Parallel()

	for _, xpub := range []string{validTestXPub, "xpub-test"} {
		identity, err := testDerivedIdentity()
		if err != nil {
			t.Fatalf("testDerivedIdentity failed: %v", err)
		}
		service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, t.TempDir())
		if err := service.Init(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		epmBytes := service.GetNodeEPM()
		if err := VerifyEPMSignature(epmBytes); err != nil {
			t.Fatalf("VerifyEPMSignature failed before mutation (xpub=%q): %v", xpub, err)
		}

		tampered := append([]byte(nil), epmBytes...)
		root := EPM.GetSizePrefixedRootAsEPM(tampered, 0)
		if !root.MutateSIGNATURE_TIMESTAMP(root.SIGNATURE_TIMESTAMP() + 1) {
			t.Fatal("failed to mutate signature timestamp")
		}
		if err := VerifyEPMSignature(tampered); err == nil {
			t.Fatalf("VerifyEPMSignature accepted mutated wire field (xpub=%q)", xpub)
		}
	}
}

// TestNodeEPMWireKeysIncludeEd25519SigningKeyHDBranch pins that the
// HD-identity wire EPM carries the Ed25519 signing key (the key behind EPM
// self-signatures, PNM signatures, and dataset publications) so directory
// consumers can resolve it from the EPM alone.
func TestNodeEPMWireKeysIncludeEd25519SigningKeyHDBranch(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}
	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, validTestXPub, t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	root := EPM.GetSizePrefixedRootAsEPM(service.GetNodeEPM(), 0)
	pub := wireEd25519SigningKey(t, root)

	identityPub, err := identity.SigningPubKey.Raw()
	if err != nil {
		t.Fatalf("SigningPubKey.Raw failed: %v", err)
	}
	if hex.EncodeToString(pub) != hex.EncodeToString(identityPub) {
		t.Fatalf("wire Ed25519 signing key = %x, want %x", pub, identityPub)
	}

	// Derived secp256k1 keys must still be present (unchanged HD surface).
	var key EPM.CryptoKey
	secpSigning := false
	for i := 0; i < root.KEYSLength(); i++ {
		if root.KEYS(&key, i) && key.KEY_TYPE() == EPM.KeyTypeSigning &&
			strings.EqualFold(string(key.ADDRESS_TYPE()), "secp256k1") {
			secpSigning = true
		}
	}
	if !secpSigning {
		t.Fatal("HD wire EPM lost the derived secp256k1 signing key")
	}
}

// TestRuntimeIdentityKeysIncludeEd25519InBothBranches pins the directory JSON
// projection: the Ed25519 signing key must be present whether or not the
// identity is on the xpub-derived branch.
func TestRuntimeIdentityKeysIncludeEd25519InBothBranches(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}
	info := identity.Info()

	for _, tc := range []struct {
		name string
		xpub string
	}{
		{name: "hd-xpub-derived", xpub: validTestXPub},
		{name: "direct-key", xpub: ""},
	} {
		keys := runtimeIdentityKeys(info, tc.xpub)
		found := false
		for _, key := range keys {
			if key["key_type"] == "signing" && key["address_type"] == "ed25519" {
				if got, want := key["public_key"], info.SigningPubKeyHex; got != want {
					t.Fatalf("%s: ed25519 public_key = %v, want %q", tc.name, got, want)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: runtimeIdentityKeys dropped the Ed25519 signing key: %v", tc.name, keys)
		}
	}
}

// wireEd25519SigningKey extracts the Ed25519 signing public key from the EPM
// KEYS vector, as remote verifiers and directory indexers do.
func wireEd25519SigningKey(t *testing.T, root *EPM.EPM) ed25519.PublicKey {
	t.Helper()
	var key EPM.CryptoKey
	for i := 0; i < root.KEYSLength(); i++ {
		if !root.KEYS(&key, i) || key.KEY_TYPE() != EPM.KeyTypeSigning {
			continue
		}
		addrType := strings.ToLower(strings.TrimSpace(string(key.ADDRESS_TYPE())))
		if addrType != "" && addrType != "ed25519" {
			continue
		}
		pub, err := decodeHexString(string(key.PUBLIC_KEY()))
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}
		return ed25519.PublicKey(pub)
	}
	t.Fatal("no Ed25519 signing key found in wire EPM KEYS")
	return nil
}
