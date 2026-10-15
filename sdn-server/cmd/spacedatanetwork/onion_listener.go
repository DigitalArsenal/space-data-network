package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/netorigin"
)

// The Tor onion service gets its own loopback socket (security audit
// 2026-10-15, round 2: the onion service defeated SEC-062's premise).
//
// The hidden service used to forward straight to admin.listen_addr. Tor
// forwards raw TCP from its own process on this machine, so every onion
// client reached the admin listener from 127.0.0.1 with no forwarding header,
// which every "originated on this box" rule reads as a local caller:
//
//   - remote_requires_sealed was skipped for it (a cookie or bearer session
//     from anywhere on the Tor network was accepted as local);
//   - ClientIP trusted its X-Forwarded-For / X-Real-IP, so each onion client
//     chose its own rate-limit bucket;
//   - an auth-off node behind the onion passed adminListenerSafety, because
//     the listener itself was loopback.
//
// Now, when the onion service fronts the admin surface, the node opens a
// second, ephemeral loopback socket only Tor is told about, and every request
// on it has its peer rewritten to netorigin.OnionClientAddr before the admin
// chain sees it: not loopback, not private, one shared rate-limit bucket. A
// local process that finds the socket gains nothing from it: it is treated as
// a remote, anonymous client. adminListenerSafety refuses an auth-off node
// whose admin surface the onion service publishes.

// defaultAdminListenAddr is admin.listen_addr when the key is unset.
const defaultAdminListenAddr = "127.0.0.1:5001"

// onionFrontsAdmin reports whether the configured onion service forwards to
// the admin listener: no hidden_service_target (the default is the admin
// listener), or a target on the admin listener's port. Any same-port target is
// treated as the admin listener, whatever host it names, so an alias
// ("localhost:5001", "[::1]:5001", "0.0.0.0:5001", the box's LAN address)
// cannot route around the dedicated socket.
func onionFrontsAdmin(cfg *config.Config) bool {
	if cfg == nil || !cfg.Admin.Enabled || !cfg.Tor.Enabled || !cfg.Tor.HiddenServiceEnabled {
		return false
	}
	target := strings.TrimSpace(cfg.Tor.HiddenServiceTarget)
	if target == "" {
		return true
	}
	adminAddr := strings.TrimSpace(cfg.Admin.ListenAddr)
	if adminAddr == "" {
		adminAddr = defaultAdminListenAddr
	}
	_, adminPort, err := net.SplitHostPort(adminAddr)
	if err != nil {
		return true
	}
	if strings.HasPrefix(target, ":") {
		return strings.TrimPrefix(target, ":") == adminPort
	}
	_, targetPort, err := net.SplitHostPort(target)
	if err != nil {
		// Unparseable: tor.Start refuses it; treat it as the admin listener
		// so nothing here relaxes on a typo.
		return true
	}
	return targetPort == adminPort
}

// listenOnionAdmin opens the ephemeral loopback socket the onion service
// forwards to, and asserts the bound address really is loopback.
func listenOnionAdmin() (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("onion service listener: %w", err)
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("onion service listener bound a non-loopback address %s", ln.Addr())
	}
	return ln, nil
}

// onionClientHandler marks every request from the onion socket as an
// anonymous remote client before the admin chain sees it.
func onionClientHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marked := r.WithContext(r.Context())
		marked.RemoteAddr = netorigin.OnionClientAddr
		next.ServeHTTP(w, marked)
	})
}
