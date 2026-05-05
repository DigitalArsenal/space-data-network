// Package ingest provides data source sync workers for OMM/MPE/CAT/SPW ingestion.
package ingest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	MPEFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	SPWFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/SPW"
	flatbuffers "github.com/google/flatbuffers/go"
	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

var log = logging.Logger("ingest")

const (
	defaultCelestrakCatalogURL      = "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv"
	defaultCelestrakSatcatURL       = "https://celestrak.org/pub/satcat.txt"
	defaultCelestrakSpaceWeatherURL = "https://celestrak.org/SpaceData/SW-All.csv"
	defaultSpaceTrackLoginURL       = "https://www.space-track.org/ajaxauth/login"
	defaultSpaceTrackQueryTmpl      = "https://www.space-track.org/basicspacedata/query/class/gp_history/EPOCH/%s--%s/format/csv"
	minCelestrakFetchInterval       = 3 * time.Hour
	parserVersionCelestrakGP        = "celestrak-gp/v1"
	parserVersionCelestrakSatcat    = "celestrak-satcat/v1"
	parserVersionCelestrakSPW       = "celestrak-space-weather/v1"
	parserVersionSpaceTrackGP       = "spacetrack-gp-history/v1"
	fetchRetryBudget                = 3
	fetchRetryBackoff               = 10 * time.Millisecond
)

type fetchMetadata struct {
	SourceURL    string
	HTTPStatus   int
	ETag         string
	LastModified string
	ContentType  string
	RetrievedAt  time.Time
	FromCache    bool
	CachePath    string
}

type ingestBatchProvenance struct {
	SourceURL        string            `json:"source_url"`
	HTTPStatus       int               `json:"http_status,omitempty"`
	ETag             string            `json:"etag,omitempty"`
	LastModified     string            `json:"last_modified,omitempty"`
	ContentType      string            `json:"content_type,omitempty"`
	RetrievedAt      string            `json:"retrieved_at"`
	ParserVersion    string            `json:"parser_version"`
	SourceSHA256     string            `json:"source_sha256"`
	NormalizedSHA256 string            `json:"normalized_sha256"`
	NormalizedCount  int               `json:"normalized_count"`
	SchemaCounts     map[string]int    `json:"schema_counts"`
	SchemaHashes     map[string]string `json:"schema_hashes"`
	Warnings         []string          `json:"warnings"`
	FromCache        bool              `json:"from_cache"`
	CachePath        string            `json:"cache_path,omitempty"`
}

// Config controls ingestion worker behavior.
type Config struct {
	StoragePath string
	RawPath     string
	Once        bool

	CelestrakCatalogURL      string
	CelestrakSatcatURL       string
	CelestrakSpaceWeatherURL string
	CelestrakInterval        time.Duration
	SatcatInterval           time.Duration
	SpaceWeatherInterval     time.Duration

	SpaceTrackEnabled      bool
	SpaceTrackIdentity     string
	SpaceTrackPassword     string
	SpaceTrackLoginURL     string
	SpaceTrackQueryTmpl    string
	SpaceTrackStartDay     string
	SpaceTrackBatchDays    int
	SpaceTrackBatchSleep   time.Duration
	SpaceTrackPollInterval time.Duration

	HTTPTimeout time.Duration
}

// Runner executes source sync and ingestion loops.
type Runner struct {
	cfg         Config
	store       *storage.FlatSQLStore
	httpClient  *http.Client
	checkpoints *checkpointStore
}

// NewRunner constructs a Runner with local storage and checkpoint state.
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.StoragePath == "" {
		return nil, fmt.Errorf("storage path is required")
	}

	if cfg.RawPath == "" {
		cfg.RawPath = filepath.Join(filepath.Dir(cfg.StoragePath), "raw")
	}

	if cfg.CelestrakCatalogURL == "" {
		cfg.CelestrakCatalogURL = defaultCelestrakCatalogURL
	}
	if cfg.CelestrakSatcatURL == "" {
		cfg.CelestrakSatcatURL = defaultCelestrakSatcatURL
	}
	if cfg.CelestrakSpaceWeatherURL == "" {
		cfg.CelestrakSpaceWeatherURL = defaultCelestrakSpaceWeatherURL
	}
	if cfg.SpaceTrackLoginURL == "" {
		cfg.SpaceTrackLoginURL = defaultSpaceTrackLoginURL
	}
	if cfg.SpaceTrackQueryTmpl == "" {
		cfg.SpaceTrackQueryTmpl = defaultSpaceTrackQueryTmpl
	}
	if cfg.CelestrakInterval <= 0 {
		cfg.CelestrakInterval = minCelestrakFetchInterval
	}
	if cfg.CelestrakInterval < minCelestrakFetchInterval {
		cfg.CelestrakInterval = minCelestrakFetchInterval
	}
	if cfg.SatcatInterval <= 0 {
		cfg.SatcatInterval = 24 * time.Hour
	}
	if cfg.SatcatInterval < minCelestrakFetchInterval {
		cfg.SatcatInterval = minCelestrakFetchInterval
	}
	if cfg.SpaceWeatherInterval <= 0 {
		cfg.SpaceWeatherInterval = minCelestrakFetchInterval
	}
	if cfg.SpaceWeatherInterval < minCelestrakFetchInterval {
		cfg.SpaceWeatherInterval = minCelestrakFetchInterval
	}
	if cfg.SpaceTrackPollInterval <= 0 {
		cfg.SpaceTrackPollInterval = 30 * time.Minute
	}
	if cfg.SpaceTrackBatchDays <= 0 {
		cfg.SpaceTrackBatchDays = 3
	}
	if cfg.SpaceTrackBatchSleep <= 0 {
		cfg.SpaceTrackBatchSleep = 3 * time.Second
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 90 * time.Second
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize validator: %w", err)
	}

	store, err := storage.NewFlatSQLStore(cfg.StoragePath, validator)
	if err != nil {
		return nil, fmt.Errorf("failed to open storage: %w", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	cp, err := newCheckpointStore(filepath.Join(cfg.StoragePath, "ingest-checkpoints.json"))
	if err != nil {
		store.Close()
		return nil, err
	}

	return &Runner{
		cfg:   cfg,
		store: store,
		httpClient: &http.Client{
			Timeout: cfg.HTTPTimeout,
			Jar:     jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		checkpoints: cp,
	}, nil
}

// Close releases underlying resources.
func (r *Runner) Close() error {
	if r.store != nil {
		return r.store.Close()
	}
	return nil
}

// Run executes once or starts periodic workers depending on config.
func (r *Runner) Run(ctx context.Context) error {
	defer r.Close()

	if r.cfg.Once {
		return r.runCycle(ctx)
	}

	if err := r.runCycle(ctx); err != nil {
		log.Warnf("Initial ingest cycle finished with errors: %v", err)
	}

	gpTicker := time.NewTicker(r.cfg.CelestrakInterval)
	satTicker := time.NewTicker(r.cfg.SatcatInterval)
	spwTicker := time.NewTicker(r.cfg.SpaceWeatherInterval)
	stTicker := time.NewTicker(r.cfg.SpaceTrackPollInterval)
	defer gpTicker.Stop()
	defer satTicker.Stop()
	defer spwTicker.Stop()
	defer stTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-gpTicker.C:
			if err := r.syncCelestrakGP(ctx); err != nil {
				log.Warnf("CelesTrak GP sync failed: %v", err)
			}
		case <-satTicker.C:
			if err := r.syncCelestrakSatcat(ctx); err != nil {
				log.Warnf("CelesTrak SATCAT sync failed: %v", err)
			}
		case <-spwTicker.C:
			if err := r.syncCelestrakSpaceWeather(ctx); err != nil {
				log.Warnf("CelesTrak space weather sync failed: %v", err)
			}
		case <-stTicker.C:
			if err := r.syncSpaceTrackGapFill(ctx); err != nil {
				log.Warnf("Space-Track gap-fill failed: %v", err)
			}
		}
	}
}

