package tlsmgr

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	bootstrapCertFileName = "bootstrap-cert.pem"
	bootstrapKeyFileName  = "bootstrap-key.pem"
)

type BootstrapIdentityInput struct {
	PeerID                     string
	EncryptionPath             string
	EncryptionX25519PublicKey  []byte
	EncryptionProofEd25519Seed []byte
	Hosts                      []string
}

func (m *Manager) loadOrCreateBootstrapCert(dir string, identity BootstrapIdentityInput) (*tls.Certificate, error) {
	certPath := filepath.Join(dir, bootstrapCertFileName)
	keyPath := filepath.Join(dir, bootstrapKeyFileName)

	if cert, err := loadCertificatePair(certPath, keyPath); err == nil {
		return cert, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create tls cache dir: %w", err)
	}

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate bootstrap private key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate bootstrap serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:         "Space Data Network Node",
			Organization:       []string{"Space Data Network"},
			OrganizationalUnit: []string{"Bootstrap TLS"},
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(5 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
		},
	}
	addBootstrapSANs(template, identity.Hosts)

	spkiDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal bootstrap spki: %w", err)
	}
	spkiHash := sha256.Sum256(spkiDER)
	bindingExt, err := EncodeBootstrapBinding(BootstrapBindingInput{
		PeerID:                    identity.PeerID,
		EncryptionPath:            identity.EncryptionPath,
		EncryptionX25519PublicKey: identity.EncryptionX25519PublicKey,
		ProofEd25519Seed:          identity.EncryptionProofEd25519Seed,
		TLSSPKISHA256:             spkiHash[:],
	})
	if err != nil {
		return nil, fmt.Errorf("encode bootstrap binding: %w", err)
	}
	template.ExtraExtensions = []pkix.Extension{bindingExt}

	derCert, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, fmt.Errorf("create bootstrap certificate: %w", err)
	}
	derKey, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("marshal bootstrap private key: %w", err)
	}

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derCert}), 0o644); err != nil {
		return nil, fmt.Errorf("write bootstrap certificate: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: derKey}), 0o600); err != nil {
		return nil, fmt.Errorf("write bootstrap private key: %w", err)
	}

	return loadCertificatePair(certPath, keyPath)
}

func loadCertificatePair(certPath, keyPath string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse leaf certificate: %w", err)
	}
	cert.Leaf = leaf
	return &cert, nil
}

func addBootstrapSANs(template *x509.Certificate, hosts []string) {
	if template == nil {
		return
	}
	for _, raw := range hosts {
		host := strings.TrimSpace(raw)
		if host == "" || host == "0.0.0.0" || host == "::" {
			continue
		}
		if parsed := net.ParseIP(host); parsed != nil {
			if !containsIP(template.IPAddresses, parsed) {
				template.IPAddresses = append(template.IPAddresses, parsed)
			}
			continue
		}
		if !containsString(template.DNSNames, host) {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func containsIP(items []net.IP, want net.IP) bool {
	for _, item := range items {
		if item.Equal(want) {
			return true
		}
	}
	return false
}
