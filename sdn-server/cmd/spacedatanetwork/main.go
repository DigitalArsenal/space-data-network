// Package main provides the entry point for the Space Data Network server.
// This is a specialized fork of IPFS (Kubo) tailored for space data standards.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	logging "github.com/ipfs/go-log/v2"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storefront"
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
			adminMux := http.NewServeMux()
			var tokenVerifier *license.TokenVerifier
			if n.LicenseService() != nil {
				tokenVerifier = n.LicenseService().Verifier()
				licenseAPI := license.NewAPIHandler(n.LicenseService())
				licenseAPI.RegisterRoutes(adminMux)
				log.Infof("License verification API available at http://%s/api/v1/license/verify", adminAddr)
				log.Infof("License entitlement admin API available at http://%s/api/v1/license/entitlements", adminAddr)
			}
			dataAPI := api.NewDataQueryHandler(n.Store(), tokenVerifier)
			dataAPI.RegisterRoutes(adminMux)

			// Storefront API (listings, purchases, Stripe checkout/webhooks).
			if n.Store() != nil {
				storefrontDBPath := filepath.Join(cfg.Storage.Path, "storefront.db")
				sfStore, err := storefront.NewStore(storefrontDBPath)
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
						sfAPI.RegisterRoutes(adminMux)
						storefrontSvc = sfSvc
						storefrontStore = sfStore
						storefrontDelivery = sfDelivery
						log.Infof("Storefront API available at http://%s/api/storefront/listings", adminAddr)
						log.Infof("Stripe webhook endpoint: http://%s/api/storefront/payments/stripe/webhook", adminAddr)
					}
				}
			}

			adminMux.Handle("/", adminUI)

			adminServer = &http.Server{
				Addr:    adminAddr,
				Handler: adminMux,
			}
			go func() {
				log.Infof("Admin interface available at http://%s/admin", adminAddr)
				log.Infof("Peer API available at http://%s/api/peers", adminAddr)
				log.Infof("Public data API available at http://%s/api/v1/data/omm", adminAddr)
				if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
