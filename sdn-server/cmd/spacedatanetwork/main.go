// Package main provides the entry point for the Space Data Network server.
// This is a specialized fork of IPFS (Kubo) tailored for space data standards.
package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

	logging "github.com/ipfs/go-log/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/frontend"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storefront"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

var log = logging.Logger("sdn")

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
	deriveXPubCmd.Flags().StringVar(&wasmPath, "wasm", "", "path to hd-wallet.wasm (default: $HD_WALLET_WASM_PATH or ../../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm)")
	showIdentityCmd.Flags().BoolVar(&showMnemonic, "show-mnemonic", false, "display the decrypted mnemonic phrase (SENSITIVE)")
	showIdentityCmd.Flags().StringVar(&wasmPath, "wasm", "", "path to hd-wallet.wasm")

	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(initCmd)
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
	// Resolve empty frontend path to standard location
	if strings.TrimSpace(cfg.Admin.FrontendPath) == "" {
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
	var authHandler *auth.Handler
	var storefrontSvc *storefront.Service
	var storefrontStore *storefront.Store
	var storefrontDelivery *storefront.DeliveryService
	if cfg.Admin.Enabled {
		adminUI, err := peers.NewAdminUI(n.PeerRegistry(), n.PeerGater())
		if err != nil {
			log.Warnf("Failed to create admin UI: %v", err)
		} else {
			adminAddr := cfg.Admin.ListenAddr
			if adminAddr == "" {
				adminAddr = "127.0.0.1:5001"
			}
			adminTLS := cfg.Admin.TLSEnabled
			adminCertFile := strings.TrimSpace(cfg.Admin.TLSCertFile)
			adminKeyFile := strings.TrimSpace(cfg.Admin.TLSKeyFile)
			if adminTLS && (adminCertFile == "" || adminKeyFile == "") {
				return fmt.Errorf("admin TLS is enabled but tls_cert_file or tls_key_file is empty")
			}

			adminScheme := "http"
			if adminTLS {
				adminScheme = "https"
			}
			adminMux := http.NewServeMux()

			// Plugin routes
			if n.PluginManager() != nil {
				n.PluginManager().RegisterRoutes(adminMux)
			}
			tokenVerifier := n.TokenVerifier()
			if tokenVerifier != nil {
				log.Infof("License verification API available at %s://%s/api/v1/license/verify", adminScheme, adminAddr)
				log.Infof("License entitlement admin API available at %s://%s/api/v1/license/entitlements", adminScheme, adminAddr)
				log.Infof("Plugin manifest API available at %s://%s/api/v1/plugins/manifest", adminScheme, adminAddr)
			}

			// Data API routes
			dataAPI := api.NewDataQueryHandler(n.Store(), tokenVerifier)
			dataAPI.RegisterRoutes(adminMux)

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
					gwProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
						http.Error(w, "upstream IPFS gateway unavailable", http.StatusBadGateway)
					}
					adminMux.Handle("/ipfs/", gwProxy)
					log.Infof("Proxying /ipfs/* to %s", rawGWURL)
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
					sfSvc, err := storefront.NewService(sfStore, n.PeerID().String(), nil, nil)
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

			// Node info API endpoint
			adminMux.HandleFunc("/api/node/info", handleNodeInfo(n))

			// EPM (Entity Profile Message) API endpoints
			adminMux.HandleFunc("/api/node/epm/json", handleNodeEPMJSON(n))
			adminMux.HandleFunc("/api/node/epm/vcard", handleNodeEPMVCard(n))
			adminMux.HandleFunc("/api/node/epm/qr", handleNodeEPMQR(n))
			adminMux.HandleFunc("/api/node/epm", handleNodeEPM(n))

			// Peer graph API endpoints
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
				authHandler.RegisterRoutes(adminMux)
				log.Infof("HD wallet authentication enabled at %s://%s/login", adminScheme, adminAddr)

				// Serve wallet-ui static files if configured
				if walletUIPath := strings.TrimSpace(cfg.Admin.WalletUIPath); walletUIPath != "" {
					adminMux.Handle("/wallet-ui/", http.StripPrefix("/wallet-ui/", http.FileServer(http.Dir(walletUIPath))))
					log.Infof("Wallet UI served at %s://%s/wallet-ui/ from %s", adminScheme, adminAddr, walletUIPath)
				}

				// Discover wallet-ui assets and pass to admin UI for the Wallet tab
				auth.DiscoverWalletAssets(cfg.Admin.WalletUIPath)
				if jsFile, cssFile := auth.WalletAssets(); jsFile != "" {
					adminUI.SetWalletAssets(jsFile, cssFile)
				}
			}

			// ----------------------------------------------------------------
			// Plugin upload API (admin-only, requires auth + license plugin)
			// ----------------------------------------------------------------
			if authHandler != nil {
				if licSvc := n.LicenseService(); licSvc != nil {
					if reg := licSvc.PluginRegistry(); reg != nil {
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
			}

			// ----------------------------------------------------------------
			// Frontend management API (admin-only)
			// ----------------------------------------------------------------
			frontendMgr := frontend.NewManager(cfg.Admin.FrontendPath)
			frontendMgr.RegisterRoutes(adminMux)
			log.Infof("Frontend manager at %s://%s/api/admin/frontend/ (dir: %s)", adminScheme, adminAddr, cfg.Admin.FrontendPath)

			// ----------------------------------------------------------------
			// Admin panel at /admin — IPFS WebUI (if configured) behind admin auth
			// ----------------------------------------------------------------
			if cfg.Admin.RequireAuth {
				if authHandler == nil {
					return fmt.Errorf("admin authentication required but handler is unavailable")
				}
				if webuiPath := strings.TrimSpace(cfg.Admin.WebuiPath); webuiPath != "" {
					webuiHandler, err := makeWebUIHandler(webuiPath, "/admin")
					if err != nil {
						log.Warnf("IPFS WebUI disabled at /admin: %v", err)
						adminMux.HandleFunc("/admin", authHandler.RequireAuth(peers.Admin, adminUI.ServeHTTP))
						adminMux.HandleFunc("/admin/", authHandler.RequireAuth(peers.Admin, adminUI.ServeHTTP))
					} else {
						// Redirect /admin → /admin/ so the React SPA's relative asset
						// paths (homepage: "./") resolve under /admin/ not site root.
						adminMux.HandleFunc("/admin", authHandler.RequireAuth(peers.Admin, func(w http.ResponseWriter, r *http.Request) {
							http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
						}))
						adminMux.HandleFunc("/admin/", authHandler.RequireAuth(peers.Admin, http.StripPrefix("/admin", webuiHandler).ServeHTTP))
						log.Infof("IPFS WebUI at %s://%s/admin (requires admin auth) from %s", adminScheme, adminAddr, webuiPath)
					}
				} else {
					adminMux.HandleFunc("/admin", authHandler.RequireAuth(peers.Admin, adminUI.ServeHTTP))
					adminMux.HandleFunc("/admin/", authHandler.RequireAuth(peers.Admin, adminUI.ServeHTTP))
				}
			} else {
				// No auth: admin panel open (local development mode)
				adminMux.HandleFunc("/admin", adminUI.ServeHTTP)
				adminMux.HandleFunc("/admin/", adminUI.ServeHTTP)
			}

			// ----------------------------------------------------------------
			// Public frontend at / — configurable static file server
			// ----------------------------------------------------------------
			if frontendPath := strings.TrimSpace(cfg.Admin.FrontendPath); frontendPath != "" {
				frontendHandler, err := makeFrontendHandler(frontendPath)
				if err != nil {
					log.Warnf("Frontend disabled (falling back to built-in landing): %v", err)
					landingHTML := loadLandingPageFallback(cfg.Admin.HomepageFile)
					adminMux.Handle("/", adminLandingHandler(adminUI, landingHTML))
				} else {
					adminMux.Handle("/", frontendHandler)
					log.Infof("Public frontend at %s://%s/ from %s", adminScheme, adminAddr, frontendPath)
				}
			} else {
				landingHTML := loadLandingPageFallback(cfg.Admin.HomepageFile)
				if buildAssetsDir := resolveBuildAssetsDir(cfg.Admin.HomepageFile); buildAssetsDir != "" {
					adminMux.Handle("/Build/", http.StripPrefix("/Build/", http.FileServer(http.Dir(buildAssetsDir))))
					log.Infof("Static build assets at %s://%s/Build/ from %s", adminScheme, adminAddr, buildAssetsDir)
				}
				adminMux.Handle("/", adminLandingHandler(adminUI, landingHTML))
			}

			adminServer = &http.Server{
				Addr:              adminAddr,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      60 * time.Second,
				IdleTimeout:       120 * time.Second,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Global security headers on ALL responses
					w.Header().Set("X-Content-Type-Options", "nosniff")
					w.Header().Set("X-Frame-Options", "DENY")
					w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

					// Cross-origin isolation headers are set by the frontend handler
					// (makeFrontendHandler) for OrbPro routes that need SharedArrayBuffer.
					if adminTLS {
						w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
					}

					// CSRF protection: for state-changing requests using cookie auth,
					// require same-origin Origin/Referer, or X-Requested-With.
					if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
						if hasSessionCookie(r) && !isWebhookPath(r.URL.Path) && !isPublicAPIPath(r.URL.Path) {
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

						if isAPIOrPlugin && !isPublicAPIPath(path) {
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
				log.Infof("Public data API available at %s://%s/api/v1/data/omm", adminScheme, adminAddr)
				var err error
				if adminTLS {
					err = adminServer.ListenAndServeTLS(adminCertFile, adminKeyFile)
				} else {
					err = adminServer.ListenAndServe()
				}
				if err != nil && err != http.ErrServerClosed {
					log.Warnf("Admin server error: %v", err)
				}
			}()
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
		return []byte(defaultLandingPageHTML), nil
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

func isPublicAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/data/") ||
		strings.HasPrefix(path, "/api/v1/license/") ||
		strings.HasPrefix(path, "/api/v1/plugins/manifest") ||
		strings.HasPrefix(path, "/api/v1/demo/") ||
		strings.HasPrefix(path, "/api/storefront/payments/stripe/webhook") ||
		strings.HasPrefix(path, "/api/storefront/listings") ||
		strings.HasPrefix(path, "/api/storefront/reviews") ||
		strings.HasPrefix(path, "/api/storefront/trust/") ||
		strings.HasPrefix(path, "/api/auth/") ||
		strings.HasPrefix(path, "/api/node/info") ||
		strings.HasPrefix(path, "/api/v0/") ||
		strings.HasPrefix(path, "/ipfs/") ||
		strings.HasPrefix(path, "/orbpro-key-broker/v1/orbpro/public-key") ||
		strings.HasPrefix(path, "/orbpro-key-broker/v1/orbpro/key") ||
		path == "/api/v0" ||
		path == "/sdn/libp2p.js"
}

func isWebhookPath(path string) bool {
	return strings.HasPrefix(path, "/api/storefront/payments/stripe/webhook")
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

func isAdminOnlyAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/peers") ||
		strings.HasPrefix(path, "/api/groups") ||
		strings.HasPrefix(path, "/api/blocklist") ||
		strings.HasPrefix(path, "/api/settings") ||
		strings.HasPrefix(path, "/api/export") ||
		strings.HasPrefix(path, "/api/import") ||
		strings.HasPrefix(path, "/api/admin/") ||
		path == "/api/v1/plugins/upload"
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

// provisionFrontendDir creates the frontend directory with a default index.html
// if it doesn't already exist.
func provisionFrontendDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil // already exists
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	indexPath := filepath.Join(dir, "index.html")
	return os.WriteFile(indexPath, []byte(defaultFrontendHTML), 0644)
}

const defaultFrontendHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Space Data Network Node</title>
  <style>
    body { margin:0; font-family:system-ui,sans-serif; background:#0b1020; color:#e6edf6; }
    main { max-width:760px; margin:6rem auto; padding:0 1rem; }
    h1 { margin:0 0 .5rem; font-size:2rem; }
    p { color:#a6b0c3; line-height:1.5; }
    .card { margin-top:1.5rem; background:#11182c; border:1px solid #27314d; border-radius:10px; padding:1rem; }
    a { color:#7ec8ff; text-decoration:none; }
    code { background:#18233e; border:1px solid #27314d; border-radius:6px; padding:.15rem .35rem; }
  </style>
</head>
<body>
  <main>
    <h1>Space Data Network Node</h1>
    <p>This node is online. Customize this page from the <a href="/admin">admin panel</a>.</p>
    <div class="card">
      <p><a href="/api/v1/data/health">GET /api/v1/data/health</a></p>
      <p><a href="/api/v1/data/omm?format=json&amp;limit=5">GET /api/v1/data/omm</a></p>
      <p><a href="/admin">Admin Panel</a></p>
    </div>
  </main>
</body>
</html>`

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

	// Read index.html and inject key broker URL configuration
	indexHTML, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("frontend_path %q: read index.html: %w", frontendDir, err)
	}
	injectedHTML := injectFrontendConfig(indexHTML)

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
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(injectedHTML)
		}
	}), nil
}

// injectFrontendConfig injects SDN runtime configuration into index.html.
// This adds a <script> block before the closing </head> tag with the node's
// IPFS peer info so the frontend can connect over libp2p for key exchange.
// Plugin key exchange happens over encrypted IPFS/libp2p, NOT HTTP.
func injectFrontendConfig(html []byte) []byte {
	configScript := []byte(`<script>window.__SDN_CONFIG__={apiBase:"/api/v1"};</script>`)
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
		return []byte(defaultLandingPageHTML)
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
func handleNodeInfo(n *node.Node) http.HandlerFunc {
	type nodeInfoResponse struct {
		PeerID            string              `json:"peer_id"`
		ListenAddresses   []string            `json:"listen_addresses"`
		SigningPubKeyHex  string              `json:"signing_pubkey_hex,omitempty"`
		EncryptionPubHex  string              `json:"encryption_pubkey_hex,omitempty"`
		SigningKeyPath    string              `json:"signing_key_path,omitempty"`
		EncryptionKeyPath string              `json:"encryption_key_path,omitempty"`
		Addresses         *wasm.CoinAddresses `json:"addresses,omitempty"`
		XPub              string              `json:"xpub,omitempty"`
		HasEPM            bool                `json:"has_epm"`
		Mode              string              `json:"mode"`
		Version           string              `json:"version"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		addrs := n.ListenAddrs()
		addrStrings := make([]string, len(addrs))
		for i, a := range addrs {
			addrStrings[i] = a.String()
		}

		info := nodeInfoResponse{
			PeerID:          n.PeerID().String(),
			ListenAddresses: addrStrings,
			Mode:            n.Config().Mode,
			Version:         "spacedatanetwork/1.0.0",
		}

		if identity := n.Identity(); identity != nil {
			idInfo := identity.Info()
			info.SigningPubKeyHex = idInfo.SigningPubKeyHex
			info.EncryptionPubHex = idInfo.EncryptionPubHex
			info.SigningKeyPath = idInfo.SigningKeyPath
			info.EncryptionKeyPath = idInfo.EncryptionKeyPath
			info.Addresses = idInfo.Addresses
		}

		if epmSvc := n.EPMService(); epmSvc != nil {
			info.HasEPM = epmSvc.GetNodeEPM() != nil
			if profile := epmSvc.GetNodeProfile(); profile != nil {
				// xpub is embedded in the EPM JSON; expose at top level too
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	}
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(epmSvc.GetNodeEPMJSON())

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

// handlePeerGraphSchema serves the PGR.fbs schema file.
func handlePeerGraphSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(epm.PGRSchema))
}

const defaultLandingPageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>SpaceAware API</title>
  <style>
    body {
      margin: 0;
      font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif;
      background: #0b1020;
      color: #e6edf6;
    }
    main {
      max-width: 760px;
      margin: 6rem auto;
      padding: 0 1rem;
    }
    h1 { margin: 0 0 .5rem 0; font-size: 2rem; }
    p { color: #a6b0c3; line-height: 1.5; }
    .card {
      margin-top: 1.5rem;
      background: #11182c;
      border: 1px solid #27314d;
      border-radius: 10px;
      padding: 1rem;
    }
    a {
      color: #7ec8ff;
      text-decoration: none;
    }
    code {
      background: #18233e;
      border: 1px solid #27314d;
      border-radius: 6px;
      padding: .15rem .35rem;
    }
  </style>
</head>
<body>
  <main>
    <h1>SpaceAware API is online</h1>
    <p>This origin serves Space Data Network APIs over HTTPS.</p>
    <div class="card">
      <p><a href="/api/v1/data/health">GET /api/v1/data/health</a></p>
      <p><a href="/api/v1/data/omm?norad_cat_id=25544&amp;day=2026-02-11&amp;limit=5">GET /api/v1/data/omm</a> (FlatBuffers default)</p>
      <p><a href="/api/v1/data/omm?norad_cat_id=25544&amp;day=2026-02-11&amp;limit=5&amp;format=json">GET /api/v1/data/omm?format=json</a></p>
      <p><a href="/api/v1/data/cat?norad_cat_id=25544&amp;limit=1&amp;format=json">GET /api/v1/data/cat?format=json</a></p>
    </div>
  </main>
</body>
</html>`

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
		wp = "../../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm"
	}
	if _, err := os.Stat(wp); err != nil {
		return fmt.Errorf("hd-wallet.wasm not found at %q (set --wasm or HD_WALLET_WASM_PATH)", wp)
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
		wp = "../../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm"
	}
	if _, err := os.Stat(wp); err != nil {
		return fmt.Errorf("hd-wallet.wasm not found at %q (set --wasm or HD_WALLET_WASM_PATH)", wp)
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
