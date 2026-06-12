package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// UDL (USSF Unified Data Library) pull adapter.
//
// Feeds implemented (only schemas with existing builders are targeted):
//   - /udl/elset -> OMM.fbs (sds.OMMBuilder)
//   - /udl/sgi   -> SPW.fbs (buildSPW, same path as CelesTrak space weather)
//
// /udl/conjunction is intentionally NOT implemented because no CDM builder
// exists in internal/sds/builders.go. /udl/statevector is likewise skipped
// (no matching builder/schema mapping).
//
// CLASSIFICATION_MARKING handling: each record's CLASSIFICATION_MARKING is
// passed to OMMBuilder.WithClassificationType so the value is stored in the
// CCSDS CLASSIFICATION_TYPE field of the serialised OMM FlatBuffer.  Markings
// are also counted per-batch and written to the provenance JSON for audit
// purposes (classification_markings counts).
const (
	defaultUDLBaseURL     = "https://unifieddatalibrary.com"
	udlProviderID         = "udl"
	parserVersionUDLElset = "udl-elset/v1"
	parserVersionUDLSGI   = "udl-sgi/v1"
	defaultUDLMaxResults  = 10000
	udlEpochFormat        = "2006-01-02T15:04:05.000000Z"
	udlElsetCheckpointKey = "udl_elset_last_day"
	udlSGICheckpointKey   = "udl_sgi_last_day"
)

// udlFeedSpec describes one UDL REST feed pulled with epoch-windowed batches.
type udlFeedSpec struct {
	name          string // provenance + source-tag name, e.g. "udl-elset"
	path          string // REST path, e.g. "/udl/elset"
	rangeParam    string // epoch-range query parameter name
	parserVersion string
	checkpointKey string
	schemaName    string
	archivePrefix string
	ingest        func(records []map[string]string, sourcePeer string, tags storage.SourceTags) (int, string, map[string]int, []string, error)
}

func (r *Runner) udlElsetFeed() udlFeedSpec {
	return udlFeedSpec{
		name:          "udl-elset",
		path:          "/udl/elset",
		rangeParam:    "epoch",
		parserVersion: parserVersionUDLElset,
		checkpointKey: udlElsetCheckpointKey,
		schemaName:    "OMM.fbs",
		archivePrefix: "elset",
		ingest:        r.ingestUDLElsetRecords,
	}
}

func (r *Runner) udlSGIFeed() udlFeedSpec {
	return udlFeedSpec{
		name:          "udl-sgi",
		path:          "/udl/sgi",
		rangeParam:    "sgiDate",
		parserVersion: parserVersionUDLSGI,
		checkpointKey: udlSGICheckpointKey,
		schemaName:    "SPW.fbs",
		archivePrefix: "sgi",
		ingest:        r.ingestUDLSGIRecords,
	}
}

