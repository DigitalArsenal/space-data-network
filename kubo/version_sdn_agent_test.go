package ipfs

import (
	"strings"
	"testing"
)

// This fork is a Space Data Network node built on kubo, and the SDN accounts
// board decides membership from the libp2p identify agent-version, not from
// protocol participation (owner rule 2026-07-28: "This table should ONLY show
// spacedatanetwork nodes"). An upstream "kubo/..." string made this node an
// unexplainable row on our own board, and then dropped it off entirely.
func TestUserAgentPresentsAsSpaceDataNetwork(t *testing.T) {
	agent := GetUserAgentVersion()

	if !strings.HasPrefix(agent, SDNAgentName+"/") {
		t.Fatalf("agent %q does not present as a Space Data Network node", agent)
	}
	// The membership rule downstream is a case-insensitive substring match on
	// "spacedatanetwork"; guard the exact spelling here so a rename cannot
	// silently drop this node off the board.
	if !strings.Contains(strings.ToLower(agent), "spacedatanetwork") {
		t.Fatalf("agent %q would fail the SDN membership rule", agent)
	}
	// Nothing diagnostic is lost: the kubo build is still identifiable.
	if !strings.Contains(agent, "kubo/"+CurrentVersionNumber) {
		t.Fatalf("agent %q dropped the kubo build metadata", agent)
	}
	if strings.HasPrefix(agent, "kubo/") {
		t.Fatalf("agent %q still leads with the kubo identity", agent)
	}
}

func TestUserAgentSuffixStillApplies(t *testing.T) {
	original := userAgentSuffix
	t.Cleanup(func() { userAgentSuffix = original })

	SetUserAgentSuffix("testsuffix")
	agent := GetUserAgentVersion()
	if !strings.Contains(agent, "testsuffix") {
		t.Fatalf("agent %q lost its configured suffix", agent)
	}
	if !strings.HasPrefix(agent, SDNAgentName+"/") {
		t.Fatalf("agent %q lost the SDN identity when a suffix was set", agent)
	}
}
