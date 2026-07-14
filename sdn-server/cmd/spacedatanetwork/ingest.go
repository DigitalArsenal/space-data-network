package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/ingest"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/tor"
	"github.com/spf13/cobra"
)

var ingestCmd = &cobra.Command{
	Use:   "ingest",
	Short: "Run CelesTrak/Space-Track/UDL ingestion workers",
	Long: `Ingests CelesTrak, Space-Track, and Unified Data Library (UDL) data into
FlatSQL with checkpoints, raw archive snapshots, and gap-fill batching for
production-safe sync.`,
	RunE: runIngest,
}

var (
	ingestStoragePath          string
	ingestRawPath              string
	ingestOnce                 bool
	ingestCelestrakInterval    time.Duration
	ingestSatcatInterval       time.Duration
	ingestSpaceWeatherInterval time.Duration
	ingestCatalogURL           string
	ingestSatcatURL            string
	ingestSatcatCSVURL         string
	ingestSpaceWeatherURL      string
	ingestSpaceTrackEnabled    bool
	ingestSpaceTrackIdentity   string
	ingestSpaceTrackPassword   string
	ingestSpaceTrackStartDay   string
	ingestSpaceTrackBatchDays  int
	ingestSpaceTrackBatchSleep time.Duration
	ingestSpaceTrackPoll       time.Duration
	ingestSpaceTrackLoginURL   string
	ingestSpaceTrackQueryTmpl  string
	ingestSpaceTrackPublicFiles bool
	ingestSpaceTrackCurrentGP   bool
	ingestSpaceTrackSupPoll     time.Duration
	ingestUDLEnabled           bool
	ingestUDLUsername          string
	ingestUDLPassword          string
	ingestUDLBaseURL           string
	ingestUDLStartDay          string
	ingestUDLBatchDays         int
	ingestUDLBatchSleep        time.Duration
	ingestUDLPoll              time.Duration
	ingestUDLMaxResults        int
	ingestHTTPTimeout          time.Duration
	ingestDatasetPublishURL    string
	ingestMinFreeDiskGB        float64
)

