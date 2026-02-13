// Package wasmlicenseplugin implements the OrbPro key broker as an SDN plugin
// backed by a C++ WASI module. The plugin handles P-256 ECDH key exchange for
// OrbPro's protection runtime, running the crypto entirely inside WASM/WASI
// via the Wazero runtime.
package wasmlicenseplugin

import (
	"context"
	"crypto/ecdh"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/wasiplugin"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

var log = logging.Logger("wasm-license")

// ID is the canonical plugin identifier.
const ID = "orbpro-key-broker"

// Plugin wraps the WASI key broker module into the SDN plugin contract.
type Plugin struct {
	mu       sync.RWMutex
	runtime  *wasiplugin.Runtime
	handler  *wasiplugin.Handler
	wasmPath string
}

// New returns an unstarted plugin that will load the WASM module from wasmPath.
func New(wasmPath string) *Plugin {
	return &Plugin{wasmPath: wasmPath}
}

// ID returns the plugin identifier.
func (p *Plugin) ID() string { return ID }

// Start loads the WASM module, derives the P-256 public key, packs the binary
// config blob, and calls plugin_init. Config comes from environment variables:
//
//   - ORBPRO_SERVER_PRIVATE_KEY_HEX  — 32-byte P-256 private key (64 hex chars)
//   - DERIVATION_SECRET              — shared secret for KDF program
//   - ORBPRO_KEYSERVER_ALLOWED_DOMAINS — comma-separated allowed origins
//   - ORBPRO_KEYSERVER_EPOCH_PERIOD_MS (optional)
//   - ORBPRO_KEYSERVER_MAX_SKEW_MS    (optional)
//   - ORBPRO_KEYSERVER_LEASE_MS       (optional)
func (p *Plugin) Start(ctx context.Context, runtime plugins.RuntimeContext) error {
	privateKeyHex := strings.TrimSpace(os.Getenv("ORBPRO_SERVER_PRIVATE_KEY_HEX"))
	if privateKeyHex == "" {
		log.Warn("ORBPRO_SERVER_PRIVATE_KEY_HEX not set — key broker plugin disabled")
		return nil
	}

	privateKey, err := hex.DecodeString(privateKeyHex)
	if err != nil || len(privateKey) != 32 {
		return fmt.Errorf("invalid ORBPRO_SERVER_PRIVATE_KEY_HEX: must be 64 hex chars (32 bytes)")
	}

	derivationSecret := os.Getenv("DERIVATION_SECRET")
	if derivationSecret == "" {
		return fmt.Errorf("DERIVATION_SECRET environment variable is required")
	}

	allowedDomains := strings.TrimSpace(os.Getenv("ORBPRO_KEYSERVER_ALLOWED_DOMAINS"))
	if allowedDomains == "" {
		return fmt.Errorf("ORBPRO_KEYSERVER_ALLOWED_DOMAINS environment variable is required")
	}

	epochPeriodMs := envInt64("ORBPRO_KEYSERVER_EPOCH_PERIOD_MS", 0)
	maxSkewMs := envInt64("ORBPRO_KEYSERVER_MAX_SKEW_MS", 0)
	leaseMs := envInt64("ORBPRO_KEYSERVER_LEASE_MS", 0)

	// Derive the uncompressed P-256 public key (65 bytes: 0x04 + x + y).
	pubKey, err := p256PublicKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to compute P-256 public key: %w", err)
	}

	wasmBytes, err := os.ReadFile(p.wasmPath)
	if err != nil {
		return fmt.Errorf("failed to read WASM file %s: %w", p.wasmPath, err)
	}

	rt, err := wasiplugin.New(ctx, wasmBytes)
	if err != nil {
		return fmt.Errorf("failed to create WASI runtime: %w", err)
	}

	// Pack binary config for plugin_init:
	//   privateKey(32) + publicKey(65) + secretLen(4 LE) + secret(N)
	//   + domainsCsv(NUL-terminated) + epochPeriodMs(8 LE) + maxSkewMs(8 LE) + leaseMs(8 LE)
	secretBytes := []byte(derivationSecret)
	domainsBytes := append([]byte(allowedDomains), 0)

	configSize := 32 + 65 + 4 + len(secretBytes) + len(domainsBytes) + 24
	config := make([]byte, configSize)
	off := 0

	copy(config[off:], privateKey)
	off += 32
	copy(config[off:], pubKey)
	off += 65
	binary.LittleEndian.PutUint32(config[off:], uint32(len(secretBytes)))
	off += 4
	copy(config[off:], secretBytes)
	off += len(secretBytes)
	copy(config[off:], domainsBytes)
	off += len(domainsBytes)
	binary.LittleEndian.PutUint64(config[off:], uint64(epochPeriodMs))
	off += 8
	binary.LittleEndian.PutUint64(config[off:], uint64(maxSkewMs))
	off += 8
	binary.LittleEndian.PutUint64(config[off:], uint64(leaseMs))

	if err := rt.Init(ctx, config); err != nil {
		rt.Close(ctx)
		return fmt.Errorf("plugin_init failed: %w", err)
	}

	handler := wasiplugin.NewHandler(rt)

	p.mu.Lock()
	p.runtime = rt
	p.handler = handler
	p.mu.Unlock()

	log.Infof("OrbPro key broker plugin started (domains: %s)", allowedDomains)
	return nil
}

// RegisterRoutes mounts the OrbPro key broker HTTP endpoints.
func (p *Plugin) RegisterRoutes(mux *http.ServeMux) {
	p.mu.RLock()
	h := p.handler
	p.mu.RUnlock()

	if h == nil {
		return
	}

	mux.HandleFunc("/orbpro-key-broker/v1/orbpro/public-key", h.HandlePublicKey)
	mux.HandleFunc("/orbpro-key-broker/v1/orbpro/key", h.HandleKeyExchange)
	mux.HandleFunc("/orbpro-key-broker/v1/orbpro/ui", h.HandleUI)
}

// Version returns the plugin version string.
func (p *Plugin) Version() string { return "1.0.0" }

// Description returns a short description of the plugin.
func (p *Plugin) Description() string {
	return "P-256 ECDH key broker for OrbPro protection runtime"
}

// UIDescriptor returns the plugin's web UI metadata.
func (p *Plugin) UIDescriptor() plugins.UIDescriptor {
	return plugins.UIDescriptor{
		Title:       "OrbPro Key Broker",
		Description: "P-256 ECDH key exchange service for OrbPro content protection",
		Icon:        "\U0001F511",
		Color:       "#fef3c7",
		TextColor:   "#92400e",
		URL:         "/orbpro-key-broker/v1/orbpro/ui",
	}
}

// Close shuts down the WASI runtime.
func (p *Plugin) Close() error {
	p.mu.Lock()
	rt := p.runtime
	p.runtime = nil
	p.handler = nil
	p.mu.Unlock()

	if rt != nil {
		return rt.Close(context.Background())
	}
	return nil
}

// p256PublicKey derives the uncompressed P-256 public key (65 bytes) from a
// 32-byte private key scalar.
func p256PublicKey(privateKeyBytes []byte) ([]byte, error) {
	priv, err := ecdh.P256().NewPrivateKey(privateKeyBytes)
	if err != nil {
		return nil, err
	}
	return priv.PublicKey().Bytes(), nil
}

func envInt64(key string, defaultVal int64) int64 {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return defaultVal
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return defaultVal
	}
	return v
}
