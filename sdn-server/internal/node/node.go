// Package node provides the main SDN node implementation.
package node

import (
	"context"
	crypto_ecdh "crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
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
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	"github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt/capabilities"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/logservice"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spacedatanetwork/sdn-server/plugins"
	"github.com/spacedatanetwork/sdn-server/plugins/ailogplugin"
)

var log = logging.Logger("sdn-node")

const (
	// SDNVersion is the current version used for discovery namespace
	SDNVersion = versioninfo.CurrentAdvertisementFlag

	// mDNS service name
	MDNSServiceName = "space-data-network-mdns"
)

// Node represents a Space Data Network node.
type Node struct {
	host        host.Host
	dht         *dht.IpfsDHT
	pubsub      *pubsub.PubSub
	topics      map[string]*pubsub.Topic
	flatc       *wasm.FlatcModule
	hdwallet    *wasm.HDWalletModule
	identity    *wasm.DerivedIdentity // nil if using random key (no HD wallet)
	validator   *sds.Validator
	store       *storage.FlatSQLStore
	protocol    *protocol.SDSExchangeHandler
	plugins     *plugins.Manager
	epmService  *epm.Service
	logService  *logservice.Service
	flowManager *flowrt.FlowManager
	config      *config.Config

	// Trusted peer management
	peerRegistry *peers.Registry
	peerGater    *peers.TrustedConnectionGater

	pluginRegistry          *license.PluginRegistry
	moduleDeliveryDiscovery cid.Cid
	sdnAdvertisementTarget  sdnAdvertisementDiscoveryTarget
	sdnDiscoveryTargets     []sdnAdvertisementDiscoveryTarget
	sdnDiscoveryMu          sync.RWMutex
	sdnDiscoveryFlagsByPeer map[peer.ID]map[string]time.Time
	sdnDiscoveryAddrsByPeer map[peer.ID][]string
	autoRelayPeerChan       chan peer.AddrInfo

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

const licensingModuleID = "licensing"

// New creates a new SDN node.
func New(ctx context.Context, cfg *config.Config) (*Node, error) {
	nodeCtx, cancel := context.WithCancel(ctx)

	n := &Node{
		topics:                  make(map[string]*pubsub.Topic),
		config:                  cfg,
		ctx:                     nodeCtx,
		cancel:                  cancel,
		sdnDiscoveryFlagsByPeer: make(map[peer.ID]map[string]time.Time),
		sdnDiscoveryAddrsByPeer: make(map[peer.ID][]string),
		autoRelayPeerChan:       make(chan peer.AddrInfo, 64),
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
	if providerPubKey, err := compressedSecp256k1PublicKey(privKey); err != nil {
		log.Warnf("Module delivery provider public key unavailable: %v", err)
	} else if discoveryCID, err := computeModuleDeliveryDiscoveryCID(providerPubKey); err != nil {
		log.Warnf("Module delivery discovery CID unavailable: %v", err)
	} else {
		n.moduleDeliveryDiscovery = discoveryCID
	}
	if currentTarget, discoverTargets, err := sdnAdvertisementDiscoveryTargets(versioninfo.CurrentAdvertisementFlag, versioninfo.CopySupportedAdvertisementFlags()); err != nil {
		log.Warnf("SDN advertisement discovery targets unavailable: %v", err)
	} else {
		n.sdnAdvertisementTarget = currentTarget
		n.sdnDiscoveryTargets = discoverTargets
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
		1000,                      // low water
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
		libp2p.EnableAutoRelayWithPeerSource(
			func(ctx context.Context, _ int) <-chan peer.AddrInfo {
				return n.autoRelayPeerChan
			},
			autorelay.WithMinInterval(0),
		),
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
	go n.feedAutoRelayCandidates(n.ctx)

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
	log.Infof("SECURITY: SDS message auth mode = transport-authenticated streams (no detached payload signatures)")

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

	n.protocol = protocol.NewSDSExchangeHandlerWithOptions(n.store, n.validator, limits, rateLimiter)
	n.host.SetStreamHandler(protocol.SDSProtocolID, n.protocol.HandleStream)
	n.host.SetStreamHandler(protocol.IDExchangeProtoID, protocol.HandleLegacyIDExchange)
	n.host.SetStreamHandler(protocol.ChatProtoID, protocol.HandleLegacyChat)

	// Initialize EPM (Entity Profile Message) service for node identity cards.
	basePath := filepath.Dir(n.config.Storage.Path)
	storageBasePath := strings.TrimSpace(n.config.Storage.Path)
	var xpubStr string
	if n.hdwallet != nil && n.identity != nil {
		// Derive xpub from encrypted mnemonic seed for the EPM
		mnemonicPath := filepath.Join(basePath, "keys", "mnemonic")
		if mnemonicData, err := os.ReadFile(mnemonicPath); err == nil {
			var mnemonic string
			if keys.IsMnemonicEncrypted(mnemonicData) {
				mnemonic, _ = keys.DecryptMnemonic(mnemonicData, n.resolveKeyPassword())
			} else {
				mnemonic = string(mnemonicData)
			}
			if mnemonic != "" {
				if seed, err := n.hdwallet.MnemonicToSeed(n.ctx, mnemonic, ""); err == nil {
					if xpub, err := n.hdwallet.DeriveXPub(n.ctx, seed, 0); err == nil {
						xpubStr = xpub
					}
				}
			}
		}
	}
	// Initialize publication log service for PLG/PLH hash-chained logs.
	var signingKey crypto.PrivKey
	if n.identity != nil {
		signingKey = n.identity.SigningPrivKey
	}
	n.logService = logservice.NewService(n.store, signingKey, n.host.ID().String())

	// Register sync handler for MsgSyncLog requests.
	syncHandler := logservice.NewSyncHandler(n.store)
	n.protocol.SetSyncHandler(syncHandler)

	n.epmService = epm.NewService(n.identity, n.peerRegistry, n.host.ID(), xpubStr, basePath)
	if err := n.epmService.Init(); err != nil {
		log.Warnf("EPM service initialization failed (non-fatal): %v", err)
	} else {
		n.epmService.RegisterProtocol(n.host)
	}

	// Initialize runtime plugins.
	n.plugins = plugins.New()
	if err := n.plugins.Register(ailogplugin.New()); err != nil {
		log.Warnf("Failed to register plugin %q: %v", ailogplugin.ID, err)
	}

	runtimeIPFSAPIURL := n.resolveRuntimeIPFSAPIURL()
	if runtimeIPFSAPIURL != "" && strings.TrimSpace(n.config.Admin.IPFSAPIURL) == "" {
		log.Infof("Using detected local Kubo API for module runtime capabilities: %s", runtimeIPFSAPIURL)
	}

	pluginCtx := plugins.RuntimeContext{
		Host:         n.host,
		DHT:          n.dht,
		BaseDataPath: storageBasePath,
		PeerID:       n.host.ID().String(),
		IPFSAPIURL:   runtimeIPFSAPIURL,
		Mode:         n.config.Mode,
	}
	if _, wrappingKey, err := n.moduleRuntimeKeySlots(); err != nil {
		log.Warnf("Module runtime encryption key unavailable: %v", err)
	} else if len(wrappingKey) == 32 {
		pluginCtx.NodeEncryptionKey = wrappingKey
	} else {
		if envNodeEncKey := strings.TrimSpace(os.Getenv("SDN_DEV_NODE_ENCRYPTION_KEY_HEX")); envNodeEncKey != "" {
			if decoded, err := hex.DecodeString(envNodeEncKey); err != nil {
				log.Warnf("Invalid SDN_DEV_NODE_ENCRYPTION_KEY_HEX value, expected 64 hex chars: %v", err)
			} else if len(decoded) != 32 {
				log.Warnf("SDN_DEV_NODE_ENCRYPTION_KEY_HEX must be 32 bytes (got %d bytes)", len(decoded))
			} else {
				pluginCtx.NodeEncryptionKey = decoded
				log.Warnf("Using development node encryption key from SDN_DEV_NODE_ENCRYPTION_KEY_HEX")
			}
		}
	}

	// Register the unified licensing runtime, then publish encrypted catalog
	// modules through it so the delivery path matches the browser shim flow.
	var licensingModule *modulert.Module
	if reg, regErr := n.loadPluginRegistry(); regErr != nil {
		log.Warnf("Plugin registry unavailable: %v", regErr)
	} else if reg != nil {
		n.pluginRegistry = reg
		recipientKey, keyErr := n.findPluginDecryptPrivateKey()
		if keyErr != nil {
			log.Warnf("Plugin decryption key invalid: %v", keyErr)
		}

		if n.shouldLoadLicensingFromCatalog(reg) {
			nodeCtx, err := n.buildModuleNodeContext()
			if err != nil {
				log.Warnf("Failed to build module node context: %v", err)
			} else {
				capReg := n.buildCapRegistry()
				wasmBytes, err := reg.DecryptBundle(licensingModuleID, recipientKey)
				if err != nil {
					log.Warnf("Licensing module decryption failed: %v", err)
				} else if mod, err := modulert.NewModule(wasmBytes, capReg, nodeCtx); err != nil {
					log.Warnf("Licensing module load failed: %v", err)
				} else {
					licensingModule = mod
				}
			}
		}
	}

	// Register a fallback module-sdk WASM from explicit path.
	// This uses the generic modulert runner — no plugin-type-specific Go code.
	if licensingModule == nil {
		if wasmPath := n.findKeyBrokerWasmPath(); wasmPath != "" {
			kbBytes, decryptedEnvelope, loadErr := n.loadKeyBrokerWASMBytes(wasmPath)
			if loadErr != nil {
				log.Warnf("Failed to load module-sdk WASM from %s: %v", wasmPath, loadErr)
			} else {
				kbHash := sha256.Sum256(kbBytes)
				if decryptedEnvelope {
					log.Infof("WASM module loaded (decrypted): %s (sha256: %s)", wasmPath, hex.EncodeToString(kbHash[:]))
				} else {
					log.Infof("WASM module loaded: %s (sha256: %s)", wasmPath, hex.EncodeToString(kbHash[:]))
				}

				nodeCtx, err := n.buildModuleNodeContext()
				if err != nil {
					log.Warnf("Failed to build module node context: %v", err)
				} else {
					capReg := n.buildCapRegistry()
					mod, err := modulert.NewModule(kbBytes, capReg, nodeCtx)
					if err != nil {
						log.Warnf("Failed to create module from %s: %v", wasmPath, err)
					} else {
						licensingModule = mod
						log.Infof("Unified licensing module loaded from %s", wasmPath)
					}
				}
			}
		}
	}

	if licensingModule != nil {
		if n.pluginRegistry != nil {
			if err := bootstrapLicensingModule(licensingModule, n.pluginRegistry); err != nil {
				log.Warnf("Licensing module bootstrap completed with errors: %v", err)
			}
		}
		if err := n.plugins.Register(licensingModule); err != nil {
			log.Warnf("Failed to register module %q: %v", licensingModule.ID(), err)
		} else {
			log.Infof("Unified licensing module registered")
		}
	}

	// Initialize flow runtime manager and load installed flows.
	if n.config.Flows.Enabled {
		flowCaps := flowrt.HandlerMap{}
		if n.config.Admin.IPFSAPIURL != "" {
			ipfsHandlers := capabilities.NewIPFSHandlers(capabilities.IPFSConfig{
				APIURL: n.config.Admin.IPFSAPIURL,
			})
			flowCaps = flowCaps.Merge(ipfsHandlers)
		}
		if n.store != nil {
			storageHandlers := capabilities.NewStorageHandlers(n.store)
			flowCaps = flowCaps.Merge(storageHandlers)
		}

		fm, err := flowrt.NewFlowManager(n.config.Flows, n.plugins, flowCaps)
		if err != nil {
			log.Warnf("Failed to create flow manager: %v", err)
		} else {
			n.flowManager = fm
			if err := fm.LoadAll(n.ctx); err != nil {
				log.Warnf("Failed to load flows: %v", err)
			}
		}
	}

	if err := n.plugins.StartAll(n.ctx, pluginCtx); err != nil {
		log.Warnf("Plugin startup completed with errors: %v", err)
	}

	return nil
}

func (n *Node) loadPluginRegistry() (*license.PluginRegistry, error) {
	baseDataPath := strings.TrimSpace(n.config.Storage.Path)
	pluginRoot := strings.TrimSpace(os.Getenv("SDN_PLUGIN_ROOT"))
	if pluginRoot == "" {
		pluginRoot = license.DefaultPluginRoot(baseDataPath)
	}

	reg, err := license.LoadPluginRegistry(pluginRoot)
	if err != nil {
		return nil, fmt.Errorf("load plugin registry from %q: %w", pluginRoot, err)
	}
	if reg == nil {
		return nil, nil
	}
	if reg.Count() > 0 {
		log.Infof("Loaded %d plugin catalog entry(s) from %s", reg.Count(), pluginRoot)
	}
	return reg, nil
}

func (n *Node) shouldLoadLicensingFromCatalog(reg *license.PluginRegistry) bool {
	if reg == nil {
		return false
	}
	if _, ok := reg.Get(licensingModuleID); !ok {
		return false
	}
	return strings.TrimSpace(n.findKeyBrokerWasmPath()) == ""
}

func (n *Node) findPluginDecryptPrivateKey() ([]byte, error) {
	_, wrappingKey, err := n.moduleRuntimeKeySlots()
	if err != nil {
		return nil, err
	}
	if len(wrappingKey) == 32 {
		return wrappingKey, nil
	}

	return nil, nil
}

func (n *Node) registerCatalogPlugins(reg *license.PluginRegistry, pluginCtx plugins.RuntimeContext, recipientKey []byte) error {
	if reg == nil {
		return nil
	}

	nodeCtx, err := n.buildModuleNodeContext()
	if err != nil {
		return fmt.Errorf("build module node context: %w", err)
	}
	capReg := n.buildCapRegistry()

	var errs []error
	for _, descriptor := range reg.ListPublic() {
		pluginID := strings.TrimSpace(descriptor.ID)
		if pluginID == "" {
			continue
		}

		if existing := n.plugins.Get(pluginID); existing != nil {
			log.Infof("Plugin %q already registered; skipping catalog registration", pluginID)
			continue
		}

		wasmBytes, err := reg.DecryptBundle(pluginID, recipientKey)
		if err != nil {
			errMsg := fmt.Errorf("plugin %q decryption failed: %w", pluginID, err)
			_ = reg.SetRuntimeStatus(pluginID, "error", errMsg.Error())
			errs = append(errs, errMsg)
			continue
		}

		// Use the generic module runner — no plugin-type-specific Go code
		mod, err := modulert.NewModule(wasmBytes, capReg, nodeCtx)
		if err != nil {
			errMsg := fmt.Errorf("plugin %q module load failed: %w", pluginID, err)
			_ = reg.SetRuntimeStatus(pluginID, "error", errMsg.Error())
			errs = append(errs, errMsg)
			continue
		}

		if err := n.plugins.Register(mod); err != nil {
			errMsg := fmt.Errorf("plugin %q registration failed: %w", pluginID, err)
			_ = reg.SetRuntimeStatus(pluginID, "error", errMsg.Error())
			errs = append(errs, errMsg)
			continue
		}

		if err := reg.SetRuntimeStatus(pluginID, "stopped", "registered, waiting for startup"); err != nil {
			log.Warnf("Unable to update runtime status for plugin %q: %v", pluginID, err)
		}
		log.Infof("Registered catalog plugin %q via generic module runner", pluginID)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// buildCapRegistry creates a CapabilityRegistry populated with the SDN node's
// available services. Any module capability that maps to an unavailable service
// (e.g. IPFS when no Kubo URL is configured) is simply not registered — the
// module will receive an "operation not supported" error if it tries to use it.
func (n *Node) buildCapRegistry() *modulert.CapabilityRegistry {
	reg := modulert.NewCapabilityRegistry()

	// IPFS capability — prefer configured Kubo RPC, but fall back to a detected local daemon in dev.
	if runtimeIPFSAPIURL := n.resolveRuntimeIPFSAPIURL(); runtimeIPFSAPIURL != "" {
		reg.Register("ipfs", caps.NewIPFSCapFactory(runtimeIPFSAPIURL, nil))
	}

	// Storage capabilities — require an initialized FlatSQL store
	if n.store != nil {
		storageFac := caps.NewStorageCapFactory(n.store)
		reg.Register("storage_query", storageFac)
		reg.Register("storage_write", storageFac)
		reg.Register("storage_adapter", storageFac)
	}

	// HTTP outbound capability — always available
	reg.Register("http", caps.NewHTTPCapFactory())

	// Crypto capabilities — always available (pure Go stdlib)
	cryptoFac := caps.NewCryptoCapFactory()
	reg.Register("crypto_hash", cryptoFac)
	reg.Register("crypto_sign", cryptoFac)
	reg.Register("crypto_verify", cryptoFac)
	reg.Register("crypto_encrypt", cryptoFac)
	reg.Register("crypto_decrypt", cryptoFac)
	reg.Register("crypto_key_agreement", cryptoFac)
	reg.Register("crypto_kdf", cryptoFac)
	reg.Register("wallet_sign", caps.NewKeyslotCapFactory())

	// PubSub capability — requires libp2p pubsub to be running
	if n.pubsub != nil {
		reg.Register("pubsub", caps.NewPubSubCapFactory(n.pubsub))
	}

	// Protocol dial capability — requires the node's libp2p host to be running.
	if n.host != nil {
		reg.Register("protocol_dial", caps.NewProtocolCapFactory())
	}

	return reg
}

func (n *Node) getPluginByID(reg *license.PluginRegistry, pluginID string) (plugins.Plugin, bool) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return nil, false
	}
	if reg != nil {
		if _, ok := reg.Get(pluginID); !ok {
			return nil, false
		}
	}
	if n.plugins == nil {
		return nil, false
	}
	p := n.plugins.Get(pluginID)
	if p == nil {
		return nil, false
	}
	return p, true
}

func (n *Node) hasCatalogLicensingModule(reg *license.PluginRegistry) bool {
	_, ok := n.getPluginByID(reg, licensingModuleID)
	return ok
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

		// Resolve key password: env var > config > machine-derived default
		keyPassword := n.resolveKeyPassword()

		var mnemonic string

		// Try to load existing mnemonic (encrypted or plaintext)
		if data, err := os.ReadFile(mnemonicPath); err == nil {
			if keys.IsMnemonicEncrypted(data) {
				// Decrypt encrypted mnemonic
				mnemonic, err = keys.DecryptMnemonic(data, keyPassword)
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt mnemonic from %s: %w", mnemonicPath, err)
				}
				log.Infof("Loaded encrypted mnemonic from %s", mnemonicPath)
			} else {
				// Plaintext mnemonic found — migrate to encrypted format
				mnemonic = string(data)
				log.Warnf("Found plaintext mnemonic at %s — migrating to encrypted storage", mnemonicPath)
				encrypted, err := keys.EncryptMnemonic(mnemonic, keyPassword)
				if err != nil {
					return nil, fmt.Errorf("failed to encrypt mnemonic during migration: %w", err)
				}
				if err := os.WriteFile(mnemonicPath, encrypted, 0600); err != nil {
					return nil, fmt.Errorf("failed to write encrypted mnemonic: %w", err)
				}
				log.Infof("Mnemonic migrated to encrypted storage at %s", mnemonicPath)
			}
		} else {
			// Generate new mnemonic
			newMnemonic, _, err := n.hdwallet.GenerateNewIdentity(n.ctx, 24)
			if err != nil {
				log.Warnf("HD wallet mnemonic generation failed, falling back to random key: %v", err)
				return n.generateRandomKey(keyDir, keyPath)
			}
			mnemonic = newMnemonic

			// Save encrypted mnemonic to disk
			encrypted, err := keys.EncryptMnemonic(mnemonic, keyPassword)
			if err != nil {
				return nil, fmt.Errorf("failed to encrypt mnemonic: %w", err)
			}
			if err := os.WriteFile(mnemonicPath, encrypted, 0600); err != nil {
				return nil, fmt.Errorf("failed to save encrypted mnemonic: %w", err)
			}
			log.Infof("Generated and saved encrypted mnemonic to %s", mnemonicPath)
		}

		// Derive identity from mnemonic
		identity, err := n.hdwallet.IdentityFromMnemonic(n.ctx, mnemonic, "", 0)
		if err != nil {
			log.Warnf("HD wallet identity derivation failed, falling back to random key: %v", err)
			return n.generateRandomKey(keyDir, keyPath)
		}

		n.identity = identity
		info := identity.Info()
		log.Infof("HD wallet identity derived: PeerID=%s IdentityPath=%s SigningPath=%s EncryptionPath=%s",
			info.PeerID, info.IdentityKeyPath, info.SigningKeyPath, info.EncryptionKeyPath)

		// Also save the serialized key for backward compatibility
		keyData, err := identity.MarshalPrivateKey()
		if err == nil {
			_ = os.WriteFile(keyPath, keyData, 0600)
		}

		// Return secp256k1 identity key for libp2p PeerID
		return identity.IdentityPrivKey, nil
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

// resolveKeyPassword returns the password for mnemonic encryption/decryption.
// Priority: SDN_KEY_PASSWORD env var > config security.key_password > machine-derived default.
func (n *Node) resolveKeyPassword() string {
	if envPw := os.Getenv("SDN_KEY_PASSWORD"); envPw != "" {
		return envPw
	}
	if n.config.Security.KeyPassword != "" {
		return n.config.Security.KeyPassword
	}
	return keys.DeriveDefaultPassword()
}

func (n *Node) generateRandomKey(keyDir, keyPath string) (crypto.PrivKey, error) {
	privKey, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
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

// deriveP256PublicKeyHex derives a P-256 public key from a 32-byte seed and
// returns it as a hex string. Used to populate node.publicKey in the hostcall bridge.
func deriveP256PublicKeyHex(seed []byte) (string, error) {
	if len(seed) < 32 {
		return "", fmt.Errorf("seed too short")
	}
	h := sha256.Sum256(seed)
	privKey, err := crypto_ecdh.P256().NewPrivateKey(h[:])
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(privKey.PublicKey().Bytes()), nil
}

func (n *Node) findKeyBrokerWasmPath() string {
	if envPath := os.Getenv("ORBPRO_LICENSING_WASM_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}
	if envPath := os.Getenv("ORBPRO_KEY_BROKER_WASM_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}
	paths := []string{
		"packages/space-data-network-plugins/packages/licensing/dist/isomorphic/module.wasm",
		"../space-data-network-plugins/packages/licensing/dist/isomorphic/module.wasm",
		"../../space-data-network-plugins/packages/licensing/dist/isomorphic/module.wasm",
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

	// Parse and validate bootstrap addresses with peer ID pinning. If the
	// configured set is empty or invalid, fall back to the built-in default
	// peers so DHT discovery still comes up.
	pinnedPeers, usedFallback, err := bootstrap.ResolveBootstrapPeers(n.config.Network.Bootstrap)
	if err != nil {
		log.Warnf("Error resolving bootstrap addresses: %v", err)
	}
	if usedFallback {
		log.Warn("Configured bootstrap peers were empty or invalid; falling back to built-in defaults")
	}

	// Connect to bootstrap peers asynchronously with peer ID verification
	for _, p := range pinnedPeers {
		n.wg.Add(1)
		go func(peerInfo bootstrap.PeerInfo) {
			defer n.wg.Done()
			if err := n.host.Connect(ctx, peerInfo.AddrInfo); err != nil {
				log.Warnf("Failed to connect to bootstrap peer %s: %v", peerInfo.AddrInfo.ID, err)
			} else {
				n.enqueueAutoRelayCandidate(peerInfo.AddrInfo)
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

	// Start EPM auto-publish via PubSub (every 30 minutes)
	if n.epmService != nil && n.epmService.GetNodeEPM() != nil {
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			n.epmService.StartAutoPublish(n.ctx, n, 30*time.Minute)
		}()
	}

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
	node *Node
}

// HandlePeerFound is called when a peer is discovered via mDNS.
func (m *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if m == nil || m.node == nil || m.node.host == nil {
		return
	}

	// Don't connect to ourselves
	if pi.ID == m.node.host.ID() {
		return
	}

	log.Debugf("mDNS discovered peer: %s", pi.ID)

	// Connect to the discovered peer
	if err := m.node.host.Connect(m.node.ctx, pi); err != nil {
		log.Debugf("Failed to connect to mDNS peer %s: %v", pi.ID, err)
	} else {
		m.node.enqueueAutoRelayCandidate(pi)
		log.Infof("Connected to mDNS peer: %s", pi.ID)
	}
}

func (n *Node) runMDNS() {
	defer n.wg.Done()

	notifee := &mdnsNotifee{
		node: n,
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

	moduleTargets := moduleDeliveryDiscoveryTargets(n.moduleDeliveryDiscovery)
	if len(moduleTargets) == 0 && len(n.sdnDiscoveryTargets) == 0 {
		log.Warn("No DHT discovery targets available")
		return
	}
	if len(moduleTargets) > 0 {
		log.Infof("Module delivery discovery namespace: %s", moduleDeliveryDiscoveryNamespace)
		log.Infof("Module delivery discovery CID: %s", moduleTargets[0].String())
	}
	if n.sdnAdvertisementTarget.CID.Defined() {
		log.Infof("SDN advertisement namespace: %s", sdnAdvertisementDiscoveryNamespace)
		log.Infof("SDN advertisement flag: %s", n.sdnAdvertisementTarget.Flag)
	}

	// Announcement interval (every 30 seconds as per Agents.md spec)
	announceTicker := time.NewTicker(30 * time.Second)
	defer announceTicker.Stop()

	// Discovery ticker (find other peers every 60 seconds)
	discoveryTicker := time.NewTicker(60 * time.Second)
	defer discoveryTicker.Stop()

	// Initial announcement
	for _, target := range moduleTargets {
		n.announceOnDHT(target)
	}
	if n.sdnAdvertisementTarget.CID.Defined() {
		n.announceOnDHT(n.sdnAdvertisementTarget.CID)
	}

	for {
		select {
		case <-n.ctx.Done():
			log.Debug("DHT discovery stopped")
			return

		case <-announceTicker.C:
			for _, target := range moduleTargets {
				n.announceOnDHT(target)
			}
			if n.sdnAdvertisementTarget.CID.Defined() {
				n.announceOnDHT(n.sdnAdvertisementTarget.CID)
			}

		case <-discoveryTicker.C:
			for _, target := range moduleTargets {
				n.discoverPeers(target)
			}
			for _, target := range n.sdnDiscoveryTargets {
				n.discoverSDNAdvertisementPeers(target)
			}
		}
	}
}

func moduleDeliveryDiscoveryTargets(discoveryCID cid.Cid) []cid.Cid {
	if !discoveryCID.Defined() {
		return nil
	}
	return []cid.Cid{discoveryCID}
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
				n.enqueueAutoRelayCandidate(pi)
				log.Infof("Connected to discovered SDN peer: %s", pi.ID)
			}
		}(peerInfo)
	}
}

func (n *Node) discoverSDNAdvertisementPeers(target sdnAdvertisementDiscoveryTarget) {
	ctx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
	defer cancel()

	peerChan := n.dht.FindProvidersAsync(ctx, target.CID, 20)

	for peerInfo := range peerChan {
		if peerInfo.ID == n.host.ID() {
			continue
		}

		n.recordSDNAdvertisementPeerInfo(peerInfo, target.Flag)

		if n.host.Network().Connectedness(peerInfo.ID) == 2 {
			continue
		}

		go func(pi peer.AddrInfo, flag string) {
			connectCtx, connectCancel := context.WithTimeout(n.ctx, 10*time.Second)
			defer connectCancel()

			if err := n.host.Connect(connectCtx, pi); err != nil {
				log.Debugf("Failed to connect to discovered SDN advertisement peer %s (%s): %v", pi.ID, flag, err)
			} else {
				n.enqueueAutoRelayCandidate(pi)
				log.Infof("Connected to discovered SDN advertisement peer: %s (%s)", pi.ID, flag)
			}
		}(peerInfo, target.Flag)
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
	if n.flowManager != nil {
		n.flowManager.CloseAll()
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

// FlowManager returns the flow runtime manager, or nil if flows are disabled.
func (n *Node) FlowManager() *flowrt.FlowManager {
	return n.flowManager
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

// LogService returns the publication log service.
func (n *Node) LogService() *logservice.Service {
	return n.logService
}

// Validator returns the SDS schema validator.
func (n *Node) Validator() *sds.Validator {
	return n.validator
}

// PluginManager returns the node plugin manager.
func (n *Node) PluginManager() *plugins.Manager {
	return n.plugins
}

// Identity returns the node's HD wallet identity, or nil if using a random key.
func (n *Node) Identity() *wasm.DerivedIdentity {
	return n.identity
}

// ModuleDeliveryDiscoveryCID returns the provider-identity discovery CID.
func (n *Node) ModuleDeliveryDiscoveryCID() cid.Cid {
	return n.moduleDeliveryDiscovery
}

// PluginRegistry returns the loaded plugin registry metadata, if available.
func (n *Node) PluginRegistry() *license.PluginRegistry {
	return n.pluginRegistry
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

// EPMService returns the node's EPM service for identity card management.
func (n *Node) EPMService() *epm.Service {
	return n.epmService
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

// IdentityKeyMaterial returns the raw private key bytes used for this node's
// libp2p identity. This is used for deterministic derivations (for example, TOR
// hidden-service key material).
func (n *Node) IdentityKeyMaterial() []byte {
	if n.host == nil {
		return nil
	}
	priv := n.host.Peerstore().PrivKey(n.host.ID())
	if priv == nil {
		return nil
	}
	raw, err := priv.Raw()
	if err != nil {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}

const moduleDeliveryDiscoveryNamespace = "space-data-network/module-delivery/provider-pubkey"

func computeModuleDeliveryDiscoveryCID(providerPublicKey []byte) (cid.Cid, error) {
	if err := validateModuleDeliveryProviderPublicKey(providerPublicKey); err != nil {
		return cid.Undef, err
	}
	input := make([]byte, 0, len(moduleDeliveryDiscoveryNamespace)+len(providerPublicKey))
	input = append(input, []byte(moduleDeliveryDiscoveryNamespace)...)
	input = append(input, providerPublicKey...)

	sum := sha256.Sum256(input)
	multihash, err := mh.Encode(sum[:], mh.SHA2_256)
	if err != nil {
		return cid.Undef, fmt.Errorf("encode discovery multihash: %w", err)
	}
	return cid.NewCidV1(cid.Raw, multihash), nil
}

func computeRawCIDV1FromDigest(digest []byte) cid.Cid {
	if len(digest) != sha256.Size {
		return cid.Undef
	}
	multihash, err := mh.Encode(digest, mh.SHA2_256)
	if err != nil {
		return cid.Undef
	}
	return cid.NewCidV1(cid.Raw, multihash)
}

func compressedSecp256k1PublicKey(privKey crypto.PrivKey) ([]byte, error) {
	if privKey == nil {
		return nil, errors.New("missing private key")
	}
	pubKey := privKey.GetPublic()
	if pubKey == nil {
		return nil, errors.New("missing public key")
	}
	raw, err := pubKey.Raw()
	if err != nil {
		return nil, err
	}
	compressed, err := normalizeCompressedSecp256k1PublicKey(raw)
	if err != nil {
		return nil, err
	}
	return compressed, nil
}

func normalizeCompressedSecp256k1PublicKey(raw []byte) ([]byte, error) {
	if len(raw) != 33 {
		return nil, fmt.Errorf("expected 33-byte compressed secp256k1 public key, got %d bytes", len(raw))
	}
	pubKey, err := secp256k1.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid compressed secp256k1 pubkey: %w", err)
	}
	compressed := pubKey.SerializeCompressed()
	if len(compressed) != 33 {
		return nil, fmt.Errorf("unexpected compressed secp256k1 public key length: %d", len(compressed))
	}
	return compressed, nil
}

func validateModuleDeliveryProviderPublicKey(providerPublicKey []byte) error {
	if len(providerPublicKey) != 33 {
		return fmt.Errorf("provider public key must be 33-byte compressed secp256k1, got %d bytes", len(providerPublicKey))
	}
	if providerPublicKey[0] != 0x02 && providerPublicKey[0] != 0x03 {
		return fmt.Errorf("provider public key must use compressed secp256k1 prefix 0x02/0x03")
	}
	return nil
}
