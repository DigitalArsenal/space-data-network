package node

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func (n *Node) feedAutoRelayCandidates(ctx context.Context) {
	if n == nil || n.host == nil || n.autoRelayPeerChan == nil {
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		// CONFIGURED RELAYS FIRST, and without the DHT.
		//
		// `network.edge_relays` was declared and never read — the same defect
		// as `enable_relay` before 2026-08-08. An operator listing relays got
		// nothing: candidates came only from dht.GetClosestPeers, and a node
		// behind a firewall is exactly the node whose DHT walk returns nothing
		// useful. Measured: a node on a Docker bridge with no published ports
		// reached 1 peer and made ZERO reservations in 15 minutes with a
		// working relay configured and reachable, because it was never offered
		// as a candidate.
		//
		// Bootstrap peers count too. They are public by construction, and with
		// relaying now the default for reachable nodes they are the relays a
		// fresh node can actually find.
		n.enqueueConfiguredAutoRelayCandidates()

		// The DHT walk still runs, for relays nobody configured. It needs a
		// populated routing table, so it is the supplement, not the source.
		if n.dht != nil {
			n.enqueueClosestAutoRelayCandidates(ctx)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// enqueueConfiguredAutoRelayCandidates offers the relays this node was TOLD
// about: network.edge_relays, then network.bootstrap. Both are multiaddrs that
// must carry a /p2p/ component to be dialable as a relay.
func (n *Node) enqueueConfiguredAutoRelayCandidates() {
	if n == nil || n.config == nil {
		return
	}
	for _, group := range [][]string{n.config.Network.EdgeRelays, n.config.Network.Bootstrap} {
		for _, raw := range group {
			info, err := relayCandidateFromMultiaddr(raw)
			if err != nil {
				log.Debugf("Not usable as a relay candidate (%s): %v", raw, err)
				continue
			}
			n.enqueueAutoRelayCandidate(info)
		}
	}
}

// relayCandidateFromMultiaddr turns "/ip4/.../tcp/4001/p2p/<id>" into the
// AddrInfo autorelay needs. A multiaddr with no /p2p/ component cannot be a
// relay candidate: a reservation is made with a specific peer.
func relayCandidateFromMultiaddr(raw string) (peer.AddrInfo, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return peer.AddrInfo{}, errors.New("empty address")
	}
	addr, err := ma.NewMultiaddr(trimmed)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	return *info, nil
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