func (r *Runner) runCycle(ctx context.Context) error {
	var errs []string
	if err := r.syncCelestrakGP(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	if err := r.syncCelestrakSatcat(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	if err := r.syncCelestrakSpaceWeather(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	if err := r.syncSpaceTrackGapFill(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (r *Runner) syncCelestrakGP(ctx context.Context) error {
	data, metadata, err := r.fetchWithCache(ctx, r.cfg.CelestrakCatalogURL, "celestrak-gp.csv", minCelestrakFetchInterval)
	if err != nil {
		return fmt.Errorf("fetch celestrak catalog: %w", err)
	}

	if !metadata.FromCache {
		catalogArchiveName := archiveFilenameForURL(r.cfg.CelestrakCatalogURL, "catalog.csv")
		if err := r.archiveRaw("celestrak", catalogArchiveName, data); err != nil {
			log.Warnf("Failed to archive CelesTrak %s: %v", catalogArchiveName, err)
		}
	} else {
		log.Infof("Using cached CelesTrak GP payload (minimum refresh interval: %s)", minCelestrakFetchInterval)
	}

	countOMM, countMPE, normalizedHash, err := r.ingestGPData(data, "source:celestrak")
	if err != nil {
		return fmt.Errorf("ingest celestrak catalog: %w", err)
	}
	if err := r.recordIngestBatchProvenance("celestrak-gp", data, metadata, parserVersionCelestrakGP, normalizedHash, map[string]int{
		"OMM.fbs": countOMM,
		"MPE.fbs": countMPE,
	}, warningsForFetch(metadata)); err != nil {
		log.Warnf("Failed to record CelesTrak GP provenance: %v", err)
	}

	r.checkpoints.setString("celestrak_gp_last_success", time.Now().UTC().Format(time.RFC3339))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist checkpoints: %v", err)
	}

	log.Infof("CelesTrak GP sync complete: OMM=%d MPE=%d", countOMM, countMPE)
	return nil
}

func (r *Runner) syncCelestrakSatcat(ctx context.Context) error {
	data, metadata, err := r.fetchWithCache(ctx, r.cfg.CelestrakSatcatURL, "celestrak-satcat.txt", minCelestrakFetchInterval)
	if err != nil {
		return fmt.Errorf("fetch celestrak satcat: %w", err)
	}

	if !metadata.FromCache {
		satcatArchiveName := archiveFilenameForURL(r.cfg.CelestrakSatcatURL, "satcat.txt")
		if err := r.archiveRaw("celestrak", satcatArchiveName, data); err != nil {
			log.Warnf("Failed to archive CelesTrak %s: %v", satcatArchiveName, err)
		}
	} else {
		log.Infof("Using cached CelesTrak SATCAT payload (minimum refresh interval: %s)", minCelestrakFetchInterval)
	}

	countCAT, normalizedHash, err := r.ingestSatcatData(data, "source:celestrak")
	if err != nil {
		return fmt.Errorf("ingest celestrak satcat: %w", err)
	}
	if err := r.recordIngestBatchProvenance("celestrak-satcat", data, metadata, parserVersionCelestrakSatcat, normalizedHash, map[string]int{
		"CAT.fbs": countCAT,
	}, warningsForFetch(metadata)); err != nil {
		log.Warnf("Failed to record CelesTrak SATCAT provenance: %v", err)
	}

	r.checkpoints.setString("celestrak_satcat_last_success", time.Now().UTC().Format(time.RFC3339))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist checkpoints: %v", err)
	}

	log.Infof("CelesTrak SATCAT sync complete: CAT=%d", countCAT)
	return nil
}

func (r *Runner) syncCelestrakSpaceWeather(ctx context.Context) error {
	data, metadata, err := r.fetchWithCache(ctx, r.cfg.CelestrakSpaceWeatherURL, "celestrak-space-weather.csv", minCelestrakFetchInterval)
	if err != nil {
		return fmt.Errorf("fetch celestrak space weather: %w", err)
	}

	if !metadata.FromCache {
		archiveName := archiveFilenameForURL(r.cfg.CelestrakSpaceWeatherURL, "SW-All.csv")
		if err := r.archiveRaw("celestrak", archiveName, data); err != nil {
			log.Warnf("Failed to archive CelesTrak %s: %v", archiveName, err)
		}
	} else {
		log.Infof("Using cached CelesTrak space weather payload (minimum refresh interval: %s)", minCelestrakFetchInterval)
	}

	countSPW, normalizedHash, err := r.ingestSpaceWeatherData(data, "source:celestrak")
	if err != nil {
		return fmt.Errorf("ingest celestrak space weather: %w", err)
	}
	if err := r.recordIngestBatchProvenance("celestrak-space-weather", data, metadata, parserVersionCelestrakSPW, normalizedHash, map[string]int{
		"SPW.fbs": countSPW,
	}, warningsForFetch(metadata)); err != nil {
		log.Warnf("Failed to record CelesTrak space weather provenance: %v", err)
	}

	r.checkpoints.setString("celestrak_space_weather_last_success", time.Now().UTC().Format(time.RFC3339))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist checkpoints: %v", err)
	}

	log.Infof("CelesTrak space weather sync complete: SPW=%d", countSPW)
	return nil
}

func (r *Runner) syncSpaceTrackGapFill(ctx context.Context) error {
	if !r.cfg.SpaceTrackEnabled {
		return nil
	}

	if r.cfg.SpaceTrackIdentity == "" || r.cfg.SpaceTrackPassword == "" {
		log.Warn("Space-Track credentials missing; skipping gap-fill")
		return nil
	}

	startDay, err := r.resolveSpaceTrackStartDay()
	if err != nil {
		return err
	}

	endDay := time.Now().UTC().AddDate(0, 0, -1)
	if startDay.After(endDay) {
		return nil
	}

	if err := r.spaceTrackLogin(ctx); err != nil {
		return err
	}

	for batchStart := startDay; !batchStart.After(endDay); batchStart = batchStart.AddDate(0, 0, r.cfg.SpaceTrackBatchDays) {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		batchEnd := batchStart.AddDate(0, 0, r.cfg.SpaceTrackBatchDays-1)
		if batchEnd.After(endDay) {
			batchEnd = endDay
		}

		queryURL := fmt.Sprintf(r.cfg.SpaceTrackQueryTmpl, batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"))
		data, metadata, err := r.fetchBytesWithMetadata(ctx, queryURL)
		if err != nil {
			return fmt.Errorf("fetch spacetrack range %s..%s: %w", batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"), err)
		}

		archiveName := fmt.Sprintf("gp_history_%s_%s.csv", batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"))
		if err := r.archiveRaw("spacetrack", archiveName, data); err != nil {
			log.Warnf("Failed to archive Space-Track data %s: %v", archiveName, err)
		}

		countOMM, countMPE, normalizedHash, err := r.ingestGPData(data, "source:spacetrack")
		if err != nil {
			return fmt.Errorf("ingest spacetrack range %s..%s: %w", batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"), err)
		}
		if err := r.recordIngestBatchProvenance("spacetrack-gp-history", data, metadata, parserVersionSpaceTrackGP, normalizedHash, map[string]int{
			"OMM.fbs": countOMM,
			"MPE.fbs": countMPE,
		}, warningsForFetch(metadata)); err != nil {
			log.Warnf("Failed to record Space-Track GP provenance: %v", err)
		}

		r.checkpoints.setString("spacetrack_last_day", batchEnd.Format("2006-01-02"))
		if err := r.checkpoints.save(); err != nil {
			log.Warnf("Failed to persist checkpoints: %v", err)
		}

		log.Infof("Space-Track gap-fill %s..%s complete: OMM=%d MPE=%d",
			batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"), countOMM, countMPE)

		if batchEnd.Before(endDay) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(r.cfg.SpaceTrackBatchSleep):
			}
		}
	}

	return nil
}

func (r *Runner) resolveSpaceTrackStartDay() (time.Time, error) {
	if day := strings.TrimSpace(r.checkpoints.getString("spacetrack_last_day")); day != "" {
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid spacetrack_last_day checkpoint %q: %w", day, err)
		}
		return parsed.AddDate(0, 0, 1), nil
	}

	if strings.TrimSpace(r.cfg.SpaceTrackStartDay) != "" {
		parsed, err := time.Parse("2006-01-02", strings.TrimSpace(r.cfg.SpaceTrackStartDay))
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid --spacetrack-start-day %q (expected YYYY-MM-DD)", r.cfg.SpaceTrackStartDay)
		}
		return parsed, nil
	}

	// Safe default if no checkpoint or explicit start is provided.
	return time.Now().UTC().AddDate(0, 0, -30), nil
}

func (r *Runner) spaceTrackLogin(ctx context.Context) error {
	form := url.Values{}
	form.Set("identity", r.cfg.SpaceTrackIdentity)
	form.Set("password", r.cfg.SpaceTrackPassword)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.SpaceTrackLoginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("spacetrack login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("spacetrack login failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

func (r *Runner) ingestGPData(content []byte, sourcePeer string) (int, int, string, error) {
	var countOMM, countMPE int
	normalized := sha256.New()

	rows, err := parseCSV(content)
	if err != nil {
		return 0, 0, "", err
	}

	for _, row := range rows {
		norad, ok := parseUint32(getValue(row, "NORAD_CAT_ID", "NORAD_CAT_NUM"))
		if !ok || norad == 0 {
			continue
		}

		builder := sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectName(valueOr(getValue(row, "OBJECT_NAME", "SATNAME", "NAME"), fmt.Sprintf("SAT-%d", norad))).
			WithObjectID(valueOr(getValue(row, "OBJECT_ID", "INTLDES", "INTERNATIONAL_DESIGNATOR"), fmt.Sprintf("NORAD-%d", norad)))

		if epoch := normalizeEpoch(getValue(row, "EPOCH", "EPOCH_UTC")); epoch != "" {
			builder = builder.WithEpoch(epoch)
		}
		if v, ok := parseFloat(getValue(row, "MEAN_MOTION", "N")); ok {
			builder = builder.WithMeanMotion(v)
		}
		if v, ok := parseFloat(getValue(row, "ECCENTRICITY", "ECC")); ok {
			builder = builder.WithEccentricity(v)
		}
		if v, ok := parseFloat(getValue(row, "INCLINATION", "INC")); ok {
			builder = builder.WithInclination(v)
		}
		if v, ok := parseFloat(getValue(row, "RA_OF_ASC_NODE", "RAAN")); ok {
			builder = builder.WithRaOfAscNode(v)
		}
		if v, ok := parseFloat(getValue(row, "ARG_OF_PERICENTER", "ARGP")); ok {
			builder = builder.WithArgOfPericenter(v)
		}
		if v, ok := parseFloat(getValue(row, "MEAN_ANOMALY", "MA")); ok {
			builder = builder.WithMeanAnomaly(v)
		}

		ommBytes := builder.Build()
		if _, err := r.store.Store("OMM.fbs", ommBytes, sourcePeer, nil); err != nil {
			return countOMM, countMPE, "", err
		}
		writeNormalizedHashRecord(normalized, "OMM.fbs", ommBytes)
		countOMM++

		epochUnix := int64(0)
		if epoch := normalizeEpoch(getValue(row, "EPOCH", "EPOCH_UTC")); epoch != "" {
			if t, err := parseEpoch(epoch); err == nil {
				epochUnix = t.Unix()
			}
		}
		mpeBytes := buildMPE(
			valueOr(getValue(row, "OBJECT_ID", "INTLDES", "INTERNATIONAL_DESIGNATOR"), fmt.Sprintf("NORAD-%d", norad)),
			epochUnix,
			parseFloatOrZero(getValue(row, "MEAN_MOTION", "N")),
			parseFloatOrZero(getValue(row, "ECCENTRICITY", "ECC")),
			parseFloatOrZero(getValue(row, "INCLINATION", "INC")),
			parseFloatOrZero(getValue(row, "RA_OF_ASC_NODE", "RAAN")),
			parseFloatOrZero(getValue(row, "ARG_OF_PERICENTER", "ARGP")),
			parseFloatOrZero(getValue(row, "MEAN_ANOMALY", "MA")),
			parseFloatOrZero(getValue(row, "BSTAR", "B_STAR")),
		)
		if _, err := r.store.Store("MPE.fbs", mpeBytes, sourcePeer, nil); err != nil {
			return countOMM, countMPE, "", err
		}
		writeNormalizedHashRecord(normalized, "MPE.fbs", mpeBytes)
		countMPE++
	}

	return countOMM, countMPE, hex.EncodeToString(normalized.Sum(nil)), nil
}

func (r *Runner) ingestSatcatData(content []byte, sourcePeer string) (int, string, error) {
	rows, err := parseSatcatRows(content)
	if err != nil {
		return 0, "", err
	}

	count := 0
	normalized := sha256.New()
	for _, row := range rows {
		norad, ok := parseUint32(getValue(row, "NORAD_CAT_ID", "NORAD_CAT_NUM", "NORAD"))
		if !ok || norad == 0 {
			continue
		}

		builder := sds.NewCATBuilder().
			WithNoradCatID(norad).
			WithObjectName(valueOr(getValue(row, "OBJECT_NAME", "SATNAME", "NAME"), fmt.Sprintf("SAT-%d", norad))).
			WithObjectID(valueOr(getValue(row, "OBJECT_ID", "INTLDES", "INTERNATIONAL_DESIGNATOR"), fmt.Sprintf("NORAD-%d", norad)))

		if launchDate := strings.TrimSpace(getValue(row, "LAUNCH_DATE", "LAUNCH")); launchDate != "" {
			builder = builder.WithLaunchDate(launchDate)
		}

		period := parseFloatOrZero(getValue(row, "PERIOD"))
		inclination := parseFloatOrZero(getValue(row, "INCLINATION", "INCL"))
		apogee := parseFloatOrZero(getValue(row, "APOGEE", "APOGEE_KM"))
		perigee := parseFloatOrZero(getValue(row, "PERIGEE", "PERIGEE_KM"))
		builder = builder.WithOrbitalParams(period, inclination, apogee, perigee)

		if v, ok := parseFloat(getValue(row, "MASS", "MASS_KG")); ok {
			builder = builder.WithMass(v)
		}
		if v, ok := parseFloat(getValue(row, "SIZE", "SIZE_M")); ok {
			builder = builder.WithSize(v)
		}
		if v := strings.TrimSpace(getValue(row, "MANEUVERABLE", "MAN")); v != "" {
			builder = builder.WithManeuverable(parseTruthy(v))
		}

		catBytes := builder.Build()
		if _, err := r.store.Store("CAT.fbs", catBytes, sourcePeer, nil); err != nil {
			return count, "", err
		}
		writeNormalizedHashRecord(normalized, "CAT.fbs", catBytes)
		count++
	}

	return count, hex.EncodeToString(normalized.Sum(nil)), nil
}

func (r *Runner) ingestSpaceWeatherData(content []byte, sourcePeer string) (int, string, error) {
	rows, err := parseCSV(content)
	if err != nil {
		return 0, "", err
	}

	count := 0
	normalized := sha256.New()
	for _, row := range rows {
		spwDate := normalizeSpaceWeatherDate(getValue(row, "DATE"))
		if spwDate == "" {
			continue
		}

		spwBytes := buildSPW(row, spwDate)
		if _, err := r.store.Store("SPW.fbs", spwBytes, sourcePeer, nil); err != nil {
			return count, "", err
		}
		writeNormalizedHashRecord(normalized, "SPW.fbs", spwBytes)
		count++
	}
	if count == 0 {
		return 0, "", fmt.Errorf("no SPW rows parsed")
	}
	return count, hex.EncodeToString(normalized.Sum(nil)), nil
}

func archiveFilenameForURL(rawURL, fallback string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fallback
	}

	base := strings.TrimSpace(path.Base(parsed.Path))
	if base == "" || base == "." || base == "/" {
		return fallback
	}
	if strings.EqualFold(base, "gp.php") {
		return fallback
	}
	return base
}

func (r *Runner) fetchBytes(ctx context.Context, sourceURL string) ([]byte, error) {
	data, _, err := r.fetchBytesWithMetadata(ctx, sourceURL)
	return data, err
}

func (r *Runner) fetchBytesWithMetadata(ctx context.Context, sourceURL string, validators ...fetchMetadata) ([]byte, fetchMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, fetchMetadata{}, err
	}
	if len(validators) > 0 {
		if etag := strings.TrimSpace(validators[0].ETag); etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		if lastModified := strings.TrimSpace(validators[0].LastModified); lastModified != "" {
			req.Header.Set("If-Modified-Since", lastModified)
		}
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fetchMetadata{}, err
	}
	defer resp.Body.Close()
	metadata := fetchMetadata{
		SourceURL:    sourceURL,
		HTTPStatus:   resp.StatusCode,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		ContentType:  resp.Header.Get("Content-Type"),
		RetrievedAt:  time.Now().UTC(),
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, metadata, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024)) // 100MB limit
	return data, metadata, err
}

func (r *Runner) fetchWithCache(ctx context.Context, sourceURL, cacheName string, minInterval time.Duration) ([]byte, fetchMetadata, error) {
	cachePath := filepath.Join(r.cfg.RawPath, "cache", cacheName)

	if data, modTime, ok, err := readCachedPayload(cachePath, minInterval); err == nil && ok {
		metadata := readCachedFetchMetadata(cachePath)
		metadata.SourceURL = sourceURL
		metadata.RetrievedAt = firstNonZeroTime(metadata.RetrievedAt, modTime.UTC())
		metadata.FromCache = true
		metadata.CachePath = cachePath
		return data, metadata, nil
	} else if err != nil {
		log.Warnf("Failed reading cache %s: %v", cachePath, err)
	}

	validators := readCachedFetchMetadata(cachePath)
	data, metadata, attempts, err := r.fetchBytesWithRetry(ctx, sourceURL, validators)
	if err != nil {
		if metadata.HTTPStatus == http.StatusNotModified {
			if fallback, modTime, ok, cacheErr := readCachedPayload(cachePath, 0); cacheErr == nil && ok {
				r.clearFetchFailure(cacheName)
				metadata.SourceURL = sourceURL
				metadata.RetrievedAt = firstNonZeroTime(validators.RetrievedAt, modTime.UTC())
				metadata.ETag = firstNonEmptyString(metadata.ETag, validators.ETag)
				metadata.LastModified = firstNonEmptyString(metadata.LastModified, validators.LastModified)
				metadata.ContentType = firstNonEmptyString(metadata.ContentType, validators.ContentType)
				metadata.FromCache = true
				metadata.CachePath = cachePath
				return fallback, metadata, nil
			}
			return nil, metadata, err
		}
		if isFetchHardStopStatus(metadata.HTTPStatus) {
			r.recordFetchFailureForReview(cacheName, sourceURL, metadata, attempts, err)
			return nil, metadata, err
		}
		if fallback, modTime, ok, cacheErr := readCachedPayload(cachePath, 0); cacheErr == nil && ok {
			log.Warnf("CelesTrak fetch failed (%v); using stale cache %s", err, cachePath)
			r.recordFetchFailure(cacheName, attempts)
			cachedMetadata := readCachedFetchMetadata(cachePath)
			cachedMetadata.SourceURL = sourceURL
			cachedMetadata.RetrievedAt = firstNonZeroTime(cachedMetadata.RetrievedAt, modTime.UTC())
			cachedMetadata.FromCache = true
			cachedMetadata.CachePath = cachePath
			return fallback, cachedMetadata, nil
		}
		r.recordFetchFailureForReview(cacheName, sourceURL, metadata, attempts, err)
		return nil, metadata, err
	}
	r.clearFetchFailure(cacheName)

	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		log.Warnf("Failed creating cache directory for %s: %v", cachePath, err)
		return data, metadata, nil
	}
	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		log.Warnf("Failed writing cache %s: %v", cachePath, err)
	}
	if err := writeCachedFetchMetadata(cachePath, metadata); err != nil {
		log.Warnf("Failed writing cache metadata %s: %v", cacheMetadataPath(cachePath), err)
	}

	return data, metadata, nil
}

