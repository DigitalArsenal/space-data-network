package sealed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// Route is where the node accepts sealed commands.
const Route = "/api/rpc"

// ContentType marks a request or response body that is one $RPC envelope.
const ContentType = "application/x-sds-rpc"

const (
	// maxEnvelope bounds one sealed command.
	maxEnvelope = 16 << 20
	// clockSkew is how far a sealed command's timestamp may be from the
	// node's clock; the replay cache remembers nonces for twice this.
	clockSkew = 2 * time.Minute
)

type admittedKey struct{}

// Admitted reports whether a request is the unwrapped inner request of a
// verified sealed command from an admin.
func Admitted(ctx context.Context) bool {
	v, _ := ctx.Value(admittedKey{}).(bool)
	return v
}

// AdmittedContext is the context an unwrapped sealed command runs with: the
// admin's session, and the mark Admitted reads.
func AdmittedContext(ctx context.Context, session *auth.Session) context.Context {
	return context.WithValue(auth.ContextWithSession(ctx, session), admittedKey{}, true)
}

// Delegations is the session-key registry (auth/delegation.go).
type Delegations interface {
	DelegatedWallet(sessionPub ed25519.PublicKey) (wallet ed25519.PublicKey, root bool, ok bool)
	RevokeDelegation(sessionPub ed25519.PublicKey)
}

// RevokeRoute, sent sealed, ends the signing session key's delegation.
const RevokeRoute = "/api/auth/delegate/revoke"

// Admins resolves a sign-in key to its account. *auth.UserStore satisfies it.
type Admins interface {
	GetUserBySigningPubKey(signingPubKeyHex string) (*auth.User, error)
}

