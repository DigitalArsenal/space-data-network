package api

// admin_store.go — operator store-maintenance controls.
//
// ROUTE (admin write):
//
//	POST /api/v1/admin/store/hydrate[?force=true]
//	    Replays the compact record catalog into the SQL control tables and
//	    rebuilds the derived source summaries, then returns the resulting
//	    counts. This re-syncs /api/v1/stats sources[], /api/v1/data/index, and
//	    batch-clear bookkeeping WITHOUT a daemon restart — for the case where
//	    pre-restart runs are missing from the board because the post-boot
//	    background hydration has not finished yet (or a prior attempt errored).
//	    Returns {records_replayed, sources, total_records, duration_ms, forced}.
//
//	    force=true re-runs the metadata replay even when the catalog is already
//	    hydrated for this process (safe: every apply is an idempotent upsert).
//	    Without force, an already-hydrated catalog replays nothing
//	    (records_replayed=0) but the source summaries are still rebuilt from
//	    durable state, which is enough to re-sync a board that drifted.
//
//	POST /api/v1/admin/store/supersede[?dry_run=true]
//	    Retires every source batch except one explicitly named keep batch for
//	    a generic (schema, provider, source) storage lane. The JSON body is
//	    {"schema":"CAT.fbs","provider_id":"...","source_name":"...",
//	    "keep_batch":"sha256:..."}. Every field is required; schema must be
//	    admitted by this build and keep_batch must already tag a record in the
//	    exact requested lane. The response contains DatasetSupersedeResult,
//	    dry_run, and the retired batch IDs. dry_run=true computes the exact
//	    tag/record/file retirement without changing the store.
//
// AUTH: the /api/v1/admin/ prefix places this behind the SAME top-level
// admin-auth wall as every other admin API (main.go isAdminOnlyAPIPath → Admin
// trust when RequireAuth is on; unauthenticated on auth-disabled dev nodes),
// exactly like the runs_control.go admin writes. Both routes have the same
// posture and neither is a loopback self-gated bypass. The top-level admin
// security middleware also rejects foreign-origin cookie-authenticated writes.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	storeHydrateRoute   = "/api/v1/admin/store/hydrate"
	storeSupersedeRoute = "/api/v1/admin/store/supersede"
	storeAdminMaxBody   = 16 << 10
	storePreviewPage    = 5000
)

type storeSupersedeRequest struct {
	Schema     string `json:"schema"`
	ProviderID string `json:"provider_id"`
	SourceName string `json:"source_name"`
	KeepBatch  string `json:"keep_batch"`
}

type storeSupersedeResponse struct {
	storage.DatasetSupersedeResult
	DryRun         bool     `json:"dry_run"`
	RetiredBatches []string `json:"retired_batches"`
}

// StoreAdminHandler serves operator store-maintenance controls.
type StoreAdminHandler struct {
	store *storage.FlatSQLStore
}

// NewStoreAdminHandler creates the handler.
func NewStoreAdminHandler(store *storage.FlatSQLStore) *StoreAdminHandler {
	return &StoreAdminHandler{store: store}
}

// RegisterRoutes mounts the admin store-maintenance routes on the admin mux.
func (h *StoreAdminHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(storeHydrateRoute, h.handleHydrate)
	mux.HandleFunc(storeSupersedeRoute, h.handleSupersede)
}

func (h *StoreAdminHandler) handleHydrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	force := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "true")

	start := time.Now()
	replayed, err := h.store.ReplayRecordCatalog(force, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "replay record catalog: "+err.Error())
		return
	}
	if err := h.store.RebuildSourceSummaries(); err != nil {
		writeError(w, http.StatusInternalServerError, "rebuild source summaries: "+err.Error())
		return
	}
	duration := time.Since(start)

	sources, total := 0, int64(0)
	if summary, sErr := h.store.DataSummary(); sErr == nil && summary != nil {
		sources = len(summary.Sources)
		total = summary.TotalRecords
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"records_replayed": replayed,
		"sources":          sources,
		"total_records":    total,
		"duration_ms":      duration.Milliseconds(),
		"forced":           force,
	})
}

func (h *StoreAdminHandler) handleSupersede(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}

	dryRun, err := parseStoreDryRun(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req, err := decodeStoreSupersedeRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateStoreSupersedeRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	preview, retiredBatches, keepExists, err := previewStoreSupersede(h.store, req, dryRun)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "preview source batches: "+err.Error())
		return
	}
	if !keepExists {
		writeError(w, http.StatusNotFound, "keep_batch does not exist for the requested schema/provider/source")
		return
	}
	if dryRun {
		writeJSON(w, http.StatusOK, storeSupersedeResponse{
			DatasetSupersedeResult: preview,
			DryRun:                 true,
			RetiredBatches:         retiredBatches,
		})
		return
	}

	result, err := h.store.SupersedeSourceBatches(req.Schema, req.ProviderID, req.SourceName, req.KeepBatch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "supersede source batches: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, storeSupersedeResponse{
		DatasetSupersedeResult: result,
		DryRun:                 false,
		RetiredBatches:         retiredBatches,
	})
}

func parseStoreDryRun(r *http.Request) (bool, error) {
	values, present := r.URL.Query()["dry_run"]
	if !present {
		return false, nil
	}
	if len(values) != 1 {
		return false, errors.New("dry_run must be specified once")
	}
	dryRun, err := strconv.ParseBool(strings.TrimSpace(values[0]))
	if err != nil {
		return false, errors.New("dry_run must be true or false")
	}
	return dryRun, nil
}