func (r *Runner) fetchBytesWithRetry(ctx context.Context, sourceURL string, validators fetchMetadata) ([]byte, fetchMetadata, int, error) {
	var (
		data     []byte
		metadata fetchMetadata
		err      error
	)
	for attempt := 1; attempt <= fetchRetryBudget; attempt++ {
		data, metadata, err = r.fetchBytesWithMetadata(ctx, sourceURL, validators)
		if err == nil || !shouldRetryFetch(ctx, metadata, err) || attempt == fetchRetryBudget {
			return data, metadata, attempt, err
		}
		select {
		case <-ctx.Done():
			return nil, metadata, attempt, ctx.Err()
		case <-time.After(fetchRetryBackoff * time.Duration(attempt)):
		}
	}
	return data, metadata, fetchRetryBudget, err
}

func shouldRetryFetch(ctx context.Context, metadata fetchMetadata, err error) bool {
	if err == nil || ctx.Err() != nil || metadata.HTTPStatus == http.StatusNotModified || isFetchHardStopStatus(metadata.HTTPStatus) {
		return false
	}
	return metadata.HTTPStatus == 0 ||
		metadata.HTTPStatus == http.StatusTooManyRequests ||
		metadata.HTTPStatus >= http.StatusInternalServerError
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Now().UTC()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isFetchHardStopStatus(status int) bool {
	return status == http.StatusForbidden ||
		status == http.StatusNotFound ||
		(status >= http.StatusMultipleChoices && status < http.StatusBadRequest && status != http.StatusNotModified)
}

func cacheMetadataPath(cachePath string) string {
	return cachePath + ".metadata.json"
}

func readCachedFetchMetadata(cachePath string) fetchMetadata {
	data, err := os.ReadFile(cacheMetadataPath(cachePath))
	if err != nil || len(data) == 0 {
		return fetchMetadata{}
	}
	var metadata fetchMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		log.Warnf("Failed parsing cache metadata %s: %v", cacheMetadataPath(cachePath), err)
		return fetchMetadata{}
	}
	return metadata
}

