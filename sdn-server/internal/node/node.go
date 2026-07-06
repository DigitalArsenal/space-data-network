// Package node provides the main SDN node implementation.
package node

import (
	"context"
	crypto_ecdh "crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt/capabilities"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/logservice"
	"github.com/spacedatanetwork/sdn-server/internal/metrics"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
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

	bootstrapReconnectInterval = time.Minute

	datasetPublicationCatchupInitialDelay = 15 * time.Second
	datasetPublicationCatchupInterval     = 5 * time.Minute
	datasetPublicationCatchupLimit        = 5000
	datasetShardPublicationCatchupLimit   = 5000
)

// Node represents a Space Data Network node.
type Node struct {
	host           host.Host
	dht            *dht.IpfsDHT
	pubsub         *pubsub.PubSub
	topics         map[string]*pubsub.Topic
	flatc          *wasm.FlatcModule
	hdwallet       *wasm.HDWalletModule
	identity       *wasm.DerivedIdentity // nil if using random key (no HD wallet)
	validator      *sds.Validator
	store          *storage.FlatSQLStore
	protocol       *protocol.SDSExchangeHandler
	plugins        *plugins.Manager
	epmService     *epm.Service
	directorySvc   *directory.Service
	logService     *logservice.Service
	flowManager    *flowrt.FlowManager
	mountedFlows   []*flowrt.MountedFlow
	config         *config.Config
	identityBundle *IdentityBundle

	// Trusted peer management
	peerRegistry *peers.Registry
	peerGater    *peers.TrustedConnectionGater

	pluginRegistry          *license.PluginRegistry
	licensingModule         *modulert.Module
	modulePublishAuthorizer license.ModulePublishAuthorizer
	moduleDeliveryDiscovery cid.Cid
	sdnAdvertisementTarget  sdnAdvertisementDiscoveryTarget
	sdnDiscoveryTargets     []sdnAdvertisementDiscoveryTarget
	sdnDiscoveryMu          sync.RWMutex
	sdnDiscoveryFlagsByPeer map[peer.ID]map[string]time.Time
	sdnDiscoveryAddrsByPeer map[peer.ID][]string
	epmExchangeMu           sync.Mutex
	epmExchangeLastRequest  map[peer.ID]time.Time
	autoRelayPeerChan       chan peer.AddrInfo
	datasetMaterializeMu    sync.Mutex
	datasetMaterializedPNMs map[string]time.Time

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
		epmExchangeLastRequest:  make(map[peer.ID]time.Time),
		autoRelayPeerChan:       make(chan peer.AddrInfo, 64),
		datasetMaterializedPNMs: make(map[string]time.Time),
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
		if err := upsertConfiguredTrustedPeer(n.peerRegistry, tp); err != nil {
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
	resourceManager, err := newFlatSQLSyncResourceManager()
	if err != nil {
		return fmt.Errorf("failed to create libp2p resource manager: %w", err)
	}
	hostCreated := false
	defer func() {
		if !hostCreated {
			_ = resourceManager.Close()
		}
	}()

	// Create libp2p host with connection gater for trust-based filtering
	var dhtRouting *dht.IpfsDHT
	hostOptions := append([]libp2p.Option{
		libp2p.Identity(privKey),
		libp2p.UserAgent(versioninfo.AgentVersion),
		libp2p.ListenAddrs(listenAddrs...),
	}, hostTransportOptions()...)
	n.host, err = libp2p.New(append(hostOptions,
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.Security(noise.ID, noise.New),
		libp2p.ConnectionManager(connMgr),
		libp2p.ConnectionGater(n.peerGater), // Trust-based connection gating
		libp2p.ResourceManager(resourceManager),
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
	)...)
	if err != nil {
		return fmt.Errorf("failed to create libp2p host: %w", err)
	}
	hostCreated = true
	n.dht = dhtRouting
	go n.feedAutoRelayCandidates(n.ctx)
	metrics.SetPeerCountFunc(func() int { return len(n.host.Network().Peers()) })

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
		n.store, err = storage.NewFlatSQLStore(n.config.Storage.Path, n.validator,
			storage.WithEngineHotWindow(n.config.Storage.EngineHotWindow))
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
	n.protocol.SetPubSubPNMHandler(n.handleDatasetPublicationPNM)
	n.host.SetStreamHandler(protocol.SDSProtocolID, n.protocol.HandleStream)
	if n.store != nil {
		n.host.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(n.store).HandleStream)
	}
	n.host.SetStreamHandler(protocol.IDExchangeProtoID, protocol.HandleLegacyIDExchange)
	n.host.SetStreamHandler(protocol.ChatProtoID, protocol.HandleLegacyChat)

	// Initialize EPM (Entity Profile Message) service for node identity cards.
	basePath := filepath.Dir(n.config.Storage.Path)
	storageBasePath := strings.TrimSpace(n.config.Storage.Path)
	var xpubStr string
	if n.identityBundle != nil {
		xpubStr = n.identityBundle.XPub
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
	if n.store != nil {
		n.epmService.SetProfileStore(n.store)
	}
	if err := n.epmService.Init(); err != nil {
		log.Warnf("EPM service initialization failed (non-fatal): %v", err)
	} else {
		n.epmService.RegisterProtocol(n.host)
	}
	if n.store != nil {
		n.directorySvc = directory.NewService(n.store)
		n.directorySvc.SetLocalPeerID(n.host.ID().String())
		if err := n.indexLocalNodeEPM(); err != nil {
			log.Warnf("Failed to index local node EPM: %v", err)
		}
	}
	n.host.Network().Notify(&epmExchangeNotifee{node: n})
	n.requestEPMFromConnectedPeers("peer-connect")

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
	n.licensingModule = licensingModule
	n.registerModulePublishHandler()

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
			storageHandlers := capabilities.NewStorageHandlersWithProducer(n.store, n.host.ID().String())
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

func upsertConfiguredTrustedPeer(registry *peers.Registry, configured *peers.TrustedPeer) error {
	if registry == nil || configured == nil {
		return nil
	}
	if err := registry.AddPeer(configured); err == nil {
		return nil
	} else if err != peers.ErrPeerAlreadyExists {
		return err
	}

	existing, err := registry.GetPeer(configured.ID)
	if err != nil {
		return err
	}
	existing.Addrs = configured.Addrs
	existing.AddrsStrings = configured.AddrsStrings
	existing.TrustLevel = peers.Trusted
	if strings.TrimSpace(existing.Name) == "" {
		existing.Name = configured.Name
	}
	return registry.UpdatePeer(existing)
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
	// Register dependency-first so a module's dependencies are brought up before
	// the module that composes with them (WS4.4).
	for _, pluginID := range catalogRegistrationOrder(reg) {
		pluginID = strings.TrimSpace(pluginID)
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
		// Raw-archive root + disk guardrail mirror the in-daemon ingest
		// runner's policy (loop C.8a flow ingest): raw/ lives beside the
		// store directory, guardrail floor from config ingest.min_free_disk_gb.
		basePath := filepath.Dir(n.store.Path())
		rawRoot := strings.TrimSpace(n.config.Ingest.RawPath)
		if rawRoot == "" {
			rawRoot = filepath.Join(filepath.Dir(basePath), "raw")
		}
		var minFreeDiskBytes int64
		if n.config.Ingest.MinFreeDiskGB > 0 {
			minFreeDiskBytes = int64(n.config.Ingest.MinFreeDiskGB * 1024 * 1024 * 1024)
		}
		storageFac := caps.NewStorageCapFactoryWithOptions(n.store, caps.StorageCapOptions{
			RawRoot:          rawRoot,
			MinFreeDiskBytes: minFreeDiskBytes,
		})
		reg.RegisterBridgeAware("storage_query", storageFac)
		reg.RegisterBridgeAware("storage_write", storageFac)
		reg.RegisterBridgeAware("storage_adapter", storageFac)
		reg.RegisterBridgeAware("storage_ingest", storageFac)
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

	if n.hdwallet != nil {
		bundle, err := n.loadOrCreateIdentityBundle()
		if err != nil {
			return nil, fmt.Errorf("hd wallet identity derivation failed: %w", err)
		}

		n.identity = bundle.Identity
		n.identityBundle = bundle
		info := bundle.Identity.Info()
		log.Infof("HD wallet identity derived: PeerID=%s IdentityPath=%s SigningPath=%s EncryptionPath=%s",
			info.PeerID, info.IdentityKeyPath, info.SigningKeyPath, info.EncryptionKeyPath)

		if repoPath := strings.TrimSpace(os.Getenv("IPFS_PATH")); repoPath != "" {
			if err := EnsureManagedIPFSRepoIdentity(repoPath, bundle); err != nil {
				return nil, fmt.Errorf("managed IPFS repo identity sync: %w", err)
			}
		}

		// Also save the serialized key for backward compatibility
		keyData, err := bundle.Identity.MarshalPrivateKey()
		if err == nil {
			_ = os.WriteFile(keyPath, keyData, 0600)
		}

		// Return secp256k1 identity key for libp2p PeerID
		return bundle.Identity.IdentityPrivKey, nil
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

// usingDerivedKeyPassword reports whether the mnemonic key password comes from
// the machine-derived default (no explicit SDN_KEY_PASSWORD / config password).
// Legacy-key migration is only attempted in that case.
func (n *Node) usingDerivedKeyPassword() bool {
	return os.Getenv("SDN_KEY_PASSWORD") == "" && n.config.Security.KeyPassword == ""
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
	if layout := bundle.ResolveCurrent(); layout.HDWalletWASM != "" {
		if _, err := os.Stat(layout.HDWalletWASM); err == nil {
			return layout.HDWalletWASM
		}
	}
	// Look for the pure WASI wallet artifact. The browser hd-wallet.wasm package
	// artifact imports Emscripten JS glue and cannot be used by Go/WASI hosts.
	paths := []string{
		"sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"/opt/spacedatanetwork/wasm/hd-wallet-wasi.wasm",
		"/usr/local/lib/hd-wallet-wasi.wasm",
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
		// Canonical, actively-maintained licensing/core (carries the PKI xpub gate:
		// ALLOWED_XPUBS enforcement + EPM verification). Prefer it over the legacy
		// space-data-network-plugins copy, which is deprecated and ungated.
		"../space-data-network-modules/licensing/core/dist/isomorphic/module.wasm",
		"../../space-data-network-modules/licensing/core/dist/isomorphic/module.wasm",
		"../../packages/space-data-network-modules/licensing/core/dist/isomorphic/module.wasm",
		// Deprecated fallback (stale repo; ungated) — kept only for older local layouts.
		"../space-data-network-plugins/licensing/core/dist/isomorphic/module.wasm",
		"../../space-data-network-plugins/licensing/core/dist/isomorphic/module.wasm",
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
				n.requestConnectedPeerEPM(peerInfo.AddrInfo.ID, "bootstrap")
				log.Infof("Connected to bootstrap peer %s (peer ID verified)", peerInfo.AddrInfo.ID)
			}
		}(p)
	}

	// Keep bootstrap connectivity alive: the startup dials above run once, so
	// a healed network partition would otherwise leave the node isolated
	// until restart (catch-up loops only consult currently connected peers).
	n.wg.Add(1)
	go n.maintainBootstrapConnections(pinnedPeers)

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

		if n.store != nil {
			feedTopicName := sdnpubsub.DatasetFeedHeadTopic(schema)
			feedTopic, err := n.pubsub.Join(feedTopicName)
			if err != nil {
				log.Warnf("Failed to join dataset feed-head topic %s: %v", feedTopicName, err)
				continue
			}
			n.topics[feedTopicName] = feedTopic
			feedSub, err := feedTopic.Subscribe()
			if err != nil {
				log.Warnf("Failed to subscribe to dataset feed-head topic %s: %v", feedTopicName, err)
				continue
			}
			n.wg.Add(1)
			go n.handleDatasetFeedHeadSubscription(feedSub, schema)
		}
	}

	// Start mDNS discovery
	n.wg.Add(1)
	go n.runMDNS()

	// Announce on DHT with custom discovery namespace
	n.wg.Add(1)
	go n.runDHTDiscovery()

	// Replay recently stored dataset PNMs so subscribers recover when they
	// receive the general PNM announcement but miss a schema-topic burst.
	if n.store != nil {
		n.wg.Add(1)
		go n.runDatasetPublicationPNMCatchup()
		n.wg.Add(1)
		go n.runDatasetShardPublicationCatchup()
	}

	// Start EPM auto-publish via PubSub (every 30 minutes)
	if n.epmService != nil && n.epmService.GetNodeEPM() != nil {
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			n.epmService.StartAutoPublish(n.ctx, n, 30*time.Minute)
		}()
	}

	// In-daemon ingest workers (config ingest.enabled): the single-writer
	// replacement for the separate spacedatanetwork-ingest.service process
	// (loop C.6b, node/ingest.go).
	if err := n.startInDaemonIngest(); err != nil {
		return err
	}

	if err := n.startFlowServices(); err != nil {
		return err
	}

	return nil
}

// startFlowServices loads the config-declared timer-served flows (loop C.8a
// ingest-as-flow) and registers them with the plugin manager so the cron
// scheduler drives their timer triggers. The host contributes timers +
// capability hostcalls only; every ingest decision lives in the wasm flow.
func (n *Node) startFlowServices() error {
	services := n.config.Flows.Services
	if len(services) == 0 {
		return nil
	}
	nodeCtx, err := n.buildModuleNodeContext()
	if err != nil {
		return fmt.Errorf("build module node context: %w", err)
	}
	deps := flowrt.FlowMountDeps{
		CapRegistry:    n.buildCapRegistry(),
		NodeCtx:        nodeCtx,
		MaxMemoryPages: n.config.Flows.MaxMemoryPages,
		AOTCacheDir:    flatsqldrv.DefaultAOTCacheDir(),
	}
	if n.flowManager != nil {
		deps.Store = n.flowManager.Store()
	}
	loaded, err := flowrt.LoadFlowServices(services, deps)
	if err != nil {
		return fmt.Errorf("load flow services: %w", err)
	}
	for _, sf := range loaded {
		if err := n.plugins.Register(sf); err != nil {
			return fmt.Errorf("register flow service %q: %w", sf.ID(), err)
		}
	}
	return nil
}

func (n *Node) runDatasetPublicationPNMCatchup() {
	defer n.wg.Done()

	timer := time.NewTimer(datasetPublicationCatchupInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-timer.C:
			if materialized, err := n.materializeStoredDatasetPublicationPNMs(n.ctx, datasetPublicationCatchupLimit); err != nil {
				log.Warnf("Dataset publication PNM catch-up completed with errors after materializing %d update(s): %v", materialized, err)
			} else if materialized > 0 {
				log.Infof("Dataset publication PNM catch-up materialized %d update(s)", materialized)
			}
			timer.Reset(datasetPublicationCatchupInterval)
		}
	}
}

