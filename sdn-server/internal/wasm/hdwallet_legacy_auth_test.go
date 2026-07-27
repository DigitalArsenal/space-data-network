package wasm

// Locks DeriveLegacyAuthPublicKey against the scheme hd-wallet-ui's LEGACY
// identity schemes actually use, so the node can recognise its own root account
// when an operator signs in through today's raw-32 admit point (owner directive
// 2026-07-27; §14 of graph/tasks/nst-node-admin-contract.md).
//
// Every mnemonic here is GENERATED AT RUNTIME by the wallet module and never
// written to disk or into this source file — the repository's mnemonic guard
// blocks any file containing a BIP-39 wordlist run, and rightly so.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"
)

// TestDeriveLegacyAuthPublicKeyMatchesTheDocumentedScheme reproduces the
// derivation independently — secp256k1 BIP-32 scalar at m/44'/0'/account'/0/0
// used directly as an Ed25519 seed — and asserts the helper agrees. This is the
// scheme hd-wallet-wasm labels "bip32-scalar-as-ed25519-seed", verified against
// the wallet itself by probe on 2026-07-27.
func TestDeriveLegacyAuthPublicKeyMatchesTheDocumentedScheme(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	mnemonic, err := hw.GenerateMnemonic(ctx, 24)
	if err != nil {
		t.Fatalf("GenerateMnemonic: %v", err)
	}
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed: %v", err)
	}

	for _, account := range []uint32{0, 1, 7} {
		account := account
		t.Run(fmt.Sprintf("account-%d", account), func(t *testing.T) {
			got, err := hw.DeriveLegacyAuthPublicKey(ctx, seed, account)
			if err != nil {
				t.Fatalf("DeriveLegacyAuthPublicKey: %v", err)
			}
			if len(got) != ed25519.PublicKeySize {
				t.Fatalf("public key is %d bytes, want %d", len(got), ed25519.PublicKeySize)
			}

			// Independent reproduction of the documented scheme.
			path := fmt.Sprintf(LegacyAuthKeyPath, account)
			derived, err := hw.DeriveSecp256k1Key(ctx, seed, path)
			if err != nil {
				t.Fatalf("DeriveSecp256k1Key(%s): %v", path, err)
			}
			if len(derived.PrivateKey) != ed25519.SeedSize {
				t.Fatalf("scalar is %d bytes, want %d", len(derived.PrivateKey), ed25519.SeedSize)
			}
			want := ed25519.NewKeyFromSeed(derived.PrivateKey).Public().(ed25519.PublicKey)
			if !bytes.Equal(got, want) {
				t.Fatalf("legacy auth key mismatch at %s", path)
			}
		})
	}
}

// TestLegacyAuthKeyDiffersFromTheSigningKey locks the fact that made the root
// carve-out need TWO keys: for one seed, the legacy raw-challenge key and the
// SLIP-10 §2 signing key are DIFFERENT. A node that registered only one of them
// would silently fail to recognise its own root through the other wallet path.
func TestLegacyAuthKeyDiffersFromTheSigningKey(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	mnemonic, err := hw.GenerateMnemonic(ctx, 24)
	if err != nil {
		t.Fatalf("GenerateMnemonic: %v", err)
	}
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed: %v", err)
	}

	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity: %v", err)
	}
	slipRaw, err := identity.SigningPubKey.Raw()
	if err != nil {
		t.Fatalf("SigningPubKey.Raw: %v", err)
	}

	legacy, err := hw.DeriveLegacyAuthPublicKey(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveLegacyAuthPublicKey: %v", err)
	}

	if bytes.Equal(slipRaw, legacy) {
		t.Fatal("the legacy auth key equals the SLIP-10 signing key; the two derivation paths were expected to differ")
	}
	if identity.SigningKeyPath == fmt.Sprintf(LegacyAuthKeyPath, 0) {
		t.Fatalf("SigningKeyPath and LegacyAuthKeyPath are the same path %q", identity.SigningKeyPath)
	}
}

// TestDeriveLegacyAuthPublicKeyRejectsBadSeeds locks fail-closed input handling:
// a wrong-sized seed must error rather than derive something.
func TestDeriveLegacyAuthPublicKeyRejectsBadSeeds(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	for name, seed := range map[string][]byte{
		"nil":   nil,
		"empty": {},
		"short": make([]byte, 32),
		"long":  make([]byte, 96),
	} {
		if _, err := hw.DeriveLegacyAuthPublicKey(ctx, seed, 0); err == nil {
			t.Fatalf("DeriveLegacyAuthPublicKey accepted a %s seed", name)
		}
	}
}
