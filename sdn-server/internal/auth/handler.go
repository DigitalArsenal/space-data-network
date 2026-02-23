package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const (
	maxPendingChallenges         = 10000
	maxRateLimitEntries          = 50000
	authRateWindow               = time.Minute
	maxChallengePerMinutePerIP   = 60
	maxChallengePerMinutePerXPub = 30
	maxVerifyPerMinutePerIP      = 120
	maxVerifyPerMinutePerXPub    = 60
	maxXPubLength                = 256
)

// Handler serves HTTP authentication endpoints using Ed25519 challenge-response.
type Handler struct {
	userStore    *UserStore
	sessions     *SessionStore
	challenges   map[string]pendingChallenge
	mu           sync.Mutex
	challengeTTL time.Duration
	sessionTTL   time.Duration
	clockSkew    time.Duration
	walletUIPath string // filesystem path to hd-wallet-ui dist, or empty for CDN
	configPath   string // filesystem path to config.yaml for setup instructions
	nodeAttestations map[string]epm.IdentityAttestation
	attestMu    sync.RWMutex
	rateMu       sync.Mutex
	rates        map[string]rateEntry
}

type pendingChallenge struct {
	id        string
	xpub      string
	pubKey    ed25519.PublicKey
	challenge []byte
	createdAt time.Time
	expiresAt time.Time
}

type rateEntry struct {
	count       int
	windowStart time.Time
}

// challenge request/response types
type challengeRequest struct {
	XPub            string `json:"xpub"`
	ClientPubKeyHex string `json:"client_pubkey_hex"`
	TS              int64  `json:"ts"`
}

type challengeResponse struct {
	ChallengeID string `json:"challenge_id"`
	Challenge   string `json:"challenge"`
	ExpiresAt   int64  `json:"expires_at"`
}

type verifyRequest struct {
	ChallengeID     string `json:"challenge_id"`
	XPub            string `json:"xpub"`
	ClientPubKeyHex string `json:"client_pubkey_hex"`
	Challenge       string `json:"challenge"`
	SignatureHex    string `json:"signature_hex"`
}

type verifyResponse struct {
	User      User  `json:"user"`
	ExpiresAt int64 `json:"expires_at"`
}

