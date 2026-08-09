package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/abac"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

type contextKey string

const sessionContextKey contextKey = "auth_session"

// RequireAuth wraps an http.HandlerFunc to require a valid session with minimum trust level.
// Redirects to /login for browser PAGE requests only; API paths (/api/...)
// ALWAYS get status-code JSON, never a redirect (gateway allowlist policy,
// sdn-server/docs/gateway-api.md §4 — this retires the "epoch endpoint
// answers 302" oddity: an anonymous browser GET of a gated API route now
// reads 401, not a login page).
//
// That JSON refusal is written through writeAuthRefusal (cors.go), which makes
// it READABLE to a cross-origin browser. The comment above says the split
// exists so an anonymous browser "reads 401, not a login page" — before that
// decoration the intent was defeated for every cross-origin caller, which is
// every embedded page and every gallery demo: they read neither.
func (h *Handler) RequireAuth(minTrust peers.TrustLevel, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := h.sessionFromRequest(r)
		if err != nil {
			if wantsJSON(r) || isAPIPath(r) {
				writeAuthRefusal(w, r, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "not authenticated"})
			} else {
				http.Redirect(w, r, loginPagePath(r.URL.RequestURI(), false), http.StatusFound)
			}
			return
		}

		if session.TrustLevel < minTrust {
			if wantsJSON(r) || isAPIPath(r) {
				writeAuthRefusal(w, r, http.StatusForbidden, errorResponse{Code: "forbidden", Message: "insufficient permissions"})
			} else {
				http.Redirect(w, r, loginPagePath(r.URL.RequestURI(), true), http.StatusFound)
			}
			return
		}

		session = h.maybeRefreshSessionCookie(w, r, session)

		// Store session in request context
		ctx := context.WithValue(r.Context(), sessionContextKey, session)
		next(w, r.WithContext(ctx))
	}
}

// RequireTrust enforces minTrust on r and reports whether the caller may
// proceed. When it returns false it has ALREADY written the same JSON failure
// shapes RequireAuth writes for API paths — 401 {"code":"unauthorized"} with no
// valid session, 403 {"code":"forbidden"} when the session is below minTrust.
//
// It exists for handlers that need a METHOD-granular gate (the canonical case:
// GET /api/node/epm is part of the anonymous read surface while PUT is an
// operator write), which the wrapper form RequireAuth cannot express because it
// gates a whole route. Unlike RequireAuth it never redirects and never rotates
// the session cookie, so it composes safely underneath the top-level auth wall
// (cmd/spacedatanetwork/main.go serveAdminMuxRequest) without emitting a second
// Set-Cookie or invalidating the token that wall just rotated.
//
// Fail-closed: a nil Handler admits nobody.
func (h *Handler) RequireTrust(w http.ResponseWriter, r *http.Request, minTrust peers.TrustLevel) bool {
	if h == nil {
		writeAuthRefusal(w, r, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "not authenticated"})
		return false
	}
	session, err := h.sessionFromRequest(r)
	if err != nil || session == nil {
		writeAuthRefusal(w, r, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "not authenticated"})
		return false
	}
	if session.TrustLevel < minTrust {
		writeAuthRefusal(w, r, http.StatusForbidden, errorResponse{Code: "forbidden", Message: "insufficient permissions"})
		return false
	}
	return true
}

// PolicyEngine is the interface used by RequirePolicy.  *abac.Engine satisfies it.
type PolicyEngine interface {
	Evaluate(sub abac.Subject, action abac.Action, res abac.Resource) abac.Decision
}

// RequirePolicy returns a middleware that applies an ABAC policy check AFTER
// the standard trust-level gate (defence in depth).  The caller supplies:
//   - action: the operation being attempted.
//   - resourceFn: a function that extracts the Resource from the request; called
//     only when a session is present and the engine is non-nil.
//
// When engine is nil the middleware is a transparent pass-through, preserving
// backward-compatibility when policies are disabled.
//
// Denials are logged as structured WARNING entries so that they appear in the
// audit trail even without a full audit.Logger dependency.
func (h *Handler) RequirePolicy(engine PolicyEngine, action abac.Action, resourceFn func(*http.Request) abac.Resource) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if engine == nil {
				next(w, r)
				return
			}

			session := SessionFromContext(r.Context())
			if session == nil {
				// No session in context — trust gate should have already rejected this,
				// but be defensive.
				writeAuthRefusal(w, r, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "not authenticated"})
				return
			}

			sub := abac.Subject{
				XPub:       session.XPub,
				TrustLevel: int(session.TrustLevel),
				Attrs:      map[string]string{},
			}

			res := resourceFn(r)
			decision := engine.Evaluate(sub, action, res)

			if !decision.Allowed {
				log.Warnw("abac policy denial",
					"xpub", session.XPub,
					"trust_level", session.TrustLevel,
					"action", action,
					"schema", res.Schema,
					"classification", res.Classification,
					"provider_id", res.ProviderID,
					"reason", decision.Reason,
					"rule_index", decision.RuleIndex,
					"remote_addr", r.RemoteAddr,
					"path", r.URL.Path,
				)
				writeAuthRefusal(w, r, http.StatusForbidden, errorResponse{Code: "policy_denied", Message: "access denied by policy: " + decision.Reason})
				return
			}

			next(w, r)
		}
	}
}

// OptionalAuth attaches the session to the request context if present, but does not require it.
func (h *Handler) OptionalAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := h.sessionFromRequest(r)
		if err == nil && session != nil {
			session = h.maybeRefreshSessionCookie(w, r, session)
			ctx := context.WithValue(r.Context(), sessionContextKey, session)
			r = r.WithContext(ctx)
		}
		next(w, r)
	}
}

// SessionFromContext retrieves the authenticated session from the request context.
func SessionFromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionContextKey).(*Session)
	return s
}

// ContextWithSession returns a context carrying an authenticated session.
// It is primarily useful for focused handler tests that bypass middleware.
func ContextWithSession(ctx context.Context, session *Session) context.Context {
	return context.WithValue(ctx, sessionContextKey, session)
}

// wantsJSON returns true if the request prefers JSON over HTML.
func wantsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json") ||
		r.Header.Get("Content-Type") == "application/json"
}

// isAPIPath reports whether the request targets the API surface. API
// clients (curl, fetch, data tooling) must receive status codes, never a
// login-page redirect, regardless of their Accept header.
func isAPIPath(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/")
}

func loginPagePath(next string, unauthorized bool) string {
	query := url.Values{}
	if safeNext := sanitizePostLoginPath(next); safeNext != "" {
		query.Set("next", safeNext)
	}
	if unauthorized {
		query.Set("unauthorized", "1")
	}
	if encoded := query.Encode(); encoded != "" {
		return "/login?" + encoded
	}
	return "/login"
}

func sanitizePostLoginPath(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}

	parsed, err := url.Parse(next)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return ""
	}

	switch {
	case parsed.Path == "/admin" || strings.HasPrefix(parsed.Path, "/admin/"):
		if parsed.Path == "/admin" {
			parsed.Path = "/admin/"
		}
		return parsed.RequestURI()
	case parsed.Path == "/webui" || strings.HasPrefix(parsed.Path, "/webui/"):
		if parsed.Path == "/webui" {
			parsed.Path = "/webui/"
		}
		return parsed.RequestURI()
	case parsed.Path == "/":
		return parsed.RequestURI()
	default:
		return ""
	}
}
