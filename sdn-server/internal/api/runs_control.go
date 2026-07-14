package api

// runs_control.go — operator run controls for the App 2 supplemental-OMM
// pipeline (owner directive 2026-07-14: "stop / restart / clear the runs" +
// "select which providers will be used").
//
// ROUTES (admin writes + anonymous reads):
//
//	POST /api/v1/admin/runs/clear?schema=&provider_id=&source_name=&batch_id=
//	    Deletes that batch's source-tag rows + any record thereby orphaned
//	    (records shared with other kept batches survive — storage
//	    ClearSourceBatch, same semantics as the dataset supersede path).
//	    Returns {schema, source_name, batch_id, tags_deleted, records_deleted}.
//
//	POST /api/v1/admin/runs/stop?batch_id=<id>[&clear=true]
//	    Sets (or, with clear=true, removes) a COOPERATIVE stop flag for the
//	    batch. The node cannot kill the external pipeline runner — the flag is
//	    a contract: the runner polls GET /api/v1/runs/flags between publish
//	    chunks and aborts cleanly when its batch is flagged. run_token= is
//	    accepted as an alias for batch_id=. Flags persist across node restarts.
//
//	GET/POST /api/v1/admin/runs/providers
//	    Reads / replaces the persisted provider selection ({"providers":
//	    {"<key>":bool}}). A key absent from the map is ENABLED by default —
//	    the selection only records explicit operator choices. For providers
//	    backed by a runtime-module cron schedule, the node also flips that
//	    schedule's enabled bit (via the injected module toggler); external-
//	    runner providers are covered purely by the read contract below.
//
//	GET /api/v1/runs/flags?batch_id=      (ANONYMOUS read)
//	GET /api/v1/runs/providers            (ANONYMOUS read)
//
// RUNNER CONTRACT (the external pipeline's node-side source of truth):
//   - Between publish chunks, poll GET /api/v1/runs/flags?batch_id=<its batch>
//     → {"batch_id":..., "stop":bool, "requested_at":RFC3339}. stop=true means
//     finish the in-flight chunk, publish nothing further for that batch, and
//     exit cleanly (the batch stays queryable until an operator clears it).
//   - At startup (and optionally between providers), read
//     GET /api/v1/runs/providers → {"providers":{key:bool},"updated_at":...}.
//     When the node it publishes to carries a selection, the runner INTERSECTS
//     it with its own --providers arguments: node selection is the source of
//     truth for that node. A key absent from the map is enabled.
//
// AUTH: the write routes live under /api/v1/admin/ and are therefore behind
// the SAME top-level admin-auth wall as every other admin API (main.go
// isAdminOnlyAPIPath → Admin trust when RequireAuth is on; unauthenticated on
// auth-disabled dev nodes, exactly like the rest of the admin mux). The two
// GET reads are in the anonymous allowlist (isPublicReadAPIPath) so the board
// and the runner can read flags/selection without credentials. No bypasses.
//
// PERSISTENCE: one JSON file next to the store (runs-control.json), written
// atomically (tmp+rename, 0600) — the same operator-state file pattern as
// modulert's capability policy store.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// runsControlFileName is the operator-state file, colocated with the store.
const runsControlFileName = "runs-control.json"

// RunStopFlag is one cooperative stop request, keyed by batch id.
type RunStopFlag struct {
	BatchID     string `json:"batch_id"`
	SchemaName  string `json:"schema,omitempty"`
	ProviderID  string `json:"provider_id,omitempty"`
	SourceName  string `json:"source_name,omitempty"`
	RequestedAt string `json:"requested_at"`
}

