// Package main provides the entry point for the Space Data Network server.
// This is a specialized fork of IPFS (Kubo) tailored for space data standards.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	logging "github.com/ipfs/go-log/v2"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	_ "github.com/mattn/go-sqlite3"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/adminui"
	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt/editor"
	"github.com/spacedatanetwork/sdn-server/internal/frontend"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storefront"
	"github.com/spacedatanetwork/sdn-server/internal/tlsmgr"
	"github.com/spacedatanetwork/sdn-server/internal/tor"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

var (
	log              = logging.Logger("sdn")
	processStartTime = time.Now()
)

var rootCmd = &cobra.Command{
	Use:   "spacedatanetwork",
	Short: "Space Data Network - FlatBuffer-native P2P for space data",
	Long: `spacedatanetwork is a specialized fork of IPFS tailored for the Space Data Network.
It replaces generic content-addressed storage with FlatBuffer-native data handling
and SQLite-based structured storage, optimized for space data standards.`,
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the SDN daemon",
	Long:  `Start the Space Data Network daemon in full node mode.`,
	RunE:  runDaemon,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize SDN configuration",
	Long:  `Initialize the Space Data Network configuration and data directories.`,
	RunE:  runInit,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print SDN version information",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintf(cmd.OutOrStdout(), "version=%s\n", versioninfo.SuiteVersion)
		fmt.Fprintf(cmd.OutOrStdout(), "agent=%s\n", versioninfo.AgentVersion)
		return nil
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Print the SDN configuration path",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := strings.TrimSpace(configPath)
		if path == "" {
			path = config.DefaultPath()
		}
		fmt.Fprintln(cmd.OutOrStdout(), path)
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print local SDN daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(cmd)
	},
}

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Print the local SDN UI URL",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), adminURL(cfg))
		return nil
	},
}

var reindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Rebuild storage indexes for fast API queries",
	Long:  `Rebuilds the sdn_record_index table from existing schema records.`,
	RunE:  runReindex,
}

var deriveXPubCmd = &cobra.Command{
	Use:   "derive-xpub",
	Short: "Derive a BIP-32 xpub from a BIP-39 mnemonic",
	Long: `Derives the standard BIP-32 extended public key at m/44'/0'/0' from a BIP-39 mnemonic.
The resulting xpub can be pasted directly into config.yaml as the user's xpub field.
The Ed25519 signing key is bound on first wallet login (TOFU).`,
	RunE: runDeriveXPub,
}

var showIdentityCmd = &cobra.Command{
	Use:   "show-identity",
	Short: "Show the node's identity (PeerID, xpub, mnemonic)",
	Long: `Decrypts the stored mnemonic and derives the node's full identity:
PeerID, xpub, signing public key, and optionally the mnemonic phrase itself.

The mnemonic is only shown when --show-mnemonic is passed.
Password is resolved from SDN_KEY_PASSWORD env, config, or machine default.`,
	RunE: runShowIdentity,
}

var (
	configPath   string
	listenAddr   string
	debug        bool
	wasmPath     string
	showMnemonic bool
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "enable debug logging")

	daemonCmd.Flags().StringVarP(&listenAddr, "listen", "l", "", "override listen address")
	deriveXPubCmd.Flags().StringVar(&wasmPath, "wasm", "", "path to hd-wallet-wasi.wasm (default: $HD_WALLET_WASM_PATH or ../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm)")
	showIdentityCmd.Flags().BoolVar(&showMnemonic, "show-mnemonic", false, "display the decrypted mnemonic phrase (SENSITIVE)")
	showIdentityCmd.Flags().StringVar(&wasmPath, "wasm", "", "path to hd-wallet-wasi.wasm")

	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(openCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(reindexCmd)
	rootCmd.AddCommand(deriveXPubCmd)
	rootCmd.AddCommand(showIdentityCmd)
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

func runStatus(cmd *cobra.Command) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "admin_url=%s\n", adminURL(cfg))
	fmt.Fprintln(cmd.OutOrStdout(), "daemon_status=unknown")
	return nil
}

func adminURL(cfg *config.Config) string {
	addr := "127.0.0.1:5001"
	scheme := "http"
	if cfg != nil {
		if strings.TrimSpace(cfg.Admin.ListenAddr) != "" {
			addr = cfg.Admin.ListenAddr
		}
		if cfg.Admin.EffectiveTLSMode() != tlsmgr.ModeDisabled {
			scheme = "https"
		}
	}
	return fmt.Sprintf("%s://%s/", scheme, addr)
}

func applyBundleDefaults(cfg *config.Config, layout bundle.Layout) {
	if cfg == nil || layout.Root == "" {
		return
	}
	if strings.TrimSpace(cfg.Admin.FrontendPath) == "" && pathExists(layout.SDNUIPath) {
		cfg.Admin.FrontendPath = layout.SDNUIPath
	}
	if strings.TrimSpace(cfg.Admin.WebuiPath) == "" && pathExists(layout.WebUIPath) {
		cfg.Admin.WebuiPath = layout.WebUIPath
	}
}

func pathExists(pathValue string) bool {
	if strings.TrimSpace(pathValue) == "" {
		return false
	}
	_, err := os.Stat(pathValue)
	return err == nil
}

