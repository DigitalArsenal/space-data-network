package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

// consoleChain is desktopConsoleChain with the console secret installed the
// way the daemon installs it (installConsoleSecret, from SDN_CONSOLE_TOKEN)
// and the Host allow-list in front, exactly as runDaemon composes them.
func consoleChain(t *testing.T) (srv *httptest.Server, users *auth.UserStore, port, token string) {
	t.Helper()
	dir := t.TempDir()
	users, err := auth.NewUserStore(filepath.Join(dir, "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = users.Close() })
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })
	sessions, err := auth.NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	h := auth.NewHandler(users, sessions, 24*time.Hour, "", "")
	pub, _, _ := ed25519.GenerateKey(nil)
	h.SetNodeRootIdentity(&auth.RootIdentity{XPub: "xpub-node-root", SigningKeys: []ed25519.PublicKey{pub}})
	h.EnableLocalConsole()

	adminMux := http.NewServeMux()
	h.RegisterRoutes(adminMux)
	wall := func(w http.ResponseWriter, r *http.Request) {
		serveAdminMuxRequest(w, r, adminMux, true, false, h, isPublicAPIRequest, nil, true)
	}
	secured := adminSecurityMiddleware(http.HandlerFunc(wall), "", isPublicAPIRequest)

	// The listener's address is only known once it exists: start unstarted,
	// install the secret for that address, then serve.
	srv = httptest.NewUnstartedServer(nil)
	addr := srv.Listener.Addr().String()
	port = strings.TrimPrefix(addr, "127.0.0.1:")
	token, err = auth.GenerateConsoleToken()
	if err != nil {
		t.Fatalf("GenerateConsoleToken: %v", err)
	}
	t.Setenv(auth.ConsoleTokenEnv, token)
	if file, err := installConsoleSecret(h, addr, filepath.Join(dir, "auth.db")); err != nil || file != "" {
		t.Fatalf("installConsoleSecret = %q, %v; want the shell's secret, no file", file, err)
	}
	if v, ok := os.LookupEnv(auth.ConsoleTokenEnv); ok {
		t.Fatalf("%s still in the environment (%d bytes): processes the node starts would inherit it", auth.ConsoleTokenEnv, len(v))
	}
	guarded, err := adminHostAllowList(addr, newAdminUpgradeRouter(nil, secured))
	if err != nil {
		t.Fatalf("adminHostAllowList: %v", err)
	}
	srv.Config.Handler = guarded
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, users, port, token
}

func TestConsoleSecretAdmitsTheConsoleOnly(t *testing.T) {
	srv, users, port, token := consoleChain(t)
	host := "127.0.0.1:" + port
	cookie := auth.ConsoleCookieName + "=" + token

	// The console's own page: cookie, its own Origin.
	status, body := doRequest(t, srv, http.MethodGet, "/api/auth/me", host, map[string]string{"Cookie": cookie}, "")
	if status != http.StatusOK || !strings.Contains(body, `"admin"`) {
		t.Fatalf("console GET /api/auth/me = %d %s", status, body)
	}
	status, body = doRequest(t, srv, http.MethodPost, "/api/auth/users", host, map[string]string{
		"Cookie":         cookie,
		"Origin":         "http://" + host,
		"Sec-Fetch-Site": "same-origin",
		"Content-Type":   "application/json",
	}, `{"xpub":"xpub-console-added","name":"added","trust_level":"standard","signing_pubkey_hex":"`+strings.Repeat("ab", 32)+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("console POST /api/auth/users = %d %s", status, body)
	}
	if u, _ := users.GetUser("xpub-console-added"); u == nil {
		t.Fatalf("console POST did not create the user")
	}

	// A local tool holding the secret uses the header.
	status, body = doRequest(t, srv, http.MethodGet, "/api/auth/users", host, map[string]string{auth.ConsoleTokenHeader: token}, "")
	if status != http.StatusOK {
		t.Fatalf("header-token GET /api/auth/users = %d %s", status, body)
	}

	// The wrong secret, or the right one with a cross-site signal, admits nobody.
	for name, headers := range map[string]map[string]string{
		"wrong secret":           {"Cookie": auth.ConsoleCookieName + "=" + strings.Repeat("x", 43)},
		"cross-site fetch":       {"Cookie": cookie, "Sec-Fetch-Site": "cross-site"},
		"same-site other port":   {"Cookie": cookie, "Sec-Fetch-Site": "same-site"},
		"foreign origin":         {"Cookie": cookie, "Origin": "https://evil.example"},
		"other localhost origin": {"Cookie": cookie, "Origin": "http://127.0.0.1:8000"},
		"proxy hop":              {"Cookie": cookie, "X-Forwarded-For": "203.0.113.9"},
	} {
		status, body := doRequest(t, srv, http.MethodGet, "/api/auth/users", host, headers, "")
		if status == http.StatusOK {
			t.Fatalf("%s: admitted (%d %s)", name, status, body)
		}
	}
}

