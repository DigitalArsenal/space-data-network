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
	candidatePeerIDs := uniqueStrings(append(peerIDs(advertisementFlagsByPeer), sdnPeerIDsFromSnapshot(snapshot, registryByID)...))
	for _, peerID := range candidatePeerIDs {
		if peerID == "" || peerID == snapshot.LocalPeerID {
			continue
		}

		flags := uniqueStrings(advertisementFlagsByPeer[peerID])
		node := nodesByID[peerID]
		if len(flags) == 0 && !isConnectedSDNPeer(node, protocolsByPeer[peerID], registryByID[peerID]) {
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
		if strings.TrimSpace(entry.Metadata["agent_version"]) == "" && len(flags) > 0 {
			entry.Metadata["agent_version"] = flags[0]
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
