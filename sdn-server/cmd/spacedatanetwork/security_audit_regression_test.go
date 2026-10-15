package main

// Regression tests for the 2026-10-15 security audit findings fixed in this
// package. Each reproduces the verifier's attack through the REAL admin chain
// (adminSecurityMiddleware -> serveAdminMuxRequest -> auth.Handler) on a real
// loopback TCP listener, and uses only APIs that existed before the fix, so
// the same file fails against the unfixed tree.

import (
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

// desktopConsoleChain is a default desktop node: require_auth, sealed remote
// admin, admin.local_console on, a node root identity, behind the real
// middleware, on a loopback listener.
func desktopConsoleChain(t *testing.T) (*httptest.Server, *auth.UserStore, string) {
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
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	h.SetNodeRootIdentity(&auth.RootIdentity{XPub: "xpub-node-root", SigningKeys: []ed25519.PublicKey{pub}})
	h.EnableLocalConsole()

	adminMux := http.NewServeMux()
	h.RegisterRoutes(adminMux)
	wall := func(w http.ResponseWriter, r *http.Request) {
		serveAdminMuxRequest(w, r, adminMux, true, false, h, isPublicAPIRequest, nil, true)
	}
	secured := adminSecurityMiddleware(http.HandlerFunc(wall), "", isPublicAPIRequest)
	srv := httptest.NewServer(newAdminUpgradeRouter(nil, secured))
	t.Cleanup(srv.Close)
	return srv, users, strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
}

func doRequest(t *testing.T, srv *httptest.Server, method, path, host string, headers map[string]string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if host != "" {
		req.Host = host
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// SEC-012: a web page's cross-site no-cors text/plain POST created a
// persistent admin user on a default desktop node.
func TestSEC012_CrossSiteNoCorsPostCannotCreateAnAdmin(t *testing.T) {
	srv, users, port := desktopConsoleChain(t)
	status, body := doRequest(t, srv, http.MethodPost, "/api/auth/users", "127.0.0.1:"+port, map[string]string{
		"Origin":         "https://evil.example",
		"Content-Type":   "text/plain;charset=UTF-8",
		"Sec-Fetch-Site": "cross-site",
		"Sec-Fetch-Mode": "no-cors",
	}, `{"xpub":"xpub-ATTACKER","name":"backdoor","trust_level":"admin"}`)
	if status == http.StatusCreated || status == http.StatusOK {
		t.Fatalf("cross-site POST admitted: %d %s", status, body)
	}
	if u, _ := users.GetUser("xpub-ATTACKER"); u != nil {
		t.Fatalf("cross-site POST planted user %q at %v", u.Name, u.TrustLevel)
	}
}

// SEC-012: with the Host of a DNS-rebinding name, admin GETs answered.
func TestSEC012_DNSRebindingCannotReadAdminData(t *testing.T) {
	srv, _, port := desktopConsoleChain(t)
	for _, path := range []string{"/api/auth/users", "/api/auth/me"} {
		status, body := doRequest(t, srv, http.MethodGet, path, "rebind.evil.example:"+port, map[string]string{
			"Origin": "http://rebind.evil.example:" + port,
		}, "")
		if status == http.StatusOK {
			t.Fatalf("rebinding Host read %s: %d %s", path, status, body)
		}
	}
}

// SEC-012: loopback alone is not the console. Any other local process (or a
// module with the http capability) got root admin by asking.
func TestSEC012_LoopbackWithoutTheConsoleSecretIsNotRoot(t *testing.T) {
	srv, _, port := desktopConsoleChain(t)
	status, body := doRequest(t, srv, http.MethodGet, "/api/auth/users", "127.0.0.1:"+port, nil, "")
	if status == http.StatusOK {
		t.Fatalf("a loopback caller without the console secret read the user list: %d %s", status, body)
	}
	status, body = doRequest(t, srv, http.MethodPost, "/api/auth/users", "127.0.0.1:"+port, map[string]string{
		"Content-Type":     "application/json",
		"X-Requested-With": "XMLHttpRequest",
	}, `{"xpub":"xpub-LOCAL","name":"local","trust_level":"admin"}`)
	if status == http.StatusCreated || status == http.StatusOK {
		t.Fatalf("a loopback caller without the console secret created an admin: %d %s", status, body)
	}
}

// SEC-012: the CSRF check ran only when a session cookie was present.
// Without one, a cross-site state change reached the handler.
func TestSEC012_CSRFCheckRunsWithoutACookie(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	})
	mw := adminSecurityMiddleware(next, "", isPublicAPIRequest)
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"cross-site origin", map[string]string{"Origin": "https://evil.example", "Content-Type": "application/json"}},
		{"sec-fetch-site cross-site", map[string]string{"Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}},
		{"null origin", map[string]string{"Origin": "null", "Content-Type": "application/json"}},
		{"another localhost port", map[string]string{"Origin": "http://127.0.0.1:8000", "Content-Type": "application/json"}},
	} {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5001/api/modules/capabilities/approve", strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:40000"
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if reached || rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d, handler reached=%v; want 403 before the handler", tc.name, rec.Code, reached)
		}
	}
}

