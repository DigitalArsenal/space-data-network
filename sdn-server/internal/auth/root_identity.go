package auth

// The node's OWN root account as the always-admitted admin.
//
// OWNER DIRECTIVE 2026-07-27, verbatim: "it needs to accept the root account
// that the node is based on as the admin sign-in, no error message".
//
// # Why this is not a backdoor
//
// The operator who holds the node's root mnemonic already controls the node
// completely: that seed IS the node's libp2p identity, its publishing key and
// its EPM signing key, and it sits on the same disk as the database this
// process writes. Requiring that operator to separately enrol themselves in the
// node's own user store protected nothing — it only produced the failure mode
// the owner is ruling out, where the person holding the node's keys is locked
// out of the node's console with an opaque 403.
//
// So the match here is NOT "a trusted key we were configured with". It is
// "this is provably the key derived from THIS node's own seed", computed by the
// node from its own mnemonic at startup. Nothing client-supplied participates:
// the presented public key is compared against locally derived material, and
// the identity the session is issued for is the node's OWN account xpub, never
// an xpub the client sent. That is what sidesteps the master-xpub storage
// problem of §13.1 entirely.
//
// # Fail-closed everywhere else
//
// Recognition is exact-match against a small set of locally derived Ed25519
// public keys, in constant time. A key that does not match gets exactly the
// behaviour it had before — including §13.1's refusal to bootstrap an admin
// from an arbitrary wallet's master xpub, which the owner did not relax. The
// root account is a carve-out, not a general relaxation.

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// RootIdentity is the node's own account, as derived from the node's own seed.
type RootIdentity struct {
	// XPub is the node's OWN account-level extended public key
	// (m/44'/0'/account' — depth 3). It is the identity a root session is
	// issued for, and it is node-derived, never client-supplied.
	XPub string

	// Name labels the resulting operator entry.
	Name string

	// SigningKeys are the Ed25519 public keys that PROVE possession of the
	// node's root seed. There is more than one because the wallet's identity
	// scheme decides which key signs:
	//
	//   - the SLIP-10 key at m/44'/0'/account'/0'/0' (internal/wasm
	//     SigningKeyPath) — the node's documented §2 key, and what the modern
	//     hd-wallet-ui identity and the v2 admit point use;
	//   - the bip32-scalar key at m/44'/0'/account'/0/0 (internal/wasm
	//     LegacyAuthKeyPath) — what hd-wallet-ui's legacy schemes present, and
	//     therefore the only one reachable through today's raw-32 admit point.
	//
	// Both are the same root account. Accepting both is what makes root
	// sign-in work today AND keep working after v2 lands.
	SigningKeys []ed25519.PublicKey
}

// rootIdentityState is the handler's live copy.
type rootIdentityState struct {
	mu       sync.RWMutex
	identity *RootIdentity
	keys     [][]byte
}

// SetNodeRootIdentity registers the node's own root account. Keys that are not
// well-formed Ed25519 public keys are dropped rather than accepted, so a
// derivation failure upstream can never widen the match set.
func (h *Handler) SetNodeRootIdentity(identity *RootIdentity) {
	if h == nil {
		return
	}
	h.root.mu.Lock()
	defer h.root.mu.Unlock()

	if identity == nil || strings.TrimSpace(identity.XPub) == "" {
		h.root.identity = nil
		h.root.keys = nil
		return
	}

	keys := make([][]byte, 0, len(identity.SigningKeys))
	for _, k := range identity.SigningKeys {
		if len(k) != ed25519.PublicKeySize {
			continue
		}
		keys = append(keys, append([]byte(nil), k...))
	}
	if len(keys) == 0 {
		// An identity with no usable proof key can never be matched; storing
		// it would only invite a future nil-key match bug.
		h.root.identity = nil
		h.root.keys = nil
		log.Warnf("Node root identity registered with no usable Ed25519 signing keys; root admin sign-in is disabled")
		return
	}

	stored := *identity
	stored.Name = strings.TrimSpace(stored.Name)
	if stored.Name == "" {
		stored.Name = "Node Root"
	}
	h.root.identity = &stored
	h.root.keys = keys

	fingerprints := make([]string, 0, len(keys))
	for _, k := range keys {
		fingerprints = append(fingerprints, hex.EncodeToString(k[:4]))
	}
	log.Infof("Node root identity registered: %d signing key(s) accepted as admin (%s)",
		len(keys), strings.Join(fingerprints, ", "))
}

