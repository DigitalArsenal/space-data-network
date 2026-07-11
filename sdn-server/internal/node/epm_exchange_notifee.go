package node

import (
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
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
	go n.fetchAndIndexDiscoveredNodeEPM(pid, source)
}

func (n *Node) directoryEPMSourceForPeer(pid peer.ID, source string) string {
	source = strings.TrimSpace(source)
	if source != "peer-connect" {
		return source
	}
	if n.hasSDNAdvertisementPeer(pid) {
		return "sdn-advertisement-discovery"
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
	n.epmExchangeLastRequest[pid] = now
	return true
}
