package main

import (
	"fmt"
	"net"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// adminListenerSafety refuses the one configuration that hands anonymous
// writes to the network (SEC-01): an admin listener bound to a routable
// address while admin.require_auth is false. With authentication off the
// admin surface serves every operator action to whoever connects, and a
// loopback check is no gate behind a reverse proxy; so authentication off is
// a loopback-only convenience, never a network posture. Returning an error
// stops the daemon before it binds.
func adminListenerSafety(cfg *config.Config) error {
	if cfg == nil || !cfg.Admin.Enabled || cfg.Admin.RequireAuth {
		return nil
	}
	addr := strings.TrimSpace(cfg.Admin.ListenAddr)
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("admin.listen_addr %q is not a loopback address while admin.require_auth is false: "+
		"authentication off exposes every operator action (publish, sync, archive, connectors) to anyone who can reach the listener. "+
		"Set admin.require_auth: true (the node's root key signs in without any seeding) or bind the admin listener to 127.0.0.1", addr)
}
