package epm

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func TestGetNodeEPMJSONIncludesSecp256k1IdentitySigningKey(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-test", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	info := service.GetNodeEPMJSON()
	rawIdentityPubKey, err := identity.IdentityPubKey.Raw()
	if err != nil {
		t.Fatalf("IdentityPubKey.Raw failed: %v", err)
	}

	keys, ok := info["keys"].([]map[string]interface{})
	if !ok {
		t.Fatalf("keys field type = %T", info["keys"])
	}

	for _, key := range keys {
		if key["key_type"] == "signing" && key["address_type"] == "secp256k1" {
			if got, want := key["public_key"], hex.EncodeToString(rawIdentityPubKey); got != want {
				t.Fatalf("public_key = %v, want %q", got, want)
			}
			if got, want := key["key_address"], identity.IdentityKeyPath; got != want {
				t.Fatalf("key_address = %v, want %q", got, want)
			}
			return
		}
	}

	t.Fatal("expected secp256k1 signing key in EPM keys")
}

func TestGetNodeEPMJSONProjectsRuntimeIdentityFields(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-test", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	info := service.GetNodeEPMJSON()
	signingPubBytes, err := identity.SigningPubKey.Raw()
	if err != nil {
		t.Fatalf("SigningPubKey.Raw failed: %v", err)
	}

	if got, want := info["signing_pubkey_hex"], hex.EncodeToString(signingPubBytes); got != want {
		t.Fatalf("signing_pubkey_hex = %v, want %q", got, want)
	}
	if got, want := info["signing_key_path"], identity.SigningKeyPath; got != want {
		t.Fatalf("signing_key_path = %v, want %q", got, want)
	}
	if got, want := info["encryption_pubkey_hex"], hex.EncodeToString(identity.EncryptionPub); got != want {
		t.Fatalf("encryption_pubkey_hex = %v, want %q", got, want)
	}
	if got, want := info["encryption_key_path"], identity.EncryptionKeyPath; got != want {
		t.Fatalf("encryption_key_path = %v, want %q", got, want)
	}
	if got, want := info["xpub"], "xpub-test"; got != want {
		t.Fatalf("xpub = %v, want %q", got, want)
	}
}

func testDerivedIdentity() (*wasm.DerivedIdentity, error) {
	identityPrivKey, _, err := crypto.GenerateSecp256k1Key(bytes.NewReader(bytes.Repeat([]byte{0x11}, 64)))
	if err != nil {
		return nil, err
	}
	signingPrivKey, signingPubKey, err := crypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x22}, 64)))
	if err != nil {
		return nil, err
	}
	peerID, err := peer.IDFromPublicKey(identityPrivKey.GetPublic())
	if err != nil {
		return nil, err
	}

	return &wasm.DerivedIdentity{
		IdentityPrivKey:    identityPrivKey,
		IdentityPubKey:     identityPrivKey.GetPublic(),
		SigningPrivKey:     signingPrivKey,
		SigningPubKey:      signingPubKey,
		EncryptionKey:      bytes.Repeat([]byte{0x33}, 32),
		EncryptionPub:      bytes.Repeat([]byte{0x44}, 32),
		PeerID:             peerID,
		IdentityKeyPath:    "m/44'/0'/0'",
		SigningKeyPath:     "m/44'/0'/0'/0'/0'",
		EncryptionKeyPath:  "m/44'/0'/0'/1'/0'",
		BitcoinKeyPath:     "m/44'/0'/0'/0/0",
		BitcoinPrivateKey:  bytes.Repeat([]byte{0x55}, 32),
		EthereumKeyPath:    "m/44'/60'/0'/0/0",
		EthereumPrivateKey: bytes.Repeat([]byte{0x66}, 32),
		SolanaKeyPath:      "m/44'/501'/0'/0'",
		SolanaPrivateKey:   bytes.Repeat([]byte{0x77}, 32),
	}, nil
}
