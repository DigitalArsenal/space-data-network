package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// newTestSigningKeyHex generates a fresh Ed25519 keypair via crypto/rand
// (never hardcoded/real key material) and returns its public key as the
// lowercase hex string userstore.go expects.
func newTestSigningKeyHex(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return hex.EncodeToString(pub)
}

func TestReconcileSigningKey_NoUser_NothingToConflictWith(t *testing.T) {
	dir := t.TempDir()
	store, err := NewUserStore(filepath.Join(dir, "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer store.Close()

	if err := store.ReconcileSigningKey("xpub-does-not-exist", newTestSigningKeyHex(t)); err != nil {
		t.Fatalf("ReconcileSigningKey() error = %v, want nil", err)
	}
}

func TestReconcileSigningKey_NoBoundKeyYet_NothingToConflictWith(t *testing.T) {
	dir := t.TempDir()
	store, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{XPub: "xpub-tofu-pending", Name: "Pending User", TrustLevel: "standard"},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer store.Close()

	if err := store.ReconcileSigningKey("xpub-tofu-pending", newTestSigningKeyHex(t)); err != nil {
		t.Fatalf("ReconcileSigningKey() error = %v, want nil (no key bound yet)", err)
	}

	// And it must NOT have bound anything as a side effect.
	user, err := store.GetUser("xpub-tofu-pending")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.SigningPubKeyHex != "" {
		t.Fatalf("ReconcileSigningKey must not bind a key as a side effect, got %q", user.SigningPubKeyHex)
	}
}

func TestReconcileSigningKey_MatchingCandidate_NoConflict(t *testing.T) {
	boundHex := newTestSigningKeyHex(t)

	dir := t.TempDir()
	store, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{XPub: "xpub-bound", Name: "Bound User", TrustLevel: "standard", SigningPubKeyHex: boundHex},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer store.Close()

	if err := store.ReconcileSigningKey("xpub-bound", boundHex); err != nil {
		t.Fatalf("ReconcileSigningKey() error = %v, want nil for a matching candidate", err)
	}
	// Case/0x-prefix tolerance.
	if err := store.ReconcileSigningKey("xpub-bound", "0x"+boundHex); err != nil {
		t.Fatalf("ReconcileSigningKey() error = %v, want nil for a 0x-prefixed matching candidate", err)
	}
}

func TestReconcileSigningKey_ConflictingCandidate_Rejected(t *testing.T) {
	boundHex := newTestSigningKeyHex(t)
	conflictingHex := newTestSigningKeyHex(t)

	dir := t.TempDir()
	store, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{XPub: "xpub-bound", Name: "Bound User", TrustLevel: "standard", SigningPubKeyHex: boundHex},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer store.Close()

	err = store.ReconcileSigningKey("xpub-bound", conflictingHex)
	if !errors.Is(err, ErrTOFUConflict) {
		t.Fatalf("ReconcileSigningKey() error = %v, want ErrTOFUConflict", err)
	}

	// The existing binding must be left untouched (no silent override).
	user, err := store.GetUser("xpub-bound")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.SigningPubKeyHex != boundHex {
		t.Fatalf("SigningPubKeyHex = %q, want unchanged %q", user.SigningPubKeyHex, boundHex)
	}
}

func TestReconcileSigningKey_ConflictAfterDatabaseTOFUBind_Rejected(t *testing.T) {
	// Mirrors the real flow: a config user starts with no signing key
	// (TOFU-pending); handler.go's post-verify bind path calls
	// UpdateSigningPubKey on first successful login. A later candidate
	// asserting a DIFFERENT key for the same xpub (e.g. surfaced by a
	// signed grant) must be rejected, not silently override the DB bind.
	firstLoginHex := newTestSigningKeyHex(t)
	laterConflictingHex := newTestSigningKeyHex(t)

	dir := t.TempDir()
	store, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{XPub: "xpub-tofu", Name: "TOFU User", TrustLevel: "standard"},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer store.Close()

	if err := store.UpdateSigningPubKey("xpub-tofu", firstLoginHex); err != nil {
		t.Fatalf("UpdateSigningPubKey (simulated first login): %v", err)
	}

	if err := store.ReconcileSigningKey("xpub-tofu", firstLoginHex); err != nil {
		t.Fatalf("ReconcileSigningKey() with the SAME key as the TOFU bind: error = %v, want nil", err)
	}

	err = store.ReconcileSigningKey("xpub-tofu", laterConflictingHex)
	if !errors.Is(err, ErrTOFUConflict) {
		t.Fatalf("ReconcileSigningKey() error = %v, want ErrTOFUConflict", err)
	}

	user, err := store.GetUser("xpub-tofu")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.SigningPubKeyHex != firstLoginHex {
		t.Fatalf("SigningPubKeyHex = %q, want unchanged (first-login-bound) %q", user.SigningPubKeyHex, firstLoginHex)
	}
}

func TestReconcileGrantSubjectKey_UsesGrantSubjectEmbeddedKey(t *testing.T) {
	// The grant's Subject peer ID embeds an Ed25519 public key (see
	// internal/peers/grants.go); ReconcileGrantSubjectKey must extract that
	// key and reconcile it exactly like ReconcileSigningKey.
	_, subjectPub, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	subjectID, err := peer.IDFromPublicKey(subjectPub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	subjectRaw, err := subjectPub.Raw()
	if err != nil {
		t.Fatalf("subjectPub.Raw: %v", err)
	}
	subjectHex := hex.EncodeToString(subjectRaw)

	granterPriv, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key (granter): %v", err)
	}
	grant, err := peers.SignGrant(granterPriv, peers.Grant{
		Subject: subjectID,
		Level:   peers.Trusted,
	})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}

	dir := t.TempDir()
	t.Run("matches_bound_key", func(t *testing.T) {
		store, err := NewUserStore(filepath.Join(dir, "match.db"), []config.UserEntry{
			{XPub: "xpub-match", Name: "Match", TrustLevel: "standard", SigningPubKeyHex: subjectHex},
		})
		if err != nil {
			t.Fatalf("NewUserStore: %v", err)
		}
		defer store.Close()

		if err := store.ReconcileGrantSubjectKey("xpub-match", grant); err != nil {
			t.Fatalf("ReconcileGrantSubjectKey() error = %v, want nil", err)
		}
	})

	t.Run("conflicts_with_bound_key", func(t *testing.T) {
		otherHex := newTestSigningKeyHex(t)
		store, err := NewUserStore(filepath.Join(dir, "conflict.db"), []config.UserEntry{
			{XPub: "xpub-conflict", Name: "Conflict", TrustLevel: "standard", SigningPubKeyHex: otherHex},
		})
		if err != nil {
			t.Fatalf("NewUserStore: %v", err)
		}
		defer store.Close()

		err = store.ReconcileGrantSubjectKey("xpub-conflict", grant)
		if !errors.Is(err, ErrTOFUConflict) {
			t.Fatalf("ReconcileGrantSubjectKey() error = %v, want ErrTOFUConflict", err)
		}
	})
}

// TestReconcileGrantSubjectKey_NonEd25519Subject_SkipsCleanly is the
// minor-fix regression test: SignGrant/Verify only require the GRANTER to
// be Ed25519, so a verified grant can legitimately name a non-Ed25519
// (e.g. secp256k1) subject. Since a TOFU-bound wallet signing key is
// always Ed25519, ReconcileGrantSubjectKey must recognize this case and
// skip cleanly (nil, nothing to reconcile) instead of erroring or
// misreporting ErrTOFUConflict against an existing, unrelated Ed25519
// binding.
func TestReconcileGrantSubjectKey_NonEd25519Subject_SkipsCleanly(t *testing.T) {
	secpPriv, secpPub, err := libp2pcrypto.GenerateKeyPair(libp2pcrypto.Secp256k1, 256)
	if err != nil {
		t.Fatalf("GenerateKeyPair (secp256k1 subject): %v", err)
	}
	_ = secpPriv
	subjectID, err := peer.IDFromPublicKey(secpPub)
	if err != nil {
		t.Fatalf("IDFromPublicKey (secp256k1 subject): %v", err)
	}

	granterPriv, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key (granter): %v", err)
	}
	grant, err := peers.SignGrant(granterPriv, peers.Grant{
		Subject: subjectID,
		Level:   peers.Trusted,
	})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}
	if err := grant.Verify(); err != nil {
		t.Fatalf("Verify: %v (a non-Ed25519 SUBJECT should not affect an Ed25519-granter grant's validity)", err)
	}

	boundHex := newTestSigningKeyHex(t)
	dir := t.TempDir()
	store, err := NewUserStore(filepath.Join(dir, "nonEd25519subject.db"), []config.UserEntry{
		{XPub: "xpub-nonEd25519-subject", Name: "NonEd25519Subject", TrustLevel: "standard", SigningPubKeyHex: boundHex},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer store.Close()

	if err := store.ReconcileGrantSubjectKey("xpub-nonEd25519-subject", grant); err != nil {
		t.Fatalf("ReconcileGrantSubjectKey() with non-Ed25519 subject: error = %v, want nil (skip cleanly)", err)
	}

	// The existing Ed25519 TOFU binding must be left untouched.
	user, err := store.GetUser("xpub-nonEd25519-subject")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.SigningPubKeyHex != boundHex {
		t.Fatalf("SigningPubKeyHex = %q, want unchanged %q", user.SigningPubKeyHex, boundHex)
	}
}
