package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/NDS"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/status"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// laneWith wires a handler to a snapshot lane serving a fixed set of numbers,
// with no store behind it — the point of the lane is that the request path
// never reaches one.
func laneWith(t *testing.T, s storeStats) *CoreAPIHandler {
	t.Helper()
	h := &CoreAPIHandler{}
	d := &dashboardSnapshots{last: s}
	d.cache = status.NewSnapshotCache(status.LaneConfig{
		Name:     dashboardStatsLane,
		Interval: time.Hour,
		Build: func() ([]byte, error) {
			return status.BuildDashboardStatsSet(dashboardInputFrom(s)), nil
		},
	})
	h.dashboard = d
	if err := d.cache.Refresh(dashboardStatsLane); err != nil {
		t.Fatalf("lane refresh: %v", err)
	}
	return h
}

func sampleStats() storeStats {
	return storeStats{
		Built:        true,
		TotalRecords: 10847,
		TotalBytes:   4200000,
		AsOf:         time.Unix(1756000000, 0),
		Schemas: []storage.DataSchemaSummary{
			{SchemaName: "OMM", Count: 10847, TotalBytes: 4200000},
		},
		Sources: []storage.SourceBatchProgress{{
			SchemaName: "OMM", ProviderID: "celestrak", SourceName: "gp",
			BatchID: "b-1", Count: 10847, TotalBytes: 4200000,
			FirstSeenUnix: 1755999000, LastSeenUnix: 1756000000, UpdatedAtUnix: 1756000000,
		}},
	}
}

func TestHandleDashboardStatsServesCachedFrame(t *testing.T) {
	h := laneWith(t, sampleStats())

	rec := httptest.NewRecorder()
	h.handleDashboardStats(rec, httptest.NewRequest(http.MethodGet, DashboardStatsPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != dashboardStreamContentType {
		t.Errorf("Content-Type = %q, want %q", got, dashboardStreamContentType)
	}
	if got := rec.Header().Get("X-SDN-Stream-Format"); got != dashboardStreamFormat {
		t.Errorf("X-SDN-Stream-Format = %q, want %q", got, dashboardStreamFormat)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	frame, _, ok := h.DashboardStatsFrame()
	if !ok {
		t.Fatal("lane reported no frame")
	}
	if got := rec.Body.Bytes(); string(got) != string(frame) {
		t.Errorf("body is not the cached frame verbatim (%d vs %d bytes)", len(got), len(frame))
	}

	// If-None-Match on the same generation is a 304 with no body.
	req := httptest.NewRequest(http.MethodGet, DashboardStatsPath, nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.handleDashboardStats(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 body = %d bytes, want 0", rec.Body.Len())
	}
}

func TestDashboardStatsFrameUsesPhysicalStorageBytes(t *testing.T) {
	s := sampleStats()
	s.StorageBytes = 9_900_000
	s.StorageBytesKnown = true
	h := laneWith(t, s)
	frame, _, ok := h.DashboardStatsFrame()
	if !ok || !NDS.SizePrefixedNDSBufferHasIdentifier(frame) {
		t.Fatal("dashboard frame is not a size-prefixed $NDS")
	}
	root := NDS.GetSizePrefixedRootAsNDS(frame, 0)
	if got := root.TOTAL_BYTES(); got != s.StorageBytes {
		t.Fatalf("NDS TOTAL_BYTES = %d, want physical usage %d", got, s.StorageBytes)
	}
	if got := root.TOTAL_RECORDS(); got != s.TotalRecords {
		t.Fatalf("NDS TOTAL_RECORDS = %d, want %d", got, s.TotalRecords)
	}
}

func TestReadStoreStatsMeasuresFlatSQLDiskUsage(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &CoreAPIHandler{store: store, statsCache: newBoundedReader(8)}
	stats := h.readStoreStats()
	want, err := store.DiskUsageBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.StorageBytesKnown || stats.StorageBytes != want {
		t.Fatalf("storage usage = %d known=%v, want %d", stats.StorageBytes, stats.StorageBytesKnown, want)
	}
}

func TestHandleDashboardStatsColdLane(t *testing.T) {
	h := &CoreAPIHandler{}
	rec := httptest.NewRecorder()
	h.handleDashboardStats(rec, httptest.NewRequest(http.MethodGet, DashboardStatsPath, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 before the lane has built", rec.Code)
	}
}

func TestHandleStatsServesLaneNumbers(t *testing.T) {
	// No store at all: any number that appears in the response came from the
	// lane, which proves the request path ran no query.
	h := laneWith(t, sampleStats())

	rec := httptest.NewRecorder()
	h.handleStats(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		TotalRecords int64 `json:"total_records"`
		TotalBytes   int64 `json:"total_bytes"`
		Stale        bool  `json:"stale"`
		AsOf         string
		Schemas      []struct {
			Schema string `json:"schema"`
			Count  int64  `json:"count"`
		} `json:"schemas"`
		Sources []struct {
			ProviderID string `json:"provider_id"`
			LastSeen   string `json:"last_seen"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TotalRecords != 10847 || body.TotalBytes != 4200000 {
		t.Errorf("totals = %d/%d, want 10847/4200000", body.TotalRecords, body.TotalBytes)
	}
	if body.Stale {
		t.Error("stale = true, want false")
	}
	if len(body.Schemas) != 1 || body.Schemas[0].Schema != "OMM" || body.Schemas[0].Count != 10847 {
		t.Errorf("schemas = %+v", body.Schemas)
	}
	if len(body.Sources) != 1 || body.Sources[0].ProviderID != "celestrak" || body.Sources[0].LastSeen == "" {
		t.Errorf("sources = %+v", body.Sources)
	}
}

func TestHandleStatsReportsStaleNotZero(t *testing.T) {
	s := sampleStats()
	s.Stale = true
	h := laneWith(t, s)

	rec := httptest.NewRecorder()
	h.handleStats(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil))

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["stale"] != true {
		t.Errorf("stale = %v, want true", body["stale"])
	}
	// A stale snapshot still carries the last-known-good numbers; reporting a
	// confident zero is the failure mode this lane exists to prevent.
	if got, _ := body["total_records"].(float64); got != 10847 {
		t.Errorf("total_records = %v, want 10847 alongside stale=true", body["total_records"])
	}
}
