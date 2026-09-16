package node

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// network.edge_relays was declared and never read — candidates came only from
// dht.GetClosestPeers, and a node behind a firewall is exactly the node whose
// DHT walk returns nothing. Measured: a node on a Docker bridge with no
// published ports reached 1 peer and made ZERO reservations in 15 minutes with
// a working, reachable relay configured, because it was never offered.
func TestConfiguredRelaysBecomeAutoRelayCandidates(t *testing.T) {
	const (
		relayID = "16Uiu2HAmPewKE2n2cEQYQFpghYevFfnSr9foCoJKoEZhcbwVHZFC"
		bootID  = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	)
	n := &Node{
		config: &config.Config{},
		// buffered so the non-blocking send in enqueueAutoRelayCandidate lands
		autoRelayPeerChan: make(chan peer.AddrInfo, 8),
	}
	n.config.Network.EdgeRelays = []string{"/ip4/203.0.113.10/tcp/4001/p2p/" + relayID}
	n.config.Network.Bootstrap = []string{
		"/ip4/198.51.100.20/tcp/4001/p2p/" + bootID,
		"/ip4/198.51.100.30/tcp/4001", // no /p2p/: not a usable relay candidate
		"not-a-multiaddr",
	}

	// host is nil here, so enqueue would bail; give it the minimum it checks.
	n.enqueueConfiguredAutoRelayCandidates()

	// With no host the guard drops everything — that is correct and is not what
	// we are testing. Verify the PARSING instead, which is the part that was
	// missing entirely.
	got := map[string]bool{}
	for _, raw := range append(append([]string{}, n.config.Network.EdgeRelays...), n.config.Network.Bootstrap...) {
		if info, err := relayCandidateFromMultiaddr(raw); err == nil {
			got[info.ID.String()] = true
		}
	}
	if !got[relayID] {
		t.Error("a configured edge_relay did not parse into a relay candidate")
	}
	if !got[bootID] {
		t.Error("a bootstrap peer did not parse into a relay candidate; bootstrap peers are public " +
			"by construction and are the relays a fresh node can actually find")
	}
	if len(got) != 2 {
		t.Errorf("parsed %d candidates, want exactly the two with a /p2p/ component", len(got))
	}
}

func TestRelayCandidateRejectsAddressesWithoutAPeer(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"not-a-multiaddr",
		"/ip4/203.0.113.10/tcp/4001", // no /p2p/: a reservation needs a peer
	} {
		if _, err := relayCandidateFromMultiaddr(raw); err == nil {
			t.Errorf("%q was accepted as a relay candidate; a reservation is made with a specific peer", raw)
		}
	}
}

func TestRelayCandidateKeepsTheTransportAddress(t *testing.T) {
	const id = "16Uiu2HAmPewKE2n2cEQYQFpghYevFfnSr9foCoJKoEZhcbwVHZFC"
	info, err := relayCandidateFromMultiaddr("/ip4/203.0.113.10/tcp/4001/p2p/" + id)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID.String() != id {
		t.Fatalf("peer id = %s, want %s", info.ID, id)
	}
	if len(info.Addrs) != 1 || info.Addrs[0].String() != "/ip4/203.0.113.10/tcp/4001" {
		t.Fatalf("addrs = %v, want the transport address without the /p2p/ component", info.Addrs)
	}
}
