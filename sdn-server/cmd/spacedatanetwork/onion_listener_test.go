package main

// Round-2 tests for the admin listener's network edges: the Tor onion socket,
// the Host allow-list on auth-off nodes (NET-03's DNS-rebinding variant), the
// header-carried console secret, and the SQL gate on every flow mount.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/netorigin"
)

// ---- Tor onion service ---------------------------------------------------

func TestOnionFrontsAdmin(t *testing.T) {
	base := func(target string) *config.Config {
		cfg := &config.Config{}
		cfg.Admin.Enabled = true
		cfg.Admin.ListenAddr = "127.0.0.1:5001"
		cfg.Tor.Enabled = true
		cfg.Tor.HiddenServiceEnabled = true
		cfg.Tor.HiddenServiceTarget = target
		return cfg
	}
	for _, target := range []string{"", "127.0.0.1:5001", "localhost:5001", "[::1]:5001", ":5001", "0.0.0.0:5001", "10.0.0.4:5001", "garbage"} {
		if !onionFrontsAdmin(base(target)) {
			t.Fatalf("target %q: not treated as the admin listener", target)
		}
	}
	if onionFrontsAdmin(base("127.0.0.1:8088")) {
		t.Fatalf("a different port was treated as the admin listener")
	}
	off := base("")
	off.Tor.HiddenServiceEnabled = false
	if onionFrontsAdmin(off) {
		t.Fatalf("no hidden service, yet fronted")
	}
	unset := base("")
	unset.Admin.ListenAddr = ""
	if !onionFrontsAdmin(unset) {
		t.Fatalf("default admin listener not recognised")
	}
}

// Over a real socket, exactly as Tor connects: from 127.0.0.1, with whatever
// forwarding headers the onion client chose to send.
func TestOnionClientsAreAnonymousRemoteClients(t *testing.T) {
	ln, err := listenOnionAdmin()
	if err != nil {
		t.Fatalf("listenOnionAdmin: %v", err)
	}
	srv := &http.Server{Handler: onionClientHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "local=%v client=%s bucket=%s onion=%v", netorigin.OriginatedOnThisBox(r), netorigin.ClientIP(r), netorigin.RateLimitKey(r), netorigin.IsOnionRequest(r))
	}))}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		req.Host = "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrstuvw.onion"
		if i%2 == 0 {
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		} else {
			req.Header.Set("X-Real-IP", fmt.Sprintf("203.0.113.%d", i))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		got := string(body)
		if !strings.Contains(got, "local=false") || !strings.Contains(got, "onion=true") {
			t.Fatalf("an onion client looked local: %s", got)
		}
		seen[got] = true
	}
	if len(seen) != 1 {
		t.Fatalf("onion clients chose their own client address / rate-limit bucket: %v", seen)
	}
}

// remote_requires_sealed and the loopback self-gated control routes treated
// every onion client as this machine.
func TestOnionClientsDoNotReachLoopbackOnlyRoutes(t *testing.T) {
	reached := map[string]int{}
	for name, mark := range map[string]bool{"loopback": false, "onion": true} {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/admin/update/shutdown", func(w http.ResponseWriter, r *http.Request) {
			reached[name]++
			w.WriteHeader(http.StatusAccepted)
		})
		var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveAdminMuxRequest(w, r, mux, true, false, auth.NewHandler(nil, nil, 0, "", ""), isPublicAPIRequest, nil, true)
		})
		if mark {
			h = onionClientHandler(h)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
	if reached["loopback"] != 1 {
		t.Fatalf("the local control route was not reached from loopback: %v", reached)
	}
	if reached["onion"] != 0 {
		t.Fatalf("an onion client reached a loopback-only control route: %v", reached)
	}
}

// ---- NET-03: DNS rebinding against an auth-off node -----------------------

// authOffChain is a require_auth:false node's admin chain composed exactly as
// runDaemon composes it, serving a stand-in for the open read surface.
func authOffChain(t *testing.T, allowedHosts []string) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peers", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "peer-table")
	})
	wall := func(w http.ResponseWriter, r *http.Request) {
		serveAdminMuxRequest(w, r, mux, false, false, nil, isPublicAPIRequest, nil, true)
	}
	var handler http.Handler = newAdminUpgradeRouter(nil, adminSecurityMiddleware(http.HandlerFunc(wall), "", isPublicAPIRequest))

	srv := httptest.NewUnstartedServer(nil)
	addr := srv.Listener.Addr().String()
	cfg := &config.Config{}
	cfg.Admin.Enabled = true
	cfg.Admin.RequireAuth = false
	cfg.Admin.ListenAddr = addr
	cfg.Admin.AllowedHosts = allowedHosts
	if !adminListenerHasAmbientAuthority(cfg, false) {
		t.Fatalf("an auth-off listener was not recognised as ambient authority")
	}
	guarded, err := adminHostAllowList(addr, cfg.Admin.AllowedHosts, handler)
	if err != nil {
		t.Fatalf("adminHostAllowList: %v", err)
	}
	srv.Config.Handler = guarded
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(addr, "127.0.0.1:")
}

func TestNET03_AuthOffNodeRefusesDNSRebinding(t *testing.T) {
	srv, port := authOffChain(t, []string{"Records.Example.org"})
	for _, host := range []string{"rebind.evil.example:" + port, "127.0.0.1.nip.io:" + port, "localhost.:" + port, "evil.example"} {
		status, body := doRequest(t, srv, http.MethodGet, "/api/peers", host, map[string]string{"Origin": "http://" + host}, "")
		if status != http.StatusMisdirectedRequest {
			t.Fatalf("rebinding Host %q read the auth-off node: %d %s", host, status, body)
		}
	}
	for _, host := range []string{"127.0.0.1:" + port, "localhost:" + port, "localhost", "records.example.org"} {
		status, body := doRequest(t, srv, http.MethodGet, "/api/peers", host, nil, "")
		if status != http.StatusOK || body != "peer-table" {
			t.Fatalf("Host %q: %d %s, want the read surface", host, status, body)
		}
	}
}