func TestConsoleHostAllowListStopsDNSRebinding(t *testing.T) {
	srv, _, port, token := consoleChain(t)
	cookie := auth.ConsoleCookieName + "=" + token
	for _, host := range []string{"rebind.evil.example:" + port, "127.0.0.1:1", "localhost.evil.example:" + port} {
		status, _ := doRequest(t, srv, http.MethodGet, "/api/auth/users", host, map[string]string{"Cookie": cookie}, "")
		if status != http.StatusMisdirectedRequest {
			t.Fatalf("Host %q: status %d, want 421", host, status)
		}
	}
	for _, host := range []string{"127.0.0.1:" + port, "localhost:" + port, "LOCALHOST:" + port} {
		status, body := doRequest(t, srv, http.MethodGet, "/api/auth/users", host, map[string]string{"Cookie": cookie}, "")
		if status != http.StatusOK {
			t.Fatalf("Host %q: status %d %s, want 200", host, status, body)
		}
	}
}

func TestConsoleBootstrapExchangesTheSecretForTheCookie(t *testing.T) {
	srv, _, port, token := consoleChain(t)
	host := "127.0.0.1:" + port
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	get := func(query string, headers map[string]string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+auth.ConsoleBootstrapPath+query, nil)
		req.Host = host
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		resp.Body.Close()
		return resp
	}

	resp := get("?token="+token, map[string]string{"Sec-Fetch-Site": "none"})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bootstrap with the secret = %d, want 303", resp.StatusCode)
	}
	var got *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.ConsoleCookieName {
			got = c
		}
	}
	if got == nil || got.Value != token || !got.HttpOnly || got.SameSite != http.SameSiteStrictMode {
		t.Fatalf("bootstrap cookie = %+v, want HttpOnly SameSite=Strict carrying the secret", got)
	}

	for name, tc := range map[string]struct {
		query   string
		headers map[string]string
	}{
		"no secret":             {"", nil},
		"wrong secret":          {"?token=" + strings.Repeat("y", 43), nil},
		"cross-site navigation": {"?token=" + token, map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"through a proxy":       {"?token=" + token, map[string]string{"Forwarded": "for=203.0.113.9"}},
	} {
		resp := get(tc.query, tc.headers)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: bootstrap = %d, want 403", name, resp.StatusCode)
		}
		for _, c := range resp.Cookies() {
			if c.Name == auth.ConsoleCookieName {
				t.Fatalf("%s: bootstrap set the console cookie", name)
			}
		}
	}
}

func TestInstallConsoleSecretWithoutTheShellWritesAPrivateFile(t *testing.T) {
	dir := t.TempDir()
	users, err := auth.NewUserStore(filepath.Join(dir, "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer users.Close()
	h := auth.NewHandler(users, nil, time.Hour, "", "")
	t.Setenv(auth.ConsoleTokenEnv, "")
	file, err := installConsoleSecret(h, "127.0.0.1:5001", filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatalf("installConsoleSecret: %v", err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat %s: %v", file, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("console secret file mode %v, want 0600", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(file)
	if len(strings.TrimSpace(string(raw))) < auth.MinConsoleTokenLength {
		t.Fatalf("console secret file holds %d bytes", len(raw))
	}

	// A secret too short to be one is refused at startup.
	t.Setenv(auth.ConsoleTokenEnv, "short")
	if _, err := installConsoleSecret(h, "127.0.0.1:5001", filepath.Join(dir, "auth.db")); err == nil {
		t.Fatalf("a short console secret was accepted")
	}
	// A listener that is not loopback can carry no console.
	t.Setenv(auth.ConsoleTokenEnv, strings.Repeat("z", 43))
	if _, err := installConsoleSecret(h, "0.0.0.0:5001", filepath.Join(dir, "auth.db")); err == nil {
		t.Fatalf("a console secret was installed for a non-loopback listener")
	}
}

// Credentials a browser never attaches by itself are not CSRF-able, so the
// cross-origin and body-type rules leave them to their own verification; the
// ambient ones (a cookie riding along, Basic auth) are not exempt.
func TestCSRFExemptsOnlyNonAmbientCredentials(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		cookie  bool
		exempt  bool
	}{
		{"signed request", map[string]string{"Authorization": "SDN-Signed challenge=\"c\", signature=\"00\""}, false, true},
		{"bearer token", map[string]string{"Authorization": "Bearer abc"}, false, true},
		{"basic auth", map[string]string{"Authorization": "Basic dXNlcjpwYXNz"}, false, false},
		{"signed request with a cookie", map[string]string{"Authorization": "SDN-Signed challenge=\"c\""}, true, false},
		{"console header", map[string]string{auth.ConsoleTokenHeader: "x"}, false, false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5001/api/admin/x", strings.NewReader("{}"))
		for k, v := range tc.headers {
			r.Header.Set(k, v)
		}
		if tc.cookie {
			r.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: "t"})
		}
		if got := isSelfAuthenticatingRequest(r); got != tc.exempt {
			t.Fatalf("%s: self-authenticating = %v, want %v", tc.name, got, tc.exempt)
		}
	}
}
