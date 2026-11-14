package bootstrap_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

func TestLocalDiscoveryConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		local      bool
	}{
		{"absent", "{}\n", false},
		{"explicit", "peers:\n  enable_mdns: false\nnetwork:\n  bootstrap: []\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			for round := 0; round < 3; round++ {
				cfg, err := config.Load(path)
				if err != nil {
					t.Fatal(err)
				}
				peers, fallback, err := bootstrap.ResolveBootstrapPeers(cfg.Network.Bootstrap)
				if err != nil {
					t.Fatal(err)
				}
				if tc.local {
					if cfg.Peers.EnableMDNS || len(peers) != 0 || fallback || cfg.Network.Bootstrap == nil {
						t.Fatal("local configuration restored discovery")
					}
				} else if !cfg.Peers.EnableMDNS || len(peers) == 0 {
					t.Fatal("absent keys lost defaults")
				}
				if err := config.Save(path, cfg); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNilBootstrapKeepsDefault(t *testing.T) {
	peers, fallback, err := bootstrap.ResolveBootstrapPeers(nil)
	if err != nil || !fallback || len(peers) == 0 {
		t.Fatal("nil no longer uses defaults", err)
	}
}