// syncUDL pulls all configured UDL feeds with basic-auth credentials.
func (r *Runner) syncUDL(ctx context.Context) error {
	if !r.cfg.UDLEnabled {
		return nil
	}
	if err := r.requireFreeDisk("UDL sync"); err != nil {
		return err
	}
	if r.cfg.UDLUsername == "" || r.cfg.UDLPassword == "" {
		log.Warn("UDL credentials missing; skipping UDL sync")
		return nil
	}

	var errs []string
	for _, feed := range []udlFeedSpec{r.udlElsetFeed(), r.udlSGIFeed()} {
		if err := ctx.Err(); err != nil {
			if len(errs) > 0 {
				return errors.New(strings.Join(errs, "; "))
			}
			return err
		}
		if err := r.syncUDLFeed(ctx, feed); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// syncUDLFeed performs epoch-windowed incremental pulls for one UDL feed
// using day-batched windows and the shared checkpoint store, mirroring the
// Space-Track gap-fill worker.
func (r *Runner) syncUDLFeed(ctx context.Context, feed udlFeedSpec) error {
	startDay, err := r.resolveUDLStartDay(feed.checkpointKey)
	if err != nil {
		return err
	}

	endDay := time.Now().UTC().AddDate(0, 0, -1)
	if startDay.After(endDay) {
		return nil
	}

	for batchStart := startDay; !batchStart.After(endDay); batchStart = batchStart.AddDate(0, 0, r.cfg.UDLBatchDays) {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		batchEnd := batchStart.AddDate(0, 0, r.cfg.UDLBatchDays-1)
		if batchEnd.After(endDay) {
			batchEnd = endDay
		}

		windowStart := dayStartUTC(batchStart)
		windowEnd := dayStartUTC(batchEnd).AddDate(0, 0, 1)

		records, raw, metadata, err := r.fetchUDLWindow(ctx, feed, windowStart, windowEnd)
		if err != nil {
			return fmt.Errorf("fetch %s range %s..%s: %w", feed.name, batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"), err)
		}

		if len(records) > 0 {
			archiveName := fmt.Sprintf("%s_%s_%s.json", feed.archivePrefix, batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"))
			if err := r.archiveRaw("udl", archiveName, raw); err != nil {
				log.Warnf("Failed to archive UDL data %s: %v", archiveName, err)
			}

			tags := sourceTags(udlProviderID, feed.name, metadata.SourceURL, raw)
			count, normalizedHash, markings, warnings, err := feed.ingest(records, "source:udl", tags)
			if err != nil {
				r.recordIngestFailureForReview(feed.name, err)
				return fmt.Errorf("ingest %s range %s..%s: %w", feed.name, batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"), err)
			}
			if err := r.recordIngestBatchProvenanceDetailed(feed.name, raw, metadata, feed.parserVersion, normalizedHash, map[string]int{
				feed.schemaName: count,
			}, warnings, markings); err != nil {
				log.Warnf("Failed to record %s provenance: %v", feed.name, err)
			}
			log.Infof("UDL %s sync %s..%s complete: %s=%d", feed.name, batchStart.Format("2006-01-02"), batchEnd.Format("2006-01-02"), feed.schemaName, count)
		}

		r.checkpoints.setString(feed.checkpointKey, batchEnd.Format("2006-01-02"))
		if err := r.checkpoints.save(); err != nil {
			log.Warnf("Failed to persist checkpoints: %v", err)
		}

		if batchEnd.Before(endDay) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(r.cfg.UDLBatchSleep):
			}
		}
	}

	return nil
}

func (r *Runner) resolveUDLStartDay(checkpointKey string) (time.Time, error) {
	if day := strings.TrimSpace(r.checkpoints.getString(checkpointKey)); day != "" {
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid %s checkpoint %q: %w", checkpointKey, day, err)
		}
		return parsed.AddDate(0, 0, 1), nil
	}

	if strings.TrimSpace(r.cfg.UDLStartDay) != "" {
		parsed, err := time.Parse("2006-01-02", strings.TrimSpace(r.cfg.UDLStartDay))
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid --udl-start-day %q (expected YYYY-MM-DD)", r.cfg.UDLStartDay)
		}
		return parsed, nil
	}

	// Safe default if no checkpoint or explicit start is provided.
	return time.Now().UTC().AddDate(0, 0, -30), nil
}

func dayStartUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// fetchUDLWindow pulls one epoch window with maxResults/firstResult paging.
// It returns the parsed records, the concatenated raw page payloads (used for
// archiving, batch IDs, and provenance hashing), and the metadata of the
// first page request.
func (r *Runner) fetchUDLWindow(ctx context.Context, feed udlFeedSpec, windowStart, windowEnd time.Time) ([]map[string]string, []byte, fetchMetadata, error) {
	maxResults := r.cfg.UDLMaxResults
	if maxResults <= 0 {
		maxResults = defaultUDLMaxResults
	}

	var (
		all      []map[string]string
		combined bytes.Buffer
		meta     fetchMetadata
	)

	for first := 0; ; first += maxResults {
		query := url.Values{}
		query.Set(feed.rangeParam, windowStart.UTC().Format(udlEpochFormat)+".."+windowEnd.UTC().Format(udlEpochFormat))
		query.Set("maxResults", strconv.Itoa(maxResults))
		if first > 0 {
			query.Set("firstResult", strconv.Itoa(first))
		}
		pageURL := strings.TrimRight(r.cfg.UDLBaseURL, "/") + feed.path + "?" + query.Encode()

		data, metadata, err := r.fetchUDLBytes(ctx, pageURL)
		if err != nil {
			return nil, nil, metadata, err
		}
		if first == 0 {
			meta = metadata
		}

		page, err := parseUDLRecords(data)
		if err != nil {
			return nil, nil, metadata, fmt.Errorf("parse UDL %s response: %w", feed.path, err)
		}
		combined.Write(data)
		combined.WriteByte('\n')
		all = append(all, page...)

		if len(page) < maxResults {
			break
		}

		// Polite rate limit between successive pages.
		select {
		case <-ctx.Done():
			return nil, nil, meta, ctx.Err()
		case <-time.After(r.cfg.UDLBatchSleep):
		}
	}

	return all, combined.Bytes(), meta, nil
}

// fetchUDLBytes performs a single basic-auth GET against the UDL REST API.
func (r *Runner) fetchUDLBytes(ctx context.Context, sourceURL string) ([]byte, fetchMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, fetchMetadata{}, err
	}
	req.SetBasicAuth(r.cfg.UDLUsername, r.cfg.UDLPassword)
	req.Header.Set("Accept", "application/json")

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

// parseUDLRecords decodes a UDL JSON array response into normalized
// key/value rows. Keys are normalized so both UDL camelCase (e.g. satNo,
// classificationMarking) and OMM-style upper-snake keys (e.g. NORAD_CAT_ID,
// CLASSIFICATION_MARKING) resolve identically through getValue.
func parseUDLRecords(data []byte) ([]map[string]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var rawRecords []map[string]any
	if err := decoder.Decode(&rawRecords); err != nil {
		return nil, fmt.Errorf("decode UDL JSON array: %w", err)
	}

	records := make([]map[string]string, 0, len(rawRecords))
	for _, raw := range rawRecords {
		row := make(map[string]string, len(raw))
		for key, value := range raw {
			row[normalizeUDLKey(key)] = strings.TrimSpace(udlValueToString(value))
		}
		records = append(records, row)
	}
	return records, nil
}

// normalizeUDLKey converts camelCase UDL JSON keys to the upper-snake form
// used by getValue (satNo -> SAT_NO, classificationMarking ->
// CLASSIFICATION_MARKING). Already upper-snake keys pass through unchanged.
func normalizeUDLKey(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	for i, r := range raw {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(raw[i-1])
			if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
				b.WriteByte('_')
			}
		}
		b.WriteRune(r)
	}
	return normalizeKey(b.String())
}

