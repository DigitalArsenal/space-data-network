package auth

// POST /api/auth/attest — the live wire for F2's VerifyAttestation
// (attestation.go). A wallet user proves, per request, that they hold the
// Ed25519 signing key TOFU-bound to their xpub, by signing a canonical
// Attestation{XPub, Claim, IssuedAt, Nonce}. Verification is purely against
// the key already on file; it grants nothing and changes no trust level —
// the response just reports who signed and the trust level they already
// hold (same shape as /api/auth/me).
//
// Fail-closed shape: malformed input is 400 invalid_request; EVERY
// verification failure (unknown xpub, no key bound yet, bad signature) is
// the same opaque 401 attestation_failed, so the endpoint is not a
// membership oracle for registered xpubs. The distinct sentinel error is
// logged server-side for operators. IssuedAt must be within the handler's
// existing clockSkew window (the same +/- tolerance handleChallenge applies
// to its timestamp) — the package-level primitive deliberately leaves
// replay concerns to the caller, and for a live HTTP endpoint a freshness
// window is the minimum sane posture. Rate limited per IP and per xpub
// like handleChallenge.

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxAttestPerMinutePerIP   = 30
	maxAttestPerMinutePerXPub = 10
)

type attestRequest struct {
	XPub  string `json:"xpub"`
	Claim string `json:"claim"`
	// IssuedAt is RFC3339 (second precision). The signer MUST sign the
	// exact transmitted value — the canonical preimage is rebuilt from
	// this parsed field, so signing a sub-second timestamp and sending
	// its truncation fails verification by construction.
	IssuedAt     string `json:"issued_at"`
	NonceHex     string `json:"nonce_hex"`
	SignatureHex string `json:"signature_hex"`
}

func (h *Handler) handleAttest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req attestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "invalid JSON body"})
		return
	}

	req.XPub = strings.TrimSpace(req.XPub)
	req.Claim = strings.TrimSpace(req.Claim)
	if req.XPub == "" || req.Claim == "" || req.SignatureHex == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "xpub, claim and signature_hex are required"})
		return
	}
	if len(req.XPub) > 256 || len(req.Claim) > 1024 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "field too long"})
		return
	}

	now := time.Now().UTC()
	clientIP := clientIPForRequest(r)
	if !h.allowRateLimited("attest:ip:"+clientIP, maxAttestPerMinutePerIP, now) ||
		!h.allowRateLimited("attest:xpub:"+strings.ToLower(req.XPub), maxAttestPerMinutePerXPub, now) {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Code: "too_many_requests", Message: "rate limit exceeded"})
		return
	}

	issuedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(req.IssuedAt))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_timestamp", Message: "issued_at must be RFC3339"})
		return
	}
	if diff := now.Sub(issuedAt.UTC()); diff < -h.clockSkew || diff > h.clockSkew {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_timestamp", Message: "issued_at outside acceptance window"})
		return
	}

	nonce, err := hex.DecodeString(strings.TrimSpace(req.NonceHex))
	if err != nil || len(nonce) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "nonce_hex must be non-empty hex"})
		return
	}
	sig, err := hex.DecodeString(strings.TrimSpace(req.SignatureHex))
	if err != nil || len(sig) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "signature_hex must be non-empty hex"})
		return
	}

	att := Attestation{
		XPub:     req.XPub,
		Claim:    req.Claim,
		IssuedAt: issuedAt.UTC(),
		Nonce:    nonce,
	}
	user, err := VerifyAttestation(h.userStore, att, sig)
	if err != nil {
		// One opaque failure code on the wire (no membership oracle);
		// the distinct sentinel goes to the server log only.
		log.Warnf("Attestation verification failed for xpub %q claim %q: %v", req.XPub, req.Claim, err)
		writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "attestation_failed", Message: "attestation verification failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"verified":    true,
		"name":        user.Name,
		"trust_level": user.TrustLevel.String(),
	})
}
