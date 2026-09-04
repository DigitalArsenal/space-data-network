package api

// .sdn export lane (fbcs program): every routed standard streams out as a
// re-importable size-prefixed FlatBuffer file. The first frame is a $QRP page
// header describing the selection; every following frame is one stored record
// exactly as the store holds it, so POST /api/v1/data/publish/batch/{schema}
// and the archive import accept the file unchanged.
//
//	GET /api/v1/data/<code lower>/export?source=&provider_id=&batch_id=&from=&to=&cursor=&limit=   admin
//
// Without provider/batch/time filters the export walks the durable rowid
// pager (every stored frame, never the engine window); with them it walks the
// indexed record query. limit=0 means everything; a truncated export names
// the NEXT_CURSOR that resumes it.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func init() {
	RegisterAdminMount("export", mountExport)
}

const (
	// exportFullTablePage is the durable pager's chunk (its own maximum).
	exportFullTablePage = 2000
	// exportIndexedPage is the indexed query's large-result page.
	exportIndexedPage = 250000
	// exportSpoolDirName holds the spool files an export is counted into
	// before its header is written; it lives beside the archive plane.
	exportSpoolDirName = "export-spool"
)

var exportFileNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ExportHandler serves the per-standard export routes.
type ExportHandler struct {
	deps *AdminMountDeps
}

// NewExportHandler builds a handler over the mount deps.
func NewExportHandler(deps *AdminMountDeps) *ExportHandler {
	if deps == nil {
		deps = &AdminMountDeps{}
	}
	return &ExportHandler{deps: deps}
}

func mountExport(mux *http.ServeMux, deps *AdminMountDeps) {
	h := NewExportHandler(deps)
	h.RegisterRoutes(mux)
	log.Infof("Export lane (.sdn) mounted for %d standards under /api/v1/data/<code>/export", len(sds.SupportedSchemas))
}

// ExportPath is the export route for a standard code.
func ExportPath(code string) string {
	return "/api/v1/data/" + strings.ToLower(strings.TrimSuffix(strings.TrimSpace(code), ".fbs")) + "/export"
}

// RegisterRoutes mounts one export route per supported schema.
func (h *ExportHandler) RegisterRoutes(mux *http.ServeMux) {
	for _, schema := range sds.SupportedSchemas {
		code := strings.ToUpper(strings.TrimSuffix(schema, ".fbs"))
		if code == "" {
			continue
		}
		mux.HandleFunc(ExportPath(code), h.deps.adminGate(h.exportHandler(code)))
	}
}

// exportRequest is one parsed export selection.
type exportRequest struct {
	code       string
	schema     string
	sourceName string
	providerID string
	batchID    string
	from, to   *time.Time
	limit      int
	cursorRaw  string
	// indexed selects the indexed record query (provider/batch/time filters).
	indexed bool
	// tableCursor resumes the durable pager; indexedOffset the indexed walk.
	tableCursor   storage.FullTablePageCursor
	indexedOffset int
}

