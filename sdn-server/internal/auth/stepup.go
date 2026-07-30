package auth

// stepup.go — is THIS signing key the identity THIS session names?
//
// One exported predicate, for one caller: the node's daemon-lifecycle control
// surface (cmd/spacedatanetwork/node_service_api.go, graph task
// sdn-dashboard-wave3-service-lifecycle). The Seal Council (Hermes + Hephaestus,
// 2026-07-30) DISSENTED from gating a destructive host control on the session
// cookie alone — "a bearer credential darkening a live host" — and required a
// FRESH wallet signature over a single-use server nonce, from the same Admin
// identity, for every RESTART/STOP. CHECK, being a read, is exempt.
//
// A signature is only step-up authority if the key that made it is the key the
// session belongs to; otherwise any Admin session could be paired with any
// operator's signature. Session records carry the XPUB, not the Ed25519 signing
// key (sessions.go: Session.XPub), and the mapping from a signing key to an xpub
// is this package's own business — it has three legitimate forms, all already
// implemented for the admit point (handleChallenge):
//
//	1. the node's OWN root signing key (root_identity.go), whose session xpub is
//	   the node's own — this is how the owner signs in ("Node Root · ADMIN");
//	2. a key the node has ATTESTED for an xpub (getNodeAttestedXPubBySigningKey);
//	3. an ENROLLED operator whose user record carries the signing key
//	   (UserStore.GetUserBySigningPubKey).
//
// This file adds no fourth form and no new trust: it exposes the SAME three the
// admit point already accepts, so a key that cannot sign in cannot step up
// either. Fail-closed by construction — every path that cannot prove the binding
// returns false.

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// SigningKeyAuthorizesSession reports whether pubKey is the Ed25519 signing key
// of the identity that session was established for.
//
// It answers a question about IDENTITY only. It says nothing about trust tier,
// about whether the session is still valid, or about whether the operation is
// permitted — the caller has already passed the auth wall's Admin gate
// (serveAdminMuxRequest -> isAdminOnlyAPIPath) and owns those decisions.
//
// Safe on a nil Handler and a nil session (both false).
func (h *Handler) SigningKeyAuthorizesSession(pubKey []byte, session *Session) bool {
	if h == nil || session == nil {
		return false
	}
	if len(pubKey) != ed25519.PublicKeySize {
		return false
	}
	sessionXPub := strings.TrimSpace(session.XPub)
	if sessionXPub == "" {
		return false
	}

	// (1) The node's own root key. Compared against the xpub the root sign-in
	// path itself puts on the session, so a root key cannot step up into some
	// other operator's session.
	if h.isNodeRootSigningKey(pubKey) {
		if root := h.rootSessionXPub(); root != "" && xpubEqual(root, sessionXPub) {
			return true
		}
	}

	// (2) A key this node has attested for an xpub.
	if attested := h.getNodeAttestedXPubBySigningKey(pubKey); attested != "" {
		if xpubEqual(attested, sessionXPub) {
			return true
		}
	}

	// (3) An enrolled operator record carrying this signing key. A lookup error
	// is a refusal, never a pass: an unreadable user store must not widen
	// authority.
	if h.userStore != nil {
		user, err := h.userStore.GetUserBySigningPubKey(hex.EncodeToString(pubKey))
		if err == nil && user != nil && xpubEqual(user.XPub, sessionXPub) {
			return true
		}
	}

	return false
}

// xpubEqual compares two xpubs in constant time. Extended public keys are not
// secrets, but this comparison decides whether a destructive host control fires,
// and a length-leaking compare on an authorization path is a habit worth not
// having.
func xpubEqual(a, b string) bool {
	x := strings.TrimSpace(a)
	y := strings.TrimSpace(b)
	if len(x) != len(y) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(x), []byte(y)) == 1
}
