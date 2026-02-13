// Package node provides the main SDN node implementation.
package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	"github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spacedatanetwork/sdn-server/plugins"
	"github.com/spacedatanetwork/sdn-server/plugins/licenseplugin"
	"github.com/spacedatanetwork/sdn-server/plugins/wasmlicenseplugin"
)

var log = logging.Logger("sdn-node")

const (
	// SDNVersion is the current version used for discovery namespace
	SDNVersion = "spacedatanetwork/1.0.0"

	// mDNS service name
	MDNSServiceName = "space-data-network-mdns"
)

// Node represents a Space Data Network node.
type Node struct {
	host      host.Host
	dht       *dht.IpfsDHT
	pubsub    *pubsub.PubSub
	topics    map[string]*pubsub.Topic
	flatc     *wasm.FlatcModule
	hdwallet  *wasm.HDWalletModule
	identity  *wasm.DerivedIdentity // nil if using random key (no HD wallet)
	validator *sds.Validator
	store     *storage.FlatSQLStore
	protocol  *protocol.SDSExchangeHandler
	plugins   *plugins.Manager
	license   *licenseplugin.Plugin
	keyBroker *wasmlicenseplugin.Plugin
	config    *config.Config

	// Trusted peer management
	peerRegistry *peers.Registry
	peerGater    *peers.TrustedConnectionGater

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new SDN node.
func New(ctx context.Context, cfg *config.Config) (*Node, error) {
	nodeCtx, cancel := context.WithCancel(ctx)

	n := &Node{
		topics: make(map[string]*pubsub.Topic),
		config: cfg,
		ctx:    nodeCtx,
		cancel: cancel,
	}

	if err := n.init(); err != nil {
		cancel()
		return nil, err
	}

	return n, nil
}

func (n *Node) init() error {
	// Initialize HD wallet WASM module (optional, enables deterministic identity)
	if hdPath := n.findHDWalletWasmPath(); hdPath != "" {
		// H11: Compute and log SHA-256 hash of WASM file for integrity verification.
		wasmBytes, err := os.ReadFile(hdPath)
		if err != nil {
			log.Warnf("HD wallet WASM not loaded (will use random key): %v", err)
		} else {
			wasmHash := sha256.Sum256(wasmBytes)
			log.Infof("WASM module loaded: %s (sha256: %s)", hdPath, hex.EncodeToString(wasmHash[:]))

			hw, err := wasm.NewHDWalletModuleFromBytes(n.ctx, wasmBytes)
			if err != nil {
				log.Warnf("HD wallet WASM not loaded (will use random key): %v", err)
			} else {
				n.hdwallet = hw
				// M10: Make entropy injection failure fatal - log critical warning.
				entropy := make([]byte, 64)
				if _, err := rand.Read(entropy); err != nil {
					return fmt.Errorf("CRITICAL: failed to read random entropy: %w", err)
				}
				if err := hw.InjectEntropy(n.ctx, entropy); err != nil {
					log.Errorf("CRITICAL: Failed to inject entropy into WASM module: %v", err)
				}
				log.Infof("HD wallet WASM loaded - deterministic identity derivation available")
			}
		}
	}

	// Generate or load identity key
	privKey, err := n.loadOrCreateKey()
	if err != nil {
		return fmt.Errorf("failed to load identity: %w", err)
	}

	// Initialize trusted peer registry
	registryPath := n.config.Peers.RegistryPath
	if registryPath == "" {
		registryPath = filepath.Join(filepath.Dir(n.config.Storage.Path), "peers.db")
	}
	persistence, err := peers.NewSQLitePersistence(registryPath)
	if err != nil {
		log.Warnf("Failed to create peer persistence, using in-memory registry: %v", err)
		persistence = nil
	}
	n.peerRegistry = peers.NewRegistry(n.config.Peers.StrictMode, persistence)
	n.peerGater = peers.NewTrustedConnectionGater(n.peerRegistry)

	// Log trusted peer mode
	if n.config.Peers.StrictMode {
		log.Infof("Trusted peer strict mode ENABLED - only registry peers allowed")
	} else {
		log.Infof("Trusted peer strict mode disabled - unknown peers allowed with Standard trust")
	}

	// Add configured trusted peers to registry
	for _, peerAddr := range n.config.Peers.TrustedPeers {
		addrInfo, err := peer.AddrInfoFromString(peerAddr)
		if err != nil {
			log.Warnf("Invalid trusted peer address %s: %v", peerAddr, err)
			continue
		}
		tp := &peers.TrustedPeer{
			ID:         addrInfo.ID,
			Addrs:      addrInfo.Addrs,
			TrustLevel: peers.Trusted,
			Name:       "Config Trusted Peer",
		}
		if err := n.peerRegistry.AddPeer(tp); err != nil && err != peers.ErrPeerAlreadyExists {
			log.Warnf("Failed to add trusted peer %s: %v", addrInfo.ID, err)
		}
	}

	// Parse listen addresses
	listenAddrs := make([]multiaddr.Multiaddr, 0, len(n.config.Network.Listen))
	for _, addr := range n.config.Network.Listen {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			return fmt.Errorf("invalid listen address %s: %w", addr, err)
		}
		listenAddrs = append(listenAddrs, ma)
	}

	// Create connection manager
	connMgr, err := connmgr.NewConnManager(
		100,                       // low water
		n.config.Network.MaxConns, // high water
	)
	if err != nil {
		return fmt.Errorf("failed to create connection manager: %w", err)
	}

	// Create libp2p host with connection gater for trust-based filtering
	var dhtRouting *dht.IpfsDHT
	n.host, err = libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrs(listenAddrs...),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(websocket.New),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.Security(noise.ID, noise.New),
		libp2p.ConnectionManager(connMgr),
		libp2p.ConnectionGater(n.peerGater), // Trust-based connection gating
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
		libp2p.EnableRelayService(),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			var err error
			dhtRouting, err = dht.New(n.ctx, h,
				dht.Mode(dht.ModeAutoServer),
				dht.ProtocolPrefix("/spacedatanetwork"),
			)
			return dhtRouting, err
		}),
		libp2p.NATPortMap(),
		libp2p.EnableNATService(),
	)
	if err != nil {
		return fmt.Errorf("failed to create libp2p host: %w", err)
	}
	n.dht = dhtRouting

	// Create GossipSub
	n.pubsub, err = pubsub.NewGossipSub(n.ctx, n.host)
	if err != nil {
		return fmt.Errorf("failed to create pubsub: %w", err)
	}

	// Initialize WASM module for FlatBuffers (if available)
	n.flatc, err = wasm.NewFlatcModule(n.ctx, n.findWasmPath())
	if err != nil {
		log.Warnf("FlatBuffer WASM not loaded (optional): %v", err)
		// Continue without WASM - it's optional for basic operation
	}

	// Initialize validator (uses WASM if available)
	n.validator, err = sds.NewValidator(n.flatc)
	if err != nil {
		return fmt.Errorf("failed to create validator: %w", err)
	}

	// Initialize storage (if not edge mode)
	if n.config.Mode != "edge" {
		n.store, err = storage.NewFlatSQLStore(n.config.Storage.Path, n.validator)
		if err != nil {
			return fmt.Errorf("failed to create storage: %w", err)
		}
	}

	// Setup protocol handler with message limits from config
	limits := protocol.MessageLimits{
		MaxMessageSize: n.config.Network.MaxMessageSize,
		MaxSchemaName:  n.config.Network.MaxSchemaName,
		MaxQuerySize:   n.config.Network.MaxQuerySize,
	}
	// Use defaults if not configured
	if limits.MaxMessageSize <= 0 {
		limits.MaxMessageSize = 10 * 1024 * 1024 // 10MB
	}
	if limits.MaxSchemaName <= 0 {
		limits.MaxSchemaName = 256
	}
	if limits.MaxQuerySize <= 0 {
		limits.MaxQuerySize = 4 * 1024 // 4KB
	}

	// Log security status at startup
	if n.flatc == nil && !n.config.Security.InsecureMode {
		log.Errorf("SECURITY: WASM crypto module not loaded and insecure mode is disabled")
		log.Errorf("SECURITY: All data push operations will be REJECTED until WASM is available")
		log.Errorf("SECURITY: Ensure flatc.wasm is available, or enable insecure mode for development")
	}

	// Create rate limiter for DoS protection
	var rateLimiter *protocol.PeerRateLimiter
	if n.config.Network.MaxMessagesPerSecond > 0 || n.config.Network.MaxMessagesPerMinute > 0 {
		rateLimitConfig := protocol.RateLimitConfig{
			MaxMessagesPerSecond: n.config.Network.MaxMessagesPerSecond,
			MaxMessagesPerMinute: n.config.Network.MaxMessagesPerMinute,
			Burst:                n.config.Network.RateLimitBurst,
		}
		// Apply defaults if not configured
		if rateLimitConfig.MaxMessagesPerSecond <= 0 {
			rateLimitConfig.MaxMessagesPerSecond = 100
		}
		if rateLimitConfig.MaxMessagesPerMinute <= 0 {
			rateLimitConfig.MaxMessagesPerMinute = 1000
		}
		if rateLimitConfig.Burst <= 0 {
			rateLimitConfig.Burst = 50
		}
		rateLimiter = protocol.NewPeerRateLimiter(rateLimitConfig)
	}

	n.protocol = protocol.NewSDSExchangeHandlerWithOptions(n.store, n.validator, n.flatc, limits, n.config.Security.InsecureMode, rateLimiter)
	n.host.SetStreamHandler(protocol.SDSProtocolID, n.protocol.HandleStream)
	n.host.SetStreamHandler(protocol.IDExchangeProtoID, protocol.HandleLegacyIDExchange)
	n.host.SetStreamHandler(protocol.ChatProtoID, protocol.HandleLegacyChat)

	// Initialize runtime plugins.
	n.plugins = plugins.New()
	n.license = licenseplugin.New()
	if err := n.plugins.Register(n.license); err != nil {
		log.Warnf("Failed to register plugin %q: %v", licenseplugin.ID, err)
	}

	// Register WASI-based OrbPro key broker plugin.
	if wasmPath := n.findKeyBrokerWasmPath(); wasmPath != "" {
		// H11: Compute and log SHA-256 hash of WASM file for integrity verification.
		if kbBytes, err := os.ReadFile(wasmPath); err == nil {
			kbHash := sha256.Sum256(kbBytes)
			log.Infof("WASM module loaded: %s (sha256: %s)", wasmPath, hex.EncodeToString(kbHash[:]))
		}
		n.keyBroker = wasmlicenseplugin.New(wasmPath)
		if err := n.plugins.Register(n.keyBroker); err != nil {
			log.Warnf("Failed to register plugin %q: %v", wasmlicenseplugin.ID, err)
		} else {
			log.Infof("OrbPro key broker WASM registered from %s", wasmPath)
		}
	}

	basePath := filepath.Dir(n.config.Storage.Path)
	pluginCtx := plugins.RuntimeContext{
		Host:         n.host,
		BaseDataPath: basePath,
		PeerID:       n.host.ID().String(),
		Mode:         n.config.Mode,
	}
	if err := n.plugins.StartAll(n.ctx, pluginCtx); err != nil {
		log.Warnf("Plugin startup completed with errors: %v", err)
	}
	if n.license.Service() != nil {
		log.Infof("Plugin enabled: %s (%s)", n.license.ID(), license.ProtocolID)
	}

	return nil
}

