package api

// GET /api/v1/data/table — the dashboard's server-side table lane: one page of
// one standard's complete durable record set. The FlatSQL engine relation is a
// bounded resident window and is deliberately not the pagination source.
//
// GET /api/v1/data/table/sources — the distinct `_source` lanes one standard's
// table holds, with counts. Powers the "by source" selector.
//
// Source selection and metadata ordering execute against the durable catalog.
// Projected-column filters, global search, and projected-column ordering scan
// the selected durable set in bounded chunks, decoding each FlatBuffer through
// the same generated bindings as the JSON download lane before paging the
// result. Admin-gated like the sandbox lane: this is the operator's explorer;
// the anonymous read surface stays the routed bulk/index endpoints.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	tableDefaultLimit = 100
	tableMaxLimit     = 1000
	// tableSearchColumnCap bounds how many columns a global search term ORs
	// across, so a wide table cannot turn one keystroke into a 60-branch scan.
	tableSearchColumnCap = 24
	tableQueryTimeout    = 20 * time.Second

	// Filtered/sorted requests walk the durable catalog in bounded store-lock
	// windows. The RECORD ceiling is the bound that shapes a request; the
	// wall-clock ceiling is a safety net only. Measured 2026-09-02 on the dev
	// store: a 32,324-record MPE scan takes ~1.7 s alone, but the engine is
	// single-threaded, so one concurrent reader (the grid's own block fetch,
	// the 5 s stats lane) doubled that and a 2 s ceiling cut the scan at
	// 20,000 records — an honest "partial" answer for a set that the record
	// budget says must be scanned whole.
	tableScanChunkSize    = 2000
	tableScanRecordBudget = 250_000
	tableScanTimeBudget   = 10 * time.Second
)

