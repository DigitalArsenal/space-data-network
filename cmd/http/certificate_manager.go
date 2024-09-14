package httpserver

import (
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/DigitalArsenal/space-data-network/serverconfig"
	"golang.org/x/crypto/acme/autocert"
)

var autocertManager *autocert.Manager

// InitializeAutocertManager initializes the autocert manager and returns it.
func InitializeAutocertManager(domain string) *autocert.Manager {
	certDir := filepath.Join(serverconfig.Conf.Datastore.Directory, "cert")

	// Ensure the certificate directory exists
	err := os.MkdirAll(certDir, 0700)
	if err != nil {
		log.Fatalf("Failed to create certificate directory: %v", err)
	}

	autocertManager = &autocert.Manager{
		Cache:      autocert.DirCache(certDir),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
	}

	return autocertManager
}

// StartAutocertChallengeServer starts an HTTP server for handling autocert challenges.
func StartAutocertChallengeServer() {
	httpServer := &http.Server{
		Addr:    ":80",
		Handler: autocertManager.HTTPHandler(nil),
	}

	log.Printf("Starting HTTP server for autocert challenges on %s", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTP server ListenAndServe: %v", err)
	}
}

// GetTLSConfig returns the TLS configuration for HTTPS server using autocert certificates.
func GetTLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: autocertManager.GetCertificate,
	}
}
