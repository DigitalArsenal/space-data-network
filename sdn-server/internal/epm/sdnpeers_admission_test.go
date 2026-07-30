package epm

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
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

// "When a peer drops off the network it should just disappear."
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
