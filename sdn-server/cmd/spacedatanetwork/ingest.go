package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/ingest"
	"github.com/spacedatanetwork/sdn-server/internal/tor"
	"github.com/spf13/cobra"
)

var ingestCmd = &cobra.Command{
	Use:   "ingest",
	Short: "Run CelesTrak/Space-Track ingestion workers",
	Long: `Ingests CelesTrak and Space-Track data into FlatSQL with checkpoints,
raw archive snapshots, and gap-fill batching for production-safe sync.`,
	RunE: runIngest,
}

var (
	ingestStoragePath          string
	ingestRawPath              string
	ingestOnce                 bool
	ingestCelestrakInterval    time.Duration
	ingestSatcatInterval       time.Duration
	ingestCatalogURL           string
	ingestSatcatURL            string
	ingestSpaceTrackEnabled    bool
	ingestSpaceTrackIdentity   string
	ingestSpaceTrackPassword   string
	ingestSpaceTrackStartDay   string
	ingestSpaceTrackBatchDays  int
	ingestSpaceTrackBatchSleep time.Duration
	ingestSpaceTrackPoll       time.Duration
	ingestSpaceTrackLoginURL   string
	ingestSpaceTrackQueryTmpl  string
	ingestHTTPTimeout          time.Duration
)

func init() {
	ingestCmd.Flags().StringVar(&ingestStoragePath, "storage-path", "", "override storage path (defaults to config.storage.path)")
	ingestCmd.Flags().StringVar(&ingestRawPath, "raw-path", "", "raw archive path (default: <storage-parent>/raw)")
	ingestCmd.Flags().BoolVar(&ingestOnce, "once", false, "run one sync cycle and exit")

	ingestCmd.Flags().DurationVar(&ingestCelestrakInterval, "celestrak-interval", time.Hour, "CelesTrak GP sync interval")
	ingestCmd.Flags().DurationVar(&ingestSatcatInterval, "satcat-interval", 24*time.Hour, "CelesTrak SATCAT sync interval")
	ingestCmd.Flags().StringVar(&ingestCatalogURL, "celestrak-catalog-url", "", "override CelesTrak GP catalog CSV URL")
	ingestCmd.Flags().StringVar(&ingestSatcatURL, "celestrak-satcat-url", "", "override CelesTrak SATCAT CSV URL")

	ingestCmd.Flags().BoolVar(&ingestSpaceTrackEnabled, "spacetrack-enabled", true, "enable Space-Track gap-fill worker")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackIdentity, "spacetrack-identity", "", "Space-Track login identity (or SPACETRACK_IDENTITY env)")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackPassword, "spacetrack-password", "", "Space-Track login password (or SPACETRACK_PASSWORD env)")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackStartDay, "spacetrack-start-day", "", "initial gap-fill start day YYYY-MM-DD when no checkpoint exists")
	ingestCmd.Flags().IntVar(&ingestSpaceTrackBatchDays, "spacetrack-batch-days", 3, "days per Space-Track request batch")
	ingestCmd.Flags().DurationVar(&ingestSpaceTrackBatchSleep, "spacetrack-batch-sleep", 3*time.Second, "sleep between Space-Track batches")
	ingestCmd.Flags().DurationVar(&ingestSpaceTrackPoll, "spacetrack-poll-interval", 30*time.Minute, "Space-Track gap-fill poll interval")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackLoginURL, "spacetrack-login-url", "", "override Space-Track login URL")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackQueryTmpl, "spacetrack-query-template", "", "Space-Track query URL template with two %s placeholders for start/end day")

	ingestCmd.Flags().DurationVar(&ingestHTTPTimeout, "http-timeout", 90*time.Second, "HTTP request timeout")

	rootCmd.AddCommand(ingestCmd)
}

func runIngest(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	storagePath := ingestStoragePath
	if storagePath == "" {
		storagePath = cfg.Storage.Path
	}
	if storagePath == "" {
		return fmt.Errorf("storage path is required")
	}

	rawPath := ingestRawPath
	if rawPath == "" {
		rawPath = filepath.Join(filepath.Dir(storagePath), "raw")
	}

	identity := ingestSpaceTrackIdentity
	if identity == "" {
		identity = strings.TrimSpace(os.Getenv("SPACETRACK_IDENTITY"))
	}
	password := ingestSpaceTrackPassword
	if password == "" {
		password = strings.TrimSpace(os.Getenv("SPACETRACK_PASSWORD"))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	torStartTimeout := 30 * time.Second
	if raw := strings.TrimSpace(cfg.Tor.StartTimeout); raw != "" {
		if parsed, parseErr := time.ParseDuration(raw); parseErr != nil {
			log.Warnf("Invalid tor.start_timeout %q, using %s", raw, torStartTimeout)
		} else {
			torStartTimeout = parsed
		}
	}

	torRuntime, err := tor.Start(ctx, tor.StartOptions{
		Enabled:              cfg.Tor.Enabled,
		BinaryPath:           cfg.Tor.BinaryPath,
		StoragePath:          storagePath,
		DataDir:              cfg.Tor.DataDir,
		SocksAddress:         cfg.Tor.SocksAddress,
		StartTimeout:         torStartTimeout,
		HiddenServiceEnabled: false,
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
		log.Infof("Ingest outbound HTTP proxying enabled via TOR (%s)", torRuntime.ProxyURL())
	}

	runner, err := ingest.NewRunner(ingest.Config{
		StoragePath: storagePath,
		RawPath:     rawPath,
		Once:        ingestOnce,

		CelestrakCatalogURL: ingestCatalogURL,
		CelestrakSatcatURL:  ingestSatcatURL,
		CelestrakInterval:   ingestCelestrakInterval,
		SatcatInterval:      ingestSatcatInterval,

		SpaceTrackEnabled:      ingestSpaceTrackEnabled,
		SpaceTrackIdentity:     identity,
		SpaceTrackPassword:     password,
		SpaceTrackStartDay:     ingestSpaceTrackStartDay,
		SpaceTrackBatchDays:    ingestSpaceTrackBatchDays,
		SpaceTrackBatchSleep:   ingestSpaceTrackBatchSleep,
		SpaceTrackPollInterval: ingestSpaceTrackPoll,
		SpaceTrackLoginURL:     ingestSpaceTrackLoginURL,
		SpaceTrackQueryTmpl:    ingestSpaceTrackQueryTmpl,

		HTTPTimeout: ingestHTTPTimeout,
	})
	if err != nil {
		return err
	}

	log.Infof("Starting ingest workers: storage=%s raw=%s once=%v", storagePath, rawPath, ingestOnce)
	if ingestSpaceTrackEnabled && (identity == "" || password == "") {
		log.Warn("Space-Track enabled but credentials are empty; gap-fill will be skipped")
	}

	return runner.Run(ctx)
}
