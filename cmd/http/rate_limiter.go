package httpserver

import (
	"log"
	"net"
	"net/http"
	"time"
)

// Middleware to enforce rate limiting with 1000 requests per hour
func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Unable to determine IP address", http.StatusInternalServerError)
			return
		}

		// Check if IP is blacklisted
		blacklistLock.Lock()
		if ipBlacklist[ip] {
			blacklistLock.Unlock()
			http.Error(w, "Your IP is temporarily blacklisted due to excessive requests", http.StatusTooManyRequests)
			return
		}
		blacklistLock.Unlock()

		// Enforce rate limiting
		requestLock.Lock()
		// Get the current timestamp and filter out requests older than 1 hour
		now := time.Now()
		requestTimes := requestCount[ip]
		newRequestTimes := []time.Time{}
		for _, t := range requestTimes {
			if now.Sub(t) <= time.Hour {
				newRequestTimes = append(newRequestTimes, t)
			}
		}

		// If the request count exceeds 1000 in the last hour, block the IP
		if len(newRequestTimes) >= requestLimit {
			requestLock.Unlock()
			log.Printf("IP %s has exceeded 1000 requests per hour limit", ip)
			http.Error(w, "Rate limit exceeded. Try again later.", http.StatusTooManyRequests)
			return
		}

		// Add the current request time
		newRequestTimes = append(newRequestTimes, now)
		requestCount[ip] = newRequestTimes
		requestLock.Unlock()

		// Serve the next handler
		next.ServeHTTP(w, r)
	})
}
