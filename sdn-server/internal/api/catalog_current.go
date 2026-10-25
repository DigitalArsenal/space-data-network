package api

// TODAY'S CURRENT CATALOG over one GET (owner 2026-10-25): every object the
// node holds, once per mean-element theory, as the node's own $OMM records.
//
//	GET /api/v1/data/catalog/current[?format=flatbuffer|json][&theory=SGP4|SGP4XP|all]
//
//	format   flatbuffer (default): the size-prefixed $OMM stream, byte for byte
//	         the stored records. json: the FlatBuffers JSON form of the same
//	         records (the IDL's field names, enums by name), one array.
//	theory   which MEAN_ELEMENT_THEORY: SGP4, SGP4XP or all (default). A stored
//	         record of another theory (SDNAP, removed 2026-10-25) is not served.
//
// There is no history: the answer is the records nearest now for each object
// and theory. Nothing else is accepted, so the cache key space is two formats
// times three theory values.
//
// CACHING. The entity tag is the SHA-256 of the format, the theory and the
// record CIDs (a CID is the hash of the record's bytes), so it changes exactly
// when the answer does. Last-Modified is the newest publication time among the
// records. Cache-Control is public, with s-maxage until the next publication
// (the publication interval after the newest one, never under a minute), so a
// shared cache serves the day's catalog without the node and revalidates with
// If-None-Match afterwards. Cloudflare caches a path like this one only under
// a Cache Rule (deployment/cloudflare-catalog-cache-rule.md).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	// CatalogCurrentPath is the route.
	CatalogCurrentPath = "/api/v1/data/catalog/current"

	// catalogPublicationInterval is how often the catalog node publishes
	// (its flows' hourly timer).
	catalogPublicationInterval = time.Hour
	// catalogMinSharedTTL keeps a shared cache from revalidating faster than
	// the node can recompute.
	catalogMinSharedTTL = time.Minute
	// catalogSelectionTTL bounds how stale the in-process selection is.
	catalogSelectionTTL = 30 * time.Second
	catalogQueryLimit   = 250000
)

var catalogTheories = []string{"SGP4", "SGP4XP"}

type catalogSelection struct {
	at        time.Time
	digest    string
	records   []*storage.Record
	published time.Time
	renders   map[string]*catalogRender
}

type catalogRender struct {
	body []byte
	etag string
}

type catalogCurrentState struct {
	mu   sync.Mutex
	last *catalogSelection
	now  func() time.Time
}

// catalogServes reports whether the catalog serves a mean-element theory.
func catalogServes(theory string) bool {
	for _, t := range catalogTheories {
		if t == theory {
			return true
		}
	}
	return false
}

func (h *DataQueryHandler) catalogNow() time.Time {
	if h.catalog.now != nil {
		return h.catalog.now()
	}
	return time.Now().UTC()
}

// catalogRecords selects today's current records of every theory: per lane the
// record nearest now for each object, plus every record of today's UTC day (a
// lane that holds several theories of one object keeps each), reduced to the
// record nearest now for each (object, theory).
func (h *DataQueryHandler) catalogRecords(now time.Time) ([]*storage.Record, time.Time, error) {
	summary, err := h.store.DataSummary()
	if err != nil {
		return nil, time.Time{}, err
	}
	type lane struct{ provider, source string }
	seen := map[lane]bool{}
	var lanes []lane
	if summary != nil {
		for _, src := range summary.Sources {
			l := lane{src.ProviderID, src.SourceName}
			if src.SchemaName != "OMM.fbs" || src.Count <= 0 || seen[l] {
				continue
			}
			seen[l] = true
			if config.AnyLaneMatches(h.namedOnly, "", "OMM.fbs", l.provider, l.source) {
				continue
			}
			lanes = append(lanes, l)
		}
	}
	type key struct {
		norad  uint32
		theory string
	}
	type pick struct {
		record *storage.Record
		delta  int64
	}
	best := map[key]pick{}
	consider := func(m storage.EpochRecordMatch) error {
		omm, err := decodeOMM(m.Record.Data)
		if err != nil {
			return err
		}
		theory := omm.MEAN_ELEMENT_THEORY().String()
		if !catalogServes(theory) {
			return nil
		}
		k := key{omm.NORAD_CAT_ID(), theory}
		delta := m.MatchedEpoch.Unix() - now.Unix()
		if delta < 0 {
			delta = -delta
		}
		if prev, ok := best[k]; !ok || delta < prev.delta || (delta == prev.delta && m.Record.CID < prev.record.CID) {
			best[k] = pick{m.Record, delta}
		}
		return nil
	}
	for _, l := range lanes {
		for _, q := range []storage.EpochRecordQuery{
			{Profile: storage.EpochProfileNearest, At: now},
			{Profile: storage.EpochProfileDay, Day: now.Format("2006-01-02")},
		} {
			q.SchemaName, q.ProviderID, q.SourceName, q.Limit = "OMM.fbs", l.provider, l.source, catalogQueryLimit
			matches, err := h.store.QueryEpochRecords(q)
			if err != nil {
				return nil, time.Time{}, err
			}
			for _, m := range matches {
				if err := consider(m); err != nil {
					return nil, time.Time{}, err
				}
			}
		}
	}
	keys := make([]key, 0, len(best))
	for k := range best {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].norad != keys[j].norad {
			return keys[i].norad < keys[j].norad
		}
		return keys[i].theory < keys[j].theory
	})
	records := make([]*storage.Record, 0, len(keys))
	var published time.Time
	for _, k := range keys {
		r := best[k].record
		records = append(records, r)
		if r.Timestamp.After(published) {
			published = r.Timestamp
		}
	}
	return records, published, nil
}

