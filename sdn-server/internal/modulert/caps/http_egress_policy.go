package caps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Outbound egress DESTINATION policy for the module `http` capability
// (security audit 2026-10-15, SEC-027).
//
// THE GAP
// -------
// http.request took any URL and followed redirects anywhere. An operator who
// approved `http` for a data fetcher also, without being told, gave that
// module:
//
//   - the node's own admin API on loopback (root on a desktop node, where the
//     loopback console used to be ambient authority),
//   - the unauthenticated Kubo RPC on 127.0.0.1:5002 (shutdown, pin rm, keys),
//   - cloud instance metadata (169.254.169.254: IAM credentials),
//   - every RFC 1918 / CGNAT / ULA service on the operator's network,
//
// and a compromised upstream could steer an honest module there with a 307.
//
// THE POLICY
// ----------
// Internal destinations are refused unless the operator names them in
// modules.egress_allow_internal. The check runs in the DIALER, on the address
// actually being connected to, after DNS resolution:
//
//   - every connection is checked, so every redirect hop is checked (a
//     redirect to a new host is a new dial);
//   - a hostname that resolves (or re-resolves: DNS rebinding) to an internal
//     address is refused at connect time, not at URL-parse time;
//   - no environment proxy is consulted (Proxy is nil), so HTTP_PROXY cannot
//     route around the check.
//
// Only http and https URLs are accepted, on the first request and on every
// redirect hop, and a redirect chain is capped at maxEgressRedirects.

// maxEgressRedirects bounds a redirect chain (net/http's own default is 10).
const maxEgressRedirects = 10

// ErrEgressRefused is wrapped by every destination refusal.
var ErrEgressRefused = errors.New("egress refused")

// internalPrefixes are destinations no module reaches without an operator
// allow-list entry, beyond what netip's IsLoopback/IsPrivate/IsLinkLocal*/
// IsMulticast/IsUnspecified already cover.
var internalPrefixes = mustPrefixes(
	"0.0.0.0/8",        // "this network"
	"100.64.0.0/10",    // CGNAT (RFC 6598); Alibaba metadata 100.100.100.200
	"192.0.0.0/24",     // IETF protocol assignments; Oracle metadata 192.0.0.192
	"192.0.2.0/24",     // TEST-NET-1
	"198.18.0.0/15",    // benchmarking
	"198.51.100.0/24",  // TEST-NET-2
	"203.0.113.0/24",   // TEST-NET-3
	"240.0.0.0/4",      // reserved, incl. 255.255.255.255
	"168.63.129.16/32", // Azure WireServer / metadata (a PUBLIC address)
	"::/96",            // IPv4-compatible (deprecated), incl. ::1 and ::
	"100::/64",         // discard
	"2001::/32",        // Teredo: tunnels to an embedded IPv4 address
	"2001:db8::/32",    // documentation
	"fec0::/10",        // site-local (deprecated)
)

func mustPrefixes(raw ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, r := range raw {
		out = append(out, netip.MustParsePrefix(r))
	}
	return out
}

var (
	siitPrefix  = netip.MustParsePrefix("::ffff:0:0:0/96") // IPv4-translated (RFC 2765 SIIT)
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local  = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour   = netip.MustParsePrefix("2002::/16")
)

