// Package status builds and serves the node-status feed: a public, read-only
// snapshot of this core node and the SDN peers it observes, serialized with
// the local NodeStatus FlatBuffers transport (internal/status/nst) and pushed
// over /ws/status. Every field is already public via /api/v1/id and
// /api/peers/sdn; this package only reshapes those existing surfaces plus a
// fail-open GeoIP lookup into one binary frame. It is application-blind core
// node telemetry — it carries no app, flow, or record semantics.
package status

import (
	"net"
	"strings"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/geoip"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/status/nst"
)

// GeoResolver resolves a textual IP to coarse coordinates. *geoip.Reader
// satisfies it; tests supply a stub. A nil resolver is treated as no geo data.
type GeoResolver interface {
	Lookup(ip string) geoip.Location
}

// Input is the assembled, node-agnostic data BuildNodeStatusSet serializes.
// Callers populate it from the existing surfaces — epm.BuildGraphSnapshot,
// epm.BuildObservedSDNPeers, and the EPM service's GetNodeVCard — so this
// package never re-derives peer state.
type Input struct {
	// Snapshot is epm.BuildGraphSnapshot output; used for local addresses,
	// per-peer online state, and last-seen timestamps.
	Snapshot *epm.PeerGraphSnapshot
	// Observed is epm.BuildObservedSDNPeers output: the SDN-only peer list.
	Observed []*peers.TrustedPeer

	// Self identity/version fields for the IS_SELF entry.
	SelfPeerID       string
	SelfVCard        string
	SelfDN           string
	SelfOrganization string
	AgentVersion     string
	SuiteVersion     string
	StandardsVersion string
	Uptime           time.Duration

	// Geo resolves peer public IPs to coordinates (fail-open, may be nil).
	Geo GeoResolver

	// FallbackAddrs maps peer ID -> known dialable multiaddrs (e.g. the
	// bootstrap constants). Used for geo resolution when the live peerstore
	// only holds relay/private addresses; never emitted as the peer's addrs.
	FallbackAddrs map[string][]string

	// Now is the generation time; zero means time.Now().
	Now time.Time
}

// BuildNodeStatusSet composes the input into a size-prefixed NodeStatusSet
// FlatBuffer carrying the $NST file identifier. The self node is emitted first
// with IS_SELF=true.
func BuildNodeStatusSet(in Input) []byte {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	selfPeerID := strings.TrimSpace(in.SelfPeerID)
	if selfPeerID == "" && in.Snapshot != nil {
		selfPeerID = in.Snapshot.LocalPeerID
	}

	// Cross-reference the graph snapshot for online state and last-seen.
	online := make(map[string]bool)
	lastSeen := make(map[string]int64)
	var selfAddrs []string
	if in.Snapshot != nil {
		for _, node := range in.Snapshot.Nodes {
			pid := strings.TrimSpace(node.PeerID)
			if pid == "" {
				continue
			}
			online[pid] = online[pid] || node.IsOnline
			if ts := parseUnixSeconds(node.LastSeen); ts != 0 {
				lastSeen[pid] = ts
			}
			if pid == selfPeerID {
				selfAddrs = append(selfAddrs, node.MultiformatAddress...)
			}
		}
	}

	rows := make([]peerRow, 0, len(in.Observed)+1)

	// Self entry first.
	self := peerRow{
		PeerID:           selfPeerID,
		DN:               in.SelfDN,
		Organization:     in.SelfOrganization,
		AgentVersion:     in.AgentVersion,
		Multiaddrs:       selfAddrs,
		IsOnline:         true,
		VCard:            in.SelfVCard,
		IsSelf:           true,
		UptimeS:          int64(in.Uptime.Seconds()),
		SuiteVersion:     in.SuiteVersion,
		StandardsVersion: in.StandardsVersion,
	}
	self.applyGeo(in.Geo)
	rows = append(rows, self)

	for _, tp := range in.Observed {
		if tp == nil {
			continue
		}
		pid := tp.ID.String()
		if pid == selfPeerID {
			continue
		}
		row := peerRow{
			PeerID:       pid,
			DN:           tp.Name,
			Organization: tp.Organization,
			TrustLevel:   tp.TrustLevel.String(),
			AgentVersion: metaValue(tp.Metadata, "agent_version"),
			Multiaddrs:   multiaddrStrings(tp.Addrs),
			LastSeen:     lastSeen[pid],
			IsOnline:     online[pid],
			VCard:        peers.TrustedPeerToVCard(tp),
		}
		row.applyGeo(in.Geo)
		if row.Lat == 0 && row.Lon == 0 {
			row.applyGeoFromAddrs(in.Geo, in.FallbackAddrs[pid])
		}
		rows = append(rows, row)
	}

	return serialize(selfPeerID, now.UnixMilli(), rows)
}

// peerRow is the flattened per-node view serialized into one NodeStatus table.
type peerRow struct {
	PeerID           string
	DN               string
	Organization     string
	TrustLevel       string
	Role             string
	AgentVersion     string
	Multiaddrs       []string
	LastSeen         int64
	IsOnline         bool
	LatencyMs        float32
	VCard            string
	Lat              float32
	Lon              float32
	Country          string
	City             string
	IsSelf           bool
	UptimeS          int64
	SuiteVersion     string
	StandardsVersion string
}