// NodeRootIdentity returns the registered root identity, or nil.
func (h *Handler) NodeRootIdentity() *RootIdentity {
	if h == nil {
		return nil
	}
	h.root.mu.RLock()
	defer h.root.mu.RUnlock()
	if h.root.identity == nil {
		return nil
	}
	snapshot := *h.root.identity
	return &snapshot
}

// isNodeRootSigningKey reports whether pub is one of the node's own root keys.
// Comparison is constant-time and length-checked; an empty match set never
// matches.
func (h *Handler) isNodeRootSigningKey(pub []byte) bool {
	if h == nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	h.root.mu.RLock()
	defer h.root.mu.RUnlock()

	matched := false
	for _, candidate := range h.root.keys {
		if len(candidate) != len(pub) {
			continue
		}
		if subtle.ConstantTimeCompare(candidate, pub) == 1 {
			matched = true
		}
	}
	return matched
}

// rootSessionXPub returns the identity a root session is issued for: the
// node's OWN account xpub.
func (h *Handler) rootSessionXPub() string {
	if h == nil {
		return ""
	}
	h.root.mu.RLock()
	defer h.root.mu.RUnlock()
	if h.root.identity == nil {
		return ""
	}
	return h.root.identity.XPub
}

// rootTrustLevel is the tier a root sign-in is admitted at.
//
// peers.Admin, not peers.Ultimate, and deliberately so: Ultimate is reserved
// for "this identity IS the node's own peer identity" self-recognition in the
// peer registry (see the TrustLevel doc comments in internal/peers/trust.go and
// assignableTrustLevel's C7 ceiling in handler.go). The owner asked for "the
// admin sign-in"; admitting root at Admin gives it every operator power the
// console exposes without disturbing the reserved meaning of Ultimate.
const rootTrustLevel = peers.Admin

// completeRootSignIn issues the session for a verified root-account challenge.
//
// It deliberately does NOT require, create, or consult a user-store entry for
// authorization: the trust level comes from rootTrustLevel, not from a row that
// an operator could have edited or deleted. The store is still updated, but
// only as a RECORD of the sign-in (§14.2) — if that write fails the operator is
// still admitted, because losing the audit row must never lock the node's owner
// out of their own node.
func (h *Handler) completeRootSignIn(w http.ResponseWriter, r *http.Request, pending pendingChallenge) {
	ip := clientIPForRequest(r)
	name := "Node Root"
	if identity := h.NodeRootIdentity(); identity != nil && identity.Name != "" {
		name = identity.Name
	}

	// Record the sign-in in the trust store, best-effort.
	if h.userStore != nil {
		if err := h.userStore.RecordRootSignIn(pending.xpub, name, rootTrustLevel, hex.EncodeToString(pending.pubKey)); err != nil {
			log.Warnf("Failed to record root sign-in for the node's own account: %v", err)
		}
	}

	token, err := h.sessions.CreateSession(pending.xpub, rootTrustLevel, ip, r.UserAgent(), h.sessionTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "failed to create session"})
		return
	}

	h.setSessionCookie(w, r, token, time.Now().Add(h.sessionTTL))
	log.Infof("Node root account authenticated (trust=%s) from %s", rootTrustLevel, ip)

	writeJSON(w, http.StatusOK, verifyResponse{
		User: authSessionUser{
			XPubFingerprint: XPubFingerprint(pending.xpub),
			Name:            name,
			TrustLevel:      rootTrustLevel,
		},
		ExpiresAt: time.Now().Add(h.sessionTTL).Unix(),
	})
}
