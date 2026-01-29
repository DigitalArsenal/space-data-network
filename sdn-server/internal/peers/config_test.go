package peers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePeerMultiaddr(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{
			name:    "valid multiaddr with peer ID",
			addr:    "/ip4/192.168.1.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
			wantErr: false,
		},
		{
			name:    "valid DNS multiaddr with peer ID",
			addr:    "/dns4/node.example.com/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
			wantErr: false,
		},
		{
			name:    "multiaddr without peer ID",
			addr:    "/ip4/192.168.1.1/tcp/4001",
			wantErr: true,
		},
		{
			name:    "invalid multiaddr",
			addr:    "not-a-multiaddr",
			wantErr: true,
		},
		{
			name:    "empty string",
			addr:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParsePeerMultiaddr(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePeerMultiaddr(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if info == nil {
					t.Fatal("Expected non-nil AddrInfo")
				}
				if info.ID.String() == "" {
					t.Error("Expected non-empty peer ID")
				}
				if len(info.Addrs) == 0 {
					t.Error("Expected at least one address")
				}
			}
		})
	}
}

func TestParsePeerAddrInfos(t *testing.T) {
	addrs := []string{
		"/ip4/192.168.1.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		"/ip4/192.168.1.2/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN", // Same peer, different addr
		"/ip4/10.0.0.1/tcp/4001/p2p/12D3KooWNvSZnPi3RrhrTwEY4LuuBeB6K6facKUCJcyWG1aoDd2p",
		"invalid-addr", // Should be skipped
	}

	infos := ParsePeerAddrInfos(addrs)

	if len(infos) != 2 {
		t.Errorf("Expected 2 unique peers, got %d", len(infos))
	}

	// Find the peer with 2 addresses
	for _, info := range infos {
		if info.ID.String() == "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN" {
			if len(info.Addrs) != 2 {
				t.Errorf("Expected 2 addresses for first peer, got %d", len(info.Addrs))
			}
		}
	}
}

func TestInitializeFromConfig_InMemory(t *testing.T) {
	cfg := RegistryConfig{
		StrictMode: false,
		TrustedPeers: []string{
			"/ip4/192.168.1.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
			"/ip4/10.0.0.1/tcp/4001/p2p/12D3KooWNvSZnPi3RrhrTwEY4LuuBeB6K6facKUCJcyWG1aoDd2p",
		},
		TrustBasedRateLimiting: true,
	}

	registry, gater, limiter, err := InitializeFromConfig(cfg)
	if err != nil {
		t.Fatalf("InitializeFromConfig failed: %v", err)
	}

	if registry == nil {
		t.Fatal("Expected non-nil registry")
	}
	if gater == nil {
		t.Fatal("Expected non-nil gater")
	}
	if limiter == nil {
		t.Fatal("Expected non-nil limiter")
	}

	// Check peers were added
	if registry.PeerCount() != 2 {
		t.Errorf("Expected 2 peers, got %d", registry.PeerCount())
	}

	// Check trust level
	peers := registry.ListPeers()
	for _, p := range peers {
		if p.TrustLevel != Trusted {
			t.Errorf("Peer %s should have Trusted level, got %s", p.ID.ShortString(), p.TrustLevel)
		}
	}
}

func TestInitializeFromConfig_WithJSONPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-peers-config-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := RegistryConfig{
		StrictMode:   true,
		RegistryPath: filepath.Join(tmpDir, "peers.json"),
		TrustedPeers: []string{
			"/ip4/192.168.1.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		},
	}

	registry, gater, limiter, err := InitializeFromConfig(cfg)
	if err != nil {
		t.Fatalf("InitializeFromConfig failed: %v", err)
	}

	if registry == nil || gater == nil {
		t.Fatal("Expected non-nil registry and gater")
	}
	if limiter != nil {
		t.Error("Expected nil limiter when rate limiting disabled")
	}

	if registry.PeerCount() != 1 {
		t.Errorf("Expected 1 peer, got %d", registry.PeerCount())
	}

	if !registry.IsStrictMode() {
		t.Error("Expected strict mode to be enabled")
	}

	// Verify persistence file was created
	if _, err := os.Stat(filepath.Join(tmpDir, "peers.json")); os.IsNotExist(err) {
		t.Error("Expected peers.json to be created")
	}
}

func TestInitializeFromConfig_DoesNotOverwriteExisting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-peers-config-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	registryPath := filepath.Join(tmpDir, "peers.json")

	// First initialization: add peer with Trusted level
	cfg := RegistryConfig{
		RegistryPath: registryPath,
		TrustedPeers: []string{
			"/ip4/192.168.1.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		},
	}

	registry, _, _, err := InitializeFromConfig(cfg)
	if err != nil {
		t.Fatalf("First init failed: %v", err)
	}

	// Change trust level to Admin
	peerID := registry.ListPeers()[0].ID
	registry.SetTrustLevel(peerID, Admin)

	// Second initialization: should not overwrite the Admin level
	registry2, _, _, err := InitializeFromConfig(cfg)
	if err != nil {
		t.Fatalf("Second init failed: %v", err)
	}

	tp, err := registry2.GetPeer(peerID)
	if err != nil {
		t.Fatalf("Failed to get peer: %v", err)
	}

	if tp.TrustLevel != Admin {
		t.Errorf("Expected Admin trust level (persisted), got %s", tp.TrustLevel)
	}
}

func TestInitializeFromConfig_SkipsInvalidAddrs(t *testing.T) {
	cfg := RegistryConfig{
		TrustedPeers: []string{
			"/ip4/192.168.1.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
			"invalid-multiaddr",
			"/ip4/192.168.1.1/tcp/4001", // No peer ID
		},
	}

	registry, _, _, err := InitializeFromConfig(cfg)
	if err != nil {
		t.Fatalf("InitializeFromConfig failed: %v", err)
	}

	// Only the valid peer should be added
	if registry.PeerCount() != 1 {
		t.Errorf("Expected 1 peer (invalid addrs skipped), got %d", registry.PeerCount())
	}
}
