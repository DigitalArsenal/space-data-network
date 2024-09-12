package httpserver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"

	"github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/ethereum/go-ethereum/crypto"
)

type Node = nodepkg.Node

var currentNode *Node
var httpServer *http.Server // Global variable to keep track of the HTTP server

// Handler for verifying the domain from the GET request
func verifyDomainHandler(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// Extract the domain from the query string
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, "Missing domain query parameter", http.StatusBadRequest)
		return
	}

	// Get the node's IP addresses
	nodeIPs := []string{}
	for _, addr := range currentNode.Host.Addrs() { // assuming currentNode is properly initialized
		ip, err := addr.ValueForProtocol(net.IPv4len)
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
	}{
		Domain:      domain,
		NodeIPs:     nodeIPs,
		DomainIPs:   domainIPs,
		MatchingIPs: matchingIPs,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// Function to verify if the signature comes from an approved Ethereum address
func verifyEthereumSignature(signature string, message string, approvedAddress string) (bool, error) {
	// Decode the signature
	sigBytes, err := hex.DecodeString(signature)
	if err != nil {
		return false, fmt.Errorf("invalid signature format: %v", err)
	}

	// Hash the message
	msgHash := crypto.Keccak256Hash([]byte(message))

	// Recover the public key from the signature
	pubKey, err := crypto.SigToPub(msgHash.Bytes(), sigBytes)
	if err != nil {
		return false, fmt.Errorf("failed to recover public key: %v", err)
	}

	// Get the Ethereum address from the public key
	recoveredAddr := crypto.PubkeyToAddress(*pubKey)

	// Compare recovered address with the approved address
	if recoveredAddr.Hex() == approvedAddress {
		return true, nil
	}

	return false, nil
}

// Handler for POST requests to check Ethereum signature
func postHandler(w http.ResponseWriter, r *http.Request) {
	// Only allow POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// Read the signature and message from headers (example: X-Signature, X-Message)
	signature := r.Header.Get("X-Signature")
	message := r.Header.Get("X-Message")

	if signature == "" || message == "" {
		http.Error(w, "Missing signature or message", http.StatusBadRequest)
		return
	}

	// Approved Ethereum address (could be configured elsewhere)
	approvedAddress := "0xYourApprovedEthereumAddress"

	// Verify the signature
	valid, err := verifyEthereumSignature(signature, message, approvedAddress)
	if err != nil {
		http.Error(w, fmt.Sprintf("Signature verification failed: %v", err), http.StatusInternalServerError)
		return
	}

	if valid {
		fmt.Fprintf(w, "Signature is valid and from approved address")
	} else {
		http.Error(w, "Invalid signature or unauthorized address", http.StatusUnauthorized)
	}
}

// Middleware to enable CORS
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Signature, X-Message")

		// Handle preflight request (OPTIONS)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

func helloHandler(w http.ResponseWriter, r *http.Request) {
	// Handling root path for non-file requests
	if r.URL.Path == "/" {
		// Construct the path to index.html inside the RootFolder
		indexFilePath := serverconfig.Conf.Folders.RootFolder + "/index.html"

		// Check if index.html exists in the RootFolder
		if _, err := os.Stat(indexFilePath); err == nil {
			// Serve index.html from RootFolder
			http.ServeFile(w, r, indexFilePath)
		} else {
			// Serve a simple HTML page if index.html is missing
			fmt.Fprintf(w, "<html><body><h1>INDEX.HTML MISSING</h1></body></html>")
		}
		return
	}

	// Serve other files from the RootFolder
	http.FileServer(http.Dir(serverconfig.Conf.Folders.RootFolder)).ServeHTTP(w, r)
}

// Helper function to extract the node's IP address
func getNodeIPAddress(node *Node) string {
	// Attempt to retrieve IP addresses from the node's multiaddresses
	for _, addr := range node.Host.Addrs() {
		ip, err := addr.ValueForProtocol(net.IPv4len)
		if err == nil {
			return ip // Return the first valid IP address found
		}
	}
	return ""
}

// Updated StartHTTPServer function to include the middleware
func StartHTTPServer(node *Node) {
	currentNode = node
	nodeIP := getNodeIPAddress(currentNode)

	// Logging the node's IP address for verification
	log.Printf("Starting server on node IP address: %s", nodeIP)

	// Create a new ServeMux
	mux := http.NewServeMux()

	// Register handler to serve files from the root directory
	mux.HandleFunc("/", helloHandler) // helloHandler now also handles file serving

	// Register handler for POST requests to verify Ethereum signature
	mux.HandleFunc("/verify", postHandler)

	// Register handler for domain verification
	mux.HandleFunc("/verify-domain", verifyDomainHandler)

	// Wrap the entire mux with the CORS middleware
	handlerWithCORS := corsMiddleware(mux)

	// Create a server instance
	httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", serverconfig.Conf.Webserver.Port),
		Handler: handlerWithCORS, // Use the handler with CORS
	}

	// Start server in a goroutine so that it doesn't block.
	go func() {
		log.Printf("HTTP server %s serving files from %s\n", httpServer.Addr, serverconfig.Conf.Folders.RootFolder)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("Failed to start HTTP server: %v", err)
		}
	}()
}

// Stops the HTTP server gracefully
func stopHTTPServer() {
	// Create a deadline to wait for.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Doesn't block if no connections, but will wait for the timeout if there are active connections.
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server Shutdown: %v", err)
	} else {
		log.Println("HTTP server stopped gracefully")
	}
}