func udlValueToString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

// ingestUDLElsetRecords maps UDL elset records to OMM FlatBuffers. Malformed
// records (missing/invalid NORAD catalog ID or epoch) are skipped with a
// warning instead of aborting the batch. CLASSIFICATION_MARKING is passed
// through to the per-record OMM CLASSIFICATION_TYPE field via
// OMMBuilder.WithClassificationType; markings are also counted and surfaced
// through provenance metadata for audit purposes.
func (r *Runner) ingestUDLElsetRecords(records []map[string]string, sourcePeer string, tags storage.SourceTags) (int, string, map[string]int, []string, error) {
	count := 0
	skipped := 0
	normalized := sha256.New()
	markings := make(map[string]int)

	for _, record := range records {
		norad, ok := parseUint32(getValue(record, "NORAD_CAT_ID", "SAT_NO"))
		if !ok || norad == 0 {
			skipped++
			continue
		}
		rawEpoch := getValue(record, "EPOCH")
		if strings.TrimSpace(rawEpoch) == "" {
			skipped++
			continue
		}
		parsedEpoch, err := parseEpoch(rawEpoch)
		if err != nil {
			log.Warnf("Skipping UDL elset NORAD_CAT_ID=%d with malformed EPOCH %q: %v", norad, rawEpoch, err)
			skipped++
			continue
		}

		marking := strings.TrimSpace(getValue(record, "CLASSIFICATION_MARKING"))
		if marking != "" {
			markings[marking]++
		}

		builder := sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectName(valueOr(getValue(record, "OBJECT_NAME", "SATNAME", "NAME"), fmt.Sprintf("SAT-%d", norad))).
			WithObjectID(valueOr(getValue(record, "OBJECT_ID", "ID_ON_ORBIT", "INTLDES", "INTERNATIONAL_DESIGNATOR"), fmt.Sprintf("NORAD-%d", norad))).
			WithCreationDate(deterministicOMMCreationDate(record, parsedEpoch)).
			WithEpoch(parsedEpoch.UTC().Format(time.RFC3339)).
			WithClassificationType(marking)

		meanMotion, hasMeanMotion := parseFloat(getValue(record, "MEAN_MOTION"))
		if !hasMeanMotion {
			if semiMajorAxis, ok := parseFloat(getValue(record, "SEMI_MAJOR_AXIS")); ok {
				meanMotion, hasMeanMotion = meanMotionFromSemiMajorAxis(semiMajorAxis)
			}
		}
		if hasMeanMotion {
			builder = builder.WithMeanMotion(meanMotion)
		}
		if v, ok := parseFloat(getValue(record, "ECCENTRICITY")); ok {
			builder = builder.WithEccentricity(v)
		}
		if v, ok := parseFloat(getValue(record, "INCLINATION")); ok {
			builder = builder.WithInclination(v)
		}
		if v, ok := parseFloat(getValue(record, "RA_OF_ASC_NODE", "RAAN")); ok {
			builder = builder.WithRaOfAscNode(v)
		}
		if v, ok := parseFloat(getValue(record, "ARG_OF_PERICENTER", "ARG_OF_PERIGEE")); ok {
			builder = builder.WithArgOfPericenter(v)
		}
		if v, ok := parseFloat(getValue(record, "MEAN_ANOMALY")); ok {
			builder = builder.WithMeanAnomaly(v)
		}

		ommBytes := builder.Build()
		if _, err := r.storeIngestRecord("OMM.fbs", ommBytes, sourcePeer, tags); err != nil {
			return count, "", markings, nil, err
		}
		writeNormalizedHashRecord(normalized, "OMM.fbs", ommBytes)
		count++
	}

	var warnings []string
	if skipped > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d malformed UDL elset record(s)", skipped))
	}
	if count == 0 {
		return 0, "", markings, warnings, fmt.Errorf("no OMM rows parsed from UDL elset payload")
	}
	return count, hex.EncodeToString(normalized.Sum(nil)), markings, warnings, nil
}

