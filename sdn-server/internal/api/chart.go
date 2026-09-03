package api

// Chart aggregation lane — GET /api/v1/data/chart.
//
// Owner rulings 2026-09-03: charts show ALL records in a time period and
// must load fast. Paging projected JSON rows to the browser cannot do that
// (every filtered page is a scan), so the aggregation happens here: ONE
// pass over the durable records in range, only the columns the chart needs,
// derived orbital quantities computed in Go, and a compact aggregate on the
// wire (bins, category counts, series or scatter points). Results are cached
// per query so switching presets is instant. Readers never wait on
// maintenance: the underlying scan holds the store lock per chunk only.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	chartScanChunk       = 4000
	chartScanTimeBudget  = 25 * time.Second
	chartScanRecordCap   = 3_000_000
	chartScatterPointCap = 400_000
	chartSeriesPointCap  = 400_000
	chartCacheTTL        = 120 * time.Second
	chartCacheEntries    = 48
	chartMaxBins         = 200
	chartMaxCategories   = 100
	chartMaxSplits       = 12
)

type chartRequest struct {
	Schema  string
	Source  string
	Kind    string // scatter | histogram | category | series
	X       string
	Y       []string
	Split   string
	Bins    int
	Top     int
	Filters map[string]string
}

type chartBin struct {
	X0     float64          `json:"x0"`
	X1     float64          `json:"x1"`
	Counts map[string]int64 `json:"counts"`
}

type chartCategory struct {
	X      string           `json:"x"`
	Total  int64            `json:"total"`
	Counts map[string]int64 `json:"counts"`
}

type chartSeries struct {
	Name   string      `json:"name"`
	Points [][]float64 `json:"points"`
}

type chartResponse struct {
	Schema     string          `json:"schema"`
	Kind       string          `json:"kind"`
	X          string          `json:"x"`
	XIsTime    bool            `json:"x_is_time"`
	XUnit      string          `json:"x_unit,omitempty"`
	Y          []string        `json:"y,omitempty"`
	Split      string          `json:"split,omitempty"`
	Scanned    int64           `json:"scanned"`
	Matched    int64           `json:"matched"`
	Stored     int64           `json:"stored"`
	Partial    bool            `json:"partial"`
	Truncated  bool            `json:"truncated"`
	Bins       []chartBin      `json:"bins,omitempty"`
	Categories []chartCategory `json:"categories,omitempty"`
	Series     []chartSeries   `json:"series,omitempty"`
	ElapsedMs  int64           `json:"elapsed_ms"`
	CachedAt   string          `json:"cached_at,omitempty"`
}

type chartCacheEntry struct {
	key    string
	body   *chartResponse
	expiry time.Time
}

type chartCache struct {
	mu      sync.Mutex
	entries map[string]*chartCacheEntry
	order   []string
}

func (c *chartCache) get(key string) *chartResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiry) {
		return nil
	}
	return e.body
}

func (c *chartCache) put(key string, body *chartResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*chartCacheEntry{}
	}
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = &chartCacheEntry{key: key, body: body, expiry: time.Now().Add(chartCacheTTL)}
	for len(c.order) > chartCacheEntries {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

var chartResults chartCache

// Derived columns mirror the dashboard's presets so a histogram of, say,
// orbital period is computed here over every record, not on a sample.
const (
	muEarth     = 398600.4418 // km^3 s^-2
	earthRadius = 6378.137    // km
)

var chartDerivedNeeds = map[string][]string{
	"PERIOD_MIN":   {"MEAN_MOTION"},
	"APOGEE_KM":    {"MEAN_MOTION", "ECCENTRICITY"},
	"PERIGEE_KM":   {"MEAN_MOTION", "ECCENTRICITY"},
	"EPOCH_AGE_H":  {"EPOCH"},
	"LAUNCH_YEAR":  {"LAUNCH_DATE"},
	"DECAY_STATE":  {"DECAY_DATE"},
	"ORBIT_REGIME": {"MEAN_MOTION", "ECCENTRICITY"},
}

func chartSemiMajorAxis(meanMotion float64) (float64, bool) {
	if !(meanMotion > 0) {
		return 0, false
	}
	rad := meanMotion * 2 * math.Pi / 86400
	return math.Cbrt(muEarth / (rad * rad)), true
}

func chartEpochSeconds(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		switch {
		case n >= 631152000 && n <= 4102444800:
			return n, true
		case n >= 631152000000 && n <= 4102444800000:
			return n / 1000, true
		}
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return float64(ts.UTC().UnixNano()) / 1e9, true
		}
	}
	return 0, false
}

