package epm

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// These tests encode the owner's rulings of 2026-07-30 for the peer board:
//
//	"The Peers table should not ever show peers that have never been seen,
//	 UNLESS they have been added manually and 'pinned'."
//	"When a peer drops off the network it should just disappear."
//	"I have no idea what these peers are that are in the table"
//
// The shapes below are taken from the LIVE feed measured that day
// (wss://sdn.spaceaware.io/ws/status): 36 rows, 33 offline, LAST_SEEN 0 on
// every single one, and 34 carrying the identical synthesized agent string
// "spacedatanetwork/1.0.0" because they were DHT rendezvous advertisements the
// node had never dialled.

const (
	localID     = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	strangerID  = "16Uiu2HAkuSSuf8u32gYvsSrxARFqBS4dTAjmVjmC1sVCNyGSNCP4"
	connectedID = "12D3KooWKh3diobFtzBk2RvdwR4TuFB8nkU31th8Mc2iKb7bZBWs"
	pinnedID    = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"
)

func observedIDs(t *testing.T, snapshot *PeerGraphSnapshot, flags map[string][]string) map[string]*peers.TrustedPeer {
	t.Helper()
	out := map[string]*peers.TrustedPeer{}
	for _, tp := range BuildObservedSDNPeers(snapshot, nil, flags, nil) {
		out[tp.ID.String()] = tp
	}
	return out
}

// An advertisement in a public DHT is a claim anyone can make. It must not seat
// a stranger on the operator's board.
func TestAdvertisementOnlyPeerIsNeverAdmitted(t *testing.T) {
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			{PeerID: strangerID, IsOnline: false},
		},
	}
	got := observedIDs(t, snapshot, map[string][]string{strangerID: {"spacedatanetwork/1.0.0"}})
	if _, ok := got[strangerID]; ok {
		t.Fatal("a never-contacted advertisement discovery reached the board")
	}
	if len(got) != 0 {
		t.Fatalf("board should be empty, got %d rows", len(got))
	}
}

// The advertisement flag must never be laundered into an agent version: that is
// how 34 rows all came to display a version this node had never observed.
func TestAdvertisementFlagIsNotAnAgentVersion(t *testing.T) {
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			// Pinned so it is admitted; the point is the agent string.
			{PeerID: strangerID, IsOnline: false, Pinned: true, PinSource: peers.PinSourceOperator},
		},
	}
	got := observedIDs(t, snapshot, map[string][]string{strangerID: {"spacedatanetwork/1.0.0"}})
	entry, ok := got[strangerID]
	if !ok {
		t.Fatal("pinned peer should be admitted")
	}
	if entry.Metadata["agent_version"] != "" {
		t.Fatalf("agent_version was synthesized from an advertisement flag: %q", entry.Metadata["agent_version"])
	}
}

// A connected SDN node is admitted and says so.
func TestConnectedSDNPeerIsAdmittedAsConnected(t *testing.T) {
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			{PeerID: connectedID, IsOnline: true, AgentVersion: "spacedatanetwork/1.0.4"},
		},
	}
	got := observedIDs(t, snapshot, nil)
	entry, ok := got[connectedID]
	if !ok {
		t.Fatal("a connected SDN peer must be on the board")
	}
	if entry.Metadata["source"] != "connected" {
		t.Fatalf("source = %q, want connected", entry.Metadata["source"])
	}
	if entry.Metadata["pinned"] == "true" {
		t.Fatal("a merely-connected peer is not pinned")
	}
}

// "When a peer drops off the network it should just disappear." — still true
// of a peer this node has NO record of ever having met. Nothing can be said
// about it, so it says nothing.
func TestUnpinnedPeerDisappearsWhenItGoesOffline(t *testing.T) {
	online := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			{PeerID: connectedID, IsOnline: true, AgentVersion: "spacedatanetwork/1.0.4"},
		},
	}
	if _, ok := observedIDs(t, online, nil)[connectedID]; !ok {
		t.Fatal("precondition: peer should be on the board while connected")
	}

	offline := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			// Same peer, same recorded agent version, no longer connected.
			{PeerID: connectedID, IsOnline: false, AgentVersion: "spacedatanetwork/1.0.4"},
		},
	}
	if _, ok := observedIDs(t, offline, nil)[connectedID]; ok {
		t.Fatal("a peer that dropped off the network is still on the board")
	}
}

