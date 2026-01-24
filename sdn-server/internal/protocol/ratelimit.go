// Package protocol provides the SDS exchange protocol handlers.
package protocol

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/time/rate"
)

// RateLimitConfig contains rate limiting configuration.
type RateLimitConfig struct {
	// MaxMessagesPerSecond is the sustained rate limit per peer.
	MaxMessagesPerSecond float64
	// MaxMessagesPerMinute is an additional per-minute limit per peer.
	MaxMessagesPerMinute int
	// Burst is the maximum burst size allowed.
	Burst int
}

// DefaultRateLimitConfig returns sensible default rate limiting configuration.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		MaxMessagesPerSecond: 100,
		MaxMessagesPerMinute: 1000,
		Burst:                50,
	}
}

// peerLimiter holds rate limiters for a single peer.
type peerLimiter struct {
	// Token bucket limiter for per-second rate limiting
	limiter *rate.Limiter
	// Sliding window counter for per-minute rate limiting
	minuteCount  int
	minuteWindow time.Time
	// Last activity time for cleanup
	lastActive time.Time
}

// PeerRateLimiter manages rate limiting for multiple peers.
type PeerRateLimiter struct {
	config   RateLimitConfig
	limiters map[peer.ID]*peerLimiter
	mu       sync.RWMutex

	// Cleanup settings
	cleanupInterval time.Duration
	maxIdleTime     time.Duration
	stopCleanup     chan struct{}
}

// NewPeerRateLimiter creates a new rate limiter for tracking per-peer message rates.
func NewPeerRateLimiter(config RateLimitConfig) *PeerRateLimiter {
	prl := &PeerRateLimiter{
		config:          config,
		limiters:        make(map[peer.ID]*peerLimiter),
		cleanupInterval: 5 * time.Minute,
		maxIdleTime:     10 * time.Minute,
		stopCleanup:     make(chan struct{}),
	}

	// Start background cleanup goroutine
	go prl.cleanupLoop()

	return prl
}

// Allow checks if a message from the given peer should be allowed.
// Returns true if the message is allowed, false if rate limited.
func (prl *PeerRateLimiter) Allow(peerID peer.ID) bool {
	prl.mu.Lock()
	defer prl.mu.Unlock()

	now := time.Now()
	pl, exists := prl.limiters[peerID]

	if !exists {
		// Create new limiter for this peer
		pl = &peerLimiter{
			limiter:      rate.NewLimiter(rate.Limit(prl.config.MaxMessagesPerSecond), prl.config.Burst),
			minuteCount:  0,
			minuteWindow: now.Truncate(time.Minute),
			lastActive:   now,
		}
		prl.limiters[peerID] = pl
	}

	pl.lastActive = now

	// Check per-second rate limit using token bucket
	if !pl.limiter.Allow() {
		log.Debugf("Rate limit exceeded (per-second) for peer %s", peerID.ShortString())
		return false
	}

	// Check per-minute rate limit using sliding window
	currentMinute := now.Truncate(time.Minute)
	if currentMinute.After(pl.minuteWindow) {
		// New minute window, reset counter
		pl.minuteCount = 0
		pl.minuteWindow = currentMinute
	}

	pl.minuteCount++
	if pl.minuteCount > prl.config.MaxMessagesPerMinute {
		log.Debugf("Rate limit exceeded (per-minute) for peer %s: %d/%d", peerID.ShortString(), pl.minuteCount, prl.config.MaxMessagesPerMinute)
		return false
	}

	return true
}

// GetPeerStats returns rate limiting statistics for a peer.
// Returns (messagesThisMinute, isLimited).
func (prl *PeerRateLimiter) GetPeerStats(peerID peer.ID) (int, bool) {
	prl.mu.RLock()
	defer prl.mu.RUnlock()

	pl, exists := prl.limiters[peerID]
	if !exists {
		return 0, false
	}

	now := time.Now()
	currentMinute := now.Truncate(time.Minute)
	if currentMinute.After(pl.minuteWindow) {
		return 0, false
	}

	isLimited := pl.minuteCount >= prl.config.MaxMessagesPerMinute
	return pl.minuteCount, isLimited
}

// Reset clears rate limiting state for a specific peer.
func (prl *PeerRateLimiter) Reset(peerID peer.ID) {
	prl.mu.Lock()
	defer prl.mu.Unlock()

	delete(prl.limiters, peerID)
}

// ResetAll clears all rate limiting state.
func (prl *PeerRateLimiter) ResetAll() {
	prl.mu.Lock()
	defer prl.mu.Unlock()

	prl.limiters = make(map[peer.ID]*peerLimiter)
}

// Close stops the background cleanup goroutine.
func (prl *PeerRateLimiter) Close() {
	close(prl.stopCleanup)
}

// cleanupLoop periodically removes idle peer limiters to prevent memory leaks.
func (prl *PeerRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(prl.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			prl.cleanup()
		case <-prl.stopCleanup:
			return
		}
	}
}

// cleanup removes limiters for peers that have been idle too long.
func (prl *PeerRateLimiter) cleanup() {
	prl.mu.Lock()
	defer prl.mu.Unlock()

	now := time.Now()
	for peerID, pl := range prl.limiters {
		if now.Sub(pl.lastActive) > prl.maxIdleTime {
			delete(prl.limiters, peerID)
			log.Debugf("Cleaned up rate limiter for idle peer %s", peerID.ShortString())
		}
	}
}

// PeerCount returns the number of peers currently being tracked.
func (prl *PeerRateLimiter) PeerCount() int {
	prl.mu.RLock()
	defer prl.mu.RUnlock()

	return len(prl.limiters)
}