// maintainBootstrapConnections periodically re-dials pinned bootstrap peers
// that are not currently connected, so connectivity recovers automatically
// after a network partition heals instead of waiting for a restart.
func (n *Node) maintainBootstrapConnections(pinnedPeers []bootstrap.PeerInfo) {
	defer n.wg.Done()

	ticker := time.NewTicker(bootstrapReconnectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			for _, peerInfo := range pinnedPeers {
				if peerInfo.AddrInfo.ID == n.host.ID() {
					continue
				}
				if n.host.Network().Connectedness(peerInfo.AddrInfo.ID) == network.Connected {
					continue
				}
				dialCtx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
				err := n.host.Connect(dialCtx, peerInfo.AddrInfo)
				cancel()
				if err != nil {
					log.Debugf("Bootstrap reconnect to %s failed: %v", peerInfo.AddrInfo.ID, err)
					continue
				}
				n.enqueueAutoRelayCandidate(peerInfo.AddrInfo)
				n.requestConnectedPeerEPM(peerInfo.AddrInfo.ID, "bootstrap-reconnect")
				log.Infof("Reconnected to bootstrap peer %s (peer ID verified)", peerInfo.AddrInfo.ID)
			}
		}
	}
}

func (n *Node) runDatasetShardPublicationCatchup() {
	defer n.wg.Done()

	timer := time.NewTimer(datasetPublicationCatchupInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-timer.C:
			if materialized, err := n.catchUpDatasetShardPublicationsFromTrustedPeers(n.ctx); err != nil {
				log.Warnf("Dataset shard publication catch-up completed with errors after materializing %d shard(s): %v", materialized, err)
			} else if materialized > 0 {
				log.Infof("Dataset shard publication catch-up materialized %d shard(s)", materialized)
			}
			timer.Reset(datasetPublicationCatchupInterval)
		}
	}
}