// ingestUDLSGIRecords maps UDL Solar/Geomagnetic Index records to SPW
// FlatBuffers via the shared buildSPW path used by CelesTrak space weather.
// Field mapping: SGI_DATE -> DATE, F10 -> F107_OBS, F10B -> F107_OBS_CENTER81,
// AP -> AP_AVG (rounded), KP -> KP_SUM (stored in tenths).
func (r *Runner) ingestUDLSGIRecords(records []map[string]string, sourcePeer string, tags storage.SourceTags) (int, string, map[string]int, []string, error) {
	count := 0
	skipped := 0
	normalized := sha256.New()
	markings := make(map[string]int)

	for _, record := range records {
		rawDate := getValue(record, "SGI_DATE", "DATE")
		if strings.TrimSpace(rawDate) == "" {
			skipped++
			continue
		}
		parsedDate, err := parseEpoch(rawDate)
		if err != nil {
			log.Warnf("Skipping UDL sgi record with malformed SGI_DATE %q: %v", rawDate, err)
			skipped++
			continue
		}
		spwDate := parsedDate.UTC().Format("2006-01-02")

		if marking := strings.TrimSpace(getValue(record, "CLASSIFICATION_MARKING")); marking != "" {
			markings[marking]++
		}

		row := map[string]string{
			"F107_OBS":          getValue(record, "F10", "F107_OBS"),
			"F107_OBS_CENTER81": getValue(record, "F10B", "F107_OBS_CENTER81"),
			"AP_AVG":            roundedIntString(getValue(record, "AP", "AP_AVG")),
			"KP_SUM":            getValue(record, "KP", "KP_SUM"),
			"ISN":               roundedIntString(getValue(record, "SSN", "ISN")),
		}

		spwBytes := buildSPW(row, spwDate)
		if _, err := r.storeIngestRecord("SPW.fbs", spwBytes, sourcePeer, tags); err != nil {
			return count, "", markings, nil, err
		}
		writeNormalizedHashRecord(normalized, "SPW.fbs", spwBytes)
		count++
	}

	var warnings []string
	if skipped > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d malformed UDL sgi record(s)", skipped))
	}
	if count == 0 {
		return 0, "", markings, warnings, fmt.Errorf("no SPW rows parsed from UDL sgi payload")
	}
	return count, hex.EncodeToString(normalized.Sum(nil)), markings, warnings, nil
}

// meanMotionFromSemiMajorAxis derives mean motion (rev/day) from a semi-major
// axis in kilometers when the UDL elset omits MEAN_MOTION.
func meanMotionFromSemiMajorAxis(semiMajorAxisKm float64) (float64, bool) {
	if semiMajorAxisKm <= 0 {
		return 0, false
	}
	const muEarth = 398600.4418 // km^3/s^2
	periodSeconds := 2 * math.Pi * math.Sqrt(semiMajorAxisKm*semiMajorAxisKm*semiMajorAxisKm/muEarth)
	if periodSeconds <= 0 {
		return 0, false
	}
	return 86400.0 / periodSeconds, true
}

func roundedIntString(raw string) string {
	if f, ok := parseFloat(raw); ok {
		return strconv.Itoa(int(math.Round(f)))
	}
	return ""
}
