// Package main provides the registry builder - a DHT monitor that discovers
// edge relays and rebuilds the encrypted WASM registry.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/multiformats/go-multihash"
	"github.com/spf13/cobra"
)

var log = logging.Logger("registry-builder")

// RelayInfo contains information about an edge relay.
type RelayInfo struct {
	PeerID    string `json:"peerId"`
	Multiaddr string `json:"multiaddr"`
	LastSeen  int64  `json:"lastSeen"`
	Region    string `json:"region,omitempty"`
}

// RegistryBuilder monitors DHT for edge relay announcements.
type RegistryBuilder struct {
	host         host.Host
	dht          *dht.IpfsDHT
	knownRelays  map[string]*RelayInfo
	cdnEndpoints []string
	buildScript  string
	outputDir    string
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
}

var rootCmd = &cobra.Command{
	Use:   "registry-builder",
	Short: "SDN Registry Builder - Monitors DHT and rebuilds edge relay registry",
	Long: `registry-builder monitors the DHT for edge relay announcements and
automatically rebuilds the encrypted WASM registry when changes are detected.

The registry is then deployed to configured CDN endpoints.`,
	RunE: runBuilder,
}

var (
	bootstrapPeers []string
	buildScript    string
	outputDir      string
	cdnEndpoints   []string
	pollInterval   time.Duration
	staleTimeout   time.Duration
	debug          bool
)

func init() {
	rootCmd.Flags().StringArrayVarP(&bootstrapPeers, "bootstrap", "b", []string{}, "bootstrap peer addresses")
	rootCmd.Flags().StringVarP(&buildScript, "build-script", "s", "./scripts/build-edge-registry.ts", "path to build script")
	rootCmd.Flags().StringVarP(&outputDir, "output", "o", "./sdn-js/wasm", "output directory for WASM")
	rootCmd.Flags().StringArrayVarP(&cdnEndpoints, "cdn", "c", []string{}, "CDN endpoints for deployment")
	rootCmd.Flags().DurationVarP(&pollInterval, "poll", "p", 5*time.Minute, "DHT poll interval")
	rootCmd.Flags().DurationVarP(&staleTimeout, "stale", "t", 1*time.Hour, "stale relay timeout")
	rootCmd.Flags().BoolVarP(&debug, "debug", "d", false, "enable debug logging")
}

func main() {
	if debug {
		logging.SetAllLoggers(logging.LevelDebug)
	} else {
		logging.SetAllLoggers(logging.LevelInfo)
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runBuilder(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())

	builder, err := NewRegistryBuilder(ctx, bootstrapPeers, buildScript, outputDir, cdnEndpoints)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create registry builder: %w", err)
	}
	builder.cancel = cancel

	log.Info("Starting Registry Builder...")
	log.Infof("Peer ID: %s", builder.host.ID())
	log.Infof("Poll interval: %s", pollInterval)
	log.Infof("Stale timeout: %s", staleTimeout)

	// Start monitoring
	go builder.MonitorDHT(ctx, pollInterval, staleTimeout)

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Info("Shutting down...")
	cancel()
	return builder.Close()
}

// NewRegistryBuilder creates a new registry builder.
func NewRegistryBuilder(ctx context.Context, bootstraps []string, buildScript, outputDir string, cdnEndpoints []string) (*RegistryBuilder, error) {
	// Generate identity
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	// Create libp2p host
	var dhtRouting *dht.IpfsDHT
	h, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			var err error
			dhtRouting, err = dht.New(ctx, h,
				dht.Mode(dht.ModeClient),
				dht.ProtocolPrefix("/spacedatanetwork"),
			)
			return dhtRouting, err
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create host: %w", err)
	}

	rb := &RegistryBuilder{
		host:         h,
		dht:          dhtRouting,
		knownRelays:  make(map[string]*RelayInfo),
		cdnEndpoints: cdnEndpoints,
		buildScript:  buildScript,
		outputDir:    outputDir,
		ctx:          ctx,
	}

	// Bootstrap DHT
	if err := dhtRouting.Bootstrap(ctx); err != nil {
		log.Warnf("DHT bootstrap warning: %v", err)
	}

	// Connect to bootstrap peers
	for _, addr := range bootstraps {
		peerInfo, err := peer.AddrInfoFromString(addr)
		if err != nil {
			log.Warnf("Invalid bootstrap address %s: %v", addr, err)
			continue
		}
		go func(pi *peer.AddrInfo) {
			if err := h.Connect(ctx, *pi); err != nil {
				log.Warnf("Failed to connect to bootstrap peer %s: %v", pi.ID, err)
			} else {
				log.Infof("Connected to bootstrap peer %s", pi.ID)
			}
		}(peerInfo)
	}

	return rb, nil
}

