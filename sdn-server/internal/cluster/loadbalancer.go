package cluster

import (
	"math"
	"sort"
	"sync"
)

const (
	// maxLatencyMs caps the latency normalization window.
	maxLatencyMs = 5000.0

	// maxFailures is the failure count at which a backend is removed.
	maxFailures = 3
)

// BackendScore holds scoring data for a cluster backend.
type BackendScore struct {
	PeerID       string
	Load         float64 // 0.0–1.0
	LatencyMs    float64 // round-trip time in milliseconds
	FailureCount int
}

// Score computes a weighted score (lower is better) matching the JS SDK
// algorithm: score = (load * 50) + (normalizedLatency * 30) + (failureScore * 20)
func (bs *BackendScore) Score() float64 {
	normalizedLatency := math.Min(bs.LatencyMs/maxLatencyMs, 1.0)
	failureScore := math.Min(float64(bs.FailureCount)/float64(maxFailures), 1.0)
	return (bs.Load * 50) + (normalizedLatency * 30) + (failureScore * 20)
}

// LoadBalancer tracks and scores cluster backends for routing decisions.
type LoadBalancer struct {
	mu       sync.RWMutex
	backends map[string]*BackendScore
}

// NewLoadBalancer creates a new load balancer.
func NewLoadBalancer() *LoadBalancer {
	return &LoadBalancer{
		backends: make(map[string]*BackendScore),
	}
}

// Update records or updates a backend's metrics.
func (lb *LoadBalancer) Update(score *BackendScore) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.backends[score.PeerID] = score
}

// MarkFailed increments a backend's failure counter. Returns true if the
// backend should be removed (reached maxFailures).
func (lb *LoadBalancer) MarkFailed(peerID string) bool {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	bs, ok := lb.backends[peerID]
	if !ok {
		return false
	}
	bs.FailureCount++
	if bs.FailureCount >= maxFailures {
		delete(lb.backends, peerID)
		return true
	}
	return false
}

// MarkSuccess resets a backend's failure counter.
func (lb *LoadBalancer) MarkSuccess(peerID string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if bs, ok := lb.backends[peerID]; ok {
		bs.FailureCount = 0
	}
}

// Remove deletes a backend.
func (lb *LoadBalancer) Remove(peerID string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	delete(lb.backends, peerID)
}

// GetBestBackends returns the top N backends sorted by score (lowest first).
func (lb *LoadBalancer) GetBestBackends(count int) []*BackendScore {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	all := make([]*BackendScore, 0, len(lb.backends))
	for _, bs := range lb.backends {
		all = append(all, bs)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Score() < all[j].Score()
	})

	if count > len(all) {
		count = len(all)
	}
	return all[:count]
}

// BackendCount returns the number of tracked backends.
func (lb *LoadBalancer) BackendCount() int {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return len(lb.backends)
}