func (n *Node) catchUpDatasetShardPublicationsFromTrustedPeers(ctx context.Context) (int, error) {
	if n == nil || n.host == nil || n.store == nil || n.peerRegistry == nil {
		return 0, nil
	}
	var total int
	var errs []error
	for _, id := range n.host.Network().Peers() {
		if !n.peerRegistry.IsTrusted(id) {
			continue
		}
		materialized, err := n.catchUpDatasetShardPublicationsFromPeer(ctx, id)
		total += materialized
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id.ShortString(), err))
		}
	}
	return total, errors.Join(errs...)
}

func (n *Node) catchUpDatasetShardPublicationsFromPeer(ctx context.Context, from peer.ID) (int, error) {
	if n == nil || n.host == nil || n.store == nil || n.validator == nil || n.peerRegistry == nil {
		return 0, nil
	}
	if !n.peerRegistry.IsTrusted(from) {
		return 0, nil
	}
	var total int
	var errs []error
	for _, schema := range n.validator.Schemas() {
		publications, err := n.listRemoteDatasetShardPublications(ctx, from, schema)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: list %s publications: %w", from.ShortString(), schema, err))
			continue
		}
		for _, publication := range publications {
			if n.datasetShardPublicationAlreadyCached(publication) {
				continue
			}
			imported, err := n.materializeDatasetFeedHeadAnnouncement(ctx, datasetShardPublicationAnnouncement(publication), from)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: materialize %s %s: %w", from.ShortString(), schema, publication.ShardCID, err))
				continue
			}
			if imported > 0 {
				total++
			}
		}
	}
	return total, errors.Join(errs...)
}

func (n *Node) datasetShardPublicationAlreadyCached(publication storage.DatasetShardPublication) bool {
	if n == nil || n.store == nil {
		return false
	}
	existing, found, err := n.store.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   publication.SchemaName,
		ProviderID:   publication.ProviderID,
		SourceName:   publication.SourceName,
		BatchID:      publication.BatchID,
		QueryProfile: publication.QueryProfile,
		Offset:       publication.Offset,
		Limit:        publication.Limit,
	})
	return err == nil && found && existing.ShardCID == publication.ShardCID && existing.IndexCID == publication.IndexCID
}