func writeCachedFetchMetadata(cachePath string, metadata fetchMetadata) error {
	metadata.FromCache = false
	metadata.CachePath = ""
	payload, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cacheMetadataPath(cachePath), payload, 0644)
}

func (r *Runner) recordFetchFailure(cacheName string, attempts int) {
	key := fetchFailureKey(cacheName)
	r.checkpoints.setString(key, strconv.Itoa(attempts))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist fetch failure checkpoint %s: %v", key, err)
	}
}

func (r *Runner) recordFetchFailureForReview(cacheName, sourceURL string, metadata fetchMetadata, attempts int, err error) {
	r.recordFetchFailure(cacheName, attempts)
	reviewKey := fetchHumanReviewKey(cacheName)
	value := time.Now().UTC().Format(time.RFC3339)
	r.checkpoints.setString(reviewKey, value)
	if saveErr := r.checkpoints.save(); saveErr != nil {
		log.Warnf("Failed to persist human-review checkpoint %s: %v", reviewKey, saveErr)
	}
	log.Errorf("Human review required for ingest fetch %s after %d attempt(s): url=%s status=%d error=%v",
		cacheName, attempts, sourceURL, metadata.HTTPStatus, err)
}

func (r *Runner) clearFetchFailure(cacheName string) {
	r.checkpoints.delete(fetchFailureKey(cacheName))
	r.checkpoints.delete(fetchHumanReviewKey(cacheName))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to clear fetch failure checkpoints for %s: %v", cacheName, err)
	}
}