func (h *DataQueryHandler) catalogSelect() (*catalogSelection, time.Time, error) {
	now := h.catalogNow()
	h.catalog.mu.Lock()
	defer h.catalog.mu.Unlock()
	if s := h.catalog.last; s != nil && now.Sub(s.at) < catalogSelectionTTL && !now.Before(s.at) {
		return s, s.published, nil
	}
	records, published, err := h.catalogRecords(now)
	if err != nil {
		return nil, time.Time{}, err
	}
	sum := sha256.New()
	for _, rec := range records {
		fmt.Fprintf(sum, "%s\x00", rec.CID)
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	// The same records as the last selection keep its renders: a refresh that
	// finds nothing new costs the queries, not a re-encoding of the catalog.
	if prev := h.catalog.last; prev != nil && prev.digest == digest {
		prev.at, prev.published = now, published
		return prev, published, nil
	}
	h.catalog.last = &catalogSelection{at: now, digest: digest, records: records, published: published, renders: map[string]*catalogRender{}}
	return h.catalog.last, published, nil
}

func catalogTheoryFilter(records []*storage.Record, theory string) ([]*storage.Record, error) {
	if theory == "all" {
		return records, nil
	}
	out := make([]*storage.Record, 0, len(records))
	for _, r := range records {
		omm, err := decodeOMM(r.Data)
		if err != nil {
			return nil, err
		}
		if omm.MEAN_ELEMENT_THEORY().String() == theory {
			out = append(out, r)
		}
	}
	return out, nil
}

// catalogCacheControl is public, shared for the time left before the next
// publication.
func catalogCacheControl(now, published time.Time) string {
	shared := catalogMinSharedTTL
	if !published.IsZero() {
		if left := published.Add(catalogPublicationInterval).Sub(now); left > shared {
			shared = left
		}
	}
	if shared > catalogPublicationInterval {
		shared = catalogPublicationInterval
	}
	return fmt.Sprintf("public, max-age=%d, s-maxage=%d, stale-while-revalidate=%d",
		int(catalogMinSharedTTL/time.Second), int(shared/time.Second), int(catalogPublicationInterval/time.Second))
}

// etagMatches reports whether an If-None-Match header names the strong tag.
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == etag || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

func (h *DataQueryHandler) handleCatalogCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "the current catalog takes GET", 0)
		return
	}
	values := r.URL.Query()
	for name := range values {
		if name != "format" && name != "theory" {
			WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "the current catalog takes format and theory only; it has no history.", 0)
			return
		}
	}
	format := bulkFormatFlatBuffer
	switch strings.ToLower(strings.TrimSpace(values.Get("format"))) {
	case "", "flatbuffer", "flatbuffers", "fb":
	case "json":
		format = bulkFormatJSON
	default:
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "unsupported format; use flatbuffer or json.", 0)
		return
	}
	theory := "all"
	if raw := strings.ToUpper(strings.TrimSpace(values.Get("theory"))); raw != "" && raw != "ALL" {
		theory = raw
		if !catalogServes(theory) {
			WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "unsupported theory; use SGP4, SGP4XP or all.", 0)
			return
		}
	}
	if !h.ensureStore(w) {
		return
	}
	selection, published, err := h.catalogSelect()
	if err != nil {
		writeDataReadError(w, err)
		return
	}
	cacheKey := string(format) + "/" + theory
	h.catalog.mu.Lock()
	render := selection.renders[cacheKey]
	h.catalog.mu.Unlock()
	if render == nil {
		records, err := catalogTheoryFilter(selection.records, theory)
		if err != nil {
			WriteErrorFrame(w, http.StatusInternalServerError, "internal", err.Error(), 0)
			return
		}
		var body bytes.Buffer
		if format == bulkFormatJSON {
			binding, err := bulkBindingForSchema("OMM.fbs")
			if err == nil {
				err = writeBulkJSONArray(&body, binding, records)
			}
			if err != nil {
				WriteErrorFrame(w, http.StatusInternalServerError, "internal", err.Error(), 0)
				return
			}
		} else if err := h.store.WriteRawRecordFrames(&body, records); err != nil {
			WriteErrorFrame(w, http.StatusInternalServerError, "internal", err.Error(), 0)
			return
		}
		sum := sha256.New()
		fmt.Fprintf(sum, "%s\x00%s", format, theory)
		for _, rec := range records {
			fmt.Fprintf(sum, "\x00%s", rec.CID)
		}
		render = &catalogRender{body: body.Bytes(), etag: `"` + hex.EncodeToString(sum.Sum(nil)) + `"`}
		h.catalog.mu.Lock()
		selection.renders[cacheKey] = render
		h.catalog.mu.Unlock()
	}

	hdr := w.Header()
	hdr.Set("ETag", render.etag)
	hdr.Set("Cache-Control", catalogCacheControl(h.catalogNow(), published))
	if !published.IsZero() {
		hdr.Set("Last-Modified", published.UTC().Format(http.TimeFormat))
	}
	hdr.Set("X-SDN-Schema", "OMM.fbs")
	hdr.Set("X-Content-Type-Options", "nosniff")
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, render.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if format == bulkFormatJSON {
		hdr.Set("Content-Type", ContentTypeJSON)
	} else {
		hdr.Set("Content-Type", ContentTypeFlatBufferStream)
		hdr.Set("X-SDN-Stream-Format", "flatsql-size-prefixed-le-u32")
	}
	hdr.Set("Content-Length", strconv.Itoa(len(render.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(render.body)
	}
}