// runsControlState is the persisted operator state.
type runsControlState struct {
	// StopFlags: batch_id -> flag. Present = stop requested.
	StopFlags map[string]RunStopFlag `json:"stop_flags,omitempty"`
	// Providers: provider key -> enabled. ABSENT KEY = ENABLED (default-on);
	// the map records explicit operator choices only.
	Providers map[string]bool `json:"providers,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

// ModuleScheduleToggler flips the cron schedules of runtime modules backing a
// provider lane. Returns how many schedules were updated (0 = the provider has
// no module-scheduled lane on this node — the external-runner case).
type ModuleScheduleToggler func(ctx context.Context, providerKey string, enabled bool) (int, error)

// RunsControlHandler serves the run-control surface.
type RunsControlHandler struct {
	store        *storage.FlatSQLStore
	statePath    string
	moduleToggle ModuleScheduleToggler

	mu    sync.Mutex
	state runsControlState
}

// NewRunsControlHandler creates the handler, loading any persisted state from
// runs-control.json next to the store. moduleToggle may be nil (no runtime
// module manager on this node).
func NewRunsControlHandler(store *storage.FlatSQLStore, moduleToggle ModuleScheduleToggler) *RunsControlHandler {
	h := &RunsControlHandler{store: store, moduleToggle: moduleToggle}
	if store != nil && strings.TrimSpace(store.Path()) != "" {
		h.statePath = filepath.Join(filepath.Dir(store.Path()), runsControlFileName)
	}
	h.loadState()
	return h
}

// NewRunsControlHandlerWithStatePath is the test seam: explicit state file path.
func NewRunsControlHandlerWithStatePath(store *storage.FlatSQLStore, statePath string, moduleToggle ModuleScheduleToggler) *RunsControlHandler {
	h := &RunsControlHandler{store: store, statePath: statePath, moduleToggle: moduleToggle}
	h.loadState()
	return h
}

// RegisterRoutes mounts the admin writes and the anonymous reads. The mux is
// the admin mux; the /api/v1/admin/ prefix places the writes behind the
// top-level admin-auth wall automatically (see the file comment).
func (h *RunsControlHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/runs/clear", h.handleClear)
	mux.HandleFunc("/api/v1/admin/runs/stop", h.handleStop)
	mux.HandleFunc("/api/v1/admin/runs/providers", h.handleProvidersAdmin)
	mux.HandleFunc("/api/v1/runs/flags", h.handleFlagsRead)
	mux.HandleFunc("/api/v1/runs/providers", h.handleProvidersRead)
}

// ---------------------------------------------------------------------------
// State persistence (atomic JSON file, capability-policy pattern)
// ---------------------------------------------------------------------------

func (h *RunsControlHandler) loadState() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = runsControlState{}
	if h.statePath == "" {
		return
	}
	data, err := os.ReadFile(h.statePath)
	if err != nil {
		return // missing file = empty state (all providers enabled, no flags)
	}
	var loaded runsControlState
	if err := json.Unmarshal(data, &loaded); err != nil {
		return // unreadable state file: start empty rather than fail the node
	}
	h.state = loaded
}

// persistLocked writes the state file atomically. Callers hold h.mu.
func (h *RunsControlHandler) persistLocked() error {
	if h.statePath == "" {
		return fmt.Errorf("runs-control state path unavailable (no store path)")
	}
	h.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(h.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runs-control state: %w", err)
	}
	tmp := h.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write runs-control state: %w", err)
	}
	if err := os.Rename(tmp, h.statePath); err != nil {
		return fmt.Errorf("commit runs-control state: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Admin writes
// ---------------------------------------------------------------------------

func (h *RunsControlHandler) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	values := r.URL.Query()
	schema := normalizeRecordIndexSchema(firstNonEmptyDataString(values.Get("schema"), values.Get("schema_name")))
	sourceName := firstNonEmptyDataString(values.Get("source_name"), values.Get("sourceName"))
	batchID := firstNonEmptyDataString(values.Get("batch_id"), values.Get("batchId"))
	providerID := firstNonEmptyDataString(values.Get("provider_id"), values.Get("providerId"))
	if sourceName == "" || batchID == "" {
		writeError(w, http.StatusBadRequest, "source_name and batch_id are required")
		return
	}

	result, err := h.store.ClearSourceBatch(schema, providerID, sourceName, batchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// A cleared batch's stop flag is moot — drop it so the flags read stays
	// an honest reflection of live intent.
	h.mu.Lock()
	if _, ok := h.state.StopFlags[batchID]; ok {
		delete(h.state.StopFlags, batchID)
		_ = h.persistLocked() // best-effort; the clear itself already succeeded
	}
	h.mu.Unlock()

	writeJSON(w, http.StatusOK, result)
}

func (h *RunsControlHandler) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	values := r.URL.Query()
	batchID := firstNonEmptyDataString(values.Get("batch_id"), values.Get("batchId"), values.Get("run_token"), values.Get("runToken"))
	if batchID == "" {
		writeError(w, http.StatusBadRequest, "batch_id (or run_token) is required")
		return
	}
	clearFlag := strings.EqualFold(strings.TrimSpace(values.Get("clear")), "true")

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.StopFlags == nil {
		h.state.StopFlags = map[string]RunStopFlag{}
	}
	if clearFlag {
		delete(h.state.StopFlags, batchID)
	} else {
		h.state.StopFlags[batchID] = RunStopFlag{
			BatchID:     batchID,
			SchemaName:  firstNonEmptyDataString(values.Get("schema"), values.Get("schema_name")),
			ProviderID:  firstNonEmptyDataString(values.Get("provider_id"), values.Get("providerId")),
			SourceName:  firstNonEmptyDataString(values.Get("source_name"), values.Get("sourceName")),
			RequestedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}
	if err := h.persistLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	flag, stop := h.state.StopFlags[batchID]
	resp := map[string]interface{}{"batch_id": batchID, "stop": stop}
	if stop {
		resp["requested_at"] = flag.RequestedAt
	}
	writeJSON(w, http.StatusOK, resp)
}

// providersUpdateRequest is the POST body for the provider selection.
type providersUpdateRequest struct {
	Providers map[string]bool `json:"providers"`
}

func (h *RunsControlHandler) handleProvidersAdmin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.writeProviders(w)
	case http.MethodPost, http.MethodPut:
		var req providersUpdateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid providers payload: "+err.Error())
			return
		}
		if req.Providers == nil {
			writeError(w, http.StatusBadRequest, "providers map is required")
			return
		}

		h.mu.Lock()
		previous := h.state.Providers
		h.state.Providers = req.Providers
		err := h.persistLocked()
		if err != nil {
			h.state.Providers = previous // do not report state the disk did not accept
		}
		h.mu.Unlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Module-scheduled lanes: flip their cron schedules to match. Reported
		// per provider ("updated N schedule(s)" / "none" / the error) — never
		// silently pretended. External-runner lanes are 0/none by design.
		moduleResults := map[string]string{}
		for key, enabled := range req.Providers {
			if h.moduleToggle == nil {
				moduleResults[key] = "no module runtime on this node"
				continue
			}
			n, err := h.moduleToggle(r.Context(), key, enabled)
			switch {
			case err != nil:
				moduleResults[key] = "error: " + err.Error()
			case n == 0:
				moduleResults[key] = "no module schedule (external runner lane)"
			default:
				moduleResults[key] = fmt.Sprintf("updated %d schedule(s)", n)
			}
		}

		h.mu.Lock()
		resp := map[string]interface{}{
			"providers":        h.state.Providers,
			"updated_at":       h.state.UpdatedAt,
			"module_schedules": moduleResults,
		}
		h.mu.Unlock()
		writeJSON(w, http.StatusOK, resp)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// Anonymous reads
// ---------------------------------------------------------------------------

func (h *RunsControlHandler) handleFlagsRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	batchID := firstNonEmptyDataString(r.URL.Query().Get("batch_id"), r.URL.Query().Get("batchId"), r.URL.Query().Get("run_token"))

	h.mu.Lock()
	defer h.mu.Unlock()
	if batchID != "" {
		flag, stop := h.state.StopFlags[batchID]
		resp := map[string]interface{}{"batch_id": batchID, "stop": stop}
		if stop {
			resp["requested_at"] = flag.RequestedAt
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	flags := make([]RunStopFlag, 0, len(h.state.StopFlags))
	for _, f := range h.state.StopFlags {
		flags = append(flags, f)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"flags": flags})
}

func (h *RunsControlHandler) handleProvidersRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.writeProviders(w)
}

func (h *RunsControlHandler) writeProviders(w http.ResponseWriter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	providers := h.state.Providers
	if providers == nil {
		providers = map[string]bool{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"providers":  providers,
		"updated_at": h.state.UpdatedAt,
		// Contract reminder for consumers: a key ABSENT from the map is
		// enabled; the map records explicit operator choices only.
		"default": "enabled",
	})
}
