package sdnapi

// Supplemental-OMM RUN API — the read-only board surface over the node's
// REAL OD-fit results, derived from the mounted OD ServiceFlow's fire history
// + its linked FlatSQL store (sdn/sdnodresults). It is a SEPARATE handler
// from the core NewHandler surface (like the credentials admin handler): it
// owns the /sdn/v1/runs subtree and is mounted on the same loopback listener
// by the kubo plugin. Every route is GET.
//
//	GET /sdn/v1/runs                                        run log (+ the live run)
//	GET /sdn/v1/runs/{id}                                   one run's single-row stats
//	GET /sdn/v1/runs/{id}/providers                          LEVEL 1: declared providers
//	GET /sdn/v1/runs/{id}/objects?search=<text>              LEVEL 2: searchable objects
//	GET /sdn/v1/runs/{id}/download?cid=<cid>                 one record, exact stored bytes
//
// This REPLACES the disconnected, inert sdnruns.Store as the run log's data
// source (the pre-existing Go-orchestration run engine, made fully inert per
// the SDN_OD_FLOW_LOOP.md STOP block — see plugin/plugins/sdnruntime/sdnruns.go
// — its 82 historical rows are stale and no longer reflect what the WASM
// engine actually stores; derived runs from the flow's OWN fire history are
// the run log going forward). The reader is resolved per request (nil before
// the runtime is up, or on a node with no OD flow mounted), so the listener
// may start before the run engine exists and still report an honest empty
// result — never a crash, never an invented row.

import (
	"net/http"
	"strings"

	"github.com/ipfs/kubo/sdn/sdnodresults"
)

// RunsDeps are the live sources the run API reads.
type RunsDeps struct {
	// Reader returns the live OD-results reader, or nil when the OD flow is
	// not mounted / the runtime is not up yet (the routes then report empty
	// rather than crashing — the honest fallback for a node with no OD flow).
	Reader func() *sdnodresults.Reader
}

type runsHandler struct {
	deps RunsDeps
}

// NewRunsHandler builds the supplemental-OMM run API. The returned handler owns
// the /sdn/v1/runs subtree; mount it on the plugin's loopback listener.
func NewRunsHandler(deps RunsDeps) http.Handler {
	h := &runsHandler{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sdn/v1/runs", h.list)
	mux.HandleFunc("GET /sdn/v1/runs/{id}", h.get)
	mux.HandleFunc("GET /sdn/v1/runs/{id}/providers", h.providers)
	mux.HandleFunc("GET /sdn/v1/runs/{id}/objects", h.objects)
	mux.HandleFunc("GET /sdn/v1/runs/{id}/download", h.download)
	return mux
}

func (h *runsHandler) reader() *sdnodresults.Reader {
	if h.deps.Reader == nil {
		return nil
	}
	return h.deps.Reader()
}

// runsListResp is the GET /sdn/v1/runs body: every run's summary plus the live
// (currently executing) run, so the board can render progress at a glance.
type runsListResp struct {
	Runs []sdnodresults.RunSummary `json:"runs"`
	Live *sdnodresults.LiveRun     `json:"live"`
}

func (h *runsHandler) list(w http.ResponseWriter, _ *http.Request) {
	resp := runsListResp{Runs: []sdnodresults.RunSummary{}}
	if rd := h.reader(); rd != nil {
		if list := rd.Runs(); list != nil {
			resp.Runs = list
		}
		if live, ok := rd.Live(); ok {
			resp.Live = &live
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *runsHandler) get(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "run id is required")
		return
	}
	rd := h.reader()
	if rd == nil {
		writeErr(w, http.StatusServiceUnavailable, "the OD flow is not mounted on this node")
		return
	}
	run, ok := rd.Run(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// providersResp is the GET /sdn/v1/runs/{id}/providers body — LEVEL 1 of the
// drill-down: one row per provider the flow DECLARES, honestly flagged when
// its per-provider stats are not yet attributable (see sdnodresults' doc).
type providersResp struct {
	RunID     string                      `json:"run_id"`
	Providers []sdnodresults.ProviderStat `json:"providers"`
}

func (h *runsHandler) providers(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "run id is required")
		return
	}
	rd := h.reader()
	if rd == nil {
		writeErr(w, http.StatusServiceUnavailable, "the OD flow is not mounted on this node")
		return
	}
	providers, ok := rd.RunProviders(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	if providers == nil {
		providers = []sdnodresults.ProviderStat{}
	}
	writeJSON(w, http.StatusOK, providersResp{RunID: id, Providers: providers})
}

// objectsResp is the GET /sdn/v1/runs/{id}/objects body — LEVEL 2 of the
// drill-down: real per-object rows, searchable by norad/name/object id.
type objectsResp struct {
	RunID   string                   `json:"run_id"`
	Search  string                   `json:"search,omitempty"`
	Total   int                      `json:"total"`
	Objects []sdnodresults.ObjectRow `json:"objects"`
}

func (h *runsHandler) objects(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "run id is required")
		return
	}
	rd := h.reader()
	if rd == nil {
		writeErr(w, http.StatusServiceUnavailable, "the OD flow is not mounted on this node")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	objs, ok := rd.RunObjects(id, search)
	if !ok {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	if objs == nil {
		objs = []sdnodresults.ObjectRow{}
	}
	writeJSON(w, http.StatusOK, objectsResp{RunID: id, Search: search, Total: len(objs), Objects: objs})
}

// download serves ONE record's exact stored bytes by its content-addressed
// cid (?cid=<cid>) — the canonical, byte-for-byte downloadable form. The run
// id in the path scopes the download to routes the board already renders
// per-run, but the lookup itself is by cid across the whole store (a cid is
// globally content-addressed; this keeps the endpoint honest rather than
// silently 404ing a valid record because of a run-boundary mismatch).
func (h *runsHandler) download(w http.ResponseWriter, r *http.Request) {
	cid := strings.TrimSpace(r.URL.Query().Get("cid"))
	if cid == "" {
		writeErr(w, http.StatusBadRequest, "cid query parameter is required")
		return
	}
	rd := h.reader()
	if rd == nil {
		writeErr(w, http.StatusServiceUnavailable, "the OD flow is not mounted on this node")
		return
	}
	data, table, ok := rd.DownloadRecord(cid)
	if !ok {
		writeErr(w, http.StatusNotFound, "record not found for that cid")
		return
	}
	sdsType, ext := "", "bin"
	switch table {
	case "sds_omm":
		sdsType, ext = "OMM", "omm.fb"
	case "sds_ocm":
		sdsType, ext = "OCM", "ocm.fb"
	case "sds_obd":
		sdsType, ext = "OBD", "obd.fb"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-SDS-Type", sdsType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+cid+"."+ext+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
