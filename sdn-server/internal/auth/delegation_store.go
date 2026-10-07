package auth

// DELEGATIONS THAT OUTLIVE A RESTART (owner 2026-10-07: "I don't want to have
// to login every time"). The browser keeps its delegated session key, sealed
// under a key the page cannot export (sdn-js sealed-session-store.ts), so the
// node keeps the delegation too: auth.db holds every live one and NewHandler
// reloads them, so a restart or a fleet update signs nobody out.
//
// Only what the delegation proved is stored: this session key speaks for this
// wallet until this time. It still grants nothing by itself (delegation.go):
// each sealed command is admitted at the trust the wallet holds when the
// command arrives, and whether the wallet is the node's own root is decided
// then too, never read back from storage.

import (
	"crypto/ed25519"
	"encoding/hex"
	"time"
)

func (s *UserStore) initDelegations() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS session_delegations (
			session_pub TEXT PRIMARY KEY,
			wallet_pub TEXT NOT NULL,
			expires_at_ms INTEGER NOT NULL
		)
	`)
	return err
}

// saveDelegation records that sessionPub speaks for wallet until expiresAt.
func (s *UserStore) saveDelegation(sessionPub, wallet ed25519.PublicKey, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO session_delegations (session_pub, wallet_pub, expires_at_ms) VALUES (?, ?, ?)`,
		hex.EncodeToString(sessionPub), hex.EncodeToString(wallet), expiresAt.UnixMilli())
	return err
}

// deleteDelegation forgets sessionPub's delegation (sign-out).
func (s *UserStore) deleteDelegation(sessionPub ed25519.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM session_delegations WHERE session_pub = ?`, hex.EncodeToString(sessionPub))
	return err
}

// liveDelegations drops the expired rows and returns the rest, keyed like
// Handler.delegations.
func (s *UserStore) liveDelegations(now time.Time) (map[string]delegation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM session_delegations WHERE expires_at_ms <= ?`, now.UnixMilli()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT session_pub, wallet_pub, expires_at_ms FROM session_delegations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := make(map[string]delegation)
	for rows.Next() {
		var sessionHex, walletHex string
		var expiresAtMs int64
		if err := rows.Scan(&sessionHex, &walletHex, &expiresAtMs); err != nil {
			return nil, err
		}
		sessionPub, err1 := hex.DecodeString(sessionHex)
		wallet, err2 := hex.DecodeString(walletHex)
		if err1 != nil || err2 != nil || len(sessionPub) != ed25519.PublicKeySize || len(wallet) != ed25519.PublicKeySize {
			continue
		}
		live[string(sessionPub)] = delegation{wallet: ed25519.PublicKey(wallet), expiresAt: time.UnixMilli(expiresAtMs).UTC()}
	}
	return live, rows.Err()
}

// loadDelegations restores the delegations auth.db holds (NewHandler).
func (h *Handler) loadDelegations() {
	if h.userStore == nil {
		return
	}
	live, err := h.userStore.liveDelegations(time.Now().UTC())
	if err != nil {
		log.Warnf("Signed-in browsers not restored: %v", err)
		return
	}
	h.mu.Lock()
	h.delegations = live
	h.mu.Unlock()
	if len(live) > 0 {
		log.Infof("Restored %d signed-in browser session(s)", len(live))
	}
}

// recordDelegation keeps a verified delegation in memory and in auth.db.
func (h *Handler) recordDelegation(sessionPub, wallet ed25519.PublicKey, expiresAt, now time.Time) {
	h.mu.Lock()
	if h.delegations == nil {
		h.delegations = make(map[string]delegation)
	}
	for key, d := range h.delegations {
		if d.expiresAt.Before(now) {
			delete(h.delegations, key)
		}
	}
	h.delegations[string(sessionPub)] = delegation{wallet: append(ed25519.PublicKey(nil), wallet...), expiresAt: expiresAt}
	h.mu.Unlock()
	if h.userStore == nil {
		return
	}
	if err := h.userStore.saveDelegation(sessionPub, wallet, expiresAt); err != nil {
		log.Warnf("Session delegation kept in memory only, so a restart signs it out: %v", err)
	}
}
