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
					AgentVersion:       "spacedatanetwork/1.0.3",
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
		nil,
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
	if out[0].Metadata["agent_version"] != "spacedatanetwork/1.0.3" {
		t.Fatalf("agent_version metadata = %q", out[0].Metadata["agent_version"])
	}
}

func TestBuildObservedSDNPeersIncludesAdvertisementOnlyPeersWithKnownAddresses(t *testing.T) {
	t.Parallel()

	localID := mustPeerID(t)
	discoveredID := mustPeerID(t)

	discoveredAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/dns4/relay.example/tcp/443/wss/p2p/%s", discoveredID))
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
			},
		},
		nil,
		map[string][]string{
			discoveredID.String(): {"spacedatanetwork/1.0.0"},
		},
		map[string][]string{
			discoveredID.String(): {discoveredAddr.String()},
		},
	)

	if len(out) != 1 {
		t.Fatalf("observed SDN peer count = %d, want 1", len(out))
	}
	if out[0].ID != discoveredID {
		t.Fatalf("observed peer ID = %s, want %s", out[0].ID, discoveredID)
	}
	if len(out[0].Addrs) != 1 || out[0].Addrs[0].String() != discoveredAddr.String() {
		t.Fatalf("observed peer addrs = %v, want [%s]", out[0].Addrs, discoveredAddr)
	}
	if out[0].Metadata["advertisement_flags"] != "spacedatanetwork/1.0.0" {
		t.Fatalf("advertisement_flags metadata = %q", out[0].Metadata["advertisement_flags"])
	}
	if out[0].Metadata["agent_version"] != "spacedatanetwork/1.0.0" {
		t.Fatalf("agent_version metadata = %q", out[0].Metadata["agent_version"])
	}
}

func TestBuildObservedSDNPeersIncludesConnectedSDNDesktopWithoutAdvertisement(t *testing.T) {
	t.Parallel()

	localID := mustPeerID(t)
	desktopID := mustPeerID(t)

	out := BuildObservedSDNPeers(
		&PeerGraphSnapshot{
			LocalPeerID: localID.String(),
			Nodes: []PeerNode{
				{
					PeerID:   localID.String(),
					IsOnline: true,
				},
				{
					PeerID:             desktopID.String(),
					IsOnline:           true,
					AgentVersion:       "kubo/0.39.0/sdn-desktop",
					MultiformatAddress: []string{fmt.Sprintf("/ip4/203.0.113.20/tcp/4001/p2p/%s", desktopID)},
				},
			},
			Edges: []PeerEdge{
				{
					SourcePeerID: localID.String(),
					TargetPeerID: desktopID.String(),
					Protocols:    []string{"/ipfs/id/1.0.0"},
				},
			},
		},
		nil,
		nil,
		nil,
	)

	if len(out) != 1 {
		t.Fatalf("observed SDN peer count = %d, want 1", len(out))
	}
	if out[0].ID != desktopID {
		t.Fatalf("observed peer ID = %s, want %s", out[0].ID, desktopID)
	}
	if out[0].Metadata["agent_version"] != "kubo/0.39.0/sdn-desktop" {
		t.Fatalf("agent_version metadata = %q", out[0].Metadata["agent_version"])
	}
}

// TestCountSDNPeersSplitsConnectedFromKnown proves the SDN peer counts served
// to the app boards are a strict subset of the libp2p swarm: an IPFS-only peer
// is never counted, a connected peer speaking an SDN protocol is the headline
// "connected" number, and an advertisement-discovered peer that is not
// currently connected only raises "known".
func TestCountSDNPeersSplitsConnectedFromKnown(t *testing.T) {
	t.Parallel()

	localID := mustPeerID(t)
	sdnConnectedID := mustPeerID(t)
	ipfsOnlyID := mustPeerID(t)
	sdnOfflineID := mustPeerID(t)

	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID.String(),
		Nodes: []PeerNode{
			{PeerID: localID.String(), IsOnline: true, AgentVersion: "spacedatanetwork/1.0.3"},
			{PeerID: sdnConnectedID.String(), IsOnline: true},
			{PeerID: ipfsOnlyID.String(), IsOnline: true, AgentVersion: "kubo/0.28.0"},
			{PeerID: sdnOfflineID.String(), IsOnline: false},
		},
		Edges: []PeerEdge{
			{
				SourcePeerID: localID.String(),
				TargetPeerID: sdnConnectedID.String(),
				Protocols:    []string{"/spacedatanetwork/sds-exchange/1.0.0"},
			},
			{
				SourcePeerID: localID.String(),
				TargetPeerID: ipfsOnlyID.String(),
				Protocols:    []string{"/ipfs/bitswap/1.2.0", "/ipfs/kad/1.0.0"},
			},
		},
	}

	counts := CountSDNPeers(
		snapshot,
		nil,
		// The offline peer is known only through SDN advertisement discovery.
		map[string][]string{sdnOfflineID.String(): {"sdn/1.0.3"}},
		nil,
	)

	if counts.Connected != 1 {
		t.Fatalf("Connected = %d, want 1 (only the SDN-protocol peer; the IPFS-only peer must not count)", counts.Connected)
	}
	if counts.Known != 2 {
		t.Fatalf("Known = %d, want 2 (connected SDN peer + advertisement-discovered offline SDN peer)", counts.Known)
	}
	if counts.Known != len(BuildObservedSDNPeers(snapshot, nil, map[string][]string{sdnOfflineID.String(): {"sdn/1.0.3"}}, nil)) {
		t.Fatalf("Known must equal the observed SDN peer list served at /api/peers/sdn")
	}
	if counts.Connected > counts.Known {
		t.Fatalf("Connected (%d) must never exceed Known (%d)", counts.Connected, counts.Known)
	}
}

func TestCountSDNPeersNilSnapshot(t *testing.T) {
	t.Parallel()

	if got := CountSDNPeers(nil, nil, nil, nil); got.Connected != 0 || got.Known != 0 {
		t.Fatalf("CountSDNPeers(nil) = %+v, want zero counts", got)
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
