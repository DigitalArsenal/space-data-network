package node

// In-daemon ingest (loop C.6b): the CelesTrak/Space-Track/UDL source-sync
// workers run as a goroutine INSIDE the daemon, driving the existing ingest
// pipeline against the daemon's own single-writer store handle. This
// replaces the separate `spacedatanetwork-ingest.service` process topology,
// which the v2 store's single-writer lock now rejects. Records land through
// the normal store write path, so hot-window enforcement, datasync cursor
// rowids, and engine-mirror invalidation (query-cache generation bumps) all
// apply identically to daemon-served queries.
//
// Architecture note (updated loop C.8a): the module-paradigm ingest path
// NOW EXISTS for the CelesTrak sources — config `flows.services` loads the
// compiled celestrak-{gp,satcat,spw}-ingest flow bundles
// (space-data-network-modules/flows/celestrak-ingest) as timer-served flow
// services (internal/flowrt/cronmount.go): cron TIMER trigger →
// hostcap/http-request → WASM provider parsers (spacedatastandards.org
// generated code, byte-parity with this pipeline's builders) → the
// policy-mediated storage.ingest_with_source cap op (SourceTags
// attribution, reconcile, disk guardrail, raw + provenance archiving).
// This Go runner remains for (a) Space-Track gap-fill and UDL sync — the
// credentialed, checkpointed multi-batch workers that stayed host-side
// (see the C.8 report's descope rationale) — and (b) deployments that have
// not opted into the flow topology yet. Dataset publication triggering
// also still lives here (DatasetPublishURL); the flow path does not fire
// it yet.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/ingest"
)

// startInDaemonIngest launches the ingest workers on the daemon's store
// when config `ingest.enabled` is set. Called from Node.Start; the workers
// stop with the node context and are waited on via n.wg.
func (n *Node) startInDaemonIngest() error {
	if !n.config.Ingest.Enabled {
		return nil
	}
	if n.store == nil {
		return fmt.Errorf("config ingest.enabled requires a full node with storage (mode=%q has no store)", n.config.Mode)
	}

	cfg, err := buildIngestRunnerConfig(n.config)
	if err != nil {
		return fmt.Errorf("invalid ingest config: %w", err)
	}
	runner, err := ingest.NewRunnerWithStore(cfg, n.store)
	if err != nil {
		return fmt.Errorf("start in-daemon ingest: %w", err)
	}

	log.Infof("In-daemon ingest enabled: storage=%s raw=%s celestrak=%s satcat=%s spw=%s spacetrack=%v udl=%v",
		cfg.StoragePath, cfg.RawPath, cfg.CelestrakInterval, cfg.SatcatInterval, cfg.SpaceWeatherInterval,
		cfg.SpaceTrackEnabled, cfg.UDLEnabled)
	if cfg.SpaceTrackEnabled && (cfg.SpaceTrackIdentity == "" || cfg.SpaceTrackPassword == "") {
		log.Warn("In-daemon ingest: Space-Track enabled but SPACETRACK_IDENTITY/SPACETRACK_PASSWORD are empty; gap-fill will be skipped")
	}
	if cfg.UDLEnabled && (cfg.UDLUsername == "" || cfg.UDLPassword == "") {
		log.Warn("In-daemon ingest: UDL enabled but UDL_USERNAME/UDL_PASSWORD are empty; UDL sync will be skipped")
	}

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := runner.Run(n.ctx); err != nil {
			log.Errorf("In-daemon ingest stopped with error: %v", err)
		}
	}()
	return nil
}