var (
	tableSchemaCodePattern  = regexp.MustCompile(`^[A-Za-z0-9]{2,8}$`)
	tableIdentPattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	tableCursorTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// tableRequest is one parsed, not-yet-validated table page request.
type tableRequest struct {
	Schema  string
	Cols    []string
	Page    int
	Limit   int
	Cursor  string
	Sort    string
	Dir     string
	Q       string
	Source  string
	Filters map[string]string
}

type tableResponse struct {
	Schema     string     `json:"schema"`
	Columns    []string   `json:"columns"`
	Rows       [][]string `json:"rows"`
	Total      int64      `json:"total"`
	Page       int        `json:"page"`
	Limit      int        `json:"limit"`
	Truncated  bool       `json:"truncated"`
	Source     string     `json:"source,omitempty"`
	Partial    bool       `json:"partial,omitempty"`
	Scanned    int64      `json:"scanned,omitempty"`
	Stored     int64      `json:"stored,omitempty"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type tableScanBudget struct {
	MaxRecords int
	MaxTime    time.Duration
}

type tableScanBudgetContextKey struct{}

func tableScanBudgetForContext(ctx context.Context) tableScanBudget {
	budget := tableScanBudget{MaxRecords: tableScanRecordBudget, MaxTime: tableScanTimeBudget}
	if injected, ok := ctx.Value(tableScanBudgetContextKey{}).(tableScanBudget); ok {
		if injected.MaxRecords > 0 {
			budget.MaxRecords = injected.MaxRecords
		}
		if injected.MaxTime > 0 {
			budget.MaxTime = injected.MaxTime
		}
	}
	return budget
}

// withTableScanBudget is an internal test seam. Request parameters can never
// raise or lower the production scan ceilings.
func withTableScanBudget(ctx context.Context, budget tableScanBudget) context.Context {
	return context.WithValue(ctx, tableScanBudgetContextKey{}, budget)
}

type tableSourcesResponse struct {
	Schema  string             `json:"schema"`
	Sources []tableSourceCount `json:"sources"`
}

type tableSourceCount struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// RegisterTableRoutes mounts the server-side table lane behind the admin
// trust gate, beside the sandbox query it is built on.
func (h *CoreAPIHandler) RegisterTableRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/data/table", h.requireAuth(peers.Admin, h.handleTablePage))
	mux.HandleFunc("/api/v1/data/table/sources", h.requireAuth(peers.Admin, h.handleTableSources))
	mux.HandleFunc("/api/v1/data/chart", h.requireAuth(peers.Admin, h.handleChart))
}

func parseTableRequest(r *http.Request) (tableRequest, error) {
	v := r.URL.Query()
	req := tableRequest{
		Schema:  strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(v.Get("schema"), ".fbs"))),
		Page:    parsePositiveIntParam(v.Get("page"), 1),
		Limit:   parsePositiveIntParam(v.Get("limit"), tableDefaultLimit),
		Cursor:  strings.TrimSpace(v.Get("cursor")),
		Sort:    strings.TrimSpace(v.Get("sort")),
		Dir:     strings.ToLower(strings.TrimSpace(v.Get("dir"))),
		Q:       strings.TrimSpace(v.Get("q")),
		Source:  strings.TrimSpace(v.Get("source")),
		Filters: map[string]string{},
	}
	if !tableSchemaCodePattern.MatchString(req.Schema) {
		return req, fmt.Errorf("schema is required (a standard code such as OMM)")
	}
	if req.Limit > tableMaxLimit {
		req.Limit = tableMaxLimit
	}
	if req.Dir != "asc" && req.Dir != "desc" {
		req.Dir = ""
	}
	if cols := strings.TrimSpace(v.Get("cols")); cols != "" {
		for _, c := range strings.Split(cols, ",") {
			if c = strings.TrimSpace(c); c != "" {
				req.Cols = append(req.Cols, c)
			}
		}
	}
	for key, vals := range v {
		if !strings.HasPrefix(key, "f.") || len(vals) == 0 {
			continue
		}
		if val := strings.TrimSpace(vals[0]); val != "" {
			req.Filters[strings.TrimPrefix(key, "f.")] = val
		}
	}
	return req, nil
}

// resolveColumn matches a requested identifier against the table's actual
// columns, case-insensitively, and refuses anything that is not a column.
// This — never quoting — is what keeps identifiers out of the injection
// surface.
func resolveColumn(name string, columns []string) (string, error) {
	if !tableIdentPattern.MatchString(name) {
		return "", fmt.Errorf("%q is not a column", name)
	}
	for _, c := range columns {
		if strings.EqualFold(c, name) {
			return c, nil
		}
	}
	return "", fmt.Errorf("%q is not a column of this table", name)
}

// escapeTableLiteral doubles single quotes for a plain string literal.
func escapeTableLiteral(v string) string {
	return strings.ReplaceAll(v, "'", "''")
}

// escapeTableLikeTerm escapes LIKE metacharacters and quotes, for use inside
// '%...%' with ESCAPE '\'.
func escapeTableLikeTerm(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `%`, `\%`)
	v = strings.ReplaceAll(v, `_`, `\_`)
	return escapeTableLiteral(v)
}

// defaultTableProjection is every column except the raw record blob and its
// stream offset — the table answers structure and values; record CONTENT is
// served by the record endpoints.
func defaultTableProjection(columns []string) []string {
	out := make([]string, 0, len(columns))
	for _, c := range columns {
		if c == "_data" || c == "_offset" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// buildTableSQL composes the COUNT and page statements for one validated
// request against the table's actual column list. Pure; every literal is
// escaped and every identifier resolved against `columns` or refused.
func buildTableSQL(req tableRequest, columns []string) (countSQL, pageSQL string, projection []string, err error) {
	if len(req.Cols) > 0 {
		for _, c := range req.Cols {
			resolved, rerr := resolveColumn(c, columns)
			if rerr != nil {
				return "", "", nil, rerr
			}
			projection = append(projection, resolved)
		}
	} else {
		projection = defaultTableProjection(columns)
	}
	if len(projection) == 0 {
		return "", "", nil, fmt.Errorf("no columns to select")
	}

	var where []string
	if req.Source != "" {
		where = append(where, fmt.Sprintf("_source = '%s'", escapeTableLiteral(req.Source)))
	}
	for col, val := range req.Filters {
		resolved, rerr := resolveColumn(col, columns)
		if rerr != nil {
			return "", "", nil, rerr
		}
		if lo, hi, ok := parseRangeFilter(val); ok {
			// A range on a numeric or epoch column: `from..to`, `>=from`,
			// `<=to`; bounds are seconds or UTC ISO time. Cells may hold
			// seconds or ISO text, so ISO text is converted in SQL.
			where = append(where, fmt.Sprintf(
				`(CASE WHEN %s GLOB '[0-9][0-9][0-9][0-9]-*' THEN CAST(strftime('%%s', %s) AS REAL) ELSE CAST(%s AS REAL) END) BETWEEN %s AND %s`,
				resolved, resolved, resolved,
				strconv.FormatFloat(lo, 'f', -1, 64), strconv.FormatFloat(hi, 'f', -1, 64)))
			continue
		}
		where = append(where, fmt.Sprintf(`CAST(%s AS TEXT) LIKE '%%%s%%' ESCAPE '\'`, resolved, escapeTableLikeTerm(val)))
	}
	if req.Q != "" {
		term := escapeTableLikeTerm(req.Q)
		var branches []string
		for _, c := range projection {
			if strings.HasPrefix(c, "_") {
				continue // lane bookkeeping is not what a search means
			}
			branches = append(branches, fmt.Sprintf(`CAST(%s AS TEXT) LIKE '%%%s%%' ESCAPE '\'`, c, term))
			if len(branches) >= tableSearchColumnCap {
				break
			}
		}
		if len(branches) > 0 {
			where = append(where, "("+strings.Join(branches, " OR ")+")")
		}
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	orderSQL := " ORDER BY _rowid DESC"
	if req.Sort != "" {
		resolved, rerr := resolveColumn(req.Sort, columns)
		if rerr != nil {
			return "", "", nil, rerr
		}
		dir := "ASC"
		if req.Dir == "desc" {
			dir = "DESC"
		}
		orderSQL = fmt.Sprintf(" ORDER BY %s %s", resolved, dir)
	}

	offset := (req.Page - 1) * req.Limit
	countSQL = fmt.Sprintf("SELECT COUNT(*) FROM %s%s", req.Schema, whereSQL)
	pageSQL = fmt.Sprintf("SELECT %s FROM %s%s%s LIMIT %d OFFSET %d",
		strings.Join(projection, ", "), req.Schema, whereSQL, orderSQL, req.Limit, offset)
	return countSQL, pageSQL, projection, nil
}

// buildSourcesSQL composes the distinct-source statement for one standard.
func buildSourcesSQL(schema string) string {
	return fmt.Sprintf("SELECT _source, COUNT(*) FROM %s GROUP BY _source ORDER BY COUNT(*) DESC", schema)
}

// tableColumns discovers the table's real column list through the sandbox
// (LIMIT 0 — metadata only). An engine error here is the "unknown standard"
// answer.
func (h *CoreAPIHandler) tableColumns(r *http.Request, schema string) ([]string, error) {
	res, err := h.store.SandboxedSelect(r.Context(),
		fmt.Sprintf("SELECT * FROM %s LIMIT 0", schema),
		storage.SandboxSelectCaps{MaxRows: 1, Timeout: tableQueryTimeout})
	if err != nil {
		return nil, err
	}
	return res.Columns, nil
}

// tableStoreWarming answers 503 while THIS standard's engine table is not yet
// loaded by a running hot-window hydration. It is per standard on purpose: the
// hydration locks per ingest batch, so a standard whose window is already
// loaded answers normally while a larger one is still being rebuilt (owner
// 2026-09-02: reads are independent of data-layer maintenance).
func (h *CoreAPIHandler) tableStoreWarming(w http.ResponseWriter, schema string) bool {
	if schema == "" {
		// Free-form SQL names no single standard; it answers with whatever is
		// loaded rather than being refused for the length of a rebuild.
		return false
	}
	if h.store.EngineHotWindowHydrating() && !h.store.EngineSchemaReady(schema+".fbs") {
		writeError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("the store is still loading %s after a restart; retry shortly", schema))
		return true
	}
	return false
}

