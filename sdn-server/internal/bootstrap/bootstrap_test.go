package bootstrap

import (
	"testing"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

func TestParseBootstrapAddress_WithPeerID(t *testing.T) {
	// Valid address with peer ID
	addr := "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n"

	info, err := ParseBootstrapAddress(addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.HasPinnedID {
		t.Error("expected HasPinnedID to be true")
	}

	if info.AddrInfo.ID.String() != "12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n" {
		t.Errorf("unexpected peer ID: %s", info.AddrInfo.ID)
	}

	if info.RawAddress != addr {
		t.Errorf("expected RawAddress to be %s, got %s", addr, info.RawAddress)
	}
}

func TestParseBootstrapAddress_WithoutPeerID(t *testing.T) {
	// Address without peer ID
	addr := "/ip4/127.0.0.1/tcp/4001"

	info, err := ParseBootstrapAddress(addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.HasPinnedID {
		t.Error("expected HasPinnedID to be false")
	}

	if len(info.AddrInfo.Addrs) != 1 {
		t.Errorf("expected 1 address, got %d", len(info.AddrInfo.Addrs))
	}
}

func TestParseBootstrapAddress_InvalidAddress(t *testing.T) {
	// Invalid multiaddr
	addr := "not-a-valid-multiaddr"

	_, err := ParseBootstrapAddress(addr)
	if err == nil {
		t.Error("expected error for invalid address")
	}
}

func TestParseBootstrapAddress_DNSAddr(t *testing.T) {
	// DNS address with peer ID
	addr := "/dnsaddr/bootstrap.example.com/p2p/12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n"

	info, err := ParseBootstrapAddress(addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.HasPinnedID {
		t.Error("expected HasPinnedID to be true for dnsaddr")
	}

	if info.AddrInfo.ID.String() != "12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n" {
		t.Errorf("unexpected peer ID: %s", info.AddrInfo.ID)
	}
}

func TestParseBootstrapAddresses_MixedAddresses(t *testing.T) {
	addresses := []string{
		"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n",
		"/ip4/127.0.0.2/tcp/4001", // No peer ID
		"/ip4/127.0.0.3/tcp/4001/p2p/12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe2Xo",
		"invalid-address", // Should be skipped
	}

	peers, err := ParseBootstrapAddresses(addresses)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 3 valid addresses (invalid one skipped)
	if len(peers) != 3 {
		t.Errorf("expected 3 peers, got %d", len(peers))
	}

	// Count pinned vs unpinned
	pinned := 0
	unpinned := 0
	for _, p := range peers {
		if p.HasPinnedID {
			pinned++
		} else {
			unpinned++
		}
	}

	if pinned != 2 {
		t.Errorf("expected 2 pinned peers, got %d", pinned)
	}
	if unpinned != 1 {
		t.Errorf("expected 1 unpinned peer, got %d", unpinned)
	}
}

func TestValidateBootstrapConfig(t *testing.T) {
	addresses := []string{
		"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n",
		"/ip4/127.0.0.2/tcp/4001", // Missing peer ID
		"/dnsaddr/bootstrap.example.com/p2p/12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe2Xo",
		"/ip4/192.168.1.1/udp/4001/quic-v1", // Missing peer ID
	}

	warnings := ValidateBootstrapConfig(addresses)

	if len(warnings) != 2 {
		t.Errorf("expected 2 warnings, got %d", len(warnings))
	}
}

func TestRequirePinnedPeerIDs(t *testing.T) {
	addresses := []string{
		"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n",
		"/ip4/127.0.0.2/tcp/4001",
		"/ip4/127.0.0.3/tcp/4001/p2p/12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe2Xo",
	}

	peers, _ := ParseBootstrapAddresses(addresses)
	pinned := RequirePinnedPeerIDs(peers)

	if len(pinned) != 2 {
		t.Errorf("expected 2 pinned peers, got %d", len(pinned))
	}

	for _, p := range pinned {
		if !p.HasPinnedID {
			t.Error("RequirePinnedPeerIDs returned unpinned peer")
		}
	}
}

func TestContainsP2PComponent(t *testing.T) {
	tests := []struct {
		addr     string
		expected bool
	}{
		{"/ip4/127.0.0.1/tcp/4001/p2p/QmTest", true},
		{"/ip4/127.0.0.1/tcp/4001/ipfs/QmTest", true}, // Legacy format
		{"/ip4/127.0.0.1/tcp/4001", false},
		{"/dnsaddr/example.com/p2p/QmTest", true},
		{"/dnsaddr/example.com", false},
	}

	for _, tc := range tests {
		result := containsP2PComponent(tc.addr)
		if result != tc.expected {
			t.Errorf("containsP2PComponent(%q) = %v, expected %v", tc.addr, result, tc.expected)
		}
	}
}

func TestParseBootstrapAddress_LegacyIPFS(t *testing.T) {
	// Legacy /ipfs/ format (should also work)
	addr := "/ip4/127.0.0.1/tcp/4001/ipfs/12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n"

	info, err := ParseBootstrapAddress(addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.HasPinnedID {
		t.Error("expected HasPinnedID to be true for legacy /ipfs/ format")
	}
}

func TestParseBootstrapAddress_QUICv1(t *testing.T) {
	// QUIC-v1 transport with peer ID
	addr := "/ip4/127.0.0.1/udp/4001/quic-v1/p2p/12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n"

	info, err := ParseBootstrapAddress(addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.HasPinnedID {
		t.Error("expected HasPinnedID to be true for QUIC-v1")
	}
}

func TestParseBootstrapAddress_WebSocket(t *testing.T) {
	// WebSocket transport with peer ID
	addr := "/ip4/127.0.0.1/tcp/8080/ws/p2p/12D3KooWLr1gYejUTeriAsSu6roR2aQ423G3Q4fFTqzqSwTsMz9n"

	info, err := ParseBootstrapAddress(addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.HasPinnedID {
		t.Error("expected HasPinnedID to be true for WebSocket")
	}
}

func TestDefaultBootstrapAddresses_UsesRealPinnedPeers(t *testing.T) {
	addresses := DefaultBootstrapAddresses()
	if len(addresses) == 0 {
		t.Fatal("DefaultBootstrapAddresses returned no peers")
	}

	foundApprovedSeed := false
	for _, addr := range addresses {
		if addr == "/ip4/104.131.11.220/tcp/8080/ws/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45" {
			foundApprovedSeed = true
		}
		if addr == "/dnsaddr/bootstrap.digitalarsenal.io/p2p/QmBootstrap1" {
			t.Fatalf("placeholder bootstrap address leaked into defaults: %s", addr)
		}
		if _, err := ParseBootstrapAddress(addr); err != nil {
			t.Fatalf("DefaultBootstrapAddresses contained invalid peer %q: %v", addr, err)
		}
	}

	if !foundApprovedSeed {
		t.Fatal("DefaultBootstrapAddresses did not include the approved demo relay seed")
	}
}

func TestResolveBootstrapPeers_FallsBackToDefaultsWhenConfiguredListIsInvalid(t *testing.T) {
	peers, usedFallback, err := ResolveBootstrapPeers([]string{
		"/dnsaddr/bootstrap.digitalarsenal.io/p2p/QmBootstrap1",
	})
	if err != nil {
		t.Fatalf("ResolveBootstrapPeers failed: %v", err)
	}
	if !usedFallback {
		t.Fatal("ResolveBootstrapPeers did not fall back to defaults for invalid config")
	}
	if len(peers) == 0 {
		t.Fatal("ResolveBootstrapPeers returned no fallback peers")
	}
}

func TestResolveBootstrapPeers_PreservesConfiguredPinnedPeers(t *testing.T) {
	configured := dht.DefaultBootstrapPeers[0].String()

	peers, usedFallback, err := ResolveBootstrapPeers([]string{configured})
	if err != nil {
		t.Fatalf("ResolveBootstrapPeers failed: %v", err)
	}
	if usedFallback {
		t.Fatal("ResolveBootstrapPeers unexpectedly used fallback for valid configured peer")
	}
	if len(peers) != 1 {
		t.Fatalf("ResolveBootstrapPeers peer count = %d, want 1", len(peers))
	}
	if peers[0].RawAddress != configured {
		t.Fatalf("ResolveBootstrapPeers peer address = %q, want %q", peers[0].RawAddress, configured)
	}
}