type datasetShardPublicationListResponse struct {
	Op                    string                               `json:"op"`
	Status                string                               `json:"status"`
	Schema                string                               `json:"schema"`
	SyncProtocol          string                               `json:"sync_protocol"`
	PublicationOffset     int                                  `json:"publication_offset"`
	PublicationCount      int                                  `json:"publication_count"`
	TotalPublicationCount int                                  `json:"total_publication_count"`
	Publications          []datasetShardPublicationListItemDTO `json:"publications"`
	Error                 *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type datasetShardPublicationListItemDTO struct {
	Schema       string    `json:"schema"`
	ProviderID   string    `json:"provider_id"`
	SourceName   string    `json:"source_name"`
	BatchID      string    `json:"batch_id"`
	QueryProfile string    `json:"query_profile"`
	Offset       int       `json:"offset"`
	Limit        int       `json:"limit"`
	RecordCount  int       `json:"record_count"`
	ByteCount    int64     `json:"byte_count"`
	ShardCID     string    `json:"shard_cid"`
	IndexCID     string    `json:"index_cid"`
	ManifestCID  string    `json:"manifest_cid"`
	PNMCID       string    `json:"pnm_cid"`
	ShardSHA256  string    `json:"shard_sha256"`
	IndexSHA256  string    `json:"index_sha256"`
	QuerySHA256  string    `json:"query_sha256"`
	ResultSHA256 string    `json:"result_sha256"`
	FeedSequence int64     `json:"feed_sequence"`
	PreviousHead string    `json:"previous_head"`
	FeedHead     string    `json:"feed_head"`
	PublishedAt  time.Time `json:"published_at"`
}

func (n *Node) listRemoteDatasetShardPublications(ctx context.Context, from peer.ID, schema string) ([]storage.DatasetShardPublication, error) {
	var publications []storage.DatasetShardPublication
	for offset := 0; ; {
		page, err := n.listRemoteDatasetShardPublicationPage(ctx, from, schema, offset, datasetShardPublicationCatchupLimit)
		if err != nil {
			return publications, err
		}
		for _, item := range page.Publications {
			publications = append(publications, datasetShardPublicationFromListItem(schema, item))
		}
		nextOffset := page.PublicationOffset + page.PublicationCount
		if page.PublicationCount == 0 || nextOffset >= page.TotalPublicationCount {
			return publications, nil
		}
		offset = nextOffset
	}
}

func (n *Node) listRemoteDatasetShardPublicationPage(ctx context.Context, from peer.ID, schema string, offset int, limit int) (datasetShardPublicationListResponse, error) {
	var response datasetShardPublicationListResponse
	stream, err := n.host.NewStream(ctx, from, protocol.FlatSQLSyncProtocolID)
	if err != nil {
		return response, err
	}
	defer stream.Close()
	if err := protocol.WriteFlatSQLSyncJSONFrame(stream, map[string]interface{}{
		"op":                 "list_published_shards",
		"schema":             schema,
		"query_profile":      storage.DatasetPublicationQueryProfile,
		"publication_offset": offset,
		"publication_limit":  limit,
	}); err != nil {
		return response, err
	}
	if err := protocol.ReadFlatSQLSyncJSONFrame(stream, datasync.StreamRequestMaxBytes, &response); err != nil {
		return response, err
	}
	if response.Status != "ok" {
		if response.Error != nil && response.Error.Message != "" {
			return response, errors.New(response.Error.Message)
		}
		return response, fmt.Errorf("list published shards returned status %q", response.Status)
	}
	if response.Op != "list_published_shards" || response.SyncProtocol != protocol.FlatSQLSyncProtocolID {
		return response, fmt.Errorf("unexpected list published shards response: %+v", response)
	}
	return response, nil
}

func datasetShardPublicationFromListItem(schema string, item datasetShardPublicationListItemDTO) storage.DatasetShardPublication {
	schemaName := strings.TrimSpace(item.Schema)
	if schemaName == "" {
		schemaName = schema
	}
	return storage.DatasetShardPublication{
		SchemaName:   schemaName,
		ProviderID:   strings.TrimSpace(item.ProviderID),
		SourceName:   strings.TrimSpace(item.SourceName),
		BatchID:      strings.TrimSpace(item.BatchID),
		QueryProfile: strings.TrimSpace(item.QueryProfile),
		Offset:       item.Offset,
		Limit:        item.Limit,
		RecordCount:  item.RecordCount,
		ByteCount:    item.ByteCount,
		ShardCID:     strings.TrimSpace(item.ShardCID),
		IndexCID:     strings.TrimSpace(item.IndexCID),
		ManifestCID:  strings.TrimSpace(item.ManifestCID),
		PNMCID:       strings.TrimSpace(item.PNMCID),
		ShardSHA256:  strings.TrimSpace(item.ShardSHA256),
		IndexSHA256:  strings.TrimSpace(item.IndexSHA256),
		QuerySHA256:  strings.TrimSpace(item.QuerySHA256),
		ResultSHA256: strings.TrimSpace(item.ResultSHA256),
		FeedSequence: item.FeedSequence,
		PreviousHead: strings.TrimSpace(item.PreviousHead),
		FeedHead:     strings.TrimSpace(item.FeedHead),
		PublishedAt:  item.PublishedAt,
	}
}

func datasetShardPublicationAnnouncement(publication storage.DatasetShardPublication) sdnpubsub.DatasetFeedHeadAnnouncement {
	return sdnpubsub.DatasetFeedHeadAnnouncement{
		MessageType:  sdnpubsub.DatasetFeedHeadMessageType,
		Schema:       publication.SchemaName,
		ProviderID:   publication.ProviderID,
		SourceName:   publication.SourceName,
		BatchID:      publication.BatchID,
		QueryProfile: publication.QueryProfile,
		Offset:       publication.Offset,
		Limit:        publication.Limit,
		FeedSequence: publication.FeedSequence,
		PreviousHead: publication.PreviousHead,
		FeedHead:     publication.FeedHead,
		RecordCount:  publication.RecordCount,
		ByteCount:    publication.ByteCount,
		ShardCID:     publication.ShardCID,
		IndexCID:     publication.IndexCID,
		ManifestCID:  publication.ManifestCID,
		PNMCID:       publication.PNMCID,
		PublishedAt:  publication.PublishedAt,
	}
}

func (n *Node) materializeStoredDatasetPublicationPNMs(ctx context.Context, limit int) (int, error) {
	if n == nil || n.store == nil || n.peerRegistry == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = datasetPublicationCatchupLimit
	}
	records, err := n.store.QueryRecentRecords("PNM.fbs", limit)
	if err != nil {
		return 0, fmt.Errorf("query stored PNM records: %w", err)
	}
	records = n.catchupCandidateDatasetPublicationPNMs(records)
	materialized := 0
	var firstErr error
	for _, record := range records {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				break
			}
		}
		if record == nil || len(record.Data) == 0 || !PNM.SizePrefixedPNMBufferHasIdentifier(record.Data) {
			continue
		}
		pnm := PNM.GetSizePrefixedRootAsPNM(record.Data, 0)
		schema := datasetPublicationFileIDSchema(string(pnm.FILE_ID()))
		if schema == "" {
			continue
		}
		didMaterialize, err := n.materializeStoredDatasetPublicationPNM(ctx, schema, record)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Warnf("Stored dataset PNM catch-up failed on %s: %v", schema, err)
			continue
		}
		if didMaterialize {
			materialized++
		}
	}
	return materialized, firstErr
}

func (n *Node) catchupCandidateDatasetPublicationPNMs(records []*storage.Record) []*storage.Record {
	if len(records) == 0 {
		return records
	}
	candidates := records[:0]
	for _, record := range records {
		if n.storedDatasetPublicationPNMIsSelfOwned(record) {
			continue
		}
		candidates = append(candidates, record)
	}
	return latestDatasetPublicationPNMBatches(candidates)
}

func (n *Node) storedDatasetPublicationPNMIsSelfOwned(record *storage.Record) bool {
	if n == nil || n.host == nil || record == nil {
		return false
	}
	return strings.TrimSpace(record.PeerID) == n.host.ID().String()
}

func (n *Node) materializeStoredDatasetPublicationPNM(ctx context.Context, schema string, record *storage.Record) (bool, error) {
	var lastSignerMismatch error
	for _, from := range n.datasetPublicationSignerCandidates(strings.TrimSpace(record.PeerID)) {
		if !n.peerRegistry.IsTrusted(from) {
			continue
		}
		didMaterialize, err := n.materializeDatasetPublicationPNM(ctx, schema, record.Data, from)
		if err == nil {
			return didMaterialize, nil
		}
		if isDatasetPublicationSignerMismatch(err) {
			lastSignerMismatch = err
			continue
		}
		return false, fmt.Errorf("from %s: %w", from.ShortString(), err)
	}
	if lastSignerMismatch != nil {
		return false, lastSignerMismatch
	}
	return false, nil
}

func (n *Node) datasetPublicationSignerCandidates(storedPeerID string) []peer.ID {
	seen := make(map[peer.ID]bool)
	candidates := make([]peer.ID, 0, 4)
	if storedPeerID != "" {
		if id, err := peer.Decode(storedPeerID); err == nil {
			candidates = append(candidates, id)
			seen[id] = true
		}
	}
	if n == nil || n.peerRegistry == nil {
		return candidates
	}
	for _, trustedPeer := range n.peerRegistry.ListPeers() {
		if trustedPeer == nil || !n.peerRegistry.IsTrusted(trustedPeer.ID) || seen[trustedPeer.ID] {
			continue
		}
		candidates = append(candidates, trustedPeer.ID)
		seen[trustedPeer.ID] = true
	}
	return candidates
}

func isDatasetPublicationSignerMismatch(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "invalid PNM signature") ||
		strings.Contains(msg, "no Ed25519 signing key found")
}

func isPermanentDatasetPublicationMaterializationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"buffer missing identifier",
		"does not match DPM FILE_ID",
		"DPM missing",
		"dataset export index",
		"offset/length outside shard",
		"frame length",
		"record CID mismatch",
		"SHA-256 does not match",
		"replayed result bytes do not match",
		"replayed result hash",
		"invalid PNM signature",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
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

