package epm

import (
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestBuildObservedSDNPeersFiltersToAdvertisementEvidence(t *testing.T) {
	t.Parallel()

	localID := mustPeerID(t)
	trustedID := mustPeerID(t)
	ipfsOnlyID := mustPeerID(t)

	trustedAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/dns4/relay.example/tcp/443/wss/p2p/%s", trustedID))
	if err != nil {
		t.Fatalf("multiaddr.NewMultiaddr failed: %v", err)
	}

	out := BuildObservedSDNPeers(
		&PeerGraphSnapshot{
			LocalPeerID: localID.String(),
			Nodes: []PeerNode{
				{
					PeerID:             localID.String(),
					IsOnline:           true,
					MultiformatAddress: []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/14001/p2p/%s", localID)},
				},
				{
					PeerID:             trustedID.String(),
					DN:                 "Trusted Relay",
					Organization:       "Space Data Network",
					TrustLevel:         "trusted",
					IsOnline:           true,
					MultiformatAddress: []string{trustedAddr.String()},
					LastSeen:           time.Now().UTC().Format(time.RFC3339),
				},
				{
					PeerID:             ipfsOnlyID.String(),
					IsOnline:           true,
					MultiformatAddress: []string{fmt.Sprintf("/ip4/203.0.113.20/tcp/4001/p2p/%s", ipfsOnlyID)},
				},
			},
			Edges: []PeerEdge{
				{
					SourcePeerID: localID.String(),
					TargetPeerID: trustedID.String(),
					Protocols:    []string{"/space-data-network/module-delivery/1.0.0"},
				},
				{
					SourcePeerID: localID.String(),
					TargetPeerID: ipfsOnlyID.String(),
					Protocols:    []string{"/ipfs/bitswap/1.2.0"},
				},
			},
		},
		[]*peers.TrustedPeer{
			{
				ID:           trustedID,
				Addrs:        []multiaddr.Multiaddr{trustedAddr},
				TrustLevel:   peers.Trusted,
				Name:         "Trusted Relay",
				Organization: "Space Data Network",
				Metadata: map[string]string{
					"agent_version": "sdn-server/2.0.2",
				},
			},
		},
		map[string][]string{
			trustedID.String(): {"spacedatanetwork/1.0.0"},
		},
	)

	if len(out) != 1 {
		t.Fatalf("observed SDN peer count = %d, want 1", len(out))
	}
	if out[0].ID != trustedID {
		t.Fatalf("observed peer ID = %s, want %s", out[0].ID, trustedID)
	}
	if out[0].Metadata["protocols"] != "/space-data-network/module-delivery/1.0.0" {
		t.Fatalf("protocol metadata = %q", out[0].Metadata["protocols"])
	}
	if out[0].Metadata["advertisement_flags"] != "spacedatanetwork/1.0.0" {
		t.Fatalf("advertisement_flags metadata = %q", out[0].Metadata["advertisement_flags"])
	}
}

func mustPeerID(t *testing.T) peer.ID {
	t.Helper()

	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("crypto.GenerateEd25519Key failed: %v", err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("peer.IDFromPublicKey failed: %v", err)
	}
	return pid
}
