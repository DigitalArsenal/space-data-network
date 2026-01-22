// http_server.go

package httpserver

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
	"github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/cenkalti/backoff/v4"
	"golang.org/x/crypto/acme/autocert"
)

// Node represents the node structure.
type Node = nodepkg.Node

var (
	currentNode *Node
	httpServer  *http.Server
	httpsServer *http.Server

	// DDoS Protection Variables
	requestCount  = make(map[string][]time.Time)
	requestLock   sync.Mutex
	ipBlacklist   = make(map[string]bool)
	blacklistLock sync.Mutex
	requestLimit  = 10000

	verifyDomainBackoffs = make(map[string]*backoff.ExponentialBackOff)
	verifyDomainCounts   = make(map[string]int)
	verifyDomainLock     sync.Mutex

	serverUpgradedToHTTPS bool
	serverUpgradeLock     sync.Mutex

	handlerWithMiddleware http.Handler

	// Define the certificate directories
	certDirSelfSigned string
	certDirAutocert   string

	// Paths for self-signed certificates
	selfSignedCertPath string
	selfSignedKeyPath  string

	// autocertManager is the global autocert.Manager instance
	autocertManager *autocert.Manager
	mux             = http.NewServeMux()

	// Domains that have been verified and are using autocert
	verifiedDomains = make(map[string]bool)
)

// StartHTTPServer initializes and starts the HTTP and HTTPS servers.
func StartHTTPServer(node *Node) {
	currentNode = node

	// Define the certificate directories
	certDirSelfSigned = filepath.Join(serverconfig.Conf.Datastore.Directory, "certificates", "selfsigned")
	certDirAutocert = filepath.Join(serverconfig.Conf.Datastore.Directory, "certificates", "autocert")

	// Define paths for self-signed certificates
	selfSignedCertPath = filepath.Join(certDirSelfSigned, "server.crt")
	selfSignedKeyPath = filepath.Join(certDirSelfSigned, "server.key")

	log.Printf("Self-signed Certificate directory set to: %s", certDirSelfSigned)
	log.Printf("Autocert Certificate directory set to: %s", certDirAutocert)

	// Register application handlers
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/verify-domain", verifyDomainHandler)

	// Wrap the mux with CORS and rate limit middleware
	handlerWithMiddleware = corsMiddleware(rateLimitMiddleware(mux))

	// Ensure the certificate directories exist
	err := os.MkdirAll(certDirSelfSigned, 0700)
	if err != nil {
		log.Fatalf("Failed to create self-signed certificate directory: %v", err)
	}

	err = os.MkdirAll(certDirAutocert, 0700)
	if err != nil {
		log.Fatalf("Failed to create autocert certificate directory: %v", err)
	}

	// Generate self-signed certificates if they do not exist
	err = GenerateSelfSignedCert(selfSignedCertPath, selfSignedKeyPath)
	if err != nil {
		log.Fatalf("Failed to generate self-signed certificates: %v", err)
	}
	log.Println("Self-signed certificates are ready.")

	// Check if autocert certificates already exist
	autocertCertExists, err := checkAndInitAutoCert()
	if err != nil {
		log.Printf("Error checking autocert certificates: %v", err)
		autocertCertExists = false
	}

	if autocertCertExists {
		// Start HTTPS server with autocert
		log.Println("Autocert certificates found. Starting HTTPS server with Let's Encrypt certificates.")
		serverUpgradedToHTTPS = true
		startHTTPSWithAutocert()
	} else {
		// Generate self-signed certificates if they do not exist
		err = GenerateSelfSignedCert(selfSignedCertPath, selfSignedKeyPath)
		if err != nil {
			log.Fatalf("Failed to generate self-signed certificates: %v", err)
		}
		log.Println("Self-signed certificates are ready.")

		// Start HTTPS server with self-signed certificates
		startHTTPSServerWithSelfSigned()
	}

	// Start HTTP server on port 80 with autocert's HTTPHandler for ACME challenges and redirect other requests to HTTPS
	startHTTPServer()
}

