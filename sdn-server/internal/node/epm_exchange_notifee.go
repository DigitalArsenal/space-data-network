package node

import (
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
)

const peerEPMRequestCooldown = 5 * time.Minute

type epmExchangeNotifee struct {
	node *Node
}

func (e *epmExchangeNotifee) Listen(network.Network, multiaddr.Multiaddr)      {}
func (e *epmExchangeNotifee) ListenClose(network.Network, multiaddr.Multiaddr) {}
func (e *epmExchangeNotifee) OpenedStream(network.Network, network.Stream)     {}
func (e *epmExchangeNotifee) ClosedStream(network.Network, network.Stream)     {}

// Disconnected taps the node_activity_read activity ring (M2 activity
// capability, caps/nodeactivity.go). e.node.activityRing.Append is
// nil/panic-safe, so this is safe even before/after e.node is fully torn
// down.
func (e *epmExchangeNotifee) Disconnected(_ network.Network, conn network.Conn) {
	if e == nil || e.node == nil || conn == nil {
		return
	}
	e.node.activityRing.Append("peer_disconnected", conn.RemotePeer().String(), "")
}

func (e *epmExchangeNotifee) Connected(_ network.Network, conn network.Conn) {
	if e == nil || e.node == nil || conn == nil {
		return
	}
	// Tap the activity ring (M2 activity capability, caps/nodeactivity.go)
	// before the existing EPM-exchange side effect below.
	e.node.activityRing.Append("peer_connected", conn.RemotePeer().String(), "")
	e.node.requestConnectedPeerEPM(conn.RemotePeer(), "peer-connect")
}

func (n *Node) requestEPMFromConnectedPeers(source string) {
	if n == nil || n.host == nil {
		return
	}
	for _, conn := range n.host.Network().Conns() {
		if conn == nil {
			continue
		}
		n.requestConnectedPeerEPM(conn.RemotePeer(), source)
	}
}

func (n *Node) requestConnectedPeerEPM(pid peer.ID, source string) {
	source = n.directoryEPMSourceForPeer(pid, source)
	if source == "" {
		return
	}
	if !n.reserveConnectedPeerEPMRequest(pid) {
		return
	}
	go n.fetchAndIndexPeerEPM(pid, source)
}

// fetchAndIndexPeerEPM is the connect-time EPM request (security re-audit
// NET-01b, 2026-10-15). The bytes are whatever the peer answers, and the
// indexer reads them through ~30 generated accessors (VerifyEPMSignature):
// on origin/main one EPM with a field offset past its end panicked this bare
// goroutine and ended the process. So the answer is proved memory-safe
// before anything reads it, and the goroutine contains any panic past that
// as a dropped EPM.
func (n *Node) fetchAndIndexPeerEPM(pid peer.ID, source string) {
	defer recoverPeerIngress("epm-connect", pid, false, nil)
	if n == nil || pid == "" || n.peerRegistry == nil || n.host == nil {
		return
	}
	epmBytes, err := n.fetchDiscoveredNodeEPM(pid)
	if err != nil {
		log.Debugf("Failed to fetch EPM for peer %s: %v", pid, err)
		return
	}
	if len(epmBytes) == 0 {
		return
	}
	if err := epm.VerifyEPMBuffer(epmBytes); err != nil {
		log.Warnf("Rejecting EPM from %s (%s): %v", pid.ShortString(), source, err)
		return
	}
	n.indexFetchedDiscoveredNodeEPM(pid, source, epmBytes)
}

func (n *Node) directoryEPMSourceForPeer(pid peer.ID, source string) string {
	source = strings.TrimSpace(source)
	if source != "peer-connect" {
		return source
	}
	if n.hasSDNAdvertisementPeer(pid) {
		return "sdn-advertisement-discovery"
	}
	// A peer this node TRUSTS is a publisher whose records it accepts, and its
	// signed profile is what names those records in the dashboard — so it is
	// asked on every connect whether or not it advertises.
	if n.peerRegistry != nil && n.peerRegistry.IsTrusted(pid) {
		return "trusted-peer"
	}
	return ""
}

func (n *Node) reserveConnectedPeerEPMRequest(pid peer.ID) bool {
	if n == nil || pid == "" || n.host == nil || n.ctx == nil {
		return false
	}
	if pid == n.host.ID() {
		return false
	}
	select {
	case <-n.ctx.Done():
		return false
	default:
	}

	now := time.Now().UTC()
	n.epmExchangeMu.Lock()
	defer n.epmExchangeMu.Unlock()

	if n.epmExchangeLastRequest == nil {
		n.epmExchangeLastRequest = make(map[peer.ID]time.Time)
	}
	if last, ok := n.epmExchangeLastRequest[pid]; ok && now.Sub(last) < peerEPMRequestCooldown {
		return false
	}
	if len(n.epmExchangeLastRequest) >= 4096 {
		for id, last := range n.epmExchangeLastRequest {
			if now.Sub(last) >= peerEPMRequestCooldown {
				delete(n.epmExchangeLastRequest, id)
			}
		}
		if len(n.epmExchangeLastRequest) >= 4096 {
			log.Debugf("epm-connect: request for %s refused: discovery cooldown capacity", pid)
			return false
		}
	}
	n.epmExchangeLastRequest[pid] = now
	return true
}