func (n *Node) loadOrCreateKey() (crypto.PrivKey, error) {
	keyDir := filepath.Join(filepath.Dir(n.config.Storage.Path), "keys")
	keyPath := filepath.Join(keyDir, "node.key")
	mnemonicPath := filepath.Join(keyDir, "mnemonic")

	// If HD wallet is available, prefer mnemonic-based identity
	if n.hdwallet != nil {
		if err := os.MkdirAll(keyDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create key directory: %w", err)
		}

		var mnemonic string

		// Try to load existing mnemonic
		if data, err := os.ReadFile(mnemonicPath); err == nil {
			mnemonic = string(data)
			log.Infof("Loaded existing mnemonic from %s", mnemonicPath)
		} else {
			// Generate new mnemonic
			newMnemonic, _, err := n.hdwallet.GenerateNewIdentity(n.ctx, 24)
			if err != nil {
				log.Warnf("HD wallet mnemonic generation failed, falling back to random key: %v", err)
				return n.generateRandomKey(keyDir, keyPath)
			}
			mnemonic = newMnemonic

			// Save mnemonic to disk
			if err := os.WriteFile(mnemonicPath, []byte(mnemonic), 0600); err != nil {
				return nil, fmt.Errorf("failed to save mnemonic: %w", err)
			}
			log.Infof("Generated and saved new mnemonic to %s", mnemonicPath)
		}

		// Derive identity from mnemonic
		identity, err := n.hdwallet.IdentityFromMnemonic(n.ctx, mnemonic, "", 0)
		if err != nil {
			log.Warnf("HD wallet identity derivation failed, falling back to random key: %v", err)
			return n.generateRandomKey(keyDir, keyPath)
		}

		n.identity = identity
		info := identity.Info()
		log.Infof("HD wallet identity derived: PeerID=%s SigningPath=%s EncryptionPath=%s",
			info.PeerID, info.SigningKeyPath, info.EncryptionKeyPath)

		// Also save the serialized key for backward compatibility
		keyData, err := identity.MarshalPrivateKey()
		if err == nil {
			_ = os.WriteFile(keyPath, keyData, 0600)
		}

		return identity.SigningPrivKey, nil
	}

	// Fallback: load existing key or generate random one
	if keyData, err := os.ReadFile(keyPath); err == nil {
		privKey, err := crypto.UnmarshalPrivateKey(keyData)
		if err == nil {
			log.Infof("Loaded existing node identity from %s", keyPath)
			return privKey, nil
		}
		log.Warnf("Failed to unmarshal existing key, generating new one: %v", err)
	}

	return n.generateRandomKey(keyDir, keyPath)
}

