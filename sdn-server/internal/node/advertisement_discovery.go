package node

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	mh "github.com/multiformats/go-multihash"
)

const sdnAdvertisementDiscoveryNamespace = "space-data-network/discovery/advertisement-flag"

type sdnAdvertisementDiscoveryTarget struct {
	Flag string
	CID  cid.Cid
}

func computeSDNAdvertisementDiscoveryCID(flag string) (cid.Cid, error) {
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return cid.Undef, fmt.Errorf("advertisement flag is required")
	}

	input := make([]byte, 0, len(sdnAdvertisementDiscoveryNamespace)+len(flag))
	input = append(input, []byte(sdnAdvertisementDiscoveryNamespace)...)
	input = append(input, []byte(flag)...)

	sum := sha256.Sum256(input)
	multihash, err := mh.Encode(sum[:], mh.SHA2_256)
	if err != nil {
		return cid.Undef, fmt.Errorf("encode advertisement discovery multihash: %w", err)
	}
	return cid.NewCidV1(cid.Raw, multihash), nil
}

func sdnAdvertisementDiscoveryTargets(currentFlag string, supportedFlags []string) (sdnAdvertisementDiscoveryTarget, []sdnAdvertisementDiscoveryTarget, error) {
	currentFlag = strings.TrimSpace(currentFlag)
	if currentFlag == "" {
		return sdnAdvertisementDiscoveryTarget{}, nil, fmt.Errorf("current advertisement flag is required")
	}

	orderedFlags := make([]string, 0, len(supportedFlags)+1)
	seen := make(map[string]struct{}, len(supportedFlags)+1)
	appendFlag := func(flag string) {
		flag = strings.TrimSpace(flag)
		if flag == "" {
			return
		}
		if _, exists := seen[flag]; exists {
			return
		}
		seen[flag] = struct{}{}
		orderedFlags = append(orderedFlags, flag)
	}

	appendFlag(currentFlag)
	for _, flag := range supportedFlags {
		appendFlag(flag)
	}

	discoverTargets := make([]sdnAdvertisementDiscoveryTarget, 0, len(orderedFlags))
	for _, flag := range orderedFlags {
		discoveryCID, err := computeSDNAdvertisementDiscoveryCID(flag)
		if err != nil {
			return sdnAdvertisementDiscoveryTarget{}, nil, err
		}
		discoverTargets = append(discoverTargets, sdnAdvertisementDiscoveryTarget{
			Flag: flag,
			CID:  discoveryCID,
		})
	}

	return discoverTargets[0], discoverTargets, nil
}

func (n *Node) recordSDNAdvertisementDiscovery(pid peer.ID, flag string) {
	if n == nil || pid == "" {
		return
	}
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return
	}

	n.sdnDiscoveryMu.Lock()
	defer n.sdnDiscoveryMu.Unlock()

	if n.sdnDiscoveryFlagsByPeer == nil {
		n.sdnDiscoveryFlagsByPeer = make(map[peer.ID]map[string]time.Time)
	}
	flags := n.sdnDiscoveryFlagsByPeer[pid]
	if flags == nil {
		flags = make(map[string]time.Time)
		n.sdnDiscoveryFlagsByPeer[pid] = flags
	}
	flags[flag] = time.Now().UTC()
}

func (n *Node) recordSDNAdvertisementPeerInfo(info peer.AddrInfo, flag string) {
	if n == nil || info.ID == "" {
		return
	}

	n.recordSDNAdvertisementDiscovery(info.ID, flag)

	n.sdnDiscoveryMu.Lock()
	defer n.sdnDiscoveryMu.Unlock()

	if n.sdnDiscoveryAddrsByPeer == nil {
		n.sdnDiscoveryAddrsByPeer = make(map[peer.ID][]string)
	}
	n.sdnDiscoveryAddrsByPeer[info.ID] = uniqueNonEmptyStrings(append(n.sdnDiscoveryAddrsByPeer[info.ID], addrInfoStrings(info)...))
}

func (n *Node) recordCurrentSDNAdvertisementDiscovery(pid peer.ID) {
	if n == nil {
		return
	}
	n.recordSDNAdvertisementDiscovery(pid, n.sdnAdvertisementTarget.Flag)
}

func (n *Node) recordCurrentSDNAdvertisementPeerInfo(info peer.AddrInfo) {
	if n == nil {
		return
	}
	n.recordSDNAdvertisementPeerInfo(info, n.sdnAdvertisementTarget.Flag)
}

func (n *Node) SDNAdvertisementFlagsByPeer() map[string][]string {
	if n == nil {
		return nil
	}

	n.sdnDiscoveryMu.RLock()
	defer n.sdnDiscoveryMu.RUnlock()

	if len(n.sdnDiscoveryFlagsByPeer) == 0 {
		return nil
	}

	out := make(map[string][]string, len(n.sdnDiscoveryFlagsByPeer))
	for peerID, flags := range n.sdnDiscoveryFlagsByPeer {
		if len(flags) == 0 {
			continue
		}
		ordered := make([]string, 0, len(flags))
		for flag := range flags {
			ordered = append(ordered, flag)
		}
		sort.Strings(ordered)
		out[peerID.String()] = ordered
	}
	return out
}

func (n *Node) SDNAdvertisementAddrsByPeer() map[string][]string {
	if n == nil {
		return nil
	}

	n.sdnDiscoveryMu.RLock()
	defer n.sdnDiscoveryMu.RUnlock()

	if len(n.sdnDiscoveryAddrsByPeer) == 0 {
		return nil
	}

	out := make(map[string][]string, len(n.sdnDiscoveryAddrsByPeer))
	for peerID, addrs := range n.sdnDiscoveryAddrsByPeer {
		addrs = uniqueNonEmptyStrings(addrs)
		if len(addrs) == 0 {
			continue
		}
		out[peerID.String()] = addrs
	}
	return out
}

func addrInfoStrings(info peer.AddrInfo) []string {
	if len(info.Addrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(info.Addrs))
	for _, addr := range info.Addrs {
		if addr == nil {
			continue
		}
		if value := strings.TrimSpace(addr.String()); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func uniqueNonEmptyStrings(values []string) []string {
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
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
