// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"sync"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("sdn-peers")

// TrustedConnectionGater implements ConnectionGater to enforce trust policies.
type TrustedConnectionGater struct {
	registry    *Registry
	blocklist   map[peer.ID]struct{}
	blocklistMu sync.RWMutex

	// Callback for connection events
	onBlocked func(peerID peer.ID, reason string)
}

// NewTrustedConnectionGater creates a new connection gater.
func NewTrustedConnectionGater(registry *Registry) *TrustedConnectionGater {
	return &TrustedConnectionGater{
		registry:  registry,
		blocklist: make(map[peer.ID]struct{}),
	}
}

// SetBlockedCallback sets a callback for when connections are blocked.
func (g *TrustedConnectionGater) SetBlockedCallback(cb func(peerID peer.ID, reason string)) {
	g.onBlocked = cb
}

// Block adds a peer to the blocklist.
func (g *TrustedConnectionGater) Block(peerID peer.ID) {
	g.blocklistMu.Lock()
	defer g.blocklistMu.Unlock()
	g.blocklist[peerID] = struct{}{}
	log.Infof("Blocked peer: %s", peerID.ShortString())
}

// Unblock removes a peer from the blocklist.
func (g *TrustedConnectionGater) Unblock(peerID peer.ID) {
	g.blocklistMu.Lock()
	defer g.blocklistMu.Unlock()
	delete(g.blocklist, peerID)
	log.Infof("Unblocked peer: %s", peerID.ShortString())
}

// IsBlocked checks if a peer is on the blocklist.
func (g *TrustedConnectionGater) IsBlocked(peerID peer.ID) bool {
	g.blocklistMu.RLock()
	defer g.blocklistMu.RUnlock()
	_, blocked := g.blocklist[peerID]
	return blocked
}

// ListBlocked returns all blocked peer IDs.
func (g *TrustedConnectionGater) ListBlocked() []peer.ID {
	g.blocklistMu.RLock()
	defer g.blocklistMu.RUnlock()
	blocked := make([]peer.ID, 0, len(g.blocklist))
	for id := range g.blocklist {
		blocked = append(blocked, id)
	}
	return blocked
}

// InterceptPeerDial is called before dialing a peer.
func (g *TrustedConnectionGater) InterceptPeerDial(p peer.ID) bool {
	// Check blocklist first
	if g.IsBlocked(p) {
		log.Debugf("Blocked dial to peer %s: on blocklist", p.ShortString())
		if g.onBlocked != nil {
			g.onBlocked(p, "blocklist")
		}
		return false
	}

	// In strict mode, only allow dialing to known peers
	if g.registry.IsStrictMode() {
		if _, err := g.registry.GetPeer(p); err != nil {
			log.Debugf("Blocked dial to peer %s: not in trusted registry (strict mode)", p.ShortString())
			if g.onBlocked != nil {
				g.onBlocked(p, "not in registry (strict mode)")
			}
			return false
		}

		// Check if peer is allowed (trust level > Untrusted)
		if !g.registry.IsAllowed(p) {
			log.Debugf("Blocked dial to peer %s: untrusted", p.ShortString())
			if g.onBlocked != nil {
				g.onBlocked(p, "untrusted")
			}
			return false
		}
	}

	return true
}

// InterceptAddrDial is called before dialing a specific address.
func (g *TrustedConnectionGater) InterceptAddrDial(p peer.ID, addr multiaddr.Multiaddr) bool {
	// Use the same logic as InterceptPeerDial
	return g.InterceptPeerDial(p)
}

// InterceptAccept is called when accepting a connection from a multiaddr.
func (g *TrustedConnectionGater) InterceptAccept(addrs network.ConnMultiaddrs) bool {
	// We can't know the peer ID at this point, so allow all accepts
	// The peer ID check happens in InterceptSecured
	return true
}

// InterceptSecured is called after the security handshake is complete.
func (g *TrustedConnectionGater) InterceptSecured(dir network.Direction, p peer.ID, addrs network.ConnMultiaddrs) bool {
	// Check blocklist first
	if g.IsBlocked(p) {
		log.Debugf("Rejected secured connection from peer %s: on blocklist", p.ShortString())
		if g.onBlocked != nil {
			g.onBlocked(p, "blocklist")
		}
		return false
	}

	// In strict mode, only allow connections from known peers
	if g.registry.IsStrictMode() {
		if _, err := g.registry.GetPeer(p); err != nil {
			log.Debugf("Rejected connection from peer %s: not in trusted registry (strict mode)", p.ShortString())
			if g.onBlocked != nil {
				g.onBlocked(p, "not in registry (strict mode)")
			}
			return false
		}

		// Check if peer is allowed
		if !g.registry.IsAllowed(p) {
			log.Debugf("Rejected connection from peer %s: untrusted", p.ShortString())
			if g.onBlocked != nil {
				g.onBlocked(p, "untrusted")
			}
			return false
		}
	}

	return true
}

// InterceptUpgraded is called after the connection is fully upgraded.
func (g *TrustedConnectionGater) InterceptUpgraded(conn network.Conn) (bool, control.DisconnectReason) {
	peerID := conn.RemotePeer()

	// Record the connection in the registry
	g.registry.RecordConnection(peerID)

	// Log based on trust level
	trustLevel := g.registry.GetTrustLevel(peerID)
	log.Debugf("Connection upgraded with peer %s (trust level: %s)", peerID.ShortString(), trustLevel.String())

	return true, 0
}