// Handler serves Route: it opens a sealed command, admits it only when the
// signer is an admin, runs the inner request through Next as that admin, and
// seals the response back to the reply key.
type Handler struct {
	// EncryptionPriv is the private half of the encryption key the node
	// advertises; requests are sealed to it.
	EncryptionPriv []byte
	// Signer signs every response.
	Signer ed25519.PrivateKey
	Admins Admins
	// RootKeys are the node's own sign-in keys, admitted as admin like the
	// ordinary sign-in admits them.
	RootKeys []ed25519.PublicKey
	// RootAccount is the account a root-key signer acts as (the node's own
	// account xpub, as root sign-in uses).
	RootAccount string
	// Delegations resolves a browser session key to the wallet that
	// delegated it at sign-in (auth.Handler satisfies it). May be nil.
	Delegations Delegations
	Next        http.Handler
	Now         func() time.Time

	mu     sync.Mutex
	nonces map[string]time.Time
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxEnvelope+1))
	if err != nil || len(raw) > maxEnvelope {
		http.Error(w, "sealed command too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	env, err := Open(raw)
	if err != nil || env.Response {
		// One opaque refusal for every verification failure, as sign-in does.
		http.Error(w, "sealed command refused", http.StatusBadRequest)
		return
	}
	if skew := h.now().Sub(env.Timestamp); skew > clockSkew || skew < -clockSkew {
		http.Error(w, "sealed command timestamp outside the allowed window", http.StatusBadRequest)
		return
	}
	trust, account := h.trustOf(env.Signer)
	if trust < peers.Admin {
		http.Error(w, "admin access required", http.StatusForbidden)
		return
	}
	if !h.claimNonce(env) {
		http.Error(w, "sealed command replayed", http.StatusConflict)
		return
	}
	body, err := env.Decrypt(h.EncryptionPriv)
	if err != nil {
		http.Error(w, "sealed command refused", http.StatusBadRequest)
		return
	}
	replyKX, ok := keyExchangeFor(body.ReplyKey)
	if !ok {
		http.Error(w, "sealed command has no usable reply key", http.StatusBadRequest)
		return
	}

	var status int
	var contentType string
	var out []byte
	if body.Route == RevokeRoute && h.Delegations != nil {
		h.Delegations.RevokeDelegation(env.Signer)
		status, contentType, out = http.StatusOK, "application/json", []byte(`{"status":"revoked"}`)
	} else {
		status, contentType, out = h.dispatch(r, body, account, trust)
	}
	reply, err := Seal(Body{
		Status:        uint16(status),
		Body:          out,
		BodyFileID:    contentType,
		RequestNonce:  env.Nonce,
		RequestDigest: Digest(env.Raw),
	}, SealOptions{
		Response:     true,
		RecipientPub: body.ReplyKey,
		KeyExchange:  replyKX,
		Signer:       h.Signer,
		SessionID:    env.SessionID,
		Now:          h.now(),
	})
	if err != nil {
		http.Error(w, "could not seal the response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(reply)
}

// dispatch runs the inner request through Next with the admin's session on
// its context, the same way the auth wall hands a session to a gated route.
func (h *Handler) dispatch(outer *http.Request, body Body, account string, trust peers.TrustLevel) (int, string, []byte) {
	method := strings.ToUpper(strings.TrimSpace(body.Method))
	if method == "" {
		method = http.MethodGet
	}
	route := body.Route
	if !strings.HasPrefix(route, "/") || strings.HasPrefix(route, Route) {
		return http.StatusBadRequest, "text/plain; charset=utf-8", []byte("sealed command has an invalid route")
	}
	session := &auth.Session{XPub: account, TrustLevel: trust, CreatedAt: h.now(), ExpiresAt: h.now()}
	ctx := AdmittedContext(context.WithoutCancel(outer.Context()), session)
	inner := httptest.NewRequest(method, route, bytes.NewReader(body.Body)).WithContext(ctx)
	inner.RemoteAddr = outer.RemoteAddr
	inner.Host = outer.Host
	// A command relayed by a proxy must still look remote once unwrapped.
	for _, name := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto", "Cf-Connecting-Ip"} {
		if v := outer.Header.Values(name); len(v) > 0 {
			inner.Header[name] = append([]string(nil), v...)
		}
	}
	inner.Header.Set("X-Requested-With", "XMLHttpRequest")
	if body.BodyFileID != "" {
		inner.Header.Set("Content-Type", body.BodyFileID)
	}
	rec := httptest.NewRecorder()
	h.Next.ServeHTTP(rec, inner)
	return rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes()
}

// trustOf is the signer's trust now: a sign-in key directly, or a delegated
// session key at its delegating wallet's current trust.
func (h *Handler) trustOf(signer ed25519.PublicKey) (peers.TrustLevel, string) {
	if h.Delegations != nil {
		if wallet, root, ok := h.Delegations.DelegatedWallet(signer); ok {
			if root {
				return peers.Admin, h.RootAccount
			}
			return h.keyTrust(wallet)
		}
	}
	return h.keyTrust(signer)
}

func (h *Handler) keyTrust(signer ed25519.PublicKey) (peers.TrustLevel, string) {
	for _, root := range h.RootKeys {
		if bytes.Equal(root, signer) {
			return peers.Admin, h.RootAccount
		}
	}
	if h.Admins == nil {
		return peers.Never, ""
	}
	user, err := h.Admins.GetUserBySigningPubKey(hex.EncodeToString(signer))
	if err != nil || user == nil {
		return peers.Never, ""
	}
	return user.TrustLevel, user.XPub
}

// claimNonce records signer+nonce, refusing one seen inside the window.
func (h *Handler) claimNonce(env Envelope) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if h.nonces == nil {
		h.nonces = make(map[string]time.Time)
	}
	for k, seen := range h.nonces {
		if now.Sub(seen) > 2*clockSkew {
			delete(h.nonces, k)
		}
	}
	key := string(env.Signer) + string(env.Nonce)
	if _, dup := h.nonces[key]; dup {
		return false
	}
	h.nonces[key] = now
	return true
}

func keyExchangeFor(pub []byte) (ecies.KeyExchange, bool) {
	switch len(pub) {
	case 32:
		return ecies.X25519, true
	case 33:
		return ecies.Secp256k1, true
	}
	return 0, false
}
