package peers

import (
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

func deniedPeers(t *testing.T) []peer.ID {
	t.Helper()
	var out []peer.ID
	for _, id := range trustanchors.Load().Denylist.PeerIDs {
		p, err := peer.Decode(id)
		if err != nil {
			t.Fatalf("denylisted PeerID %s does not decode: %v", id, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		t.Fatal("the SEC-001 denylist names no peer")
	}
	return out
}

// SEC-001: the gater refuses the published identity in both directions, even
// when an operator (or a persisted registry) has it at the highest trust level.
func TestGaterRefusesTheDenylistedIdentityWhateverItsTrust(t *testing.T) {
	for _, strict := range []bool{false, true} {
		registry := NewRegistry(strict, nil)
		gater := NewTrustedConnectionGater(registry)
		var blocked []string
		gater.SetBlockedCallback(func(_ peer.ID, reason string) { blocked = append(blocked, reason) })
		for _, p := range deniedPeers(t) {
			// A registry persisted before SEC-001 may still hold it at Admin.
			registry.mu.Lock()
			registry.peers[p] = &TrustedPeer{ID: p, TrustLevel: Admin}
			registry.mu.Unlock()
			if gater.InterceptPeerDial(p) {
				t.Fatalf("strict=%v: dial to the denylisted peer was allowed", strict)
			}
			if gater.InterceptAddrDial(p, nil) {
				t.Fatalf("strict=%v: address dial to the denylisted peer was allowed", strict)
			}
			if gater.InterceptSecured(network.DirInbound, p, nil) {
				t.Fatalf("strict=%v: inbound connection from the denylisted peer was allowed", strict)
			}
			if gater.InterceptSecured(network.DirOutbound, p, nil) {
				t.Fatalf("strict=%v: outbound connection to the denylisted peer was allowed", strict)
			}
		}
		if len(blocked) == 0 || blocked[0] != "SEC-001 denylisted identity" {
			t.Fatalf("blocked callback reasons = %v", blocked)
		}
	}
}

func TestGaterStillAdmitsOrdinaryPeers(t *testing.T) {
	gater := NewTrustedConnectionGater(NewRegistry(false, nil))
	p, err := peer.Decode("16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U")
	if err != nil {
		t.Fatal(err)
	}
	if !gater.InterceptPeerDial(p) || !gater.InterceptSecured(network.DirInbound, p, nil) {
		t.Fatal("an ordinary, non-denylisted peer was refused")
	}
}

func TestConfigTrustedPeersDropTheDenylistedIdentity(t *testing.T) {
	var addrs []string
	for _, p := range deniedPeers(t) {
		addrs = append(addrs, "/ip4/159.203.150.8/tcp/4004/ws/p2p/"+p.String())
	}
	addrs = append(addrs, "/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U")
	registry, _, _, err := InitializeFromConfig(RegistryConfig{TrustedPeers: addrs})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range deniedPeers(t) {
		if _, err := registry.GetPeer(p); err == nil {
			t.Fatalf("the denylisted peer %s was added to the trusted registry", p)
		}
	}
	celestrak, _ := peer.Decode("16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U")
	if _, err := registry.GetPeer(celestrak); err != nil {
		t.Fatalf("an ordinary trusted peer was dropped: %v", err)
	}
}

// Verifier S2: the daemon registers peers.trusted_peers through AddPeer /
// UpdatePeer (internal/node/node.go upsertConfiguredTrustedPeer), not through
// InitializeFromConfig. The registry itself refuses the denylisted identity any
// positive trust, so every path drops it, and a persisted entry reads as Never.
func TestRegistryRefusesTrustForTheDenylistedIdentityOnEveryPath(t *testing.T) {
	registry := NewRegistry(false, nil)
	for _, p := range deniedPeers(t) {
		for _, level := range []TrustLevel{Limited, Standard, Trusted, Admin, Ultimate} {
			if err := registry.AddPeer(&TrustedPeer{ID: p, TrustLevel: level}); !errors.Is(err, trustanchors.ErrDenied) {
				t.Fatalf("AddPeer(level %d) = %v, want ErrDenied", level, err)
			}
		}
		// Recording it in order to block it is allowed.
		if err := registry.AddPeer(&TrustedPeer{ID: p, TrustLevel: Never}); err != nil {
			t.Fatalf("AddPeer(Never) = %v", err)
		}
		if err := registry.UpdatePeer(&TrustedPeer{ID: p, TrustLevel: Trusted}); !errors.Is(err, trustanchors.ErrDenied) {
			t.Fatalf("UpdatePeer(Trusted) = %v, want ErrDenied", err)
		}
		if err := registry.SetTrustLevel(p, Admin); !errors.Is(err, trustanchors.ErrDenied) {
			t.Fatalf("SetTrustLevel(Admin) = %v, want ErrDenied", err)
		}
		// A persisted pre-SEC-001 entry at Admin reads as Never everywhere.
		registry.mu.Lock()
		registry.peers[p] = &TrustedPeer{ID: p, TrustLevel: Admin}
		registry.mu.Unlock()
		if got := registry.GetTrustLevel(p); got != Never {
			t.Fatalf("GetTrustLevel = %d, want Never", got)
		}
		if registry.IsAllowed(p) || registry.IsTrusted(p) || registry.IsAdmin(p) {
			t.Fatal("a persisted denylisted peer is still allowed, trusted or admin")
		}
	}
	// Ordinary peers are untouched.
	other, err := peer.Decode("16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddPeer(&TrustedPeer{ID: other, TrustLevel: Trusted}); err != nil {
		t.Fatalf("AddPeer(ordinary) = %v", err)
	}
	if registry.GetTrustLevel(other) != Trusted {
		t.Fatal("ordinary peer lost its trust")
	}
}
