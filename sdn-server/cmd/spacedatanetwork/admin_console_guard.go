package main

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/sealed"
)

// The admin listener's defences against the browser on the same machine
// (security audit 2026-10-15, SEC-012).
//
// A loopback listener is reachable by every web page the operator visits
// (no-cors POSTs need no permission) and, through DNS rebinding, readable by
// them. Before this change a cross-site text/plain POST from any page created
// a persistent admin user on a default desktop node: local_console admitted
// any loopback request, the CSRF check ran only when a session cookie was
// present, nothing validated Host, and handlers decoded JSON whatever the
// Content-Type said. The pieces here, with the console secret in
// internal/auth/console_gate.go, close each link:
//
//   - adminHostAllowList: with local_console or dev_auto_admin armed, the
//     listener answers only Host values that name it (127.0.0.1:<port>,
//     localhost:<port>, [::1]:<port>). A rebinding name gets 421.
//   - adminCSRFRefusal: every state-changing request to a non-public path is
//     refused when the browser says it is cross-site (Sec-Fetch-Site) or its
//     Origin is not this origin, with or without a cookie. A self-
//     authenticating request (sealed transport, an SDN-Signed Authorization
//     with no cookie) is exempt: it carries no ambient credential to forge.
//   - The CORS-safelisted body types (text/plain, form-urlencoded, none) are
//     the ones a cross-site page can send without a preflight. A request with
//     a body of one of those types must prove it is same-origin (a matching
//     Origin, or Sec-Fetch-Site: same-origin); otherwise 415. JSON endpoints
//     therefore need application/json from anything that is not the node's
//     own page.

// adminHostAllowList refuses any Host that does not name the loopback console
// listener. Installed only when an ambient console mode is armed.
func adminHostAllowList(listenAddr string, next http.Handler) (http.Handler, error) {
	hosts, err := auth.ConsoleHostsForListenAddr(listenAddr)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts[strings.ToLower(strings.TrimSpace(r.Host))] {
			http.Error(w, "misdirected request: this console answers only its own loopback address", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

// carriesAmbientCredential reports whether the browser could have attached a
// credential to r on its own: a session cookie or the console cookie.
func carriesAmbientCredential(r *http.Request) bool {
	return hasSessionCookie(r)
}

// isSelfAuthenticatingRequest reports a request whose credential the browser
// never attaches by itself, so a cross-origin page cannot forge it: the
// sealed admin transport (signed and encrypted per command), signed-request
// admission (an SDN-Signed Authorization over this exact request) and an
// explicit Bearer token (license tokens, the asset-pin workflow token). Basic
// and Digest are NOT on the list: browsers replay cached ones on their own.
// None counts when an ambient credential rides along.
func isSelfAuthenticatingRequest(r *http.Request) bool {
	if carriesAmbientCredential(r) {
		return false
	}
	if r.URL.Path == sealed.Route {
		return true
	}
	scheme, _, _ := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	return strings.EqualFold(scheme, auth.SignedRequestScheme) || strings.EqualFold(scheme, "Bearer")
}

// provenSameOrigin reports whether the browser vouched that r comes from this
// origin.
func provenSameOrigin(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "same-origin") {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	return origin != "" && isSameOrigin(r, origin)
}

// csrfSafelistedBody reports whether r carries a body whose Content-Type is
// one a cross-site page can send without a CORS preflight (or none at all).
func csrfSafelistedBody(r *http.Request) bool {
	if r.ContentLength == 0 || r.Body == nil || r.Body == http.NoBody {
		return false
	}
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return true
	}
	switch strings.ToLower(mediaType) {
	case "text/plain", "application/x-www-form-urlencoded":
		return true
	}
	return false
}

// adminCSRFRefusal returns a non-empty refusal for a state-changing request
// the CSRF rules reject, and the status to send it with.
func adminCSRFRefusal(r *http.Request, publicAPIRequest func(method, path string) bool) (int, string) {
	if !isStateChangingMethod(r.Method) || isWebhookPath(r.URL.Path) || publicAPIRequest(r.Method, r.URL.Path) {
		return 0, ""
	}
	if isSelfAuthenticatingRequest(r) {
		return 0, ""
	}

	// The browser's own word, whether or not a cookie is attached: an ambient
	// console admission carries no session cookie at all.
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return http.StatusForbidden, "CSRF validation failed (cross-site request)"
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" && !isSameOrigin(r, origin) {
		return http.StatusForbidden, "CSRF validation failed (origin mismatch)"
	}

	if carriesAmbientCredential(r) && origin == "" {
		referer := strings.TrimSpace(r.Header.Get("Referer"))
		xrw := strings.TrimSpace(r.Header.Get("X-Requested-With"))
		if referer != "" {
			if !isSameOrigin(r, referer) {
				return http.StatusForbidden, "CSRF validation failed (referer mismatch)"
			}
		} else if xrw == "" {
			return http.StatusForbidden, "CSRF validation failed (missing origin)"
		}
	}

	// A body a cross-site page could have sent without a preflight must come
	// from this origin. The loopback self-gated control routes carry their
	// own gate (loopback, no proxy, a one-time token) and are driven by local
	// tools, not pages.
	if csrfSafelistedBody(r) && !provenSameOrigin(r) &&
		!(isLoopbackSelfGatedAdminPath(r.URL.Path) && requestOriginatedOnThisBox(r)) {
		return http.StatusUnsupportedMediaType, "this endpoint requires Content-Type: application/json (or a same-origin request)"
	}
	return 0, ""
}

// consoleTokenFileName is where a daemon started without SDN_CONSOLE_TOKEN
// keeps the console secret it generated, next to the auth database.
const consoleTokenFileName = "console-token"

// installConsoleSecret arms the console gate for local_console and
// dev_auto_admin: the per-launch secret from the desktop shell
// (SDN_CONSOLE_TOKEN, removed from this process's environment so nothing the
// node spawns inherits it), or, with no shell, a fresh secret written 0600 to
// <auth db dir>/console-token. It also pins the console's Host values to the
// listener.
func installConsoleSecret(h *auth.Handler, listenAddr, authDBPath string) (tokenFile string, err error) {
	token := strings.TrimSpace(os.Getenv(auth.ConsoleTokenEnv))
	_ = os.Unsetenv(auth.ConsoleTokenEnv)
	if err := h.SetConsoleListenAddr(listenAddr); err != nil {
		return "", err
	}
	if token != "" {
		if err := h.SetConsoleToken(token); err != nil {
			return "", fmt.Errorf("%s: %w", auth.ConsoleTokenEnv, err)
		}
		return "", nil
	}
	token, err = auth.GenerateConsoleToken()
	if err != nil {
		return "", fmt.Errorf("generate console secret: %w", err)
	}
	dir := filepath.Dir(authDBPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("console secret directory: %w", err)
	}
	tokenFile = filepath.Join(dir, consoleTokenFileName)
	_ = os.Remove(tokenFile) // a stale file may carry looser permissions
	f, err := os.OpenFile(tokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("write console secret: %w", err)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write console secret: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write console secret: %w", err)
	}
	if err := h.SetConsoleToken(token); err != nil {
		return "", err
	}
	return tokenFile, nil
}