func (h *CoreAPIHandler) handleTablePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}
	req, err := parseTableRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	columns, err := h.store.FullTableColumns(req.Schema + ".fbs")
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("standard %s is not queryable here: %v", req.Schema, err))
		return
	}
	projection, filters, sortColumn, err := resolveFullTableRequest(req, columns)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sourceName, err := durableTableSourceName(req.Schema, req.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cursor, err := decodeFullTableCursor(req.Schema+".fbs", req.Cursor)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	includeSource := fullTableNeedsSource(projection, filters, sortColumn, sourceName)

	// Count and page are intentionally separate store calls. Each takes one
	// bounded read-lock window; maintenance never waits behind a whole request.
	summary, err := h.store.DataSummary()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	total := fullTableStoredCount(summary, req.Schema+".fbs", sourceName)
	knownSourceName := ""
	if includeSource {
		knownSourceName = fullTableUniformSource(summary, req.Schema+".fbs", total)
	}
	if req.Q != "" || len(filters) > 0 || sortColumn != "" {
		rows, matched, scanned, partial, err := h.scanFullTableSelection(
			r.Context(), req, projection, filters, sortColumn, sourceName,
			knownSourceName, total, tableScanBudgetForContext(r.Context()),
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, tableResponse{
			Schema:    req.Schema + ".fbs",
			Columns:   projection,
			Rows:      rows,
			Total:     matched,
			Page:      req.Page,
			Limit:     req.Limit,
			Truncated: false,
			Source:    req.Source,
			Partial:   partial,
			Scanned:   scanned,
			Stored:    total,
		})
		return
	}

	// Fast path: an unfiltered, unsorted request reads only its requested
	// durable page (or the complete small selection), exactly as before.
	globalSort := ""
	if sortColumn == "_rowid" || sortColumn == "_source" || sortColumn == "_offset" {
		globalSort = sortColumn
	}
	pageLimit := req.Limit
	pageOffset := (req.Page - 1) * req.Limit
	if len(cursor) > 0 {
		pageOffset = 0
	}
	// A selected set that fits in one bounded block keeps exact historical
	// sort/filter/search semantics. Larger sets deliberately project only the
	// requested page so neither memory nor the store lock grows with total.
	completeSelection := total <= tableMaxLimit && len(cursor) == 0
	if completeSelection {
		pageLimit = int(total)
		pageOffset = 0
	}
	pageResult, err := h.store.FullTablePageWithCursor(storage.FullTablePageQuery{
		SchemaName:      req.Schema + ".fbs",
		SourceName:      sourceName,
		Limit:           pageLimit,
		Offset:          pageOffset,
		Cursor:          cursor,
		IncludeSource:   includeSource,
		KnownSourceName: knownSourceName,
		Sort:            globalSort,
		Descending:      req.Dir == "desc",
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	records := pageResult.Records
	rows, err := projectFullTablePage(req.Schema, records, projection, filters, req.Q, sortColumn, req.Dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if completeSelection {
		total = int64(len(rows))
		start := (req.Page - 1) * req.Limit
		if start >= len(rows) {
			rows = [][]string{}
		} else {
			end := start + req.Limit
			if end > len(rows) {
				end = len(rows)
			}
			rows = rows[start:end]
		}
	}
	nextCursor := ""
	if !completeSelection {
		nextCursor = encodeFullTableCursor(req.Schema+".fbs", pageResult.NextCursor)
	}
	writeJSON(w, http.StatusOK, tableResponse{
		Schema:     req.Schema + ".fbs",
		Columns:    projection,
		Rows:       rows,
		Total:      total,
		Page:       req.Page,
		Limit:      req.Limit,
		Truncated:  false,
		Source:     req.Source,
		NextCursor: nextCursor,
	})
}

func (h *CoreAPIHandler) handleTableSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}
	schema := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(r.URL.Query().Get("schema"), ".fbs")))
	if !tableSchemaCodePattern.MatchString(schema) {
		writeError(w, http.StatusBadRequest, "schema is required (a standard code such as OMM)")
		return
	}
	summary, err := h.store.DataSummary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("standard %s sources are unavailable: %v", schema, err))
		return
	}
	counts := map[string]int64{}
	for _, source := range summary.Sources {
		if source.SchemaName == schema+".fbs" && strings.TrimSpace(source.SourceName) != "" {
			counts[schema+"@"+source.SourceName] += source.Count
		}
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] == counts[names[j]] {
			return names[i] < names[j]
		}
		return counts[names[i]] > counts[names[j]]
	})
	out := tableSourcesResponse{Schema: schema + ".fbs", Sources: []tableSourceCount{}}
	for _, name := range names {
		out.Sources = append(out.Sources, tableSourceCount{Source: name, Count: int(counts[name])})
	}
	writeJSON(w, http.StatusOK, out)
}