func init() {
	ingestCmd.Flags().StringVar(&ingestStoragePath, "storage-path", "", "override storage path (defaults to config.storage.path)")
	ingestCmd.Flags().StringVar(&ingestRawPath, "raw-path", "", "raw archive path (default: <storage-parent>/raw)")
	ingestCmd.Flags().BoolVar(&ingestOnce, "once", false, "run one sync cycle and exit")

	ingestCmd.Flags().DurationVar(&ingestCelestrakInterval, "celestrak-interval", 3*time.Hour, "CelesTrak GP sync interval (minimum 3h)")
	ingestCmd.Flags().DurationVar(&ingestSatcatInterval, "satcat-interval", 24*time.Hour, "CelesTrak SATCAT sync interval")
	ingestCmd.Flags().DurationVar(&ingestSpaceWeatherInterval, "celestrak-space-weather-interval", 3*time.Hour, "CelesTrak space-weather sync interval (minimum 3h)")
	ingestCmd.Flags().StringVar(&ingestCatalogURL, "celestrak-catalog-url", "", "override CelesTrak GP catalog CSV URL")
	ingestCmd.Flags().StringVar(&ingestSatcatURL, "celestrak-satcat-url", "", "override CelesTrak SATCAT URL (txt or csv)")
	ingestCmd.Flags().StringVar(&ingestSatcatCSVURL, "celestrak-satcat-csv-url", "", "override CelesTrak SATCAT CSV records URL")
	ingestCmd.Flags().StringVar(&ingestSpaceWeatherURL, "celestrak-space-weather-url", "", "override CelesTrak space-weather CSV URL")

	ingestCmd.Flags().BoolVar(&ingestSpaceTrackEnabled, "spacetrack-enabled", true, "enable Space-Track gap-fill worker")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackIdentity, "spacetrack-identity", "", "Space-Track login identity (or SPACETRACK_IDENTITY env)")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackPassword, "spacetrack-password", "", "Space-Track login password (or SPACETRACK_PASSWORD env)")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackStartDay, "spacetrack-start-day", "", "initial gap-fill start day YYYY-MM-DD when no checkpoint exists")
	ingestCmd.Flags().IntVar(&ingestSpaceTrackBatchDays, "spacetrack-batch-days", 3, "days per Space-Track request batch")
	ingestCmd.Flags().DurationVar(&ingestSpaceTrackBatchSleep, "spacetrack-batch-sleep", 3*time.Second, "sleep between Space-Track batches")
	ingestCmd.Flags().DurationVar(&ingestSpaceTrackPoll, "spacetrack-poll-interval", 30*time.Minute, "Space-Track gap-fill poll interval")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackLoginURL, "spacetrack-login-url", "", "override Space-Track login URL")
	ingestCmd.Flags().StringVar(&ingestSpaceTrackQueryTmpl, "spacetrack-query-template", "", "Space-Track query URL template with two %s placeholders for start/end day")
	ingestCmd.Flags().BoolVar(&ingestSpaceTrackPublicFiles, "spacetrack-publicfiles-enabled", true, "enable Space-Track publicfiles operator-ephemeris (CCSDS OEM) lane (requires --spacetrack-enabled)")
	ingestCmd.Flags().BoolVar(&ingestSpaceTrackCurrentGP, "spacetrack-current-gp-enabled", true, "enable Space-Track current full-catalog gp (OMM) lane (requires --spacetrack-enabled)")
	ingestCmd.Flags().DurationVar(&ingestSpaceTrackSupPoll, "spacetrack-supplemental-poll-interval", 6*time.Hour, "Space-Track supplemental (publicfiles + current-gp) poll interval")

	ingestCmd.Flags().BoolVar(&ingestUDLEnabled, "udl-enabled", true, "enable Unified Data Library (UDL) sync worker")
	ingestCmd.Flags().StringVar(&ingestUDLUsername, "udl-username", "", "UDL basic auth username (or UDL_USERNAME env)")
	ingestCmd.Flags().StringVar(&ingestUDLPassword, "udl-password", "", "UDL basic auth password (or UDL_PASSWORD env)")
	ingestCmd.Flags().StringVar(&ingestUDLBaseURL, "udl-base-url", "", "override UDL REST base URL (default https://unifieddatalibrary.com)")
	ingestCmd.Flags().StringVar(&ingestUDLStartDay, "udl-start-day", "", "initial UDL epoch-window start day YYYY-MM-DD when no checkpoint exists")
	ingestCmd.Flags().IntVar(&ingestUDLBatchDays, "udl-batch-days", 3, "days per UDL epoch-window request batch")
	ingestCmd.Flags().DurationVar(&ingestUDLBatchSleep, "udl-batch-sleep", 3*time.Second, "sleep between UDL pages/batches (rate limiting)")
	ingestCmd.Flags().DurationVar(&ingestUDLPoll, "udl-poll-interval", 30*time.Minute, "UDL sync poll interval")
	ingestCmd.Flags().IntVar(&ingestUDLMaxResults, "udl-max-results", 10000, "maxResults page size for UDL queries")

	ingestCmd.Flags().DurationVar(&ingestHTTPTimeout, "http-timeout", 90*time.Second, "HTTP request timeout")
	ingestCmd.Flags().StringVar(&ingestDatasetPublishURL, "dataset-publish-url", "", "local SDN admin dataset publication endpoint")
	ingestCmd.Flags().Float64Var(&ingestMinFreeDiskGB, "min-free-disk-gb", 0, "minimum free disk (GB) required before a sync runs (0 = default 5 GiB); lower on small volumes")

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
	udlUsername := ingestUDLUsername
	if udlUsername == "" {
		udlUsername = strings.TrimSpace(os.Getenv("UDL_USERNAME"))
	}
	udlPassword := ingestUDLPassword
	if udlPassword == "" {
		udlPassword = strings.TrimSpace(os.Getenv("UDL_PASSWORD"))
	}
	datasetPublishURL := ingestDatasetPublishURL
	if datasetPublishURL == "" {
		datasetPublishURL = strings.TrimSpace(os.Getenv("SDN_DATASET_PUBLISH_URL"))
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

	var minFreeDiskBytes int64
	if ingestMinFreeDiskGB > 0 {
		minFreeDiskBytes = int64(ingestMinFreeDiskGB * 1024 * 1024 * 1024)
	}

	runner, err := ingest.NewRunner(ingest.Config{
		StoragePath:      storagePath,
		RawPath:          rawPath,
		Once:             ingestOnce,
		MinFreeDiskBytes: minFreeDiskBytes,

		CelestrakCatalogURL:      ingestCatalogURL,
		CelestrakSatcatURL:       ingestSatcatURL,
		CelestrakSatcatCSVURL:    ingestSatcatCSVURL,
		CelestrakSpaceWeatherURL: ingestSpaceWeatherURL,
		CelestrakInterval:        ingestCelestrakInterval,
		SatcatInterval:           ingestSatcatInterval,
		SpaceWeatherInterval:     ingestSpaceWeatherInterval,

		SpaceTrackEnabled:      ingestSpaceTrackEnabled,
		SpaceTrackIdentity:     identity,
		SpaceTrackPassword:     password,
		SpaceTrackStartDay:     ingestSpaceTrackStartDay,
		SpaceTrackBatchDays:    ingestSpaceTrackBatchDays,
		SpaceTrackBatchSleep:   ingestSpaceTrackBatchSleep,
		SpaceTrackPollInterval: ingestSpaceTrackPoll,
		SpaceTrackLoginURL:     ingestSpaceTrackLoginURL,
		SpaceTrackQueryTmpl:    ingestSpaceTrackQueryTmpl,

		SpaceTrackPublicFilesEnabled: ingestSpaceTrackEnabled && ingestSpaceTrackPublicFiles,
		SpaceTrackCurrentGPEnabled:   ingestSpaceTrackEnabled && ingestSpaceTrackCurrentGP,
		SpaceTrackSupplementalPoll:   ingestSpaceTrackSupPoll,

		UDLEnabled:      ingestUDLEnabled,
		UDLUsername:     udlUsername,
		UDLPassword:     udlPassword,
		UDLBaseURL:      ingestUDLBaseURL,
		UDLStartDay:     ingestUDLStartDay,
		UDLBatchDays:    ingestUDLBatchDays,
		UDLBatchSleep:   ingestUDLBatchSleep,
		UDLPollInterval: ingestUDLPoll,
		UDLMaxResults:   ingestUDLMaxResults,

		HTTPTimeout: ingestHTTPTimeout,

		DatasetPublishURL: datasetPublishURL,
	})
	if err != nil {
		if errors.Is(err, storage.ErrStoreLocked) {
			// The v2 store is single-writer: the standalone ingest verb can
			// no longer run against a store a daemon (or any other process)
			// holds — that topology corrupts record metadata/stream state.
			return fmt.Errorf("%w\n\nThe storage path %s is held by another process (most likely a running spacedatanetwork daemon).\n"+
				"The standalone 'ingest' command only works against a store no daemon is using (offline/standalone mode).\n"+
				"To ingest alongside a running daemon, enable in-daemon ingest in the daemon config instead:\n\n"+
				"  ingest:\n    enabled: true\n\nand remove/disable any spacedatanetwork-ingest service unit.", err, storagePath)
		}
		return err
	}

	log.Infof("Starting ingest workers: storage=%s raw=%s once=%v", storagePath, rawPath, ingestOnce)
	if ingestSpaceTrackEnabled && (identity == "" || password == "") {
		log.Warn("Space-Track enabled but credentials are empty; gap-fill will be skipped")
	}
	if ingestUDLEnabled && (udlUsername == "" || udlPassword == "") {
		log.Warn("UDL enabled but credentials are empty; UDL sync will be skipped")
	}

	return runner.Run(ctx)
}