// Ensure TrustedConnectionGater implements the ConnectionGater interface.
var _ connmgr.ConnectionGater = (*TrustedConnectionGater)(nil)

// TrustBasedRateLimiter adjusts rate limits based on peer trust level.
type TrustBasedRateLimiter struct {
	registry *Registry
	baseMPS  float64 // Base messages per second
	baseMPM  int     // Base messages per minute
	baseBurst int    // Base burst size
}

// NewTrustBasedRateLimiter creates a rate limiter that adjusts based on trust.
func NewTrustBasedRateLimiter(registry *Registry, baseMPS float64, baseMPM, baseBurst int) *TrustBasedRateLimiter {
	return &TrustBasedRateLimiter{
		registry:  registry,
		baseMPS:   baseMPS,
		baseMPM:   baseMPM,
		baseBurst: baseBurst,
	}
}

// GetLimits returns the rate limits for a peer based on their trust level.
func (trl *TrustBasedRateLimiter) GetLimits(peerID peer.ID) (mps float64, mpm, burst int) {
	trustLevel := trl.registry.GetTrustLevel(peerID)

	switch trustLevel {
	case Untrusted:
		// Should not happen if connection gater is working
		return 0, 0, 0
	case Limited:
		// Very restricted
		return trl.baseMPS * 0.1, trl.baseMPM / 10, trl.baseBurst / 5
	case Standard:
		// Base limits
		return trl.baseMPS, trl.baseMPM, trl.baseBurst
	case Trusted:
		// Relaxed limits
		return trl.baseMPS * 5, trl.baseMPM * 5, trl.baseBurst * 3
	case Admin:
		// Essentially unlimited
		return trl.baseMPS * 100, trl.baseMPM * 100, trl.baseBurst * 10
	default:
		return trl.baseMPS, trl.baseMPM, trl.baseBurst
	}
}

// NotificationSubscriber handles peer registry events.
type NotificationSubscriber interface {
	OnPeerAdded(tp *TrustedPeer)
	OnPeerRemoved(peerID peer.ID)
	OnPeerUpdated(tp *TrustedPeer)
	OnTrustLevelChanged(peerID peer.ID, oldLevel, newLevel TrustLevel)
}

// NotifyingRegistry wraps a Registry with notification support.
type NotifyingRegistry struct {
	*Registry
	subscribers []NotificationSubscriber
	subMu       sync.RWMutex
}

// NewNotifyingRegistry creates a registry with notification support.
func NewNotifyingRegistry(strictMode bool, persistence PersistenceProvider) *NotifyingRegistry {
	return &NotifyingRegistry{
		Registry:    NewRegistry(strictMode, persistence),
		subscribers: make([]NotificationSubscriber, 0),
	}
}

// Subscribe adds a notification subscriber.
func (nr *NotifyingRegistry) Subscribe(sub NotificationSubscriber) {
	nr.subMu.Lock()
	defer nr.subMu.Unlock()
	nr.subscribers = append(nr.subscribers, sub)
}

// Unsubscribe removes a notification subscriber.
func (nr *NotifyingRegistry) Unsubscribe(sub NotificationSubscriber) {
	nr.subMu.Lock()
	defer nr.subMu.Unlock()
	for i, s := range nr.subscribers {
		if s == sub {
			nr.subscribers = append(nr.subscribers[:i], nr.subscribers[i+1:]...)
			return
		}
	}
}

// AddPeer adds a peer and notifies subscribers.
func (nr *NotifyingRegistry) AddPeer(tp *TrustedPeer) error {
	if err := nr.Registry.AddPeer(tp); err != nil {
		return err
	}

	nr.subMu.RLock()
	defer nr.subMu.RUnlock()
	for _, sub := range nr.subscribers {
		sub.OnPeerAdded(tp)
	}

	return nil
}

// RemovePeer removes a peer and notifies subscribers.
func (nr *NotifyingRegistry) RemovePeer(id peer.ID) error {
	if err := nr.Registry.RemovePeer(id); err != nil {
		return err
	}

	nr.subMu.RLock()
	defer nr.subMu.RUnlock()
	for _, sub := range nr.subscribers {
		sub.OnPeerRemoved(id)
	}

	return nil
}

// UpdatePeer updates a peer and notifies subscribers.
func (nr *NotifyingRegistry) UpdatePeer(tp *TrustedPeer) error {
	if err := nr.Registry.UpdatePeer(tp); err != nil {
		return err
	}

	nr.subMu.RLock()
	defer nr.subMu.RUnlock()
	for _, sub := range nr.subscribers {
		sub.OnPeerUpdated(tp)
	}

	return nil
}

// SetTrustLevel updates trust level and notifies subscribers.
func (nr *NotifyingRegistry) SetTrustLevel(id peer.ID, level TrustLevel) error {
	oldLevel := nr.GetTrustLevel(id)

	if err := nr.Registry.SetTrustLevel(id, level); err != nil {
		return err
	}

	if oldLevel != level {
		nr.subMu.RLock()
		defer nr.subMu.RUnlock()
		for _, sub := range nr.subscribers {
			sub.OnTrustLevelChanged(id, oldLevel, level)
		}
	}

	return nil
}
