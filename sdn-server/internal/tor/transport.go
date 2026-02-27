package tor

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// OutboundMode controls how outbound HTTP requests are routed.
type OutboundMode string

const (
	// OutboundTorOnly routes all HTTP through Tor SOCKS; fails if Tor is down.
	OutboundTorOnly OutboundMode = "tor-only"

	// OutboundTorPreferred tries Tor first, falls back to clearnet.
	OutboundTorPreferred OutboundMode = "tor-preferred"

	// OutboundClearnet makes direct connections (no Tor proxy).
	OutboundClearnet OutboundMode = "clearnet"
)

// NewTorTransport returns an *http.Transport that routes through a Tor
// SOCKS5h proxy at the given address (e.g. "127.0.0.1:9050").
// The "h" in SOCKS5h means the proxy resolves DNS, preventing DNS leaks.
func NewTorTransport(socksAddr string) (*http.Transport, error) {
	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create SOCKS5 dialer for %s: %w", socksAddr, err)
	}

	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS5 dialer does not support DialContext")
	}

	return &http.Transport{
		DialContext:           contextDialer.DialContext,
		MaxIdleConns:         100,
		MaxIdleConnsPerHost:  10,
		IdleConnTimeout:      90 * time.Second,
		TLSHandshakeTimeout:  30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		Proxy:                 nil, // bypass env vars — we dial through SOCKS directly
	}, nil
}

// NewFallbackTransport returns an http.RoundTripper that tries Tor first
// and falls back to clearnet if mode is OutboundTorPreferred.
func NewFallbackTransport(socksAddr string, mode OutboundMode) (http.RoundTripper, error) {
	switch mode {
	case OutboundClearnet:
		return http.DefaultTransport, nil
	case OutboundTorOnly:
		return NewTorTransport(socksAddr)
	case OutboundTorPreferred:
		torTransport, err := NewTorTransport(socksAddr)
		if err != nil {
			// Can't even create the dialer — fall back immediately.
			log.Warnf("tor transport unavailable, using clearnet: %v", err)
			return http.DefaultTransport, nil
		}
		return &fallbackTransport{
			torTransport:      torTransport,
			clearnetTransport: http.DefaultTransport,
			socksAddr:         socksAddr,
		}, nil
	default:
		return nil, fmt.Errorf("unknown outbound mode: %q", mode)
	}
}

// Transport returns an *http.Transport configured for this runtime's SOCKS proxy.
// Returns nil if the runtime is nil (Tor not running).
func (r *Runtime) Transport() (*http.Transport, error) {
	if r == nil || r.proxyURL == "" {
		return nil, nil
	}

	parsed, err := url.Parse(r.proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL %q: %w", r.proxyURL, err)
	}

	return NewTorTransport(parsed.Host)
}

// fallbackTransport tries Tor first and falls back to clearnet on dial failure.
type fallbackTransport struct {
	torTransport      http.RoundTripper
	clearnetTransport http.RoundTripper
	socksAddr         string
}

func (ft *fallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := ft.torTransport.RoundTrip(req)
	if err == nil {
		return resp, nil
	}

	// Only fall back on connection-level errors, not HTTP errors.
	if !isDialError(err) {
		return nil, err
	}

	log.Warnf("tor proxy at %s unreachable for %s %s, falling back to clearnet: %v",
		ft.socksAddr, req.Method, req.URL.Redacted(), err)

	return ft.clearnetTransport.RoundTrip(req)
}

// isDialError returns true if the error is a connection-level failure
// (can't reach the SOCKS proxy) rather than an HTTP-level error.
func isDialError(err error) bool {
	if err == nil {
		return false
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	return false
}
