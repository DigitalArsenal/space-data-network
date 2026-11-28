package epm

import (
	"context"
	"strings"
	"testing"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestBuildGraphSnapshotIncludesConnectedPeerAddrs(t *testing.T) {
	h1, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create h1: %v", err)
	}
	defer h1.Close()

	h2, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create h2: %v", err)
	}
	defer h2.Close()

	h3, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create h3: %v", err)
	}
	defer h3.Close()

	// h2 is in the registry with NO configured addresses (the inbound-peer case
	// where GeoIP previously saw an empty address list).
	reg := peers.NewRegistry(false, nil)
	if err := reg.AddPeer(&peers.TrustedPeer{
		ID:         h2.ID(),
		Name:       "Inbound Registry Peer",
		TrustLevel: peers.Standard,
	}); err != nil {
		t.Fatalf("add registry peer: %v", err)
	}

	ctx := context.Background()
	if err := h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}); err != nil {
		t.Fatalf("connect h1->h2: %v", err)
	}
	// h3 is connected without being in the registry.
	if err := h1.Connect(ctx, peer.AddrInfo{ID: h3.ID(), Addrs: h3.Addrs()}); err != nil {
		t.Fatalf("connect h1->h3: %v", err)
	}

	snap := BuildGraphSnapshot(h1, reg)
	byID := map[string]PeerNode{}
	for _, n := range snap.Nodes {
		byID[n.PeerID] = n
	}

	for _, id := range []peer.ID{h2.ID(), h3.ID()} {
		n, ok := byID[id.String()]
		if !ok {
			t.Fatalf("connected peer %s missing from snapshot", id)
		}
		if !n.IsOnline {
			t.Fatalf("connected peer %s should be online", id)
		}
		hasIPv4 := false
		for _, addr := range n.MultiformatAddress {
			if strings.Contains(addr, "/ip4/127.0.0.1/") {
				hasIPv4 = true
				break
			}
		}
		if !hasIPv4 {
			t.Fatalf("connected peer %s missing live remote multiaddr in %v", id, n.MultiformatAddress)
		}
	}
}