func runDaemon(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Override listen address if specified
	if listenAddr != "" {
		cfg.Network.Listen = []string{listenAddr}
	}

	// Allow environment variable overrides for paths commonly set via systemd env files
	if cfg.Admin.WalletUIPath == "" {
		if envPath := os.Getenv("SDN_WALLET_UI_PATH"); envPath != "" {
			cfg.Admin.WalletUIPath = envPath
		}
	}
	if cfg.Admin.AdminUIPath == "" {
		if envPath := os.Getenv("SDN_ADMIN_UI_PATH"); envPath != "" {
			cfg.Admin.AdminUIPath = envPath
		}
	}
	if cfg.Admin.WebuiPath == "" {
		if envPath := os.Getenv("SDN_WEBUI_PATH"); envPath != "" {
			cfg.Admin.WebuiPath = envPath
		}
	}
	if cfg.Admin.IPFSAPIURL == "" {
		if envURL := os.Getenv("SDN_IPFS_API_URL"); envURL != "" {
			cfg.Admin.IPFSAPIURL = envURL
		}
	}
	if cfg.Admin.IPFSGatewayURL == "" {
		if envURL := os.Getenv("SDN_IPFS_GATEWAY_URL"); envURL != "" {
			cfg.Admin.IPFSGatewayURL = envURL
		}
	}
	if envPath := os.Getenv("SDN_FRONTEND_PATH"); envPath != "" {
		cfg.Admin.FrontendPath = envPath
	}
	applyBundleDefaults(cfg, bundle.ResolveCurrent())
	// Resolve empty frontend path to the built SDN Svelte UI when available,
	// then fall back to the managed frontend directory.
	cfg.Admin.FrontendPath = resolveFrontendPath(cfg.Admin.FrontendPath)
	if cfg.Admin.FrontendPath == "" {
		cfg.Admin.FrontendPath = config.DefaultFrontendPath()
	}
	// Auto-provision frontend directory with default page if it doesn't exist
	if err := provisionFrontendDir(cfg.Admin.FrontendPath); err != nil {
		log.Warnf("Could not provision frontend directory %q: %v", cfg.Admin.FrontendPath, err)
	}

	// Create and start the node
	n, err := node.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to create node: %w", err)
	}

	torStartTimeout := 30 * time.Second
	if raw := strings.TrimSpace(cfg.Tor.StartTimeout); raw != "" {
		if parsed, parseErr := time.ParseDuration(raw); parseErr != nil {
			log.Warnf("Invalid tor.start_timeout %q, using %s", raw, torStartTimeout)
		} else {
			torStartTimeout = parsed
		}
	}

	hiddenServiceTarget := strings.TrimSpace(cfg.Tor.HiddenServiceTarget)
	if hiddenServiceTarget == "" {
		hiddenServiceTarget = cfg.Admin.ListenAddr
	}
	if strings.TrimSpace(hiddenServiceTarget) == "" {
		hiddenServiceTarget = "127.0.0.1:5001"
	}
	hiddenServicePort := cfg.Tor.HiddenServicePort
	if hiddenServicePort <= 0 {
		if cfg.Admin.EffectiveTLSMode() != tlsmgr.ModeDisabled {
			hiddenServicePort = 443
		} else {
			hiddenServicePort = 80
		}
	}

	torRuntime, err := tor.Start(ctx, tor.StartOptions{
		Enabled:                 cfg.Tor.Enabled,
		BinaryPath:              cfg.Tor.BinaryPath,
		StoragePath:             cfg.Storage.Path,
		DataDir:                 cfg.Tor.DataDir,
		SocksAddress:            cfg.Tor.SocksAddress,
		StartTimeout:            torStartTimeout,
		HiddenServiceEnabled:    cfg.Admin.Enabled && cfg.Tor.HiddenServiceEnabled,
		HiddenServicePort:       hiddenServicePort,
		HiddenServiceTarget:     hiddenServiceTarget,
		NodeIdentityKeyMaterial: n.IdentityKeyMaterial(),
	})
	if err != nil {
		return fmt.Errorf("failed to start tor runtime: %w", err)
	}
	if torRuntime != nil {
		defer func() {
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelStop()
			if stopErr := torRuntime.Stop(stopCtx); stopErr != nil {
				log.Warnf("TOR shutdown error: %v", stopErr)
			}
		}()

		if err := torRuntime.ApplyHTTPProxy(cfg.Tor.BypassLocalAddresses); err != nil {
			return fmt.Errorf("failed to apply tor proxy settings: %w", err)
		}
		log.Infof("Outbound HTTP proxying enabled via TOR (%s)", torRuntime.ProxyURL())

		if epmSvc := n.EPMService(); epmSvc != nil && torRuntime.OnionHost() != "" {
			useTLS := cfg.Admin.EffectiveTLSMode() != tlsmgr.ModeDisabled || hiddenServicePort == 443
			if err := epmSvc.SetRuntimeAddresses([]string{torRuntime.OnionURL(useTLS)}); err != nil {
				log.Warnf("Failed to inject onion metadata into EPM: %v", err)
			}
		}
	}

	log.Info("Starting Space Data Network daemon...")
	if err := n.Start(ctx); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	// Print node info
	log.Infof("Peer ID: %s", n.PeerID())
	for _, addr := range n.ListenAddrs() {
		log.Infof("Listening on: %s", addr)
	}

	// Start admin server if enabled
	var adminServer *http.Server
	var httpChallengeServer *http.Server
	var authHandler *auth.Handler
	var storefrontSvc *storefront.Service
	var storefrontStore *storefront.Store
	var storefrontDelivery *storefront.DeliveryService
	if cfg.Admin.Enabled {
		var (
			adminUIHandler http.Handler
			legacyAdminUI  *peers.AdminUI
		)
		if adminUIPath := resolveAdminUIPath(cfg.Admin.AdminUIPath); adminUIPath != "" {
			host, hostErr := adminui.NewHost(adminUIPath)
			if hostErr != nil {
				log.Warnf("Failed to create hosted admin UI from %q: %v", adminUIPath, hostErr)
			} else {
				adminUIHandler = host
				log.Infof("Hosted admin UI available at /admin from %s", adminUIPath)
			}
		}
		if adminUIHandler == nil {
			legacyAdminUI, err = peers.NewAdminUI(n.PeerRegistry(), n.PeerGater())
			if err != nil {
				log.Warnf("Failed to create legacy admin UI: %v", err)
			} else {
				adminUIHandler = legacyAdminUI
				log.Warn("Falling back to legacy inline admin UI because no hosted admin build was found")
			}
		}
		if adminUIHandler == nil {
			log.Warn("Admin UI disabled because no hosted or legacy admin handler could be created")
		} else {
			adminAddr := cfg.Admin.ListenAddr
			if adminAddr == "" {
				adminAddr = "127.0.0.1:5001"
			}
			tlsManager, err := tlsmgr.New(cfg.Admin)
			if err != nil {
				return fmt.Errorf("configure admin tls: %w", err)
			}
			adminTLS := tlsManager.UsesNativeTLS()

			if tlsManager.Mode() == tlsmgr.ModeManaged {
				identity := n.Identity()
				if identity == nil {
					return fmt.Errorf("managed tls requires an HD wallet-derived node identity")
				}
				info := identity.Info()
				bootstrapHosts := make([]string, 0, 1)
				host, _, splitErr := net.SplitHostPort(adminAddr)
				if splitErr == nil && host != "" {
					bootstrapHosts = append(bootstrapHosts, host)
				}
				if err := tlsManager.ConfigureBootstrap(tlsmgr.BootstrapIdentityInput{
					PeerID:                     info.PeerID,
					EncryptionPath:             info.EncryptionKeyPath,
					EncryptionX25519PublicKey:  append([]byte(nil), identity.EncryptionPub...),
					EncryptionProofEd25519Seed: append([]byte(nil), identity.EncryptionKey...),
					Hosts:                      bootstrapHosts,
				}); err != nil {
					return fmt.Errorf("configure bootstrap tls: %w", err)
				}
			}

			adminScheme := "http"
			if adminTLS {
				adminScheme = "https"
			}
			adminMux := http.NewServeMux()
			var wsUpgradeProxy http.Handler

			if adminTLS {
				listenAddrStrings := make([]string, 0, len(n.ListenAddrs()))
				for _, addr := range n.ListenAddrs() {
					listenAddrStrings = append(listenAddrStrings, addr.String())
				}
				if wsTarget, sourceAddr := resolveLocalLibp2pWsProxyTarget(listenAddrStrings); wsTarget != nil {
					wsProxy := httputil.NewSingleHostReverseProxy(wsTarget)
					wsProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
						http.Error(w, "upstream libp2p websocket unavailable", http.StatusBadGateway)
					}
					wsUpgradeProxy = wsProxy
					log.Infof(
						"Proxying secure websocket upgrades to local libp2p transport (%s -> %s)",
						sourceAddr,
						wsTarget.String(),
					)
				} else {
					log.Warn("Admin TLS enabled but no local /ws libp2p listen address was discovered; secure browser key exchange may fail")
				}
			}

			// Plugin routes
			if n.PluginManager() != nil {
				n.PluginManager().RegisterRoutes(adminMux)
			}
			// Data API routes
			dataAPI := api.NewDataQueryHandler(n.Store(), nil)
			dataAPI.RegisterRoutes(adminMux)
			channelAPI := api.NewChannelHandler(n.Store())
			channelAPI.RegisterRoutes(adminMux)

			// Log API routes (publication log queries)
			if n.Store() != nil {
				logAPI := api.NewLogQueryHandler(n.Store())
				logAPI.RegisterRoutes(adminMux)
			}

			// Local dataset publication route used by ingest workers after a
			// successful provider sync.
			if n.Store() != nil {
				publicationSigningKey, err := datasetPublicationSigningKey(cfg, n.SigningKey())
				if err != nil {
					log.Warnf("Dataset publication signing unavailable: %v", err)
				}
				if len(publicationSigningKey) == ed25519.PrivateKeySize && n.Identity() == nil {
					if epmSvc := n.EPMService(); epmSvc != nil {
						if err := epmSvc.SetRuntimeSigningKey(ed25519.PrivateKey(publicationSigningKey), "sdn/dataset-publication/v1"); err != nil {
							log.Warnf("Could not advertise dataset publication signing key in node EPM: %v", err)
						} else if err := n.IndexLocalNodeEPM(); err != nil {
							log.Warnf("Could not refresh local node EPM directory entry after adding dataset publication key: %v", err)
						}
					}
				}
				providerEPMCID := ""
				if n.EPMService() != nil {
					if epmCID, err := n.EPMService().GetNodeEPMCID(); err == nil {
						providerEPMCID = epmCID
					} else {
						log.Warnf("Could not resolve node EPM CID for dataset publications: %v", err)
					}
				}
				publicationDir := filepath.Join(filepath.Dir(cfg.Storage.Path), "dataset-publications")
				publicationAPI := api.NewDatasetPublicationHandler(api.NewConcreteDatasetPublicationService(
					n.Store(),
					n,
					publicationSigningKey,
					n.PeerID().String(),
					providerEPMCID,
					cfg.Admin.IPFSAPIURL,
					publicationDir,
				))
				publicationAPI.RegisterRoutes(adminMux)
				log.Infof("Dataset publication API available at %s://%s/api/v1/admin/dataset-updates/publish", adminScheme, adminAddr)
			}

			// Catalog API route (public)
			if n.Store() != nil {
				catalogAPI := api.NewCatalogHandler(n.Store(), n.PeerID(), cfg)
				catalogAPI.RegisterRoutes(adminMux)
				log.Infof("Catalog API available at %s://%s/api/v1/catalog", adminScheme, adminAddr)
			}

			// Demo API routes (encrypted WASM demo)
			if demoPayloadPath := os.Getenv("SDN_DEMO_PAYLOAD_PATH"); demoPayloadPath != "" {
				ipfsAPIURL := strings.TrimSpace(cfg.Admin.IPFSAPIURL)
				demoAPI := api.NewDemoHandler(demoPayloadPath, ipfsAPIURL)
				demoAPI.RegisterRoutes(adminMux)
				log.Infof("Demo available at %s://%s/demo", adminScheme, adminAddr)
				log.Infof("Demo API available at %s://%s/api/v1/demo/payload", adminScheme, adminAddr)

				// Pin demo payload to IPFS in background if configured
				if ipfsAPIURL != "" {
					go func() {
						cid, err := demoAPI.PinToIPFS(ctx)
						if err != nil {
							log.Warnf("Failed to pin demo payload to IPFS: %v", err)
						} else {
							log.Infof("Demo payload pinned to IPFS: %s", cid)
							log.Infof("IPFS gateway: https://ipfs.io/ipfs/%s", cid)
						}
					}()
				}
			}

			// Optional: proxy Kubo RPC API so the React WebUI can talk to IPFS via the
			// authenticated SDN admin server.
			if rawIPFSURL := strings.TrimSpace(cfg.Admin.IPFSAPIURL); rawIPFSURL != "" {
				target, err := url.Parse(rawIPFSURL)
				if err != nil || target.Scheme == "" || target.Host == "" {
					log.Warnf("Invalid admin.ipfs_api_url %q: expected base URL like http://127.0.0.1:5001", rawIPFSURL)
				} else {
					if strings.TrimSpace(target.Path) != "" && target.Path != "/" {
						log.Warnf("admin.ipfs_api_url should not include a path (got %q); ignoring path", target.Path)
					}
					target.Path = ""
					proxy := httputil.NewSingleHostReverseProxy(target)
					origDirector := proxy.Director
					proxy.Director = func(req *http.Request) {
						origDirector(req)
						// Kubo's RPC API rejects browser User-Agent headers (403) and
						// Origins not in its allowlist. Strip all three when proxying.
						req.Header.Del("Origin")
						req.Header.Del("Referer")
						req.Header.Del("User-Agent")
					}
					proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
						http.Error(w, "upstream IPFS API unavailable", http.StatusBadGateway)
					}
					adminMux.Handle("/api/v0/", proxy)
					adminMux.Handle("/api/v0", http.RedirectHandler("/api/v0/", http.StatusPermanentRedirect))
					log.Infof("Proxying /api/v0/* to %s", rawIPFSURL)
				}
			}

			// Optional: proxy Kubo HTTP gateway so the WebUI can fetch IPFS content
			// via the same origin without needing direct access to the gateway port.
			if rawGWURL := strings.TrimSpace(cfg.Admin.IPFSGatewayURL); rawGWURL != "" {
				gwTarget, err := url.Parse(rawGWURL)
				if err != nil || gwTarget.Scheme == "" || gwTarget.Host == "" {
					log.Warnf("Invalid admin.ipfs_gateway_url %q: expected base URL like http://127.0.0.1:8080", rawGWURL)
				} else {
					gwTarget.Path = ""
					gwProxy := httputil.NewSingleHostReverseProxy(gwTarget)
					origGWDirector := gwProxy.Director
					gwProxy.Director = func(req *http.Request) {
						origGWDirector(req)
						req.Header.Del("Origin")
						req.Header.Del("Referer")
						req.Header.Del("User-Agent")
					}
					gwProxy.ModifyResponse = func(resp *http.Response) error {
						normalizeIPFSGatewayCORSHeaders(resp.Header)
						return nil
					}
					gwProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
						http.Error(w, "upstream IPFS gateway unavailable", http.StatusBadGateway)
					}
					adminMux.Handle("/ipfs/", gwProxy)
					log.Infof("Proxying /ipfs/* to %s", rawGWURL)
				}
			}

			// Public IPFS WebUI mount.
			if webuiPath := strings.TrimSpace(cfg.Admin.WebuiPath); webuiPath != "" {
				webuiHandler, err := makeWebUIHandler(webuiPath, "/webui")
				if err != nil {
					log.Warnf("IPFS WebUI disabled at /webui: %v", err)
				} else {
					serveWebUI := func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/webui" {
							http.Redirect(w, r, "/webui/", http.StatusMovedPermanently)
							return
						}
						if !strings.HasPrefix(r.URL.Path, "/webui/") {
							http.NotFound(w, r)
							return
						}

						serve := func(w http.ResponseWriter, r *http.Request) {
							http.StripPrefix("/webui", webuiHandler).ServeHTTP(w, r)
						}
						if cfg.Admin.RequireAuth {
							if authHandler == nil {
								http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
								return
							}
							authHandler.RequireAuth(peers.Standard, serve)(w, r)
							return
						}
						serve(w, r)
					}
					adminMux.HandleFunc("/webui", serveWebUI)
					adminMux.HandleFunc("/webui/", serveWebUI)
					log.Infof("IPFS WebUI at %s://%s/webui from %s", adminScheme, adminAddr, webuiPath)
				}
			}

			// Trusted peer registry management (admin UI React app consumes these endpoints).
			adminMux.Handle("/api/", peers.NewAPIHandler(n.PeerRegistry(), n.PeerGater()))

			// Storefront API (listings, purchases, Stripe checkout/webhooks).
			// Uses FlatSQL for content-addressed storage of STF/ACL/PUR/REV records.
			if n.Store() != nil {
				sfStore, err := storefront.NewStore(n.Store())
				if err != nil {
					log.Warnf("Failed to initialize storefront store: %v", err)
				} else {
					sfSigningKey, err := storefrontSigningKeyFromRaw(n.SigningKey())
					if err != nil {
						log.Warnf("Storefront grants will be unsigned; node signing key unavailable: %v", err)
					}
					sfSvc, err := storefront.NewService(sfStore, n.PeerID().String(), sfSigningKey, nil)
					if err != nil {
						log.Warnf("Failed to initialize storefront service: %v", err)
						_ = sfStore.Close()
					} else {
						sfCatalog := storefront.NewCatalog(sfStore, nil)
						sfDelivery := storefront.NewDeliveryService(storefront.DefaultDeliveryConfig(), nil)
						var chainVerifiers []storefront.ChainVerifier
						if cfg.Blockchain.Ethereum.RPCURL != "" {
							chainVerifiers = append(chainVerifiers, storefront.NewEthereumVerifier(storefront.ChainConfig{
								RPCURL:                cfg.Blockchain.Ethereum.RPCURL,
								RequiredConfirmations: cfg.Blockchain.Ethereum.RequiredConfirmations,
							}))
						}
						if cfg.Blockchain.Solana.RPCURL != "" {
							chainVerifiers = append(chainVerifiers, storefront.NewSolanaVerifier(storefront.ChainConfig{
								RPCURL:                cfg.Blockchain.Solana.RPCURL,
								RequiredConfirmations: cfg.Blockchain.Solana.RequiredConfirmations,
							}))
						}
						if cfg.Blockchain.Bitcoin.RPCURL != "" {
							chainVerifiers = append(chainVerifiers, storefront.NewBitcoinVerifier(storefront.ChainConfig{
								RPCURL:                cfg.Blockchain.Bitcoin.RPCURL,
								RequiredConfirmations: cfg.Blockchain.Bitcoin.RequiredConfirmations,
							}))
						}
						sfPayment := storefront.NewPaymentProcessor(sfStore, n.PeerID().String(), chainVerifiers...)
						sfTrust := storefront.NewTrustScorer(sfStore, storefront.DefaultTrustWeights())
						sfAPI := storefront.NewAPIHandler(sfSvc, sfCatalog, sfDelivery, sfPayment, sfTrust)
						sfAPI.RegisterRoutes(adminMux, authHandler)
						storefrontSvc = sfSvc
						storefrontStore = sfStore
						storefrontDelivery = sfDelivery
						log.Infof("Storefront API available at %s://%s/api/storefront/listings", adminScheme, adminAddr)
						log.Infof("Stripe webhook endpoint: %s://%s/api/storefront/payments/stripe/webhook", adminScheme, adminAddr)
					}
				}
			}

			// Flow management API and editor
			if fm := n.FlowManager(); fm != nil {
				flowrt.RegisterAPI(adminMux, fm)
				log.Infof("Flow management API registered at /api/v1/flows/")

				if cfg.Flows.EditorEnabled {
					editorPath := cfg.Flows.EditorPath
					if editorPath == "" {
						editorPath = "/flow-editor"
					}
					editor.RegisterEditor(adminMux, editorPath, fm)
					log.Infof("Flow editor embedded at %s", editorPath)
				}
			}

			// Node info API endpoint
			adminMux.HandleFunc("/api/node/info", handleNodeInfo(n, torRuntime))
			adminMux.HandleFunc("/api/module-delivery/provider", handleProviderDescriptor(n))
			adminMux.HandleFunc("/api/module-delivery/listings", handleModuleDeliveryListings(n.PluginRegistry()))
			adminMux.HandleFunc("/api/v1/modules/runtime", handleModuleRuntimeSnapshot(n.PluginManager(), n.PluginRegistry()))
			adminMux.HandleFunc("/api/v1/modules/runtime/", handleModuleRuntimeMutation(n.PluginManager()))
			adminMux.Handle("/api/directory/", directory.NewHTTPHandler(n.DirectoryService()))

			// Relay status endpoint (public, used by clients for load balancing)
			adminMux.HandleFunc("/api/relay/status", handleRelayStatus(n))

			// EPM (Entity Profile Message) API endpoints
			adminMux.HandleFunc("/api/node/epm/json", handleNodeEPMJSON(n))
			adminMux.HandleFunc("/api/node/epm/vcard", handleNodeEPMVCard(n))
			adminMux.HandleFunc("/api/node/epm/qr", handleNodeEPMQR(n))
			adminMux.HandleFunc("/api/node/epm", handleNodeEPM(n))

			// Peer graph API endpoints
			adminMux.HandleFunc("/api/peers/sdn", handleObservedSDNPeers(n))
			adminMux.HandleFunc("/api/peers/graph", handlePeerGraph(n))
			adminMux.HandleFunc("/api/peers/graph/schema", handlePeerGraphSchema)

			// libp2p bootstrap JS — serves a JS module with the node's raw IP,
			// peer ID, and ws:// multiaddr injected at request time so browsers
			// can connect using the raw IP without DNS.
			adminMux.HandleFunc("/sdn/libp2p.js", handleLibp2pJS(n))

			// HD wallet authentication
			if cfg.Admin.RequireAuth {
				authDBPath := filepath.Join(cfg.Storage.Path, "auth.db")
				authDB, err := sql.Open("sqlite3", authDBPath+"?_journal_mode=WAL")
				if err != nil {
					return fmt.Errorf("admin authentication required: open auth database: %w", err)
				}

				userStore, err := auth.NewUserStore(authDBPath, cfg.Users)
				if err != nil {
					_ = authDB.Close()
					return fmt.Errorf("admin authentication required: create user store: %w", err)
				}

				sessionStore, err := auth.NewSessionStore(authDB)
				if err != nil {
					_ = authDB.Close()
					return fmt.Errorf("admin authentication required: create session store: %w", err)
				}

				sessionTTL, _ := time.ParseDuration(cfg.Admin.SessionExpiry)
				if sessionTTL == 0 {
					sessionTTL = 24 * time.Hour
				}

				cfgDisplayPath := configPath
				if cfgDisplayPath == "" {
					cfgDisplayPath = config.DefaultPath()
				}
				authHandler = auth.NewHandler(userStore, sessionStore, sessionTTL, cfg.Admin.WalletUIPath, cfgDisplayPath)
				authHandler.SetTLSManager(tlsManager)
				if epmSvc := n.EPMService(); epmSvc != nil {
					if att := epmSvc.GetIdentityAttestation(); att != nil {
						authHandler.SetNodeSigningAttestation(att)
					}
				}
				authHandler.RegisterRoutes(adminMux)
				n.SetModulePublishAuthorizer(func(xpub string) (license.ModulePublishPrincipal, error) {
					user, err := authHandler.UserStore().GetUser(xpub)
					if err != nil {
						return license.ModulePublishPrincipal{}, err
					}
					if user == nil {
						return license.ModulePublishPrincipal{}, nil
					}
					return license.ModulePublishPrincipal{
						XPub:             user.XPub,
						SigningPubKeyHex: user.SigningPubKeyHex,
						Admin:            user.TrustLevel >= peers.Admin,
					}, nil
				})
				log.Infof("HD wallet authentication enabled at %s://%s/login", adminScheme, adminAddr)

				if n.DirectoryService() != nil {
					adminDirectoryHandler := directory.NewAdminHTTPHandler(n.DirectoryService())
					adminMux.HandleFunc("/api/v1/admin/directory/import", authHandler.RequireAuth(peers.Standard, adminDirectoryHandler.ServeHTTP))
					log.Infof("Directory import API available at %s://%s/api/v1/admin/directory/import", adminScheme, adminAddr)
				}

				// Publish API (requires auth)
				if n.Store() != nil && cfg.Publishing.Enabled {
					quotas := api.NewStorageQuotaManager(n.Store(), cfg.Publishing.DefaultQuotaBytes)
					publishAPI := api.NewPublishHandler(n.Store(), n.Validator(), quotas, &cfg.Publishing, authHandler)
					publishAPI.SetLogService(n.LogService())
					publishAPI.RegisterRoutes(adminMux)
					log.Infof("Publish API available at %s://%s/api/v1/data/publish/", adminScheme, adminAddr)
				}

				// Peer ACL admin API (requires admin auth)
				if n.PeerRegistry() != nil {
					aclAPI := api.NewACLHandler(n.PeerRegistry(), authHandler)
					aclAPI.RegisterRoutes(adminMux)
					log.Infof("Peer ACL API available at %s://%s/api/v1/admin/peers", adminScheme, adminAddr)
				}

				// Serve wallet-ui static files if configured
				if walletUIPath := strings.TrimSpace(cfg.Admin.WalletUIPath); walletUIPath != "" {
					serveRoot := auth.WalletUIStaticRoot(walletUIPath)
					if serveRoot == "" {
						serveRoot = walletUIPath
					}
					adminMux.Handle("/wallet-ui/", http.StripPrefix("/wallet-ui/", http.FileServer(http.Dir(serveRoot))))
					log.Infof("Wallet UI served at %s://%s/wallet-ui/ from %s", adminScheme, adminAddr, serveRoot)
				}

				// Discover wallet-ui assets for the login page and the legacy admin UI fallback.
				auth.DiscoverWalletAssets(cfg.Admin.WalletUIPath)
				if legacyAdminUI != nil {
					if jsFile, cssFile := auth.WalletAssets(); jsFile != "" {
						legacyAdminUI.SetWalletAssets(jsFile, cssFile)
					}
				}
			}

			// ----------------------------------------------------------------
			// Plugin upload API (admin-only, requires auth + license plugin)
			// ----------------------------------------------------------------
			if authHandler != nil {
				if reg := n.PluginRegistry(); reg != nil {
					uploadHandler := license.NewUploadHandler(
						reg,
						func(xpub string) (string, error) {
							user, err := authHandler.UserStore().GetUser(xpub)
							if err != nil {
								return "", err
							}
							if user == nil {
								return "", fmt.Errorf("user not found")
							}
							return user.SigningPubKeyHex, nil
						},
						func(r *http.Request) (string, error) {
							session := auth.SessionFromContext(r.Context())
							if session == nil {
								return "", fmt.Errorf("no session")
							}
							return session.XPub, nil
						},
					)
					adminMux.HandleFunc("/api/v1/plugins/upload", uploadHandler.ServeHTTP)
					log.Infof("Plugin upload API at %s://%s/api/v1/plugins/upload", adminScheme, adminAddr)
				}
			}

			// ----------------------------------------------------------------
			// Frontend management API (admin-only)
			// ----------------------------------------------------------------
			frontendMgr := frontend.NewManager(cfg.Admin.FrontendPath)
			frontendMgr.RegisterRoutes(adminMux)
			log.Infof("Frontend manager at %s://%s/api/admin/frontend/ (dir: %s)", adminScheme, adminAddr, cfg.Admin.FrontendPath)

			// Serve favicon.ico directly so root icon requests do not 404.
			// Prefer the public frontend favicon, then wallet UI favicon, then fallback
			// to a tiny built-in transparent icon.
			frontendFaviconPath := filepath.Join(strings.TrimSpace(cfg.Admin.FrontendPath), "favicon.ico")
			walletFaviconPath := ""
			if wui := strings.TrimSpace(cfg.Admin.WalletUIPath); wui != "" {
				walletFaviconPath = filepath.Join(wui, "favicon.ico")
			}
			adminMux.Handle("/favicon.ico", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serveFavicon(w, r, []string{frontendFaviconPath, walletFaviconPath})
			}))

			// ----------------------------------------------------------------
			// Admin panel at /admin — admin/auth surface only
			// ----------------------------------------------------------------
			adminUISubtree := http.StripPrefix("/admin", adminUIHandler)
			serveAdminUI := func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/admin" {
					http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/admin/") {
					http.NotFound(w, r)
					return
				}

				serve := func(w http.ResponseWriter, r *http.Request) {
					adminUISubtree.ServeHTTP(w, r)
				}
				if cfg.Admin.RequireAuth {
					if authHandler == nil {
						http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
						return
					}
					authHandler.RequireAuth(peers.Admin, serve)(w, r)
					return
				}
				serve(w, r)
			}
			adminMux.HandleFunc("/admin", serveAdminUI)
			adminMux.HandleFunc("/admin/", serveAdminUI)

			// ----------------------------------------------------------------
			// Public homepage at / — intentionally separate from /admin.
			// ----------------------------------------------------------------
			frontendHandler, frontendErr := makeFrontendHandler(cfg.Admin.FrontendPath)
			if frontendErr != nil {
				log.Warnf("Could not serve frontend_path %q at /: %v", cfg.Admin.FrontendPath, frontendErr)
				homepageFile := publicHomepageFile(cfg.Admin.FrontendPath, cfg.Admin.HomepageFile)
				landingHTML := loadLandingPageFallback(homepageFile)
				if buildAssetsDir := resolveBuildAssetsDir(homepageFile); buildAssetsDir != "" {
					adminMux.Handle("/Build/", http.StripPrefix("/Build/", http.FileServer(http.Dir(buildAssetsDir))))
					log.Infof("Static build assets at %s://%s/Build/ from %s", adminScheme, adminAddr, buildAssetsDir)
				}
				frontendHandler = adminLandingHandler(http.NotFoundHandler(), landingHTML)
			}
			adminMux.Handle("/", makeFrontendSurfaceHandler(frontendHandler, authHandler, cfg.Admin.RequireAuth))
			log.Infof("SDN UI at %s://%s/ from %s (admin portal remains at /admin)", adminScheme, adminAddr, cfg.Admin.FrontendPath)

			adminServer = &http.Server{
				Addr:              adminAddr,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      10 * time.Minute,
				IdleTimeout:       120 * time.Second,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Tunnel secure websocket upgrades to the local libp2p ws listener.
					if wsUpgradeProxy != nil && isWebSocketUpgradeRequest(r) {
						wsUpgradeProxy.ServeHTTP(w, r)
						return
					}

					// Global security headers on ALL responses
					w.Header().Set("X-Content-Type-Options", "nosniff")
					w.Header().Set("X-Frame-Options", "DENY")
					w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

					// Cross-origin isolation headers are set by the frontend handler
					// (makeFrontendHandler) for OrbPro routes that need SharedArrayBuffer.
					if tlsManager.Mode() == tlsmgr.ModeStatic {
						w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
					}

					if isPublicAPIRequest(r.Method, r.URL.Path) {
						applyPublicAPICORSHeaders(w.Header(), r.Header.Get("Origin"))
						if r.Method == http.MethodOptions {
							w.WriteHeader(http.StatusNoContent)
							return
						}
					}

					// CSRF protection: for state-changing requests using cookie auth,
					// require same-origin Origin/Referer, or X-Requested-With.
					if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
						if hasSessionCookie(r) && !isWebhookPath(r.URL.Path) && !isPublicAPIRequest(r.Method, r.URL.Path) {
							origin := strings.TrimSpace(r.Header.Get("Origin"))
							referer := strings.TrimSpace(r.Header.Get("Referer"))
							xrw := strings.TrimSpace(r.Header.Get("X-Requested-With"))

							// If Origin is present, enforce same-origin.
							if origin != "" {
								if !isSameOrigin(r, origin) {
									http.Error(w, "CSRF validation failed (origin mismatch)", http.StatusForbidden)
									return
								}
							} else if referer != "" {
								// Otherwise fall back to Referer check.
								if !isSameOrigin(r, referer) {
									http.Error(w, "CSRF validation failed (referer mismatch)", http.StatusForbidden)
									return
								}
							} else if xrw == "" {
								// No Origin/Referer: require explicit X-Requested-With (AJAX).
								http.Error(w, "CSRF validation failed (missing origin)", http.StatusForbidden)
								return
							}
						}
					}

					// Default-deny: gate all API and plugin routes behind auth,
					// except explicitly listed public endpoints.
					if cfg.Admin.RequireAuth {
						if authHandler == nil {
							http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
							return
						}

						path := r.URL.Path
						isAPIOrPlugin := strings.HasPrefix(path, "/api/") ||
							strings.HasPrefix(path, "/orbpro-key-broker/")

						if isAPIOrPlugin && !isPublicAPIRequest(r.Method, path) {
							minTrust := peers.Standard
							if isAdminOnlyAPIPath(path) {
								minTrust = peers.Admin
							}
							authHandler.RequireAuth(minTrust, func(w http.ResponseWriter, r *http.Request) {
								adminMux.ServeHTTP(w, r)
							})(w, r)
							return
						}
					}
					adminMux.ServeHTTP(w, r)
				}),
			}
			go func() {
				if cfg.Admin.RequireAuth && authHandler != nil {
					log.Infof("Admin interface at %s://%s/admin (requires HD wallet login at /login)", adminScheme, adminAddr)
				} else {
					log.Infof("Admin interface available at %s://%s/admin", adminScheme, adminAddr)
				}
				log.Infof("Peer API available at %s://%s/api/peers", adminScheme, adminAddr)
				log.Infof("Node info API available at %s://%s/api/node/info", adminScheme, adminAddr)
				log.Infof("Module delivery provider descriptor available at %s://%s/api/module-delivery/provider", adminScheme, adminAddr)
				log.Infof("Public data API available at %s://%s/api/v1/data/omm/bulk", adminScheme, adminAddr)
				var err error
				if adminTLS {
					adminServer.TLSConfig = tlsManager.TLSConfig()
					err = adminServer.ListenAndServeTLS("", "")
				} else {
					err = adminServer.ListenAndServe()
				}
				if err != nil && err != http.ErrServerClosed {
					log.Warnf("Admin server error: %v", err)
				}
			}()

			if tlsManager.Mode() == tlsmgr.ModeManaged {
				challengeAddr := strings.TrimSpace(cfg.Admin.HTTPChallengeAddr)
				if challengeAddr == "" {
					challengeAddr = "127.0.0.1:5080"
				}
				httpChallengeServer = &http.Server{
					Addr:    challengeAddr,
					Handler: tlsManager.HTTPHandler(adminAddr),
				}
				go func() {
					if err := httpChallengeServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
						log.Warnf("HTTP challenge server error: %v", err)
					}
				}()
			}
		}
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Info("Shutting down...")

	// Shutdown admin server
	if adminServer != nil {
		adminServer.Shutdown(ctx)
	}
	if httpChallengeServer != nil {
		httpChallengeServer.Shutdown(ctx)
	}
	if storefrontSvc != nil {
		if err := storefrontSvc.Close(); err != nil {
			log.Warnf("Storefront service shutdown error: %v", err)
		}
	}
	if storefrontDelivery != nil {
		storefrontDelivery.Close()
	}
	if storefrontStore != nil {
		if err := storefrontStore.Close(); err != nil {
			log.Warnf("Storefront store close error: %v", err)
		}
	}

	return n.Stop()
}

