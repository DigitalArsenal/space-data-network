package httpserver

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// Handler for verifying the domain from the GET request
func verifyDomainHandler(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// Extract the IP address of the requester
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "Unable to determine IP address", http.StatusInternalServerError)
		return
	}

	// Apply exponential backoff starting from the second request
	verifyDomainLock.Lock()
	verifyDomainCounts[ip]++

	if verifyDomainCounts[ip] > 1 {
		if verifyDomainBackoffs[ip] == nil {
			expBackoff := backoff.NewExponentialBackOff()
			expBackoff.InitialInterval = 1 * time.Second
			expBackoff.MaxInterval = 60 * time.Second
			expBackoff.MaxElapsedTime = 15 * time.Minute
			verifyDomainBackoffs[ip] = expBackoff
		}
		nextBackoff := verifyDomainBackoffs[ip].NextBackOff()
		if nextBackoff == backoff.Stop {
			// MaxElapsedTime exceeded
			verifyDomainLock.Unlock()
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}
		verifyDomainLock.Unlock()
		// Sleep for the duration
		time.Sleep(nextBackoff)
	} else {
		verifyDomainLock.Unlock()
	}

	// Extract the domain from the query string
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, "Missing domain query parameter", http.StatusBadRequest)
		return
	}

	// Check if the domain is 'localhost'
	if domain == "localhost" {
		// If domain is localhost, run self-signed certificate generator and start HTTPS server with it
		serverUpgradeLock.Lock()
		if !serverUpgradedToHTTPS {
			serverUpgradedToHTTPS = true
			serverUpgradeLock.Unlock()
			// Generate and start the HTTPS server using self-signed certificate
			stopHTTPServer()
			go StartHTTPSServer()
			response := struct {
				Message string `json:"message"`
			}{
				Message: "Server is restarting with self-signed HTTPS for localhost",
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				http.Error(w, "Failed to encode response", http.StatusInternalServerError)
			}
			return
		} else {
			serverUpgradeLock.Unlock()
			response := struct {
				Message string `json:"message"`
			}{
				Message: "Server is already running with HTTPS",
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				http.Error(w, "Failed to encode response", http.StatusInternalServerError)
			}
			return
		}
	}

	// Get the node's IP addresses for other domains
	nodeIPs := []string{}
	for _, addr := range currentNode.Host.Addrs() {
		ip, _, err := net.SplitHostPort(addr.String())
		if err == nil {
			nodeIPs = append(nodeIPs, ip)
		}
	}

	// Perform a DNS lookup to get the IP addresses for the domain
	domainIPs, err := net.LookupHost(domain)
	if err != nil {
		http.Error(w, "Failed to resolve domain", http.StatusInternalServerError)
		return
	}

	// Compare the node IPs with the domain IPs
	matchingIPs := []string{}
	for _, nodeIP := range nodeIPs {
		for _, domainIP := range domainIPs {
			if nodeIP == domainIP {
				matchingIPs = append(matchingIPs, nodeIP)
			}
		}
	}

	// Log the domain, node IPs, and matching IPs
	log.Printf("Verifying domain: %s", domain)
	log.Printf("Node IP addresses: %v", nodeIPs)
	log.Printf("Domain IP addresses: %v", domainIPs)
	log.Printf("Matching IP addresses: %v", matchingIPs)

	// Return the response in JSON format with the domain IPs, node IPs, and matching IPs
	response := struct {
		Domain      string   `json:"domain"`
		NodeIPs     []string `json:"node_ips"`
		DomainIPs   []string `json:"domain_ips"`
		MatchingIPs []string `json:"matching_ips"`
		Message     string   `json:"message"`
	}{
		Domain:      domain,
		NodeIPs:     nodeIPs,
		DomainIPs:   domainIPs,
		MatchingIPs: matchingIPs,
	}

	if len(domainIPs) > 0 {
		// If domain is localhost, run self-signed certificate generator and start HTTPS server with it
		serverUpgradeLock.Lock()
		if !serverUpgradedToHTTPS {
			serverUpgradedToHTTPS = true
			serverUpgradeLock.Unlock()
			// Generate and start the HTTPS server using self-signed certificate
			stopHTTPServer()
			go StartHTTPSServer()
			response := struct {
				Message string `json:"message"`
			}{
				Message: "Server is restarting with self-signed HTTPS for localhost",
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				http.Error(w, "Failed to encode response", http.StatusInternalServerError)
			}
			return
		} else {
			serverUpgradeLock.Unlock()
			response := struct {
				Message string `json:"message"`
			}{
				Message: "Server is already running with HTTPS",
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				http.Error(w, "Failed to encode response", http.StatusInternalServerError)
			}
			return
		}
	} else {
		response.Message = "Domain verification failed. No matching IPs."
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}
