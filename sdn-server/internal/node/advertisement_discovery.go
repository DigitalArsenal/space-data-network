package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
)

// sdnAdvertisementDiscoveryNamespace is the canonical SDN membership flag on
// the public IPFS/Amino DHT. Since A1 (SDN_ALIGNMENT_FIX_LOOP) the node's DHT
// (see publicDHTOptions in node.go) joins the stock public "/ipfs/kad/1.0.0"
// swarm rather than a private "/spacedatanetwork" one, so DHT membership
// alone no longer implies SDN membership: the routing table and
// FindProvidersAsync/GetClosestPeers results are full of unrelated public
// IPFS nodes. This rendezvous namespace (via
// drouting.NewRoutingDiscovery(dht).Advertise / .FindPeers, versioned per
// flag under "<namespace>/<flag>") is what actually distinguishes an SDN
// node from an arbitrary DHT peer: a peer counts as SDN-discovered only when
// it is found by (or advertises under) this namespace, never merely by
// appearing in the DHT routing table, a FindProvidersAsync result for an
// unrelated CID, or an inbound/outbound libp2p connection. Callers that
// select "SDN peers" out of a mixed DHT view MUST filter by this flag (see
// hasSDNAdvertisementPeer, recordSDNAdvertisementDiscovery,
// sdnAdvertisementDiscoveredPeerIDs) rather than trusting "any DHT peer."
const sdnAdvertisementDiscoveryNamespace = "space-data-network/discovery/advertisement-flag"