func (h *ExportHandler) exportHandler(code string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to export records.", 0)
			return
		}
		store := h.deps.Store
		if store == nil {
			WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "The record store is not available on this node.", 5*time.Second)
			return
		}
		req, err := parseExportRequest(code, r)
		if err != nil {
			WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error(), 0)
			return
		}
		if !store.RecordCatalogHydrated() {
			WriteErrorFrame(w, http.StatusServiceUnavailable, "hydrating", "The record catalog is still loading; the export would be incomplete. Try again shortly.", 30*time.Second)
			return
		}
		summary, err := store.DataSummary()
		if err != nil {
			WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "Record counts are not available right now.", 5*time.Second)
			return
		}
		total := fullTableStoredCount(summary, req.schema, req.sourceName)
		if req.providerID != "" || req.batchID != "" {
			total = exportLaneCount(summary, req)
		}

		spool, err := h.openSpool()
		if err != nil {
			WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "The export could not be staged on this node.", 5*time.Second)
			return
		}
		defer func() {
			_ = spool.Close()
			_ = os.Remove(spool.Name())
		}()

		count, nextCursor, err := h.streamSelection(store, req, spool)
		if err != nil {
			if isUnknownStandard(err) {
				WriteErrorFrame(w, http.StatusNotFound, "not_found", "That standard is not routed on this node.", 0)
				return
			}
			log.Warnf("Export %s: %v", req.code, err)
			WriteErrorFrame(w, http.StatusInternalServerError, "export_failed", "The export stopped before it finished. Try again.", 0)
			return
		}
		spoolSize, err := spool.Seek(0, io.SeekEnd)
		if err != nil {
			WriteErrorFrame(w, http.StatusInternalServerError, "export_failed", "The export could not be read back.", 0)
			return
		}
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			WriteErrorFrame(w, http.StatusInternalServerError, "export_failed", "The export could not be read back.", 0)
			return
		}

		now := time.Now().UTC()
		fields := QRPFields{
			Kind:           QRPKindPage,
			Status:         QRPStatusOk,
			SchemaName:     req.schema,
			FileIdentifier: "$" + req.code,
			ProviderID:     req.providerID,
			SourceName:     req.sourceName,
			BatchID:        req.batchID,
			Cursor:         req.cursorRaw,
			NextCursor:     nextCursor,
			Limit:          clampUint32(int64(req.limit)),
			RecordCount:    clampUint32(int64(count)),
			TotalCount:     uint64(nonNegative64(total)),
			Stored:         uint64(nonNegative64(total)),
			Truncated:      nextCursor != "",
			GeneratedAtMs:  now.UnixMilli(),
			Etag:           exportEtag(r.URL.RawQuery, total),
		}
		if req.from != nil {
			fields.FromEpochMs = req.from.UnixMilli()
		}
		if req.to != nil {
			fields.ToEpochMs = req.to.UnixMilli()
		}
		if nextCursor != "" {
			fields.Status = QRPStatusPartial
			fields.Partial = true
		}
		header := BuildQRP(fields)

		sourceLabel := "all"
		if req.sourceName != "" {
			sourceLabel = exportFileNameSanitizer.ReplaceAllString(req.sourceName, "-")
		}
		fileName := fmt.Sprintf("%s-%s-%s.sdn", strings.ToLower(req.code), sourceLabel, now.Format("20060102T150405Z"))

		hdr := w.Header()
		hdr.Set("Content-Type", StreamContentType)
		hdr.Set(StreamFormatHeader, StreamFormat)
		hdr.Set(StreamSchemaHeader, req.schema)
		hdr.Set(StreamRecordCountHeader, strconv.Itoa(count))
		if nextCursor != "" {
			hdr.Set(StreamNextCursorHeader, nextCursor)
		}
		hdr.Set("Cache-Control", "no-store")
		hdr.Set("Content-Disposition", `attachment; filename="`+fileName+`"`)
		hdr.Set("Content-Length", strconv.FormatInt(int64(len(header))+spoolSize, 10))
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(header); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.Copy(w, spool)
	}
}

// parseExportRequest reads the export selection from the query string.
func parseExportRequest(code string, r *http.Request) (exportRequest, error) {
	q := r.URL.Query()
	req := exportRequest{code: code, schema: code + ".fbs"}

	source := strings.TrimSpace(q.Get("source"))
	if strings.HasPrefix(source, code+"@") {
		name, err := durableTableSourceName(code, source)
		if err != nil {
			return req, errors.New("The source must name a " + code + " lane.")
		}
		source = name
	}
	req.sourceName = source
	req.providerID = strings.TrimSpace(q.Get("provider_id"))
	req.batchID = strings.TrimSpace(q.Get("batch_id"))

	var err error
	if req.from, err = parseEpochParam(q.Get("from")); err != nil {
		return req, errors.New("The from value must be an RFC 3339 time or unix milliseconds.")
	}
	if req.to, err = parseEpochParam(q.Get("to")); err != nil {
		return req, errors.New("The to value must be an RFC 3339 time or unix milliseconds.")
	}
	if req.from != nil && req.to != nil && req.from.After(*req.to) {
		return req, errors.New("The from value must not be after the to value.")
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return req, errors.New("The limit must be a whole number; 0 exports everything.")
		}
		req.limit = n
	}
	req.indexed = req.providerID != "" || req.batchID != "" || req.from != nil || req.to != nil
	req.cursorRaw = strings.TrimSpace(q.Get("cursor"))
	if req.cursorRaw != "" {
		if req.indexed {
			offset, err := strconv.Atoi(req.cursorRaw)
			if err != nil || offset < 0 {
				return req, errors.New("The cursor does not belong to this selection.")
			}
			req.indexedOffset = offset
		} else {
			cursor, err := decodeFullTableCursor(code, req.cursorRaw)
			if err != nil {
				return req, errors.New("The cursor does not belong to this selection.")
			}
			req.tableCursor = cursor
		}
	}
	return req, nil
}

