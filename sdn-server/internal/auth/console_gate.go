package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/netorigin"
)

// THE CONSOLE SECRET — what admits the node's own console (security audit
// 2026-10-15, SEC-012).
//
// admin.local_console and admin.dev_auto_admin used to admit any loopback
// request with no session as an Admin. Loopback is not a person: every web
// page the user visits can send a no-cors POST to 127.0.0.1, a DNS-rebinding
// page can READ the answers, and every other local process (including a
// module with the `http` capability) shares the interface. The reproduced
// result was a cross-site text/plain POST creating a persistent admin user.
//
// Both modes now also require a per-launch secret only the console holds:
//
//   - The desktop shell generates 32 random bytes per launch and hands them
//     to the daemon in the SDN_CONSOLE_TOKEN environment variable (the daemon
//     reads it once and removes it from its environment). The shell puts the
//     same value in an HttpOnly, SameSite=Strict cookie (ConsoleCookieName)
//     on the node's own origin in its window's session before it loads the
//     dashboard, so the browser presents it on every same-origin request.
//   - A daemon started without the variable generates its own secret and
//     writes it to a 0600 file; GET /api/auth/console?token=<secret> on the
//     console origin exchanges it for the same cookie.
//   - A non-browser local client may send it in the ConsoleTokenHeader header.
//
// And, defence in depth for the browser case, an ambient admission also needs:
//
//   - a loopback peer and no forwarding header (unchanged);
//   - a Host that is the console's own loopback address (no DNS rebinding);
//   - no cross-site signal: an Origin, when present, must be the console's
//     exact origin (scheme, host AND port: other localhost ports are other
//     origins), and Sec-Fetch-Site must not say cross-site or same-site.
//
// Without a configured secret neither mode admits anybody: fail closed.

const (
	// ConsoleTokenEnv is the environment variable the desktop shell uses to
	// hand the daemon its per-launch console secret.
	ConsoleTokenEnv = "SDN_CONSOLE_TOKEN"
	// ConsoleCookieName carries the console secret from the console's browser.
	ConsoleCookieName = "sdn_console"
	// ConsoleTokenHeader carries the console secret from a non-browser local
	// client.
	ConsoleTokenHeader = "X-SDN-Console-Token"
	// ConsoleBootstrapPath exchanges the secret (query parameter "token") for
	// the console cookie.
	ConsoleBootstrapPath = "/api/auth/console"
	// MinConsoleTokenLength is the shortest secret accepted (the shell sends
	// 43 base64url characters: 256 bits).
	MinConsoleTokenLength = 32
)

// ErrConsoleTokenTooShort refuses a secret that could be guessed.
var ErrConsoleTokenTooShort = fmt.Errorf("console token must be at least %d characters", MinConsoleTokenLength)

// GenerateConsoleToken returns a fresh 256-bit console secret.
func GenerateConsoleToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SetConsoleToken installs the per-launch console secret. Call once at startup,
// before the admin listener opens.
func (h *Handler) SetConsoleToken(token string) error {
	token = strings.TrimSpace(token)
	if len(token) < MinConsoleTokenLength {
		return ErrConsoleTokenTooShort
	}
	if strings.ContainsAny(token, " \t\r\n;,\"\\") {
		return errors.New("console token must be a single cookie-safe word")
	}
	h.consoleToken = token
	return nil
}

// SetConsoleListenAddr pins the Host values an ambient console request may
// carry to the admin listener's own loopback address: for 127.0.0.1:5001 that
// is "127.0.0.1:5001", "localhost:5001" and "[::1]:5001" (and nothing else,
// not even another port). Without it, any loopback IP or localhost Host is
// accepted, on any port.
func (h *Handler) SetConsoleListenAddr(listenAddr string) error {
	hosts, err := ConsoleHostsForListenAddr(listenAddr)
	if err != nil {
		return err
	}
	h.consoleHosts = hosts
	return nil
}