func decodeStoreSupersedeRequest(w http.ResponseWriter, r *http.Request) (storeSupersedeRequest, error) {
	var req storeSupersedeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, storeAdminMaxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, fmtStoreRequestDecodeError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return req, errors.New("request body must contain one JSON object")
		}
		return req, fmtStoreRequestDecodeError(err)
	}
	req.Schema = strings.TrimSpace(req.Schema)
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	req.SourceName = strings.TrimSpace(req.SourceName)
	req.KeepBatch = strings.TrimSpace(req.KeepBatch)
	return req, nil
}

func fmtStoreRequestDecodeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return errors.New("request body is required")
	}
	return errors.New("invalid JSON request body: " + err.Error())
}

func validateStoreSupersedeRequest(req storeSupersedeRequest) error {
	for _, field := range []struct {
		name  string
		value string
		max   int
	}{
		{"schema", req.Schema, 128},
		{"provider_id", req.ProviderID, 1024},
		{"source_name", req.SourceName, 1024},
		{"keep_batch", req.KeepBatch, 1024},
	} {
		if field.value == "" {
			return errors.New(field.name + " is required")
		}
		if len(field.value) > field.max {
			return errors.New(field.name + " is too long")
		}
		if strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return errors.New(field.name + " contains control characters")
		}
	}
	for _, schemaName := range sds.SupportedSchemas {
		if req.Schema == schemaName {
			return nil
		}
	}
	return errors.New("schema is not admitted by this build")
}

// previewStoreSupersede computes the same logical tag and orphan-record result
// as SupersedeSourceBatches without writing. The per-CID count includes every
// source tag for the schema, so records shared with a kept or unrelated lane
// are not reported as deletions.
func previewStoreSupersede(store *storage.FlatSQLStore, req storeSupersedeRequest, countOrphans bool) (storage.DatasetSupersedeResult, []string, bool, error) {
	result := storage.DatasetSupersedeResult{
		SchemaName: req.Schema,
		ProviderID: req.ProviderID,
		SourceName: req.SourceName,
		KeepBatch:  req.KeepBatch,
	}
	staleTagsByCID := make(map[string]int64)
	retired := make(map[string]struct{})
	keepExists := false

	for offset := 0; ; offset += storePreviewPage {
		records, err := store.QueryRawRecordRefs(storage.RawRecordQuery{
			SchemaName: req.Schema,
			ProviderID: req.ProviderID,
			SourceName: req.SourceName,
			Limit:      storePreviewPage,
			Offset:     offset,
		})
		if err != nil {
			return result, nil, false, err
		}
		for _, record := range records {
			if record.SourceTags.BatchID == req.KeepBatch {
				keepExists = true
				continue
			}
			result.TagsDeleted++
			staleTagsByCID[record.CID]++
			retired[record.SourceTags.BatchID] = struct{}{}
		}
		if len(records) < storePreviewPage {
			break
		}
	}

	if countOrphans {
		for cid, staleTags := range staleTagsByCID {
			totalTags, err := store.CountRawRecords(storage.RawRecordQuery{
				SchemaName: req.Schema,
				CID:        cid,
			})
			if err != nil {
				return result, nil, false, err
			}
			if totalTags == staleTags {
				result.RecordsDeleted++
			}
		}
	}

	filesDeleted, fileBatches, err := previewSupersedePublicationFiles(store, req)
	if err != nil {
		return result, nil, false, err
	}
	result.FilesDeleted = filesDeleted
	for batchID := range fileBatches {
		retired[batchID] = struct{}{}
	}

	retiredBatches := make([]string, 0, len(retired))
	for batchID := range retired {
		retiredBatches = append(retiredBatches, batchID)
	}
	sort.Strings(retiredBatches)
	return result, retiredBatches, keepExists, nil
}

func previewSupersedePublicationFiles(store *storage.FlatSQLStore, req storeSupersedeRequest) (int, map[string]struct{}, error) {
	publications, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   req.Schema,
		ProviderID:   req.ProviderID,
		SourceName:   req.SourceName,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		return 0, nil, err
	}
	keepFiles := make(map[string]struct{})
	for _, publication := range publications {
		if publication.BatchID != req.KeepBatch {
			continue
		}
		if path, pathErr := store.DatasetPublicationShardPath(publication); pathErr == nil {
			keepFiles[path] = struct{}{}
		}
		if path, pathErr := store.DatasetPublicationIndexPath(publication); pathErr == nil {
			keepFiles[path] = struct{}{}
		}
	}

	staleFiles := make(map[string]string)
	for _, publication := range publications {
		if publication.BatchID == req.KeepBatch {
			continue
		}
		if path, pathErr := store.DatasetPublicationShardPath(publication); pathErr == nil {
			staleFiles[path] = publication.BatchID
		}
		if path, pathErr := store.DatasetPublicationIndexPath(publication); pathErr == nil {
			staleFiles[path] = publication.BatchID
		}
	}
	fileBatches := make(map[string]struct{})
	filesDeleted := 0
	for path, batchID := range staleFiles {
		if _, shared := keepFiles[path]; shared {
			continue
		}
		if _, statErr := os.Stat(path); statErr == nil {
			filesDeleted++
			fileBatches[batchID] = struct{}{}
		} else if !os.IsNotExist(statErr) {
			return 0, nil, statErr
		}
	}
	return filesDeleted, fileBatches, nil
}
