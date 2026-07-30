package epm

import (
	"sort"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// BuildObservedSDNPeers projects the peer graph plus advertisement discovery
// evidence into the trusted-peer JSON shape consumed by the SDN dashboard.
func BuildObservedSDNPeers(snapshot *PeerGraphSnapshot, registryPeers []*peers.TrustedPeer, advertisementFlagsByPeer map[string][]string, advertisementAddrsByPeer map[string][]string) []*peers.TrustedPeer {
	if snapshot == nil {
		return nil
	}

	registryByID := make(map[string]*peers.TrustedPeer, len(registryPeers))
	for _, tp := range registryPeers {
		if tp == nil {
			continue
		}
		registryByID[tp.ID.String()] = tp
	}
	protocolsByPeer := buildEdgeProtocolMap(snapshot)
	nodesByID := make(map[string]PeerNode, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		peerID := strings.TrimSpace(node.PeerID)
		if peerID == "" {
			continue
		}
		nodesByID[peerID] = node
	}

	out := make([]*peers.TrustedPeer, 0)
	candidatePeerIDs := uniqueStrings(append(
		append(peerIDs(advertisementFlagsByPeer), pinnedPeerIDsFromSnapshot(snapshot)...),
		sdnPeerIDsFromSnapshot(snapshot, registryByID)...))
	for _, peerID := range candidatePeerIDs {
		if peerID == "" || peerID == snapshot.LocalPeerID {
			continue
		}

		flags := uniqueStrings(advertisementFlagsByPeer[peerID])
		node := nodesByID[peerID]

		// ADMISSION (owner rulings 2026-07-30, verbatim: "The Peers table
		// should not ever show peers that have never been seen, UNLESS they
		// have been added manually and 'pinned'" / "When a peer drops off the
		// network it should just disappear" / "I have no idea what these peers
		// are that are in the table").
		//
		// A row is admitted for EXACTLY TWO reasons, and the row says which:
		//   PINNED    — the operator or the config file deliberately kept it.
		//   CONNECTED — it is on a live connection right now.
		//
		// What this deliberately kills: DHT rendezvous advertisement
		// discoveries. Measured on the live feed 2026-07-30, 34 of 35 rows were
		// exactly that — never dialled, no name, LAST_SEEN 0, no coordinates,
		// and all carrying the identical agent string "spacedatanetwork/1.0.0"
		// that the old code SYNTHESIZED for them from flags[0] a few lines
		// below. Publishing an advertisement into a public DHT is a claim
		// anyone can make; it is not evidence this node has ever met the peer,
		// and it must not seat anyone on an operator's peer board.
		//
		// The disappearance rule falls out of this: an unpinned peer is here
		// only while connected, so when it drops off it is simply absent from
		// the next frame. No tombstones, no "last seen — never" accumulating.
		if !node.Pinned && !node.IsOnline {
			continue
		}
		if !node.Pinned && !isConnectedSDNPeer(node, protocolsByPeer[peerID], registryByID[peerID]) {
			continue
		}

		// MEMBERSHIP (owner rule 2026-07-28): this board shows Space Data
		// Network nodes, and nothing else. Speaking an SDN protocol is not the
		// same as BEING an SDN node — an upstream kubo build can talk to us
		// fluently while identifying itself as "kubo/0.40.0-dev/", and such a
		// peer was appearing on the accounts board as a row nobody could
		// explain.
		//
		// So membership is decided by IDENTITY (the libp2p identify
		// agent-version), not by protocol participation. This is enforced here,
		// server-side, because the feed is the contract: a client-side filter
		// would leave every other consumer of this projection showing the row.
		//
		// A peer that has gone offline keeps whatever agent-version the
		// registry recorded, so a known SDN node that is merely unreachable
		// still belongs on the board.
		//
		// A PIN OVERRIDES THIS. An operator pinning a box by peer id has said
		// what it is; demanding it also prove SDN membership before it may be
		// listed would make a fresh, not-yet-reachable pin invisible — which is
		// the one case the pin exists for.
		if !node.Pinned && !identifiesAsSDNAgent(node, registryByID[peerID], flags) {
			continue
		}

		decodedPeerID, err := peer.Decode(peerID)
		if err != nil {
			continue
		}

		registryPeer := registryByID[peerID]
		entry := &peers.TrustedPeer{
			ID:           decodedPeerID,
			Addrs:        mergePeerAddrs(node.MultiformatAddress, registryPeer, advertisementAddrsByPeer[peerID]),
			TrustLevel:   resolveTrustLevel(node.TrustLevel, registryPeer),
			Name:         firstNonEmpty(node.DN, peerName(registryPeer)),
			Organization: firstNonEmpty(node.Organization, peerOrganization(registryPeer)),
			Metadata:     cloneMetadata(registryPeer),
		}

		protocols := uniqueStrings(protocolsByPeer[peerID])
		if len(protocols) > 0 {
			entry.Metadata["protocols"] = strings.Join(protocols, ",")
		}
		entry.Metadata["advertisement_flags"] = strings.Join(flags, ",")
		if strings.TrimSpace(node.AgentVersion) != "" {
			entry.Metadata["agent_version"] = strings.TrimSpace(node.AgentVersion)
		}
		// The advertisement flag is NOT an agent version. Copying flags[0] into
		// agent_version is how 34 never-contacted rows all came to display
		// "spacedatanetwork/1.0.0" on the live board — a version string this
		// node never observed, presented as though it had. An unknown agent is
		// an empty agent.

		// PROVENANCE — the answer to the owner's "so how did they get there?",
		// carried on the row itself rather than left to be inferred.
		entry.Metadata["source"] = observedPeerSource(node)
		if node.Pinned {
			entry.Metadata["pinned"] = "true"
			if note := strings.TrimSpace(node.PinNote); note != "" {
				entry.Metadata["pin_note"] = note
			}
		}

		out = append(out, entry)
	}

	return out
}

// SDNPeerCounts summarizes how many of the peers this node knows about are
// actual Space Data Network nodes, as opposed to the raw libp2p/DHT swarm.
//
// Connected is the headline number: SDN peers with a live connection right now
// (an online peer graph node carrying SDN evidence — an SDN agent version or an
// /space-data-network//spacedatanetwork protocol on the connection, or the same
// evidence recorded in the peer registry; see isConnectedSDNPeer).
//
// Known is the full observed-SDN-peer set (BuildObservedSDNPeers): connected SDN
// peers PLUS peers discovered through SDN advertisement rendezvous that are not
// currently connected. Known >= Connected always.
type SDNPeerCounts struct {
	Connected int `json:"connected"`
	Known     int `json:"known"`
}

// CountSDNPeers derives the SDN peer counts from exactly the same evidence the
// SDN dashboard's observed-peer list uses, so the numbers can never disagree
// with /api/peers/sdn.
func CountSDNPeers(snapshot *PeerGraphSnapshot, registryPeers []*peers.TrustedPeer, advertisementFlagsByPeer map[string][]string, advertisementAddrsByPeer map[string][]string) SDNPeerCounts {
	if snapshot == nil {
		return SDNPeerCounts{}
	}

	observed := BuildObservedSDNPeers(snapshot, registryPeers, advertisementFlagsByPeer, advertisementAddrsByPeer)
	online := make(map[string]bool, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		peerID := strings.TrimSpace(node.PeerID)
		if peerID == "" {
			continue
		}
		online[peerID] = online[peerID] || node.IsOnline
	}

	counts := SDNPeerCounts{Known: len(observed)}
	for _, entry := range observed {
		if entry == nil {
			continue
		}
		if online[entry.ID.String()] {
			counts.Connected++
		}
	}
	return counts
}

// observedPeerSource names why a row is on the board, in the vocabulary the
// $NST SOURCE field and the dashboard badge share: "config", "pinned" or
// "connected". A pin's own source wins, because a config pin is locked in the
// UI and must name the file that owns it — including while it is connected.
func observedPeerSource(node PeerNode) string {
	if node.Pinned {
		switch node.PinSource {
		case peers.PinSourceConfig:
			return peers.PinSourceConfig
		default:
			return peers.PinSourceOperator
		}
	}
	return "connected"
}

func pinnedPeerIDsFromSnapshot(snapshot *PeerGraphSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	out := make([]string, 0)
	for _, node := range snapshot.Nodes {
		peerID := strings.TrimSpace(node.PeerID)
		if peerID == "" || peerID == snapshot.LocalPeerID || !node.Pinned {
			continue
		}
		out = append(out, peerID)
	}
	return out
}

func sdnPeerIDsFromSnapshot(snapshot *PeerGraphSnapshot, registryByID map[string]*peers.TrustedPeer) []string {
	if snapshot == nil {
		return nil
	}
	protocolsByPeer := buildEdgeProtocolMap(snapshot)
	out := make([]string, 0)
	for _, node := range snapshot.Nodes {
		peerID := strings.TrimSpace(node.PeerID)
		if peerID == "" || peerID == snapshot.LocalPeerID {
			continue
		}
		if isConnectedSDNPeer(node, protocolsByPeer[peerID], registryByID[peerID]) {
			out = append(out, peerID)
		}
	}
	return out
}

func isConnectedSDNPeer(node PeerNode, protocols []string, registryPeer *peers.TrustedPeer) bool {
	if !node.IsOnline {
		return false
	}
	if isSDNAgentVersion(node.AgentVersion) {
		return true
	}
	if hasSDNProtocol(protocols) {
		return true
	}
	if registryPeer != nil {
		if isSDNAgentVersion(registryPeer.Metadata["agent_version"]) || isSDNAgentVersion(registryPeer.Metadata["advertisement_flags"]) {
			return true
		}
		if hasSDNProtocol(strings.Split(registryPeer.Metadata["protocols"], ",")) {
			return true
		}
	}
	return false
}

// identifiesAsSDNAgent reports whether a peer PRESENTS as a Space Data Network
// node. Evidence, in order: the live identify agent-version, the agent-version
// the registry recorded when the peer was last seen (so an offline SDN node
// keeps its seat), and finally the SDN advertisement flags it published.
//
// Deliberately NOT evidence: speaking an SDN protocol. A foreign
// implementation can do that and still not be one of our nodes; if we want its
// box on the board, it should say so in its own agent string.
func identifiesAsSDNAgent(node PeerNode, registryPeer *peers.TrustedPeer, flags []string) bool {
	if isSDNAgentVersion(node.AgentVersion) {
		return true
	}
	if registryPeer != nil && isSDNAgentVersion(registryPeer.Metadata["agent_version"]) {
		return true
	}
	// An SDN ADVERTISEMENT is itself a self-declaration of membership: it is
	// our own discovery mechanism, published deliberately, and a foreign
	// implementation does not emit one incidentally the way it can speak a
	// protocol. Any advertisement therefore admits the peer regardless of the
	// flag's spelling ("sdn/1.0.3" and friends).
	return len(flags) > 0
}

func isSDNAgentVersion(agentVersion string) bool {
	value := strings.ToLower(strings.TrimSpace(agentVersion))
	return strings.Contains(value, "spacedatanetwork") ||
		strings.Contains(value, "space-data-network") ||
		strings.Contains(value, "sdn-desktop")
}

func hasSDNProtocol(protocols []string) bool {
	for _, protocol := range protocols {
		value := strings.TrimSpace(protocol)
		if strings.HasPrefix(value, "/space-data-network/") || strings.HasPrefix(value, "/spacedatanetwork/") {
			return true
		}
	}
	return false
}

func peerIDs(values map[string][]string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for peerID := range values {
		out = append(out, peerID)
	}
	return out
}

func buildEdgeProtocolMap(snapshot *PeerGraphSnapshot) map[string][]string {
	protocolsByPeer := make(map[string][]string)
	for _, edge := range snapshot.Edges {
		if edge.SourcePeerID != snapshot.LocalPeerID || strings.TrimSpace(edge.TargetPeerID) == "" {
			continue
		}
		existing := protocolsByPeer[edge.TargetPeerID]
		protocolsByPeer[edge.TargetPeerID] = uniqueStrings(append(existing, edge.Protocols...))
	}
	return protocolsByPeer
}

func mergePeerAddrs(nodeAddrs []string, registryPeer *peers.TrustedPeer, discoveredAddrs []string) []multiaddr.Multiaddr {
	seen := make(map[string]struct{})
	addrs := make([]multiaddr.Multiaddr, 0)

	appendAddr := func(addr multiaddr.Multiaddr) {
		if addr == nil {
			return
		}
		key := addr.String()
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		addrs = append(addrs, addr)
	}

	for _, addr := range nodeAddrs {
		parsed, err := multiaddr.NewMultiaddr(addr)
		if err == nil {
			appendAddr(parsed)
		}
	}
	for _, addr := range discoveredAddrs {
		parsed, err := multiaddr.NewMultiaddr(addr)
		if err == nil {
			appendAddr(parsed)
		}
	}
	if registryPeer != nil {
		for _, addr := range registryPeer.Addrs {
			appendAddr(addr)
		}
	}
	return addrs
}

func resolveTrustLevel(nodeTrust string, registryPeer *peers.TrustedPeer) peers.TrustLevel {
	if trust, err := peers.ParseTrustLevel(strings.TrimSpace(nodeTrust)); err == nil {
		return trust
	}
	if registryPeer != nil {
		return registryPeer.TrustLevel
	}
	return peers.Standard
}

func cloneMetadata(registryPeer *peers.TrustedPeer) map[string]string {
	if registryPeer == nil || len(registryPeer.Metadata) == 0 {
		return make(map[string]string)
	}
	out := make(map[string]string, len(registryPeer.Metadata)+2)
	for key, value := range registryPeer.Metadata {
		out[key] = value
	}
	return out
}

func peerName(tp *peers.TrustedPeer) string {
	if tp == nil {
		return ""
	}
	return tp.Name
}

func peerOrganization(tp *peers.TrustedPeer) string {
	if tp == nil {
		return ""
	}
	return tp.Organization
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
