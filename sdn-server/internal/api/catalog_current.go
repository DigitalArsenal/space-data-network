package api

// TODAY'S CURRENT CATALOG over one GET (owner 2026-10-25): every object the
// node holds, once per mean-element theory. One record type per theory lane
// (program design row 9): SGP4 sets are the node's stored $OMM records, SGP4XP
// and AP sets its stored $OCM element-set records (MEAN_ELEMENTS). An SGP4 set
// is never served as $OCM and an SGP4XP or AP set never as $OMM.
//
//	GET /api/v1/data/catalog/current[?format=flatbuffer|json][&theory=SGP4|SGP4XP|AP|all]
//
//	theory   SGP4: the $OMM lane (MEAN_ELEMENT_THEORY SGP4). SGP4XP or AP: the
//	         $OCM lane (MEAN_ELEMENTS.MEAN_ELEMENT_THEORY). all (default): both
//	         lanes. Any other value is refused.
//	format   flatbuffer (default): one size-prefixed stream, byte for byte the
//	         stored records, each under its own file identifier ($OMM frames,
//	         then $OCM frames). json: the FlatBuffers JSON form of the same
//	         records (the IDL's field names, enums by name): one array for one
//	         lane; for all, {"$OMM": [...], "$OCM": [...]}.
//
// There is no history: the answer is the record nearest now for each object
// and theory ($OMM: NORAD_CAT_ID and EPOCH; $OCM: Metadata.NORAD_CAT_ID and
// MEAN_ELEMENTS.EPOCH). Nothing else is accepted, so the cache key space is two
// formats times four theory values.
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
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"

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

// catalogLanes is the record type of each served theory, in serving order.
var catalogLanes = []struct{ theory, schema string }{
	{"SGP4", "OMM.fbs"},
	{"SGP4XP", "OCM.fbs"},
	{"AP", "OCM.fbs"},
}

// catalogTheoryError is the refusal of any other theory.
const catalogTheoryError = "unsupported theory; use SGP4, SGP4XP, AP or all."

// catalogSet is one served record, its lane's schema and its theory.
type catalogSet struct {
	record *storage.Record
	schema string
	theory string
}

type catalogSelection struct {
	at        time.Time
	digest    string
	sets      []catalogSet
	published time.Time
	renders   map[string]*catalogRender
}

type catalogRender struct {
	body   []byte
	etag   string
	schema string
}

type catalogCurrentState struct {
	mu   sync.Mutex
	last *catalogSelection
	now  func() time.Time
}

// catalogLaneSchema is the record type a theory is served as; ok is false for
// a theory the catalog does not serve.
func catalogLaneSchema(theory string) (string, bool) {
	for _, l := range catalogLanes {
		if l.theory == theory {
			return l.schema, true
		}
	}
	return "", false
}

func (h *DataQueryHandler) catalogNow() time.Time {
	if h.catalog.now != nil {
		return h.catalog.now()
	}
	return time.Now().UTC()
}

type catalogLane struct{ provider, source string }

// catalogSourceLanes is every provider and source lane holding records of a
// schema, less the named-only lanes.
func (h *DataQueryHandler) catalogSourceLanes(summary *storage.DataSummary, schema string) []catalogLane {
	seen := map[catalogLane]bool{}
	var lanes []catalogLane
	if summary == nil {
		return nil
	}
	for _, src := range summary.Sources {
		l := catalogLane{src.ProviderID, src.SourceName}
		if src.SchemaName != schema || src.Count <= 0 || seen[l] {
			continue
		}
		seen[l] = true
		if config.AnyLaneMatches(h.namedOnly, "", schema, l.provider, l.source) {
			continue
		}
		lanes = append(lanes, l)
	}
	return lanes
}