func loadLandingPage(customPath string) ([]byte, error) {
	if strings.TrimSpace(customPath) == "" {
		return []byte(defaultFrontendHTML), nil
	}

	content, err := os.ReadFile(customPath)
	if err != nil {
		return nil, fmt.Errorf("read admin.homepage_file %q: %w", customPath, err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, fmt.Errorf("admin.homepage_file %q is empty", customPath)
	}
	return content, nil
}

func resolveBuildAssetsDir(homepageFile string) string {
	path := strings.TrimSpace(homepageFile)
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "Build")
}

func publicHomepageFile(frontendPath string, homepageFile string) string {
	// homepage_file is a legacy single-file override. frontend_path supersedes it,
	// so preserve the documented behavior and fall back to the embedded landing page.
	if strings.TrimSpace(frontendPath) != "" {
		return ""
	}
	return strings.TrimSpace(homepageFile)
}

func storefrontSigningKeyFromRaw(raw []byte) (ed25519.PrivateKey, error) {
	switch len(raw) {
	case 0:
		return nil, fmt.Errorf("empty signing key")
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return append(ed25519.PrivateKey(nil), raw...), nil
	default:
		return nil, fmt.Errorf("unexpected signing key length %d", len(raw))
	}
}

func datasetPublicationSigningKey(cfg *config.Config, raw []byte) ([]byte, error) {
	if len(raw) > 0 {
		return storefrontSigningKeyFromRaw(raw)
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	basePath := strings.TrimSpace(cfg.Setup.DataPath)
	if basePath == "" {
		storagePath := strings.TrimSpace(cfg.Storage.Path)
		if storagePath == "" {
			return nil, fmt.Errorf("storage path is required")
		}
		basePath = filepath.Dir(storagePath)
	}

	keyMgr, err := keys.NewManager(basePath)
	if err != nil {
		return nil, fmt.Errorf("create publication key manager: %w", err)
	}
	var identity *keys.Identity
	if keyMgr.HasIdentity() {
		identity, err = keyMgr.LoadIdentity()
	} else {
		identity, err = keyMgr.GenerateIdentity()
	}
	if err != nil {
		return nil, fmt.Errorf("load publication signing identity: %w", err)
	}
	if identity == nil || identity.SigningKey == nil || len(identity.SigningKey.PrivateKey) == 0 {
		return nil, fmt.Errorf("publication signing identity is unavailable")
	}
	return storefrontSigningKeyFromRaw(identity.SigningKey.PrivateKey)
}

func isPublicAPIPath(path string) bool {
	return isPublicAPIRequest(http.MethodGet, path)
}

func isPublicAPIRequest(method string, path string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == http.MethodOptions {
		return isPublicAPIRequest(http.MethodGet, path) ||
			isPublicAPIRequest(http.MethodPost, path)
	}

	if method == http.MethodPost {
		switch path {
		case "/api/auth/challenge", "/api/auth/verify", "/api/storefront/listings/search", "/api/storefront/payments/stripe/webhook":
			return true
		}
		return false
	}

	if method != http.MethodGet && method != http.MethodHead {
		return false
	}

	return isPublicReadAPIPath(path)
}

func isPublicReadAPIPath(path string) bool {
	switch path {
	case "/api/module-delivery/provider",
		"/api/module-delivery/listings",
		"/api/node/info",
		"/api/relay/status",
		"/api/auth/status",
		"/api/storefront/listings",
		"/api/v1/catalog",
		"/api/v1/data/health",
		"/api/v1/data/omm",
		"/api/v1/data/omm/bulk",
		"/api/v1/data/mpe",
		"/api/v1/data/mpe/bulk",
		"/api/v1/data/cat",
		"/api/v1/data/cat/bulk",
		"/api/v1/data/spw/bulk",
		"/api/v1/data/secure/omm",
		"/sdn/libp2p.js":
		return true
	}

	return strings.HasPrefix(path, "/api/directory/") ||
		strings.HasPrefix(path, "/api/v1/demo/") ||
		strings.HasPrefix(path, "/api/storefront/listings/") ||
		strings.HasPrefix(path, "/api/storefront/trust/") ||
		strings.HasPrefix(path, "/api/v1/log/")
}

func isWebhookPath(path string) bool {
	return strings.HasPrefix(path, "/api/storefront/payments/stripe/webhook")
}

func applyPublicAPICORSHeaders(header http.Header, origin string) {
	allowedOrigin := strings.TrimSpace(origin)
	if allowedOrigin == "" {
		allowedOrigin = "*"
	}
	header.Set("Access-Control-Allow-Origin", allowedOrigin)
	header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization")
	header.Set("Vary", "Origin")
}

func hasSessionCookie(r *http.Request) bool {
	if _, err := r.Cookie("sdn_wallet_session"); err == nil {
		return true
	}
	if _, err := r.Cookie("sdn_session"); err == nil {
		return true
	}
	return false
}

func isSameOrigin(r *http.Request, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Hostname() == "" {
		return false
	}

	originHost := strings.ToLower(u.Hostname())
	originPort := u.Port()
	if originPort == "" {
		originPort = defaultPortForScheme(u.Scheme)
	}

	expectedURL, err := url.Parse(u.Scheme + "://" + r.Host)
	if err != nil || expectedURL.Hostname() == "" {
		return false
	}
	expectedHost := strings.ToLower(expectedURL.Hostname())
	expectedPort := expectedURL.Port()
	if expectedPort == "" {
		expectedPort = defaultPortForScheme(u.Scheme)
	}

	return originHost == expectedHost && originPort == expectedPort
}

func defaultPortForScheme(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	if scheme == "http" {
		return "80"
	}
	return ""
}

func isWebSocketUpgradeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !headerHasToken(r.Header.Get("Connection"), "upgrade") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}

