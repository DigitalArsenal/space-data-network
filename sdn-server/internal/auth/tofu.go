package auth

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

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
func (s *UserStore) ReconcileGrantSubjectKey(xpub string, grant peers.SignedGrant) error {
	pub, err := grant.Subject.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("auth: reconcile grant subject key: extract public key: %w", err)
	}
	raw, err := pub.Raw()
	if err != nil {
		return fmt.Errorf("auth: reconcile grant subject key: %w", err)
	}
	return s.ReconcileSigningKey(xpub, hex.EncodeToString(raw))
}
