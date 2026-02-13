// Package main provides the entry point for the Space Data Network server.
// This is a specialized fork of IPFS (Kubo) tailored for space data standards.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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

var (
	configPath string
	listenAddr string
	debug      bool
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "enable debug logging")

	daemonCmd.Flags().StringVarP(&listenAddr, "listen", "l", "", "override listen address")

	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(reindexCmd)
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
						sfPayment := storefront.NewPaymentProcessor(sfStore, n.PeerID().String())
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

				authHandler = auth.NewHandler(userStore, sessionStore, sessionTTL, cfg.Admin.WalletUIPath)
				authHandler.RegisterRoutes(adminMux)
				log.Infof("HD wallet authentication enabled at %s://%s/login", adminScheme, adminAddr)

				// Serve wallet-ui static files if configured
				if walletUIPath := strings.TrimSpace(cfg.Admin.WalletUIPath); walletUIPath != "" {
					adminMux.Handle("/wallet-ui/", http.StripPrefix("/wallet-ui/", http.FileServer(http.Dir(walletUIPath))))
					log.Infof("Wallet UI served at %s://%s/wallet-ui/ from %s", adminScheme, adminAddr, walletUIPath)
				}
			}

			// Static build assets (OrbPro, Cesium, etc.)
			landingHTML, err := loadLandingPage(cfg.Admin.HomepageFile)
			if err != nil {
				log.Warnf("Falling back to built-in landing page: %v", err)
				landingHTML = []byte(defaultLandingPageHTML)
			}
			if buildAssetsDir := resolveBuildAssetsDir(cfg.Admin.HomepageFile); buildAssetsDir != "" {
				adminMux.Handle("/Build/", http.StripPrefix("/Build/", http.FileServer(http.Dir(buildAssetsDir))))
				log.Infof("Static build assets served at %s://%s/Build/ from %s", adminScheme, adminAddr, buildAssetsDir)
			}

			// Admin panel — gated by auth if RequireAuth is set
			if cfg.Admin.RequireAuth {
				if authHandler == nil {
					return fmt.Errorf("admin authentication required but handler is unavailable")
				}
				adminMux.HandleFunc("/admin", authHandler.RequireAuth(peers.Admin, adminUI.ServeHTTP))
				adminMux.HandleFunc("/admin/", authHandler.RequireAuth(peers.Admin, adminUI.ServeHTTP))
			} else {
				// No auth: admin panel open (local development mode)
			}

			// Landing page (catch-all at root)
			adminMux.Handle("/", adminLandingHandler(adminUI, landingHTML))

			adminServer = &http.Server{
				Addr:              adminAddr,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      60 * time.Second,
				IdleTimeout:       120 * time.Second,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Global security headers on ALL responses
					w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
					w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
					w.Header().Set("X-Content-Type-Options", "nosniff")
					w.Header().Set("X-Frame-Options", "DENY")
					w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
					if adminTLS {
						w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
					}

					// CSRF protection: for state-changing requests using cookie auth,
					// require Origin/Referer or X-Requested-With header.
					if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
						if _, cookieErr := r.Cookie("sdn_session"); cookieErr == nil {
							origin := r.Header.Get("Origin")
							xrw := r.Header.Get("X-Requested-With")
							if origin == "" && xrw == "" {
								referer := r.Header.Get("Referer")
								if referer == "" && !isWebhookPath(r.URL.Path) {
									http.Error(w, "CSRF validation failed", http.StatusForbidden)
									return
								}
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
		strings.HasPrefix(path, "/api/storefront/payments/stripe/webhook") ||
		strings.HasPrefix(path, "/api/storefront/listings") ||
		strings.HasPrefix(path, "/api/storefront/reviews") ||
		strings.HasPrefix(path, "/api/storefront/trust/") ||
		strings.HasPrefix(path, "/api/auth/") ||
		strings.HasPrefix(path, "/api/node/info") ||
		strings.HasPrefix(path, "/orbpro-key-broker/v1/")
}

func isWebhookPath(path string) bool {
	return strings.HasPrefix(path, "/api/storefront/payments/stripe/webhook")
}

func isAdminOnlyAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/peers") ||
		strings.HasPrefix(path, "/api/groups") ||
		strings.HasPrefix(path, "/api/blocklist") ||
		strings.HasPrefix(path, "/api/settings") ||
		strings.HasPrefix(path, "/api/export") ||
		strings.HasPrefix(path, "/api/import")
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

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	}
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
