package node

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// NotifyBundle contains callback functions for network events.
type NotifyBundle struct {
	ListenF       func(network.Network, ma.Multiaddr)
	ListenCloseF  func(network.Network, ma.Multiaddr)
	ConnectedF    func(network.Network, network.Conn)
	DisconnectedF func(network.Network, network.Conn)
}

// Implement the network.Notifiee interface for NotifyBundle.
func (nb *NotifyBundle) Listen(n network.Network, a ma.Multiaddr) {
	if nb.ListenF != nil {
		nb.ListenF(n, a)
	}
}

func (nb *NotifyBundle) ListenClose(n network.Network, a ma.Multiaddr) {
	if nb.ListenCloseF != nil {
		nb.ListenCloseF(n, a)
	}
}

func (nb *NotifyBundle) Connected(n network.Network, c network.Conn) {
	if nb.ConnectedF != nil {
		nb.ConnectedF(n, c)
	}
}

func (nb *NotifyBundle) Disconnected(n network.Network, c network.Conn) {
	if nb.DisconnectedF != nil {
		nb.DisconnectedF(n, c)
	}
}

func (nb *NotifyBundle) OpenedStream(n network.Network, s network.Stream) {}
func (nb *NotifyBundle) ClosedStream(n network.Network, s network.Stream) {}

// discoveryNotifee is used for handling discovered peers in mDNS service.
type discoveryNotifee struct {
	h                   host.Host
	contactedPeers      map[peer.ID]struct{}
	mutex               *sync.Mutex
	discoveredPeersChan chan peer.AddrInfo
}

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.ID() || alreadyContacted(pi.ID, n.mutex) {
		return
	}
	n.discoveredPeersChan <- pi
}