// "...UNLESS they have been added manually and 'pinned'."
func TestPinnedPeerKeepsItsSeatWhileUnreachable(t *testing.T) {
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			{
				PeerID:    pinnedID,
				IsOnline:  false,
				Pinned:    true,
				PinSource: peers.PinSourceConfig,
				PinNote:   "/etc/space-data-network/config.yaml · peers.trusted_peers",
			},
		},
	}
	got := observedIDs(t, snapshot, nil)
	entry, ok := got[pinnedID]
	if !ok {
		t.Fatal("a pinned peer must keep its seat even though it has never been seen")
	}
	if entry.Metadata["source"] != peers.PinSourceConfig {
		t.Fatalf("source = %q, want %q", entry.Metadata["source"], peers.PinSourceConfig)
	}
	if entry.Metadata["pinned"] != "true" {
		t.Fatal("a config peer must be marked pinned")
	}
	if entry.Metadata["pin_note"] == "" {
		t.Fatal("a locked config row must name the real file and key an operator can edit")
	}
}

// A pin is enough on its own: an operator pinning a box by id has said what it
// is, and a fresh pin that has never been reached must still be visible.
func TestOperatorPinDoesNotNeedSDNEvidence(t *testing.T) {
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			{PeerID: pinnedID, IsOnline: false, Pinned: true, PinSource: peers.PinSourceOperator},
		},
	}
	entry, ok := observedIDs(t, snapshot, nil)[pinnedID]
	if !ok {
		t.Fatal("an operator pin with no SDN evidence must still be listed")
	}
	if entry.Metadata["source"] != peers.PinSourceOperator {
		t.Fatalf("source = %q, want %q", entry.Metadata["source"], peers.PinSourceOperator)
	}
}

// The live board, replayed: one self row plus 34 advertisement strangers and
// one config pin must collapse to exactly the pin.
func TestLiveBoardShapeCollapsesToRealNodes(t *testing.T) {
	nodes := []PeerNode{{PeerID: localID, IsOnline: true}}
	flags := map[string][]string{}
	for _, id := range []string{
		"16Uiu2HAkubW1wpDc43UpTkzcVLR8UEjLRDGpvUtcZ2vLgxKprPFj",
		"16Uiu2HAkuoZnuZk5GKPZbCoNQmvKmpqNyGXvSGmjnLxhPXpV6RUD",
		"16Uiu2HAkwthuxxPy48FheNJHhFqcTNyPzMBK3XZL5UyNMxLdEqfE",
	} {
		nodes = append(nodes, PeerNode{PeerID: id, IsOnline: false})
		flags[id] = []string{"spacedatanetwork/1.0.0"}
	}
	nodes = append(nodes, PeerNode{
		PeerID: pinnedID, IsOnline: false, Pinned: true,
		PinSource: peers.PinSourceConfig, PinNote: "/etc/space-data-network/config.yaml · peers.trusted_peers",
	})

	got := observedIDs(t, &PeerGraphSnapshot{LocalPeerID: localID, Nodes: nodes}, flags)
	if len(got) != 1 {
		t.Fatalf("board has %d rows, want exactly the 1 pinned node", len(got))
	}
	if _, ok := got[pinnedID]; !ok {
		t.Fatal("the surviving row must be the pinned node")
	}
}

