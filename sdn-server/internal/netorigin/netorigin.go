// Package netorigin answers two questions about an inbound HTTP request, the
// same way everywhere in the node:
//
//   - did it originate on this machine? (OriginatedOnThisBox)
//   - which client address does it stand for? (ClientIP)
//
// Every "local only" gate and every per-IP rate limit used to carry its own
// copy of these rules, and the copies had drifted: one list of proxy headers
// left out X-Forwarded-Proto, and three client-IP helpers took the LEFTMOST
// X-Forwarded-For entry, which the client writes (security audit 2026-10-15,
// SEC-062). They now share this package.
//
// # Origin on this box
//
// A loopback RemoteAddr proves only that the LAST hop was local. With a
// reverse proxy on the same machine (the deployed shape: nginx terminating
// TLS in front of a loopback listener) every internet client arrives from
// 127.0.0.1. Forwarding headers are therefore never trusted to GRANT anything;
// their mere presence refuses. A proxy that adds none of them cannot be
// detected here, which is why no loopback rule on its own is an authority:
// the console additionally requires a per-launch secret (internal/auth).
//
// # Client address
//
// The node trusts exactly the proxies on its own loopback interface, and
// TrustedProxyHops of them (default 1). A proxy appends the address it saw to
// X-Forwarded-For (nginx $proxy_add_x_forwarded_for), so the entry the
// nearest trusted proxy wrote is the RIGHTMOST one; everything to its left was
// supplied by the client and is attacker-controlled.
package netorigin

import (
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

// proxyHeaders are the headers whose presence is evidence that a request
// crossed a forwarding hop. The list is deliberately broad: a false positive
// costs a local caller a refusal, a false negative hands the internet a
// loopback privilege.
var proxyHeaders = []string{
	"X-Forwarded-For",
	"X-Real-Ip",
	"Forwarded",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Forwarded-Server",
	"X-Forwarded-Prefix",
	"X-Original-Forwarded-For",
	"Via",
	"X-Client-Ip",
	"True-Client-Ip",
	"Cf-Connecting-Ip",
	"Fastly-Client-Ip",
	"X-Cluster-Client-Ip",
}

// ProxyHeaders returns a copy of the headers CrossedAProxy treats as evidence
// of a forwarding hop.
func ProxyHeaders() []string {
	return append([]string(nil), proxyHeaders...)
}

// CrossedAProxy reports whether r carries any forwarding header. Used only to
// REFUSE loopback-only privileges, never to grant anything.
func CrossedAProxy(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, name := range proxyHeaders {
		for _, v := range r.Header.Values(name) {
			if strings.TrimSpace(v) != "" {
				return true
			}
		}
	}
	return false
}

// ParseIP parses an address as it appears in RemoteAddr or a forwarding
// header: "1.2.3.4", "1.2.3.4:5678", "::1", "[::1]:5678". An IPv4-mapped IPv6
// address is returned as its IPv4 form, so "::ffff:127.0.0.1" and "127.0.0.1"
// are one client. A scoped address ("fe80::1%en0") is refused (nil): no
// remote client legitimately presents one, and the zone would otherwise let
// one address hash into many rate-limit buckets.
func ParseIP(raw string) net.IP {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	if strings.Contains(s, "%") {
		return nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

// IsLoopbackRemote reports whether a RemoteAddr is a loopback address
// (127.0.0.0/8, ::1, or an IPv4-mapped loopback).
func IsLoopbackRemote(remoteAddr string) bool {
	ip := ParseIP(remoteAddr)
	return ip != nil && ip.IsLoopback()
}

// OriginatedOnThisBox reports whether r came from a loopback peer AND carries
// no evidence of a forwarding hop.
func OriginatedOnThisBox(r *http.Request) bool {
	if r == nil {
		return false
	}
	return !CrossedAProxy(r) && IsLoopbackRemote(r.RemoteAddr)
}

// trustedProxyHops is how many proxies on this machine's loopback interface
// sit in front of the node. 0 ignores every forwarding header.
var trustedProxyHops atomic.Int32

func init() { trustedProxyHops.Store(1) }

// SetTrustedProxyHops sets how many loopback reverse proxies are trusted to
// append to X-Forwarded-For (admin.trusted_proxy_hops). Negative values are
// treated as 0.
func SetTrustedProxyHops(n int) {
	if n < 0 {
		n = 0
	}
	trustedProxyHops.Store(int32(n))
}

// TrustedProxyHops returns the configured number of trusted loopback proxies.
func TrustedProxyHops() int { return int(trustedProxyHops.Load()) }

// ClientIP returns the address r stands for, in canonical form.
//
//   - A non-loopback peer is the client: forwarding headers from anyone else
//     are never read.
//   - A loopback peer is a trusted reverse proxy (up to TrustedProxyHops of
//     them). The client is the X-Forwarded-For entry the outermost trusted
//     proxy appended: counting from the RIGHT, never from the left.
//   - With no X-Forwarded-For, X-Real-IP is used when it parses as an IP.
//   - Anything unparseable falls back to the peer address, never to the raw
//     header text.
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	peer := ParseIP(r.RemoteAddr)
	if peer == nil {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		return strings.TrimSpace(host)
	}
	hops := TrustedProxyHops()
	if !peer.IsLoopback() || hops == 0 {
		return peer.String()
	}

	var entries []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(line, ",") {
			if p := strings.TrimSpace(part); p != "" {
				entries = append(entries, p)
			}
		}
	}
	if len(entries) > 0 {
		idx := len(entries) - hops
		if idx < 0 {
			idx = 0
		}
		if ip := ParseIP(entries[idx]); ip != nil {
			return ip.String()
		}
		return peer.String()
	}
	if ip := ParseIP(r.Header.Get("X-Real-Ip")); ip != nil {
		return ip.String()
	}
	return peer.String()
}
