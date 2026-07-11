package caps

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// ---------------------------------------------------------------------------
// node_status_read.status
// ---------------------------------------------------------------------------

var (
	nodeStatusFixedNow       = time.Date(2026, 7, 11, 12, 0, 30, 0, time.UTC)
	nodeStatusFixedStartedAt = time.Date(2026, 7, 11, 11, 58, 0, 0, time.UTC) // uptime = 150s
)

func nodeStatusTestMaterials() NodeStatusMaterials {
	history := []BandwidthHistorySample{
		{At: nodeStatusFixedNow.Add(-10 * time.Second), TotalIn: 900, TotalOut: 1800, RateIn: 10, RateOut: 30},
		{At: nodeStatusFixedNow.Add(-5 * time.Second), TotalIn: 1000, TotalOut: 2000, RateIn: 12.5, RateOut: 34.5},
	}
	return NodeStatusMaterials{
		StartedAt: nodeStatusFixedStartedAt,
		Now:       func() time.Time { return nodeStatusFixedNow },
		Mode:      "full",
		StorageSummary: func() (int64, int64, error) {
			return 123456789, 4242, nil
		},
		StoragePath: "/var/lib/sdn/storage",
		DiskStat: func(path string) (DiskStat, error) {
			if path != "/var/lib/sdn/storage" {
				return DiskStat{}, os.ErrNotExist
			}
			return DiskStat{CapacityBytes: 500000000000, FreeBytes: 100000000000, AvailableBytes: 90000000000}, nil
		},
		BandwidthTotals: func() (int64, int64, float64, float64, bool) {
			return 1000, 2000, 12.5, 34.5, true
		},
		BandwidthHistory: func() []BandwidthHistorySample { return history },
	}
}

// invokeNodeStatus drives the node_status_read capability handler directly
// (no HostBridge needed — the factory is a plain modulert.CapFactory).
func invokeNodeStatus(t *testing.T, materials NodeStatusMaterials, op string) []byte {
	t.Helper()
	factory := NewNodeStatusCapFactory(materials)
	handler := factory(&modulert.Module{})
	response, err := handler(op, nil)
	if err != nil {
		t.Fatalf("%s: unexpected transport error: %v", op, err)
	}
	return response
}

func TestNodeStatusFullSnapshot(t *testing.T) {
	response := invokeNodeStatus(t, nodeStatusTestMaterials(), "node_status_read.status")
	meta, segments := decodePreEncodedEnvelope(t, response)
	if len(segments) != 0 {
		t.Fatalf("node_status_read.status must carry no binary segments, got %d", len(segments))
	}
	result := resultOf(t, meta)

	if result["uptime_seconds"] != float64(150) {
		t.Fatalf("uptime_seconds = %v, want 150", result["uptime_seconds"])
	}
	if result["started_at"] != "2026-07-11T11:58:00Z" {
		t.Fatalf("started_at = %v", result["started_at"])
	}

	store := result["store"].(map[string]interface{})
	if store["total_bytes"] != float64(123456789) || store["total_records"] != float64(4242) {
		t.Fatalf("store totals: %v", store)
	}
	if store["storage_path"] != "/var/lib/sdn/storage" {
		t.Fatalf("store.storage_path: %v", store)
	}

	disk := result["disk"].(map[string]interface{})
	if disk["capacity_bytes"] != float64(500000000000) ||
		disk["free_bytes"] != float64(100000000000) ||
		disk["available_bytes"] != float64(90000000000) {
		t.Fatalf("disk: %v", disk)
	}

	service := result["service"].(map[string]interface{})
	if service["state"] != "running" || service["mode"] != "full" || service["autostart_known"] != false {
		t.Fatalf("service: %v", service)
	}

	bandwidth := result["bandwidth"].(map[string]interface{})
	if bandwidth["total_in_bytes"] != float64(1000) || bandwidth["total_out_bytes"] != float64(2000) ||
		bandwidth["rate_in_bps"] != float64(12.5) || bandwidth["rate_out_bps"] != float64(34.5) {
		t.Fatalf("bandwidth totals: %v", bandwidth)
	}
	history := bandwidth["history"].([]interface{})
	if len(history) != 2 {
		t.Fatalf("bandwidth.history length = %d, want 2", len(history))
	}
	first := history[0].(map[string]interface{})
	if first["ts"] != "2026-07-11T12:00:20Z" || first["total_in_bytes"] != float64(900) {
		t.Fatalf("history[0] (oldest-first order): %v", first)
	}
	second := history[1].(map[string]interface{})
	if second["ts"] != "2026-07-11T12:00:25Z" || second["total_in_bytes"] != float64(1000) {
		t.Fatalf("history[1]: %v", second)
	}
}

