package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// SESSION-KEY DELEGATION — how a browser signs every sealed ($RPC) command
// without asking the wallet each time.
//
// The wallet confirms each signature it makes, on purpose, so it cannot sign
// every command. At sign-in the browser makes a throwaway Ed25519 session key,
// and the wallet approves it once through the ordinary raw-challenge ceremony.
// What the wallet signs is not the node's challenge itself but
//
//	DelegationDigest = SHA-256("SDN-RPC-DELEGATION/v1" || challenge ||
//	                           sessionPub || be64(expiresAtMs))
//
// computed by the page. Because the page commits to its own session key, a
// party in the middle of a plain-HTTP connection cannot substitute its own:
// the node recomputes the digest from what it receives and the wallet's
// signature only matches the page's key. The challenge is the node's fresh,
// single-use one, so a delegation cannot be replayed.
//
// A delegation grants nothing by itself: every sealed command signed by the
// session key is admitted at whatever trust the delegating wallet holds at
// that moment, so demoting or removing an admin takes effect immediately.

// DelegationPrefix domain-separates a delegation digest from every other
// signature the wallet's sign-in key makes.
const DelegationPrefix = "SDN-RPC-DELEGATION/v1"

// MaxDelegation is the longest a session key may be delegated for.
const MaxDelegation = 12 * time.Hour

// DelegationDigest is the 32 bytes the wallet signs to delegate sessionPub.
func DelegationDigest(challenge []byte, sessionPub ed25519.PublicKey, expiresAtMs uint64) []byte {
	var expiry [8]byte
	binary.BigEndian.PutUint64(expiry[:], expiresAtMs)
	h := sha256.New()
	h.Write([]byte(DelegationPrefix))
	h.Write(challenge)
	h.Write(sessionPub)
	h.Write(expiry[:])
	return h.Sum(nil)
}

type delegation struct {
	wallet    ed25519.PublicKey
	root      bool
	expiresAt time.Time
}

type delegateRequest struct {
	ChallengeID      string `json:"challenge_id"`
	ClientPubKeyHex  string `json:"client_pubkey_hex"`
	SessionPubKeyHex string `json:"session_pubkey_hex"`
	ExpiresAtMs      uint64 `json:"expires_at_ms"`
	SignatureHex     string `json:"signature_hex"`
}

// handleDelegate is POST /api/auth/delegate: consume a pending challenge and
// record the wallet's delegation of a session key.
func (h *Handler) handleDelegate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req delegateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "invalid JSON body"})
		return
	}
	now := time.Now().UTC()
	clientPubHex := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.ClientPubKeyHex), "0x"))
	if !h.allowRateLimited("verify:ip:"+clientIPForRequest(r), maxVerifyPerMinutePerIP, now) ||
		!h.allowRateLimited("verify:pubkey:"+clientPubHex, maxVerifyPerMinutePerXPub, now) {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Code: "too_many_requests", Message: "rate limit exceeded"})
		return
	}
	walletPub, err1 := hex.DecodeString(clientPubHex)
	sessionPub, err2 := hex.DecodeString(strings.ToLower(strings.TrimSpace(req.SessionPubKeyHex)))
	signature, err3 := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(req.SignatureHex), "0x"))
	if err1 != nil || err2 != nil || err3 != nil || len(walletPub) != ed25519.PublicKeySize ||
		len(sessionPub) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "keys must be 32-byte and the signature 64-byte hex"})
		return
	}
	expiresAt := time.UnixMilli(int64(req.ExpiresAtMs)).UTC()
	if !expiresAt.After(now) || expiresAt.After(now.Add(MaxDelegation)) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_expiry", Message: "a delegation must expire within 12 hours"})
		return
	}

	h.cleanupChallenges(now)
	h.mu.Lock()
	pending, ok := h.challenges[strings.TrimSpace(req.ChallengeID)]
	if ok {
		delete(h.challenges, strings.TrimSpace(req.ChallengeID))
	}
	h.mu.Unlock()
	if !ok || pending.expiresAt.Before(now) || !bytes.Equal(pending.pubKey, walletPub) {
		h.writeAuthenticationFailure(w)
		return
	}
	digest := DelegationDigest(pending.challenge, sessionPub, req.ExpiresAtMs)
	if !ed25519.Verify(pending.pubKey, digest, signature) {
		h.writeAuthenticationFailure(w)
		return
	}

	h.mu.Lock()
	if h.delegations == nil {
		h.delegations = make(map[string]delegation)
	}
	for key, d := range h.delegations {
		if d.expiresAt.Before(now) {
			delete(h.delegations, key)
		}
	}
	h.delegations[string(sessionPub)] = delegation{
		wallet:    append(ed25519.PublicKey(nil), walletPub...),
		root:      pending.rootAdmin,
		expiresAt: expiresAt,
	}
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": "delegated", "expires_at_ms": req.ExpiresAtMs})
}

// DelegatedWallet returns the wallet key that delegated sessionPub, and
// whether that wallet is the node's own root, while the delegation is live.
func (h *Handler) DelegatedWallet(sessionPub ed25519.PublicKey) (ed25519.PublicKey, bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.delegations[string(sessionPub)]
	if !ok || !d.expiresAt.After(time.Now()) {
		return nil, false, false
	}
	return d.wallet, d.root, true
}

// RevokeDelegation ends sessionPub's delegation (sign-out).
func (h *Handler) RevokeDelegation(sessionPub ed25519.PublicKey) {
	h.mu.Lock()
	delete(h.delegations, string(sessionPub))
	h.mu.Unlock()
}
