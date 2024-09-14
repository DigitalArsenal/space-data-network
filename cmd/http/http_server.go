package httpserver

import (
	"context"
	"fmt"
	"log"
	"net/http"
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

// StartHTTPServer initializes and starts the HTTP server
func StartHTTPServer(node *Node) {
	currentNode = node
	nodeIP := getNodeIPAddress(currentNode)

	log.Printf("Starting server on node IP address: %s", nodeIP)

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

// StartHTTPSServer starts the HTTPS server with Let's Encrypt certificates
func StartHTTPSServer(domain string) {
	InitializeAutocertManager(domain)

	go StartAutocertChallengeServer()

	httpsServer := &http.Server{
		Addr:      ":443",
		Handler:   handlerWithMiddleware,
		TLSConfig: GetTLSConfig(),
	}

	log.Printf("Starting HTTPS server on %s", httpsServer.Addr)

	go func() {
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil {
			log.Fatalf("HTTPS server ListenAndServeTLS: %v", err)
		}
	}()

	// Start redirect HTTP server after HTTPS is running
	StartHTTPToHTTPSRedirectServer()
}

// StartSelfSignedHTTPSServer starts an HTTPS server with a self-signed certificate.
func StartSelfSignedHTTPSServer() {
	certPath := "selfsigned.crt"
	keyPath := "selfsigned.key"

	// Generate the self-signed certificate if it doesn't exist
	if err := GenerateSelfSignedCert(certPath, keyPath); err != nil {
		log.Fatalf("Failed to generate self-signed certificate: %v", err)
	}

	httpsServer := &http.Server{
		Addr:    ":443",
		Handler: handlerWithMiddleware, // Use the middleware from the main package
	}

	log.Printf("Starting HTTPS server with self-signed certificate on %s", httpsServer.Addr)

	go func() {
		if err := httpsServer.ListenAndServeTLS(certPath, keyPath); err != nil {
			log.Fatalf("HTTPS server ListenAndServeTLS: %v", err)
		}
	}()

	// Start redirect HTTP server after HTTPS is running
	StartHTTPToHTTPSRedirectServer()
}

// StartHTTPToHTTPSRedirectServer keeps the HTTP server up and redirects to HTTPS
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