func headerHasToken(rawValue string, token string) bool {
	target := strings.ToLower(strings.TrimSpace(token))
	if target == "" {
		return false
	}
	for _, entry := range strings.Split(strings.ToLower(rawValue), ",") {
		if strings.TrimSpace(entry) == target {
			return true
		}
	}
	return false
}

func resolveLocalLibp2pWsProxyTarget(listenAddrs []string) (*url.URL, string) {
	for _, rawAddr := range listenAddrs {
		addr := strings.TrimSpace(rawAddr)
		if addr == "" {
			continue
		}
		if strings.Contains(addr, "/wss") || !strings.Contains(addr, "/ws") {
			continue
		}
		port := extractTCPPortFromMultiaddr(addr)
		if port == "" {
			continue
		}

		target, err := url.Parse("http://127.0.0.1:" + port)
		if err != nil {
			continue
		}
		return target, addr
	}

	return nil, ""
}

func extractTCPPortFromMultiaddr(addr string) string {
	clean := strings.Trim(addr, "/")
	if clean == "" {
		return ""
	}
	parts := strings.Split(clean, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "tcp" {
			continue
		}
		port := strings.TrimSpace(parts[i+1])
		if port != "" {
			return port
		}
	}
	return ""
}

func isAdminOnlyAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/peers") ||
		strings.HasPrefix(path, "/api/groups") ||
		strings.HasPrefix(path, "/api/blocklist") ||
		strings.HasPrefix(path, "/api/settings") ||
		strings.HasPrefix(path, "/api/export") ||
		strings.HasPrefix(path, "/api/import") ||
		strings.HasPrefix(path, "/api/admin/") ||
		strings.HasPrefix(path, "/api/auth/users") ||
		strings.HasPrefix(path, "/api/v0") ||
		strings.HasPrefix(path, "/api/v1/admin/") ||
		path == "/api/v1/data/summary" ||
		path == "/api/v1/data/query" ||
		strings.HasPrefix(path, "/api/v1/data/records/") ||
		strings.HasPrefix(path, "/api/v1/modules/runtime/") ||
		strings.HasPrefix(path, "/api/v1/plugins/") ||
		strings.HasPrefix(path, "/api/routing/") ||
		strings.HasPrefix(path, "/api/streaming/") ||
		strings.HasPrefix(path, "/api/relay/filters") ||
		strings.HasPrefix(path, "/api/storefront/dashboard/admin")
}