func durableTableSourceName(schema, source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", nil
	}
	prefix := schema + "@"
	if !strings.HasPrefix(source, prefix) || strings.TrimPrefix(source, prefix) == "" {
		return "", fmt.Errorf("source must name a %s lane", schema)
	}
	return strings.TrimPrefix(source, prefix), nil
}

type fullTableCursorEnvelope struct {
	Schema string                      `json:"schema"`
	Rows   storage.FullTablePageCursor `json:"rows"`
}

func encodeFullTableCursor(schema string, cursor storage.FullTablePageCursor) string {
	if len(cursor) == 0 {
		return ""
	}
	payload, err := json.Marshal(fullTableCursorEnvelope{Schema: schema, Rows: cursor})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeFullTableCursor(schema, encoded string) (storage.FullTablePageCursor, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, nil
	}
	if len(encoded) > 64*1024 {
		return nil, fmt.Errorf("table cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("table cursor is invalid")
	}
	var envelope fullTableCursorEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("table cursor is invalid")
	}
	if envelope.Schema != schema || len(envelope.Rows) == 0 || len(envelope.Rows) > 1024 {
		return nil, fmt.Errorf("table cursor does not match %s", schema)
	}
	for table, rowID := range envelope.Rows {
		if len(table) > 1024 || !tableCursorTablePattern.MatchString(table) || rowID <= 0 {
			return nil, fmt.Errorf("table cursor is invalid")
		}
	}
	return envelope.Rows, nil
}