// buildIngestRunnerConfig maps the daemon YAML ingest section onto the
// ingest runner config. Credentials come exclusively from the environment
// (never from config files). Zero values defer to the runner's defaults —
// the same defaults the standalone `ingest` verb applies.
func buildIngestRunnerConfig(c *config.Config) (ingest.Config, error) {
	ic := c.Ingest

	rawPath := strings.TrimSpace(ic.RawPath)
	if rawPath == "" {
		rawPath = filepath.Join(filepath.Dir(c.Storage.Path), "raw")
	}

	var parseErrs []string
	dur := func(field, raw string) time.Duration {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return 0 // runner default
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			parseErrs = append(parseErrs, fmt.Sprintf("%s: %v", field, err))
			return 0
		}
		return d
	}

	datasetPublishURL := strings.TrimSpace(ic.DatasetPublishURL)
	if env := strings.TrimSpace(os.Getenv("SDN_DATASET_PUBLISH_URL")); env != "" {
		datasetPublishURL = env
	}

	var minFreeDiskBytes int64
	if ic.MinFreeDiskGB > 0 {
		minFreeDiskBytes = int64(ic.MinFreeDiskGB * 1024 * 1024 * 1024)
	}

	out := ingest.Config{
		StoragePath:      c.Storage.Path,
		RawPath:          rawPath,
		MinFreeDiskBytes: minFreeDiskBytes,

		CelestrakCatalogURL:      strings.TrimSpace(ic.CelestrakCatalogURL),
		CelestrakSatcatURL:       strings.TrimSpace(ic.CelestrakSatcatURL),
		CelestrakSatcatCSVURL:    strings.TrimSpace(ic.CelestrakSatcatCSVURL),
		CelestrakSpaceWeatherURL: strings.TrimSpace(ic.CelestrakSpaceWeatherURL),
		CelestrakInterval:        dur("ingest.celestrak_interval", ic.CelestrakInterval),
		SatcatInterval:           dur("ingest.satcat_interval", ic.SatcatInterval),
		SpaceWeatherInterval:     dur("ingest.space_weather_interval", ic.SpaceWeatherInterval),

		SpaceTrackEnabled:      ic.SpaceTrackEnabled,
		SpaceTrackIdentity:     strings.TrimSpace(os.Getenv("SPACETRACK_IDENTITY")),
		SpaceTrackPassword:     strings.TrimSpace(os.Getenv("SPACETRACK_PASSWORD")),
		SpaceTrackStartDay:     strings.TrimSpace(ic.SpaceTrackStartDay),
		SpaceTrackBatchDays:    ic.SpaceTrackBatchDays,
		SpaceTrackBatchSleep:   dur("ingest.spacetrack_batch_sleep", ic.SpaceTrackBatchSleep),
		SpaceTrackPollInterval: dur("ingest.spacetrack_poll_interval", ic.SpaceTrackPollInterval),
		SpaceTrackLoginURL:     strings.TrimSpace(ic.SpaceTrackLoginURL),
		SpaceTrackQueryTmpl:    strings.TrimSpace(ic.SpaceTrackQueryTmpl),

		// Supplemental Space-Track lanes (A2.2c-ST). Ride the same
		// spacetrack_enabled master switch; each defaults on and can be opted
		// out individually.
		SpaceTrackPublicFilesEnabled: ic.SpaceTrackEnabled && optBoolDefaultTrue(ic.SpaceTrackPublicFilesEnabled),
		SpaceTrackCurrentGPEnabled:   ic.SpaceTrackEnabled && optBoolDefaultTrue(ic.SpaceTrackCurrentGPEnabled),
		SpaceTrackSupplementalPoll:   dur("ingest.spacetrack_supplemental_poll_interval", ic.SpaceTrackSupplementalPoll),
		SpaceTrackCurrentGPQueryURL:  strings.TrimSpace(ic.SpaceTrackCurrentGPQueryURL),
		SpaceTrackPublicFilesBaseURL: strings.TrimSpace(ic.SpaceTrackPublicFilesBaseURL),

		UDLEnabled:      ic.UDLEnabled,
		UDLUsername:     strings.TrimSpace(os.Getenv("UDL_USERNAME")),
		UDLPassword:     strings.TrimSpace(os.Getenv("UDL_PASSWORD")),
		UDLBaseURL:      strings.TrimSpace(ic.UDLBaseURL),
		UDLStartDay:     strings.TrimSpace(ic.UDLStartDay),
		UDLBatchDays:    ic.UDLBatchDays,
		UDLBatchSleep:   dur("ingest.udl_batch_sleep", ic.UDLBatchSleep),
		UDLPollInterval: dur("ingest.udl_poll_interval", ic.UDLPollInterval),
		UDLMaxResults:   ic.UDLMaxResults,

		HTTPTimeout: dur("ingest.http_timeout", ic.HTTPTimeout),

		DatasetPublishURL: datasetPublishURL,
	}
	if len(parseErrs) > 0 {
		return ingest.Config{}, fmt.Errorf("%s", strings.Join(parseErrs, "; "))
	}
	return out, nil
}

// optBoolDefaultTrue treats an unset (nil) YAML bool as true, so a supplemental
// Space-Track lane is on by default whenever spacetrack_enabled is set and only
// off when explicitly configured false.
func optBoolDefaultTrue(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}