func TestNodeStatusDeterminism(t *testing.T) {
	materials := nodeStatusTestMaterials()
	first := invokeNodeStatus(t, materials, "node_status_read.status")
	second := invokeNodeStatus(t, materials, "node_status_read.status")
	if string(first) != string(second) {
		t.Fatalf("node_status_read.status is not deterministic for fixed materials:\n%s\n!=\n%s", first, second)
	}
}

func TestNodeStatusDiskNilOnStatfsFailure(t *testing.T) {
	materials := nodeStatusTestMaterials()
	materials.DiskStat = func(string) (DiskStat, error) { return DiskStat{}, os.ErrPermission }
	response := invokeNodeStatus(t, materials, "node_status_read.status")
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["disk"] != nil {
		t.Fatalf("disk should be null on statfs failure, got %v", result["disk"])
	}
}

func TestNodeStatusDiskNilWhenUnwired(t *testing.T) {
	materials := nodeStatusTestMaterials()
	materials.DiskStat = nil
	response := invokeNodeStatus(t, materials, "node_status_read.status")
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["disk"] != nil {
		t.Fatalf("disk should be null when no statfs func is wired, got %v", result["disk"])
	}
}

func TestNodeStatusBandwidthNilWhenReporterUnwired(t *testing.T) {
	materials := nodeStatusTestMaterials()
	materials.BandwidthTotals = nil
	response := invokeNodeStatus(t, materials, "node_status_read.status")
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["bandwidth"] != nil {
		t.Fatalf("bandwidth should be null when no reporter is wired, got %v", result["bandwidth"])
	}
}

func TestNodeStatusBandwidthNilWhenTotalsReportNotOK(t *testing.T) {
	materials := nodeStatusTestMaterials()
	materials.BandwidthTotals = func() (int64, int64, float64, float64, bool) {
		return 0, 0, 0, 0, false
	}
	response := invokeNodeStatus(t, materials, "node_status_read.status")
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["bandwidth"] != nil {
		t.Fatalf("bandwidth should be null when totals report ok=false, got %v", result["bandwidth"])
	}
}

func TestNodeStatusBandwidthEmptyHistoryWhenUnwired(t *testing.T) {
	materials := nodeStatusTestMaterials()
	materials.BandwidthHistory = nil
	response := invokeNodeStatus(t, materials, "node_status_read.status")
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	bandwidth := result["bandwidth"].(map[string]interface{})
	history, ok := bandwidth["history"].([]interface{})
	if !ok || len(history) != 0 {
		t.Fatalf("bandwidth.history should be an empty array when unwired, got %v", bandwidth["history"])
	}
}

func TestNodeStatusStoreZeroOnSummaryError(t *testing.T) {
	materials := nodeStatusTestMaterials()
	materials.StorageSummary = func() (int64, int64, error) {
		return 999, 999, os.ErrClosed
	}
	response := invokeNodeStatus(t, materials, "node_status_read.status")
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	store := result["store"].(map[string]interface{})
	if store["total_bytes"] != float64(0) || store["total_records"] != float64(0) {
		t.Fatalf("store totals should be zero on summary error, got %v", store)
	}
	// storage_path is independent of the store summary and always reported.
	if store["storage_path"] != "/var/lib/sdn/storage" {
		t.Fatalf("storage_path: %v", store["storage_path"])
	}
}

func TestNodeStatusStoreZeroWhenSummaryUnwired(t *testing.T) {
	materials := nodeStatusTestMaterials()
	materials.StorageSummary = nil
	response := invokeNodeStatus(t, materials, "node_status_read.status")
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	store := result["store"].(map[string]interface{})
	if store["total_bytes"] != float64(0) || store["total_records"] != float64(0) {
		t.Fatalf("store totals should be zero when unwired, got %v", store)
	}
}