// IsInternalAddress reports whether ip is a destination the module `http`
// capability refuses by default: loopback, unspecified, link-local (incl.
// 169.254.169.254 and fe80::/10), RFC 1918, CGNAT, ULA (fc00::/7), multicast,
// reserved and documentation ranges, the Azure metadata address, and IPv6
// forms that embed or tunnel to one of those (IPv4-mapped, IPv4-translated
// SIIT, NAT64, 6to4, Teredo).
func IsInternalAddress(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	if ip.Zone() != "" {
		return true
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, p := range internalPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	if ip.Is6() {
		b := ip.As16()
		switch {
		case siitPrefix.Contains(ip):
			return IsInternalAddress(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case nat64Prefix.Contains(ip):
			return IsInternalAddress(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case nat64Local.Contains(ip):
			return true
		case sixToFour.Contains(ip):
			return IsInternalAddress(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	return false
}

// egressAllowRule is one operator allow-list entry: an address prefix and an
// optional port (0 = any port).
type egressAllowRule struct {
	prefix netip.Prefix
	port   uint16
}

func (r egressAllowRule) allows(ip netip.Addr, port uint16) bool {
	return r.prefix.Contains(ip) && (r.port == 0 || r.port == port)
}

var egressInternalAllow atomic.Pointer[[]egressAllowRule]

// ParseEgressAllowEntry parses one modules.egress_allow_internal entry:
//
//	"10.20.0.0/16"     a CIDR, any port
//	"10.20.0.5"        one address, any port
//	"127.0.0.1:5001"   one address, one port
//	"[fd00::5]:8443"   one IPv6 address, one port
//
// Hostnames are refused on purpose: a name can be re-pointed (DNS rebinding),
// an address cannot.
func ParseEgressAllowEntry(raw string) (egressAllowRule, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return egressAllowRule{}, fmt.Errorf("empty entry")
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		if p.Addr().Zone() != "" {
			return egressAllowRule{}, fmt.Errorf("%q: scoped addresses are not accepted", raw)
		}
		if p.Addr().Is4In6() {
			// Destinations are compared unmapped, so a mapped-form CIDR
			// ("::ffff:127.0.0.0/104") never matched anything. Take it as
			// the IPv4 prefix it names, or refuse it.
			if p.Bits() < 96 {
				return egressAllowRule{}, fmt.Errorf("%q: an IPv4-mapped prefix must be /96 or longer; write the IPv4 CIDR instead", raw)
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return egressAllowRule{prefix: p.Masked()}, nil
	}
	if a, err := netip.ParseAddr(s); err == nil {
		if a.Zone() != "" {
			return egressAllowRule{}, fmt.Errorf("%q: scoped addresses are not accepted", raw)
		}
		a = a.Unmap()
		return egressAllowRule{prefix: netip.PrefixFrom(a, a.BitLen())}, nil
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		a := ap.Addr()
		if a.Zone() != "" {
			return egressAllowRule{}, fmt.Errorf("%q: scoped addresses are not accepted", raw)
		}
		if ap.Port() == 0 {
			return egressAllowRule{}, fmt.Errorf("%q: port 0 is not a port", raw)
		}
		a = a.Unmap()
		return egressAllowRule{prefix: netip.PrefixFrom(a, a.BitLen()), port: ap.Port()}, nil
	}
	return egressAllowRule{}, fmt.Errorf("%q is not an IP address, IP:port or CIDR (hostnames are not accepted)", raw)
}

// SetEgressInternalAllowList installs the operator's explicit exceptions
// (modules.egress_allow_internal). Entries that do not parse are returned and
// grant nothing. Passing nil clears the list, so every internal destination
// is refused again.
func SetEgressInternalAllowList(entries []string) (invalid []string) {
	rules := make([]egressAllowRule, 0, len(entries))
	for _, e := range entries {
		rule, err := ParseEgressAllowEntry(e)
		if err != nil {
			invalid = append(invalid, e)
			continue
		}
		rules = append(rules, rule)
	}
	egressInternalAllow.Store(&rules)
	return invalid
}

// checkEgressDestination is the policy: nil when ip:port may be dialled.
func checkEgressDestination(ip netip.Addr, port uint16) error {
	ip = ip.Unmap()
	if !IsInternalAddress(ip) {
		return nil
	}
	if rules := egressInternalAllow.Load(); rules != nil {
		for _, rule := range *rules {
			if rule.allows(ip, port) {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %s is an internal address (loopback, link-local, private, CGNAT, metadata or reserved); an operator may allow it with modules.egress_allow_internal",
		ErrEgressRefused, netip.AddrPortFrom(ip, port))
}

// egressDialControl runs on every connection attempt, with the RESOLVED
// address — the point where DNS rebinding and redirects can no longer hide
// the destination.
func egressDialControl(_ context.Context, network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("%w: network %q", ErrEgressRefused, network)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable dial address %q", ErrEgressRefused, address)
	}
	return checkEgressDestination(ap.Addr(), ap.Port())
}

// egressTransport is the one transport every module http.request uses.
var egressTransport = &http.Transport{
	Proxy: nil, // never an environment proxy: it would dial for us, unchecked
	DialContext: (&net.Dialer{
		Timeout:        30 * time.Second,
		KeepAlive:      30 * time.Second,
		ControlContext: egressDialControl,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// checkEgressURL refuses non-http(s) schemes and URLs whose host is an
// internal address literal (a fast, clear refusal; the dialer remains the
// authority for names).
func checkEgressURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: no URL", ErrEgressRefused)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("%w: scheme %q (only http and https)", ErrEgressRefused, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: URL has no host", ErrEgressRefused)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		port := uint16(0)
		if p := u.Port(); p != "" {
			if n, perr := strconv.ParseUint(p, 10, 16); perr == nil {
				port = uint16(n)
			}
		} else if strings.EqualFold(u.Scheme, "https") {
			port = 443
		} else {
			port = 80
		}
		return checkEgressDestination(ip, port)
	}
	return nil
}

// egressCheckRedirect re-validates every redirect hop's URL and caps the
// chain; the dialer re-checks the resolved address of each new connection.
func egressCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxEgressRedirects {
		return fmt.Errorf("%w: stopped after %d redirects", ErrEgressRefused, maxEgressRedirects)
	}
	return checkEgressURL(req.URL)
}
