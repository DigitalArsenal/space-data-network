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
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
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

	// statsCacheKeySummary and statsCacheKeySourceProgress are the two fixed
	// statsCache keys. They are constants because the persistence decode hook
	// keys the CONCRETE value type off them (boundedpersist.go): a renamed key
	// with a stale file must miss, never decode into the wrong type.
	statsCacheKeySummary        = "summary"
	statsCacheKeySourceProgress = "source_batch_progress"
	statsCacheKeyStorageUsage   = "storage_usage_bytes"

	// statsCacheFileName is the stats cache's backing file inside the UI cache
	// directory.
	statsCacheFileName = "stats.json"
)

// storeStats is the store-derived half of the stats payload — everything both
// the JSON and FlatBuffer surfaces render, and nothing that is host state.
type storeStats struct {
	Schemas      []storage.DataSchemaSummary
	Sources      []storage.SourceBatchProgress
	TotalRecords int64
	TotalBytes   int64
	// StorageBytes is the actual FlatSQL on-disk footprint used for the root
	// $NDS TOTAL_BYTES storage figure. TotalBytes above remains the live record
	// byte total used by the compatibility JSON response.
	StorageBytes      int64
	StorageBytesKnown bool
	// StorageFreeBytes and StorageCapacityBytes describe the filesystem
	// containing the store. Zero means statfs could not provide the value.
	StorageFreeBytes     int64
	StorageCapacityBytes int64
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
	// Frames outlive the process: Start loads the previous boot's frame before
	// the first background build, so /api/v1/dashboard/stats answers instantly
	// after a restart instead of SNAPSHOT_COLD for the hour the store hydrates.
	// The restored frame's own AS_OF states how old its numbers are.
	d.cache.SetPersistDir(h.uiCacheDir)
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
	out.StorageFreeBytes, out.StorageCapacityBytes = storeFilesystemBytes(h.store.Path())

	// ONE budget for the whole snapshot: the three reads are independent and
	// wait together (see boundedread.go readAll).
	results := h.statsCache.readAll(storeReadBudget, storeReadMinRefresh,
		boundedRequest{Key: statsCacheKeySummary, Load: func() (interface{}, error) {
			return h.store.DataSummary()
		}},
		boundedRequest{Key: statsCacheKeySourceProgress, Load: func() (interface{}, error) {
			return h.store.SourceBatchProgress()
		}},
		boundedRequest{Key: statsCacheKeyStorageUsage, Load: func() (interface{}, error) {
			return h.store.DiskUsageBytes()
		}},
	)
	summaryRes := results[statsCacheKeySummary]
	progressRes := results[statsCacheKeySourceProgress]
	storageRes := results[statsCacheKeyStorageUsage]

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
	if storageRes.OK {
		if used, ok := storageRes.Value.(int64); ok {
			out.StorageBytes = used
			out.StorageBytesKnown = true
		}
	}
	if !storageRes.Fresh {
		out.Stale = true
	}
	if storageRes.OK && (out.AsOf.IsZero() || storageRes.AsOf.Before(out.AsOf)) {
		out.AsOf = storageRes.AsOf
	}
	return out
}

// dashboardInputFrom maps the store rows onto the $NDS transport rows.
func dashboardInputFrom(s storeStats) status.DashboardStatsInput {
	rootTotalBytes := s.TotalBytes
	if s.StorageBytesKnown {
		rootTotalBytes = s.StorageBytes
	}
	in := status.DashboardStatsInput{
		TotalRecords:         s.TotalRecords,
		TotalBytes:           rootTotalBytes,
		StorageFreeBytes:     s.StorageFreeBytes,
		StorageCapacityBytes: s.StorageCapacityBytes,
		Stale:                s.Stale,
		AsOf:                 s.AsOf,
		Schemas:              make([]status.DashboardSchemaRow, 0, len(s.Schemas)),
		Sources:              make([]status.DashboardSourceRow, 0, len(s.Sources)),
	}
	for _, sc := range s.Schemas {
		in.Schemas = append(in.Schemas, status.DashboardSchemaRow{
			Schema:      sc.SchemaName,
			RecordCount: sc.Count,
			TotalBytes:  sc.TotalBytes,
		})
	}
	// TOPICS: the last minute's message observations per pubsub topic, from
	// the exchange handler (protocol.DefaultTopicActivity). Subscribed is true
	// for every topic listed — the node observes only topics it subscribes to.
	for _, obs := range protocol.DefaultTopicActivity.Snapshot(time.Now()) {
		in.Topics = append(in.Topics, status.DashboardTopicRow{
			Topic:             obs.Topic,
			Subscribed:        true,
			MessageTimestamps: obs.MessageTimestamps,
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

func storeFilesystemBytes(storePath string) (freeBytes, capacityBytes int64) {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		return 0, 0
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(storePath), &stat); err != nil || stat.Bsize <= 0 {
		return 0, 0
	}
	return statfsByteCount(uint64(stat.Bavail), uint64(stat.Bsize)),
		statfsByteCount(uint64(stat.Blocks), uint64(stat.Bsize))
}

func statfsByteCount(blocks, blockSize uint64) int64 {
	if blocks == 0 || blockSize == 0 || blocks > uint64(math.MaxInt64)/blockSize {
		return 0
	}
	return int64(blocks * blockSize)
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
