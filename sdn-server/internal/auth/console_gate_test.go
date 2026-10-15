package auth

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestConsoleGateNeedsEveryCondition(t *testing.T) {
	h := newLocalConsoleHandler(t, true)
	h.EnableLocalConsole()

	if s, err := h.sessionFromRequest(consoleRequest("127.0.0.1:50000")); err != nil || s.TrustLevel < peers.Admin {
		t.Fatalf("the console itself was refused: %v", err)
	}

	cases := map[string]func(r *http.Request){
		"no secret": func(r *http.Request) {
			r.Header.Del("Cookie")
		},
		"wrong secret": func(r *http.Request) {
			r.Header.Del("Cookie")
			r.AddCookie(&http.Cookie{Name: ConsoleCookieName, Value: strings.Repeat("A", len(testConsoleToken))})
		},
		"secret prefix": func(r *http.Request) {
			r.Header.Del("Cookie")
			r.AddCookie(&http.Cookie{Name: ConsoleCookieName, Value: testConsoleToken[:len(testConsoleToken)-1]})
		},
		"rebinding host":        func(r *http.Request) { r.Host = "rebind.evil.example:5001" },
		"other port host":       func(r *http.Request) { r.Host = "127.0.0.1:5002" },
		"cross-site":            func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site":             func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"foreign origin":        func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"other localhost port":  func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8000") },
		"null origin":           func(r *http.Request) { r.Header.Set("Origin", "null") },
		"foreign referer":       func(r *http.Request) { r.Header.Set("Referer", "https://evil.example/page") },
		"proxy via header":      func(r *http.Request) { r.Header.Set("Via", "1.1 proxy") },
		"cdn client ip header":  func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.9") },
		"remote peer":           func(r *http.Request) { r.RemoteAddr = "203.0.113.9:4444" },
		"ipv4-mapped remote":    func(r *http.Request) { r.RemoteAddr = "[::ffff:203.0.113.9]:4444" },
		"zoned link-local peer": func(r *http.Request) { r.RemoteAddr = "[fe80::1%lo0]:4444" },
	}
	for name, mutate := range cases {
		r := consoleRequest("127.0.0.1:50000")
		mutate(r)
		if s, err := h.sessionFromRequest(r); err == nil {
			t.Fatalf("%s: admitted as %s", name, s.XPub)
		}
	}

	// The same-origin console page and a local tool with the header.
	r := consoleRequest("127.0.0.1:50000")
	r.Header.Set("Origin", "http://127.0.0.1:5001")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if _, err := h.sessionFromRequest(r); err != nil {
		t.Fatalf("same-origin console page refused: %v", err)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "[::ffff:127.0.0.1]:50000"
	r.Host = "localhost:5001"
	r.Header.Set(ConsoleTokenHeader, testConsoleToken)
	if _, err := h.sessionFromRequest(r); err != nil {
		t.Fatalf("local tool with the console header refused: %v", err)
	}
}

func TestConsoleGateFailsClosedWithoutASecret(t *testing.T) {
	h := newLocalConsoleHandler(t, true)
	h.consoleToken = "" // as if SetConsoleToken was never called
	h.EnableLocalConsole()
	r := consoleRequest("127.0.0.1:50000")
	r.Header.Del("Cookie")
	r.AddCookie(&http.Cookie{Name: ConsoleCookieName, Value: ""})
	if _, err := h.sessionFromRequest(r); err == nil {
		t.Fatalf("local console admitted a request with no secret configured")
	}
	if err := h.SetConsoleToken("too-short"); err == nil {
		t.Fatalf("a guessable console secret was accepted")
	}
	if err := h.SetConsoleToken(strings.Repeat("a", 40) + ";x"); err == nil {
		t.Fatalf("a console secret that breaks the cookie was accepted")
	}
}

func TestConsoleHostsForListenAddr(t *testing.T) {
	hosts, err := ConsoleHostsForListenAddr("127.0.0.1:5001")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"127.0.0.1:5001", "localhost:5001"} {
		if !hosts[want] {
			t.Fatalf("%s missing from %v", want, hosts)
		}
	}
	if hosts["127.0.0.1:5002"] || hosts["evil.example:5001"] {
		t.Fatalf("unexpected host in %v", hosts)
	}
	for _, bad := range []string{"0.0.0.0:5001", ":5001", "10.0.0.1:5001", "127.0.0.1:0", "127.0.0.1"} {
		if _, err := ConsoleHostsForListenAddr(bad); err == nil {
			t.Fatalf("%q accepted as a console listener", bad)
		}
	}
}