func fullTableNeedsSource(projection []string, filters map[string]string, sortColumn, sourceName string) bool {
	if strings.TrimSpace(sourceName) != "" || sortColumn == "_source" {
		return true
	}
	if _, filtered := filters["_source"]; filtered {
		return true
	}
	for _, column := range projection {
		if column == "_source" {
			return true
		}
	}
	return false
}

func fullTableStoredCount(summary *storage.DataSummary, schemaName, sourceName string) int64 {
	if summary == nil {
		return 0
	}
	if sourceName == "" {
		for _, schema := range summary.Schemas {
			if schema.SchemaName == schemaName {
				return schema.Count
			}
		}
		return 0
	}
	var total int64
	for _, source := range summary.Sources {
		if source.SchemaName == schemaName && source.SourceName == sourceName {
			total += source.Count
		}
	}
	return total
}

func fullTableUniformSource(summary *storage.DataSummary, schemaName string, stored int64) string {
	if summary == nil || stored <= 0 {
		return ""
	}
	name := ""
	var count int64
	for _, source := range summary.Sources {
		if source.SchemaName != schemaName || strings.TrimSpace(source.SourceName) == "" {
			continue
		}
		if name == "" {
			name = source.SourceName
		} else if name != source.SourceName {
			return ""
		}
		count += source.Count
	}
	if count != stored {
		return ""
	}
	return name
}