func fetchFailureKey(cacheName string) string {
	return "fetch_failure_count_" + checkpointSafeKey(cacheName)
}

func fetchHumanReviewKey(cacheName string) string {
	return "fetch_human_review_required_" + checkpointSafeKey(cacheName)
}

func checkpointSafeKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func readCachedPayload(path string, maxAge time.Duration) ([]byte, time.Time, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, time.Time{}, false, nil
		}
		return nil, time.Time{}, false, err
	}

	if maxAge > 0 && time.Since(info.ModTime()) >= maxAge {
		return nil, time.Time{}, false, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	if len(data) == 0 {
		return nil, time.Time{}, false, nil
	}
	return data, info.ModTime(), true, nil
}

func (r *Runner) archiveRaw(source, filename string, data []byte) error {
	day := time.Now().UTC().Format("2006-01-02")
	dir := filepath.Join(r.cfg.RawPath, source, day)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), data, 0644)
}

func writeNormalizedHashRecord(h interface{ Write([]byte) (int, error) }, schemaName string, data []byte) {
	_, _ = h.Write([]byte(schemaName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(data)
	_, _ = h.Write([]byte{0})
}

func (r *Runner) recordIngestBatchProvenance(source string, data []byte, metadata fetchMetadata, parserVersion, normalizedHash string, schemaCounts map[string]int, warnings []string) error {
	if metadata.RetrievedAt.IsZero() {
		metadata.RetrievedAt = time.Now().UTC()
	}
	sum := sha256.Sum256(data)
	normalizedCount := 0
	for _, count := range schemaCounts {
		normalizedCount += count
	}
	payload := ingestBatchProvenance{
		SourceURL:        metadata.SourceURL,
		HTTPStatus:       metadata.HTTPStatus,
		ETag:             metadata.ETag,
		LastModified:     metadata.LastModified,
		ContentType:      metadata.ContentType,
		RetrievedAt:      metadata.RetrievedAt.UTC().Format(time.RFC3339Nano),
		ParserVersion:    parserVersion,
		SourceSHA256:     hex.EncodeToString(sum[:]),
		NormalizedSHA256: normalizedHash,
		NormalizedCount:  normalizedCount,
		SchemaCounts:     schemaCounts,
		SchemaHashes:     schemaHashes(schemaCounts),
		Warnings:         warnings,
		FromCache:        metadata.FromCache,
		CachePath:        metadata.CachePath,
	}
	if payload.Warnings == nil {
		payload.Warnings = []string{}
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(r.cfg.RawPath, "provenance", source)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	filename := time.Now().UTC().Format("20060102T150405.000000000Z") + ".json"
	return os.WriteFile(filepath.Join(dir, filename), encoded, 0644)
}

func warningsForFetch(metadata fetchMetadata) []string {
	if metadata.FromCache {
		return []string{"used cached payload"}
	}
	return []string{}
}

func schemaHashes(schemaCounts map[string]int) map[string]string {
	hashes := make(map[string]string, len(schemaCounts))
	registry, err := sds.NewSchemaRegistry()
	if err != nil {
		return hashes
	}
	for schemaName := range schemaCounts {
		content, ok := registry.Get(schemaName)
		if !ok || len(content) == 0 {
			continue
		}
		sum := sha256.Sum256(content)
		hashes[schemaName] = hex.EncodeToString(sum[:])
	}
	return hashes
}

func buildMPE(entityID string, epochUnix int64, meanMotion, ecc, incl, raan, argp, meanAnomaly, bstar float64) []byte {
	builder := flatbuffers.NewBuilder(256)
	entityIDOffset := builder.CreateString(entityID)

	MPEFB.MPEStart(builder)
	MPEFB.MPEAddENTITY_ID(builder, entityIDOffset)
	if epochUnix > 0 {
		MPEFB.MPEAddEPOCH(builder, float64(epochUnix))
	}
	if meanMotion != 0 {
		MPEFB.MPEAddMEAN_MOTION(builder, meanMotion)
	}
	if ecc != 0 {
		MPEFB.MPEAddECCENTRICITY(builder, ecc)
	}
	if incl != 0 {
		MPEFB.MPEAddINCLINATION(builder, incl)
	}
	if raan != 0 {
		MPEFB.MPEAddRA_OF_ASC_NODE(builder, raan)
	}
	if argp != 0 {
		MPEFB.MPEAddARG_OF_PERICENTER(builder, argp)
	}
	if meanAnomaly != 0 {
		MPEFB.MPEAddMEAN_ANOMALY(builder, meanAnomaly)
	}
	if bstar != 0 {
		MPEFB.MPEAddBSTAR(builder, bstar)
	}
	mpe := MPEFB.MPEEnd(builder)
	MPEFB.FinishSizePrefixedMPEBuffer(builder, mpe)

	out := make([]byte, len(builder.FinishedBytes()))
	copy(out, builder.FinishedBytes())
	return out
}

func buildSPW(row map[string]string, spwDate string) []byte {
	builder := flatbuffers.NewBuilder(256)
	dateOffset := builder.CreateString(spwDate)

	SPWFB.SPWStart(builder)
	SPWFB.SPWAddDATE(builder, dateOffset)
	SPWFB.SPWAddBSRN(builder, parseInt32OrZero(getValue(row, "BSRN")))
	SPWFB.SPWAddND(builder, parseInt32OrZero(getValue(row, "ND")))
	SPWFB.SPWAddKP1(builder, parseKpTenthsOrZero(getValue(row, "KP1")))
	SPWFB.SPWAddKP2(builder, parseKpTenthsOrZero(getValue(row, "KP2")))
	SPWFB.SPWAddKP3(builder, parseKpTenthsOrZero(getValue(row, "KP3")))
	SPWFB.SPWAddKP4(builder, parseKpTenthsOrZero(getValue(row, "KP4")))
	SPWFB.SPWAddKP5(builder, parseKpTenthsOrZero(getValue(row, "KP5")))
	SPWFB.SPWAddKP6(builder, parseKpTenthsOrZero(getValue(row, "KP6")))
	SPWFB.SPWAddKP7(builder, parseKpTenthsOrZero(getValue(row, "KP7")))
	SPWFB.SPWAddKP8(builder, parseKpTenthsOrZero(getValue(row, "KP8")))
	SPWFB.SPWAddKP_SUM(builder, parseKpTenthsOrZero(getValue(row, "KP_SUM")))
	SPWFB.SPWAddAP1(builder, parseInt32OrZero(getValue(row, "AP1")))
	SPWFB.SPWAddAP2(builder, parseInt32OrZero(getValue(row, "AP2")))
	SPWFB.SPWAddAP3(builder, parseInt32OrZero(getValue(row, "AP3")))
	SPWFB.SPWAddAP4(builder, parseInt32OrZero(getValue(row, "AP4")))
	SPWFB.SPWAddAP5(builder, parseInt32OrZero(getValue(row, "AP5")))
	SPWFB.SPWAddAP6(builder, parseInt32OrZero(getValue(row, "AP6")))
	SPWFB.SPWAddAP7(builder, parseInt32OrZero(getValue(row, "AP7")))
	SPWFB.SPWAddAP8(builder, parseInt32OrZero(getValue(row, "AP8")))
	SPWFB.SPWAddAP_AVG(builder, parseInt32OrZero(getValue(row, "AP_AVG")))
	SPWFB.SPWAddCP(builder, parseFloat32OrZero(getValue(row, "CP")))
	SPWFB.SPWAddC9(builder, parseInt32OrZero(getValue(row, "C9")))
	SPWFB.SPWAddISN(builder, parseInt32OrZero(getValue(row, "ISN")))
	SPWFB.SPWAddF107_OBS(builder, parseFloat32OrZero(getValue(row, "F10.7_OBS", "F107_OBS")))
	SPWFB.SPWAddF107_ADJ(builder, parseFloat32OrZero(getValue(row, "F10.7_ADJ", "F107_ADJ")))
	SPWFB.SPWAddF107_DATA_TYPE(builder, parseF107DataType(getValue(row, "F10.7_DATA_TYPE", "F107_DATA_TYPE")))
	SPWFB.SPWAddF107_OBS_CENTER81(builder, parseFloat32OrZero(getValue(row, "F10.7_OBS_CENTER81", "F107_OBS_CENTER81")))
	SPWFB.SPWAddF107_OBS_LAST81(builder, parseFloat32OrZero(getValue(row, "F10.7_OBS_LAST81", "F107_OBS_LAST81")))
	SPWFB.SPWAddF107_ADJ_CENTER81(builder, parseFloat32OrZero(getValue(row, "F10.7_ADJ_CENTER81", "F107_ADJ_CENTER81")))
	SPWFB.SPWAddF107_ADJ_LAST81(builder, parseFloat32OrZero(getValue(row, "F10.7_ADJ_LAST81", "F107_ADJ_LAST81")))
	spw := SPWFB.SPWEnd(builder)
	SPWFB.FinishSizePrefixedSPWBuffer(builder, spw)

	out := make([]byte, len(builder.FinishedBytes()))
	copy(out, builder.FinishedBytes())
	return out
}

func parseCSV(content []byte) ([]map[string]string, error) {
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV header: %w", err)
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("empty CSV header")
	}
	headers[0] = strings.TrimPrefix(headers[0], "\ufeff")

	normalized := make([]string, len(headers))
	for i, h := range headers {
		normalized[i] = normalizeKey(h)
	}

	rows := make([]map[string]string, 0, 1024)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read CSV row: %w", err)
		}

		row := make(map[string]string, len(normalized))
		for i, key := range normalized {
			if i < len(record) {
				row[key] = strings.TrimSpace(record[i])
			}
		}
		rows = append(rows, row)
	}

	return rows, nil
}