// SEC-012: JSON handlers decoded text/plain bodies, so a no-cors POST (which
// cannot set application/json) worked. Without proof of same origin, a
// safelisted body type is refused.
func TestSEC012_SafelistedBodyTypeNeedsSameOrigin(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	})
	mw := adminSecurityMiddleware(next, "", isPublicAPIRequest)
	for _, ctype := range []string{"text/plain;charset=UTF-8", "application/x-www-form-urlencoded", ""} {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5001/api/auth/users", strings.NewReader(`{"xpub":"x"}`))
		req.RemoteAddr = "127.0.0.1:40000"
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if reached {
			t.Fatalf("Content-Type %q with no same-origin proof reached the handler (%d)", ctype, rec.Code)
		}
	}
	// The node's own page (same Origin) and JSON clients still get through.
	for _, tc := range []map[string]string{
		{"Content-Type": "text/plain;charset=UTF-8", "Origin": "http://127.0.0.1:5001"},
		{"Content-Type": "text/plain;charset=UTF-8", "Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:5001"},
		{"Content-Type": "application/json"},
	} {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5001/api/auth/users", strings.NewReader(`{"xpub":"x"}`))
		req.RemoteAddr = "127.0.0.1:40000"
		for k, v := range tc {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if !reached {
			t.Fatalf("%v: a same-origin or JSON request was refused (%d %s)", tc, rec.Code, rec.Body.String())
		}
	}
}

// SEC-033: the Admin gate matched only the literal /api/v1/data/query, so the
// data-retrieval flow's other */query paths ran SQL with no session on an
// auth-off node, for any method.
func TestSEC033_EveryDataPlaneQueryPathIsAdminOnly(t *testing.T) {
	for _, path := range []string{
		"/api/v1/data/query",
		"/api/v1/data/x/query",
		"/api/v1/data/omm/query",
		"/api/v1/data/a/b/c/query",
		"/api/v1/data/omm/query/",
		"/api/v1/data/omm/QUERY",
	} {
		if !isAdminOnlyAPIPath(path) {
			t.Fatalf("%s is not admin-only", path)
		}
		if isPublicAPIRequest(http.MethodGet, path) || isPublicAPIRequest(http.MethodPost, path) {
			t.Fatalf("%s is on the anonymous surface", path)
		}
	}
	if isAdminOnlyAPIPath("/api/v1/data/omm/bulk") || !isPublicAPIRequest(http.MethodGet, "/api/v1/data/omm/bulk") {
		t.Fatalf("the public bulk read must stay public")
	}

	flowReached := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/data/", func(w http.ResponseWriter, r *http.Request) {
		flowReached = true
		w.WriteHeader(299)
	})
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, path := range []string{"/api/v1/data/x/query", "/api/v1/data/omm/query"} {
			flowReached = false
			req := httptest.NewRequest(method, path, strings.NewReader("SELECT * FROM sqlite_master"))
			req.RemoteAddr = "127.0.0.1:40000"
			rec := httptest.NewRecorder()
			// require_auth=false, no auth handler: the gate must fail closed.
			serveAdminMuxRequest(rec, req, mux, false, false, nil, isPublicAPIRequest, nil, true)
			if flowReached || rec.Code == 299 {
				t.Fatalf("auth-off %s %s reached the data-retrieval flow with no session", method, path)
			}
		}
	}
}

// SEC-033: listen_addr ":5001" (empty host: every interface) passed the
// auth-off loopback guard.
func TestSEC033_AuthOffListenerGuardRefusesEveryInterfaceBind(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.Enabled = true
	cfg.Admin.RequireAuth = false
	for _, addr := range []string{":5001", "[]:5001"} {
		cfg.Admin.ListenAddr = addr
		if err := adminListenerSafety(cfg); err == nil {
			t.Fatalf("listen_addr %q (every interface) with require_auth=false was accepted", addr)
		}
	}
}
