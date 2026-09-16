package node

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

// OWNER RULE: every public node that is not behind NAT relays, by default.
//
// A network whose relays are only the ones someone remembered to configure
// cannot carry the nodes that most need carrying. Measured: a node behind a
// home router with UPnP became directly reachable in about 40 seconds and found
// 28 peers, while the same build behind a firewall that would not map ports sat
// at 2 peers — the two bootstrap nodes — with ZERO relay reservations after
// seven minutes, because nothing in the fleet offered a reservation to take.
// The nodes that ARE reachable are precisely the ones that can fix that.
//
// Conditional, and bounded. On 2026-08-08 host-01 ran the HOP service
// unconditionally and sat at 98.5% CPU of 2 vCPUs with 780 inbound connections
// from ~700 distinct IPs while its real work starved. Two things keep that from
// recurring: the service only runs while AutoNAT says this node is publicly
// reachable, and it runs under the limits below rather than libp2p's defaults.
//
// The limits are deliberately tighter than relay.DefaultResources(): this is a
// contribution a node makes with its spare capacity, not a service it exists to
// provide. An operator who wants the bigger numbers sets enable_relay: always
// and gets libp2p's own defaults.
func autoRelayResources() relay.Resources {
	res := relay.DefaultResources()
	res.MaxReservations = 32 // default 128
	res.MaxCircuits = 8      // default 16, per peer
	res.MaxReservationsPerIP = 4
	res.MaxReservationsPerASN = 8
	res.ReservationTTL = 30 * time.Minute
	return res
}

// runAutoRelayService starts the circuit-relay HOP service while this node is
// publicly reachable and stops it when it is not. It returns when ctx is done.
func (n *Node) runAutoRelayService(ctx context.Context) {
	if n == nil || n.host == nil {
		return
	}

	sub, err := n.host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		log.Warnf("Relay auto-mode unavailable (reachability events): %v; this node will not relay", err)
		return
	}
	defer sub.Close()

	var (
		mu      sync.Mutex
		running *relay.Relay
	)
	stop := func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		if running == nil {
			return
		}
		if closeErr := running.Close(); closeErr != nil {
			log.Warnf("Circuit-relay HOP service did not close cleanly: %v", closeErr)
		}
		running = nil
		log.Infof("Circuit-relay HOP service STOPPED: %s", reason)
	}
	defer stop("node shutting down")

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Out():
			if !ok {
				return
			}
			changed, isReachability := ev.(event.EvtLocalReachabilityChanged)
			if !isReachability {
				continue
			}
			switch changed.Reachability {
			case network.ReachabilityPublic:
				mu.Lock()
				already := running != nil
				mu.Unlock()
				if already {
					continue
				}
				started, startErr := relay.New(n.host, relay.WithResources(autoRelayResources()))
				if startErr != nil {
					log.Warnf("Could not start the circuit-relay HOP service: %v", startErr)
					continue
				}
				mu.Lock()
				running = started
				mu.Unlock()
				res := autoRelayResources()
				log.Infof("Circuit-relay HOP service STARTED: AutoNAT reports this node publicly reachable, so it now "+
					"carries traffic for peers that cannot be dialled (max %d reservations, %d circuits per peer, %s TTL). "+
					"Set network.enable_relay: never to opt out.",
					res.MaxReservations, res.MaxCircuits, res.ReservationTTL)
			case network.ReachabilityPrivate:
				stop("AutoNAT reports this node is behind NAT; a node that cannot be dialled cannot relay")
			default:
				// Unknown: leave whatever state we are in. Flapping the service
				// on every inconclusive probe would be worse than either answer.
			}
		}
	}
}
