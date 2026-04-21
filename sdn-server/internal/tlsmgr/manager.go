package tlsmgr

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"golang.org/x/crypto/acme/autocert"
)

const (
	ModeDisabled = "disabled"
	ModeStatic   = "static"
	ModeManaged  = "managed"
)

type Manager struct {
	mu            sync.RWMutex
	mode          string
	staticCert    *tls.Certificate
	staticCertFile string
	staticKeyFile  string
	cacheDir      string
	bootstrapCert *tls.Certificate
	hosts         []string
	acmeManager   *autocert.Manager
}

func New(cfg config.AdminConfig) (*Manager, error) {
	mode := cfg.EffectiveTLSMode()
	switch mode {
	case ModeDisabled, ModeStatic, ModeManaged:
		m := &Manager{
			mode:           mode,
			staticCertFile: strings.TrimSpace(cfg.TLSCertFile),
			staticKeyFile:  strings.TrimSpace(cfg.TLSKeyFile),
			cacheDir:       strings.TrimSpace(cfg.TLSCacheDir),
		}
		if mode == ModeStatic {
			if m.staticCertFile == "" || m.staticKeyFile == "" {
				return nil, fmt.Errorf("static tls mode requires tls_cert_file and tls_key_file")
			}
			cert, err := loadCertificatePair(m.staticCertFile, m.staticKeyFile)
			if err != nil {
				return nil, fmt.Errorf("load static tls certificate: %w", err)
			}
			m.staticCert = cert
		}
		if mode == ModeManaged && m.cacheDir == "" {
			return nil, fmt.Errorf("managed tls mode requires tls_cache_dir")
		}
		if mode == ModeManaged {
			if err := m.UpdateHosts(cfg.TLSHosts); err != nil {
				return nil, err
			}
		}
		return m, nil
	default:
		return nil, fmt.Errorf("unsupported tls mode %q", mode)
	}
}

func (m *Manager) Mode() string {
	if m == nil {
		return ModeDisabled
	}
	return m.mode
}

func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return m.GetCertificate(hello)
		},
	}
}

func (m *Manager) UsesNativeTLS() bool {
	return m != nil && m.mode != ModeDisabled
}

func (m *Manager) ConfiguredHosts() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.hosts...)
}

func (m *Manager) ConfigureBootstrap(identity BootstrapIdentityInput) error {
	if m == nil || m.mode != ModeManaged {
		return nil
	}

	cacheDir := m.cacheDir
	if cacheDir == "" {
		return fmt.Errorf("bootstrap tls cache dir is empty")
	}
	if !filepath.IsAbs(cacheDir) {
		abs, err := filepath.Abs(cacheDir)
		if err != nil {
			return fmt.Errorf("resolve tls cache dir: %w", err)
		}
		cacheDir = abs
	}

	cert, err := m.loadOrCreateBootstrapCert(cacheDir, identity)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cacheDir = cacheDir
	m.bootstrapCert = cert
	return nil
}

func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if m == nil {
		return nil, fmt.Errorf("tls manager is nil")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	switch m.mode {
	case ModeStatic:
		if m.staticCert == nil {
			return nil, fmt.Errorf("static tls certificate not loaded")
		}
		return m.staticCert, nil
	case ModeManaged:
		if m.acmeManager != nil {
			if hello != nil && strings.TrimSpace(hello.ServerName) != "" {
				if cert, err := m.acmeManager.GetCertificate(hello); err == nil {
					return cert, nil
				}
			}
		}
		if m.bootstrapCert == nil {
			return nil, fmt.Errorf("bootstrap tls certificate not configured")
		}
		return m.bootstrapCert, nil
	default:
		return nil, fmt.Errorf("tls disabled")
	}
}

func (m *Manager) BootstrapCertPEM() ([]byte, error) {
	if m == nil || m.mode != ModeManaged {
		return nil, fmt.Errorf("bootstrap certificate unavailable")
	}
	m.mu.RLock()
	cacheDir := m.cacheDir
	m.mu.RUnlock()
	if cacheDir == "" {
		return nil, fmt.Errorf("bootstrap certificate unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(cacheDir, bootstrapCertFileName))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (m *Manager) UpdateHosts(hosts []string) error {
	if m == nil || m.mode != ModeManaged {
		return nil
	}
	normalized := make([]string, 0, len(hosts))
	for _, raw := range hosts {
		host := strings.TrimSpace(strings.ToLower(raw))
		if host == "" {
			continue
		}
		if err := ValidateManagedHost(host); err != nil {
			return err
		}
		if !slices.Contains(normalized, host) {
			normalized = append(normalized, host)
		}
	}

	var acmeMgr *autocert.Manager
	if len(normalized) > 0 {
		acmeMgr = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(filepath.Join(m.cacheDir, "acme-cache")),
			HostPolicy: autocert.HostWhitelist(normalized...),
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.hosts = normalized
	m.acmeManager = acmeMgr
	return nil
}

func ValidateManagedHost(host string) error {
	host = strings.TrimSpace(strings.ToLower(host))
	switch {
	case host == "":
		return fmt.Errorf("hostname is required")
	case host == "localhost":
		return fmt.Errorf("localhost is not allowed for managed certificates")
	case net.ParseIP(host) != nil:
		return fmt.Errorf("ip literals are not allowed in hostname mode")
	default:
		return nil
	}
}