func TestAdminListenerAmbientAuthority(t *testing.T) {
	cfg := &config.Config{}
	cfg.Admin.RequireAuth = true
	if adminListenerHasAmbientAuthority(cfg, true) {
		t.Fatalf("a require_auth node without a console mode needs no Host pinning (its proxy's public Host must keep working)")
	}
	cfg.Admin.LocalConsole = true
	if !adminListenerHasAmbientAuthority(cfg, true) {
		t.Fatalf("local_console not recognised")
	}
	cfg.Admin.LocalConsole = false
	cfg.Admin.DevAutoAdmin = true
	if !adminListenerHasAmbientAuthority(cfg, true) {
		t.Fatalf("dev_auto_admin not recognised")
	}
}

func TestAdminAllowedHostsAreExact(t *testing.T) {
	for raw, want := range map[string]string{
		"Records.Example.org":      "records.example.org",
		"records.example.org:8443": "records.example.org:8443",
		"10.0.0.5":                 "10.0.0.5",
		"[::1]:5001":               "[::1]:5001",
		"::1":                      "[::1]",
	} {
		got, err := config.NormalizeAllowedHost(raw)
		if err != nil || got != want {
			t.Fatalf("NormalizeAllowedHost(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "*.example.org", "http://example.org", "example.org/x", "user@example.org", "example.org:0", "example.org:99999", ".example.org", "example.org.", "exa mple.org", "ex_ample.org"} {
		if got, err := config.NormalizeAllowedHost(raw); err == nil {
			t.Fatalf("NormalizeAllowedHost(%q) accepted as %q", raw, got)
		}
	}
	if _, err := adminHostAllowList("127.0.0.1:5001", []string{"*.evil.example"}, http.NotFoundHandler()); err == nil {
		t.Fatalf("a wildcard admin.allowed_hosts entry was installed")
	}
}

// ---- console secret in its header ----------------------------------------

// A local tool holding the console secret POSTs JSON with the header alone.
// The header counted as an ambient (cookie-like) credential, so the request
// was refused for want of an Origin a non-browser client never sends.
func TestConsoleHeaderTokenPostNeedsNoBrowserHeaders(t *testing.T) {
	srv, users, port, token := consoleChain(t)
	status, body := doRequest(t, srv, http.MethodPost, "/api/auth/users", "127.0.0.1:"+port, map[string]string{
		auth.ConsoleTokenHeader: token,
		"Content-Type":          "application/json",
	}, `{"xpub":"xpub-cli-added","name":"cli","trust_level":"standard","signing_pubkey_hex":"`+strings.Repeat("cd", 32)+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("header-token POST = %d %s", status, body)
	}
	if u, _ := users.GetUser("xpub-cli-added"); u == nil {
		t.Fatalf("header-token POST did not create the user")
	}
	// Still refused: the wrong secret, and a browser's cross-site signal.
	for name, headers := range map[string]map[string]string{
		"wrong secret": {auth.ConsoleTokenHeader: strings.Repeat("x", 43), "Content-Type": "application/json"},
		"cross-site":   {auth.ConsoleTokenHeader: token, "Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"},
		"with cookie":  {auth.ConsoleTokenHeader: token, "Content-Type": "application/json", "Cookie": auth.ConsoleCookieName + "=" + token, "Origin": "https://evil.example"},
	} {
		status, body := doRequest(t, srv, http.MethodPost, "/api/auth/users", "127.0.0.1:"+port, headers,
			`{"xpub":"xpub-`+strings.ReplaceAll(name, " ", "-")+`","name":"x","trust_level":"admin","signing_pubkey_hex":"`+strings.Repeat("ef", 32)+`"}`)
		if status == http.StatusCreated || status == http.StatusOK {
			t.Fatalf("%s: admitted (%d %s)", name, status, body)
		}
	}
}

// ---- SQL gate on every flow mount ------------------------------------------

func TestSQLGateCoversEveryFlowMount(t *testing.T) {
	t.Cleanup(func() { setDataPlaneQueryMounts(nil) })
	if isDataPlaneQueryPath("/api/v1/catalog/omm/query") {
		t.Fatalf("an unmounted prefix was gated before configuration")
	}
	setDataPlaneQueryMounts([]string{"/api/v1/catalog/", "/api/v1/exact", ""})
	for _, p := range []string{"/api/v1/data/omm/query", "/api/v1/catalog/query", "/api/v1/catalog/omm/Query/", "/api/v1/exact/query"} {
		if !isDataPlaneQueryPath(p) {
			t.Fatalf("%s: not gated", p)
		}
	}
	for _, p := range []string{"/api/v1/catalog/omm/bulk", "/api/v1/trust/query", "/api/v1/exactly/query"} {
		if isDataPlaneQueryPath(p) {
			t.Fatalf("%s: gated, but it is not under a flow mount's SQL route", p)
		}
	}
	reached := false
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	r := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/omm/query?sql=SELECT+1", nil)
	r.RemoteAddr = "127.0.0.1:4000"
	w := httptest.NewRecorder()
	serveAdminMuxRequest(w, r, mux, false, false, nil, func(string, string) bool { return true }, nil, true)
	if reached {
		t.Fatalf("an anonymous caller reached a second mount's SQL route on an auth-off node (%d)", w.Code)
	}
}
