package api

// dashboard_stats.go — the instant dashboard data plane.
//
// /api/v1/stats was the dashboard's hesitation: it reads the record store, the
// store is single-writer, and mid-ingest the reader queued behind the writer.
// boundedread.go capped that wait at 750 ms; capping is not the same as never
// paying it, and 750 ms on every panel is exactly the "hesitates" the owner
// named. The fix is to take the read off the request entirely: one background
// lane assembles the numbers on a 5 s cadence and publishes a pre-serialized
// $NDS FlatBuffer, and BOTH surfaces answer from RAM —
//
//   GET /api/v1/dashboard/stats  the cached frame, byte for byte
//   GET /api/v1/stats            the same cached numbers, marshalled to JSON
//                                for the existing consumers
//
// Neither handler touches the store. When the lane has never run (no store, or
// a node that has not warmed yet) the JSON surface falls back to the bounded
// read so a cold node still answers with something true.

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/status"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	// dashboardStatsLane is the snapshot-cache lane name.
	dashboardStatsLane = "stats"
	// dashboardStatsInterval is the rebuild cadence — the same 5 s the status
	// broadcaster ticks on, so the two frames arrive together on /ws/status.
	dashboardStatsInterval = 5 * time.Second
	// DashboardStatsPath is the instant binary read.
	DashboardStatsPath = "/api/v1/dashboard/stats"
	// dashboardStreamContentType marks the body as a raw size-prefixed
	// FlatBuffer frame, not JSON.
	dashboardStreamContentType = "application/vnd.sdn.flatbuffers.stream"
	// dashboardStreamFormat names the framing, matching the /ws/status frames:
	// a 4-byte little-endian length prefix ahead of the root buffer.
	dashboardStreamFormat = "flatsql-size-prefixed-le-u32"
)

// storeStats is the store-derived half of the stats payload — everything both
// the JSON and FlatBuffer surfaces render, and nothing that is host state.
type storeStats struct {
	Schemas      []storage.DataSchemaSummary
	Sources      []storage.SourceBatchProgress
	TotalRecords int64
	TotalBytes   int64
	// Stale is true when a read hit its budget and these are last-known-good.
	Stale bool
	// AsOf is when the numbers were last true; zero = never read.
	AsOf time.Time
	// Built is false before the lane has ever produced numbers.
	Built bool
}

// dashboardSnapshots owns the lane cache and the last assembled numbers. The
// numbers are kept beside the frame so the JSON surface renders the SAME
// snapshot the binary one does — one assembly, two encodings.
type dashboardSnapshots struct {
	cache *status.SnapshotCache

	mu   sync.RWMutex
	last storeStats
}

// StartDashboardSnapshots starts the background stats lane. Idempotent; a
// handler with no store starts nothing and the JSON surface keeps its bounded
// read. Call from node wiring, never from a request.
func (h *CoreAPIHandler) StartDashboardSnapshots() {
	if h == nil || h.store == nil || h.dashboard != nil {
		return
	}
	d := &dashboardSnapshots{}
	d.cache = status.NewSnapshotCache(status.LaneConfig{
		Name:     dashboardStatsLane,
		Interval: dashboardStatsInterval,
		Build: func() ([]byte, error) {
			s := h.readStoreStats()
			d.mu.Lock()
			d.last = s
			d.mu.Unlock()
			return status.BuildDashboardStatsSet(dashboardInputFrom(s)), nil
		},
	})
	h.dashboard = d
	d.cache.Start()
}

// StopDashboardSnapshots halts the lane. Idempotent.
func (h *CoreAPIHandler) StopDashboardSnapshots() {
	if h == nil || h.dashboard == nil {
		return
	}
	h.dashboard.cache.Stop()
}

// cachedStoreStats returns the last assembled numbers, or Built=false when the
// lane has never produced any.
func (h *CoreAPIHandler) cachedStoreStats() storeStats {
	if h == nil || h.dashboard == nil {
		return storeStats{}
	}
	h.dashboard.mu.RLock()
	defer h.dashboard.mu.RUnlock()
	return h.dashboard.last
}