// ConsoleHostsForListenAddr returns the exact Host header values that name a
// loopback admin listener, lowercased.
func ConsoleHostsForListenAddr(listenAddr string) (map[string]bool, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return nil, fmt.Errorf("console listen address %q: %w", listenAddr, err)
	}
	if port == "" || port == "0" {
		return nil, fmt.Errorf("console listen address %q has no fixed port", listenAddr)
	}
	hosts := map[string]bool{}
	if strings.EqualFold(host, "localhost") {
		hosts["localhost:"+port] = true
		hosts["127.0.0.1:"+port] = true
		hosts["[::1]:"+port] = true
		return hosts, nil
	}
	ip := netorigin.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("console listen address %q is not loopback", listenAddr)
	}
	hosts[strings.ToLower(net.JoinHostPort(ip.String(), port))] = true
	hosts["localhost:"+port] = true
	if ip.To4() != nil {
		hosts["127.0.0.1:"+port] = true
	} else {
		hosts["[::1]:"+port] = true
	}
	return hosts, nil
}

// consoleHostAllowed reports whether r.Host names the console listener.
func (h *Handler) consoleHostAllowed(rawHost string) bool {
	hostport := strings.ToLower(strings.TrimSpace(rawHost))
	if hostport == "" {
		return false
	}
	if len(h.consoleHosts) > 0 {
		return h.consoleHosts[hostport]
	}
	host := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		host = hh
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requestIsCrossSite reports a browser's cross-site signal: Sec-Fetch-Site of
// cross-site or same-site (other ports of localhost are "same-site"), or an
// Origin (or, with no Origin, a Referer) that is not exactly http(s)://<Host>.
// An Origin of "null" (sandboxed frames, file: pages) is cross-site.
func requestIsCrossSite(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "cross-site", "same-site":
		return true
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return !originMatchesHost(origin, r.Host)
	}
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		return !originMatchesHost(referer, r.Host)
	}
	return false
}

// originMatchesHost reports whether raw (an Origin or Referer) is the origin
// http(s)://<host> exactly, port included (a missing port is the scheme's
// default on either side).
func originMatchesHost(raw, host string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	defaultPort := "80"
	if u.Scheme == "https" {
		defaultPort = "443"
	}
	normalize := func(hostport string) string {
		hostport = strings.ToLower(strings.TrimSpace(hostport))
		h, p, err := net.SplitHostPort(hostport)
		if err != nil {
			h, p = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"), defaultPort
		}
		return net.JoinHostPort(h, p)
	}
	return normalize(u.Host) == normalize(host)
}

// presentedConsoleToken returns the secret the request carries, if any.
func presentedConsoleToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get(ConsoleTokenHeader)); v != "" {
		return v
	}
	if c, err := r.Cookie(ConsoleCookieName); err == nil {
		return strings.TrimSpace(c.Value)
	}
	return ""
}

// consoleTokenMatches compares in constant time. An unset secret matches
// nothing.
func (h *Handler) consoleTokenMatches(presented string) bool {
	if h == nil || h.consoleToken == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(h.consoleToken)) == 1
}

// ambientConsoleRequestAllowed is the whole gate for the two ambient modes
// (local_console, dev_auto_admin). Every condition must hold.
func (h *Handler) ambientConsoleRequestAllowed(r *http.Request) bool {
	if h == nil || r == nil || h.consoleToken == "" {
		return false
	}
	if !netorigin.OriginatedOnThisBox(r) {
		return false
	}
	if !h.consoleHostAllowed(r.Host) {
		return false
	}
	if requestIsCrossSite(r) {
		return false
	}
	return h.consoleTokenMatches(presentedConsoleToken(r))
}

// consoleCookie is the cookie that carries the secret from the console's
// browser: host-only, HttpOnly, SameSite=Strict, session-lifetime (it dies
// with the browser, and the secret dies with the daemon).
func consoleCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     ConsoleCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// handleConsoleBootstrap exchanges the console secret for the console cookie:
// GET /api/auth/console?token=<secret> on the console's own origin, from this
// machine, then a redirect to the dashboard. Every refusal is the same 403 so
// the endpoint answers nothing about which check failed.
func (h *Handler) handleConsoleBootstrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.localConsole && !h.devAutoAdmin {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if !netorigin.OriginatedOnThisBox(r) || !h.consoleHostAllowed(r.Host) || requestIsCrossSite(r) || !h.consoleTokenMatches(token) {
		writeJSON(w, http.StatusForbidden, errorResponse{Code: "forbidden", Message: "console bootstrap refused"})
		return
	}
	http.SetCookie(w, consoleCookie(h.consoleToken, r.TLS != nil))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
