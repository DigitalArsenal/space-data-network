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
//
// The agent is presented to users, so it is exactly the suite string and
// nothing else: keeping the kubo build on as "+kubo/..." metadata put kubo text
// straight back into the board's protocol column, and the owner ruled it out
// the same day ("somehow a kubo is back in the protocol list").
func TestUserAgentPresentsAsSpaceDataNetwork(t *testing.T) {
	agent := GetUserAgentVersion()

	if agent != SDNAgentName+"/"+SDNAgentVersion {
		t.Fatalf("agent %q must be exactly %q — the same string every other node in the suite presents",
			agent, SDNAgentName+"/"+SDNAgentVersion)
	}
	// The membership rule downstream is a case-insensitive substring match on
	// "spacedatanetwork"; guard the exact spelling here so a rename cannot
	// silently drop this node off the board.
	if !strings.Contains(strings.ToLower(agent), "spacedatanetwork") {
		t.Fatalf("agent %q would fail the SDN membership rule", agent)
	}
	// No kubo text reaches a user-facing surface, in any spelling.
	if strings.Contains(strings.ToLower(agent), "kubo") {
		t.Fatalf("agent %q leaks the kubo build into a user-facing list", agent)
	}
	// The build is still knowable, just not advertised.
	if GetVersionInfo().Version != CurrentVersionNumber {
		t.Fatalf("kubo build version is no longer reported by GetVersionInfo")
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
