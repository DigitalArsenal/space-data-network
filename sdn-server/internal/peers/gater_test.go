package peers

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

func TestTrustedConnectionGater_Block(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")

	// Initially not blocked
	if gater.IsBlocked(peerID) {
		t.Error("Peer should not be blocked initially")
	}

	// Block peer
	gater.Block(peerID)
	if !gater.IsBlocked(peerID) {
		t.Error("Peer should be blocked after Block()")
	}

	// Verify blocked peer is in list
	blocked := gater.ListBlocked()
	if len(blocked) != 1 || blocked[0] != peerID {
		t.Error("Blocked peer should be in list")
	}

	// Unblock peer
	gater.Unblock(peerID)
	if gater.IsBlocked(peerID) {
		t.Error("Peer should not be blocked after Unblock()")
	}
}

func TestTrustedConnectionGater_InterceptPeerDial_NonStrict(t *testing.T) {
	registry := NewRegistry(false, nil) // Non-strict mode
	gater := NewTrustedConnectionGater(registry)

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")

	// Should allow unknown peers in non-strict mode
	if !gater.InterceptPeerDial(peerID) {
		t.Error("Should allow unknown peers in non-strict mode")
	}

	// Block peer and verify dial is rejected
	gater.Block(peerID)
	if gater.InterceptPeerDial(peerID) {
		t.Error("Should reject blocked peers")
	}
}

func TestTrustedConnectionGater_InterceptPeerDial_Strict(t *testing.T) {
	registry := NewRegistry(true, nil) // Strict mode
	gater := NewTrustedConnectionGater(registry)

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")

	// Should reject unknown peers in strict mode
	if gater.InterceptPeerDial(peerID) {
		t.Error("Should reject unknown peers in strict mode")
	}

	// Add peer to registry with Standard trust
	registry.AddPeer(&TrustedPeer{
		ID:         peerID,
		TrustLevel: Standard,
	})

	// Should allow known peers
	if !gater.InterceptPeerDial(peerID) {
		t.Error("Should allow known peers in registry")
	}

	// Set peer to Untrusted
	registry.SetTrustLevel(peerID, Untrusted)

	// Should reject untrusted peers
	if gater.InterceptPeerDial(peerID) {
		t.Error("Should reject untrusted peers")
	}
}

func TestTrustedConnectionGater_InterceptAccept(t *testing.T) {
	registry := NewRegistry(true, nil)
	gater := NewTrustedConnectionGater(registry)

	// Accept should always return true (peer ID not known yet)
	if !gater.InterceptAccept(nil) {
		t.Error("InterceptAccept should always return true")
	}
}

func TestTrustBasedRateLimiter(t *testing.T) {
	registry := NewRegistry(false, nil)

	peerID1, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	peerID2, _ := peer.Decode("12D3KooWNvSZnPi3RrhrTwEY4LuuBeB6K6facKUCJcyWG1aoDd2p")
	peerID3, _ := peer.Decode("12D3KooWP5MYTnN8DcQDw7aDUFZY2vQAhvMwZZZ1XN3U9Wh3mJUW")

	registry.AddPeer(&TrustedPeer{ID: peerID1, TrustLevel: Limited})
	registry.AddPeer(&TrustedPeer{ID: peerID2, TrustLevel: Standard})
	registry.AddPeer(&TrustedPeer{ID: peerID3, TrustLevel: Admin})

	limiter := NewTrustBasedRateLimiter(registry, 100, 1000, 50)

	// Limited peer should get reduced limits
	mps1, mpm1, burst1 := limiter.GetLimits(peerID1)
	if mps1 != 10 || mpm1 != 100 || burst1 != 10 {
		t.Errorf("Limited peer limits: got mps=%v mpm=%v burst=%v, want mps=10 mpm=100 burst=10",
			mps1, mpm1, burst1)
	}

	// Standard peer should get base limits
	mps2, mpm2, burst2 := limiter.GetLimits(peerID2)
	if mps2 != 100 || mpm2 != 1000 || burst2 != 50 {
		t.Errorf("Standard peer limits: got mps=%v mpm=%v burst=%v, want mps=100 mpm=1000 burst=50",
			mps2, mpm2, burst2)
	}

	// Admin peer should get higher limits
	mps3, mpm3, burst3 := limiter.GetLimits(peerID3)
	if mps3 != 10000 || mpm3 != 100000 || burst3 != 500 {
		t.Errorf("Admin peer limits: got mps=%v mpm=%v burst=%v, want mps=10000 mpm=100000 burst=500",
			mps3, mpm3, burst3)
	}
}

// TestTrustBasedRateLimiter_UsesEffectiveTrustLevel is the minor-fix
// regression test: GetLimits must consult EffectiveTrustLevel (the same
// LIVE, computed-validity-augmented accessor IsAllowed consults), not the
// direct-assignment-only GetTrustLevel — otherwise a peer the connection
// gater treats as allowed (computed validity floors it at Marginal) could
// still be handed the 0,0,0 "should not happen" limits reserved for
// directly Untrusted/Never peers.
func TestTrustBasedRateLimiter_UsesEffectiveTrustLevel(t *testing.T) {
	registry := NewRegistry(true, nil) // strict mode: unknown peers start Untrusted
	registry.SetRootIdentity(testPeerID3)

	g := trust.NewGraph()
	subject := testPeerID1.String()
	root := testPeerID3.String()
	for _, signer := range []string{"s1", "s2", "s3"} {
		mustSetEdge(t, g, root, signer, Marginal.EdgeWeight())
		mustSetEdge(t, g, signer, subject, Marginal.EdgeWeight())
	}
	registry.SetTrustGraph(g)

	if !registry.IsAllowed(testPeerID1) {
		t.Fatal("test setup: peer should be computed-valid and allowed")
	}

	limiter := NewTrustBasedRateLimiter(registry, 100, 1000, 50)
	mps, mpm, burst := limiter.GetLimits(testPeerID1)

	if mps == 0 && mpm == 0 && burst == 0 {
		t.Fatalf("GetLimits gave 0,0,0 for a computed-valid (allowed) peer: mps=%v mpm=%v burst=%v", mps, mpm, burst)
	}
	// EffectiveTrustLevel floors a computed-valid peer at Marginal, which
	// is numerically the same tier as the legacy "Limited" rate bucket.
	if mps != 10 || mpm != 100 || burst != 10 {
		t.Fatalf("GetLimits = mps=%v mpm=%v burst=%v, want the Marginal/Limited bucket (10, 100, 10)", mps, mpm, burst)
	}
}

func TestTrustedConnectionGater_BlockedCallback(t *testing.T) {
	registry := NewRegistry(true, nil)
	gater := NewTrustedConnectionGater(registry)

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")

	callbackCalled := false
	var blockedPeer peer.ID
	var blockedReason string

	gater.SetBlockedCallback(func(p peer.ID, reason string) {
		callbackCalled = true
		blockedPeer = p
		blockedReason = reason
	})

	// Trigger a block by trying to dial unknown peer in strict mode
	gater.InterceptPeerDial(peerID)

	if !callbackCalled {
		t.Error("Callback should be called when connection is blocked")
	}
	if blockedPeer != peerID {
		t.Errorf("Callback peer mismatch: got %s, want %s", blockedPeer, peerID)
	}
	if blockedReason != "not in registry (strict mode)" {
		t.Errorf("Callback reason mismatch: got %q", blockedReason)
	}
}
