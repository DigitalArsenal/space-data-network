package api

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
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
	ip := clientIP(r)
	b := rl.bucket(ip)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	var limit float64
	var tokens *float64
	if isWriteMethod(r.Method) {
		limit = writeLimitPerMin
		tokens = &b.writeTokens
	} else {
		limit = readLimitPerMin
		tokens = &b.readTokens
	}

	resetTime := b.lastRefill.Add(time.Minute)

	if *tokens < 1 {
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

// clientIP extracts the best-effort client IP from the request.
//
// Forwarding headers are honoured only when the direct peer is loopback —
// a reverse proxy on this host. Anyone else can write X-Forwarded-For, and
// trusting it from a remote peer let one caller pick its own bucket (or
// someone else's) and walk past the per-IP limit (SEC-05).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isLoopbackHost(host) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first (leftmost) address — that is the original client.
		if idx := strings.Index(xff, ","); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	return host
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}