func (n *Node) handleDatasetFeedHeadSubscription(sub *pubsub.Subscription, schema string) {
	defer n.wg.Done()

	for {
		msg, err := sub.Next(n.ctx)
		if err != nil {
			if n.ctx.Err() != nil {
				return
			}
			log.Warnf("Error reading dataset feed-head subscription %s: %v", schema, err)
			continue
		}
		if msg.ReceivedFrom == n.host.ID() {
			continue
		}
		ann, err := sdnpubsub.ParseDatasetFeedHeadAnnouncement(msg.Data)
		if err != nil {
			log.Warnf("Invalid dataset feed-head announcement on %s from %s: %v", schema, msg.ReceivedFrom.ShortString(), err)
			continue
		}
		if ann.Signature != "" {
			// Tampered or malformed signatures are always rejected.
			if err := sdnpubsub.VerifyDatasetFeedHead(ann, nil); err != nil {
				log.Warnf("Rejecting dataset feed head on %s from %s: %v", schema, msg.ReceivedFrom.ShortString(), err)
				continue
			}
		} else if n.config != nil && n.config.Security.RequireSignedFeedHeads {
			log.Warnf("Rejecting unsigned dataset feed head on %s from %s (security.require_signed_feed_heads)", schema, msg.ReceivedFrom.ShortString())
			continue
		}
		if ann.Schema != schema {
			log.Debugf("Skipping dataset feed-head announcement on %s for schema %s", schema, ann.Schema)
			continue
		}
		imported, err := n.materializeDatasetFeedHeadAnnouncement(n.ctx, ann, msg.ReceivedFrom)
		if err != nil {
			log.Warnf("Failed to materialize dataset feed head %s from %s on %s: %v", ann.FeedHead, msg.ReceivedFrom.ShortString(), schema, err)
			continue
		}
		if imported > 0 {
			log.Infof("Materialized dataset feed head %s from %s on %s: imported=%d", ann.FeedHead, msg.ReceivedFrom.ShortString(), schema, imported)
		}
	}
}

func (n *Node) handleDatasetPublicationPNM(ctx context.Context, schema string, pnmBytes []byte, from peer.ID) error {
	_, err := n.materializeDatasetPublicationPNM(ctx, schema, pnmBytes, from)
	return err
}

func (n *Node) materializeDatasetFeedHeadAnnouncement(ctx context.Context, ann sdnpubsub.DatasetFeedHeadAnnouncement, from peer.ID) (int, error) {
	if n == nil || n.store == nil || n.host == nil || n.peerRegistry == nil {
		return 0, nil
	}
	if !n.peerRegistry.IsTrusted(from) {
		log.Debugf("Skipping dataset feed head from non-trusted peer %s on %s", from.ShortString(), ann.Schema)
		return 0, nil
	}
	if strings.TrimSpace(ann.ShardCID) == "" || strings.TrimSpace(ann.IndexCID) == "" {
		return 0, fmt.Errorf("dataset feed head %s is missing shard or index CID", ann.FeedHead)
	}
	if ann.Limit <= 0 {
		return 0, fmt.Errorf("dataset feed head %s is missing positive window limit", ann.FeedHead)
	}
	baseDir := strings.TrimSpace(n.config.Storage.Path)
	if baseDir == "" {
		baseDir = os.TempDir()
	}
	workRoot := filepath.Join(baseDir, "dataset-feed-head-sync")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return 0, fmt.Errorf("create feed-head work root: %w", err)
	}
	workDir, err := os.MkdirTemp(workRoot, "feed-head-*")
	if err != nil {
		return 0, fmt.Errorf("create feed-head work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	shardPath := filepath.Join(workDir, "shard.fbshard")
	indexPath := filepath.Join(workDir, "index.json")
	shardHeader, err := n.fetchDatasetFeedHeadAssetToFile(ctx, ann, from, ann.ShardCID, "shard", shardPath)
	if err != nil {
		return 0, err
	}
	indexHeader, err := n.fetchDatasetFeedHeadAssetToFile(ctx, ann, from, ann.IndexCID, "index", indexPath)
	if err != nil {
		return 0, err
	}
	imported, index, err := n.store.ImportDatasetShardFromFiles(shardPath, indexPath, from.String())
	if err != nil {
		return 0, fmt.Errorf("import dataset feed head %s: %w", ann.FeedHead, err)
	}
	if index == nil {
		return imported, fmt.Errorf("import dataset feed head %s returned no index", ann.FeedHead)
	}
	pub := datasetShardPublicationFromFeedHead(ann, shardHeader, indexHeader, index)
	if err := n.cacheDatasetFeedHeadPublicationFiles(pub, shardPath, indexPath); err != nil {
		return imported, err
	}
	if err := n.store.UpsertDatasetShardPublication(pub); err != nil {
		return imported, fmt.Errorf("record replicated dataset shard publication %s: %w", ann.FeedHead, err)
	}
	return imported, nil
}

func datasetShardPublicationFromFeedHead(ann sdnpubsub.DatasetFeedHeadAnnouncement, shardHeader, indexHeader datasetFeedHeadAssetHeader, index *storage.DatasetExportIndex) storage.DatasetShardPublication {
	pub := storage.DatasetShardPublication{
		SchemaName:   ann.Schema,
		ProviderID:   ann.ProviderID,
		SourceName:   ann.SourceName,
		BatchID:      ann.BatchID,
		QueryProfile: ann.QueryProfile,
		Offset:       ann.Offset,
		Limit:        ann.Limit,
		RecordCount:  ann.RecordCount,
		ByteCount:    ann.ByteCount,
		ShardCID:     ann.ShardCID,
		IndexCID:     ann.IndexCID,
		ManifestCID:  ann.ManifestCID,
		PNMCID:       ann.PNMCID,
		ShardSHA256:  shardHeader.SHA256,
		IndexSHA256:  indexHeader.SHA256,
		FeedSequence: ann.FeedSequence,
		PreviousHead: ann.PreviousHead,
		FeedHead:     ann.FeedHead,
		PublishedAt:  ann.PublishedAt,
	}
	if index != nil {
		if pub.ProviderID == "" {
			pub.ProviderID = index.ProviderID
		}
		if pub.SourceName == "" {
			pub.SourceName = index.SourceName
		}
		if pub.BatchID == "" {
			pub.BatchID = index.BatchID
		}
		if pub.RecordCount <= 0 {
			pub.RecordCount = index.RecordCount
		}
		if pub.ShardSHA256 == "" {
			pub.ShardSHA256 = index.ShardSHA256
		}
		pub.QuerySHA256 = index.QuerySHA256
		pub.ResultSHA256 = index.ResultSHA256
	}
	if pub.ByteCount <= 0 {
		pub.ByteCount = shardHeader.ByteCount
	}
	if pub.PublishedAt.IsZero() {
		pub.PublishedAt = time.Now().UTC()
	}
	return pub
}

func (n *Node) cacheDatasetFeedHeadPublicationFiles(pub storage.DatasetShardPublication, shardPath, indexPath string) error {
	shardOut, err := n.store.DatasetPublicationShardPath(pub)
	if err != nil {
		return fmt.Errorf("resolve replicated dataset shard path: %w", err)
	}
	indexOut, err := n.store.DatasetPublicationIndexPath(pub)
	if err != nil {
		return fmt.Errorf("resolve replicated dataset index path: %w", err)
	}
	if err := copyDatasetFeedHeadAssetFile(shardPath, shardOut); err != nil {
		return fmt.Errorf("cache replicated dataset shard: %w", err)
	}
	if err := copyDatasetFeedHeadAssetFile(indexPath, indexOut); err != nil {
		return fmt.Errorf("cache replicated dataset index: %w", err)
	}
	return nil
}

func copyDatasetFeedHeadAssetFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return err
	}
	cleanup = false
	return nil
}