// scanFullTableSelection walks one durable standard/source selection newest to
// oldest. Every FullTablePage call owns only one small read-lock window; the
// decoder and predicates run after that lock has been released.
func (h *CoreAPIHandler) scanFullTableSelection(
	ctx context.Context,
	req tableRequest,
	projection []string,
	filters map[string]string,
	sortColumn string,
	sourceName string,
	knownSourceName string,
	stored int64,
	budget tableScanBudget,
) (rows [][]string, matched, scanned int64, partial bool, err error) {
	deadline := time.Now().Add(budget.MaxTime)
	pageStart := int64(req.Page-1) * int64(req.Limit)
	pageEnd := pageStart + int64(req.Limit)
	rows = make([][]string, 0, req.Limit)
	sortable := make([]fullTableProjectedRecord, 0)
	var binding *bulkBinding
	var cursor storage.FullTablePageCursor
	exhausted := false

scan:
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, scanned, false, err
		}
		if scanned >= int64(budget.MaxRecords) || !time.Now().Before(deadline) {
			exhausted = true
			break
		}

		chunkLimit := tableScanChunkSize
		if remaining := budget.MaxRecords - int(scanned); remaining < chunkLimit {
			chunkLimit = remaining
		}
		page, err := h.store.FullTablePageWithCursor(storage.FullTablePageQuery{
			SchemaName:      req.Schema + ".fbs",
			SourceName:      sourceName,
			Limit:           chunkLimit,
			Cursor:          cursor,
			IncludeSource:   fullTableNeedsSource(projection, filters, sortColumn, sourceName),
			KnownSourceName: knownSourceName,
		})
		if err != nil {
			return nil, 0, scanned, false, err
		}
		chunk := page.Records
		if len(chunk) == 0 {
			break
		}
		if binding == nil {
			binding, err = tableBindingForPage(req.Schema+".fbs", chunk[0].Data)
			if err != nil {
				return nil, 0, scanned, false, err
			}
		}

		for _, record := range chunk {
			if scanned >= int64(budget.MaxRecords) || !time.Now().Before(deadline) {
				exhausted = true
				break scan
			}
			projected, matches, err := projectFullTableRecord(
				req.Schema, record, binding, projection, filters, req.Q, sortColumn,
			)
			if err != nil {
				return nil, 0, scanned, false, err
			}
			scanned++
			if !matches {
				continue
			}
			if sortColumn != "" {
				sortable = append(sortable, projected)
			} else if matched >= pageStart && matched < pageEnd {
				rows = append(rows, projected.row)
			}
			matched++
		}

		cursor = page.NextCursor
		if len(chunk) < chunkLimit {
			break
		}
	}

	partial = exhausted && scanned < stored
	if sortColumn == "" {
		return rows, matched, scanned, partial, nil
	}

	sortFullTableRecords(sortable, sortColumn, req.Dir)
	if pageStart >= int64(len(sortable)) {
		return rows, matched, scanned, partial, nil
	}
	end := pageEnd
	if end > int64(len(sortable)) {
		end = int64(len(sortable))
	}
	for _, record := range sortable[int(pageStart):int(end)] {
		rows = append(rows, record.row)
	}
	return rows, matched, scanned, partial, nil
}