func adminLandingHandler(next http.Handler, landingHTML []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=120")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(landingHTML)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveFavicon(w http.ResponseWriter, r *http.Request, candidatePaths []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	for _, candidate := range candidatePaths {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			http.ServeFile(w, r, candidate)
			return
		}
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(defaultFaviconPNG)
	}
}

func normalizeIPFSGatewayCORSHeaders(header http.Header) {
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Content-Type, Range, User-Agent, X-Requested-With")
	header.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, X-Chunked-Output, X-Ipfs-Path, X-Ipfs-Roots, X-Stream-Output")
}

func makeWebUIHandler(buildDir string, _ string) (http.Handler, error) {
	buildDir = strings.TrimSpace(buildDir)
	if buildDir == "" {
		return nil, fmt.Errorf("webui_path is empty")
	}

	indexPath := filepath.Join(buildDir, "index.html")
	if st, err := os.Stat(indexPath); err != nil {
		return nil, fmt.Errorf("webui_path %q: missing index.html: %w", buildDir, err)
	} else if st.IsDir() {
		return nil, fmt.Errorf("webui_path %q: index.html is a directory", buildDir)
	}

	fs := http.FileServer(http.Dir(buildDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		clean = strings.TrimPrefix(clean, "/")
		if clean != "" {
			full := filepath.Join(buildDir, filepath.FromSlash(clean))
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
		}

		if ext := path.Ext(r.URL.Path); ext != "" && r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		http.ServeFile(w, r, indexPath)
	}), nil
}

func resolveAdminUIPath(configuredPath string) string {
	candidates := []string{
		strings.TrimSpace(configuredPath),
		config.DefaultAdminUIPath(),
		"/opt/spacedatanetwork/admin-ui",
		filepath.Join("sdn-js", "ui", "dist"),
		filepath.Join("..", "sdn-js", "ui", "dist"),
		filepath.Join("..", "..", "sdn-js", "ui", "dist"),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(candidate, "index.html"))
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func resolveFrontendPath(configuredPath string) string {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath != "" {
		return configuredPath
	}
	if candidate := firstExistingFrontendPath(defaultFrontendCandidates()); candidate != "" {
		return candidate
	}
	return config.DefaultFrontendPath()
}

func defaultFrontendCandidates() []string {
	return []string{
		filepath.Join("sdn-js", "ui", "dist"),
		filepath.Join("..", "sdn-js", "ui", "dist"),
		filepath.Join("..", "..", "sdn-js", "ui", "dist"),
		filepath.Join("..", "..", "..", "sdn-js", "ui", "dist"),
		"/opt/spacedatanetwork/sdn-ui",
		"/opt/spacedatanetwork/frontend",
		config.DefaultFrontendPath(),
	}
}

func firstExistingFrontendPath(candidates []string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(candidate, "index.html"))
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// provisionFrontendDir creates the frontend directory with a default index.html
// if it doesn't already exist.
func provisionFrontendDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("frontend path is empty")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	indexPath := filepath.Join(dir, "index.html")
	if st, err := os.Stat(indexPath); err == nil {
		if st.IsDir() {
			return fmt.Errorf("%s is a directory", indexPath)
		}
		return nil
	}
	return os.WriteFile(indexPath, []byte(defaultFrontendHTML), 0644)
}

//go:embed default_frontend.html
var defaultFrontendHTML string

// makeFrontendHandler creates a static file server for the public frontend
// directory with SPA fallback and cross-origin isolation headers for OrbPro.
func makeFrontendHandler(frontendDir string) (http.Handler, error) {
	frontendDir = strings.TrimSpace(frontendDir)
	if frontendDir == "" {
		return nil, fmt.Errorf("frontend_path is empty")
	}

	info, err := os.Stat(frontendDir)
	if err != nil {
		return nil, fmt.Errorf("frontend_path %q: %w", frontendDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("frontend_path %q: not a directory", frontendDir)
	}

	indexPath := filepath.Join(frontendDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return nil, fmt.Errorf("frontend_path %q: missing index.html: %w", frontendDir, err)
	}

	fs := http.FileServer(http.Dir(frontendDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Cross-origin isolation for SharedArrayBuffer (required by OrbPro/WASM)
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")

		// Serve index.html with injected config for "/" and "/index.html"
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			injectedHTML, err := loadInjectedFrontendIndex(indexPath)
			if err != nil {
				http.Error(w, "frontend unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(injectedHTML)
			}
			return
		}

		// Serve existing files directly
		clean := path.Clean("/" + r.URL.Path)
		clean = strings.TrimPrefix(clean, "/")
		if clean != "" {
			full := filepath.Join(frontendDir, filepath.FromSlash(clean))
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				w.Header().Set("Cache-Control", "public, max-age=1800")
				fs.ServeHTTP(w, r)
				return
			}
		}

		// Asset paths (have extension) → 404
		if ext := path.Ext(r.URL.Path); ext != "" && r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		// SPA fallback — serve injected index.html
		injectedHTML, err := loadInjectedFrontendIndex(indexPath)
		if err != nil {
			http.Error(w, "frontend unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(injectedHTML)
		}
	}), nil
}

func loadInjectedFrontendIndex(indexPath string) ([]byte, error) {
	indexHTML, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	return injectFrontendConfig(indexHTML), nil
}

func makeFrontendSurfaceHandler(frontendHandler http.Handler, _ *auth.Handler, _ bool) http.Handler {
	serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frontendHandler.ServeHTTP(w, r)
	})
	return serve
}