// chartValue resolves a raw or derived column to a text value for a record.
func chartValue(column string, values map[string]string, nowSec float64) (string, bool) {
	if v, ok := values[column]; ok {
		return v, true
	}
	f := func(c string) (float64, bool) {
		n, err := strconv.ParseFloat(strings.TrimSpace(values[c]), 64)
		return n, err == nil
	}
	switch column {
	case "PERIOD_MIN":
		if n, ok := f("MEAN_MOTION"); ok && n > 0 {
			return strconv.FormatFloat(1440/n, 'f', -1, 64), true
		}
	case "APOGEE_KM", "PERIGEE_KM":
		n, okN := f("MEAN_MOTION")
		e, okE := f("ECCENTRICITY")
		if okN && okE {
			if a, ok := chartSemiMajorAxis(n); ok {
				if column == "APOGEE_KM" {
					return strconv.FormatFloat(a*(1+e)-earthRadius, 'f', -1, 64), true
				}
				return strconv.FormatFloat(a*(1-e)-earthRadius, 'f', -1, 64), true
			}
		}
	case "EPOCH_AGE_H":
		if s, ok := chartEpochSeconds(values["EPOCH"]); ok {
			return strconv.FormatFloat((nowSec-s)/3600, 'f', -1, 64), true
		}
	case "LAUNCH_YEAR":
		raw := strings.TrimSpace(values["LAUNCH_DATE"])
		if s, ok := chartEpochSeconds(raw); ok {
			return strconv.Itoa(time.Unix(int64(s), 0).UTC().Year()), true
		}
		if len(raw) >= 4 {
			if _, err := strconv.Atoi(raw[:4]); err == nil {
				return raw[:4], true
			}
		}
	case "DECAY_STATE":
		if strings.TrimSpace(values["DECAY_DATE"]) != "" {
			return "Decayed", true
		}
		return "On orbit", true
	case "ORBIT_REGIME":
		n, okN := f("MEAN_MOTION")
		e, okE := f("ECCENTRICITY")
		if okN && okE {
			if a, ok := chartSemiMajorAxis(n); ok {
				per := a*(1-e) - earthRadius
				apo := a*(1+e) - earthRadius
				switch {
				case apo < 2000:
					return "LEO", true
				case math.Abs(n-1) < 0.02 && e < 0.02:
					return "GEO", true
				case e > 0.25:
					return "HEO", true
				case per > 2000 && apo < 35786+2000:
					return "MEO", true
				}
				return "Other", true
			}
		}
	}
	return "", false
}

func parseChartRequest(r *http.Request) (chartRequest, error) {
	v := r.URL.Query()
	req := chartRequest{
		Schema:  strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(v.Get("schema"), ".fbs"))),
		Source:  strings.TrimSpace(v.Get("source")),
		Kind:    strings.ToLower(strings.TrimSpace(v.Get("kind"))),
		X:       strings.TrimSpace(v.Get("x")),
		Split:   strings.TrimSpace(v.Get("split")),
		Bins:    parsePositiveIntParam(v.Get("bins"), 40),
		Top:     parsePositiveIntParam(v.Get("top"), 30),
		Filters: map[string]string{},
	}
	for _, y := range strings.Split(v.Get("y"), ",") {
		if y = strings.TrimSpace(y); y != "" {
			req.Y = append(req.Y, y)
		}
	}
	for key, vals := range v {
		if strings.HasPrefix(key, "f.") && len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
			req.Filters[strings.TrimPrefix(key, "f.")] = strings.TrimSpace(vals[0])
		}
	}
	if !tableSchemaCodePattern.MatchString(req.Schema) {
		return req, fmt.Errorf("schema is required (a standard code such as OMM)")
	}
	switch req.Kind {
	case "scatter", "histogram", "category", "series":
	default:
		return req, fmt.Errorf("kind must be scatter, histogram, category or series")
	}
	if req.X == "" {
		return req, fmt.Errorf("x is required")
	}
	if (req.Kind == "scatter" || req.Kind == "series") && len(req.Y) == 0 {
		return req, fmt.Errorf("y is required for %s charts", req.Kind)
	}
	if req.Bins > chartMaxBins {
		req.Bins = chartMaxBins
	}
	if req.Top > chartMaxCategories {
		req.Top = chartMaxCategories
	}
	return req, nil
}

func chartCacheKey(req chartRequest) string {
	keys := make([]string, 0, len(req.Filters))
	for k := range req.Filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s|%d|%d", req.Schema, req.Source, req.Kind, req.X, strings.Join(req.Y, ","), req.Split, req.Bins, req.Top)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%s", k, req.Filters[k])
	}
	return b.String()
}

