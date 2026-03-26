// Package wasmlicenseplugin implements the OrbPro key broker as an SDN plugin
// backed by a standalone C++ WebAssembly artifact hosted through WasmEdge.
// The plugin handles P-256 ECDH key exchange for OrbPro's protection runtime
// with the crypto remaining inside the guest artifact.
//
// Key exchange happens over encrypted libp2p streams (not HTTP), following a
// Widevine/Signal-style model. The server's P-256 public key is published to
// the DHT so clients can discover the key broker by CID.
package wasmlicenseplugin

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/host"

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
	bridge   *wasiplugin.StreamBridge
	host     host.Host
	wasmPath string
	wasmData []byte

	// Background goroutine lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New returns an unstarted plugin that will load the WASM module from wasmPath.
func New(wasmPath string) *Plugin {
	return &Plugin{wasmPath: wasmPath}
}

// NewFromBytes returns an unstarted plugin that uses inline WASM bytes.
func NewFromBytes(wasmBytes []byte) *Plugin {
	data := make([]byte, len(wasmBytes))
	copy(data, wasmBytes)
	return &Plugin{wasmData: data}
}

// ID returns the plugin identifier.
func (p *Plugin) ID() string { return ID }

// Start loads the standalone artifact, derives the P-256 node key, configures
// the guest through the canonical invoke ABI, then registers libp2p stream
// handlers and publishes the public key to the DHT.
//
// Config comes from environment variables:
//
//   - ORBPRO_KEYSERVER_MAX_SKEW_MS (optional)
//   - ORBPRO_KEYSERVER_ACTIVE_KEY_VERSION (optional)
func (p *Plugin) Start(ctx context.Context, runtime plugins.RuntimeContext) error {
	if len(runtime.NodeEncryptionKey) == 0 {
		log.Warn("Node encryption key unavailable — key broker plugin disabled")
		return nil
	}

	privateKey, err := deriveP256PrivateKey(runtime.NodeEncryptionKey)
	if err != nil {
		return fmt.Errorf("failed to derive P-256 key from node identity: %w", err)
	}

	maxSkewMs := envInt64("ORBPRO_KEYSERVER_MAX_SKEW_MS", 0)
	activeKeyVersion := envUint32("ORBPRO_KEYSERVER_ACTIVE_KEY_VERSION", 1)

	if len(p.wasmData) == 0 && strings.TrimSpace(p.wasmPath) == "" {
		return fmt.Errorf("no WASM source configured")
	}
	wasmBytes := p.wasmData
	if len(wasmBytes) == 0 {
		var err error
		wasmBytes, err = os.ReadFile(p.wasmPath)
		if err != nil {
			return fmt.Errorf("failed to read WASM file %s: %w", p.wasmPath, err)
		}
	}
	if len(wasmBytes) == 0 {
		return fmt.Errorf("plugin module is empty")
	}

	rt, err := wasiplugin.New(ctx, wasmBytes)
	if err != nil {
		return fmt.Errorf("failed to create WASI runtime: %w", err)
	}

	state, err := rt.ConfigureRuntime(
		ctx,
		wasiplugin.RuntimeConfiguration{
			ServerPrivateKeyHex: hex.EncodeToString(privateKey),
			MaxSkewMs:           maxSkewMs,
			ActiveKeyVersion:    activeKeyVersion,
		},
	)
	if err != nil {
		rt.Close(ctx)
		return fmt.Errorf("configure_runtime failed: %w", err)
	}

	handler := wasiplugin.NewHandler(rt)
	bridge := wasiplugin.NewStreamBridge(rt)

	p.mu.Lock()
	p.runtime = rt
	p.handler = handler
	p.bridge = bridge
	p.host = runtime.Host
	p.mu.Unlock()

	// Register libp2p stream handlers for key exchange over p2p transport.
	// The key exchange happens entirely over encrypted libp2p streams,
	// not HTTP — following a Widevine/Signal-style model.
	if runtime.Host != nil {
		runtime.Host.SetStreamHandler(wasiplugin.PublicKeyProtocolID, bridge.HandlePublicKeyStream)
		runtime.Host.SetStreamHandler(wasiplugin.ChallengeProtocolID, bridge.HandleChallengeStream)
		runtime.Host.SetStreamHandler(wasiplugin.KeyBrokerProtocolID, bridge.HandleKeyBrokerStream)
		log.Infof(
			"Registered libp2p stream handlers: %s, %s, %s",
			wasiplugin.PublicKeyProtocolID,
			wasiplugin.ChallengeProtocolID,
			wasiplugin.KeyBrokerProtocolID,
		)
	}

	// Publish the server's public key CID to the DHT in a background goroutine.
	// This re-announces periodically so new peers can discover the key broker.
	if runtime.DHT != nil {
		p.ctx, p.cancel = context.WithCancel(ctx)
		p.wg.Add(1)
		go p.announceLoop(runtime)
	}

	log.Infof("OrbPro key broker plugin started (transport: libp2p)")
	log.Infof("OrbPro key broker active key version: %d", state.ActiveKeyVersion)
	return nil
}

