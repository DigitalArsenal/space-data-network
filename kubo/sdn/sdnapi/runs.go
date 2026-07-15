package sdnapi

// Supplemental-OMM RUN API — the read-only board surface over the node's OD-fit
// run history. It is a SEPARATE handler from the core NewHandler surface (like the
// credentials admin handler): it owns the /sdn/v1/runs subtree and is mounted on
// the same loopback listener by the kubo plugin. Every route is GET.
//
//	GET /sdn/v1/runs                                   list runs (+ the live run)
//	GET /sdn/v1/runs/{id}                              one run: summary + per-object stats
//	GET /sdn/v1/runs/{id}/objects?search=<NORAD>       searchable per-object rows
//	GET /sdn/v1/runs/{id}/objects/{norad}/download?format=tle|omm|cdm
//	                                                   VCM-format element download
//
// The reader is resolved per request (nil until the runtime is up), so the
// listener may start before the run engine exists and still report empty.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ipfs/kubo/sdn/sdnruns"
)

// RunsReader is the read surface over the supplemental-OMM run store.
// *sdnruns.Store satisfies it.
type RunsReader interface {
	List() []sdnruns.Summary
	Get(id string) (sdnruns.Run, error)
	Objects(id, search string) ([]sdnruns.ObjectResult, error)
	Object(id string, norad uint32) (sdnruns.ObjectResult, error)
	Live() (sdnruns.LiveRun, bool)
}

// RunsDeps are the live sources the run API reads.
type RunsDeps struct {
	// Reader returns the live run store, or nil when the run engine is not up yet
	// (the routes then report empty/unavailable rather than crashing).
	Reader func() RunsReader
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
	mux.HandleFunc("GET /sdn/v1/runs/{id}/objects", h.objects)
	mux.HandleFunc("GET /sdn/v1/runs/{id}/objects/{norad}/download", h.download)
	return mux
}

func (h *runsHandler) reader() RunsReader {
	if h.deps.Reader == nil {
		return nil
	}
	return h.deps.Reader()
}

// runsListResp is the GET /sdn/v1/runs body: every run's summary plus the live
// (currently executing) run, so the board can render progress at a glance.
type runsListResp struct {
	Runs []sdnruns.Summary `json:"runs"`
	Live *sdnruns.LiveRun  `json:"live"`
}

func (h *runsHandler) list(w http.ResponseWriter, _ *http.Request) {
	resp := runsListResp{Runs: []sdnruns.Summary{}}
	if rd := h.reader(); rd != nil {
		if list := rd.List(); list != nil {
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
		writeErr(w, http.StatusServiceUnavailable, "run engine unavailable")
		return
	}
	run, err := rd.Get(id)
	if errors.Is(err, sdnruns.ErrRunNotFound) {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// objectsResp is the GET /sdn/v1/runs/{id}/objects body.
type objectsResp struct {
	RunID   string                 `json:"run_id"`
	Search  string                 `json:"search,omitempty"`
	Total   int                    `json:"total"`
	Objects []sdnruns.ObjectResult `json:"objects"`
}

func (h *runsHandler) objects(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "run id is required")
		return
	}
	rd := h.reader()
	if rd == nil {
		writeErr(w, http.StatusServiceUnavailable, "run engine unavailable")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	objs, err := rd.Objects(id, search)
	if errors.Is(err, sdnruns.ErrRunNotFound) {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if objs == nil {
		objs = []sdnruns.ObjectResult{}
	}
	writeJSON(w, http.StatusOK, objectsResp{RunID: id, Search: search, Total: len(objs), Objects: objs})
}

func (h *runsHandler) download(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "run id is required")
		return
	}
	noradStr := strings.TrimSpace(r.PathValue("norad"))
	norad64, err := strconv.ParseUint(noradStr, 10, 32)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "norad must be a positive integer")
		return
	}
	rd := h.reader()
	if rd == nil {
		writeErr(w, http.StatusServiceUnavailable, "run engine unavailable")
		return
	}
	obj, err := rd.Object(id, uint32(norad64))
	if errors.Is(err, sdnruns.ErrRunNotFound) {
		writeErr(w, http.StatusNotFound, "run or object not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	format := r.URL.Query().Get("format")
	body, contentType, filename, ok := sdnruns.RenderElements(obj, format)
	if !ok {
		writeErr(w, http.StatusBadRequest, "format must be one of tle, omm, cdm")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}
