package api

// GET /api/v1/data/table — the dashboard's server-side table lane: one page of
// one standard's engine table, with server-executed pagination, column sort,
// per-column contains-filters, a global search term, and a NETWORK SOURCE
// selector (`_source` — every engine-routed table carries the lane that
// delivered each record, e.g. "OMM@celestrak-gp").
//
// GET /api/v1/data/table/sources — the distinct `_source` lanes one standard's
// table holds, with counts. Powers the "by source" selector.
//
// Both are composed HERE, from validated identifiers and escaped literals, and
// executed through storage.SandboxedSelect — the same read-only, capped,
// single-SELECT sandbox behind POST /api/v1/query. The composition is pure
// (buildTableSQL/buildSourcesSQL) so the injection surface is testable without
// a store. Admin-gated like the sandbox lane: this is the operator's explorer;
// the anonymous read surface stays the routed bulk/index endpoints.

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	tableDefaultLimit = 100
	tableMaxLimit     = 500
	// tableSearchColumnCap bounds how many columns a global search term ORs
	// across, so a wide table cannot turn one keystroke into a 60-branch scan.
	tableSearchColumnCap = 24
	tableQueryTimeout    = 20 * time.Second
)

var (
	tableSchemaCodePattern = regexp.MustCompile(`^[A-Za-z0-9]{2,8}$`)
	tableIdentPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// tableRequest is one parsed, not-yet-validated table page request.
type tableRequest struct {
	Schema  string
	Cols    []string
	Page    int
	Limit   int
	Sort    string
	Dir     string
	Q       string
	Source  string
	Filters map[string]string
}

type tableResponse struct {
	Schema    string     `json:"schema"`
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Total     int        `json:"total"`
	Page      int        `json:"page"`
	Limit     int        `json:"limit"`
	Truncated bool       `json:"truncated"`
	Source    string     `json:"source,omitempty"`
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
}

func parseTableRequest(r *http.Request) (tableRequest, error) {
	v := r.URL.Query()
	req := tableRequest{
		Schema:  strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(v.Get("schema"), ".fbs"))),
		Page:    parsePositiveIntParam(v.Get("page"), 1),
		Limit:   parsePositiveIntParam(v.Get("limit"), tableDefaultLimit),
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

// tableStoreWarming answers 503 immediately while startup catalog hydration
// holds (or is waiting for) the store lock. Without this the handler BLOCKS on
// the read lock for the whole rebuild — measured on the dev store: hours of a
// hung request and a silently empty grid, which reads as "there are NO rows".
func (h *CoreAPIHandler) tableStoreWarming(w http.ResponseWriter) bool {
	if h.store.RecordCatalogHydrating() || h.store.EngineHotWindowHydrating() {
		writeError(w, http.StatusServiceUnavailable,
			"the store is rebuilding its catalog after a restart; retry shortly")
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
	if h.tableStoreWarming(w) {
		return
	}
	req, err := parseTableRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	columns, err := h.tableColumns(r, req.Schema)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("standard %s is not queryable here: %v", req.Schema, err))
		return
	}
	countSQL, pageSQL, projection, err := buildTableSQL(req, columns)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	countRes, err := h.store.SandboxedSelect(r.Context(), countSQL,
		storage.SandboxSelectCaps{MaxRows: 1, Timeout: tableQueryTimeout})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	total := 0
	if len(countRes.Rows) > 0 && len(countRes.Rows[0]) > 0 {
		total, _ = strconv.Atoi(countRes.Rows[0][0])
	}
	pageRes, err := h.store.SandboxedSelect(r.Context(), pageSQL,
		storage.SandboxSelectCaps{MaxRows: req.Limit, Timeout: tableQueryTimeout})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tableResponse{
		Schema:    req.Schema + ".fbs",
		Columns:   projection,
		Rows:      pageRes.Rows,
		Total:     total,
		Page:      req.Page,
		Limit:     req.Limit,
		Truncated: pageRes.Truncated,
		Source:    req.Source,
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
	if h.tableStoreWarming(w) {
		return
	}
	schema := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(r.URL.Query().Get("schema"), ".fbs")))
	if !tableSchemaCodePattern.MatchString(schema) {
		writeError(w, http.StatusBadRequest, "schema is required (a standard code such as OMM)")
		return
	}
	res, err := h.store.SandboxedSelect(r.Context(), buildSourcesSQL(schema),
		storage.SandboxSelectCaps{MaxRows: 200, Timeout: tableQueryTimeout})
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("standard %s is not queryable here: %v", schema, err))
		return
	}
	out := tableSourcesResponse{Schema: schema + ".fbs", Sources: []tableSourceCount{}}
	for _, row := range res.Rows {
		if len(row) < 2 {
			continue
		}
		n, _ := strconv.Atoi(row[1])
		out.Sources = append(out.Sources, tableSourceCount{Source: row[0], Count: n})
	}
	writeJSON(w, http.StatusOK, out)
}
