package auth

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// ErrTOFUConflict is returned when a candidate Ed25519 signing public key
// conflicts with a signing key already bound (trust-on-first-use — see the
// User.SigningPubKeyHex doc comment, and the post-verify bind in
// handler.go around "TOFU/attestation: bind signing key on first successful
// authentication") for the same xpub-identified user.
//
// # Precedence (task C4 step 6)
//
// TOFU is first-use-wins: the key bound on a user's first successful
// wallet login (or an explicit administrative UpdateSigningPubKey call) is
// authoritative for that identity. Nothing else — in particular, a
// node-key-signed permission grant (internal/peers.SignedGrant) that
// happens to assert something implying a DIFFERENT key for what would be
// the same logical identity — is allowed to silently replace it. Conflicting
// input MUST be rejected (and logged by the caller); ReconcileSigningKey /
// ReconcileGrantSubjectKey below never mutate the store themselves, so the
// existing binding always wins by construction.
var ErrTOFUConflict = errors.New("auth: candidate signing key conflicts with existing TOFU-bound key")

// ReconcileSigningKey enforces TOFU precedence for xpub against a
// candidateHex Ed25519 signing public key asserted by some external source
// (for example, a verified internal/peers.SignedGrant naming this
// identity — see ReconcileGrantSubjectKey). It never mutates the store:
//
//   - No user record exists yet for xpub: nothing to conflict with yet,
//     returns nil. (This does NOT bind anything on its own — only a real
//     wallet login's TOFU bind, or an explicit UpdateSigningPubKey call,
//     ever writes a key.)
//   - The user exists but has no signing key bound yet: nothing to conflict
//     with, returns nil. A candidate observed here is NOT auto-bound by
//     this function — binding only happens through the real login path or
//     an explicit administrative action, never as a side effect of
//     reconciling a third-party assertion.
//   - The user's bound key matches candidateHex (case-insensitive,
//     "0x"-tolerant): consistent, returns nil.
//   - The user's bound key differs from candidateHex: the existing TOFU
//     binding wins. Returns ErrTOFUConflict; the store is left untouched.
func (s *UserStore) ReconcileSigningKey(xpub, candidateHex string) error {
	user, err := s.GetUser(xpub)
	if err != nil {
		return fmt.Errorf("auth: reconcile signing key: %w", err)
	}
	if user == nil {
		return nil
	}

	bound, err := normalizeEd25519PubKeyHex(user.SigningPubKeyHex)
	if err != nil || bound == "" {
		// No usable key bound yet (empty, or an unexpectedly malformed
		// stored value) — nothing to conflict with.
		return nil
	}

	candidate, err := normalizeEd25519PubKeyHex(candidateHex)
	if err != nil {
		return fmt.Errorf("auth: reconcile signing key: invalid candidate: %w", err)
	}
	if candidate == "" {
		return nil
	}

	if !strings.EqualFold(bound, candidate) {
		return ErrTOFUConflict
	}
	return nil
}

// ReconcileGrantSubjectKey applies TOFU precedence (see ReconcileSigningKey)
// to a verified internal/peers.SignedGrant that is being used to associate
// an xpub-identified wallet user with a peer identity. It extracts the
// Ed25519 public key embedded in grant.Subject's peer ID (see
// peer.ID.ExtractPublicKey — recoverable with no other input for Ed25519
// libp2p identities, which is what internal/peers/grants.go requires of
// every SignedGrant subject/granter) and reconciles it against xpub's
// existing TOFU binding exactly like ReconcileSigningKey.
//
// Callers MUST call grant.Verify()/VerifyAt() themselves first — this
// function only performs the TOFU precedence check, not signature
// verification.
//
// # Ed25519-subject guard
//
// internal/peers/grants.go's SignGrant/Verify require the GRANTER to embed
// an Ed25519 public key, but place no such requirement on the Subject — a
// grant naming a non-Ed25519 subject (e.g. a secp256k1 libp2p identity,
// whose raw public key is 33 bytes) verifies just fine. A TOFU-bound
// wallet signing key, however, is ALWAYS Ed25519 (32 bytes) by
// construction (see normalizeEd25519PubKeyHex). Without a matching guard
// here, a non-Ed25519 subject's raw key bytes can never legitimately equal
// the bound Ed25519 key, so this function would (depending on incidental
// byte length) either misreport ErrTOFUConflict or fail with a confusing
// "invalid candidate" error for a case that isn't actually a conflict at
// all — it is simply not the kind of key TOFU binding ever deals with.
// Mirror SignGrant/Verify's own Ed25519 guard and skip cleanly (nil, "
// nothing to reconcile") for any non-Ed25519 subject instead.
func (s *UserStore) ReconcileGrantSubjectKey(xpub string, grant peers.SignedGrant) error {
	pub, err := grant.Subject.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("auth: reconcile grant subject key: extract public key: %w", err)
	}
	if _, ok := pub.(*libp2pcrypto.Ed25519PublicKey); !ok {
		// Not an Ed25519 identity: cannot correspond to any Ed25519
		// TOFU-bound wallet signing key. Nothing to reconcile.
		return nil
	}
	raw, err := pub.Raw()
	if err != nil {
		return fmt.Errorf("auth: reconcile grant subject key: %w", err)
	}
	return s.ReconcileSigningKey(xpub, hex.EncodeToString(raw))
}