// applyGeo resolves the first public IP among the row's multiaddrs and fills
// the geo fields. No-op when no resolver or no public IP is present.
func (row *peerRow) applyGeo(geo GeoResolver) {
	row.applyGeoFromAddrs(geo, row.Multiaddrs)
}

func (row *peerRow) applyGeoFromAddrs(geo GeoResolver, addrs []string) {
	if geo == nil {
		return
	}
	ip := firstPublicIP(addrs)
	if ip == "" {
		return
	}
	loc := geo.Lookup(ip)
	row.Lat = loc.Lat
	row.Lon = loc.Lon
	row.Country = loc.Country
	row.City = loc.City
}

func serialize(sourcePeerID string, generatedAtMs int64, rows []peerRow) []byte {
	b := flatbuffers.NewBuilder(1024)

	offsets := make([]flatbuffers.UOffsetT, len(rows))
	for i := range rows {
		offsets[i] = encodeRow(b, &rows[i])
	}

	nst.NodeStatusSetStartNodesVector(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	nodesVec := b.EndVector(len(offsets))

	srcOff := b.CreateString(sourcePeerID)

	nst.NodeStatusSetStart(b)
	nst.NodeStatusSetAddNodes(b, nodesVec)
	nst.NodeStatusSetAddGeneratedAt(b, generatedAtMs)
	nst.NodeStatusSetAddSourcePeerId(b, srcOff)
	set := nst.NodeStatusSetEnd(b)

	nst.FinishSizePrefixedNodeStatusSetBuffer(b, set)
	return b.FinishedBytes()
}

func encodeRow(b *flatbuffers.Builder, row *peerRow) flatbuffers.UOffsetT {
	// Strings and vectors must be created before StartObject.
	peerID := b.CreateString(row.PeerID)
	dn := b.CreateString(row.DN)
	org := b.CreateString(row.Organization)
	trust := b.CreateString(row.TrustLevel)
	role := b.CreateString(row.Role)
	agent := b.CreateString(row.AgentVersion)
	vcard := b.CreateString(row.VCard)
	country := b.CreateString(row.Country)
	city := b.CreateString(row.City)
	suite := b.CreateString(row.SuiteVersion)
	standards := b.CreateString(row.StandardsVersion)

	addrOffsets := make([]flatbuffers.UOffsetT, len(row.Multiaddrs))
	for i, a := range row.Multiaddrs {
		addrOffsets[i] = b.CreateString(a)
	}
	nst.NodeStatusStartMultiformatAddressVector(b, len(addrOffsets))
	for i := len(addrOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(addrOffsets[i])
	}
	addrsVec := b.EndVector(len(addrOffsets))

	nst.NodeStatusStart(b)
	nst.NodeStatusAddPeerId(b, peerID)
	nst.NodeStatusAddDn(b, dn)
	nst.NodeStatusAddOrganization(b, org)
	nst.NodeStatusAddTrustLevel(b, trust)
	nst.NodeStatusAddRole(b, role)
	nst.NodeStatusAddAgentVersion(b, agent)
	nst.NodeStatusAddMultiformatAddress(b, addrsVec)
	nst.NodeStatusAddLastSeen(b, row.LastSeen)
	nst.NodeStatusAddIsOnline(b, row.IsOnline)
	nst.NodeStatusAddLatencyMs(b, row.LatencyMs)
	nst.NodeStatusAddVcard(b, vcard)
	nst.NodeStatusAddLat(b, row.Lat)
	nst.NodeStatusAddLon(b, row.Lon)
	nst.NodeStatusAddGeoCountry(b, country)
	nst.NodeStatusAddGeoCity(b, city)
	nst.NodeStatusAddIsSelf(b, row.IsSelf)
	nst.NodeStatusAddUptimeS(b, row.UptimeS)
	nst.NodeStatusAddSuiteVersion(b, suite)
	nst.NodeStatusAddStandardsVersion(b, standards)
	return nst.NodeStatusEnd(b)
}

func multiaddrStrings(addrs []multiaddr.Multiaddr) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a == nil {
			continue
		}
		out = append(out, a.String())
	}
	return out
}

// firstPublicIP returns the first globally-routable IPv4/IPv6 literal found in
// a list of multiaddr strings. Private, loopback, and link-local addresses are
// skipped — only public IPs are geolocatable.
func firstPublicIP(addrs []string) string {
	for _, a := range addrs {
		// A relay circuit's outer IP locates the RELAY, not the peer.
		if strings.Contains(a, "/p2p-circuit") {
			continue
		}
		ip := ipFromMultiaddr(a)
		if ip == "" {
			continue
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast() || parsed.IsUnspecified() {
			continue
		}
		return ip
	}
	return ""
}

func ipFromMultiaddr(addr string) string {
	ma, err := multiaddr.NewMultiaddr(strings.TrimSpace(addr))
	if err != nil {
		return ""
	}
	if v, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil {
		return v
	}
	if v, err := ma.ValueForProtocol(multiaddr.P_IP6); err == nil {
		return v
	}
	return ""
}

func metaValue(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[key])
}

func parseUnixSeconds(rfc3339 string) int64 {
	rfc3339 = strings.TrimSpace(rfc3339)
	if rfc3339 == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return 0
	}
	return t.Unix()
}
