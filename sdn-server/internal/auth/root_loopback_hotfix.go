package auth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// SEC-001 containment hotfix (host-01, owner-approved 2026-10-15).
//
// The node's root admin keys are derived from its own seed, and host-01's seed
// is public. Until host-01 is re-keyed, a root sign-in is admitted only from
// the node's own box: RemoteAddr must be loopback and the request must show no
// sign of a proxy hop (the same rule localConsoleSession applies). The public
// listener, and the sealed /api/rpc transport (whose inner request keeps the
// outer RemoteAddr), never see a loopback peer, so whoever holds the published
// seed cannot become root over the network.
//
// At registration, every stored session and delegation the root account holds
// is revoked, so anything minted before this binary does not outlive it.

// RootPeerIsLocal reports whether r came from this box with no proxy hop; the
// sealed /api/rpc transport applies the same rule to root signers.
func RootPeerIsLocal(r *http.Request) bool { return rootPeerIsLocal(r) }

// rootPeerIsLocal reports whether r came from this box with no proxy hop.
func rootPeerIsLocal(r *http.Request) bool {
	if r == nil || requestCrossedAProxy(r) {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

// RevokeRootGrants revokes the registered root account's stored sessions and
// delegations. Call it right after SetNodeRootIdentity, before serving.
func (h *Handler) RevokeRootGrants() {
	if h == nil {
		return
	}
	id := h.NodeRootIdentity()
	if id == nil {
		return
	}
	h.root.mu.RLock()
	keys := make([][]byte, 0, len(h.root.keys))
	for _, k := range h.root.keys {
		keys = append(keys, append([]byte(nil), k...))
	}
	h.root.mu.RUnlock()
	h.revokeRootGrants(id.XPub, keys)
}

// revokeRootGrants drops the root account's stored sessions and every
// delegation whose wallet is one of the root keys.
func (h *Handler) revokeRootGrants(xpub string, keys [][]byte) {
	if h.sessions != nil && strings.TrimSpace(xpub) != "" {
		if err := h.sessions.RevokeAllForUser(xpub); err != nil {
			log.Warnf("SEC-001 hotfix: revoking the root account's sessions failed: %v", err)
		} else {
			log.Infof("SEC-001 hotfix: the root account's stored sessions are revoked")
		}
	}

	h.mu.Lock()
	var dropped []ed25519.PublicKey
	for k, d := range h.delegations {
		for _, rk := range keys {
			if bytes.Equal(d.wallet, rk) {
				dropped = append(dropped, ed25519.PublicKey([]byte(k)))
				delete(h.delegations, k)
				break
			}
		}
	}
	h.mu.Unlock()

	if h.userStore != nil && h.userStore.db != nil {
		h.userStore.mu.Lock()
		defer h.userStore.mu.Unlock()
		for _, rk := range keys {
			if _, err := h.userStore.db.Exec(`DELETE FROM session_delegations WHERE wallet_pub = ?`, hex.EncodeToString(rk)); err != nil {
				log.Warnf("SEC-001 hotfix: deleting the root wallet's stored delegations failed: %v", err)
			}
		}
	}
	log.Infof("SEC-001 hotfix: %d live root-wallet delegation(s) revoked; root sign-in is loopback-only", len(dropped))
}
