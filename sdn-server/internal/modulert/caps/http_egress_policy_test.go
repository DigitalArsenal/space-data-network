package caps

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// allowLoopbackEgressForTest lets a test's module http.request reach its own
// loopback httptest server, the way an operator's
// modules.egress_allow_internal entry would, and restores default-deny after.
func allowLoopbackEgressForTest(t *testing.T) {
	t.Helper()
	if invalid := SetEgressInternalAllowList([]string{"127.0.0.1", "::1"}); len(invalid) > 0 {
		t.Fatalf("allow-list entries refused: %v", invalid)
	}
	t.Cleanup(func() { SetEgressInternalAllowList(nil) })
}

func egressCapCall(t *testing.T, req map[string]interface{}) map[string]interface{} {
	t.Helper()
	payload, _ := json.Marshal(req)
	out, err := httpCapHandle("http.request", payload)
	if err != nil {
		t.Fatalf("httpCapHandle: %v", err)
	}
	var env map[string]interface{}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	return env
}

func refusedByEgressPolicy(env map[string]interface{}) bool {
	if ok, _ := env["ok"].(bool); ok {
		return false
	}
	e, _ := env["error"].(map[string]interface{})
	msg, _ := e["message"].(string)
	return strings.Contains(msg, "egress refused")
}