func parseSatcatRows(content []byte) ([]map[string]string, error) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty SATCAT payload")
	}

	scanner := bufio.NewScanner(bytes.NewReader(trimmed))
	firstLine := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		firstLine = line
		break
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan SATCAT payload: %w", err)
	}
	if firstLine == "" {
		return nil, fmt.Errorf("empty SATCAT payload")
	}
	if strings.Contains(firstLine, ",") {
		return parseCSV(content)
	}

	return parseSatcatFixedWidth(content)
}

func parseSatcatFixedWidth(content []byte) ([]map[string]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)

	rows := make([]map[string]string, 0, 8192)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		status := satcatColumn(line, 20, 22)
		row := map[string]string{
			"OBJECT_ID":    satcatColumn(line, 1, 11),
			"NORAD_CAT_ID": satcatColumn(line, 13, 18),
			"OBJECT_NAME":  satcatColumn(line, 24, 47),
			"LAUNCH_DATE":  satcatColumn(line, 57, 66),
			"LAUNCH_SITE":  satcatColumn(line, 69, 73),
			"DECAY_DATE":   satcatColumn(line, 76, 85),
			"PERIOD":       satcatColumn(line, 88, 94),
			"INCLINATION":  satcatColumn(line, 97, 101),
			"APOGEE":       satcatColumn(line, 105, 109),
			"PERIGEE":      satcatColumn(line, 113, 117),
			"RCS":          satcatColumn(line, 121, 127),
		}

		if parseSatcatManeuverable(status) {
			row["MANEUVERABLE"] = "true"
		}
		rows = append(rows, row)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SATCAT rows: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no SATCAT rows parsed")
	}
	return rows, nil
}

