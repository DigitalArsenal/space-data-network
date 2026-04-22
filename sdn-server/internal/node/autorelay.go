package node

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func (n *Node) feedAutoRelayCandidates(ctx context.Context) {
	if n == nil || n.dht == nil || n.host == nil || n.autoRelayPeerChan == nil {
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		n.enqueueClosestAutoRelayCandidates(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (n *Node) enqueueClosestAutoRelayCandidates(ctx context.Context) {
	closestPeers, err := n.dht.GetClosestPeers(ctx, n.host.ID().String())
	if err != nil {
		return
	}

	for _, pid := range closestPeers {
		addrs := n.host.Peerstore().Addrs(pid)
		if len(addrs) == 0 {
			continue
		}
		n.enqueueAutoRelayCandidate(peer.AddrInfo{ID: pid, Addrs: addrs})
	}
}

func (n *Node) enqueueAutoRelayCandidate(info peer.AddrInfo) {
	if n == nil || n.autoRelayPeerChan == nil || n.host == nil {
		return
	}
	if info.ID == "" || info.ID == n.host.ID() || len(info.Addrs) == 0 {
		return
	}

	select {
	case n.autoRelayPeerChan <- info:
	default:
	}
}