// A RELEASE binary advertises "spacedatanetwork/<release tag>", not the bare
// suite version, so the version half of the agent string now varies from build
// to build. The membership gate has always matched the NAME half only
// (isSDNAgentVersion uses strings.Contains), and this holds it to that:
// whatever this build calls itself, this build must still recognise it.
//
// Without this, tightening the gate to an exact or version-sensitive match
// would drop every release node off every board in the fleet, and nothing else
// in the tree would notice.
func TestThisBuildRecognisesItsOwnAgentString(t *testing.T) {
	if !isSDNAgentVersion(versioninfo.AgentVersion) {
		t.Fatalf("this build advertises %q and its own membership gate rejects it", versioninfo.AgentVersion)
	}

	// The shapes a peer can now present, including the stamped release form
	// that did not exist before the agent string carried the build.
	for _, agent := range []string{
		versioninfo.AgentName + "/1.0.5",
		versioninfo.AgentName + "/1.0.5-beta.67",
		versioninfo.AgentName + "/2.0.0-rc.1+build.9",
	} {
		if !isSDNAgentVersion(agent) {
			t.Fatalf("membership gate rejected an SDN peer advertising %q", agent)
		}
	}
}

// Owner 2026-09-19: "there should be an 'offline nodes' menu as well that
// shows nodes that have been seen but are not currently online, and 'last
// seen' time". A node this box has actually met keeps a seat when it drops
// off — and the row says it is not connected, rather than claiming it is.
func TestSeenPeerKeepsItsSeatWhileOffline(t *testing.T) {
	seenAt := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			{
				PeerID:       connectedID,
				IsOnline:     false,
				AgentVersion: "spacedatanetwork/1.0.4",
				LastSeen:     seenAt.Format(time.RFC3339),
			},
		},
	}
	entry, ok := observedIDs(t, snapshot, nil)[connectedID]
	if !ok {
		t.Fatal("an SDN node this box has met must stay listed while offline")
	}
	if entry.Metadata["source"] != peers.PeerSourceSeen {
		t.Fatalf("source = %q, want %q", entry.Metadata["source"], peers.PeerSourceSeen)
	}
	if entry.Metadata["pinned"] == "true" {
		t.Fatal("a seen peer is not pinned")
	}
}

// The same seat, earned from the registry's own record rather than the
// snapshot's — the shape a real offline row arrives in.
func TestRegistryContactRecordAdmitsAnOfflinePeer(t *testing.T) {
	decoded, err := peer.Decode(connectedID)
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}
	registryPeer := &peers.TrustedPeer{
		ID:              decoded,
		Name:            "Test Node",
		LastConnected:   time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC),
		ConnectionCount: 4,
		Metadata:        map[string]string{"agent_version": "spacedatanetwork/1.0.4"},
	}
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			{PeerID: connectedID, IsOnline: false},
		},
	}
	observed := BuildObservedSDNPeers(snapshot, []*peers.TrustedPeer{registryPeer}, nil, nil)
	if len(observed) != 1 || observed[0].ID.String() != connectedID {
		t.Fatalf("a peer with a recorded connection must be listed, got %d rows", len(observed))
	}
	if observed[0].Metadata["source"] != peers.PeerSourceSeen {
		t.Fatalf("source = %q, want %q", observed[0].Metadata["source"], peers.PeerSourceSeen)
	}
	// The timestamp IS the offline row's content. This projection used to build
	// a fresh TrustedPeer and drop it, so every row read "0001-01-01".
	if !observed[0].LastConnected.Equal(registryPeer.LastConnected) {
		t.Fatalf("last connected = %v, want %v", observed[0].LastConnected, registryPeer.LastConnected)
	}
	if observed[0].ConnectionCount != registryPeer.ConnectionCount {
		t.Fatalf("connection count = %d, want %d", observed[0].ConnectionCount, registryPeer.ConnectionCount)
	}
}

// An advertisement is not a meeting: a peer that only ever announced itself
// stays out of BOTH lists, exactly as 2026-07-30 required.
func TestAdvertisementOnlyPeerIsStillNotSeen(t *testing.T) {
	snapshot := &PeerGraphSnapshot{
		LocalPeerID: localID,
		Nodes: []PeerNode{
			{PeerID: localID, IsOnline: true},
			{PeerID: strangerID, IsOnline: false, AgentVersion: "spacedatanetwork/1.0.0"},
		},
	}
	if _, ok := observedIDs(t, snapshot, map[string][]string{strangerID: {"sdn/1.0.3"}})[strangerID]; ok {
		t.Fatal("an advertisement with no record of contact was admitted as seen")
	}
}