func TestNodeStatusUptimeZeroWhenStartedAtZero(t *testing.T) {
	materials := nodeStatusTestMaterials()
	materials.StartedAt = time.Time{}
	response := invokeNodeStatus(t, materials, "node_status_read.status")
	meta, _ := decodePreEncodedEnvelope(t, response)
	result := resultOf(t, meta)
	if result["uptime_seconds"] != float64(0) {
		t.Fatalf("uptime_seconds = %v, want 0 for a zero StartedAt", result["uptime_seconds"])
	}
}

func TestNodeStatusUnknownOperation(t *testing.T) {
	factory := NewNodeStatusCapFactory(nodeStatusTestMaterials())
	handler := factory(&modulert.Module{})
	response, err := handler("node_status_read.reboot", nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(response, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("unknown op must fail: %v", meta)
	}
}

// ---------------------------------------------------------------------------
// BandwidthHistoryRing
// ---------------------------------------------------------------------------

func sampleAt(n int) BandwidthHistorySample {
	return BandwidthHistorySample{
		At:      time.Unix(int64(n), 0).UTC(),
		TotalIn: int64(n),
	}
}

func TestBandwidthHistoryRingOrderWithinCapacity(t *testing.T) {
	r := NewBandwidthHistoryRing(5)
	for i := 1; i <= 3; i++ {
		r.Add(sampleAt(i))
	}
	got := r.Snapshot()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, sample := range got {
		if sample.TotalIn != int64(i+1) {
			t.Fatalf("sample %d = %+v, want oldest-first order starting at 1", i, sample)
		}
	}
}

func TestBandwidthHistoryRingOverflowDropsOldest(t *testing.T) {
	r := NewBandwidthHistoryRing(3)
	for i := 1; i <= 5; i++ {
		r.Add(sampleAt(i))
	}
	got := r.Snapshot()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (capacity)", len(got))
	}
	want := []int64{3, 4, 5} // oldest-first: the two oldest (1, 2) were evicted.
	for i, sample := range got {
		if sample.TotalIn != want[i] {
			t.Fatalf("sample %d TotalIn = %d, want %d (oldest-first, post-overflow)", i, sample.TotalIn, want[i])
		}
	}
}

func TestBandwidthHistoryRingZeroCapacityTreatedAsOne(t *testing.T) {
	r := NewBandwidthHistoryRing(0)
	r.Add(sampleAt(1))
	r.Add(sampleAt(2))
	got := r.Snapshot()
	if len(got) != 1 || got[0].TotalIn != 2 {
		t.Fatalf("got = %+v, want exactly the newest sample", got)
	}
}

func TestBandwidthHistoryRingSnapshotIsACopy(t *testing.T) {
	r := NewBandwidthHistoryRing(4)
	r.Add(sampleAt(1))
	snap := r.Snapshot()
	snap[0].TotalIn = 999
	if r.Snapshot()[0].TotalIn != 1 {
		t.Fatalf("Snapshot must return an independent copy, mutation leaked into the ring")
	}
}

func TestBandwidthHistoryRingNilSafe(t *testing.T) {
	var r *BandwidthHistoryRing
	r.Add(sampleAt(1)) // must not panic
	if got := r.Snapshot(); got != nil {
		t.Fatalf("nil ring Snapshot() = %v, want nil", got)
	}
}

func TestBandwidthHistoryRingConcurrentAdd(t *testing.T) {
	r := NewBandwidthHistoryRing(24)
	var wg sync.WaitGroup
	for i := 1; i <= 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.Add(sampleAt(n))
		}(i)
	}
	wg.Wait()
	got := r.Snapshot()
	if len(got) != 24 {
		t.Fatalf("len = %d, want 24 (capacity) after concurrent overflow", len(got))
	}
}

// ---------------------------------------------------------------------------
// StatDisk (production implementation)
// ---------------------------------------------------------------------------

func TestStatDiskRealFilesystem(t *testing.T) {
	stat, err := StatDisk(os.TempDir())
	if err != nil {
		t.Fatalf("StatDisk(%q): %v", os.TempDir(), err)
	}
	if stat.CapacityBytes == 0 {
		t.Fatalf("StatDisk capacity_bytes = 0, want > 0")
	}
}

func TestStatDiskMissingPath(t *testing.T) {
	if _, err := StatDisk("/does/not/exist/sdn-nodestatus-test"); err == nil {
		t.Fatalf("expected an error statfs-ing a nonexistent path")
	}
}
