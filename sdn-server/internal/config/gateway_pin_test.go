package config

// Gateway loop G.4: the OPT-IN gateway.pin surface. Pinning must never
// default on — an empty config pins nothing — and standard spellings
// ("OMM", "omm", "OMM.fbs", wildcards) must normalize consistently.

import "testing"

func TestGatewayPinnedStandardDefaultsOff(t *testing.T) {
	var g GatewayConfig
	if g.PinnedStandard("16Uiu2HAmPeer", "OMM.fbs") {
		t.Fatalf("empty gateway.pin must pin nothing")
	}
	if peers := g.PinnedPeers("OMM.fbs"); len(peers) != 0 {
		t.Fatalf("empty gateway.pin peers = %v", peers)
	}
}

func TestGatewayPinnedStandardMatching(t *testing.T) {
	g := GatewayConfig{Pin: []GatewayPinEntry{
		{Peer: "16Uiu2HAmProviderFixture", Standard: "OMM"},
		{Peer: "16Uiu2HAmOther", Standard: "all"},
	}}
	cases := []struct {
		peer, schema string
		want         bool
	}{
		{"16Uiu2HAmProviderFixture", "OMM.fbs", true},
		{"16Uiu2HAmProviderFixture", "omm", true},
		{"16Uiu2HAmProviderFixture", "OMM", true},
		{"16Uiu2HAmProviderFixture", "CAT.fbs", false}, // standard not pinned
		{"16Uiu2HAmUnknown", "OMM.fbs", false},         // peer not pinned
		{"16Uiu2HAmOther", "CAT.fbs", true},            // "all" wildcard
		{"16Uiu2HAmOther", "SPW.fbs", true},
		{"16Uiu2HAmProviderFixture", "", false}, // concrete standard required
	}
	for _, c := range cases {
		if got := g.PinnedStandard(c.peer, c.schema); got != c.want {
			t.Fatalf("PinnedStandard(%q, %q) = %v, want %v", c.peer, c.schema, got, c.want)
		}
	}
	peers := g.PinnedPeers("OMM.fbs")
	if len(peers) != 2 {
		t.Fatalf("PinnedPeers(OMM) = %v, want both entries", peers)
	}
	peers = g.PinnedPeers("CAT.fbs")
	if len(peers) != 1 || peers[0] != "16Uiu2HAmOther" {
		t.Fatalf("PinnedPeers(CAT) = %v, want the wildcard peer only", peers)
	}
}

func TestGatewayPinStarWildcard(t *testing.T) {
	g := GatewayConfig{Pin: []GatewayPinEntry{{Peer: "16Uiu2HAmPeer", Standard: "*"}}}
	if !g.PinnedStandard("16Uiu2HAmPeer", "MPE.fbs") {
		t.Fatalf("'*' must pin every standard for the peer")
	}
}