// announceLoop periodically publishes the public key CID to the DHT.
func (p *Plugin) announceLoop(runtime plugins.RuntimeContext) {
	defer p.wg.Done()

	p.mu.RLock()
	bridge := p.bridge
	p.mu.RUnlock()

	if bridge == nil {
		return
	}

	// Initial announcement
	if err := bridge.AnnouncePublicKey(p.ctx, runtime.DHT); err != nil {
		log.Warnf("Initial DHT announcement failed: %v", err)
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			if err := bridge.AnnouncePublicKey(p.ctx, runtime.DHT); err != nil {
				log.Debugf("DHT re-announcement failed: %v", err)
			}
		}
	}
}

// RegisterRoutes mounts the OrbPro key broker HTTP endpoints.
// The key broker protocol is served only over libp2p streams.
func (p *Plugin) RegisterRoutes(mux *http.ServeMux) {
	p.mu.RLock()
	h := p.handler
	p.mu.RUnlock()

	if h == nil {
		return
	}

	// Admin UI (behind admin auth).
	mux.HandleFunc("/orbpro-key-broker/v1/orbpro/ui", h.HandleUI)
}

// Version returns the plugin version string.
func (p *Plugin) Version() string { return "2.0.0" }

// Description returns a short description of the plugin.
func (p *Plugin) Description() string {
	return "P-256 ECDH key broker for OrbPro protection runtime (libp2p transport)"
}

// UIDescriptor returns the plugin's web UI metadata.
func (p *Plugin) UIDescriptor() plugins.UIDescriptor {
	return plugins.UIDescriptor{
		Title:       "OrbPro Key Broker",
		Description: "P-256 ECDH key exchange over libp2p (Widevine/Signal model)",
		Icon:        "\U0001F511",
		Color:       "#fef3c7",
		TextColor:   "#92400e",
		URL:         "/orbpro-key-broker/v1/orbpro/ui",
	}
}

// Close shuts down the background announce loop, removes libp2p stream
// handlers, and releases the WASI runtime.
func (p *Plugin) Close() error {
	// Stop background goroutine
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()

	p.mu.Lock()
	rt := p.runtime
	h := p.host
	p.runtime = nil
	p.handler = nil
	p.bridge = nil
	p.host = nil
	p.mu.Unlock()

	// Remove stream handlers
	if h != nil {
		h.RemoveStreamHandler(wasiplugin.PublicKeyProtocolID)
		h.RemoveStreamHandler(wasiplugin.ChallengeProtocolID)
		h.RemoveStreamHandler(wasiplugin.KeyBrokerProtocolID)
	}

	if rt != nil {
		return rt.Close(context.Background())
	}
	return nil
}

func deriveP256PrivateKey(seed []byte) ([]byte, error) {
	if len(seed) == 0 {
		return nil, fmt.Errorf("empty node encryption seed")
	}

	contextPrefix := []byte("orbpro:key-broker:p256:v1")
	for counter := 0; counter < 64; counter++ {
		hash := sha256.New()
		hash.Write(contextPrefix)
		hash.Write(seed)
		hash.Write([]byte{byte(counter)})
		candidate := hash.Sum(nil)
		if _, err := ecdh.P256().NewPrivateKey(candidate); err == nil {
			return candidate, nil
		}
	}

	return nil, fmt.Errorf("unable to derive valid P-256 private key from node seed")
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

func envUint32(key string, defaultVal uint32) uint32 {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return defaultVal
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil || v == 0 {
		return defaultVal
	}
	return uint32(v)
}