func satcatColumn(line string, start, end int) string {
	if start < 1 || end < start {
		return ""
	}
	if len(line) < start {
		return ""
	}
	if end > len(line) {
		end = len(line)
	}
	return strings.TrimSpace(line[start-1 : end])
}

func parseSatcatManeuverable(status string) bool {
	status = strings.ToUpper(strings.TrimSpace(status))
	return strings.HasPrefix(status, "M")
}

func normalizeKey(raw string) string {
	s := strings.TrimSpace(strings.ToUpper(raw))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

func getValue(row map[string]string, keys ...string) string {
	for _, key := range keys {
		v := strings.TrimSpace(row[normalizeKey(key)])
		if v != "" {
			return v
		}
	}
	return ""
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func parseFloat(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func parseFloatOrZero(raw string) float64 {
	if f, ok := parseFloat(raw); ok {
		return f
	}
	return 0
}

func parseFloat32OrZero(raw string) float32 {
	if f, ok := parseFloat(raw); ok {
		return float32(f)
	}
	return 0
}

func parseInt32OrZero(raw string) int32 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0
	}
	return int32(v)
}

func parseKpTenthsOrZero(raw string) int32 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if strings.Contains(raw, ".") {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return int32(math.Round(f * 10))
		}
		return 0
	}
	return parseInt32OrZero(raw)
}

