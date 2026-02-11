// Package ingest provides data source sync workers for OMM/MPE/CAT ingestion.
package ingest

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	MPEFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	flatbuffers "github.com/google/flatbuffers/go"
	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

var log = logging.Logger("ingest")

const (
	defaultCelestrakCatalogURL = "https://celestrak.org/pub/GP/catalog.csv"
	defaultCelestrakSatcatURL  = "https://celestrak.org/pub/satcat.csv"
	defaultSpaceTrackLoginURL  = "https://www.space-track.org/ajaxauth/login"
	defaultSpaceTrackQueryTmpl = "https://www.space-track.org/basicspacedata/query/class/gp_history/EPOCH/%s--%s/format/csv"
)

// Config controls ingestion worker behavior.
type Config struct {
	StoragePath string
	RawPath     string
	Once        bool

	CelestrakCatalogURL string
	CelestrakSatcatURL  string
	CelestrakInterval   time.Duration
	SatcatInterval      time.Duration

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
	if cfg.SpaceTrackLoginURL == "" {
		cfg.SpaceTrackLoginURL = defaultSpaceTrackLoginURL
	}
	if cfg.SpaceTrackQueryTmpl == "" {
		cfg.SpaceTrackQueryTmpl = defaultSpaceTrackQueryTmpl
	}
	if cfg.CelestrakInterval <= 0 {
		cfg.CelestrakInterval = time.Hour
	}
	if cfg.SatcatInterval <= 0 {
		cfg.SatcatInterval = 24 * time.Hour
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
	stTicker := time.NewTicker(r.cfg.SpaceTrackPollInterval)
	defer gpTicker.Stop()
	defer satTicker.Stop()
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
	if err := r.syncSpaceTrackGapFill(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (r *Runner) syncCelestrakGP(ctx context.Context) error {
	data, err := r.fetchBytes(ctx, r.cfg.CelestrakCatalogURL)
	if err != nil {
		return fmt.Errorf("fetch celestrak catalog: %w", err)
	}

	if err := r.archiveRaw("celestrak", "catalog.csv", data); err != nil {
		log.Warnf("Failed to archive CelesTrak catalog.csv: %v", err)
	}

	countOMM, countMPE, err := r.ingestGPData(data, "source:celestrak")
	if err != nil {
		return fmt.Errorf("ingest celestrak catalog: %w", err)
	}

	r.checkpoints.setString("celestrak_gp_last_success", time.Now().UTC().Format(time.RFC3339))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist checkpoints: %v", err)
	}

	log.Infof("CelesTrak GP sync complete: OMM=%d MPE=%d", countOMM, countMPE)
	return nil
}

func (r *Runner) syncCelestrakSatcat(ctx context.Context) error {
	data, err := r.fetchBytes(ctx, r.cfg.CelestrakSatcatURL)
	if err != nil {
		return fmt.Errorf("fetch celestrak satcat: %w", err)
	}

	if err := r.archiveRaw("celestrak", "satcat.csv", data); err != nil {
		log.Warnf("Failed to archive CelesTrak satcat.csv: %v", err)
	}

	countCAT, err := r.ingestSatcatData(data, "source:celestrak")
	if err != nil {
		return fmt.Errorf("ingest celestrak satcat: %w", err)
	}

	r.checkpoints.setString("celestrak_satcat_last_success", time.Now().UTC().Format(time.RFC3339))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist checkpoints: %v", err)
	}

	log.Infof("CelesTrak SATCAT sync complete: CAT=%d", countCAT)
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
		data, err := r.fetchBytes(ctx, queryURL)
		if err != nil {
			return fmt.Errorf("fetch spacetrack range %s..%s: %w", batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"), err)
		}

		archiveName := fmt.Sprintf("gp_history_%s_%s.csv", batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"))
		if err := r.archiveRaw("spacetrack", archiveName, data); err != nil {
			log.Warnf("Failed to archive Space-Track data %s: %v", archiveName, err)
		}

		countOMM, countMPE, err := r.ingestGPData(data, "source:spacetrack")
		if err != nil {
			return fmt.Errorf("ingest spacetrack range %s..%s: %w", batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"), err)
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

func (r *Runner) ingestGPData(content []byte, sourcePeer string) (int, int, error) {
	var countOMM, countMPE int

	rows, err := parseCSV(content)
	if err != nil {
		return 0, 0, err
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
			return countOMM, countMPE, err
		}
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
			return countOMM, countMPE, err
		}
		countMPE++
	}

	return countOMM, countMPE, nil
}

func (r *Runner) ingestSatcatData(content []byte, sourcePeer string) (int, error) {
	rows, err := parseCSV(content)
	if err != nil {
		return 0, err
	}

	count := 0
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

		if _, err := r.store.Store("CAT.fbs", builder.Build(), sourcePeer, nil); err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

func (r *Runner) fetchBytes(ctx context.Context, sourceURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}

func (r *Runner) archiveRaw(source, filename string, data []byte) error {
	day := time.Now().UTC().Format("2006-01-02")
	dir := filepath.Join(r.cfg.RawPath, source, day)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), data, 0644)
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