// MonitorDHT continuously monitors the DHT for edge relay announcements.
func (rb *RegistryBuilder) MonitorDHT(ctx context.Context, pollInterval, staleTimeout time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Initial discovery
	rb.discoverRelays(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updated := rb.discoverRelays(ctx)
			pruned := rb.pruneStaleRelays(staleTimeout)

			if updated || pruned {
				rb.rebuildAndDeploy()
			}
		}
	}
}

func (rb *RegistryBuilder) discoverRelays(ctx context.Context) bool {
	// Create CID for edge relay provider key
	mh, _ := multihash.Sum([]byte("spacedatanetwork-edge-relay"), multihash.SHA2_256, -1)
	providerKey := cid.NewCidV1(cid.Raw, mh)

	// Find providers
	providers := rb.dht.FindProvidersAsync(ctx, providerKey, 100)

	updated := false
	for provider := range providers {
		if rb.verifyEdgeRelay(ctx, provider) {
			rb.mu.Lock()
			peerID := provider.ID.String()
			if _, exists := rb.knownRelays[peerID]; !exists {
				// New relay discovered
				var multiaddr string
				if len(provider.Addrs) > 0 {
					multiaddr = provider.Addrs[0].String() + "/p2p/" + peerID
				}

				rb.knownRelays[peerID] = &RelayInfo{
					PeerID:    peerID,
					Multiaddr: multiaddr,
					LastSeen:  time.Now().Unix(),
				}
				updated = true
				log.Infof("Discovered new edge relay: %s", peerID[:16]+"...")
			} else {
				// Update last seen
				rb.knownRelays[peerID].LastSeen = time.Now().Unix()
			}
			rb.mu.Unlock()
		}
	}

	return updated
}

func (rb *RegistryBuilder) verifyEdgeRelay(ctx context.Context, peerInfo peer.AddrInfo) bool {
	// Connect to peer and verify it supports relay service
	if err := rb.host.Connect(ctx, peerInfo); err != nil {
		return false
	}

	// Check for relay protocol support
	protos, err := rb.host.Peerstore().GetProtocols(peerInfo.ID)
	if err != nil {
		return false
	}

	for _, proto := range protos {
		if proto == "/libp2p/circuit/relay/0.2.0/hop" {
			return true
		}
	}

	return false
}

func (rb *RegistryBuilder) pruneStaleRelays(staleTimeout time.Duration) bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	cutoff := time.Now().Add(-staleTimeout).Unix()
	pruned := false

	for id, relay := range rb.knownRelays {
		if relay.LastSeen < cutoff {
			delete(rb.knownRelays, id)
			pruned = true
			log.Infof("Pruned stale edge relay: %s", id[:16]+"...")
		}
	}

	return pruned
}

func (rb *RegistryBuilder) rebuildAndDeploy() {
	rb.mu.RLock()
	relays := make([]RelayInfo, 0, len(rb.knownRelays))
	for _, r := range rb.knownRelays {
		relays = append(relays, *r)
	}
	rb.mu.RUnlock()

	if len(relays) == 0 {
		log.Warn("No relays to build registry from")
		return
	}

	// Write relay list to temp file
	relayJSON, _ := json.Marshal(relays)
	tmpFile := filepath.Join(os.TempDir(), "edge-relays.json")
	if err := os.WriteFile(tmpFile, relayJSON, 0644); err != nil {
		log.Errorf("Failed to write relay list: %v", err)
		return
	}

	// Run build script
	log.Infof("Rebuilding registry with %d relays...", len(relays))
	cmd := exec.Command("npx", "ts-node", rb.buildScript, tmpFile)
	cmd.Dir = filepath.Dir(rb.buildScript)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Errorf("Failed to rebuild registry: %v", err)
		return
	}

	// Deploy to CDN endpoints
	for _, endpoint := range rb.cdnEndpoints {
		rb.deployToCDN(endpoint)
	}

	log.Infof("Deployed registry with %d relays", len(relays))
}

func (rb *RegistryBuilder) deployToCDN(endpoint string) {
	wasmFile := filepath.Join(rb.outputDir, "edge-relays.wasm")

	// Check if file exists
	if _, err := os.Stat(wasmFile); os.IsNotExist(err) {
		log.Warnf("WASM file not found: %s", wasmFile)
		return
	}

	// Simple S3 upload example (customize for your CDN)
	log.Infof("Deploying to CDN: %s", endpoint)

	// For AWS S3:
	// cmd := exec.Command("aws", "s3", "cp", wasmFile, endpoint,
	//     "--cache-control", "max-age=300",
	//     "--content-type", "application/wasm")

	// For now, just log
	log.Infof("Would deploy %s to %s", wasmFile, endpoint)
}

// Close shuts down the registry builder.
func (rb *RegistryBuilder) Close() error {
	if rb.cancel != nil {
		rb.cancel()
	}
	return rb.host.Close()
}

// Stats returns current relay statistics.
func (rb *RegistryBuilder) Stats() map[string]int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	return map[string]int{
		"known_relays":     len(rb.knownRelays),
		"connected_peers":  len(rb.host.Network().Peers()),
	}
}