// injectFrontendConfig injects SDN runtime configuration into index.html.
// This adds a <script> block before the closing </head> tag with the node's
// IPFS peer info so the frontend can connect over libp2p for key exchange.
// Plugin key exchange happens over encrypted IPFS/libp2p, NOT HTTP.
func injectFrontendConfig(html []byte) []byte {
	configScript := []byte(`<script>window.__SDN_CONFIG__={apiBase:"/api/v1",serverBaseUrl:window.location.origin,ipfsDashboardUrl:"/webui/"};</script>`)
	// Try to inject before </head>
	if idx := bytes.Index(html, []byte("</head>")); idx >= 0 {
		result := make([]byte, 0, len(html)+len(configScript))
		result = append(result, html[:idx]...)
		result = append(result, configScript...)
		result = append(result, html[idx:]...)
		return result
	}
	// Fallback: prepend to the whole document
	return append(configScript, html...)
}

// loadLandingPageFallback loads a custom landing page or returns the built-in default.
func loadLandingPageFallback(homepageFile string) []byte {
	html, err := loadLandingPage(homepageFile)
	if err != nil {
		if strings.TrimSpace(homepageFile) != "" {
			log.Warnf("Falling back to built-in landing page: %v", err)
		}
		return []byte(defaultFrontendHTML)
	}
	return html
}

// handleLibp2pJS serves a JavaScript module with the node's raw IP, peer ID,
// and ws:// multiaddr injected at request time. Browsers can load this script
// to connect to the node using the raw IP without DNS resolution.
//
//	GET /sdn/libp2p.js → application/javascript
func handleLibp2pJS(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		peerID := n.PeerID().String()
		addrs := n.ListenAddrs()

		// Find the first public /ip4/<ip>/tcp/<port>/ws multiaddr.
		var wsMultiaddr string
		for _, a := range addrs {
			s := a.String()
			if strings.Contains(s, "/ws") &&
				!strings.Contains(s, "/ip4/127.") &&
				!strings.Contains(s, "/ip6/::1") {
				if !strings.HasSuffix(s, "/p2p/"+peerID) {
					s += "/p2p/" + peerID
				}
				wsMultiaddr = s
				break
			}
		}

		// Collect all listen address strings.
		addrStrings := make([]string, len(addrs))
		for i, a := range addrs {
			addrStrings[i] = a.String()
		}
		addrsJSON, _ := json.Marshal(addrStrings)

		js := fmt.Sprintf(
			`// Auto-generated by SpaceAware SDN server — do not edit.
// Connection parameters injected at request time.
export const SDN_PEER_ID = %q;
export const SDN_WS_MULTIADDR = %q;
export const SDN_LISTEN_ADDRS = %s;
`,
			peerID, wsMultiaddr, addrsJSON)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte(js))
	}
}

// handleNodeInfo returns an HTTP handler that serves the node's public identity info.
// The response is the full EPM JSON with runtime metadata overlaid.
func handleNodeInfo(n *node.Node, torRuntime *tor.Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Start with the full EPM JSON as the base response
		var info map[string]interface{}
		if epmSvc := n.EPMService(); epmSvc != nil {
			info = epmSvc.GetNodeEPMJSON()
		}
		if info == nil {
			info = make(map[string]interface{})
		}
		promoteNodeInfoKeyFields(info)

		// Overlay runtime metadata
		info["peer_id"] = n.PeerID().String()
		info["mode"] = n.Config().Mode
		info["version"] = versioninfo.AgentVersion
		info["agent_version"] = versioninfo.AgentVersion
		info["suite_version"] = versioninfo.SuiteVersion
		info["standards_version"] = versioninfo.SpaceDataStandardsVersion
		info["advertisement_flag"] = versioninfo.CurrentAdvertisementFlag

		addrs := n.ListenAddrs()
		addrStrings := make([]string, len(addrs))
		for i, a := range addrs {
			addrStrings[i] = a.String()
		}
		info["listen_addresses"] = addrStrings

		if torRuntime != nil && torRuntime.OnionHost() != "" {
			info["onion_address"] = torRuntime.OnionHost()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	}
}

func promoteNodeInfoKeyFields(info map[string]interface{}) {
	if info == nil {
		return
	}

	keys, ok := info["keys"]
	if !ok {
		return
	}

	for _, key := range nodeInfoKeyEntries(keys) {
		keyType := strings.ToLower(strings.TrimSpace(nodeInfoStringValue(key["key_type"])))
		if keyType == "" {
			continue
		}

		pubKeyField := keyType + "_pubkey_hex"
		keyPathField := keyType + "_key_path"
		publicKey := nodeInfoStringValue(key["public_key"])
		keyPath := nodeInfoStringValue(key["key_path"])
		if keyPath == "" {
			keyPath = nodeInfoStringValue(key["key_address"])
		}

		if publicKey != "" && nodeInfoStringValue(info[pubKeyField]) == "" {
			info[pubKeyField] = publicKey
		}
		if keyPath != "" && nodeInfoStringValue(info[keyPathField]) == "" {
			info[keyPathField] = keyPath
		}
		if xpub := nodeInfoStringValue(key["xpub"]); xpub != "" && nodeInfoStringValue(info["xpub"]) == "" {
			info["xpub"] = xpub
		}
	}
}

func nodeInfoKeyEntries(raw interface{}) []map[string]interface{} {
	switch keys := raw.(type) {
	case []map[string]interface{}:
		return append([]map[string]interface{}(nil), keys...)
	case []interface{}:
		entries := make([]map[string]interface{}, 0, len(keys))
		for _, entry := range keys {
			if key, ok := entry.(map[string]interface{}); ok {
				entries = append(entries, key)
			}
		}
		return entries
	default:
		return nil
	}
}

func nodeInfoStringValue(value interface{}) string {
	text, _ := value.(string)
	return text
}

type providerDescriptorSource interface {
	PeerID() peer.ID
	ListenAddrs() []multiaddr.Multiaddr
	Host() libp2phost.Host
	EPMService() *epm.Service
}

type providerDescriptorIdentityAddress struct {
	Chain     string `json:"chain"`
	Address   string `json:"address"`
	KeyPath   string `json:"keyPath,omitempty"`
	PublicKey string `json:"publicKey,omitempty"`
}

type providerDescriptorIdentityResponse struct {
	XPub                string                              `json:"xpub,omitempty"`
	IdentityPublicKey   string                              `json:"identityPublicKey,omitempty"`
	SigningPublicKey    string                              `json:"signingPublicKey,omitempty"`
	EncryptionPublicKey string                              `json:"encryptionPublicKey,omitempty"`
	IPNSEntries         []string                            `json:"ipnsEntries,omitempty"`
	ENSNames            []string                            `json:"ensNames,omitempty"`
	Addresses           []providerDescriptorIdentityAddress `json:"addresses,omitempty"`
}

type providerDescriptorResponse struct {
	PublicKey      string                              `json:"publicKey"`
	PeerID         string                              `json:"peerId"`
	IPNS           string                              `json:"ipns,omitempty"`
	RelayAddresses []string                            `json:"relayAddresses,omitempty"`
	Identity       *providerDescriptorIdentityResponse `json:"identity,omitempty"`
}

type moduleDeliveryListingsResult struct {
	PluginID   string `json:"plugin_id,omitempty"`
	Version    string `json:"version,omitempty"`
	DataBase64 string `json:"data_base64"`
	Timestamp  string `json:"timestamp,omitempty"`
}

type moduleDeliveryListingsResponse struct {
	Results []moduleDeliveryListingsResult `json:"results"`
	Count   int                            `json:"count"`
}

func handleProviderDescriptor(src providerDescriptorSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		payload, err := buildProviderDescriptor(src)
		if err != nil {
			http.Error(w, "provider descriptor unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func handleModuleDeliveryListings(reg *license.PluginRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		listings, err := node.BuildModuleDeliveryListings(reg)
		if err != nil {
			http.Error(w, "module-delivery listings unavailable", http.StatusServiceUnavailable)
			return
		}

		results := make([]moduleDeliveryListingsResult, 0, len(listings))
		for _, listing := range listings {
			results = append(results, moduleDeliveryListingsResult{
				PluginID:   listing.PluginID,
				Version:    listing.Version,
				DataBase64: base64.StdEncoding.EncodeToString(listing.Payload),
				Timestamp:  listing.Timestamp,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(moduleDeliveryListingsResponse{
			Results: results,
			Count:   len(results),
		})
	}
}

func handleModuleRuntimeSnapshot(mgr *plugins.Manager, reg *license.PluginRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		snapshot := plugins.RuntimeSnapshot{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Modules:     []plugins.RuntimeModuleEntry{},
		}
		if mgr != nil {
			snapshot = mgr.RuntimeSnapshot()
		}
		mergeModuleRuntimeCatalog(&snapshot, reg)
		snapshot.Count = len(snapshot.Modules)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_ = json.NewEncoder(w).Encode(snapshot)
	}
}

func handleModuleRuntimeMutation(mgr *plugins.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			http.Error(w, "module runtime unavailable", http.StatusServiceUnavailable)
			return
		}

		moduleID, kind, key, ok := parseModuleRuntimeMutationPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		switch kind {
		case "options":
			if r.Method != http.MethodPatch && r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var payload struct {
				Value string `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "invalid option payload", http.StatusBadRequest)
				return
			}
			option, err := mgr.UpdateRuntimeModuleOption(r.Context(), moduleID, key, payload.Value)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(option)
		case "inputs":
			if r.Method != http.MethodPatch && r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var payload struct {
				Values []plugins.RuntimeModuleInputValue `json:"values"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "invalid input payload", http.StatusBadRequest)
				return
			}
			values, err := mgr.SaveRuntimeModuleInputValues(r.Context(), moduleID, payload.Values)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"moduleId":       moduleID,
				"restartPending": true,
				"inputValues":    values,
			})
		case "history":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			history, err := mgr.RuntimeModuleCommandHistory(moduleID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"moduleId": moduleID,
				"history":  history,
			})
		case "schedules":
			if key == "" {
				http.NotFound(w, r)
				return
			}
			if strings.HasSuffix(key, "/run") {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				methodID := strings.TrimSuffix(key, "/run")
				run, err := mgr.RunRuntimeModuleScheduleNow(r.Context(), moduleID, methodID)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-cache")
				_ = json.NewEncoder(w).Encode(run)
				return
			}
			if r.Method != http.MethodPatch && r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var payload plugins.RuntimeModuleScheduleConfig
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "invalid schedule payload", http.StatusBadRequest)
				return
			}
			schedule, err := mgr.SaveRuntimeModuleSchedule(r.Context(), moduleID, key, payload)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(schedule)
		case "actions":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := mgr.RunRuntimeModuleAction(r.Context(), moduleID, key); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":       true,
				"moduleId": moduleID,
				"actionId": key,
			})
		default:
			http.NotFound(w, r)
		}
	}
}

