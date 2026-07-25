package status

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/geoip"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/status/nst"
)

// two known-valid libp2p peer IDs (base58) used as fixtures.
const (
	selfPeerIDFixture = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	peerIDFixture     = "16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3"
)

// stubGeo returns a fixed Location for a single IP and zero for anything else.
type stubGeo struct {
	ip  string
	loc geoip.Location
}

func (s stubGeo) Lookup(ip string) geoip.Location {
	if ip == s.ip {
		return s.loc
	}
	return geoip.Location{}
}

func mustAddrs(t *testing.T, ss ...string) []multiaddr.Multiaddr {
	t.Helper()
	out := make([]multiaddr.Multiaddr, 0, len(ss))
	for _, s := range ss {
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			t.Fatalf("bad multiaddr %q: %v", s, err)
		}
		out = append(out, ma)
	}
	return out
}

func buildFixtureSet(t *testing.T) []byte {
	t.Helper()

	peerDecoded, err := peer.Decode(peerIDFixture)
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}

	snapshot := &epm.PeerGraphSnapshot{
		LocalPeerID: selfPeerIDFixture,
		Nodes: []epm.PeerNode{
			{
				PeerID:             selfPeerIDFixture,
				MultiformatAddress: []string{"/ip4/127.0.0.1/tcp/4001"},
				IsOnline:           true,
			},
			{
				PeerID:             peerIDFixture,
				MultiformatAddress: []string{"/ip4/81.2.69.142/tcp/4001"},
				IsOnline:           true,
				LastSeen:           "2026-07-24T00:00:00Z",
			},
		},
	}

	observed := []*peers.TrustedPeer{
		{
			ID:           peerDecoded,
			Name:         "Peer DN",
			Organization: "Peer Org",
			TrustLevel:   peers.Standard,
			Addrs:        mustAddrs(t, "/ip4/81.2.69.142/tcp/4001"),
			Metadata:     map[string]string{"agent_version": "space-data-network/1.0.4"},
		},
	}

	in := Input{
		Snapshot:         snapshot,
		Observed:         observed,
		SelfPeerID:       selfPeerIDFixture,
		SelfVCard:        "BEGIN:VCARD\nVERSION:4.0\nFN:Self Node\nEND:VCARD",
		SelfDN:           "Self DN",
		SelfOrganization: "Self Org",
		AgentVersion:     "space-data-network/1.0.4",
		SuiteVersion:     "1.0.4",
		StandardsVersion: "1.155.0",
		Uptime:           90 * time.Second,
		Geo: stubGeo{
			ip:  "81.2.69.142",
			loc: geoip.Location{Lat: 51.5142, Lon: -0.0931, Country: "United Kingdom", City: "London"},
		},
		Now: time.UnixMilli(1_700_000_000_000),
	}

	return BuildNodeStatusSet(in)
}

func TestBuildNodeStatusSetRoundtrip(t *testing.T) {
	buf := buildFixtureSet(t)

	if !nst.SizePrefixedNodeStatusSetBufferHasIdentifier(buf) {
		t.Fatalf("buffer missing $NST size-prefixed identifier")
	}

	set := nst.GetSizePrefixedRootAsNodeStatusSet(buf, 0)
	if got := string(set.SourcePeerId()); got != selfPeerIDFixture {
		t.Errorf("SourcePeerId = %q, want %q", got, selfPeerIDFixture)
	}
	if set.GeneratedAt() != 1_700_000_000_000 {
		t.Errorf("GeneratedAt = %d, want 1700000000000", set.GeneratedAt())
	}
	if set.NodesLength() != 2 {
		t.Fatalf("NodesLength = %d, want 2", set.NodesLength())
	}

	var self nst.NodeStatus
	if !set.Nodes(&self, 0) {
		t.Fatal("missing self node")
	}
	if !self.IsSelf() {
		t.Error("node[0].IS_SELF = false, want true")
	}
	if got := string(self.PeerId()); got != selfPeerIDFixture {
		t.Errorf("self PEER_ID = %q, want %q", got, selfPeerIDFixture)
	}
	if got := string(self.Vcard()); got != "BEGIN:VCARD\nVERSION:4.0\nFN:Self Node\nEND:VCARD" {
		t.Errorf("self VCARD passthrough failed: %q", got)
	}
	if got := string(self.Dn()); got != "Self DN" {
		t.Errorf("self DN = %q, want Self DN", got)
	}
	if self.UptimeS() != 90 {
		t.Errorf("self UPTIME_S = %d, want 90", self.UptimeS())
	}
	if got := string(self.SuiteVersion()); got != "1.0.4" {
		t.Errorf("self SUITE_VERSION = %q, want 1.0.4", got)
	}
	if got := string(self.StandardsVersion()); got != "1.155.0" {
		t.Errorf("self STANDARDS_VERSION = %q, want 1.155.0", got)
	}
	// Self used a loopback address only → no geo.
	if self.Lat() != 0 || self.Lon() != 0 {
		t.Errorf("self geo should be zero, got (%v,%v)", self.Lat(), self.Lon())
	}

	var p nst.NodeStatus
	if !set.Nodes(&p, 1) {
		t.Fatal("missing peer node")
	}
	if p.IsSelf() {
		t.Error("peer IS_SELF = true, want false")
	}
	if got := string(p.PeerId()); got != peerIDFixture {
		t.Errorf("peer PEER_ID = %q, want %q", got, peerIDFixture)
	}
	if got := string(p.Dn()); got != "Peer DN" {
		t.Errorf("peer DN = %q, want Peer DN", got)
	}
	if got := string(p.Organization()); got != "Peer Org" {
		t.Errorf("peer ORGANIZATION = %q, want Peer Org", got)
	}
	if got := string(p.AgentVersion()); got != "space-data-network/1.0.4" {
		t.Errorf("peer AGENT_VERSION = %q", got)
	}
	if !p.IsOnline() {
		t.Error("peer IS_ONLINE = false, want true")
	}
	if p.LastSeen() == 0 {
		t.Error("peer LAST_SEEN = 0, want parsed timestamp")
	}
	if p.MultiformatAddressLength() != 1 {
		t.Errorf("peer MULTIFORMAT_ADDRESS length = %d, want 1", p.MultiformatAddressLength())
	}
	// Peer had a public IP → geo resolved via the stub.
	if got := p.Lat(); got < 51.51 || got > 51.52 {
		t.Errorf("peer LAT = %v, want ~51.5142", got)
	}
	if got := string(p.GeoCity()); got != "London" {
		t.Errorf("peer GEO_CITY = %q, want London", got)
	}
	if got := string(p.GeoCountry()); got != "United Kingdom" {
		t.Errorf("peer GEO_COUNTRY = %q, want United Kingdom", got)
	}
}

func TestBuildNodeStatusSetNilInputs(t *testing.T) {
	// A degenerate input (no snapshot, no peers, no geo) must still produce a
	// valid, decodable single-node ($NST) frame — fail-open all the way down.
	buf := BuildNodeStatusSet(Input{SelfPeerID: selfPeerIDFixture})
	if !nst.SizePrefixedNodeStatusSetBufferHasIdentifier(buf) {
		t.Fatal("buffer missing $NST identifier")
	}
	set := nst.GetSizePrefixedRootAsNodeStatusSet(buf, 0)
	if set.NodesLength() != 1 {
		t.Fatalf("NodesLength = %d, want 1 (self only)", set.NodesLength())
	}
	var self nst.NodeStatus
	if !set.Nodes(&self, 0) || !self.IsSelf() {
		t.Error("self node missing or IS_SELF false")
	}
}
