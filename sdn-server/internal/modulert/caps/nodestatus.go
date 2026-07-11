// Package caps (nodestatus.go): node_status_read — a read-only,
// policy-mediated snapshot of the HOST's own runtime state (uptime, record
// store totals, disk headroom, service/mode, libp2p bandwidth) for the
// SpaceAware NODE dashboard (wiring analysis M1).
//
// Operation naming note: internal/modulert/module.go's capPrefixFromName
// maps a handful of capability names to a shorter hostcall prefix (e.g.
// "p2p_read" -> "p2p", so p2p.go's operations are "p2p.peers_snapshot" etc).
// "node_status_read" has NO such override (module.go is out of scope for
// this change), so — mirroring the OTHER unmapped capabilities already in
// this codebase ("ipfs", "http", "pubsub": capPrefixFromName's default case
// returns the capability name unchanged) — its hostcall prefix is the
// capability name itself. The one operation is therefore:
//
//   - node_status_read.status — a full snapshot of the node's own runtime
//     state. There is no "anonymous" vs "operator" shaping here: the host
//     always answers with full detail; an anonymous-safe subset (if any)
//     is the FLOW manifest's job in cycle B (mirrors p2p.go's
//     materials-only convention — the host never makes response-shaping
//     decisions).
//
// Result shape (snake_case JSON keys are the wire contract):
//
//	{
//	  "uptime_seconds": int,      // seconds since the Node was constructed
//	  "started_at":     string,   // RFC3339 UTC
//	  "store": {
//	    "total_bytes":   int64,
//	    "total_records": int64,
//	    "storage_path":  string
//	  },
//	  "disk": {                   // null if the statfs probe is unavailable
//	    "capacity_bytes":  uint64,      // or errors — NEVER a fabricated 0
//	    "free_bytes":      uint64,
//	    "available_bytes": uint64
//	  } | null,
//	  "service": {
//	    "state":           "running", // the daemon can only honestly claim
//	                                  // this while it is up and answering
//	    "mode":             string,   // the node's configured mode
//	    "autostart_known":  false     // autostart is systemd/desktop-owned
//	                                  // and this host has NO surface into
//	                                  // that state — report false rather
//	                                  // than fabricate a boolean
//	  },
//	  "bandwidth": {               // null if no bandwidth counter is wired
//	    "total_in_bytes":  int64,
//	    "total_out_bytes": int64,
//	    "rate_in_bps":     float64,
//	    "rate_out_bps":    float64,
//	    "history": [               // OLDEST-FIRST/newest-last, ~5s cadence,
//	      {                        // up to 24 points (~2 minutes)
//	        "ts":              string, // RFC3339 UTC
//	        "total_in_bytes":  int64,
//	        "total_out_bytes": int64,
//	        "rate_in_bps":     float64,
//	        "rate_out_bps":    float64
//	      }
//	    ]
//	  } | null
//	}
//
// Deterministic for a fixed NodeStatusMaterials + clock (see
// nodestatus_test.go): no wall-clock reads outside of Now/StartedAt/
// DiskStat/BandwidthTotals/BandwidthHistory, all of which are supplied by
// the caller.
package caps

