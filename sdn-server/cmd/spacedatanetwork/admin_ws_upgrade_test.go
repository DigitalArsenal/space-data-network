package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

// THE ROOT-PATH WEBSOCKET UPGRADE, TESTED AGAINST A LIVE LISTENER.
//
// Written for task sdn-ws-upgrade-regression-82cdbf50, after an owner outage
// report ("SpaceAware.io and the beta currently aren't receiving data") and a
// production rollback. The mechanism that broke the browsers turned out to be
// elsewhere (peer-admission trimming, internal/node/peer_admission_policy.go),
// but the investigation exposed that the single most load-bearing route on the
// public node — the one every browser's libp2p dial lands on — had NO test at
// all, and could therefore have broken exactly this way without anyone
// noticing until a human said "the site has no data".
//
// The failure being locked out: a root-path request carrying
// `Connection: Upgrade` / `Upgrade: websocket` gets answered by the dashboard
// catch-all with 200 and a page of HTML instead of 101 Switching Protocols.
// The site looks perfectly healthy; every browser silently loses its wss data
// path.
//
// These tests use a REAL net/http server, a REAL httputil reverse proxy and a
// REAL backend listener that speaks the 101 handshake, driven over a RAW
// HTTP/1.1 socket. Not httptest.NewRecorder: a recorder cannot hijack, and a
// protocol upgrade is precisely the thing a recorder cannot observe.

// upgradeEchoBackend stands in for the local libp2p /ws listener: it completes
// the websocket handshake and then writes a recognisable banner, the same shape
// as the /multistream/1.0.0 banner a real libp2p ws listener sends.
func upgradeEchoBackend(t *testing.T, banner string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isWebSocketUpgradeRequest(r) {
			http.Error(w, "backend reached without an upgrade request", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nX-Backend-Path: %s\r\n\r\n%s",
			r.URL.Path, banner)
		_ = buf.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testAdminRouter(t *testing.T, backend *httptest.Server) *httptest.Server {
	t.Helper()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	dashboard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><title>SDN node status dashboard</title>"))
	})

	srv := httptest.NewServer(newAdminUpgradeRouter(proxy, dashboard))
	t.Cleanup(srv.Close)
	return srv
}

// rawRequest speaks HTTP/1.1 down a socket and returns the status line plus the
// bytes that follow the header block. HTTP/1.1 on purpose: HTTP/2 forbids
// connection-specific headers, so `Connection: Upgrade` cannot even be
// expressed over h2 and an h2 probe of this route always and correctly sees the
// dashboard. See newAdminUpgradeRouter's note.
func rawRequest(t *testing.T, addr string, header string) (string, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := conn.Write([]byte(header)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	var body strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		body.WriteString(line)
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	rest := make([]byte, 512)
	n, _ := reader.Read(rest)
	return strings.TrimSpace(statusLine), body.String() + string(rest[:n])
}

func upgradeRequest(host, path string) string {
	return "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Protocol: /multistream/1.0.0\r\n" +
		"\r\n"
}

// TestAdminRootPathWebSocketUpgradeReturns101 is THE regression test named in
// task sdn-ws-upgrade-regression-82cdbf50: an Upgrade request to "/" against a
// live listener must return 101, and a plain GET to "/" must still return the
// dashboard with 200.
func TestAdminRootPathWebSocketUpgradeReturns101(t *testing.T) {
	backend := upgradeEchoBackend(t, "\x13/multistream/1.0.0\n")
	admin := testAdminRouter(t, backend)
	addr := strings.TrimPrefix(admin.URL, "http://")

	status, rest := rawRequest(t, addr, upgradeRequest(addr, "/"))
	if !strings.Contains(status, "101") {
		t.Fatalf("root-path upgrade answered %q, want 101 Switching Protocols.\n"+
			"A 200 here means the dashboard catch-all took the request and every browser lost its libp2p wss data path.\nbody: %.200s",
			status, rest)
	}
	if !strings.Contains(rest, "multistream/1.0.0") {
		t.Fatalf("upgraded connection did not carry the libp2p banner through the tunnel; got %.200s", rest)
	}

	// ...and the homepage still works. Both halves matter: "always tunnel" is
	// just as broken as "never tunnel", and would take the dashboard down.
	resp, err := admin.Client().Get(admin.URL + "/")
	if err != nil {
		t.Fatalf("plain GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plain GET / = %d, want 200 (the dashboard is the homepage)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("plain GET / content-type = %q, want text/html", ct)
	}
}

// TestAdminWebSocketUpgradeOverTLSReturns101 runs the same probe through a TLS
// listener, because production terminates TLS here and the upgrade path must
// survive it. The h2 hazard is in scope: NextProtos is pinned to http/1.1, the
// only ALPN over which a websocket upgrade is expressible.
func TestAdminWebSocketUpgradeOverTLSReturns101(t *testing.T) {
	backend := upgradeEchoBackend(t, "\x13/multistream/1.0.0\n")
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	dashboard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!doctype html>dashboard"))
	})

	srv := httptest.NewUnstartedServer(newAdminUpgradeRouter(proxy, dashboard))
	srv.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "https://")
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := conn.Write([]byte(upgradeRequest(addr, "/"))); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	statusLine, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("TLS root-path upgrade answered %q, want 101", strings.TrimSpace(statusLine))
	}
}