func resolveFullTableRequest(req tableRequest, columns []string) ([]string, map[string]string, string, error) {
	projection := defaultTableProjection(columns)
	if len(req.Cols) > 0 {
		projection = projection[:0]
		for _, column := range req.Cols {
			resolved, err := resolveColumn(column, columns)
			if err != nil {
				return nil, nil, "", err
			}
			projection = append(projection, resolved)
		}
	}
	if len(projection) == 0 {
		return nil, nil, "", fmt.Errorf("no columns to select")
	}
	filters := make(map[string]string, len(req.Filters))
	for column, value := range req.Filters {
		resolved, err := resolveColumn(column, columns)
		if err != nil {
			return nil, nil, "", err
		}
		filters[resolved] = value
	}
	sortColumn := ""
	if req.Sort != "" {
		resolved, err := resolveColumn(req.Sort, columns)
		if err != nil {
			return nil, nil, "", err
		}
		sortColumn = resolved
	}
	return projection, filters, sortColumn, nil
}

func tableBindingForPage(schemaName string, sample []byte) (*bulkBinding, error) {
	if binding, err := bulkBindingForSchema(schemaName); err == nil {
		return binding, nil
	}
	if schemaName != "MPE.fbs" {
		return nil, fmt.Errorf("no JSON projection is registered for %s", schemaName)
	}
	root, err := decodeMPE(sample)
	if err != nil {
		return nil, err
	}
	fields, err := generatedBindingFields(reflect.TypeOf(root))
	if err != nil {
		return nil, err
	}
	return &bulkBinding{
		schemaName: schemaName,
		rootType:   reflect.TypeOf(root),
		fields:     fields,
		decode: func(data []byte) (interface{}, error) {
			return decodeMPE(data)
		},
	}, nil
}

type fullTableProjectedRecord struct {
	row       []string
	sortValue string
	rowID     int64
}

func projectFullTablePage(schema string, records []*storage.Record, projection []string, filters map[string]string, q, sortColumn, direction string) ([][]string, error) {
	if len(records) == 0 {
		return [][]string{}, nil
	}
	binding, err := tableBindingForPage(schema+".fbs", records[0].Data)
	if err != nil {
		return nil, err
	}
	projected := make([]fullTableProjectedRecord, 0, len(records))
	for _, record := range records {
		row, matches, err := projectFullTableRecord(schema, record, binding, projection, filters, q, sortColumn)
		if err != nil {
			return nil, err
		}
		if matches {
			projected = append(projected, row)
		}
	}
	sortFullTableRecords(projected, sortColumn, direction)
	rows := make([][]string, len(projected))
	for i := range projected {
		rows[i] = projected[i].row
	}
	return rows, nil
}

func projectFullTableRecord(
	schema string,
	record *storage.Record,
	binding *bulkBinding,
	projection []string,
	filters map[string]string,
	q string,
	sortColumn string,
) (fullTableProjectedRecord, bool, error) {
	root, err := binding.decode(record.Data)
	if err != nil {
		return fullTableProjectedRecord{}, false, err
	}
	object, err := bindingObject(root, binding.fields)
	if err != nil {
		return fullTableProjectedRecord{}, false, err
	}
	values := make(map[string]string, len(projection)+len(filters)+2)
	setValue := func(column string) {
		if _, exists := values[column]; exists {
			return
		}
		switch column {
		case "_rowid":
			values[column] = strconv.FormatInt(record.RowID, 10)
		case "_offset":
			values[column] = strconv.FormatInt(record.StreamOffset, 10)
		case "_data":
			values[column] = base64.StdEncoding.EncodeToString(record.Data)
		case "_source":
			if record.SourceTags.SourceName != "" {
				values[column] = schema + "@" + record.SourceTags.SourceName
			}
		default:
			values[column] = fullTableCell(object[column])
		}
	}
	for _, column := range projection {
		setValue(column)
	}
	for column := range filters {
		setValue(column)
	}
	if sortColumn != "" {
		setValue(sortColumn)
	}
	setValue("_rowid")
	if !fullTableRecordMatches(values, projection, filters, q) {
		return fullTableProjectedRecord{}, false, nil
	}
	row := make([]string, len(projection))
	for i, column := range projection {
		row[i] = values[column]
	}
	return fullTableProjectedRecord{
		row:       row,
		sortValue: values[sortColumn],
		rowID:     record.RowID,
	}, true, nil
}

