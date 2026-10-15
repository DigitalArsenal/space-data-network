package api

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/netorigin"
)

const (
	readLimitPerMin  = 100
	writeLimitPerMin = 10
)

// rateLimiter implements per-client-IP token bucket rate limiting.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
}

type ipBucket struct {
	readTokens  float64
	writeTokens float64
	lastRefill  time.Time
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{buckets: make(map[string]*ipBucket)}
	go rl.cleanupLoop()
	return rl
}

func (rl *rateLimiter) cleanupLoop() {
	for range time.Tick(5 * time.Minute) {
		rl.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for ip, b := range rl.buckets {
			if b.lastRefill.Before(cutoff) {
				delete(rl.buckets, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// bucket returns (and refills) the ipBucket for the given IP. Caller must NOT hold rl.mu.
func (rl *rateLimiter) bucket(ip string) *ipBucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[ip]
	if !ok {
		b = &ipBucket{
			readTokens:  readLimitPerMin,
			writeTokens: writeLimitPerMin,
			lastRefill:  time.Now(),
		}
		rl.buckets[ip] = b
		return b
	}
	// Refill tokens proportional to elapsed time.
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Minutes()
	b.lastRefill = now
	b.readTokens += elapsed * readLimitPerMin
	if b.readTokens > readLimitPerMin {
		b.readTokens = readLimitPerMin
	}
	b.writeTokens += elapsed * writeLimitPerMin
	if b.writeTokens > writeLimitPerMin {
		b.writeTokens = writeLimitPerMin
	}
	return b
}

// isWriteMethod returns true for methods that consume the write bucket.
func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// Allow checks the rate limit for the request, sets X-RateLimit-* headers,
// and returns false (writing a 429) if the limit is exceeded.
func (rl *rateLimiter) Allow(w http.ResponseWriter, r *http.Request) bool {
	return rl.allow(w, r, isWriteMethod(r.Method))
}

// AllowRead is reserved for routes whose handlers enforce a read-only operation,
// including binary record queries carried by POST. It never changes r.Method.
func (rl *rateLimiter) AllowRead(w http.ResponseWriter, r *http.Request) bool {
	return rl.allow(w, r, false)
}

func (rl *rateLimiter) allow(w http.ResponseWriter, r *http.Request, write bool) bool {
	// One bucket per IPv4 client, one per IPv6 /64 (an IPv6 subscriber
	// holds a whole /64; keyed per address it had endless fresh buckets).
	b := rl.bucket(netorigin.RateLimitKey(r))

	rl.mu.Lock()
	defer rl.mu.Unlock()

	var limit float64
	var tokens *float64
	if write {
		limit = writeLimitPerMin
		tokens = &b.writeTokens
	} else {
		limit = readLimitPerMin
		tokens = &b.readTokens
	}

	resetTime := b.lastRefill.Add(time.Minute)

	if *tokens < 1 {
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", math.Max(1, math.Ceil((1-*tokens)*60/limit))))
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", limit))
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetTime.Unix()))
		writeCoreAPIError(w, http.StatusTooManyRequests, "RATE_LIMITED", "rate limit exceeded")
		return false
	}

	*tokens -= 1
	remaining := int(*tokens)
	w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", limit))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetTime.Unix()))
	return true
}

// clientIP extracts the client IP from the request by the node-wide rule
// (internal/netorigin.ClientIP).
//
// Forwarding headers are honoured only when the direct peer is loopback — a
// reverse proxy on this host. Anyone else can write X-Forwarded-For, and
// trusting it from a remote peer let one caller pick its own bucket (SEC-05).
// Behind the local proxy the client is the RIGHTMOST entry, the one the proxy
// appended: the leftmost entry is whatever the client sent, so taking it gave
// every request a fresh bucket for the price of a header (SEC-062).
func clientIP(r *http.Request) string {
	return netorigin.ClientIP(r)
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}