// TestAdminOwnWebSocketEndpointsAreNotTunnelled locks rule 2: /ws and
// /ws/status belong to the admin mux. Tunnelling them hands a libp2p
// multistream banner to a client that dialled the pubsub bridge or the status
// feed — which is how the dashboard's own live feed dies.
func TestAdminOwnWebSocketEndpointsAreNotTunnelled(t *testing.T) {
	backend := upgradeEchoBackend(t, "banner")
	target, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)

	reached := make(chan string, 4)
	admin := httptest.NewServer(newAdminUpgradeRouter(proxy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("admin mux"))
	})))
	defer admin.Close()
	addr := strings.TrimPrefix(admin.URL, "http://")

	for _, path := range []string{"/ws", "/ws/", "/ws/status", "/ws/status/"} {
		status, _ := rawRequest(t, addr, upgradeRequest(addr, path))
		if strings.Contains(status, "101") {
			t.Fatalf("%s was tunnelled to libp2p (%s); it belongs to the admin mux", path, status)
		}
		select {
		case got := <-reached:
			if got != path {
				t.Fatalf("admin mux saw %q, want %q", got, path)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s never reached the admin mux", path)
		}
	}
}

// TestNonRootPathWebSocketUpgradeIsTunnelled covers the /p2p/<peerid> shape
// sdn-js dials when it addresses the node by multiaddr rather than by root.
func TestNonRootPathWebSocketUpgradeIsTunnelled(t *testing.T) {
	backend := upgradeEchoBackend(t, "banner")
	admin := testAdminRouter(t, backend)
	addr := strings.TrimPrefix(admin.URL, "http://")

	path := "/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	status, rest := rawRequest(t, addr, upgradeRequest(addr, path))
	if !strings.Contains(status, "101") {
		t.Fatalf("%s upgrade answered %q, want 101 (body %.120s)", path, status, rest)
	}
	if !strings.Contains(rest, path) {
		t.Fatalf("tunnel did not preserve the dial path; backend reported %.200s", rest)
	}
}

// TestUpgradeRoutingWithoutATunnelFallsThrough locks rule 3: a node with no
// local /ws listener (adminTLS off, or discovery failed) must serve its admin
// surface normally rather than 502 or hang.
func TestUpgradeRoutingWithoutATunnelFallsThrough(t *testing.T) {
	admin := httptest.NewServer(newAdminUpgradeRouter(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("admin mux"))
	})))
	defer admin.Close()
	addr := strings.TrimPrefix(admin.URL, "http://")

	status, body := rawRequest(t, addr, upgradeRequest(addr, "/"))
	if !strings.Contains(status, "200") {
		t.Fatalf("no-tunnel upgrade answered %q, want the admin surface (200); body %.120s", status, body)
	}
}

// TestWebSocketUpgradeHeaderParsing pins the header contract browsers and
// proxies actually produce. `Connection: keep-alive, Upgrade` is what several
// intermediaries send, and a naive equality check on the header would drop it
// straight into the dashboard catch-all.
func TestWebSocketUpgradeHeaderParsing(t *testing.T) {
	for _, tc := range []struct {
		connection string
		upgrade    string
		want       bool
	}{
		{"Upgrade", "websocket", true},
		{"upgrade", "WebSocket", true},
		{"keep-alive, Upgrade", "websocket", true},
		{"Upgrade, keep-alive", "websocket", true},
		{" UPGRADE ", " websocket ", true},
		{"keep-alive", "websocket", false},
		{"Upgrade", "h2c", false},
		{"Upgrade", "", false},
		{"", "websocket", false},
		{"upgraded", "websocket", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.connection != "" {
			r.Header.Set("Connection", tc.connection)
		}
		if tc.upgrade != "" {
			r.Header.Set("Upgrade", tc.upgrade)
		}
		if got := isWebSocketUpgradeRequest(r); got != tc.want {
			t.Errorf("Connection=%q Upgrade=%q -> %v, want %v", tc.connection, tc.upgrade, got, tc.want)
		}
	}
	if isWebSocketUpgradeRequest(nil) {
		t.Error("nil request must not read as an upgrade")
	}
}

// TestResolveLocalLibp2pWsProxyTargetPicksTheLocalListener pins the discovery
// that decides whether a tunnel exists at all. host-01's live listen set is the
// fixture, plus the AutoTLS `/tls/sni/*.libp2p.direct/ws` address that
// internal/node adds on a node with network.autotls.enabled — that one is a
// PUBLIC CA-terminated address and must never be chosen as the loopback proxy
// target.
func TestResolveLocalLibp2pWsProxyTargetPicksTheLocalListener(t *testing.T) {
	target, source := resolveLocalLibp2pWsProxyTarget([]string{
		"/ip4/104.131.11.220/tcp/4004/ws",
		"/ip4/127.0.0.1/tcp/4004/ws",
		"/ip4/127.0.0.1/tcp/18080/ws",
	})
	if target == nil {
		t.Fatal("no proxy target from host-01's live listen set; the browser tunnel would not exist")
	}
	if target.Host != "127.0.0.1:4004" {
		t.Fatalf("proxy target = %s (from %s), want the loopback form of the first /ws listener", target, source)
	}

	if target, _ := resolveLocalLibp2pWsProxyTarget([]string{
		"/ip4/0.0.0.0/tcp/4001",
		"/ip4/0.0.0.0/udp/4001/quic-v1",
	}); target != nil {
		t.Fatalf("a node with no /ws listener resolved a tunnel target %s", target)
	}

	// AutoTLS shares the plain TCP port, so a naive scan would send the tunnel
	// at the public CA-terminated listener.
	autotls, source := resolveLocalLibp2pWsProxyTarget([]string{
		"/ip4/167.172.219.213/tcp/4001/tls/sni/example.libp2p.direct/ws",
		"/ip4/127.0.0.1/tcp/18080/ws",
	})
	if autotls == nil || autotls.Host != "127.0.0.1:18080" {
		t.Fatalf("with AutoTLS present the tunnel resolved to %v (from %s), want the plain loopback /ws listener 127.0.0.1:18080",
			autotls, source)
	}
}