func sortFullTableRecords(records []fullTableProjectedRecord, sortColumn, direction string) {
	if sortColumn == "" {
		return
	}
	descending := direction == "desc"
	sort.SliceStable(records, func(i, j int) bool {
		comparison := compareFullTableCells(records[i].sortValue, records[j].sortValue)
		if comparison == 0 {
			return records[i].rowID > records[j].rowID
		}
		if descending {
			return comparison > 0
		}
		return comparison < 0
	})
}

func fullTableCell(value interface{}) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	}
	rv := reflect.ValueOf(value)
	if rv.IsValid() && (rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		encoded, err := json.Marshal(value)
		if err == nil {
			return string(encoded)
		}
	}
	return fmt.Sprint(value)
}

func fullTableRecordMatches(values map[string]string, projection []string, filters map[string]string, q string) bool {
	for column, term := range filters {
		if lo, hi, ok := parseRangeFilter(term); ok {
			n, ok := rangeCellValue(values[column])
			if !ok || n < lo || n > hi {
				return false
			}
			continue
		}
		if !strings.Contains(strings.ToLower(values[column]), strings.ToLower(term)) {
			return false
		}
	}
	term := strings.ToLower(strings.TrimSpace(q))
	if term == "" {
		return true
	}
	checked := 0
	for _, column := range projection {
		if strings.HasPrefix(column, "_") {
			continue
		}
		if strings.Contains(strings.ToLower(values[column]), term) {
			return true
		}
		checked++
		if checked >= tableSearchColumnCap {
			break
		}
	}
	return false
}

// parseRangeFilter reads the range forms a column filter may take —
// `from..to`, `>=from`, `<=to` — with each bound either a number (epoch
// seconds for time columns) or a UTC ISO time / `YYYY-MM-DD` day, which is
// converted to epoch seconds. Anything else is not a range.
func parseRangeFilter(term string) (lo, hi float64, ok bool) {
	t := strings.TrimSpace(term)
	bound := func(v string) (float64, bool) {
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, false
		}
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n, true
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
			if ts, err := time.Parse(layout, v); err == nil {
				return float64(ts.UTC().UnixNano()) / 1e9, true
			}
		}
		return 0, false
	}
	switch {
	case strings.HasPrefix(t, ">="):
		lo, ok = bound(t[2:])
		return lo, math.MaxFloat64, ok
	case strings.HasPrefix(t, "<="):
		hi, ok = bound(t[2:])
		return -math.MaxFloat64, hi, ok
	}
	if i := strings.Index(t, ".."); i > 0 {
		lo, okLo := bound(t[:i])
		hi, okHi := bound(t[i+2:])
		if okLo && okHi && lo <= hi {
			return lo, hi, true
		}
	}
	return 0, 0, false
}

// rangeCellValue reads a cell as a number, or as UTC ISO time in epoch seconds.
func rangeCellValue(cell string) (float64, bool) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(cell, 64); err == nil {
		return n, true
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05", "2006-01-02"} {
		if ts, err := time.Parse(layout, cell); err == nil {
			return float64(ts.UTC().UnixNano()) / 1e9, true
		}
	}
	return 0, false
}

func compareFullTableCells(left, right string) int {
	leftNumber, leftErr := strconv.ParseFloat(left, 64)
	rightNumber, rightErr := strconv.ParseFloat(right, 64)
	if leftErr == nil && rightErr == nil {
		switch {
		case leftNumber < rightNumber:
			return -1
		case leftNumber > rightNumber:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(strings.ToLower(left), strings.ToLower(right))
}
