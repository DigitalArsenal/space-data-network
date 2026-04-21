package tlsmgr

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Status struct {
	Mode                  string   `json:"mode"`
	ActiveCertificateType string   `json:"active_certificate_type"`
	FingerprintSHA256     string   `json:"fingerprint_sha256"`
	NotBefore             string   `json:"not_before,omitempty"`
	NotAfter              string   `json:"not_after,omitempty"`
	Hosts                 []string `json:"hosts,omitempty"`
	PeerID                string   `json:"peer_id,omitempty"`
	EncryptionPublicKey   string   `json:"encryption_public_key,omitempty"`
	ProofStatus           string   `json:"proof_status,omitempty"`
	BootstrapCertURL      string   `json:"bootstrap_cert_url,omitempty"`
	LastError             string   `json:"last_error,omitempty"`
}

func (m *Manager) Status() Status {
	status := Status{Mode: ModeDisabled}
	if m == nil {
		return status
	}

	m.mu.RLock()
	mode := m.mode
	staticCert := m.staticCert
	bootstrapCert := m.bootstrapCert
	hosts := append([]string(nil), m.hosts...)
	m.mu.RUnlock()

	status.Mode = mode

	var cert *tls.Certificate
	switch mode {
	case ModeStatic:
		status.ActiveCertificateType = "static"
		cert = staticCert
	case ModeManaged:
		status.ActiveCertificateType = "bootstrap"
		status.BootstrapCertURL = "/bootstrap.crt"
		cert = bootstrapCert
	default:
		return status
	}
	if cert == nil || cert.Leaf == nil {
		status.LastError = "certificate unavailable"
		return status
	}

	leaf := cert.Leaf
	hash := sha256.Sum256(leaf.Raw)
	status.FingerprintSHA256 = formatFingerprint(hash[:])
	status.NotBefore = leaf.NotBefore.UTC().Format(time.RFC3339)
	status.NotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
	status.Hosts = certificateHosts(leaf)
	if len(status.Hosts) == 0 && len(hosts) > 0 {
		status.Hosts = hosts
	}

	if mode != ModeManaged {
		return status
	}

	ext := bootstrapBindingExtension(leaf)
	if ext == nil {
		status.ProofStatus = "missing"
		return status
	}

	spkiDER, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		status.ProofStatus = "invalid"
		status.LastError = fmt.Sprintf("marshal spki: %v", err)
		return status
	}
	spkiHash := sha256.Sum256(spkiDER)
	binding, err := VerifyBootstrapBinding(ext.Value, spkiHash[:])
	if err != nil {
		status.ProofStatus = "invalid"
		status.LastError = err.Error()
		return status
	}

	status.PeerID = binding.PeerID
	status.EncryptionPublicKey = hex.EncodeToString(binding.EncryptionX25519PublicKey)
	status.ProofStatus = "verified"
	return status
}

func bootstrapBindingExtension(cert *x509.Certificate) *pkix.Extension {
	if cert == nil {
		return nil
	}
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(BootstrapBindingOID) {
			copyExt := ext
			return &copyExt
		}
	}
	return nil
}

func certificateHosts(cert *x509.Certificate) []string {
	if cert == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(cert.DNSNames)+len(cert.IPAddresses))
	hosts := make([]string, 0, len(cert.DNSNames)+len(cert.IPAddresses))
	for _, host := range cert.DNSNames {
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	for _, ip := range cert.IPAddresses {
		value := ip.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		hosts = append(hosts, value)
	}
	sort.Strings(hosts)
	return hosts
}

func formatFingerprint(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	hexValue := strings.ToUpper(hex.EncodeToString(raw))
	parts := make([]string, 0, len(hexValue)/2)
	for i := 0; i < len(hexValue); i += 2 {
		parts = append(parts, hexValue[i:i+2])
	}
	return strings.Join(parts, ":")
}