// internalTarget is a loopback server that records whether it was reached:
// the stand-in for the node's own admin API, Kubo RPC or a metadata service.
func internalTarget(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("INTERNAL-ONLY " + r.URL.Path))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// SEC-027: an http-capable module reached the loopback admin API, Kubo RPC
// and the metadata service by URL.
func TestSEC027_ModuleHTTPCannotReachInternalAddresses(t *testing.T) {
	SetEgressInternalAllowList(nil)
	srv, hits := internalTarget(t)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	for _, u := range []string{
		srv.URL + "/api/modules/capabilities/approve",
		"http://localhost:" + itoaPort(port) + "/api/v0/shutdown", // a NAME that resolves to loopback
		"http://[::ffff:127.0.0.1]:" + itoaPort(port) + "/",
		"http://0x7f000001:" + itoaPort(port) + "/",
	} {
		env := egressCapCall(t, map[string]interface{}{"method": "POST", "url": u, "body": "{}", "timeout_ms": 3000,
			"headers": map[string]string{"X-Requested-With": "XMLHttpRequest"}})
		if ok, _ := env["ok"].(bool); ok {
			t.Fatalf("%s: reached (%v)", u, env)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the internal target was reached %d times", hits.Load())
	}

	started := time.Now()
	env := egressCapCall(t, map[string]interface{}{"url": "http://169.254.169.254/latest/meta-data/iam/", "timeout_ms": 5000})
	if !refusedByEgressPolicy(env) {
		t.Fatalf("metadata URL was not refused by policy: %v", env)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("the metadata refusal took %v: it was dialled instead of refused", time.Since(started))
	}
}

// SEC-027: a public upstream 307-redirected an honest module into loopback.
// Here the upstream is allow-listed by exact port and the redirect target, on
// another loopback port, is not: the redirect hop's dial is refused.
func TestSEC027_RedirectIntoAnInternalAddressIsRefusedAtTheHop(t *testing.T) {
	target, hits := internalTarget(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/v0/shutdown", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(upstream.Close)
	upPort := upstream.Listener.Addr().(*net.TCPAddr).Port
	if invalid := SetEgressInternalAllowList([]string{"127.0.0.1:" + itoaPort(upPort)}); len(invalid) > 0 {
		t.Fatalf("allow-list refused: %v", invalid)
	}
	t.Cleanup(func() { SetEgressInternalAllowList(nil) })

	env := egressCapCall(t, map[string]interface{}{"method": "POST", "url": upstream.URL + "/tle.txt", "body": "{}", "timeout_ms": 3000})
	if !refusedByEgressPolicy(env) {
		t.Fatalf("redirect into an internal address was followed: %v", env)
	}
	if hits.Load() != 0 {
		t.Fatalf("the redirect target was reached")
	}

	// The allow-listed upstream itself is reachable (the exception works).
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(direct.Close)
	SetEgressInternalAllowList([]string{"127.0.0.1:" + itoaPort(direct.Listener.Addr().(*net.TCPAddr).Port)})
	if env := egressCapCall(t, map[string]interface{}{"url": direct.URL + "/", "timeout_ms": 3000}); env["ok"] != true {
		t.Fatalf("an allow-listed internal address was refused: %v", env)
	}
}

func TestEgressPolicyRefusesNonHTTPSchemes(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "gopher://example.com/", "ftp://example.com/x", "unix:///var/run/docker.sock"} {
		env := egressCapCall(t, map[string]interface{}{"url": u, "timeout_ms": 1000})
		if !refusedByEgressPolicy(env) {
			t.Fatalf("%s: not refused by policy: %v", u, env)
		}
	}
}

func TestIsInternalAddress(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "127.255.0.9", "::1", "0.0.0.0", "::", "0.1.2.3",
		"10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"100.64.0.1", "100.100.100.200", "169.254.169.254", "fe80::1",
		"fc00::1", "fd00:ec2::254", "224.0.0.1", "ff02::1", "255.255.255.255", "240.0.0.1",
		"192.0.0.192", "198.18.0.1", "168.63.129.16",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::127.0.0.1",
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "2002:7f00:1::1", "2002:a9fe:a9fe::1",
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", "fec0::1",
	} {
		if !IsInternalAddress(netip.MustParseAddr(s)) {
			t.Fatalf("%s is internal", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "151.101.1.69", "2606:4700:4700::1111", "64:ff9b::808:808", "2002:808:808::1", "172.32.0.1", "100.128.0.1"} {
		if IsInternalAddress(netip.MustParseAddr(s)) {
			t.Fatalf("%s is public", s)
		}
	}
}

func TestParseEgressAllowEntry(t *testing.T) {
	for _, ok := range []string{"10.20.0.0/16", "10.20.0.5", "127.0.0.1:5001", "[fd00::5]:8443", "fd00::/8", " 192.168.1.10 "} {
		if _, err := ParseEgressAllowEntry(ok); err != nil {
			t.Fatalf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "localhost", "internal.example.org", "metadata.google.internal:80", "127.0.0.1:0", "fe80::1%en0", "10.0.0.0/33"} {
		if _, err := ParseEgressAllowEntry(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	rule, _ := ParseEgressAllowEntry("127.0.0.1:5001")
	if !rule.allows(netip.MustParseAddr("127.0.0.1"), 5001) || rule.allows(netip.MustParseAddr("127.0.0.1"), 5002) {
		t.Fatalf("port-scoped rule matches the wrong ports")
	}
	invalid := SetEgressInternalAllowList([]string{"10.0.0.0/8", "not-an-ip"})
	t.Cleanup(func() { SetEgressInternalAllowList(nil) })
	if len(invalid) != 1 || invalid[0] != "not-an-ip" {
		t.Fatalf("invalid entries = %v", invalid)
	}
	if err := checkEgressDestination(netip.MustParseAddr("10.1.1.1"), 443); err != nil {
		t.Fatalf("allow-listed 10/8 refused: %v", err)
	}
	if err := checkEgressDestination(netip.MustParseAddr("192.168.1.1"), 443); err == nil {
		t.Fatalf("192.168/16 allowed by a 10/8 entry")
	}
}

// The dialer is the authority: a URL check alone would miss a name that
// resolves (or re-resolves, DNS rebinding) to an internal address.
func TestEgressDialControlChecksTheResolvedAddress(t *testing.T) {
	SetEgressInternalAllowList(nil)
	for _, addr := range []string{"127.0.0.1:80", "[::1]:443", "169.254.169.254:80", "10.0.0.1:22"} {
		if err := egressDialControl(context.Background(), "tcp", addr, nil); err == nil {
			t.Fatalf("dial to %s allowed", addr)
		}
	}
	if err := egressDialControl(context.Background(), "tcp", "93.184.216.34:443", nil); err != nil {
		t.Fatalf("dial to a public address refused: %v", err)
	}
	if err := egressDialControl(context.Background(), "udp", "93.184.216.34:53", nil); err == nil {
		t.Fatalf("a non-TCP dial was allowed")
	}
	if egressTransport.Proxy != nil {
		t.Fatalf("the egress transport consults an environment proxy")
	}
	if err := egressCheckRedirect(&http.Request{URL: mustURL(t, "file:///etc/passwd")}, nil); err == nil {
		t.Fatalf("a redirect to file: was allowed")
	}
	via := make([]*http.Request, maxEgressRedirects)
	if err := egressCheckRedirect(&http.Request{URL: mustURL(t, "https://example.com/")}, via); err == nil {
		t.Fatalf("an unbounded redirect chain was allowed")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func itoaPort(p int) string {
	b, _ := json.Marshal(p)
	return string(b)
}