type addUserRequest struct {
	XPub             string `json:"xpub"`
	Name             string `json:"name"`
	TrustLevel       string `json:"trust_level"`
	SigningPubKeyHex string `json:"signing_pubkey_hex"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewHandler creates a new auth handler.
func NewHandler(userStore *UserStore, sessions *SessionStore, sessionTTL time.Duration, walletUIPath, configPath string) *Handler {
	return &Handler{
		userStore:    userStore,
		sessions:     sessions,
		challenges:   make(map[string]pendingChallenge),
		challengeTTL: 60 * time.Second,
		sessionTTL:   sessionTTL,
		clockSkew:    2 * time.Minute,
		walletUIPath: walletUIPath,
		configPath:   configPath,
		nodeAttestations: make(map[string]epm.IdentityAttestation),
		rates:        make(map[string]rateEntry),
	}
}

// SetNodeSigningAttestation injects an identity-attestation chain for key binding.
// The attestation ties a Bitcoin-derived xpub to an Ed25519 signing public key.
func (h *Handler) SetNodeSigningAttestation(attestation *epm.IdentityAttestation) {
	if attestation == nil {
		return
	}
	if strings.TrimSpace(attestation.XPub) == "" {
		return
	}

	valid, err := attestation.Verify()
	if err != nil || !valid {
		log.Warnf("Ignoring invalid node signing attestation for %q: %v", strings.TrimSpace(attestation.XPub), err)
		return
	}

	h.attestMu.Lock()
	defer h.attestMu.Unlock()
	h.nodeAttestations[attestation.XPub] = *attestation
}

// RegisterRoutes registers all auth routes on the provided mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/challenge", h.handleChallenge)
	mux.HandleFunc("/api/auth/verify", h.handleVerify)
	mux.HandleFunc("/api/auth/logout", h.handleLogout)
	mux.HandleFunc("/api/auth/me", h.handleMe)
	mux.HandleFunc("/api/auth/status", h.handleAuthStatus)
	mux.HandleFunc("/api/auth/users", h.handleUsers)
	mux.HandleFunc("/api/auth/users/", h.handleUserByXPub)
	mux.HandleFunc("/login", h.handleLoginPage)
}

func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jsFile, cssFile := WalletAssets()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"admin_configured":     h.userStore.HasAdmin(),
		"users_configured":     h.userStore.UserCount() > 0,
		"config_path":          h.configPath,
		"wallet_ui_configured": strings.TrimSpace(h.walletUIPath) != "",
		"wallet_js_file":       jsFile,
		"wallet_css_file":      cssFile,
	})
}

// UserStore returns the underlying user store for external use.
func (h *Handler) UserStore() *UserStore {
	return h.userStore
}

// Sessions returns the underlying session store for external use.
func (h *Handler) Sessions() *SessionStore {
	return h.sessions
}

func (h *Handler) handleChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req challengeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "invalid JSON body"})
		return
	}

	req.XPub = strings.TrimSpace(req.XPub)
	req.ClientPubKeyHex = strings.TrimPrefix(strings.TrimSpace(req.ClientPubKeyHex), "0x")

	if req.XPub == "" || req.ClientPubKeyHex == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "xpub and client_pubkey_hex are required"})
		return
	}
	if len(req.XPub) > maxXPubLength {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "xpub too long"})
		return
	}
	now := time.Now().UTC()
	clientIP := clientIPForRequest(r)
	if !h.allowRateLimited("challenge:ip:"+clientIP, maxChallengePerMinutePerIP, now) ||
		!h.allowRateLimited("challenge:xpub:"+strings.ToLower(req.XPub), maxChallengePerMinutePerXPub, now) {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Code: "too_many_requests", Message: "rate limit exceeded"})
		return
	}

	// Validate timestamp (mandatory)
	if req.TS == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_timestamp", Message: "timestamp required"})
		return
	}
	diff := time.Since(time.Unix(req.TS, 0))
	if diff < -h.clockSkew || diff > h.clockSkew {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_timestamp", Message: "timestamp outside allowable skew"})
		return
	}

	// Validate public key
	pubRaw, err := hex.DecodeString(req.ClientPubKeyHex)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_public_key", Message: "client_pubkey_hex must be 32-byte Ed25519 hex"})
		return
	}

	// Check if user exists
	user, err := h.userStore.GetUser(req.XPub)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "failed to look up user"})
		return
	}

	// Store challenges for known users. If the user has a bound signing key,
	// only accept challenges where client_pubkey_hex matches. If the user has
	// no signing key yet (e.g. config has xpub only), accept the presented key
	// — it will be permanently bound on first successful verify (TOFU or attestation).
	// Unknown xpubs get a valid-looking response that can never be verified
	// (prevents user enumeration).
	shouldStore := false
	expectedPubKey := []byte(nil)
	if user != nil {
		normalized, nerr := normalizeEd25519PubKeyHex(user.SigningPubKeyHex)
		if nerr != nil || normalized == "" {
			if attested := h.getNodeAttestedSigningKey(req.XPub); len(attested) > 0 {
				if !bytes.Equal(attested, pubRaw) {
					log.Warnf("Auth challenge for xpub %q: client_pubkey_hex does not match node attestation", req.XPub)
				} else {
					shouldStore = true
					expectedPubKey = attested
					log.Infof("Auth challenge for xpub %q: using attested signing key", req.XPub)
				}
			} else {
				// TOFU: no signing key bound yet — accept the presented key.
				// The key will be permanently bound on first successful verify.
				shouldStore = true
				expectedPubKey = pubRaw
				log.Infof("Auth challenge for xpub %q: no signing key bound, TOFU mode", req.XPub)
			}
		} else {
			expRaw, derr := hex.DecodeString(normalized)
			if derr != nil || len(expRaw) != ed25519.PublicKeySize {
				log.Warnf("Auth challenge for xpub %q: failed to decode signing_pubkey_hex: %v", req.XPub, derr)
			} else if bytes.Equal(expRaw, pubRaw) {
				shouldStore = true
				expectedPubKey = expRaw
			} else {
				log.Warnf("Auth challenge for xpub %q: client_pubkey_hex mismatch", req.XPub)
			}
		}
	}

	// Generate challenge
	challengeBytes := make([]byte, 32)
	if _, err := rand.Read(challengeBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "failed to generate challenge"})
		return
	}

	// Generate challenge ID
	idBytes := make([]byte, 16)
	rand.Read(idBytes)
	challengeID := hex.EncodeToString(idBytes)

	h.cleanupChallenges(now)

	// Only store the challenge if the user is known; unknown xpubs get a
	// valid-looking response that can never be verified (prevents enumeration).
	if shouldStore {
		h.mu.Lock()
		if len(h.challenges) >= maxPendingChallenges {
			h.mu.Unlock()
			writeJSON(w, http.StatusTooManyRequests, errorResponse{Code: "too_many_requests", Message: "too many pending challenges"})
			return
		}
		h.challenges[challengeID] = pendingChallenge{
			id:        challengeID,
			xpub:      req.XPub,
			pubKey:    append(ed25519.PublicKey(nil), expectedPubKey...),
			challenge: challengeBytes,
			createdAt: now,
			expiresAt: now.Add(h.challengeTTL),
		}
		h.mu.Unlock()
	}

	writeJSON(w, http.StatusOK, challengeResponse{
		ChallengeID: challengeID,
		Challenge:   base64.RawStdEncoding.EncodeToString(challengeBytes),
		ExpiresAt:   now.Add(h.challengeTTL).Unix(),
	})
}

func (h *Handler) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req verifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "invalid JSON body"})
		return
	}

	req.ChallengeID = strings.TrimSpace(req.ChallengeID)
	req.XPub = strings.TrimSpace(req.XPub)
	req.ClientPubKeyHex = strings.TrimPrefix(strings.TrimSpace(req.ClientPubKeyHex), "0x")
	req.SignatureHex = strings.TrimPrefix(strings.TrimSpace(req.SignatureHex), "0x")
	req.Challenge = strings.TrimSpace(req.Challenge)

	if req.ChallengeID == "" || req.XPub == "" || req.ClientPubKeyHex == "" || req.SignatureHex == "" || req.Challenge == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "all fields are required"})
		return
	}
	if len(req.XPub) > maxXPubLength {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "xpub too long"})
		return
	}
	now := time.Now().UTC()
	clientIP := clientIPForRequest(r)
	if !h.allowRateLimited("verify:ip:"+clientIP, maxVerifyPerMinutePerIP, now) ||
		!h.allowRateLimited("verify:xpub:"+strings.ToLower(req.XPub), maxVerifyPerMinutePerXPub, now) {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Code: "too_many_requests", Message: "rate limit exceeded"})
		return
	}

	// Decode challenge and signature
	challengeRaw, err := base64.RawStdEncoding.DecodeString(req.Challenge)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_challenge", Message: "challenge must be base64"})
		return
	}
	signature, err := hex.DecodeString(req.SignatureHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_signature", Message: "signature_hex must be 64-byte Ed25519 signature hex"})
		return
	}

	h.cleanupChallenges(now)

	// Look up and consume challenge (single-use)
	h.mu.Lock()
	pending, ok := h.challenges[req.ChallengeID]
	if ok {
		delete(h.challenges, req.ChallengeID)
	}
	h.mu.Unlock()

	if !ok {
		h.writeAuthenticationFailure(w)
		return
	}
	if pending.expiresAt.Before(now) {
		h.writeAuthenticationFailure(w)
		return
	}
	if pending.xpub != req.XPub {
		h.writeAuthenticationFailure(w)
		return
	}
	if !bytes.Equal(pending.challenge, challengeRaw) {
		h.writeAuthenticationFailure(w)
		return
	}

	// Verify Ed25519 signature
	if !ed25519.Verify(pending.pubKey, challengeRaw, signature) {
		h.writeAuthenticationFailure(w)
		return
	}

	// Look up user trust level
	user, err := h.userStore.GetUser(req.XPub)
	if err != nil || user == nil {
		h.writeAuthenticationFailure(w)
		return
	}

	// TOFU/attestation: bind signing key on first successful authentication.
	if strings.TrimSpace(user.SigningPubKeyHex) == "" {
		attested := h.getNodeAttestedSigningKey(req.XPub)
		if len(attested) > 0 && !bytes.Equal(attested, pending.pubKey) {
			h.writeAuthenticationFailure(w)
			return
		}

		sigHex := hex.EncodeToString(pending.pubKey)
		if err := h.userStore.UpdateSigningPubKey(req.XPub, sigHex); err != nil {
			log.Warnf("failed to bind signing key for %q: %v", req.XPub, err)
		} else {
			log.Infof("Bound signing key %s for user %q", sigHex, user.Name)
			user.SigningPubKeyHex = sigHex
		}
	}

	// Create session
	ip := clientIPForRequest(r)
	token, err := h.sessions.CreateSession(req.XPub, user.TrustLevel, ip, r.UserAgent(), h.sessionTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "failed to create session"})
		return
	}

	// Record login
	_ = h.userStore.RecordLogin(req.XPub)

	// Detect TLS: direct TLS or behind a TLS-terminating reverse proxy.
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "sdn_wallet_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.sessionTTL.Seconds()),
	})

	log.Infof("User authenticated: %s (trust=%s) from %s", user.Name, user.TrustLevel, ip)

	writeJSON(w, http.StatusOK, verifyResponse{
		User:      *user,
		ExpiresAt: time.Now().Add(h.sessionTTL).Unix(),
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("sdn_wallet_session")
	if err == nil {
		_ = h.sessions.RevokeSession(cookie.Value)
	}

	// Detect TLS for Secure flag.
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	// Clear cookie with matching security flags.
	http.SetCookie(w, &http.Cookie{
		Name:     "sdn_wallet_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, err := h.sessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "not authenticated"})
		return
	}

	user, err := h.userStore.GetUser(session.XPub)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "user not found"})
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (h *Handler) handleUsers(w http.ResponseWriter, r *http.Request) {
	// All user management requires admin trust
	session, err := h.sessionFromRequest(r)
	if err != nil || session.TrustLevel < peers.Admin {
		writeJSON(w, http.StatusForbidden, errorResponse{Code: "forbidden", Message: "admin access required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		users, err := h.userStore.ListUsers()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, users)

	case http.MethodPost:
		var req addUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "invalid JSON body"})
			return
		}
		trust, err := peers.ParseTrustLevel(req.TrustLevel)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_trust_level", Message: err.Error()})
			return
		}
		if err := h.userStore.AddUser(req.XPub, req.Name, trust, req.SigningPubKeyHex); err != nil {
			msg := err.Error()
			lmsg := strings.ToLower(msg)
			switch {
			case strings.Contains(lmsg, "signing_pubkey_hex"):
				writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_signing_pubkey", Message: msg})
			case strings.Contains(lmsg, "unique") || strings.Contains(lmsg, "constraint"):
				writeJSON(w, http.StatusConflict, errorResponse{Code: "user_exists", Message: msg})
			default:
				writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: msg})
			}
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleUserByXPub(w http.ResponseWriter, r *http.Request) {
	// All user management requires admin trust
	session, err := h.sessionFromRequest(r)
	if err != nil || session.TrustLevel < peers.Admin {
		writeJSON(w, http.StatusForbidden, errorResponse{Code: "forbidden", Message: "admin access required"})
		return
	}

	// Extract xpub from URL path: /api/auth/users/{xpub}
	xpub := strings.TrimPrefix(r.URL.Path, "/api/auth/users/")
	if xpub == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "xpub required in path"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		user, err := h.userStore.GetUser(xpub)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: err.Error()})
			return
		}
		if user == nil {
			writeJSON(w, http.StatusNotFound, errorResponse{Code: "not_found", Message: "user not found"})
			return
		}
		writeJSON(w, http.StatusOK, user)

	case http.MethodDelete:
		if err := h.userStore.RemoveUser(xpub); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: "remove_failed", Message: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})

	case http.MethodPut:
		var req addUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "invalid JSON body"})
			return
		}
		trust, err := peers.ParseTrustLevel(req.TrustLevel)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_trust_level", Message: err.Error()})
			return
		}
		if err := h.userStore.UpdateTrust(xpub, trust); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: "update_failed", Message: err.Error()})
			return
		}
		if strings.TrimSpace(req.SigningPubKeyHex) != "" {
			if err := h.userStore.UpdateSigningPubKey(xpub, req.SigningPubKeyHex); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{Code: "update_failed", Message: err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// sessionFromRequest extracts and validates the session from a request cookie.
func (h *Handler) sessionFromRequest(r *http.Request) (*Session, error) {
	cookie, err := r.Cookie("sdn_wallet_session")
	if err != nil {
		return nil, fmt.Errorf("no session cookie")
	}
	return h.sessions.ValidateSession(cookie.Value)
}

func (h *Handler) cleanupChallenges(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, c := range h.challenges {
		if c.expiresAt.Before(now) {
			delete(h.challenges, id)
		}
	}
}

func (h *Handler) getNodeAttestedSigningKey(xpub string) []byte {
	if strings.TrimSpace(xpub) == "" {
		return nil
	}

	h.attestMu.RLock()
	defer h.attestMu.RUnlock()

	att, ok := h.nodeAttestations[xpub]
	if !ok {
		return nil
	}

	raw, err := hex.DecodeString(att.SigningPubKeyHex)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil
	}
	return raw
}

func (h *Handler) allowRateLimited(key string, limit int, now time.Time) bool {
	if limit <= 0 || key == "" {
		return false
	}

	h.rateMu.Lock()
	defer h.rateMu.Unlock()

	if len(h.rates) >= maxRateLimitEntries {
		h.compactRateLimits(now)
		if len(h.rates) >= maxRateLimitEntries {
			return false
		}
	}

	entry := h.rates[key]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= authRateWindow {
		h.rates[key] = rateEntry{
			count:       1,
			windowStart: now,
		}
		return true
	}

	if entry.count >= limit {
		return false
	}

	entry.count++
	h.rates[key] = entry
	return true
}

func (h *Handler) compactRateLimits(now time.Time) {
	for k, entry := range h.rates {
		if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= authRateWindow {
			delete(h.rates, k)
		}
	}
}

func clientIPForRequest(r *http.Request) string {
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	if remoteHost == "" {
		remoteHost = r.RemoteAddr
	}

	remoteIP := net.ParseIP(remoteHost)
	isTrustedProxy := remoteIP != nil && remoteIP.IsLoopback()
	if isTrustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				return strings.TrimSpace(parts[0])
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	return strings.TrimSpace(remoteHost)
}

func (h *Handler) writeAuthenticationFailure(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, errorResponse{Code: "authentication_failed", Message: "authentication failed"})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
