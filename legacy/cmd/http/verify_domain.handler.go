// verify_domain_handler.go

package httpserver

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// isLocalDomain checks if the domain is 'localhost' or an IP address
func isLocalDomain(domain string) bool {
	if domain == "localhost" {
		return true
	}
	// Check if the domain is a valid IP address
	ip := net.ParseIP(domain)
	return ip != nil
}

// verifyDomainHandler handles the `/verify-domain` endpoint.
func verifyDomainHandler(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		log.Printf("Invalid request method: %s from %s", r.Method, r.RemoteAddr)
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// Extract the IP address of the requester
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		log.Printf("Unable to determine IP address from %s: %v", r.RemoteAddr, err)
		http.Error(w, "Unable to determine IP address", http.StatusInternalServerError)
		return
	}

	// Apply exponential backoff starting from the second request
	verifyDomainLock.Lock()
	verifyDomainCounts[ip]++
	count := verifyDomainCounts[ip]
	if count > 1 {
		if verifyDomainBackoffs[ip] == nil {
			expBackoff := backoff.NewExponentialBackOff()
			expBackoff.InitialInterval = 1 * time.Second
			expBackoff.MaxInterval = 60 * time.Second
			expBackoff.MaxElapsedTime = 15 * time.Minute
			verifyDomainBackoffs[ip] = expBackoff
			log.Printf("Initialized backoff for IP: %s", ip)
		}
		nextBackoff := verifyDomainBackoffs[ip].NextBackOff()
		if nextBackoff == backoff.Stop {
			// MaxElapsedTime exceeded
			verifyDomainLock.Unlock()
			log.Printf("Too many requests from IP: %s", ip)
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}
		verifyDomainLock.Unlock()
		// Sleep for the duration
		log.Printf("Applying backoff for IP: %s, sleeping for %v", ip, nextBackoff)
		time.Sleep(nextBackoff)
	} else {
		verifyDomainLock.Unlock()
	}

	// Extract the domain from the query string
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		log.Printf("Missing domain query parameter from IP: %s", ip)
		http.Error(w, "Missing domain query parameter", http.StatusBadRequest)
		return
	}

	// Check if the domain is 'localhost' or an IP address
	if isLocalDomain(domain) {
		log.Printf("Domain '%s' is local. HTTPS is already using a self-signed certificate.", domain)
		response := map[string]string{
			"message": "Domain is local. HTTPS is already using a self-signed certificate.",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// Perform DNS lookup for domain verification
	domainIPs, err := net.LookupHost(domain)
	if err != nil {
		log.Printf("Failed to resolve domain '%s': %v", domain, err)
		http.Error(w, "Failed to resolve domain", http.StatusInternalServerError)
		return
	}

	log.Printf("Domain '%s' resolved to IPs: %v", domain, domainIPs)

	// Get server's primary IP address
	serverIP := getServerPrimaryIP()
	if serverIP == "" {
		log.Println("Unable to determine server's primary IP address.")
		http.Error(w, "Server IP not found", http.StatusInternalServerError)
		return
	}

	// Check if the domain resolves to the server's IP
	domainPointsToServer := false
	for _, ipAddr := range domainIPs {
		if ipAddr == serverIP {
			domainPointsToServer = true
			break
		}
	}

	if !domainPointsToServer {
		log.Printf("Domain '%s' does not point to the server's IP (%s).", domain, serverIP)
		http.Error(w, "Domain does not point to the server's IP", http.StatusBadRequest)
		return
	}

	log.Printf("Domain '%s' successfully points to the server's IP (%s).", domain, serverIP)

	// Upgrade HTTPS server to use autocert-managed certificates
	go func(verifiedDomain string) {
		if autocertManager != nil {
			return
		}
		// Prevent multiple upgrades for the same domain
		verifyDomainLock.Lock()
		if _, exists := verifiedDomains[verifiedDomain]; exists {
			verifyDomainLock.Unlock()
			log.Printf("Domain '%s' has already been verified and processed.", verifiedDomain)
			return
		}
		verifiedDomains[verifiedDomain] = true
		verifyDomainLock.Unlock()

		// Upgrade HTTPS server
		UpgradeHTTPSWithAutocert(verifiedDomain)
	}(domain)

	// Respond back to the client
	response := map[string]interface{}{
		"domain":    domain,
		"domainIPs": domainIPs,
		"message":   "Domain verification successful. HTTPS server is being upgraded to use Let's Encrypt certificates.",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	log.Printf("Domain '%s' verified successfully. HTTPS server upgrade initiated.", domain)
}

// getServerPrimaryIP retrieves the server's primary IP address.
func getServerPrimaryIP() string {
	// This function should return the server's primary IP address.
	// Implementation depends on the environment.
	// Here's a simple implementation that picks the first non-loopback interface's IP.

	interfaces, err := net.Interfaces()
	if err != nil {
		log.Printf("Failed to get network interfaces: %v", err)
		return ""
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}
		addrs, err := iface.Addrs()
		if err != nil {
			log.Printf("Failed to get addresses for interface %s: %v", iface.Name, err)
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue // not an ipv4 address
			}
			return ip.String()
		}
	}

	log.Println("No suitable non-loopback IPv4 address found.")
	return ""
}
