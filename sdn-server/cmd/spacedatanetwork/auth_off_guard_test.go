package main

import (
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// SEC-01: authentication off is a loopback-only convenience. A routable
// admin listener with require_auth false is refused before the node binds.
func TestAdminListenerSafetyRefusesRoutableListenerWithAuthOff(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.Enabled = true
	cfg.Admin.RequireAuth = false
	for _, addr := range []string{"0.0.0.0:5001", "10.0.0.5:5001", "[::]:5001", "203.0.113.4:443"} {
		cfg.Admin.ListenAddr = addr
		err := adminListenerSafety(cfg)
		if err == nil {
			t.Fatalf("%s with require_auth=false was accepted", addr)
		}
		if !strings.Contains(err.Error(), "require_auth") {
			t.Fatalf("%s: error does not name the fix: %v", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:5001", "[::1]:5001", "localhost:5001", ""} {
		cfg.Admin.ListenAddr = addr
		if err := adminListenerSafety(cfg); err != nil {
			t.Fatalf("loopback %q with require_auth=false refused: %v", addr, err)
		}
	}
	cfg.Admin.RequireAuth = true
	cfg.Admin.ListenAddr = "0.0.0.0:443"
	if err := adminListenerSafety(cfg); err != nil {
		t.Fatalf("routable listener with require_auth=true refused: %v", err)
	}
	cfg.Admin.RequireAuth = false
	cfg.Admin.Enabled = false
	if err := adminListenerSafety(cfg); err != nil {
		t.Fatalf("disabled admin listener refused: %v", err)
	}
}