import (
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// DiskStat is a statfs(2) snapshot of the filesystem holding the record
// store's configured storage path.
type DiskStat struct {
	CapacityBytes  uint64
	FreeBytes      uint64
	AvailableBytes uint64
}

// BandwidthHistorySample is one point in the in-memory bandwidth sparkline
// ring (NodeStatusMaterials.BandwidthHistory / BandwidthHistoryRing).
type BandwidthHistorySample struct {
	At       time.Time
	TotalIn  int64
	TotalOut int64
	RateIn   float64
	RateOut  float64
}

// BandwidthHistoryRing is a fixed-capacity, race-safe ring buffer of
// BandwidthHistorySample. node.go's background sampler (~5s cadence,
// started/stopped with the Node, mirroring runStorageQuotaGC) periodically
// Adds a sample; node_status_read.status reads it back via Snapshot for the
// dashboard sparkline.
//
// Snapshot returns samples OLDEST-FIRST (append/insertion order, newest
// last) — the natural order for a left-to-right sparkline. This ordering is
// a deliberate choice, exercised by TestBandwidthHistoryRing* below.
type BandwidthHistoryRing struct {
	mu       sync.Mutex
	capacity int
	samples  []BandwidthHistorySample
}

// NewBandwidthHistoryRing creates a ring holding at most capacity samples.
// capacity <= 0 is treated as 1 (a ring must always hold at least the
// latest sample).
func NewBandwidthHistoryRing(capacity int) *BandwidthHistoryRing {
	if capacity <= 0 {
		capacity = 1
	}
	return &BandwidthHistoryRing{capacity: capacity, samples: make([]BandwidthHistorySample, 0, capacity)}
}

// Add appends a sample, evicting the oldest entry once capacity is
// exceeded. Safe for concurrent use (and safe to call on a nil ring, a
// no-op, so callers do not need to guard an unwired sampler).
func (r *BandwidthHistoryRing) Add(sample BandwidthHistorySample) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, sample)
	if over := len(r.samples) - r.capacity; over > 0 {
		// Drop the oldest `over` entries, keep the rest oldest-first.
		r.samples = append(r.samples[:0], r.samples[over:]...)
	}
}

// Snapshot returns a copy of the current ring contents, oldest-first. Safe
// to call on a nil ring (returns nil).
func (r *BandwidthHistoryRing) Snapshot() []BandwidthHistorySample {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BandwidthHistorySample, len(r.samples))
	copy(out, r.samples)
	return out
}

// NodeStatusMaterials wires the node's live services into the
// node_status_read capability as closures/values — the same
// dependency-injection style as caps/p2p.go's P2PCapOptions — so
// nodestatus_test.go can drive the handler with fakes, with no live
// Node/libp2p host/store required.
type NodeStatusMaterials struct {
	// StartedAt is the UTC timestamp the Node was constructed (captured
	// once, at node.New()).
	StartedAt time.Time
	// Now returns the current time; nil defaults to time.Now. Tests inject
	// a fixed clock so uptime_seconds is exact/deterministic.
	Now func() time.Time

	// Mode is the node's configured mode string ("full", "edge", ...).
	Mode string

	// StorageSummary returns the record store's total bytes/records — the
	// same DataSummary surface internal/api/data.go's handleSummary
	// serves. A nil func, or a returned error, reports zero totals rather
	// than failing the whole hostcall: the daemon is still "running" and
	// storage_path is still meaningful even with no/broken store (e.g.
	// edge mode has no store at all).
	StorageSummary func() (totalBytes int64, totalRecords int64, err error)
	// StoragePath is the node's configured record-store path (config
	// storage.path), independent of whether a store is actually open.
	StoragePath string

	// DiskStat statfs-probes StoragePath's filesystem. A nil func, or an
	// error, yields "disk": null — never a fabricated zero (a reported
	// 0-byte disk would actively mislead the dashboard). Injectable so
	// tests never touch a real filesystem; node.go wires in StatDisk.
	DiskStat func(path string) (DiskStat, error)

	// BandwidthTotals reports the libp2p metrics.Reporter cumulative
	// totals/rates (TotalIn, TotalOut, RateIn, RateOut, in that order).
	// ok=false — including a nil func — yields "bandwidth": null.
	BandwidthTotals func() (totalIn, totalOut int64, rateIn, rateOut float64, ok bool)
	// BandwidthHistory returns the in-memory sparkline ring contents,
	// oldest-first (node.go's background sampler, ~5s cadence, capacity
	// 24 -> ~2 minutes). nil, or a nil/empty result, yields an empty
	// history array — the "bandwidth" object itself still reports from
	// BandwidthTotals.
	BandwidthHistory func() []BandwidthHistorySample
}

// NewNodeStatusCapFactory builds the node_status_read capability handler
// factory. No HostBridge is needed (registered via CapabilityRegistry.
// Register, not RegisterBridgeAware): the result is pure JSON, no binary
// stream segments.
func NewNodeStatusCapFactory(materials NodeStatusMaterials) modulert.CapFactory {
	adapter := &nodeStatusCapAdapter{materials: materials}
	return func(_ *modulert.Module) modulert.CapHandler {
		return adapter.handle
	}
}

