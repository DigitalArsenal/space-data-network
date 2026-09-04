package node

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// A trusted publisher's signed profile is what names its records in the
// dashboard, so connecting to one asks for the profile even when the peer
// never advertised; a stranger that neither advertises nor is trusted is
// still left alone.
func TestDirectoryEPMSourceForPeerAsksTrustedPeersOnConnect(t *testing.T) {
	trusted, err := peer.Decode("16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := peer.Decode("16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45")
	if err != nil {
		t.Fatal(err)
	}
	reg := peers.NewRegistry(false, nil)
	if err := reg.AddPeer(&peers.TrustedPeer{ID: trusted, TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	n := &Node{peerRegistry: reg}
	if got := n.directoryEPMSourceForPeer(trusted, "peer-connect"); got != "trusted-peer" {
		t.Fatalf("trusted peer on connect -> %q, want trusted-peer", got)
	}
	if got := n.directoryEPMSourceForPeer(stranger, "peer-connect"); got != "" {
		t.Fatalf("stranger on connect -> %q, want no request", got)
	}
	if got := n.directoryEPMSourceForPeer(stranger, "bootstrap"); got != "bootstrap" {
		t.Fatalf("explicit source -> %q, want it unchanged", got)
	}
}