func parseF107DataType(raw string) SPWFB.F107DataType {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "INT":
		return SPWFB.F107DataTypeINT
	case "PRD":
		return SPWFB.F107DataTypePRD
	case "PRM":
		return SPWFB.F107DataTypePRM
	default:
		return SPWFB.F107DataTypeOBS
	}
}

func parseUint32(raw string) (uint32, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

func parseTruthy(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "1", "y", "yes", "true", "t", "m":
		return true
	default:
		return false
	}
}

func normalizeEpoch(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := parseEpoch(raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return raw
}

func normalizeSpaceWeatherDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := parseEpoch(raw); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return raw
}

func parseEpoch(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		if f > 0 {
			sec := int64(f)
			nsec := int64((f - float64(sec)) * float64(time.Second))
			return time.Unix(sec, nsec).UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported epoch format: %q", raw)
}

type checkpointStore struct {
	path  string
	mu    sync.RWMutex
	state map[string]string
}

func newCheckpointStore(path string) (*checkpointStore, error) {
	cp := &checkpointStore{path: path, state: make(map[string]string)}
	if err := cp.load(); err != nil {
		return nil, err
	}
	return cp, nil
}

func (c *checkpointStore) load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed reading checkpoints %s: %w", c.path, err)
	}
	if len(data) == 0 {
		return nil
	}

	var state map[string]string
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed parsing checkpoints %s: %w", c.path, err)
	}
	c.state = state
	return nil
}

func (c *checkpointStore) save() error {
	c.mu.RLock()
	stateCopy := make(map[string]string, len(c.state))
	for k, v := range c.state {
		stateCopy[k] = v
	}
	c.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return err
	}

	payload, err := json.MarshalIndent(stateCopy, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := c.path + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, c.path)
}

func (c *checkpointStore) getString(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state[key]
}

func (c *checkpointStore) setString(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state[key] = value
}

func (c *checkpointStore) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.state, key)
}
