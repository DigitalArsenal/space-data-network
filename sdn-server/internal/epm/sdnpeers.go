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
func BuildObservedSDNPeers(snapshot *PeerGraphSnapshot, registryPeers []*peers.TrustedPeer, advertisementFlagsByPeer map[string][]string) []*peers.TrustedPeer {
	if snapshot == nil || len(advertisementFlagsByPeer) == 0 {
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

	out := make([]*peers.TrustedPeer, 0)
	for _, node := range snapshot.Nodes {
		peerID := strings.TrimSpace(node.PeerID)
		if peerID == "" || peerID == snapshot.LocalPeerID || !node.IsOnline {
			continue
		}

		flags := uniqueStrings(advertisementFlagsByPeer[peerID])
		if len(flags) == 0 {
			continue
		}

		decodedPeerID, err := peer.Decode(peerID)
		if err != nil {
			continue
		}

		registryPeer := registryByID[peerID]
		entry := &peers.TrustedPeer{
			ID:           decodedPeerID,
			Addrs:        mergePeerAddrs(node.MultiformatAddress, registryPeer),
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

		out = append(out, entry)
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

func mergePeerAddrs(nodeAddrs []string, registryPeer *peers.TrustedPeer) []multiaddr.Multiaddr {
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