func parseModuleRuntimeMutationPath(pathValue string) (moduleID, kind, key string, ok bool) {
	rest := strings.TrimPrefix(pathValue, "/api/v1/modules/runtime/")
	if rest == pathValue || strings.TrimSpace(rest) == "" {
		return "", "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return "", "", "", false
	}
	decodedModuleID, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", "", false
	}
	kind = strings.TrimSpace(parts[1])
	if kind != "options" && kind != "actions" && kind != "inputs" && kind != "history" && kind != "schedules" {
		return "", "", "", false
	}
	moduleID = strings.TrimSpace(decodedModuleID)
	if kind == "inputs" || kind == "history" {
		if len(parts) != 2 || moduleID == "" {
			return "", "", "", false
		}
		return moduleID, kind, "", true
	}
	if len(parts) < 3 {
		return "", "", "", false
	}
	decodedKey, err := url.PathUnescape(strings.Join(parts[2:], "/"))
	if err != nil {
		return "", "", "", false
	}
	key = strings.TrimSpace(decodedKey)
	if moduleID == "" || key == "" {
		return "", "", "", false
	}
	return moduleID, kind, key, true
}

func mergeModuleRuntimeCatalog(snapshot *plugins.RuntimeSnapshot, reg *license.PluginRegistry) {
	if snapshot == nil || reg == nil {
		return
	}
	seen := make(map[string]int, len(snapshot.Modules))
	for index, module := range snapshot.Modules {
		seen[module.ID] = index
	}
	for _, descriptor := range reg.ListPublic() {
		catalog := &plugins.RuntimeModuleCatalog{
			RequiredScope:   descriptor.RequiredScope,
			ContentType:     descriptor.ContentType,
			CacheControl:    descriptor.CacheControl,
			BundleSHA256:    descriptor.BundleSHA256,
			SizeBytes:       descriptor.SizeBytes,
			SignatureHex:    descriptor.SignatureHex,
			SignerPubKeyHex: descriptor.SignerPubKeyHex,
			UploadedAt:      descriptor.UploadedAt,
		}
		if index, ok := seen[descriptor.ID]; ok {
			snapshot.Modules[index].Catalog = catalog
			if snapshot.Modules[index].Version == "" {
				snapshot.Modules[index].Version = descriptor.Version
			}
			if snapshot.Modules[index].Status == "" || snapshot.Modules[index].Status == "registered" {
				snapshot.Modules[index].Status = descriptor.Status
				snapshot.Modules[index].StatusMessage = descriptor.StatusMessage
			}
			continue
		}
		snapshot.Modules = append(snapshot.Modules, plugins.RuntimeModuleEntry{
			ID:            descriptor.ID,
			Version:       descriptor.Version,
			Status:        descriptor.Status,
			StatusMessage: descriptor.StatusMessage,
			Catalog:       catalog,
		})
	}
}

func buildProviderDescriptor(src providerDescriptorSource) (*providerDescriptorResponse, error) {
	if src == nil {
		return nil, fmt.Errorf("provider descriptor source is nil")
	}

	publicKeyHex, err := providerPublicKeyHex(src.Host(), src.PeerID())
	if err != nil {
		return nil, err
	}

	peerID := src.PeerID().String()
	response := &providerDescriptorResponse{
		PublicKey: publicKeyHex,
		PeerID:    peerID,
		Identity:  buildProviderDescriptorIdentity(src, publicKeyHex, peerID),
	}
	if peerID != "" {
		response.IPNS = "/ipns/" + peerID
	}

	for _, addr := range src.ListenAddrs() {
		if addr == nil {
			continue
		}
		response.RelayAddresses = append(response.RelayAddresses, addr.String())
	}

	return response, nil
}

func buildProviderDescriptorIdentity(src providerDescriptorSource, defaultPublicKeyHex, peerID string) *providerDescriptorIdentityResponse {
	identity := &providerDescriptorIdentityResponse{}
	if strings.TrimSpace(defaultPublicKeyHex) != "" {
		identity.IdentityPublicKey = defaultPublicKeyHex
	}
	if strings.TrimSpace(peerID) != "" {
		identity.IPNSEntries = []string{"/ipns/" + peerID}
	}
	if src == nil || src.EPMService() == nil {
		return identity
	}

	info := src.EPMService().GetNodeEPMJSON()
	if len(info) == 0 {
		return identity
	}

	if xpub := nodeInfoStringValue(info["xpub"]); xpub != "" {
		identity.XPub = xpub
	}
	if value := nodeInfoStringValue(info["identity_pubkey_hex"]); value != "" {
		identity.IdentityPublicKey = value
	}
	if value := nodeInfoStringValue(info["signing_pubkey_hex"]); value != "" {
		identity.SigningPublicKey = value
	}
	if value := nodeInfoStringValue(info["encryption_pubkey_hex"]); value != "" {
		identity.EncryptionPublicKey = value
	}

	identity.IPNSEntries = uniqueTrimmedStrings(append(identity.IPNSEntries, providerDescriptorIPNSEntries(info)...))
	identity.ENSNames = uniqueTrimmedStrings(providerDescriptorENSNames(info))
	identity.Addresses = providerDescriptorIdentityAddresses(info)

	return identity
}

func providerDescriptorIPNSEntries(info map[string]interface{}) []string {
	entries := make([]string, 0)
	for _, value := range nodeInfoStringEntries(info["multiformat_address"]) {
		if strings.HasPrefix(strings.TrimSpace(value), "/ipns/") {
			entries = append(entries, value)
		}
	}
	return entries
}

func providerDescriptorENSNames(info map[string]interface{}) []string {
	candidates := []string{
		nodeInfoStringValue(info["dn"]),
		nodeInfoStringValue(info["legal_name"]),
	}
	candidates = append(candidates, nodeInfoStringEntries(info["alternate_names"])...)
	candidates = append(candidates, nodeInfoStringEntries(info["multiformat_address"])...)

	ensNames := make([]string, 0)
	for _, candidate := range candidates {
		if ensName := normalizeENSName(candidate); ensName != "" {
			ensNames = append(ensNames, ensName)
		}
	}
	return ensNames
}

func providerDescriptorIdentityAddresses(info map[string]interface{}) []providerDescriptorIdentityAddress {
	proofsByChain := make(map[string]providerDescriptorIdentityAddress)
	for _, proof := range nodeInfoObjectEntries(info["chain_proofs"]) {
		chain := strings.ToLower(strings.TrimSpace(nodeInfoStringValue(proof["chain"])))
		if chain == "" {
			continue
		}
		entry := proofsByChain[chain]
		entry.Chain = chain
		if entry.Address == "" {
			entry.Address = nodeInfoStringValue(proof["address"])
		}
		if entry.KeyPath == "" {
			entry.KeyPath = nodeInfoStringValue(proof["key_path"])
		}
		if entry.PublicKey == "" {
			entry.PublicKey = nodeInfoStringValue(proof["public_key"])
		}
		proofsByChain[chain] = entry
	}

	chainOrder := []string{"bitcoin", "ethereum", "solana"}
	addresses := make([]providerDescriptorIdentityAddress, 0, len(chainOrder))
	for _, chain := range chainOrder {
		entry := proofsByChain[chain]
		entry.Chain = chain
		if entry.Address == "" {
			entry.Address = nodeInfoStringValue(info[chain+"_address"])
		}
		if entry.KeyPath == "" {
			entry.KeyPath = nodeInfoStringValue(info[chain+"_key_path"])
		}
		if strings.TrimSpace(entry.Address) == "" {
			continue
		}
		addresses = append(addresses, entry)
	}
	return addresses
}

func nodeInfoObjectEntries(raw interface{}) []map[string]interface{} {
	switch entries := raw.(type) {
	case []map[string]interface{}:
		return append([]map[string]interface{}(nil), entries...)
	case []interface{}:
		ret := make([]map[string]interface{}, 0, len(entries))
		for _, entry := range entries {
			if value, ok := entry.(map[string]interface{}); ok {
				ret = append(ret, value)
			}
		}
		return ret
	default:
		return nil
	}
}

func nodeInfoStringEntries(raw interface{}) []string {
	switch entries := raw.(type) {
	case []string:
		return append([]string(nil), entries...)
	case []interface{}:
		ret := make([]string, 0, len(entries))
		for _, entry := range entries {
			if value, ok := entry.(string); ok {
				ret = append(ret, value)
			}
		}
		return ret
	default:
		return nil
	}
}

func uniqueTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	ret := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		ret = append(ret, trimmed)
	}
	return ret
}

func normalizeENSName(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}

	if parsed, err := url.Parse(trimmed); err == nil && parsed.Hostname() != "" {
		trimmed = strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	}

	trimmed = strings.Trim(trimmed, "[](){}<>\"'.,;")
	if strings.HasSuffix(trimmed, ".eth") {
		return trimmed
	}
	return ""
}

func providerPublicKeyHex(host libp2phost.Host, peerID peer.ID) (string, error) {
	if host == nil {
		return "", fmt.Errorf("provider host is required")
	}
	if peerID == "" {
		return "", fmt.Errorf("provider peer id is required")
	}

	pubKey := host.Peerstore().PubKey(peerID)
	if pubKey == nil {
		var err error
		pubKey, err = peerID.ExtractPublicKey()
		if err != nil {
			return "", fmt.Errorf("extract provider public key: %w", err)
		}
	}
	raw, err := pubKey.Raw()
	if err != nil {
		return "", fmt.Errorf("marshal provider public key: %w", err)
	}
	compressed, err := normalizeCompressedSecp256k1PublicKey(raw)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(compressed), nil
}

func normalizeCompressedSecp256k1PublicKey(raw []byte) ([]byte, error) {
	if len(raw) != 33 {
		return nil, fmt.Errorf("expected 33-byte compressed secp256k1 public key, got %d bytes", len(raw))
	}
	pubKey, err := secp256k1.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key: %w", err)
	}
	return pubKey.SerializeCompressed(), nil
}

