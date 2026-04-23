package node

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
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

func (n *Node) fetchAndIndexDiscoveredNodeEPM(pid peer.ID, source string) {
	if n == nil || pid == "" {
		return
	}
	if n.epmService == nil || n.directorySvc == nil || n.peerRegistry == nil || n.host == nil {
		return
	}

	ctx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
	defer cancel()

	if err := n.epmService.RequestPeerEPM(ctx, n.host, pid); err != nil {
		log.Debugf("Failed to fetch EPM for discovered peer %s: %v", pid, err)
		return
	}

	n.indexKnownDiscoveredNodeEPM(pid, source)
}

func (n *Node) indexKnownDiscoveredNodeEPM(pid peer.ID, source string) {
	if n == nil || pid == "" || n.directorySvc == nil || n.peerRegistry == nil {
		return
	}

	tp, err := n.peerRegistry.GetPeer(pid)
	if err != nil || tp == nil || len(tp.EPMData) == 0 {
		return
	}

	info, err := discoveredNodeEPMJSON(tp.EPMData, pid)
	if err != nil {
		log.Debugf("Failed to normalize discovered EPM for peer %s: %v", pid, err)
		return
	}

	if err := n.directorySvc.UpsertNodeEPMJSON(info, "", source); err != nil {
		log.Debugf("Failed to index discovered EPM for peer %s: %v", pid, err)
	}
}

func discoveredNodeEPMJSON(epmBytes []byte, pid peer.ID) (map[string]any, error) {
	if len(epmBytes) == 0 {
		return nil, fmt.Errorf("empty EPM data")
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return nil, fmt.Errorf("invalid EPM data")
	}

	epmRecord := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	info := map[string]any{
		"directory_kind": "node",
		"peer_id":        pid.String(),
	}

	if dn := epmRecord.DN(); dn != nil {
		info["dn"] = string(dn)
	}
	if legalName := epmRecord.LEGAL_NAME(); legalName != nil {
		info["legal_name"] = string(legalName)
	}
	if familyName := epmRecord.FAMILY_NAME(); familyName != nil {
		info["family_name"] = string(familyName)
	}
	if givenName := epmRecord.GIVEN_NAME(); givenName != nil {
		info["given_name"] = string(givenName)
	}

	if n := epmRecord.KEYSLength(); n > 0 {
		keys := make([]map[string]any, 0, n)
		key := new(EPM.CryptoKey)
		for i := 0; i < n; i++ {
			if !epmRecord.KEYS(key, i) {
				continue
			}
			entry := make(map[string]any)
			if value := key.PUBLIC_KEY(); value != nil {
				entry["public_key"] = string(value)
			}
			if value := key.XPUB(); value != nil {
				entry["xpub"] = string(value)
			}
			if value := key.KEY_ADDRESS(); value != nil {
				entry["key_address"] = string(value)
			}
			if value := key.ADDRESS_TYPE(); value != nil {
				entry["address_type"] = string(value)
			}
			switch key.KEY_TYPE() {
			case EPM.KeyTypeSigning:
				entry["key_type"] = "signing"
			case EPM.KeyTypeEncryption:
				entry["key_type"] = "encryption"
			}
			keys = append(keys, entry)
		}
		if len(keys) > 0 {
			info["keys"] = keys
		}
	}

	if n := epmRecord.MULTIFORMAT_ADDRESSLength(); n > 0 {
		addrs := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if value := epmRecord.MULTIFORMAT_ADDRESS(i); value != nil {
				addrs = append(addrs, string(value))
			}
		}
		if len(addrs) > 0 {
			info["multiformat_address"] = addrs
		}
	}

	return info, nil
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
