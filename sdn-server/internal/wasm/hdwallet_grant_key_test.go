package wasm

// Derivation determinism and domain separation for the LICENSING GRANT signing
// key (LicensingGrantKeyPath, m/44'/0'/<account>'/2'/0').
//
// OWNER RULING 2026-08-07, verbatim: "derive a grant-signing child from the node
// identity, keep the update root isolated"
// (graph/tasks/sdn-grant-verifier-key-domain-separation.md).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"
)

// TestDeriveIdentityGrantKeyIsDeterministic — same identity in, same grant child
// out. A node that derived a different grant key across restarts would silently
// invalidate the verifier key every client holds from KRF.PUBLIC_KEY, and the
// failure would look exactly like a corrupt signature.
func TestDeriveIdentityGrantKeyIsDeterministic(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	first, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity() error = %v", err)
	}
	firstSeed, err := first.RawGrantSigningKey()
	if err != nil {
		t.Fatalf("RawGrantSigningKey() error = %v", err)
	}
	if len(firstSeed) != ed25519.SeedSize {
		t.Fatalf("grant signing seed is %d bytes, want %d", len(firstSeed), ed25519.SeedSize)
	}

	for i := 0; i < 3; i++ {
		again, err := hw.DeriveIdentity(ctx, seed, 0)
		if err != nil {
			t.Fatalf("DeriveIdentity() re-run %d error = %v", i, err)
		}
		againSeed, err := again.RawGrantSigningKey()
		if err != nil {
			t.Fatalf("RawGrantSigningKey() re-run %d error = %v", i, err)
		}
		if !bytes.Equal(firstSeed, againSeed) {
			t.Fatalf("grant signing key is not deterministic: re-run %d gave %x, want %x", i, againSeed, firstSeed)
		}
	}

	if got, want := first.GrantSigningKeyPath, "m/44'/0'/0'/2'/0'"; got != want {
		t.Fatalf("GrantSigningKeyPath = %q, want %q", got, want)
	}
}

// TestDeriveIdentityGrantKeyIsSeparatedFromUpdateRoot is the ruling's core
// invariant. The identity signing key (SigningKeyPath) is the fleet update /
// publisher-of-record root; the grant key must not be it, and must not be any
// other key the identity already exposes.
func TestDeriveIdentityGrantKeyIsSeparatedFromUpdateRoot(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}
	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity() error = %v", err)
	}

	updateRootSeed, err := identity.RawSigningKey()
	if err != nil {
		t.Fatalf("RawSigningKey() error = %v", err)
	}
	grantSeed, err := identity.RawGrantSigningKey()
	if err != nil {
		t.Fatalf("RawGrantSigningKey() error = %v", err)
	}

	if bytes.Equal(grantSeed, updateRootSeed) {
		t.Fatal("the licensing grant seed IS the fleet update/publisher root seed — the exact defect this path removes")
	}
	if bytes.Equal(grantSeed, identity.EncryptionKey) {
		t.Fatal("the licensing grant seed collides with the X25519 encryption key")
	}

	grantPub, err := identity.GrantSigningPublicKey()
	if err != nil {
		t.Fatalf("GrantSigningPublicKey() error = %v", err)
	}
	updateRootPub, err := identity.SigningPubKey.Raw()
	if err != nil {
		t.Fatalf("SigningPubKey.Raw() error = %v", err)
	}
	if bytes.Equal(grantPub, updateRootPub) {
		t.Fatal("the grant verifier public key IS the fleet update root public key")
	}

	// The published verifier key must be the one the signer actually uses — the
	// same equality the 02f012b1 fix depends on, now on the child.
	if want := ed25519.NewKeyFromSeed(grantSeed).Public().(ed25519.PublicKey); !bytes.Equal(grantPub, want) {
		t.Fatalf("GrantSigningPublicKey %x != ed25519.NewKeyFromSeed(grantSeed).Public() %x", grantPub, want)
	}

	// Info() must surface both, so the separation is checkable from a boot log.
	info := identity.Info()
	if info.GrantSigningKeyPath == "" || info.GrantSigningPubKeyHex == "" {
		t.Fatalf("Info() omits the grant key: path=%q pubkey=%q", info.GrantSigningKeyPath, info.GrantSigningPubKeyHex)
	}
	if info.GrantSigningPubKeyHex == info.SigningPubKeyHex {
		t.Fatal("Info() reports the same public key for the grant lane and the update root")
	}
}

// TestDeriveIdentityGrantKeyIsAccountScoped — different accounts derive different
// grant keys, so a multi-account node cannot cross-issue grants.
func TestDeriveIdentityGrantKeyIsAccountScoped(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	zero, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity(0) error = %v", err)
	}
	one, err := hw.DeriveIdentity(ctx, seed, 1)
	if err != nil {
		t.Fatalf("DeriveIdentity(1) error = %v", err)
	}

	zeroSeed, err := zero.RawGrantSigningKey()
	if err != nil {
		t.Fatalf("RawGrantSigningKey(0) error = %v", err)
	}
	oneSeed, err := one.RawGrantSigningKey()
	if err != nil {
		t.Fatalf("RawGrantSigningKey(1) error = %v", err)
	}
	if bytes.Equal(zeroSeed, oneSeed) {
		t.Fatal("accounts 0 and 1 derived the same licensing grant signing key")
	}
	if got, want := one.GrantSigningKeyPath, "m/44'/0'/1'/2'/0'"; got != want {
		t.Fatalf("account 1 GrantSigningKeyPath = %q, want %q", got, want)
	}
}
