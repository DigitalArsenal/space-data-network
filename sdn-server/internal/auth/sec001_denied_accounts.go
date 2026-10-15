package auth

import (
	"encoding/hex"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

// deniedAccount reports whether a stored or configured account belongs to a
// SEC-001 denylisted identity: its xpub or bound signing key derives from the
// published seed. Root sign-in persists the node's own account
// (RecordRootSignIn) at root trust, and operators enrolled the old root keys
// as admins, so after the re-key those rows would still sign in whoever holds
// the seed. Sign-in lookups (GetUser, GetUserBySigningPubKey) treat such an
// account as unknown: the challenge it gets can never be verified, and the row
// stays visible to `admin list` for an operator to remove (verifier r2).
func deniedAccount(u *User) bool {
	if u == nil {
		return false
	}
	if trustanchors.IsDenied(u.XPub) || trustanchors.IsDenied(u.SigningPubKeyHex) {
		log.Warnf("SEC-001: refusing sign-in for account %q: its xpub or signing key belongs to a denylisted identity; remove it with `spacedatanetwork admin remove`", u.Name)
		return true
	}
	return false
}

// presentsDeniedIdentity reports whether a sign-in presents a denylisted key
// or xpub. deniedAccount checks the STORED account; this checks what the
// client PRESENTS, before any lookup (verifier r3).
func presentsDeniedIdentity(pub []byte, xpub string) bool {
	return trustanchors.IsDeniedKey(pub) || trustanchors.IsDenied(strings.TrimSpace(xpub))
}

// RevokeDeniedGrants revokes, at startup and before anything is served, every
// stored session and delegation that belongs to a SEC-001 denylisted identity:
// sessions whose xpub is denylisted or whose account's bound signing key is,
// and delegations from a denylisted wallet. RevokeRootGrants (the root
// sign-in hotfix) revokes the CURRENT root's grants; after the re-key that is
// the new identity, so the OLD root's are revoked here (verifier r3).
func (h *Handler) RevokeDeniedGrants() {
	if h == nil {
		return
	}
	if h.sessions != nil && h.sessions.db != nil {
		var xpubs []string
		if rows, err := h.sessions.db.Query(`SELECT DISTINCT xpub FROM sessions WHERE revoked = 0`); err == nil {
			for rows.Next() {
				var x string
				if rows.Scan(&x) == nil && trustanchors.IsDenied(x) {
					xpubs = append(xpubs, x)
				}
			}
			rows.Close()
		}
		if h.userStore != nil {
			if users, err := h.userStore.ListUsers(); err == nil {
				for _, u := range users {
					if trustanchors.IsDenied(u.SigningPubKeyHex) {
						xpubs = append(xpubs, u.XPub)
					}
				}
			}
		}
		for _, x := range xpubs {
			if err := h.sessions.RevokeAllForUser(x); err != nil {
				log.Warnf("SEC-001: revoking the sessions of a denylisted identity failed: %v", err)
			}
		}
		if len(xpubs) > 0 {
			log.Infof("SEC-001: revoked the stored sessions of %d denylisted account(s)", len(xpubs))
		}
	}

	h.mu.Lock()
	var dropped [][]byte
	for k, d := range h.delegations {
		if trustanchors.IsDeniedKey(d.wallet) {
			dropped = append(dropped, []byte(k))
			delete(h.delegations, k)
		}
	}
	h.mu.Unlock()
	if h.userStore != nil && h.userStore.db != nil {
		h.userStore.mu.Lock()
		defer h.userStore.mu.Unlock()
		if rows, err := h.userStore.db.Query(`SELECT DISTINCT wallet_pub FROM session_delegations`); err == nil {
			var wallets []string
			for rows.Next() {
				var w string
				if rows.Scan(&w) == nil {
					if raw, derr := hex.DecodeString(w); derr == nil && trustanchors.IsDeniedKey(raw) {
						wallets = append(wallets, w)
					}
				}
			}
			rows.Close()
			for _, w := range wallets {
				if _, err := h.userStore.db.Exec(`DELETE FROM session_delegations WHERE wallet_pub = ?`, w); err != nil {
					log.Warnf("SEC-001: deleting a denylisted wallet's delegations failed: %v", err)
				}
			}
		}
	}
	if len(dropped) > 0 {
		log.Infof("SEC-001: revoked %d live delegation(s) from denylisted wallets", len(dropped))
	}
}