type nodeStatusCapAdapter struct {
	materials NodeStatusMaterials
}

func (a *nodeStatusCapAdapter) handle(operation string, _ []byte) ([]byte, error) {
	switch operation {
	case "node_status_read.status":
		return a.status(), nil
	default:
		return errCapJSON("unknown node_status_read operation: " + operation), nil
	}
}

func (a *nodeStatusCapAdapter) now() time.Time {
	if a.materials.Now != nil {
		return a.materials.Now()
	}
	return time.Now()
}

func (a *nodeStatusCapAdapter) status() []byte {
	now := a.now().UTC()
	var uptimeSeconds int64
	if !a.materials.StartedAt.IsZero() {
		if d := now.Sub(a.materials.StartedAt.UTC()); d > 0 {
			uptimeSeconds = int64(d.Seconds())
		}
	}

	var totalBytes, totalRecords int64
	if a.materials.StorageSummary != nil {
		if b, r, err := a.materials.StorageSummary(); err == nil {
			totalBytes, totalRecords = b, r
		}
	}

	result := map[string]interface{}{
		"uptime_seconds": uptimeSeconds,
		"started_at":     a.materials.StartedAt.UTC().Format(time.RFC3339),
		"store": map[string]interface{}{
			"total_bytes":   totalBytes,
			"total_records": totalRecords,
			"storage_path":  a.materials.StoragePath,
		},
		"disk": nodeStatusDiskJSON(a.materials.DiskStat, a.materials.StoragePath),
		"service": map[string]interface{}{
			"state":           "running",
			"mode":            a.materials.Mode,
			"autostart_known": false,
		},
		"bandwidth": nodeStatusBandwidthJSON(a.materials.BandwidthTotals, a.materials.BandwidthHistory),
	}
	return modulert.PreEncodedEnvelope(map[string]interface{}{"ok": true, "result": result}, nil)
}

func nodeStatusDiskJSON(statFn func(string) (DiskStat, error), path string) interface{} {
	if statFn == nil {
		return nil
	}
	stat, err := statFn(path)
	if err != nil {
		return nil
	}
	return map[string]interface{}{
		"capacity_bytes":  stat.CapacityBytes,
		"free_bytes":      stat.FreeBytes,
		"available_bytes": stat.AvailableBytes,
	}
}

func nodeStatusBandwidthJSON(totalsFn func() (int64, int64, float64, float64, bool), historyFn func() []BandwidthHistorySample) interface{} {
	if totalsFn == nil {
		return nil
	}
	totalIn, totalOut, rateIn, rateOut, ok := totalsFn()
	if !ok {
		return nil
	}
	var history []BandwidthHistorySample
	if historyFn != nil {
		history = historyFn()
	}
	historyOut := make([]map[string]interface{}, 0, len(history))
	for _, sample := range history {
		historyOut = append(historyOut, map[string]interface{}{
			"ts":              sample.At.UTC().Format(time.RFC3339),
			"total_in_bytes":  sample.TotalIn,
			"total_out_bytes": sample.TotalOut,
			"rate_in_bps":     sample.RateIn,
			"rate_out_bps":    sample.RateOut,
		})
	}
	return map[string]interface{}{
		"total_in_bytes":  totalIn,
		"total_out_bytes": totalOut,
		"rate_in_bps":     rateIn,
		"rate_out_bps":    rateOut,
		"history":         historyOut,
	}
}

// StatDisk is the production DiskStat implementation: a statfs(2) probe of
// the filesystem holding path. darwin + linux only (golang.org/x/sys/unix
// is already a direct module dependency; the `unix` package does not build
// for windows — out of scope for this cycle, see the task's "darwin+linux
// compatible" instruction). node.go wires this in as
// NodeStatusMaterials.DiskStat; tests inject their own fake instead of
// calling this.
func StatDisk(path string) (DiskStat, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return DiskStat{}, err
	}
	bsize := uint64(stat.Bsize)
	return DiskStat{
		CapacityBytes:  stat.Blocks * bsize,
		FreeBytes:      stat.Bfree * bsize,
		AvailableBytes: stat.Bavail * bsize,
	}, nil
}
