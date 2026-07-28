package node

// A node that DIALS a producer but does not TRUST it transfers nothing, stays
// connected, and logs nothing above DEBUG. That is how a stale trusted_peers
// entry survived a working bootstrap entry on host-01 for a full day on
// 2026-07-28. The mismatch must be announced at boot.

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const (
	trustTestProducerID = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"
	trustTestStaleID    = "16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4"
)

func mustDecodePeer(t *testing.T, s string) peer.ID {
	t.Helper()
	id, err := peer.Decode(s)
	if err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return id
}

func TestUntrustedBootstrapPeerIsAnnounced(t *testing.T) {
	registry := peers.NewRegistry(false, nil)
	// The live host-01 shape: trust was assigned to a STALE identity while the
	// bootstrap entry named the real producer.
	stale := mustDecodePeer(t, trustTestStaleID)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: stale, TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	producer := mustDecodePeer(t, trustTestProducerID)
	if registry.IsTrusted(producer) {
		t.Fatal("test premise broken: the producer must start untrusted")
	}

	n := &Node{peerRegistry: registry}
	flagged := n.untrustedBootstrapPeers([]bootstrap.PeerInfo{
		{AddrInfo: peer.AddrInfo{ID: producer}},
		{AddrInfo: peer.AddrInfo{ID: stale}},
	})

	var ids []string
	for _, id := range flagged {
		ids = append(ids, id.String())
	}
	joined := strings.Join(ids, ",")
	if !strings.Contains(joined, trustTestProducerID) {
		t.Fatalf("the dialled-but-untrusted producer was not flagged; flagged: %q", joined)
	}
	if strings.Contains(joined, trustTestStaleID) {
		t.Fatalf("a TRUSTED bootstrap peer was wrongly flagged; flagged: %q", joined)
	}
}

func TestFullyTrustedBootstrapSetIsSilent(t *testing.T) {
	registry := peers.NewRegistry(false, nil)
	producer := mustDecodePeer(t, trustTestProducerID)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: producer, TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	n := &Node{peerRegistry: registry}
	if flagged := n.untrustedBootstrapPeers([]bootstrap.PeerInfo{{AddrInfo: peer.AddrInfo{ID: producer}}}); len(flagged) != 0 {
		t.Fatalf("a correctly configured node flagged peers anyway: %v", flagged)
	}
}

// A node with no peer registry (construction paths that never wire one) must
// not panic on the check.
func TestUntrustedBootstrapCheckToleratesNoRegistry(t *testing.T) {
	n := &Node{}
	n.warnOnUntrustedBootstrapPeers([]bootstrap.PeerInfo{
		{AddrInfo: peer.AddrInfo{ID: mustDecodePeer(t, trustTestProducerID)}},
	})
}

// One peer reachable at several multiaddrs must be reported once, not once per
// address — the live fallback bootstrap set repeats each peer 3-4 times.
func TestUntrustedBootstrapPeerIsReportedOncePerPeer(t *testing.T) {
	registry := peers.NewRegistry(false, nil)
	producer := mustDecodePeer(t, trustTestProducerID)

	n := &Node{peerRegistry: registry}
	flagged := n.untrustedBootstrapPeers([]bootstrap.PeerInfo{
		{AddrInfo: peer.AddrInfo{ID: producer}},
		{AddrInfo: peer.AddrInfo{ID: producer}},
		{AddrInfo: peer.AddrInfo{ID: producer}},
	})
	if len(flagged) != 1 {
		t.Fatalf("one peer at three addresses produced %d warnings, want 1", len(flagged))
	}
}
