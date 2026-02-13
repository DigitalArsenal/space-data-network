package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
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
}

type pendingChallenge struct {
	id        string
	xpub      string
	pubKey    ed25519.PublicKey
	challenge []byte
	createdAt time.Time
	expiresAt time.Time
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
	SessionToken string `json:"session_token"`
	User         User   `json:"user"`
	ExpiresAt    int64  `json:"expires_at"`
}

type addUserRequest struct {
	XPub       string `json:"xpub"`
	Name       string `json:"name"`
	TrustLevel string `json:"trust_level"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewHandler creates a new auth handler.
func NewHandler(userStore *UserStore, sessions *SessionStore, sessionTTL time.Duration, walletUIPath string) *Handler {
	return &Handler{
		userStore:    userStore,
		sessions:     sessions,
		challenges:   make(map[string]pendingChallenge),
		challengeTTL: 60 * time.Second,
		sessionTTL:   sessionTTL,
		clockSkew:    2 * time.Minute,
		walletUIPath: walletUIPath,
	}
}

// RegisterRoutes registers all auth routes on the provided mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/challenge", h.handleChallenge)
	mux.HandleFunc("/api/auth/verify", h.handleVerify)
	mux.HandleFunc("/api/auth/logout", h.handleLogout)
	mux.HandleFunc("/api/auth/me", h.handleMe)
	mux.HandleFunc("/api/auth/users", h.handleUsers)
	mux.HandleFunc("/api/auth/users/", h.handleUserByXPub)
	mux.HandleFunc("/login", h.handleLoginPage)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "invalid JSON body"})
		return
	}

	req.XPub = strings.TrimSpace(req.XPub)
	req.ClientPubKeyHex = strings.TrimPrefix(strings.TrimSpace(req.ClientPubKeyHex), "0x")

	if req.XPub == "" || req.ClientPubKeyHex == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "xpub and client_pubkey_hex are required"})
		return
	}

	// Validate timestamp within clock skew
	if req.TS != 0 {
		diff := time.Since(time.Unix(req.TS, 0))
		if diff < -h.clockSkew || diff > h.clockSkew {
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_timestamp", Message: "timestamp outside allowable skew"})
			return
		}
	}

	// Validate public key
	pubRaw, err := hex.DecodeString(req.ClientPubKeyHex)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_public_key", Message: "client_pubkey_hex must be 32-byte Ed25519 hex"})
		return
	}

	// Verify user exists
	user, err := h.userStore.GetUser(req.XPub)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "failed to look up user"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusForbidden, errorResponse{Code: "unknown_user", Message: "xpub not registered"})
		return
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

	now := time.Now().UTC()
	h.cleanupChallenges(now)

	h.mu.Lock()
	h.challenges[challengeID] = pendingChallenge{
		id:        challengeID,
		xpub:      req.XPub,
		pubKey:    append(ed25519.PublicKey(nil), pubRaw...),
		challenge: challengeBytes,
		createdAt: now,
		expiresAt: now.Add(h.challengeTTL),
	}
	h.mu.Unlock()

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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	now := time.Now().UTC()
	h.cleanupChallenges(now)

	// Look up and consume challenge (single-use)
	h.mu.Lock()
	pending, ok := h.challenges[req.ChallengeID]
	if ok {
		delete(h.challenges, req.ChallengeID)
	}
	h.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "challenge_not_found", Message: "challenge not found or expired"})
		return
	}
	if pending.expiresAt.Before(now) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "challenge_expired", Message: "challenge expired"})
		return
	}
	if pending.xpub != req.XPub {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "challenge_mismatch", Message: "challenge context mismatch"})
		return
	}
	if !bytes.Equal(pending.challenge, challengeRaw) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "challenge_mismatch", Message: "challenge bytes mismatch"})
		return
	}

	// Verify Ed25519 signature
	if !ed25519.Verify(pending.pubKey, challengeRaw, signature) {
		writeJSON(w, http.StatusForbidden, errorResponse{Code: "signature_invalid", Message: "signature verification failed"})
		return
	}

	// Look up user trust level
	user, err := h.userStore.GetUser(req.XPub)
	if err != nil || user == nil {
		writeJSON(w, http.StatusForbidden, errorResponse{Code: "unknown_user", Message: "xpub not registered"})
		return
	}

	// Create session
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.Split(fwd, ",")[0]
	}
	token, err := h.sessions.CreateSession(req.XPub, user.TrustLevel, strings.TrimSpace(ip), r.UserAgent(), h.sessionTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "failed to create session"})
		return
	}

	// Record login
	_ = h.userStore.RecordLogin(req.XPub)

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "sdn_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.sessionTTL.Seconds()),
	})

	log.Infof("User authenticated: %s (trust=%s) from %s", user.Name, user.TrustLevel, ip)

	writeJSON(w, http.StatusOK, verifyResponse{
		SessionToken: token,
		User:         *user,
		ExpiresAt:    time.Now().Add(h.sessionTTL).Unix(),
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("sdn_session")
	if err == nil {
		_ = h.sessions.RevokeSession(cookie.Value)
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "sdn_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
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
		if err := h.userStore.AddUser(req.XPub, req.Name, trust); err != nil {
			writeJSON(w, http.StatusConflict, errorResponse{Code: "user_exists", Message: err.Error()})
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
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// sessionFromRequest extracts and validates the session from a request cookie.
func (h *Handler) sessionFromRequest(r *http.Request) (*Session, error) {
	cookie, err := r.Cookie("sdn_session")
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

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
