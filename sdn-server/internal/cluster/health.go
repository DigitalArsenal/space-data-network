// Package cluster provides cluster coordination, health checking,
// leader election, and load balancing for multi-node SDN deployments.
package cluster

import (
	"sync"
	"time"
)

// HealthStatus represents the health of a single cluster node.
type HealthStatus struct {
	PeerID        string    `json:"peer_id"`
	OnionHost     string    `json:"onion_host"`
	Role          Role      `json:"role"`
	Load          float64   `json:"load"`           // 0.0–1.0
	Connections   int       `json:"connections"`
	MaxConns      int       `json:"max_connections"`
	TorAlive      bool      `json:"tor_alive"`
	KuboReachable bool      `json:"kubo_reachable"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	Timestamp     time.Time `json:"timestamp"`
}

// Role is the cluster role of a node.
type Role string

const (
	RolePrimary Role = "primary"
	RoleReplica Role = "replica"
	RoleAuto    Role = "auto"
)

// HeartbeatInterval is how often nodes publish their health.
const HeartbeatInterval = 10 * time.Second

// StaleTimeout is how long before a silent node is considered down.
const StaleTimeout = 30 * time.Second

// HealthTracker maintains health state for all cluster peers.
type HealthTracker struct {
	mu    sync.RWMutex
	peers map[string]*HealthStatus // keyed by PeerID
}

// NewHealthTracker creates a new tracker.
func NewHealthTracker() *HealthTracker {
	return &HealthTracker{
		peers: make(map[string]*HealthStatus),
	}
}

// Update records a health status from a peer.
func (ht *HealthTracker) Update(status *HealthStatus) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.peers[status.PeerID] = status
}

// Get returns the latest health status for a peer, or nil if unknown.
func (ht *HealthTracker) Get(peerID string) *HealthStatus {
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	return ht.peers[peerID]
}

// IsAlive returns true if the peer has reported health within StaleTimeout.
func (ht *HealthTracker) IsAlive(peerID string, now time.Time) bool {
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	s, ok := ht.peers[peerID]
	if !ok {
		return false
	}
	return now.Sub(s.Timestamp) < StaleTimeout
}

// LivePeers returns all peers that have reported within StaleTimeout.
func (ht *HealthTracker) LivePeers(now time.Time) []*HealthStatus {
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	var live []*HealthStatus
	for _, s := range ht.peers {
		if now.Sub(s.Timestamp) < StaleTimeout {
			live = append(live, s)
		}
	}
	return live
}

// Remove deletes a peer from the tracker.
func (ht *HealthTracker) Remove(peerID string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	delete(ht.peers, peerID)
}

// PeerCount returns the total number of tracked peers (live or stale).
func (ht *HealthTracker) PeerCount() int {
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	return len(ht.peers)
}