// readStoreStats performs the two bounded store reads. It runs on the lane
// goroutine, and — only when the lane has never run — inline for one JSON
// request. It is never on the binary surface's path.
func (h *CoreAPIHandler) readStoreStats() storeStats {
	out := storeStats{Built: true}
	if h.store == nil {
		return out
	}

	// ONE budget for the whole snapshot: the two reads are independent and
	// wait together (see boundedread.go readAll).
	results := h.statsCache.readAll(storeReadBudget, storeReadMinRefresh,
		boundedRequest{Key: "summary", Load: func() (interface{}, error) {
			return h.store.DataSummary()
		}},
		boundedRequest{Key: "source_batch_progress", Load: func() (interface{}, error) {
			return h.store.SourceBatchProgress()
		}},
	)
	summaryRes := results["summary"]
	progressRes := results["source_batch_progress"]

	if summaryRes.OK {
		if summary, _ := summaryRes.Value.(*storage.DataSummary); summary != nil {
			out.Schemas = summary.Schemas
			out.TotalRecords = summary.TotalRecords
			out.TotalBytes = summary.TotalBytes
		}
		out.AsOf = summaryRes.AsOf
	}
	if !summaryRes.Fresh {
		out.Stale = true
	}

	if progressRes.OK {
		out.Sources, _ = progressRes.Value.([]storage.SourceBatchProgress)
	}
	if !progressRes.Fresh {
		out.Stale = true
	}
	if progressRes.OK && (out.AsOf.IsZero() || progressRes.AsOf.Before(out.AsOf)) {
		out.AsOf = progressRes.AsOf
	}
	return out
}

// dashboardInputFrom maps the store rows onto the $NDS transport rows.
func dashboardInputFrom(s storeStats) status.DashboardStatsInput {
	in := status.DashboardStatsInput{
		TotalRecords: s.TotalRecords,
		TotalBytes:   s.TotalBytes,
		Stale:        s.Stale,
		AsOf:         s.AsOf,
		Schemas:      make([]status.DashboardSchemaRow, 0, len(s.Schemas)),
		Sources:      make([]status.DashboardSourceRow, 0, len(s.Sources)),
	}
	for _, sc := range s.Schemas {
		in.Schemas = append(in.Schemas, status.DashboardSchemaRow{
			Schema:      sc.SchemaName,
			RecordCount: sc.Count,
			TotalBytes:  sc.TotalBytes,
		})
	}
	for _, p := range s.Sources {
		in.Sources = append(in.Sources, status.DashboardSourceRow{
			Schema:        p.SchemaName,
			ProviderID:    p.ProviderID,
			SourceName:    p.SourceName,
			BatchID:       p.BatchID,
			RecordCount:   p.Count,
			TotalBytes:    p.TotalBytes,
			FirstIngestAt: p.FirstSeenUnix,
			LastIngestAt:  p.LastSeenUnix,
			UpdatedAt:     p.UpdatedAtUnix,
		})
	}
	return in
}

// DashboardStatsFrame returns the cached $NDS frame and its generation, for the
// status broadcaster's ws push. ok is false until the lane has built once.
func (h *CoreAPIHandler) DashboardStatsFrame() (frame []byte, generation uint64, ok bool) {
	if h == nil || h.dashboard == nil {
		return nil, 0, false
	}
	snap, found := h.dashboard.cache.Frame(dashboardStatsLane)
	if !found || snap.Generation == 0 {
		return nil, 0, false
	}
	return snap.Frame, snap.Generation, true
}

// handleDashboardStats serves the cached frame verbatim. It runs no query of
// any kind: on a lane that has not built yet it says so with 503 rather than
// reaching for the store.
func (h *CoreAPIHandler) handleDashboardStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	frame, generation, ok := h.DashboardStatsFrame()
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeCoreAPIError(w, http.StatusServiceUnavailable, "SNAPSHOT_COLD",
			"dashboard stats snapshot has not been built yet")
		return
	}

	etag := `"nds-` + strconv.FormatUint(generation, 10) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-SDN-Stream-Format", dashboardStreamFormat)
	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", dashboardStreamContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(frame)
}