// parseEpochParam accepts RFC 3339 or unix milliseconds; empty means unset.
func parseEpochParam(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		t = t.UTC()
		return &t, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		t = t.UTC()
		return &t, nil
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	t := time.UnixMilli(ms).UTC()
	return &t, nil
}

// exportLaneCount counts the summary rows a provider/batch-filtered
// selection covers.
func exportLaneCount(summary *storage.DataSummary, req exportRequest) int64 {
	if summary == nil {
		return 0
	}
	var total int64
	for _, src := range summary.Sources {
		if src.SchemaName != req.schema {
			continue
		}
		if req.sourceName != "" && src.SourceName != req.sourceName {
			continue
		}
		if req.providerID != "" && src.ProviderID != req.providerID {
			continue
		}
		if req.batchID != "" && src.BatchID != req.batchID {
			continue
		}
		total += src.Count
	}
	return total
}

func exportEtag(rawQuery string, total int64) string {
	sum := sha256.Sum256([]byte(rawQuery + "\n" + strconv.FormatInt(total, 10)))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func isUnknownStandard(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown standard") || strings.Contains(msg, "invalid schema name") || strings.Contains(msg, "unsupported schema")
}

// openSpool creates the spool file an export is counted into.
func (h *ExportHandler) openSpool() (*os.File, error) {
	dir := ""
	if h.deps.Store != nil {
		if plane := h.deps.Store.ArchiveOutputDir(); plane != "" {
			dir = filepath.Join(filepath.Dir(plane), exportSpoolDirName)
		}
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			dir = ""
		}
	}
	return os.CreateTemp(dir, "export-*.sdn")
}

// streamSelection writes every selected record frame to out and returns the
// count and the cursor that resumes a truncated export.
func (h *ExportHandler) streamSelection(store *storage.FlatSQLStore, req exportRequest, out io.Writer) (int, string, error) {
	bw := bufio.NewWriterSize(out, 1<<20)
	count := 0
	nextCursor := ""
	remaining := func() int {
		if req.limit <= 0 {
			return -1
		}
		return req.limit - count
	}
	if req.indexed {
		offset := req.indexedOffset
		for {
			page := exportIndexedPage
			if rem := remaining(); rem >= 0 && rem < page {
				page = rem
			}
			if page == 0 {
				break
			}
			filter := storage.IndexedRecordQuery{
				SchemaName:          req.schema,
				ProviderID:          req.providerID,
				SourceName:          req.sourceName,
				BatchID:             req.batchID,
				From:                req.from,
				To:                  req.to,
				Limit:               page,
				Offset:              offset,
				AllowLargeResultSet: true,
			}
			records, err := store.QueryIndexedRecords(filter)
			if err != nil {
				return count, "", err
			}
			if len(records) == 0 {
				break
			}
			if err := store.WriteRawRecordFrames(bw, records); err != nil {
				return count, "", err
			}
			count += len(records)
			offset += len(records)
			if len(records) < page {
				break
			}
			if rem := remaining(); rem == 0 {
				nextCursor = strconv.Itoa(offset)
				break
			}
		}
	} else {
		cursor := req.tableCursor
		for {
			page := exportFullTablePage
			if rem := remaining(); rem >= 0 && rem < page {
				page = rem
			}
			if page == 0 {
				break
			}
			result, err := store.FullTablePageWithCursor(storage.FullTablePageQuery{
				SchemaName:      req.schema,
				SourceName:      req.sourceName,
				KnownSourceName: req.sourceName,
				Limit:           page,
				Cursor:          cursor,
			})
			if err != nil {
				return count, "", err
			}
			if len(result.Records) == 0 {
				break
			}
			if err := store.WriteRawRecordFrames(bw, result.Records); err != nil {
				return count, "", err
			}
			count += len(result.Records)
			cursor = result.NextCursor
			if len(cursor) == 0 {
				break
			}
			if rem := remaining(); rem == 0 {
				nextCursor = encodeFullTableCursor(req.code, cursor)
				break
			}
			if len(result.Records) < page {
				break
			}
		}
	}
	if err := bw.Flush(); err != nil {
		return count, "", err
	}
	return count, nextCursor, nil
}
