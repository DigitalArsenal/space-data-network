package httpserver

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
	"github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/cenkalti/backoff/v4"
)

type Node = nodepkg.Node

var currentNode *Node
var httpServer *http.Server
var httpsRedirectServer *http.Server

// DDoS Protection Variables
var requestCount = make(map[string][]time.Time)
var requestLock sync.Mutex
var ipBlacklist = make(map[string]bool)
var blacklistLock sync.Mutex

const requestLimit = 10000

var verifyDomainBackoffs = make(map[string]*backoff.ExponentialBackOff)
var verifyDomainCounts = make(map[string]int)
var verifyDomainLock sync.Mutex

var serverUpgradedToHTTPS bool
var serverUpgradeLock sync.Mutex

var handlerWithMiddleware http.Handler

// Define the certificate paths
var certDir string
var certPath string
var keyPath string

// StartHTTPServer checks for existing certificates, and starts the appropriate servers.
func StartHTTPServer(node *Node) {
	currentNode = node
	// Define the certificate paths
	certDir = filepath.Join(serverconfig.Conf.Datastore.Directory, "certificates")
	certPath = filepath.Join(certDir, "server.crt")
	keyPath = filepath.Join(certDir, "server.key")
	nodeIP := getNodeIPAddress(currentNode)

	log.Print(certDir)

	log.Printf("Starting server on node IP address: %s", nodeIP)

	// Ensure the certificate directory exists
	err := os.MkdirAll(certDir, 0700)
	if err != nil {
		log.Fatalf("Failed to create certificate directory: %v", err)
	}

	// Check if the certificates already exist
	if _, errCert := os.Stat(certPath); os.IsNotExist(errCert) {
		log.Println("No existing certificates found. Starting HTTP server with domain verification.")
		startPlainHTTPServer()
		return
	}

	if _, errKey := os.Stat(keyPath); os.IsNotExist(errKey) {
		log.Println("No existing key found. Starting HTTP server with domain verification.")
		startPlainHTTPServer()
		return
	}

	// Certificates exist, start the HTTPS and redirect servers
	log.Println("Certificates found. Starting HTTPS server with HTTP redirection.")
	StartHTTPSServer()
}

// startPlainHTTPServer starts the HTTP server and enables the `/verify-domain` path for domain verification
func startPlainHTTPServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/verify-domain", verifyDomainHandler)

	handlerWithMiddleware = corsMiddleware(rateLimitMiddleware(mux))

	httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", serverconfig.Conf.Webserver.Port),
		Handler: handlerWithMiddleware,
	}

	go func() {
		log.Printf("HTTP server %s serving files from %s\n", httpServer.Addr, serverconfig.Conf.Folders.RootFolder)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("Failed to start HTTP server: %v", err)
		}
	}()
}

// StartHTTPSServer starts the HTTPS server with existing certificates and redirects HTTP traffic to HTTPS.
func StartHTTPSServer() {
	httpsServer := &http.Server{
		Addr:    ":443",
		Handler: handlerWithMiddleware,
	}

	GenerateSelfSignedCert(certPath, keyPath)

	log.Printf("Starting HTTPS server with certificates at %s and %s", certPath, keyPath)

	go func() {
		if err := httpsServer.ListenAndServeTLS(certPath, keyPath); err != nil {
			log.Fatalf("HTTPS server ListenAndServeTLS: %v", err)
		}
	}()

	// Start HTTP redirect to HTTPS
	StartHTTPToHTTPSRedirectServer()
}

// StartHTTPToHTTPSRedirectServer starts the HTTP server to redirect traffic to HTTPS
func StartHTTPToHTTPSRedirectServer() {
	redirectMux := http.NewServeMux()

	// Redirect all HTTP traffic to HTTPS
	redirectMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		target := "https://" + r.Host + r.URL.String()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})

	httpsRedirectServer = &http.Server{
		Addr:    ":80",
		Handler: redirectMux,
	}

	log.Printf("Starting HTTP server for redirecting to HTTPS on port 80")

	go func() {
		if err := httpsRedirectServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP redirect server ListenAndServe: %v", err)
		}
	}()
}

// Stop the HTTP server gracefully
func stopHTTPServer() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server Shutdown: %v", err)
	} else {
		log.Println("HTTP server stopped gracefully")
	}

	// Also stop the redirect HTTP server if running
	if httpsRedirectServer != nil {
		if err := httpsRedirectServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP redirect server Shutdown: %v", err)
		} else {
			log.Println("HTTP redirect server stopped gracefully")
		}
	}
}
