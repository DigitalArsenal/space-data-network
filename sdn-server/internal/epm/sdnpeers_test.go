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

// SUPERSEDED BY OWNER RULING 2026-07-30. This test asserted that a peer known
// ONLY through an SDN advertisement — never dialled, never seen — belonged on
// the board. The owner looked at the result of exactly that rule (34 such rows)
// and said: "I have no idea what these peers are that are in the table" and "The
// Peers table should not ever show peers that have never been seen, UNLESS they
// have been added manually and 'pinned'". Advertisement discovery is still how
// the node LEARNS of a peer; it is no longer a reason to seat one. The
// addresses it contributes are still merged onto rows admitted for a real
// reason — which is what this test now pins.
func TestBuildObservedSDNPeersDoesNotSeatAdvertisementOnlyPeers(t *testing.T) {
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

	if len(out) != 0 {
		t.Fatalf("observed SDN peer count = %d, want 0: an advertisement is a claim anyone can publish into a public DHT, not evidence this node has ever met the peer", len(out))
	}

	// Pin the same peer and it is admitted — and the addresses learned from the
	// advertisement are still merged onto the row, so the pin is dialable.
	pinned := BuildObservedSDNPeers(
		&PeerGraphSnapshot{
			LocalPeerID: localID.String(),
			Nodes: []PeerNode{
				{PeerID: localID.String(), IsOnline: true},
				{PeerID: discoveredID.String(), IsOnline: false, Pinned: true, PinSource: peers.PinSourceOperator},
			},
		},
		nil,
		map[string][]string{discoveredID.String(): {"spacedatanetwork/1.0.0"}},
		map[string][]string{discoveredID.String(): {discoveredAddr.String()}},
	)
	if len(pinned) != 1 || pinned[0].ID != discoveredID {
		t.Fatalf("pinned advertisement peer = %v, want exactly %s", pinned, discoveredID)
	}
	if len(pinned[0].Addrs) != 1 || pinned[0].Addrs[0].String() != discoveredAddr.String() {
		t.Fatalf("pinned peer addrs = %v, want [%s]", pinned[0].Addrs, discoveredAddr)
	}
	if pinned[0].Metadata["advertisement_flags"] != "spacedatanetwork/1.0.0" {
		t.Fatalf("advertisement_flags metadata = %q", pinned[0].Metadata["advertisement_flags"])
	}
	if pinned[0].Metadata["agent_version"] != "" {
		t.Fatalf("agent_version = %q: an advertisement flag is not an observed agent version", pinned[0].Metadata["agent_version"])
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
			// Identity, not protocol, is what admits a peer (owner rule
			// 2026-07-28). This peer both speaks SDN and says it is one.
			{PeerID: sdnConnectedID.String(), IsOnline: true, AgentVersion: "spacedatanetwork/1.0.4"},
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
		t.Fatalf("Connected = %d, want 1 (only the peer identifying as SDN; the IPFS-only peer must not count)", counts.Connected)
	}
	// AMENDED BY OWNER RULING 2026-07-30. "Known" no longer means "anyone who
	// ever advertised": an advertisement-discovered peer that has never been
	// connected is not on the board, so it is not counted either. Known is now
	// pinned-or-connected, which is what the page can actually show — the whole
	// point of the ruling was that a headline number counting rows nobody could
	// account for is a dishonest number.
	if counts.Known != 1 {
		t.Fatalf("Known = %d, want 1 (the connected SDN peer; an advertisement-only peer is not on the board)", counts.Known)
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

// The board shows Space Data Network nodes and nothing else (owner rule,
// 2026-07-28). Speaking an SDN protocol fluently is not the same as BEING one
// of our nodes: an upstream kubo build can talk to us perfectly while
// identifying as "kubo/0.40.0-dev/", and such a peer was showing up on the
// accounts board as a row nobody could account for.
func TestObservedSDNPeersAdmitsBySDNIdentityNotByProtocol(t *testing.T) {
	t.Parallel()

	localID := mustPeerID(t)
	kuboID := mustPeerID(t)         // talks SDN, identifies as kubo
	sdnID := mustPeerID(t)          // talks SDN, identifies as SDN
	staleOfflineID := mustPeerID(t) // known SDN node, currently unreachable

	sdnProtocol := []string{"/spacedatanetwork/sds-exchange/1.0.0"}
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID.String(),
		Nodes: []PeerNode{
			{PeerID: localID.String(), IsOnline: true, AgentVersion: "spacedatanetwork/1.0.4"},
			{PeerID: kuboID.String(), IsOnline: true, AgentVersion: "kubo/0.40.0-dev/"},
			{PeerID: sdnID.String(), IsOnline: true, AgentVersion: "spacedatanetwork/1.0.4"},
			{PeerID: staleOfflineID.String(), IsOnline: false},
		},
		Edges: []PeerEdge{
			{SourcePeerID: localID.String(), TargetPeerID: kuboID.String(), Protocols: sdnProtocol},
			{SourcePeerID: localID.String(), TargetPeerID: sdnID.String(), Protocols: sdnProtocol},
		},
	}

	// The stale peer is offline but the registry remembers it as an SDN node.
	staleRegistry := &peers.TrustedPeer{
		ID:       staleOfflineID,
		Metadata: map[string]string{"agent_version": "spacedatanetwork/1.0.0"},
	}

	// Discovered the same way a real offline SDN node is: by its advertisement.
	staleFlags := map[string][]string{staleOfflineID.String(): {"sdn/1.0.0"}}
	observed := BuildObservedSDNPeers(snapshot, []*peers.TrustedPeer{staleRegistry}, staleFlags, nil)
	got := map[string]bool{}
	for _, entry := range observed {
		got[entry.ID.String()] = true
	}

	if got[kuboID.String()] {
		t.Fatalf("a peer identifying as kubo was admitted to the SDN board: %v", got)
	}
	if !got[sdnID.String()] {
		t.Fatalf("a peer identifying as spacedatanetwork was excluded: %v", got)
	}
	// AMENDED BY OWNER RULING 2026-07-30: "When a peer drops off the network it
	// should just disappear." The 2026-07-28 membership rule this test guards
	// (identity, not protocol) is UNCHANGED — but it decides WHETHER a peer is
	// one of ours, not whether an absent one keeps a seat. Remembering an SDN
	// node we cannot reach is exactly what produced a board full of "last seen
	// — never". Unreachable is still not foreign; it is simply not present.
	if got[staleOfflineID.String()] {
		t.Fatal("an offline, unpinned peer kept its seat; a peer that drops off the network must disappear")
	}
	// Pinning is the ONLY way to keep it, and then it is there for a reason the
	// row can state.
	pinnedSnapshot := &PeerGraphSnapshot{LocalPeerID: localID.String(), Nodes: append(
		append([]PeerNode{}, snapshot.Nodes[:len(snapshot.Nodes)-1]...),
		PeerNode{PeerID: staleOfflineID.String(), IsOnline: false, Pinned: true, PinSource: peers.PinSourceOperator},
	), Edges: snapshot.Edges}
	keptItsSeat := false
	for _, entry := range BuildObservedSDNPeers(pinnedSnapshot, []*peers.TrustedPeer{staleRegistry}, staleFlags, nil) {
		if entry.ID == staleOfflineID {
			keptItsSeat = true
			if entry.Metadata["source"] != peers.PinSourceOperator {
				t.Fatalf("pinned offline peer source = %q, want %q", entry.Metadata["source"], peers.PinSourceOperator)
			}
		}
	}
	if !keptItsSeat {
		t.Fatal("a PINNED offline SDN node must keep its seat")
	}
	if got[localID.String()] {
		t.Fatal("the local node must never appear as a row on its own board")
	}
}