// catalogRecords selects today's current sets of both lanes: per object and
// theory the record nearest now. The $OMM lane reads, per source lane, the
// record nearest now for each object plus every record of today's UTC day (a
// lane that holds several theories of one object keeps each); the $OCM lane
// reads the store's nearest element set per (NORAD_CAT_ID, theory).
func (h *DataQueryHandler) catalogRecords(now time.Time) ([]catalogSet, time.Time, error) {
	summary, err := h.store.DataSummary()
	if err != nil {
		return nil, time.Time{}, err
	}
	type key struct {
		norad  uint32
		theory string
	}
	type pick struct {
		set   catalogSet
		delta int64
	}
	best := map[string]map[key]pick{"OMM.fbs": {}, "OCM.fbs": {}}
	consider := func(schema string, norad uint32, theory string, epoch time.Time, r *storage.Record) {
		if lane, ok := catalogLaneSchema(theory); !ok || lane != schema {
			return
		}
		k := key{norad, theory}
		delta := epoch.Unix() - now.Unix()
		if delta < 0 {
			delta = -delta
		}
		if prev, ok := best[schema][k]; !ok || delta < prev.delta || (delta == prev.delta && r.CID < prev.set.record.CID) {
			best[schema][k] = pick{catalogSet{r, schema, theory}, delta}
		}
	}
	for _, l := range h.catalogSourceLanes(summary, "OMM.fbs") {
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
				omm, err := decodeOMM(m.Record.Data)
				if err != nil {
					return nil, time.Time{}, err
				}
				consider("OMM.fbs", omm.NORAD_CAT_ID(), omm.MEAN_ELEMENT_THEORY().String(), m.MatchedEpoch, m.Record)
			}
		}
	}
	for _, l := range h.catalogSourceLanes(summary, "OCM.fbs") {
		sets, err := h.store.QueryOCMElementSetsNearest(l.provider, l.source, now)
		if err != nil {
			return nil, time.Time{}, err
		}
		for _, set := range sets {
			consider("OCM.fbs", set.NoradCatID, set.Theory, set.Epoch, set.Record)
		}
	}
	var sets []catalogSet
	var published time.Time
	for _, schema := range []string{"OMM.fbs", "OCM.fbs"} {
		keys := make([]key, 0, len(best[schema]))
		for k := range best[schema] {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].norad != keys[j].norad {
				return keys[i].norad < keys[j].norad
			}
			return keys[i].theory < keys[j].theory
		})
		for _, k := range keys {
			set := best[schema][k].set
			sets = append(sets, set)
			if set.record.Timestamp.After(published) {
				published = set.record.Timestamp
			}
		}
	}
	return sets, published, nil
}

func (h *DataQueryHandler) catalogSelect() (*catalogSelection, time.Time, error) {
	now := h.catalogNow()
	h.catalog.mu.Lock()
	defer h.catalog.mu.Unlock()
	if s := h.catalog.last; s != nil && now.Sub(s.at) < catalogSelectionTTL && !now.Before(s.at) {
		return s, s.published, nil
	}
	sets, published, err := h.catalogRecords(now)
	if err != nil {
		return nil, time.Time{}, err
	}
	sum := sha256.New()
	for _, set := range sets {
		fmt.Fprintf(sum, "%s\x00", set.record.CID)
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	// The same records as the last selection keep its renders: a refresh that
	// finds nothing new costs the queries, not a re-encoding of the catalog.
	if prev := h.catalog.last; prev != nil && prev.digest == digest {
		prev.at, prev.published = now, published
		return prev, published, nil
	}
	h.catalog.last = &catalogSelection{at: now, digest: digest, sets: sets, published: published, renders: map[string]*catalogRender{}}
	return h.catalog.last, published, nil
}

// catalogTheoryRecords is the selection's records of one lane schema and a
// theory (all: every theory of that schema).
func catalogTheoryRecords(sets []catalogSet, schema, theory string) []*storage.Record {
	var out []*storage.Record
	for _, set := range sets {
		if set.schema == schema && (theory == "all" || set.theory == theory) {
			out = append(out, set.record)
		}
	}
	return out
}

var (
	catalogOCMBindingOnce sync.Once
	catalogOCMBinding     *bulkBinding
	catalogOCMBindingErr  error
)

// catalogJSONBinding is the generated-binding JSON form of a lane's schema.
func catalogJSONBinding(schema string) (*bulkBinding, error) {
	if schema != "OCM.fbs" {
		return bulkBindingForSchema(schema)
	}
	catalogOCMBindingOnce.Do(func() {
		binding := &bulkBinding{
			schemaName: schema,
			rootType:   reflect.TypeOf((*OCM.OCM)(nil)),
			decode:     func(data []byte) (interface{}, error) { return storage.DecodeOCM(data) },
		}
		binding.fields, catalogOCMBindingErr = generatedBindingFields(binding.rootType)
		catalogOCMBinding = binding
	})
	return catalogOCMBinding, catalogOCMBindingErr
}

// catalogWriteJSONArray is writeBulkJSONArray with a field whose value is not
// a finite number (the IDL's "nan when not stated" defaults of $OCM) written as
// null, which JSON can carry.
func catalogWriteJSONArray(writer io.Writer, binding *bulkBinding, records []*storage.Record) error {
	if _, err := io.WriteString(writer, "["); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if index > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		root, err := binding.decode(record.Data)
		if err != nil {
			return err
		}
		object, err := bindingObject(root, binding.fields)
		if err != nil {
			return err
		}
		if err := encoder.Encode(catalogFiniteJSON(object)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]")
	return err
}

// catalogFiniteJSON replaces every NaN or infinite number in a binding object
// with nil.
func catalogFiniteJSON(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		for key, child := range v {
			v[key] = catalogFiniteJSON(child)
		}
		return v
	case []interface{}:
		for i, child := range v {
			v[i] = catalogFiniteJSON(child)
		}
		return v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil
		}
	}
	return value
}