// chartProjection lists the raw columns the scan must decode: the chart's
// own columns plus whatever its derived columns need, plus filter columns.
func chartProjection(req chartRequest) []string {
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		if c == "" || seen[c] {
			return
		}
		if needs, derived := chartDerivedNeeds[c]; derived {
			for _, n := range needs {
				if !seen[n] {
					seen[n] = true
					out = append(out, n)
				}
			}
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	add(req.X)
	for _, y := range req.Y {
		add(y)
	}
	add(req.Split)
	for c := range req.Filters {
		add(c)
	}
	return out
}

func (h *CoreAPIHandler) handleChart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	req, err := parseChartRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := chartCacheKey(req)
	if cached := chartResults.get(key); cached != nil {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chartScanTimeBudget+5*time.Second)
	defer cancel()
	res, err := h.aggregateChart(ctx, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !res.Partial {
		chartResults.put(key, res)
	}
	writeJSON(w, http.StatusOK, res)
}

type chartAccumulator struct {
	req        chartRequest
	xIsTime    bool
	xNumeric   []float64
	splitOf    []string
	yOf        [][]float64
	categories map[string]*chartCategory
	splits     map[string]bool
	matched    int64
	truncated  bool
}

func (h *CoreAPIHandler) aggregateChart(ctx context.Context, req chartRequest) (*chartResponse, error) {
	start := time.Now()
	sourceName, err := durableTableSourceName(req.Schema, req.Source)
	if err != nil {
		return nil, err
	}
	summary, err := h.store.DataSummary()
	if err != nil {
		return nil, err
	}
	stored := fullTableStoredCount(summary, req.Schema+".fbs", sourceName)
	projection := chartProjection(req)
	filters := req.Filters
	nowSec := float64(time.Now().UnixNano()) / 1e9
	deadline := start.Add(chartScanTimeBudget)

	acc := &chartAccumulator{req: req, categories: map[string]*chartCategory{}, splits: map[string]bool{}}
	var binding *bulkBinding
	var cursor storage.FullTablePageCursor
	var scanned int64
	partial := false

scan:
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if scanned >= chartScanRecordCap || !time.Now().Before(deadline) {
			partial = true
			break
		}
		page, err := h.store.FullTablePageWithCursor(storage.FullTablePageQuery{
			SchemaName:      req.Schema + ".fbs",
			SourceName:      sourceName,
			Limit:           chartScanChunk,
			Cursor:          cursor,
			IncludeSource:   false,
			KnownSourceName: sourceName,
		})
		if err != nil {
			return nil, err
		}
		if len(page.Records) == 0 {
			break
		}
		if binding == nil {
			binding, err = tableBindingForPage(req.Schema+".fbs", page.Records[0].Data)
			if err != nil {
				return nil, err
			}
		}
		for _, record := range page.Records {
			if !time.Now().Before(deadline) {
				partial = true
				break scan
			}
			scanned++
			projected, matches, err := projectFullTableRecord(req.Schema, record, binding, projection, filters, "", "")
			if err != nil {
				return nil, err
			}
			if !matches {
				continue
			}
			values := make(map[string]string, len(projection))
			for i, c := range projection {
				if i < len(projected.row) {
					values[c] = projected.row[i]
				}
			}
			acc.add(values, nowSec)
		}
		if len(page.NextCursor) == 0 {
			break
		}
		cursor = page.NextCursor
	}

	res := acc.finish()
	res.Schema = req.Schema
	res.Scanned = scanned
	res.Stored = stored
	res.Partial = partial
	res.ElapsedMs = time.Since(start).Milliseconds()
	res.CachedAt = time.Now().UTC().Format(time.RFC3339)
	return res, nil
}

func (a *chartAccumulator) add(values map[string]string, nowSec float64) {
	xRaw, ok := chartValue(a.req.X, values, nowSec)
	if !ok {
		return
	}
	split := ""
	if a.req.Split != "" {
		if s, ok := chartValue(a.req.Split, values, nowSec); ok {
			split = s
		}
	}
	if a.req.Kind == "category" {
		x := strings.TrimSpace(xRaw)
		if x == "" {
			return
		}
		cat := a.categories[x]
		if cat == nil {
			cat = &chartCategory{X: x, Counts: map[string]int64{}}
			a.categories[x] = cat
		}
		cat.Total++
		cat.Counts[chartSplitKey(split)]++
		a.splits[chartSplitKey(split)] = true
		a.matched++
		return
	}
	var x float64
	if s, ok := chartEpochSeconds(xRaw); ok && isEpochName(a.req.X) {
		x = s * 1000
		a.xIsTime = true
	} else {
		n, err := strconv.ParseFloat(strings.TrimSpace(xRaw), 64)
		if err != nil {
			return
		}
		x = n
	}
	if a.req.Kind == "histogram" {
		a.xNumeric = append(a.xNumeric, x)
		a.splitOf = append(a.splitOf, chartSplitKey(split))
		a.splits[chartSplitKey(split)] = true
		a.matched++
		return
	}
	ys := make([]float64, len(a.req.Y))
	for i, y := range a.req.Y {
		raw, ok := chartValue(y, values, nowSec)
		if !ok {
			ys[i] = math.NaN()
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			ys[i] = math.NaN()
			continue
		}
		ys[i] = n
	}
	cap := chartSeriesPointCap
	if a.req.Kind == "scatter" {
		cap = chartScatterPointCap
	}
	if len(a.xNumeric) >= cap {
		a.truncated = true
		return
	}
	a.xNumeric = append(a.xNumeric, x)
	a.splitOf = append(a.splitOf, chartSplitKey(split))
	a.yOf = append(a.yOf, ys)
	a.splits[chartSplitKey(split)] = true
	a.matched++
}