// handleRelayStatus returns relay connection load for client-side load balancing.
func handleRelayStatus(n *node.Node) http.HandlerFunc {
	type relayStatusResponse struct {
		PeerID            string  `json:"peer_id"`
		Connections       int     `json:"connections"`
		ConfiguredNodes   int     `json:"configured_nodes"`
		MaxConnections    int     `json:"max_connections"`
		Load              float64 `json:"load"`
		Mode              string  `json:"mode"`
		Version           string  `json:"version"`
		AgentVersion      string  `json:"agent_version"`
		SuiteVersion      string  `json:"suite_version"`
		StandardsVersion  string  `json:"standards_version"`
		AdvertisementFlag string  `json:"advertisement_flag"`
		UptimeSeconds     int64   `json:"uptime_seconds"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		peers := n.Host().Network().Peers()
		maxConns := n.Config().Network.MaxConns
		if maxConns <= 0 {
			maxConns = 1000
		}

		load := float64(len(peers)) / float64(maxConns)
		if load > 1.0 {
			load = 1.0
		}

		status := relayStatusResponse{
			PeerID:            n.PeerID().String(),
			Connections:       len(peers),
			ConfiguredNodes:   configuredSDNSSHNodeCount(),
			MaxConnections:    maxConns,
			Load:              load,
			Mode:              n.Config().Mode,
			Version:           versioninfo.AgentVersion,
			AgentVersion:      versioninfo.AgentVersion,
			SuiteVersion:      versioninfo.SuiteVersion,
			StandardsVersion:  versioninfo.SpaceDataStandardsVersion,
			AdvertisementFlag: versioninfo.CurrentAdvertisementFlag,
			UptimeSeconds:     int64(time.Since(processStartTime).Seconds()),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}
}

func configuredSDNSSHNodeCount() int {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return 0
	}
	return countConfiguredSDNSSHHostStanzas(filepath.Join(home, ".ssh", "config"))
}

func countConfiguredSDNSSHHostStanzas(configPath string) int {
	file, err := os.Open(configPath)
	if err != nil {
		return 0
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || !strings.EqualFold(fields[0], "host") {
			continue
		}
		for _, alias := range fields[1:] {
			if isConfiguredSDNSSHAlias(alias) {
				count++
				break
			}
		}
	}
	return count
}

func isConfiguredSDNSSHAlias(alias string) bool {
	alias = strings.TrimSpace(alias)
	if alias == "" || strings.ContainsAny(alias, "*?") {
		return false
	}
	return strings.HasPrefix(alias, "space-data-network-") ||
		alias == "sdn.spaceaware.io" ||
		alias == "celestrak.eth"
}

// handleNodeEPMJSON returns the node's EPM as JSON.
func handleNodeEPMJSON(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		epmSvc := n.EPMService()
		if epmSvc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}

		epmJSON := epmSvc.GetNodeEPMJSON()
		if epmJSON == nil {
			http.Error(w, "no EPM available", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(epmJSON)
	}
}

// handleNodeEPMVCard returns the node's EPM as a vCard 4.0 string.
func handleNodeEPMVCard(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		epmSvc := n.EPMService()
		if epmSvc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}

		vcardStr, err := epmSvc.GetNodeVCard()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/vcard")
		w.Header().Set("Content-Disposition", "attachment; filename=node.vcf")
		w.Write([]byte(vcardStr))
	}
}

// handleNodeEPMQR returns a QR code PNG of the node's vCard.
func handleNodeEPMQR(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		epmSvc := n.EPMService()
		if epmSvc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}

		qrData, err := epmSvc.GetNodeQR(256)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "image/png")
		w.Write(qrData)
	}
}

// handleNodeEPM handles GET (binary EPM) and PUT (update profile) for the node's EPM.
func handleNodeEPM(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		epmSvc := n.EPMService()
		if epmSvc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}

		switch r.Method {
		case http.MethodGet:
			epmData := epmSvc.GetNodeEPM()
			if epmData == nil {
				http.Error(w, "no EPM available", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/x-flatbuffers")
			w.Write(epmData)

		case http.MethodPut:
			var profile epm.Profile
			if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := epmSvc.UpdateProfile(&profile); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := n.IndexLocalNodeEPM(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := epmSvc.PublishEPM(r.Context(), n); err != nil {
				log.Warnf("Failed to publish updated EPM PNM: %v", err)
			}
			epmData := epmSvc.GetNodeEPM()
			if epmData == nil {
				http.Error(w, "no EPM available", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/x-flatbuffers")
			w.Write(epmData)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handlePeerGraph returns the current peer graph as JSON.
func handlePeerGraph(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		data, err := epm.GraphSnapshotJSON(n.Host(), n.PeerRegistry())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// handleObservedSDNPeers returns the SDN-only peer list consumed by the root dashboard.
func handleObservedSDNPeers(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		snapshot := epm.BuildGraphSnapshot(n.Host(), n.PeerRegistry())
		var registryPeers []*peers.TrustedPeer
		if registry := n.PeerRegistry(); registry != nil {
			registryPeers = registry.ListPeers()
		}
		data, err := json.Marshal(epm.BuildObservedSDNPeers(
			snapshot,
			registryPeers,
			n.SDNAdvertisementFlagsByPeer(),
			n.SDNAdvertisementAddrsByPeer(),
		))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// handlePeerGraphSchema serves the PGR.fbs schema file.
func handlePeerGraphSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(epm.PGRSchema))
}

var defaultFaviconPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x04, 0x00, 0x00, 0x00, 0xb5, 0x1c, 0x0c, 0x02, 0x00, 0x00, 0x00,
	0x0b, 0x49, 0x44, 0x41, 0x54, 0x78, 0xda, 0x63, 0xfc, 0xff, 0x1f, 0x00,
	0x03, 0x03, 0x02, 0x00, 0xef, 0xbc, 0x7f, 0x44, 0x00, 0x00, 0x00, 0x00,
	0x49, 0x45, 0x4e, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

func runInit(cmd *cobra.Command, args []string) error {
	cfg := config.Default()

	if err := config.Save(configPath, cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	log.Infof("Initialized SDN configuration at %s", config.DefaultPath())
	return nil
}

func runReindex(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize schema validator: %w", err)
	}

	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()

	summary, err := store.RebuildIndex()
	if err != nil {
		return fmt.Errorf("reindex failed: %w", err)
	}

	var total int64
	for schema, count := range summary {
		total += count
		log.Infof("Indexed %d records for %s", count, schema)
	}
	log.Infof("Reindex complete: %d total records indexed", total)

	return nil
}

func runDeriveXPub(cmd *cobra.Command, args []string) error {
	// Resolve WASM path
	wp := strings.TrimSpace(wasmPath)
	if wp == "" {
		wp = os.Getenv("HD_WALLET_WASM_PATH")
	}
	if wp == "" {
		wp = "../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm"
	}
	if _, err := os.Stat(wp); err != nil {
		return fmt.Errorf("hd-wallet-wasi.wasm not found at %q (set --wasm or HD_WALLET_WASM_PATH)", wp)
	}

	ctx := context.Background()
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	// Read mnemonic from stdin
	fmt.Fprint(os.Stderr, "Enter your BIP-39 mnemonic phrase: ")
	reader := bufio.NewReader(os.Stdin)
	mnemonic, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read mnemonic: %w", err)
	}
	mnemonic = strings.TrimSpace(mnemonic)
	if mnemonic == "" {
		return fmt.Errorf("mnemonic cannot be empty")
	}

	valid, err := hw.ValidateMnemonic(ctx, mnemonic)
	if err != nil {
		return fmt.Errorf("failed to validate mnemonic: %w", err)
	}
	if !valid {
		return fmt.Errorf("invalid mnemonic phrase")
	}

	// Derive seed
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return fmt.Errorf("failed to derive seed: %w", err)
	}

	// Derive standard BIP-32 xpub at m/44'/0'/0' (account 0)
	xpubStr, err := hw.DeriveXPub(ctx, seed, 0)
	if err != nil {
		return fmt.Errorf("failed to derive xpub: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\n--- SDN Identity ---\n")
	fmt.Fprintf(os.Stderr, "XPub (BIP-32):     %s\n", xpubStr)
	fmt.Fprintf(os.Stderr, "\nAdd to config.yaml:\n")
	fmt.Fprintf(os.Stderr, "users:\n  - xpub: \"%s\"\n    trust_level: \"admin\"\n    name: \"Operator\"\n", xpubStr)

	// Print just the xpub to stdout (for scripting)
	fmt.Println(xpubStr)

	return nil
}

func runShowIdentity(cmd *cobra.Command, args []string) error {
	// Load config for storage path and key password
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Resolve key password: env > config > machine default
	keyPassword := os.Getenv("SDN_KEY_PASSWORD")
	if keyPassword == "" {
		keyPassword = cfg.Security.KeyPassword
	}
	if keyPassword == "" {
		keyPassword = keys.DeriveDefaultPassword()
	}

	// Locate mnemonic file
	keyDir := filepath.Join(filepath.Dir(cfg.Storage.Path), "keys")
	mnemonicPath := filepath.Join(keyDir, "mnemonic")

	data, err := os.ReadFile(mnemonicPath)
	if err != nil {
		return fmt.Errorf("failed to read mnemonic file %s: %w", mnemonicPath, err)
	}

	// Decrypt if encrypted, otherwise use as-is
	var mnemonic string
	if keys.IsMnemonicEncrypted(data) {
		mnemonic, err = keys.DecryptMnemonic(data, keyPassword)
		if err != nil {
			return fmt.Errorf("failed to decrypt mnemonic (wrong password?): %w", err)
		}
	} else {
		mnemonic = string(data)
	}

	// Resolve WASM path
	wp := strings.TrimSpace(wasmPath)
	if wp == "" {
		wp = os.Getenv("HD_WALLET_WASM_PATH")
	}
	if wp == "" {
		wp = "../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm"
	}
	if _, err := os.Stat(wp); err != nil {
		return fmt.Errorf("hd-wallet-wasi.wasm not found at %q (set --wasm or HD_WALLET_WASM_PATH)", wp)
	}

	ctx := context.Background()
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	// Derive seed from mnemonic
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return fmt.Errorf("failed to derive seed: %w", err)
	}

	// Derive identity (account 0)
	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		return fmt.Errorf("failed to derive identity: %w", err)
	}

	// Derive xpub
	xpubStr, err := hw.DeriveXPub(ctx, seed, 0)
	if err != nil {
		return fmt.Errorf("failed to derive xpub: %w", err)
	}

	info := identity.Info()

	fmt.Fprintf(os.Stderr, "\n--- SDN Node Identity ---\n")
	fmt.Fprintf(os.Stderr, "PeerID:         %s\n", info.PeerID)
	fmt.Fprintf(os.Stderr, "XPub:           %s\n", xpubStr)
	fmt.Fprintf(os.Stderr, "Signing Key:    %s  (path: %s)\n", info.SigningPubKeyHex, info.SigningKeyPath)
	fmt.Fprintf(os.Stderr, "Encryption Key: %s  (path: %s)\n", info.EncryptionPubHex, info.EncryptionKeyPath)
	fmt.Fprintf(os.Stderr, "Identity Path:  %s\n", info.IdentityKeyPath)
	fmt.Fprintf(os.Stderr, "Mnemonic File:  %s\n", mnemonicPath)

	if showMnemonic {
		fmt.Fprintf(os.Stderr, "\n*** MNEMONIC (SENSITIVE — DO NOT SHARE) ***\n")
		fmt.Fprintf(os.Stderr, "%s\n", mnemonic)
	}

	// Print PeerID to stdout (for scripting)
	fmt.Println(info.PeerID)

	return nil
}