type datasetFeedHeadAssetHeader struct {
	Op             string `json:"op"`
	Status         string `json:"status"`
	Schema         string `json:"schema"`
	Role           string `json:"role"`
	CID            string `json:"cid"`
	ByteCount      int64  `json:"byte_count"`
	SHA256         string `json:"sha256"`
	SyncProtocol   string `json:"sync_protocol"`
	ImmutableBytes bool   `json:"immutable_bytes"`
	Error          struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (n *Node) fetchDatasetFeedHeadAssetToFile(ctx context.Context, ann sdnpubsub.DatasetFeedHeadAnnouncement, from peer.ID, cidValue string, role string, outputPath string) (datasetFeedHeadAssetHeader, error) {
	var header datasetFeedHeadAssetHeader
	stream, err := n.host.NewStream(ctx, from, protocol.FlatSQLSyncProtocolID)
	if err != nil {
		return header, fmt.Errorf("open FlatSQL sync stream to %s: %w", from.ShortString(), err)
	}
	defer stream.Close()
	if err := protocol.WriteFlatSQLSyncJSONFrame(stream, map[string]interface{}{
		"op":            "read_published_asset",
		"schema":        ann.Schema,
		"provider_id":   ann.ProviderID,
		"source_name":   ann.SourceName,
		"batch_id":      ann.BatchID,
		"query_profile": ann.QueryProfile,
		"cid":           cidValue,
		"role":          role,
	}); err != nil {
		return header, fmt.Errorf("request published %s asset %s: %w", role, cidValue, err)
	}
	if err := protocol.ReadFlatSQLSyncJSONFrame(stream, datasync.StreamRequestMaxBytes, &header); err != nil {
		return header, fmt.Errorf("read published %s asset header %s: %w", role, cidValue, err)
	}
	if header.Status == "error" {
		return header, fmt.Errorf("provider rejected published %s asset %s: %s", role, cidValue, header.Error.Message)
	}
	if header.Op != "read_published_asset" || header.Status != "ok" || header.Role != role || header.CID != cidValue || header.SyncProtocol != protocol.FlatSQLSyncProtocolID || !header.ImmutableBytes {
		return header, fmt.Errorf("published %s asset header mismatch: %+v", role, header)
	}
	if header.Schema != ann.Schema {
		return header, fmt.Errorf("published %s asset schema = %q, want %q", role, header.Schema, ann.Schema)
	}
	if header.ByteCount <= 0 {
		return header, fmt.Errorf("published %s asset %s has invalid byte count %d", role, cidValue, header.ByteCount)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return header, fmt.Errorf("create published %s asset dir: %w", role, err)
	}
	file, err := os.Create(outputPath)
	if err != nil {
		return header, fmt.Errorf("create published %s asset file: %w", role, err)
	}
	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(file, hash), stream, header.ByteCount)
	closeErr := file.Close()
	if copyErr != nil {
		return header, fmt.Errorf("read published %s asset %s bytes: %w", role, cidValue, copyErr)
	}
	if closeErr != nil {
		return header, fmt.Errorf("close published %s asset file: %w", role, closeErr)
	}
	if written != header.ByteCount {
		return header, fmt.Errorf("published %s asset %s bytes = %d, want %d", role, cidValue, written, header.ByteCount)
	}
	actualSHA := hex.EncodeToString(hash.Sum(nil))
	if strings.TrimSpace(header.SHA256) != "" && actualSHA != header.SHA256 {
		return header, fmt.Errorf("published %s asset %s SHA-256 = %s, want %s", role, cidValue, actualSHA, header.SHA256)
	}
	return header, nil
}

func (n *Node) materializeDatasetPublicationPNM(ctx context.Context, schema string, pnmBytes []byte, from peer.ID) (bool, error) {
	if schema == "PNM.fbs" {
		return false, nil
	}
	if n == nil || n.store == nil || n.peerRegistry == nil {
		return false, nil
	}
	if !n.peerRegistry.IsTrusted(from) {
		log.Debugf("Skipping dataset PNM materialization from non-trusted peer %s on %s", from.ShortString(), schema)
		return false, nil
	}
	ipfsAPIURL := strings.TrimSpace(n.config.Admin.IPFSAPIURL)
	if ipfsAPIURL == "" {
		return false, fmt.Errorf("trusted dataset PNM received from %s but admin.ipfs_api_url is not configured", from.ShortString())
	}
	pnm := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	if publicationSchema := datasetPublicationFileIDSchema(string(pnm.FILE_ID())); publicationSchema != "" && publicationSchema != schema {
		log.Debugf("Skipping dataset PNM materialization from %s on %s: FILE_ID schema is %s", from.ShortString(), schema, publicationSchema)
		return false, nil
	}
	pnmCID := strings.TrimSpace(string(pnm.CID()))
	fileID := strings.TrimSpace(string(pnm.FILE_ID()))
	pnmKey := pnmCID + "\x00" + fileID
	if pnmKey == "\x00" {
		return false, nil
	}
	replayState, found, err := n.store.DatasetPublicationReplayState(pnmKey)
	if err != nil {
		return false, fmt.Errorf("read dataset publication replay state: %w", err)
	}
	if found {
		switch replayState.State {
		case storage.DatasetPublicationReplayStateMaterialized, storage.DatasetPublicationReplayStatePermanentError:
			return false, nil
		}
	}
	if n.datasetPNMAlreadyMaterialized(pnmKey) {
		return false, nil
	}

	providerPublicKey, err := n.datasetPublicationPublicKey(ctx, from)
	if err != nil {
		return false, fmt.Errorf("dataset provider public key unavailable for %s: %w", from.ShortString(), err)
	}
	workDir := filepath.Join(n.config.Storage.Path, "dataset-publication-replay")
	materializeCtx, cancel := context.WithTimeout(n.ctx, 2*time.Minute)
	defer cancel()
	result, err := storage.MaterializeDatasetPublication(materializeCtx, n.store, storage.DatasetPublicationReplayOptions{
		PNM:               pnmBytes,
		ProviderPublicKey: providerPublicKey,
		FetchByCID: func(ctx context.Context, cid string) ([]byte, error) {
			return storage.FetchIPFSBlockByCID(ctx, ipfsAPIURL, cid)
		},
		FetchByCIDToFile: func(ctx context.Context, cid string, path string) error {
			return storage.FetchIPFSBlockByCIDToFile(ctx, ipfsAPIURL, cid, path)
		},
		FetchRetryDelays: []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second},
		WorkDir:          workDir,
	})
	if err != nil {
		if isPermanentDatasetPublicationMaterializationError(err) {
			if stateErr := n.store.UpsertDatasetPublicationReplayState(storage.DatasetPublicationReplayState{
				PNMKey:     pnmKey,
				SchemaName: schema,
				PNMCID:     pnmCID,
				FileID:     fileID,
				State:      storage.DatasetPublicationReplayStatePermanentError,
				Error:      err.Error(),
			}); stateErr != nil {
				return false, fmt.Errorf("%w; record dataset publication replay state: %v", err, stateErr)
			}
		} else {
			n.clearDatasetPNMMaterialized(pnmKey)
		}
		return false, err
	}
	if err := n.store.UpsertDatasetPublicationReplayState(storage.DatasetPublicationReplayState{
		PNMKey:     pnmKey,
		SchemaName: result.SchemaName,
		PNMCID:     pnmCID,
		FileID:     fileID,
		State:      storage.DatasetPublicationReplayStateMaterialized,
	}); err != nil {
		log.Warnf("Failed to record dataset publication replay state for %s on %s: %v", from.ShortString(), schema, err)
	}
	log.Infof("Materialized trusted dataset update from %s on %s: schema=%s imported=%d manifest=%s shard=%s",
		from.ShortString(), schema, result.SchemaName, result.Imported, result.ManifestCID, result.ShardCID)
	return true, nil
}

func datasetPublicationFileIDSchema(fileID string) string {
	for _, part := range strings.Split(fileID, ":") {
		part = strings.TrimSpace(part)
		if strings.HasSuffix(part, ".fbs") {
			return part
		}
	}
	return ""
}