func chartSplitKey(split string) string {
	if strings.TrimSpace(split) == "" {
		return "records"
	}
	return split
}

// isEpochName mirrors the dashboard's column-name convention for time.
func isEpochName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || strings.HasPrefix(n, "_") {
		return n == "_timestamp"
	}
	return n == "epoch" || strings.Contains(n, "_epoch") || strings.HasPrefix(n, "epoch_") ||
		strings.Contains(n, "date") || strings.Contains(n, "time") || strings.HasSuffix(n, "_at") || strings.HasSuffix(n, "_utc")
}

func (a *chartAccumulator) topSplits() []string {
	names := make([]string, 0, len(a.splits))
	for s := range a.splits {
		names = append(names, s)
	}
	sort.Strings(names)
	if len(names) > chartMaxSplits {
		names = names[:chartMaxSplits]
	}
	return names
}

func (a *chartAccumulator) finish() *chartResponse {
	res := &chartResponse{Kind: a.req.Kind, X: a.req.X, XIsTime: a.xIsTime, Y: a.req.Y, Split: a.req.Split, Matched: a.matched, Truncated: a.truncated}
	if a.xIsTime {
		res.XUnit = "millis"
	}
	switch a.req.Kind {
	case "category":
		cats := make([]*chartCategory, 0, len(a.categories))
		for _, c := range a.categories {
			cats = append(cats, c)
		}
		sort.Slice(cats, func(i, j int) bool {
			ni, ei := strconv.ParseFloat(cats[i].X, 64)
			nj, ej := strconv.ParseFloat(cats[j].X, 64)
			if ei == nil && ej == nil {
				return ni < nj
			}
			if cats[i].Total != cats[j].Total {
				return cats[i].Total > cats[j].Total
			}
			return cats[i].X < cats[j].X
		})
		if len(cats) > a.req.Top {
			cats = cats[:a.req.Top]
		}
		res.Categories = make([]chartCategory, len(cats))
		for i, c := range cats {
			res.Categories[i] = *c
		}
	case "histogram":
		if len(a.xNumeric) == 0 {
			return res
		}
		lo, hi := a.xNumeric[0], a.xNumeric[0]
		for _, x := range a.xNumeric {
			if x < lo {
				lo = x
			}
			if x > hi {
				hi = x
			}
		}
		bins := a.req.Bins
		if bins < 1 {
			bins = 1
		}
		if hi == lo {
			hi = lo + 1
		}
		width := (hi - lo) / float64(bins)
		out := make([]chartBin, bins)
		for i := range out {
			out[i] = chartBin{X0: lo + float64(i)*width, X1: lo + float64(i+1)*width, Counts: map[string]int64{}}
		}
		for i, x := range a.xNumeric {
			b := int((x - lo) / width)
			if b >= bins {
				b = bins - 1
			}
			if b < 0 {
				b = 0
			}
			out[b].Counts[a.splitOf[i]]++
		}
		res.Bins = out
	default: // scatter, series
		bySplit := map[string][][]float64{}
		for i, x := range a.xNumeric {
			for yi := range a.req.Y {
				y := a.yOf[i][yi]
				if math.IsNaN(y) {
					continue
				}
				name := a.req.Y[yi]
				if a.splitOf[i] != "records" {
					name = name + " · " + a.splitOf[i]
				}
				bySplit[name] = append(bySplit[name], []float64{x, y})
			}
		}
		names := make([]string, 0, len(bySplit))
		for n := range bySplit {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > chartMaxSplits {
			names = names[:chartMaxSplits]
		}
		for _, n := range names {
			pts := bySplit[n]
			if a.req.Kind == "series" {
				sort.Slice(pts, func(i, j int) bool { return pts[i][0] < pts[j][0] })
			}
			res.Series = append(res.Series, chartSeries{Name: n, Points: pts})
		}
	}
	return res
}