// startHTTPServer starts the HTTP server on port 80.
// It uses autocert's HTTPHandler to handle ACME challenges and redirects other requests to HTTPS.
func startHTTPServer() {
	// Create a new ServeMux for HTTP server
	httpMux := http.NewServeMux()

	// Handle ACME challenges if autocertManager is initialized
	if autocertManager != nil {
		httpMux.HandleFunc("/.well-known/acme-challenge/", autocertManager.HTTPHandler(nil).ServeHTTP)
	}

	// Redirect all other HTTP requests to HTTPS
	httpMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})

	httpServer = &http.Server{
		Addr:    ":80",
		Handler: httpMux,
	}

	go func() {
		log.Printf("Starting HTTP server on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start HTTP server: %v", err)
		}
	}()
}

// startHTTPSServerWithSelfSigned starts the HTTPS server using self-signed certificates.
func startHTTPSServerWithSelfSigned() {
	httpsServer = &http.Server{
		Addr:    ":443",
		Handler: handlerWithMiddleware,
	}

	go func() {
		log.Printf("Starting HTTPS server with self-signed certificates at %s and %s", selfSignedCertPath, selfSignedKeyPath)
		if err := httpsServer.ListenAndServeTLS(selfSignedCertPath, selfSignedKeyPath); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTPS server ListenAndServeTLS error: %v", err)
		}
	}()
}

// UpgradeHTTPSWithAutocert upgrades the HTTPS server to use autocert-managed certificates.
func UpgradeHTTPSWithAutocert(domain string) {
	serverUpgradeLock.Lock()
	defer serverUpgradeLock.Unlock()

	if serverUpgradedToHTTPS {
		log.Println("HTTPS server has already been upgraded to use autocert.")
		return
	}

	log.Printf("Upgrading HTTPS server to use Let's Encrypt certificates for domain: %s", domain)

	// Initialize autocert manager
	err := initializeAutocertManager(domain)
	if err != nil {
		log.Printf("Failed to initialize autocert manager for domain '%s': %v", domain, err)
		return
	}

	// Shutdown the existing HTTPS server with self-signed certificates
	err = shutdownHTTPSServer()
	if err != nil {
		log.Printf("Failed to shutdown existing HTTPS server: %v", err)
		return
	}

	// Start HTTPS server with autocert
	startHTTPSWithAutocert()

	serverUpgradedToHTTPS = true
	log.Println("HTTPS server successfully upgraded to use Let's Encrypt certificates.")
}

// startHTTPSWithAutocert starts the HTTPS server using autocert to obtain certificates.
func startHTTPSWithAutocert() {
	tlsConfig := GetTLSConfig()

	httpsServer = &http.Server{
		Addr:      ":443",
		Handler:   handlerWithMiddleware,
		TLSConfig: tlsConfig,
	}

	go func() {
		log.Println("Starting HTTPS server with Let's Encrypt certificates via autocert.")
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTPS server ListenAndServeTLS with autocert error: %v", err)
		}
	}()
}

// shutdownHTTPSServer gracefully shuts down the HTTPS server.
func shutdownHTTPSServer() error {
	if httpsServer == nil {
		log.Println("HTTPS server is not running. No need to shut down.")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	log.Println("Shutting down existing HTTPS server...")
	err := httpsServer.Shutdown(ctx)
	if err != nil {
		log.Printf("Error shutting down HTTPS server: %v", err)
		return err
	}

	log.Println("HTTPS server shut down gracefully.")
	return nil
}

// StopHTTPServer gracefully shuts down the HTTP and HTTPS servers.
func StopHTTPServer() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if httpServer != nil {
		log.Println("Shutting down HTTP server...")
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP server Shutdown error: %v", err)
		} else {
			log.Println("HTTP server shut down gracefully.")
		}
	}

	if httpsServer != nil {
		log.Println("Shutting down HTTPS server...")
		if err := httpsServer.Shutdown(ctx); err != nil {
			log.Printf("HTTPS server Shutdown error: %v", err)
		} else {
			log.Println("HTTPS server shut down gracefully.")
		}
	}

	log.Println("All servers have been shut down.")
}

// checkAndInitAutoCert checks if valid autocert certificates exist.
func checkAndInitAutoCert() (bool, error) {
	// List all files in the autocert directory
	files, err := os.ReadDir(certDirAutocert)
	if err != nil {
		return false, err
	}

	// Check for any certificate files
	for _, file := range files {
		var fileName = file.Name()
		if !file.IsDir() && fileName != "acme_account+key" {
			log.Printf("Found autocert certificate file: %s", fileName)
			initializeAutocertManager(fileName)
			return true, nil
		}
	}

	// No certificate files found
	return false, nil
}