func TestOriginMatchesHost(t *testing.T) {
	for _, tc := range []struct {
		origin, host string
		want         bool
	}{
		{"http://127.0.0.1:5001", "127.0.0.1:5001", true},
		{"http://LOCALHOST:5001", "localhost:5001", true},
		{"http://127.0.0.1", "127.0.0.1:80", true},
		{"https://node.example", "node.example:443", true},
		{"http://127.0.0.1:8000", "127.0.0.1:5001", false},
		{"http://[::1]:5001", "[::1]:5001", true},
		{"http://[::1]:5002", "[::1]:5001", false},
		{"null", "127.0.0.1:5001", false},
		{"file:///etc/passwd", "127.0.0.1:5001", false},
		{"http://evil.example", "127.0.0.1:5001", false},
	} {
		if got := originMatchesHost(tc.origin, tc.host); got != tc.want {
			t.Fatalf("originMatchesHost(%q, %q) = %v, want %v", tc.origin, tc.host, got, tc.want)
		}
	}
}

func TestSessionAbsoluteLifetimeSurvivesRotation(t *testing.T) {
	h, users, sessions, _ := newRegressionHandler(t, nil, time.Hour)
	_ = h
	enrollSessionAccount(t, users, "xpub-long-lived", peers.Admin)
	tok, err := sessions.CreateSession("xpub-long-lived", peers.Admin, "198.51.100.1", "ua", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Rotate: the replacement keeps the original sign-in time.
	newTok, rotated, err := sessions.RotateSession(tok, time.Hour, peers.Admin)
	if err != nil {
		t.Fatalf("RotateSession: %v", err)
	}
	orig := rotated.CreatedAt
	if time.Since(orig) > time.Minute {
		t.Fatalf("rotated CreatedAt = %v", orig)
	}
	// Age the sign-in past the absolute bound: rotation can no longer extend it.
	if _, err := sessions.db.Exec("UPDATE sessions SET created_at = ? WHERE token = ?", time.Now().Add(-DefaultSessionAbsoluteLifetime-time.Hour).Unix(), hashToken(newTok)); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.ValidateSession(newTok); err == nil {
		t.Fatalf("a session older than the absolute lifetime validated")
	}
}

func TestRotateSessionNeverRaisesTrust(t *testing.T) {
	_, _, sessions, _ := newRegressionHandler(t, nil, time.Hour)
	tok, _ := sessions.CreateSession("xpub-x", peers.Standard, "198.51.100.1", "ua", time.Hour)
	_, rotated, err := sessions.RotateSession(tok, time.Hour, peers.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.TrustLevel != peers.Standard {
		t.Fatalf("rotation raised trust to %v", rotated.TrustLevel)
	}
	tok2, _ := sessions.CreateSession("xpub-y", peers.Admin, "198.51.100.1", "ua", time.Hour)
	_, rotated, err = sessions.RotateSession(tok2, time.Hour, peers.Standard)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.TrustLevel != peers.Standard {
		t.Fatalf("rotation kept %v after the account was demoted", rotated.TrustLevel)
	}
}

func TestReconcileKeepsTheNodeRootSession(t *testing.T) {
	h, _, sessions, _ := newRegressionHandler(t, nil, time.Hour)
	pub, _, _ := ed25519.GenerateKey(nil)
	h.SetNodeRootIdentity(&RootIdentity{XPub: "xpub-node-root", SigningKeys: []ed25519.PublicKey{pub}})
	tok, _ := sessions.CreateSession("xpub-node-root", peers.Ultimate, "127.0.0.1", "root", time.Hour)
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: tok})
	s, err := h.sessionFromRequest(r)
	if err != nil || s.TrustLevel != peers.Ultimate {
		t.Fatalf("the node's own rowless root session was refused or reduced: %v %v", s, err)
	}
	// Any OTHER rowless session is a removed account.
	tok, _ = sessions.CreateSession("xpub-never-enrolled", peers.Admin, "127.0.0.1", "x", time.Hour)
	r = httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: tok})
	if _, err := h.sessionFromRequest(r); err == nil {
		t.Fatalf("a session with no account row was admitted")
	}
}
