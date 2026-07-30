package bootstrap

import (
	"strings"
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

	foundSpaceaware := false
	foundCelestrak := false
	for _, addr := range addresses {
		if addr == "/ip4/159.203.150.8/tcp/4004/ws/p2p/"+bootstrapPeerSpaceaware {
			foundSpaceaware = true
		}
		if addr == "/ip4/167.172.219.213/tcp/4001/p2p/"+bootstrapPeerCelestrak {
			foundCelestrak = true
		}
		// bootstrap.spacedatanetwork.org was never allocated and the owner ruled
		// on 2026-07-30 that it never will be. A dnsaddr entry here is a
		// guaranteed NXDOMAIN dial, and a bootstrap dial that fails at startup is
		// never retried (ops-cluster-bootstrap-no-redial), so it burns a retry
		// slot permanently.
		if strings.Contains(addr, "bootstrap.spacedatanetwork.org") {
			t.Fatalf("unallocated dnsaddr bootstrap host leaked into defaults: %s", addr)
		}
		// Measured dead 2026-07-30: neither production host has ANY udp
		// listener, and neither serves 4001-on-host-01 or 8080.
		if strings.Contains(addr, "quic") {
			t.Fatalf("no production host has a udp listener; quic entry leaked into defaults: %s", addr)
		}
		if strings.Contains(addr, "/ip4/159.203.150.8/tcp/4001") ||
			strings.Contains(addr, "/tcp/8080/") {
			t.Fatalf("port measured CLOSED on the live host leaked into defaults: %s", addr)
		}
		if strings.Contains(addr, "16Uiu2HAmP8KTvYP2i7Ef2Lf7Vbn5beZf2aMTpq4pmQAK6SjRphYT") {
			t.Fatalf("retired SpaceAware full-node peer ID leaked into defaults: %s", addr)
		}
		// The legacy celestrak identity: its spacedatanetwork.service is stopped,
		// and libp2p aborts on a peer-id mismatch even with the port open.
		if strings.Contains(addr, "16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3") {
			t.Fatalf("stopped legacy celestrak peer ID leaked into defaults: %s", addr)
		}
		if addr == "/dnsaddr/bootstrap.digitalarsenal.io/p2p/QmBootstrap1" {
			t.Fatalf("placeholder bootstrap address leaked into defaults: %s", addr)
		}
		if strings.Contains(addr, "104.131.11.220") {
			t.Fatalf("retired demo relay address leaked into defaults: %s", addr)
		}
		if _, err := ParseBootstrapAddress(addr); err != nil {
			t.Fatalf("DefaultBootstrapAddresses contained invalid peer %q: %v", addr, err)
		}
	}

	if !foundSpaceaware || !foundCelestrak {
		t.Fatal("DefaultBootstrapAddresses did not include both measured-live production peers (sdn.spaceaware.io ws/4004 and celestrak.eth tcp/4001)")
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