func latestDatasetPublicationPNMBatches(records []*storage.Record) []*storage.Record {
	type batchKey struct {
		dataset string
		schema  string
	}
	type batchChoice struct {
		batchID string
		seenAt  time.Time
	}
	latest := map[batchKey]batchChoice{}
	hasFullDataset := map[string]bool{}
	recordMeta := make(map[*storage.Record]struct {
		key     batchKey
		batchID string
	})
	for _, record := range records {
		if record == nil || len(record.Data) == 0 || !PNM.SizePrefixedPNMBufferHasIdentifier(record.Data) {
			continue
		}
		pnm := PNM.GetSizePrefixedRootAsPNM(record.Data, 0)
		dataset, schema, batchID := datasetPublicationFileIDParts(string(pnm.FILE_ID()))
		if dataset == "" || schema == "" || batchID == "" {
			continue
		}
		if strings.Contains(dataset, "-full") {
			hasFullDataset[schema] = true
		}
		key := batchKey{dataset: dataset, schema: schema}
		recordMeta[record] = struct {
			key     batchKey
			batchID string
		}{key: key, batchID: batchID}
		if existing, ok := latest[key]; !ok || record.Timestamp.After(existing.seenAt) {
			latest[key] = batchChoice{batchID: batchID, seenAt: record.Timestamp}
		}
	}
	if len(latest) == 0 {
		return records
	}
	filtered := make([]*storage.Record, 0, len(records))
	for _, record := range records {
		meta, ok := recordMeta[record]
		if !ok {
			filtered = append(filtered, record)
			continue
		}
		if hasFullDataset[meta.key.schema] && !strings.Contains(meta.key.dataset, "-full") {
			continue
		}
		if latest[meta.key].batchID == meta.batchID {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func datasetPublicationFileIDParts(fileID string) (dataset string, schema string, batchID string) {
	parts := strings.Split(fileID, ":")
	if len(parts) < 3 {
		return "", datasetPublicationFileIDSchema(fileID), ""
	}
	dataset = strings.TrimSpace(parts[0])
	schema = datasetPublicationFileIDSchema(fileID)
	for i, part := range parts {
		if strings.TrimSpace(part) == schema && i+1 < len(parts) {
			batchID = strings.TrimSpace(parts[i+1])
			break
		}
	}
	return dataset, schema, batchID
}

func (n *Node) datasetPNMAlreadyMaterialized(key string) bool {
	n.datasetMaterializeMu.Lock()
	defer n.datasetMaterializeMu.Unlock()
	if n.datasetMaterializedPNMs == nil {
		n.datasetMaterializedPNMs = make(map[string]time.Time)
	}
	now := time.Now()
	for existing, seenAt := range n.datasetMaterializedPNMs {
		if now.Sub(seenAt) > 24*time.Hour {
			delete(n.datasetMaterializedPNMs, existing)
		}
	}
	if _, ok := n.datasetMaterializedPNMs[key]; ok {
		return true
	}
	n.datasetMaterializedPNMs[key] = now
	return false
}

func (n *Node) clearDatasetPNMMaterialized(key string) {
	n.datasetMaterializeMu.Lock()
	defer n.datasetMaterializeMu.Unlock()
	delete(n.datasetMaterializedPNMs, key)
}

func ed25519PublicKeyFromPeerID(id peer.ID) (ed25519.PublicKey, error) {
	pubKey, err := id.ExtractPublicKey()
	if err != nil {
		return nil, err
	}
	raw, err := pubKey.Raw()
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("peer public key length = %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(append([]byte(nil), raw...)), nil
}

func (n *Node) datasetPublicationPublicKey(ctx context.Context, id peer.ID) (ed25519.PublicKey, error) {
	if key, err := ed25519PublicKeyFromPeerID(id); err == nil {
		return key, nil
	}
	if key, err := n.datasetPublicationPublicKeyFromDirectory(id); err == nil {
		return key, nil
	}
	if n != nil && n.host != nil {
		epmBytes, fetchErr := n.fetchDiscoveredNodeEPM(id)
		if fetchErr == nil && len(epmBytes) > 0 {
			n.indexFetchedDiscoveredNodeEPM(id, "dataset-publication", epmBytes)
			if key, err := n.datasetPublicationPublicKeyFromDirectory(id); err == nil {
				return key, nil
			}
		} else if fetchErr != nil && ctx != nil && ctx.Err() == nil {
			log.Debugf("Could not fetch provider EPM for dataset publication key from %s: %v", id.ShortString(), fetchErr)
		}
	}
	return nil, fmt.Errorf("no Ed25519 signing key found in trusted provider EPM for %s", id.ShortString())
}

func (n *Node) datasetPublicationPublicKeyFromDirectory(id peer.ID) (ed25519.PublicKey, error) {
	if n == nil || n.store == nil {
		return nil, fmt.Errorf("directory store is unavailable")
	}
	records, err := n.store.QueryDirectory(storage.DirectoryQuery{
		Kind:   directory.KindNode,
		PeerID: id.String(),
		Limit:  1,
	})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("no EPM directory record found for %s", id.ShortString())
	}
	return ed25519PublicKeyFromDirectoryJSON(records[0].EPMJSON)
}

func ed25519PublicKeyFromDirectoryJSON(epmJSON string) (ed25519.PublicKey, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(epmJSON), &payload); err != nil {
		return nil, fmt.Errorf("parse EPM directory JSON: %w", err)
	}
	if key, err := decodeEd25519PublicKeyHex(firstDirectoryString(payload, "signing_pubkey_hex", "SIGNING_PUBKEY_HEX")); err == nil {
		return key, nil
	}
	keysAny := firstDirectoryAny(payload, "keys", "KEYS")
	keys, ok := keysAny.([]any)
	if !ok {
		return nil, fmt.Errorf("no Ed25519 signing public key in EPM directory record")
	}
	for _, entry := range keys {
		key, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		keyType := strings.ToLower(strings.TrimSpace(firstDirectoryString(key, "key_type", "KEY_TYPE")))
		addressType := strings.ToLower(strings.TrimSpace(firstDirectoryString(key, "address_type", "ADDRESS_TYPE")))
		if keyType != "signing" || (addressType != "" && addressType != "ed25519") {
			continue
		}
		if pub, err := decodeEd25519PublicKeyHex(firstDirectoryString(key, "public_key", "PUBLIC_KEY")); err == nil {
			return pub, nil
		}
	}
	return nil, fmt.Errorf("no Ed25519 signing public key in EPM directory record")
}

func firstDirectoryAny(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func firstDirectoryString(values map[string]any, keys ...string) string {
	value := firstDirectoryAny(values, keys...)
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func decodeEd25519PublicKeyHex(value string) (ed25519.PublicKey, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("empty Ed25519 public key")
	}
	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key length = %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(append([]byte(nil), raw...)), nil
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
		m.node.requestConnectedPeerEPM(pi.ID, "mdns")
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
	if strings.TrimSpace(n.sdnAdvertisementTarget.Namespace) != "" {
		log.Infof("SDN advertisement namespace: %s", sdnAdvertisementDiscoveryNamespace)
		log.Infof("SDN advertisement flag: %s", n.sdnAdvertisementTarget.Flag)
		log.Infof("SDN advertisement rendezvous: %s", n.sdnAdvertisementTarget.Namespace)
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
	if strings.TrimSpace(n.sdnAdvertisementTarget.Namespace) != "" {
		n.announceSDNAdvertisement(n.sdnAdvertisementTarget)
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
			if strings.TrimSpace(n.sdnAdvertisementTarget.Namespace) != "" {
				n.announceSDNAdvertisement(n.sdnAdvertisementTarget)
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

func (n *Node) announceSDNAdvertisement(target sdnAdvertisementDiscoveryTarget) {
	if strings.TrimSpace(target.Namespace) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
	defer cancel()

	routingDiscovery := drouting.NewRoutingDiscovery(n.dht)
	dutil.Advertise(ctx, routingDiscovery, target.Namespace)
	log.Debugf("SDN advertisement announce started for %s", target.Flag)
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
			n.requestConnectedPeerEPM(peerInfo.ID, "dht-discovery")
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
				n.requestConnectedPeerEPM(pi.ID, "dht-discovery")
				log.Infof("Connected to discovered SDN peer: %s", pi.ID)
			}
		}(peerInfo)
	}
}

func (n *Node) discoverSDNAdvertisementPeers(target sdnAdvertisementDiscoveryTarget) {
	if strings.TrimSpace(target.Namespace) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
	defer cancel()

	routingDiscovery := drouting.NewRoutingDiscovery(n.dht)
	peerChan, err := routingDiscovery.FindPeers(ctx, target.Namespace)
	if err != nil {
		log.Debugf("Failed to query SDN advertisement peers for %s: %v", target.Flag, err)
		return
	}

	for peerInfo := range peerChan {
		if peerInfo.ID == n.host.ID() {
			continue
		}

		n.recordSDNAdvertisementPeerInfo(peerInfo, target.Flag)

		if n.host.Network().Connectedness(peerInfo.ID) == 2 {
			n.requestConnectedPeerEPM(peerInfo.ID, "sdn-advertisement-discovery")
			continue
		}

		go func(pi peer.AddrInfo, flag string) {
			connectCtx, connectCancel := context.WithTimeout(n.ctx, 10*time.Second)
			defer connectCancel()

			if err := n.host.Connect(connectCtx, pi); err != nil {
				log.Debugf("Failed to connect to discovered SDN advertisement peer %s (%s): %v", pi.ID, flag, err)
			} else {
				n.enqueueAutoRelayCandidate(pi)
				n.requestConnectedPeerEPM(pi.ID, "sdn-advertisement-discovery")
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
	for _, mf := range n.mountedFlows {
		mf.Close()
	}
	n.mountedFlows = nil
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

// MountedFlows returns the flow modules currently mounted on HTTP listener
// paths (loop G.1: the OpenAPI generator reads their flow.json api
// extensions, so the published spec is derived from what is ACTUALLY
// mounted).
func (n *Node) MountedFlows() []*flowrt.MountedFlow {
	return n.mountedFlows
}

// MountFlows registers the config-declared flow HTTP mounts
// (config flows.mounts) on the mux. Each mount loads a compiled flow bundle
// as a WASM module through the standard flow runtime and binds it to its
// listener path; the HTTP handler is pure socket plumbing ($HTQ in, $HTR
// out). Loading rejects any flow whose declared capability set the node
// cannot satisfy.
func (n *Node) MountFlows(mux *http.ServeMux) error {
	mounts := n.config.Flows.Mounts
	if len(mounts) == 0 {
		return nil
	}
	nodeCtx, err := n.buildModuleNodeContext()
	if err != nil {
		return fmt.Errorf("build module node context: %w", err)
	}
	deps := flowrt.FlowMountDeps{
		CapRegistry:    n.buildCapRegistry(),
		NodeCtx:        nodeCtx,
		MaxMemoryPages: n.config.Flows.MaxMemoryPages,
		// Flow mounts run AOT-compiled through the same sha256-keyed cache
		// as the store engine (loop C.5b). The C.4 "out of bounds memory
		// access" trap on linked-direct dispatch was NOT an artifact bug:
		// libwasmedge 0.14 corrupts per-thread executor state when the
		// storage hostcall executes the (AOT) engine nested inside the
		// (AOT) flow's frame. Fixed by pinning engine execution to its own
		// locked OS thread (wasmrt.WithDedicatedThread; see
		// docs/wasmedge-aot-nested-execution.md, flowrt TestAOTMountRepro).
		AOTCacheDir: flatsqldrv.DefaultAOTCacheDir(),
		// Direct engine linkage (loop C.7): config-declared mounts are the
		// node admin's first-party flows, so engine-linked artifacts get the
		// store's LIVE engine instance. Untrusted/delivered third-party
		// modules never reach this path — they stay on the
		// storage.flatsql_* hostcall bridge permanently.
		EngineLink: n.store,
	}
	if n.flowManager != nil {
		deps.Store = n.flowManager.Store()
	}
	mounted, err := flowrt.RegisterFlowMounts(mux, mounts, deps)
	if err != nil {
		return err
	}
	n.mountedFlows = append(n.mountedFlows, mounted...)
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

	if err := topic.Publish(n.ctx, data); err != nil {
		return err
	}
	metrics.PubsubPublished(schema)
	return nil
}

// PublishToTopic publishes data to an explicit pub/sub topic, joining it first
// if this node has not used the topic yet.
func (n *Node) PublishToTopic(ctx context.Context, topicName string, data []byte) error {
	if n.pubsub == nil {
		return errors.New("pubsub is not running")
	}
	if ctx == nil {
		ctx = n.ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}

	topic, ok := n.topics[topicName]
	if !ok {
		joined, err := n.pubsub.Join(topicName)
		if err != nil {
			return fmt.Errorf("join topic %s: %w", topicName, err)
		}
		topic = joined
		n.topics[topicName] = topic
	}
	return topic.Publish(ctx, data)
}

// PublishDatasetUpdatePNM announces one signed dataset-publication PNM on the
// PNM topic and every affected dataset schema topic.
func (n *Node) PublishDatasetUpdatePNM(ctx context.Context, ann sdnpubsub.DatasetUpdateAnnouncement) error {
	return sdnpubsub.PublishDatasetUpdatePNM(ctx, n, ann)
}

func (n *Node) PublishDatasetFeedHead(ctx context.Context, ann sdnpubsub.DatasetFeedHeadAnnouncement) error {
	if key := n.SigningKey(); len(key) == ed25519.PrivateKeySize {
		return sdnpubsub.PublishSignedDatasetFeedHead(ctx, n, ann, ed25519.PrivateKey(key))
	}
	return sdnpubsub.PublishDatasetFeedHead(ctx, n, ann)
}

// PublishCAResultSummary publishes a signed CA result summary to the private
// result channel for a managed private node.
func (n *Node) PublishCAResultSummary(ctx context.Context, publication sdnpubsub.CAResultPublication) error {
	return sdnpubsub.PublishCAResultSummary(ctx, n, publication)
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

// DirectoryService returns the node's directory index service.
func (n *Node) DirectoryService() *directory.Service {
	return n.directorySvc
}

// IndexLocalNodeEPM writes the current node EPM profile into the local directory.
func (n *Node) IndexLocalNodeEPM() error {
	return n.indexLocalNodeEPM()
}

func (n *Node) indexLocalNodeEPM() error {
	if n == nil || n.epmService == nil || n.directorySvc == nil {
		return nil
	}
	epmCID := ""
	if cid, err := n.epmService.GetNodeEPMCID(); err == nil {
		epmCID = cid
	} else {
		log.Debugf("Failed to compute local node EPM CID: %v", err)
	}
	return n.directorySvc.UpsertNodeEPMJSON(n.epmService.DirectoryRecordJSON(), epmCID, "local-node")
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