func (n *Node) generateRandomKey(keyDir, keyPath string) (crypto.PrivKey, error) {
	privKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create key directory: %w", err)
	}

	keyData, err := crypto.MarshalPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}

	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		return nil, fmt.Errorf("failed to write key file: %w", err)
	}

	log.Infof("Generated and saved new node identity to %s", keyPath)
	return privKey, nil
}

func (n *Node) findWasmPath() string {
	// Look for flatc-wasm in common locations
	paths := []string{
		"../flatbuffers/wasm/flatc.wasm",
		"../../flatbuffers/wasm/flatc.wasm",
		"/usr/local/lib/flatc.wasm",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (n *Node) findHDWalletWasmPath() string {
	// Check environment variable first
	if envPath := os.Getenv("HD_WALLET_WASM_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}
	// Look for hd-wallet WASM binary. Prefer the hardened Emscripten WASI build
	// (hd-wallet-wasi.wasm) which includes Crypto++ with constant-time operations,
	// HMAC-DRBG entropy, and SecureAllocator. Fall back to legacy wasi-sdk build.
	paths := []string{
		"../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"/usr/local/lib/hd-wallet-wasi.wasm",
		"../../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm",
		"../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm",
		"/usr/local/lib/hd-wallet.wasm",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (n *Node) findKeyBrokerWasmPath() string {
	if envPath := os.Getenv("ORBPRO_KEY_BROKER_WASM_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}
	paths := []string{
		"../../packages/sdn-license-plugin/build-wasi/sdn-license-plugin.wasm",
		"../packages/sdn-license-plugin/build-wasi/sdn-license-plugin.wasm",
		"/usr/local/lib/sdn-license-plugin.wasm",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Start begins the node's network operations.
func (n *Node) Start(ctx context.Context) error {
	// Bootstrap DHT
	if err := n.dht.Bootstrap(ctx); err != nil {
		return fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	// Validate bootstrap configuration and warn about missing peer IDs
	if warnings := bootstrap.ValidateBootstrapConfig(n.config.Network.Bootstrap); len(warnings) > 0 {
		for _, w := range warnings {
			log.Warnf("Bootstrap configuration: %s", w)
		}
	}

	// Parse and validate bootstrap addresses with peer ID pinning
	bootstrapPeers, err := bootstrap.ParseBootstrapAddresses(n.config.Network.Bootstrap)
	if err != nil {
		log.Warnf("Error parsing bootstrap addresses: %v", err)
	}

	// Filter to only peers with pinned IDs for security
	pinnedPeers := bootstrap.RequirePinnedPeerIDs(bootstrapPeers)
	if len(pinnedPeers) < len(bootstrapPeers) {
		log.Warnf("Skipping %d bootstrap peers without peer IDs (peer ID pinning required)",
			len(bootstrapPeers)-len(pinnedPeers))
	}

	// Connect to bootstrap peers asynchronously with peer ID verification
	for _, p := range pinnedPeers {
		n.wg.Add(1)
		go func(peerInfo bootstrap.PeerInfo) {
			defer n.wg.Done()
			if err := n.host.Connect(ctx, peerInfo.AddrInfo); err != nil {
				log.Warnf("Failed to connect to bootstrap peer %s: %v", peerInfo.AddrInfo.ID, err)
			} else {
				log.Infof("Connected to bootstrap peer %s (peer ID verified)", peerInfo.AddrInfo.ID)
			}
		}(p)
	}

	// Setup per-schema PubSub topics
	for _, schema := range n.validator.Schemas() {
		topicName := fmt.Sprintf("/spacedatanetwork/sds/%s", schema)
		topic, err := n.pubsub.Join(topicName)
		if err != nil {
			log.Warnf("Failed to join topic %s: %v", topicName, err)
			continue
		}
		n.topics[schema] = topic

		// Subscribe to receive messages
		sub, err := topic.Subscribe()
		if err != nil {
			log.Warnf("Failed to subscribe to %s: %v", topicName, err)
			continue
		}

		n.wg.Add(1)
		go n.handleSubscription(sub, schema)
	}

	// Start mDNS discovery
	n.wg.Add(1)
	go n.runMDNS()

	// Announce on DHT with custom discovery namespace
	n.wg.Add(1)
	go n.runDHTDiscovery()

	return nil
}

func (n *Node) handleSubscription(sub *pubsub.Subscription, schema string) {
	defer n.wg.Done()

	for {
		msg, err := sub.Next(n.ctx)
		if err != nil {
			if n.ctx.Err() != nil {
				return
			}
			log.Warnf("Error reading from subscription %s: %v", schema, err)
			continue
		}

		// Skip messages from ourselves
		if msg.ReceivedFrom == n.host.ID() {
			continue
		}

		// Process the message
		if err := n.protocol.HandlePubSubMessage(schema, msg.Data, msg.ReceivedFrom); err != nil {
			log.Warnf("Failed to handle message on %s: %v", schema, err)
		}
	}
}

// mdnsNotifee handles mDNS peer discovery events.
type mdnsNotifee struct {
	host host.Host
	ctx  context.Context
}

// HandlePeerFound is called when a peer is discovered via mDNS.
func (m *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	// Don't connect to ourselves
	if pi.ID == m.host.ID() {
		return
	}

	log.Debugf("mDNS discovered peer: %s", pi.ID)

	// Connect to the discovered peer
	if err := m.host.Connect(m.ctx, pi); err != nil {
		log.Debugf("Failed to connect to mDNS peer %s: %v", pi.ID, err)
	} else {
		log.Infof("Connected to mDNS peer: %s", pi.ID)
	}
}

func (n *Node) runMDNS() {
	defer n.wg.Done()

	notifee := &mdnsNotifee{
		host: n.host,
		ctx:  n.ctx,
	}

	// Create mDNS service with our custom service name
	mdnsService := mdns.NewMdnsService(n.host, MDNSServiceName, notifee)
	if err := mdnsService.Start(); err != nil {
		log.Warnf("Failed to start mDNS service: %v", err)
		return
	}
	defer mdnsService.Close()

	log.Infof("mDNS discovery started with service name: %s", MDNSServiceName)

	// Wait for context cancellation
	<-n.ctx.Done()
	log.Debug("mDNS discovery stopped")
}

func (n *Node) runDHTDiscovery() {
	defer n.wg.Done()

	// Create discovery namespace from version hash using SHA-256
	// Note: Using SHA-256 instead of Argon2 since this is for deterministic
	// namespace generation, not password hashing. Argon2 is designed for
	// password-based key derivation with computational cost, which is
	// inappropriate for this use case.
	versionBytes := []byte(SDNVersion)
	hash := sha256.Sum256(versionBytes)
	discoveryNS := hex.EncodeToString(hash[:])

	log.Infof("DHT discovery namespace: %s", discoveryNS[:16]+"...")

	// Create a CID for the discovery namespace to use with DHT.Provide
	// We use the namespace hash as the content ID
	multihash, err := mh.Encode(hash[:], mh.SHA2_256)
	if err != nil {
		log.Errorf("Failed to create multihash for discovery: %v", err)
		return
	}
	discoveryCID := cid.NewCidV1(cid.Raw, multihash)

	// Announcement interval (every 30 seconds as per Agents.md spec)
	announceTicker := time.NewTicker(30 * time.Second)
	defer announceTicker.Stop()

	// Discovery ticker (find other peers every 60 seconds)
	discoveryTicker := time.NewTicker(60 * time.Second)
	defer discoveryTicker.Stop()

	// Initial announcement
	n.announceOnDHT(discoveryCID)

	for {
		select {
		case <-n.ctx.Done():
			log.Debug("DHT discovery stopped")
			return

		case <-announceTicker.C:
			n.announceOnDHT(discoveryCID)

		case <-discoveryTicker.C:
			n.discoverPeers(discoveryCID)
		}
	}
}

// announceOnDHT announces our presence in the DHT discovery namespace.
func (n *Node) announceOnDHT(discoveryCID cid.Cid) {
	ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
	defer cancel()

	err := n.dht.Provide(ctx, discoveryCID, true)
	if err != nil {
		log.Debugf("DHT announce failed: %v", err)
	} else {
		log.Debug("DHT announcement successful")
	}
}

// discoverPeers finds other SDN peers in the DHT discovery namespace.
func (n *Node) discoverPeers(discoveryCID cid.Cid) {
	ctx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
	defer cancel()

	// Find providers (other SDN nodes) in the discovery namespace
	peerChan := n.dht.FindProvidersAsync(ctx, discoveryCID, 20)

	for peerInfo := range peerChan {
		// Skip ourselves
		if peerInfo.ID == n.host.ID() {
			continue
		}

		// Skip if already connected
		if n.host.Network().Connectedness(peerInfo.ID) == 2 { // Connected
			continue
		}

		// Try to connect
		go func(pi peer.AddrInfo) {
			connectCtx, connectCancel := context.WithTimeout(n.ctx, 10*time.Second)
			defer connectCancel()

			if err := n.host.Connect(connectCtx, pi); err != nil {
				log.Debugf("Failed to connect to discovered peer %s: %v", pi.ID, err)
			} else {
				log.Infof("Connected to discovered SDN peer: %s", pi.ID)
			}
		}(peerInfo)
	}
}

// Stop gracefully shuts down the node.
func (n *Node) Stop() error {
	n.cancel()
	n.wg.Wait()

	if n.store != nil {
		if err := n.store.Close(); err != nil {
			log.Warnf("Error closing storage: %v", err)
		}
	}
	if n.plugins != nil {
		if err := n.plugins.Close(); err != nil {
			log.Warnf("Error closing plugins: %v", err)
		}
	}

	if err := n.host.Close(); err != nil {
		return fmt.Errorf("failed to close host: %w", err)
	}

	return nil
}

// PeerID returns the node's peer ID.
func (n *Node) PeerID() peer.ID {
	return n.host.ID()
}

// ListenAddrs returns the node's listen addresses.
func (n *Node) ListenAddrs() []multiaddr.Multiaddr {
	return n.host.Addrs()
}

// Publish publishes data to a schema topic.
func (n *Node) Publish(schema string, data []byte) error {
	topic, ok := n.topics[schema]
	if !ok {
		return fmt.Errorf("unknown schema: %s", schema)
	}

	return topic.Publish(n.ctx, data)
}

// PeerRegistry returns the trusted peer registry.
func (n *Node) PeerRegistry() *peers.Registry {
	return n.peerRegistry
}

// PeerGater returns the connection gater for trust-based filtering.
func (n *Node) PeerGater() *peers.TrustedConnectionGater {
	return n.peerGater
}

// Config returns the node configuration.
func (n *Node) Config() *config.Config {
	return n.config
}

// Store returns the local storage backend (nil for edge mode).
func (n *Node) Store() *storage.FlatSQLStore {
	return n.store
}

// PluginManager returns the node plugin manager.
func (n *Node) PluginManager() *plugins.Manager {
	return n.plugins
}

// LicenseService returns the local license service (nil in edge mode or if unavailable).
func (n *Node) LicenseService() *license.Service {
	if n.license == nil {
		return nil
	}
	return n.license.Service()
}

// Identity returns the node's HD wallet identity, or nil if using a random key.
func (n *Node) Identity() *wasm.DerivedIdentity {
	return n.identity
}

// TokenVerifier returns the capability-token verifier from the license plugin.
func (n *Node) TokenVerifier() *license.TokenVerifier {
	if n.license == nil {
		return nil
	}
	return n.license.TokenVerifier()
}

// DHT returns the Kademlia DHT instance for content routing.
func (n *Node) DHT() *dht.IpfsDHT {
	return n.dht
}

// Host returns the libp2p host.
func (n *Node) Host() host.Host {
	return n.host
}

// PubSub returns the GossipSub PubSub instance.
func (n *Node) PubSub() *pubsub.PubSub {
	return n.pubsub
}

// SigningKey returns the node's Ed25519 signing private key bytes, or nil if unavailable.
func (n *Node) SigningKey() []byte {
	if n.identity != nil && n.identity.SigningPrivKey != nil {
		raw, err := n.identity.SigningPrivKey.Raw()
		if err == nil {
			return raw
		}
	}
	return nil
}