// catalogRenderBody writes a theory's answer: the flatbuffer stream, or the
// JSON form (one array, or one array per file identifier for all).
func (h *DataQueryHandler) catalogRenderBody(body *bytes.Buffer, format bulkExportFormat, theory string, lanes []string, sets []catalogSet) ([]*storage.Record, error) {
	var all []*storage.Record
	if format == bulkFormatJSON && len(lanes) > 1 {
		body.WriteString("{")
	}
	for i, schema := range lanes {
		records := catalogTheoryRecords(sets, schema, theory)
		all = append(all, records...)
		if format != bulkFormatJSON {
			if err := h.store.WriteRawRecordFrames(body, records); err != nil {
				return nil, err
			}
			continue
		}
		if len(lanes) > 1 {
			if i > 0 {
				body.WriteString(",")
			}
			fmt.Fprintf(body, "%q:", "$"+strings.TrimSuffix(schema, ".fbs"))
		}
		binding, err := catalogJSONBinding(schema)
		if err != nil {
			return nil, err
		}
		if err := catalogWriteJSONArray(body, binding, records); err != nil {
			return nil, err
		}
	}
	if format == bulkFormatJSON && len(lanes) > 1 {
		body.WriteString("}")
	}
	return all, nil
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
	lanes := []string{"OMM.fbs", "OCM.fbs"}
	if raw := strings.ToUpper(strings.TrimSpace(values.Get("theory"))); raw != "" && raw != "ALL" {
		schema, ok := catalogLaneSchema(raw)
		if !ok {
			WriteErrorFrame(w, http.StatusBadRequest, "bad_request", catalogTheoryError, 0)
			return
		}
		theory, lanes = raw, []string{schema}
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
		var body bytes.Buffer
		records, err := h.catalogRenderBody(&body, format, theory, lanes, selection.sets)
		if err != nil {
			WriteErrorFrame(w, http.StatusInternalServerError, "internal", err.Error(), 0)
			return
		}
		sum := sha256.New()
		fmt.Fprintf(sum, "%s\x00%s", format, theory)
		for _, rec := range records {
			fmt.Fprintf(sum, "\x00%s", rec.CID)
		}
		render = &catalogRender{body: body.Bytes(), etag: `"` + hex.EncodeToString(sum.Sum(nil)) + `"`, schema: strings.Join(lanes, ",")}
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
	hdr.Set("X-SDN-Schema", render.schema)
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
