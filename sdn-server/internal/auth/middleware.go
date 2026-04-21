package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

type contextKey string

const sessionContextKey contextKey = "auth_session"

// RequireAuth wraps an http.HandlerFunc to require a valid session with minimum trust level.
// Redirects to /login for browser requests, returns 401 JSON for API requests.
func (h *Handler) RequireAuth(minTrust peers.TrustLevel, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := h.sessionFromRequest(r)
		if err != nil {
			if wantsJSON(r) {
				writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "not authenticated"})
			} else {
				http.Redirect(w, r, loginPagePath(r.URL.RequestURI(), false), http.StatusFound)
			}
			return
		}

		if session.TrustLevel < minTrust {
			if wantsJSON(r) {
				writeJSON(w, http.StatusForbidden, errorResponse{Code: "forbidden", Message: "insufficient permissions"})
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

// wantsJSON returns true if the request prefers JSON over HTML.
func wantsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json") ||
		r.Header.Get("Content-Type") == "application/json"
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
