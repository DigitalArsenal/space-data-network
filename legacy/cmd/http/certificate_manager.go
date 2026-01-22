// certificate_manager.go

package httpserver

import (
	"crypto/tls"
	"log"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// initializeAutocertManager initializes the autocert.Manager for the specified domain.
func initializeAutocertManager(domain string) error {
	log.Printf("Initializing autocert.Manager for domain: %s", domain)

	autocertManager = &autocert.Manager{
		Cache:      autocert.DirCache(certDirAutocert), // Folder for storing certificates
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
	}

	log.Println("autocert.Manager initialized successfully.")
	return nil
}

// GetTLSConfig returns the TLS configuration for HTTPS server using autocert certificates.
func GetTLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: autocertManager.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto}, // Include acme.ALPNProto
	}
}
