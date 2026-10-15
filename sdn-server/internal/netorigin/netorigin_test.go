package netorigin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func req(remote string, headers map[string][]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for k, vs := range headers {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	return r
}

func TestClientIPTakesTheEntryTheTrustedProxyAppended(t *testing.T) {
	SetTrustedProxyHops(1)
	t.Cleanup(func() { SetTrustedProxyHops(1) })
	for _, tc := range []struct {
		name    string
		remote  string
		headers map[string][]string
		want    string
	}{
		{"remote peer ignores headers", "198.51.100.7:1", map[string][]string{"X-Forwarded-For": {"6.6.6.6"}}, "198.51.100.7"},
		{"proxy, client-forged leftmost", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"6.6.6.6, 203.0.113.50"}}, "203.0.113.50"},
		{"proxy, two header lines", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"6.6.6.6", "203.0.113.50"}}, "203.0.113.50"},
		{"proxy, single entry", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"203.0.113.50"}}, "203.0.113.50"},
		{"proxy, entry with port", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"203.0.113.50:6000"}}, "203.0.113.50"},
		{"proxy, ipv6 entry", "[::1]:1", map[string][]string{"X-Forwarded-For": {"2001:db8::5"}}, "2001:db8::5"},
		{"proxy, mapped entry", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"::ffff:203.0.113.50"}}, "203.0.113.50"},
		{"proxy, garbage entry", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"<script>"}}, "127.0.0.1"},
		{"proxy, zoned entry", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"fe80::1%eth0"}}, "127.0.0.1"},
		{"proxy, X-Real-IP only", "127.0.0.1:1", map[string][]string{"X-Real-Ip": {"203.0.113.51"}}, "203.0.113.51"},
		{"proxy, XFF wins over X-Real-IP", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"203.0.113.50"}, "X-Real-Ip": {"6.6.6.6"}}, "203.0.113.50"},
		{"mapped loopback peer is a proxy", "[::ffff:127.0.0.1]:1", map[string][]string{"X-Forwarded-For": {"9.9.9.9, 203.0.113.50"}}, "203.0.113.50"},
		{"no headers", "127.0.0.1:1", nil, "127.0.0.1"},
	} {
		if got := ClientIP(req(tc.remote, tc.headers)); got != tc.want {
			t.Fatalf("%s: ClientIP = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestClientIPHonoursTheConfiguredHops(t *testing.T) {
	t.Cleanup(func() { SetTrustedProxyHops(1) })
	r := req("127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"6.6.6.6, 203.0.113.50, 10.0.0.2"}})
	SetTrustedProxyHops(2)
	if got := ClientIP(r); got != "203.0.113.50" {
		t.Fatalf("2 hops: %q", got)
	}
	SetTrustedProxyHops(0)
	if got := ClientIP(r); got != "127.0.0.1" {
		t.Fatalf("0 hops must ignore forwarding headers: %q", got)
	}
	SetTrustedProxyHops(9)
	if got := ClientIP(r); got != "6.6.6.6" {
		t.Fatalf("more hops than entries: %q", got)
	}
}

func TestOriginatedOnThisBox(t *testing.T) {
	if !OriginatedOnThisBox(req("127.0.0.1:1", nil)) || !OriginatedOnThisBox(req("[::1]:1", nil)) || !OriginatedOnThisBox(req("[::ffff:127.0.0.1]:1", nil)) {
		t.Fatalf("a loopback peer with no forwarding header is local")
	}
	for _, h := range ProxyHeaders() {
		if OriginatedOnThisBox(req("127.0.0.1:1", map[string][]string{h: {"x"}})) {
			t.Fatalf("%s present: not local", h)
		}
	}
	for _, remote := range []string{"203.0.113.9:1", "[::ffff:203.0.113.9]:1", "[fe80::1%lo0]:1", "garbage", ""} {
		if OriginatedOnThisBox(req(remote, nil)) {
			t.Fatalf("%q: not local", remote)
		}
	}
}