type sdnAdvertisementDiscoveryTarget struct {
	Flag      string
	Namespace string
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
		discoverTargets = append(discoverTargets, sdnAdvertisementDiscoveryTarget{
			Flag:      flag,
			Namespace: fmt.Sprintf("%s/%s", sdnAdvertisementDiscoveryNamespace, flag),
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

func (n *Node) hasSDNAdvertisementPeer(pid peer.ID) bool {
	if n == nil || pid == "" {
		return false
	}

	n.sdnDiscoveryMu.RLock()
	defer n.sdnDiscoveryMu.RUnlock()

	return len(n.sdnDiscoveryFlagsByPeer[pid]) > 0
}

// sdnAdvertisementDiscoveredPeerIDs returns the peer IDs verified via the SDN
// advertisement flag namespace (rendezvous find or a recorded advertiser),
// i.e. the "SDN membership flag" verified peer set. On the public IPFS/Amino
// DHT this is the correct peer-selection surface for anything presented as
// "known SDN peers" — the raw DHT routing table or FindProvidersAsync/
// GetClosestPeers results include arbitrary public IPFS nodes and must not
// be used as a proxy for SDN membership.
func (n *Node) sdnAdvertisementDiscoveredPeerIDs() []peer.ID {
	if n == nil {
		return nil
	}

	n.sdnDiscoveryMu.RLock()
	defer n.sdnDiscoveryMu.RUnlock()

	if len(n.sdnDiscoveryFlagsByPeer) == 0 {
		return nil
	}
	out := make([]peer.ID, 0, len(n.sdnDiscoveryFlagsByPeer))
	for pid, flags := range n.sdnDiscoveryFlagsByPeer {
		if len(flags) == 0 {
			continue
		}
		out = append(out, pid)
	}
	return out
}

func (n *Node) fetchAndIndexDiscoveredNodeEPM(pid peer.ID, source string) {
	if n == nil || pid == "" {
		return
	}
	if n.peerRegistry == nil || n.host == nil {
		return
	}

	epmBytes, err := n.fetchDiscoveredNodeEPM(pid)
	if err != nil {
		log.Debugf("Failed to fetch EPM for discovered peer %s: %v", pid, err)
		return
	}
	if len(epmBytes) == 0 {
		return
	}

	n.indexFetchedDiscoveredNodeEPM(pid, source, epmBytes)
}

func (n *Node) fetchDiscoveredNodeEPM(pid peer.ID) ([]byte, error) {
	if n == nil || pid == "" || n.host == nil {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(n.ctx, 45*time.Second)
	defer cancel()

	stream, err := n.host.NewStream(ctx, pid, epm.EPMExchangeProtocolID)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	if err := stream.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, 0)
	if _, err := stream.Write(header); err != nil {
		return nil, err
	}

	if err := stream.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return nil, err
	}
	var respHeader [8]byte
	if _, err := io.ReadFull(stream, respHeader[:]); err != nil {
		return nil, err
	}

	status := binary.LittleEndian.Uint32(respHeader[0:4])
	dataLen := binary.LittleEndian.Uint32(respHeader[4:8])
	if status != 0 || dataLen == 0 || dataLen > 64*1024 {
		return nil, nil
	}

	epmBytes := make([]byte, dataLen)
	if _, err := io.ReadFull(stream, epmBytes); err != nil {
		return nil, err
	}
	return epmBytes, nil
}

func (n *Node) indexFetchedDiscoveredNodeEPM(pid peer.ID, source string, epmBytes []byte) {
	if n == nil || pid == "" || n.peerRegistry == nil {
		return
	}
	if err := epm.VerifyEPMSignature(epmBytes); err != nil {
		log.Debugf("Skipping unverified discovered EPM for peer %s: %v", pid, err)
		return
	}
	if epmPeerID, err := epm.PeerIDFromEPM(epmBytes); err == nil && epmPeerID != "" && epmPeerID != pid.String() {
		log.Debugf("Skipping discovered EPM for peer %s: signed EPM advertises peer %s", pid, epmPeerID)
		return
	}

	info, err := discoveredNodeEPMJSON(epmBytes, pid)
	if err != nil {
		log.Debugf("Failed to normalize discovered EPM for peer %s: %v", pid, err)
		return
	}

	if err := n.cacheFetchedDiscoveredNodeEPM(pid, epmBytes); err != nil {
		log.Debugf("Failed to cache discovered EPM for peer %s: %v", pid, err)
	}

	if n.directorySvc == nil {
		return
	}
	epmCID, err := epm.ComputeEPMCID(epmBytes)
	if err != nil {
		log.Debugf("Failed to compute discovered EPM CID for peer %s: %v", pid, err)
	}
	if err := n.directorySvc.UpsertNodeEPMJSON(info, epmCID, source); err != nil {
		log.Debugf("Failed to index discovered EPM for peer %s: %v", pid, err)
	}
}

func (n *Node) cacheFetchedDiscoveredNodeEPM(pid peer.ID, epmBytes []byte) error {
	tp, err := n.peerRegistry.GetPeer(pid)
	if err != nil && err != peers.ErrPeerNotFound {
		return err
	}
	if tp == nil || err == peers.ErrPeerNotFound {
		tp = &peers.TrustedPeer{ID: pid, TrustLevel: peers.Standard}
		if addErr := n.peerRegistry.AddPeer(tp); addErr != nil && addErr != peers.ErrPeerAlreadyExists {
			return addErr
		}
		tp, _ = n.peerRegistry.GetPeer(pid)
	}
	if tp != nil {
		// The discovery loop re-runs on every re-connect; re-storing a
		// byte-identical EPM re-triggered a registry persist each time,
		// which starved registry readers under ingest load (graph task
		// sdn-registry-save-lock-starvation). Identical EPM = no-op.
		if bytes.Equal(tp.EPMData, epmBytes) && tp.VCardData != "" {
			return nil
		}
		tp.EPMData = append([]byte(nil), epmBytes...)
		if vcardStr, err := vcard.EPMToVCard(epmBytes); err == nil {
			tp.VCardData = vcardStr
		}
		return n.peerRegistry.UpdatePeer(tp)
	}
	return nil
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
		// The signed bytes travel with the row: the resolved-sources lane
		// verifies a publisher from them, and a row without them named
		// nothing (every lane fell to its source tag, 2026-09-04).
		"epm_base64": base64.StdEncoding.EncodeToString(epmBytes),
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

	if n := epmRecord.CHAIN_PROOFSLength(); n > 0 {
		proofs := make([]map[string]any, 0, n)
		proof := new(EPM.ChainProof)
		for i := 0; i < n; i++ {
			if !epmRecord.CHAIN_PROOFS(proof, i) {
				continue
			}
			entry := make(map[string]any)
			chain := ""
			if value := proof.CHAIN(); value != nil {
				chain = strings.ToLower(strings.TrimSpace(string(value)))
				entry["chain"] = chain
			}
			if value := proof.ADDRESS(); value != nil {
				address := strings.TrimSpace(string(value))
				entry["address"] = address
				switch chain {
				case "bitcoin":
					info["bitcoin_address"] = address
					if value := proof.KEY_PATH(); value != nil {
						info["bitcoin_key_path"] = strings.TrimSpace(string(value))
					}
				case "ethereum":
					info["ethereum_address"] = address
					if value := proof.KEY_PATH(); value != nil {
						info["ethereum_key_path"] = strings.TrimSpace(string(value))
					}
				case "solana":
					info["solana_address"] = address
					if value := proof.KEY_PATH(); value != nil {
						info["solana_key_path"] = strings.TrimSpace(string(value))
					}
				}
			}
			if value := proof.PUBLIC_KEY(); value != nil {
				entry["public_key"] = string(value)
			}
			if value := proof.KEY_PATH(); value != nil {
				entry["key_path"] = string(value)
			}
			if value := proof.SIGNATURE(); value != nil {
				entry["signature"] = string(value)
			}
			if value := proof.SIGNED_PAYLOAD(); value != nil {
				entry["signed_payload"] = string(value)
			}
			if value := proof.ALGORITHM(); value != nil {
				entry["algorithm"] = string(value)
			}
			if value := proof.ENCODING(); value != nil {
				entry["encoding"] = string(value)
			}
			proofs = append(proofs, entry)
		}
		if len(proofs) > 0 {
			info["chain_proofs"] = proofs
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

// DiscoverSDNAdvertisementPeers runs an immediate public-DHT lookup for SDN
// advertisement peers and indexes any EPMs that can be fetched from the peers
// it discovers. Live-DHT search uses this method before reading shared local
// search rows so DHT mode cannot silently answer from stale local-only state.
func (n *Node) DiscoverSDNAdvertisementPeers(ctx context.Context) (int, error) {
	if n == nil || n.dht == nil || n.host == nil {
		return 0, fmt.Errorf("live DHT discovery is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	targets := n.sdnDiscoveryTargets
	if len(targets) == 0 && strings.TrimSpace(n.sdnAdvertisementTarget.Namespace) != "" {
		targets = []sdnAdvertisementDiscoveryTarget{n.sdnAdvertisementTarget}
	}
	if len(targets) == 0 {
		return 0, fmt.Errorf("no SDN DHT discovery namespaces configured")
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	seen := map[peer.ID]struct{}{}
	var lastErr error
	for _, target := range targets {
		if strings.TrimSpace(target.Namespace) == "" {
			continue
		}
		count, err := n.discoverSDNAdvertisementPeersNow(discoveryCtx, target, seen)
		if err != nil {
			lastErr = err
			log.Debugf("Live DHT search discovery failed for %s: %v", target.Flag, err)
			continue
		}
		log.Debugf("Live DHT search discovery for %s observed %d peers", target.Flag, count)
	}
	if len(seen) == 0 && lastErr != nil {
		return 0, lastErr
	}
	return len(seen), nil
}

func (n *Node) discoverSDNAdvertisementPeersNow(ctx context.Context, target sdnAdvertisementDiscoveryTarget, seen map[peer.ID]struct{}) (int, error) {
	routingDiscovery := drouting.NewRoutingDiscovery(n.dht)
	peerChan, err := routingDiscovery.FindPeers(ctx, target.Namespace)
	if err != nil {
		return 0, err
	}

	count := 0
	for peerInfo := range peerChan {
		if peerInfo.ID == "" || peerInfo.ID == n.host.ID() {
			continue
		}
		if _, ok := seen[peerInfo.ID]; ok {
			continue
		}
		seen[peerInfo.ID] = struct{}{}
		count++
		n.recordSDNAdvertisementPeerInfo(peerInfo, target.Flag)

		if n.host.Network().Connectedness(peerInfo.ID) != network.Connected {
			if err := n.host.Connect(ctx, peerInfo); err != nil {
				log.Debugf("Failed to connect to live-DHT search peer %s (%s): %v", peerInfo.ID, target.Flag, err)
				continue
			}
			n.enqueueAutoRelayCandidate(peerInfo)
		}

		n.fetchAndIndexDiscoveredNodeEPM(peerInfo.ID, "sdn-advertisement-discovery")
	}
	return count, nil
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
