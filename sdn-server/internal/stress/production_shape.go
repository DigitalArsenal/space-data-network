//go:build stress
// +build stress

package stress

// PRODUCTION-SHAPE STRESS FOR THE FLATSQL RECORD STORE.
//
// The shape below is host-02's (space-data-network-02, the celestrak.eth
// retriever) as read from its own /api/v1/data/summary and journal on
// 2026-09-27: 4,025,343 records, 1,492,605,924 record bytes. It is built
// through the store's real write path (StoreBatchWithSourceTags), so the
// producer tables, index, source tags, summaries and engine hot window are the
// ones the daemon writes, not a fixture that skips them.
//
// The one number host-02 does not expose read-only is the row count of the
// second $IQC producer table (sds_p_<host-01 peer>__IQC, the mirror of the
// records host-01 replicated back). The summary counts source lanes, not
// producer tables. STRESS_SHAPE_IQC_MIRROR_FRACTION sets it; the default 1.0
// mirrors every local $IQC record, the replication of the whole published batch.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	logging "github.com/ipfs/go-log/v2"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Host-02 identities. Producer tables are sds_p_<sanitized peer id>__<STD>.
const (
	shapeLocalPeer  = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U" // host-02
	shapeMirrorPeer = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45" // host-01
	shapeProvider   = "space-data-network-02"
	shapeIQCBatch   = "60cc968008101521f0f062a74bf83f9c7e72fcc3bcd8bc3ed84b97d12f4a29e3"
)

// ShapePartition is one (producer, standard, source lane) of the store.
type ShapePartition struct {
	Schema      string
	Producer    string // peer id the records are stored under
	ProviderID  string
	SourceName  string
	Batches     int // records are spread evenly over this many batch ids
	Records     int
	RecordBytes int // mean stored payload size
	// MirrorOf names another partition (by Lane) whose first Records records
	// this partition stores again under Producer: the same CIDs in a second
	// producer table, which is what makes a standard's read source a union.
	MirrorOf string
}

// Lane is the partition's identity inside a shape.
func (p ShapePartition) Lane() string {
	return p.Schema + "|" + p.Producer + "|" + p.SourceName
}

// BatchID is the batch tag of record seq.
func (p ShapePartition) BatchID(seq int) string {
	if p.Schema == "IQC.fbs" && p.SourceName == "IQEngine" {
		return shapeIQCBatch
	}
	batches := p.Batches
	if batches < 1 {
		batches = 1
	}
	per := (p.Records + batches - 1) / batches
	if per < 1 {
		per = 1
	}
	return fmt.Sprintf("%s-%s-b%03d", strings.TrimSuffix(p.Schema, ".fbs"), p.SourceName, seq/per)
}

// Host02Shape is host-02's measured record store (summary of 2026-09-27
// 18:31 UTC), largest lanes plus the $IQC second producer table.
func Host02Shape(iqcMirrorFraction float64) []ShapePartition {
	parts := []ShapePartition{
		{Schema: "IQC.fbs", Producer: "source:sigmf", ProviderID: shapeProvider, SourceName: "IQEngine", Batches: 1, Records: 419_028, RecordBytes: 1_591},
		{Schema: "OMM.fbs", Producer: "source:celestrak", ProviderID: shapeProvider, SourceName: "celestrak-gp", Batches: 53, Records: 1_696_780, RecordBytes: 324},
		{Schema: "MPE.fbs", Producer: "source:celestrak", ProviderID: shapeProvider, SourceName: "celestrak-gp", Batches: 52, Records: 1_689_228, RecordBytes: 123},
		{Schema: "CAT.fbs", Producer: "source:celestrak", ProviderID: shapeProvider, SourceName: "celestrak-satcat-csv", Batches: 1, Records: 70_810, RecordBytes: 199},
		{Schema: "CAT.fbs", Producer: "source:celestrak", ProviderID: shapeProvider, SourceName: "celestrak-satcat", Batches: 1, Records: 69_999, RecordBytes: 199},
	}
	if iqcMirrorFraction > 0 {
		parts = append(parts, ShapePartition{
			Schema: "IQC.fbs", Producer: shapeMirrorPeer, ProviderID: shapeProvider, SourceName: "IQEngine",
			Batches: 1, Records: int(math.Round(float64(parts[0].Records) * math.Min(iqcMirrorFraction, 1))),
			RecordBytes: 1_591, MirrorOf: parts[0].Lane(),
		})
	}
	return parts
}

// ScaleShape multiplies every partition's record count by scale (min 1).
func ScaleShape(parts []ShapePartition, scale float64) []ShapePartition {
	out := make([]ShapePartition, len(parts))
	for i, p := range parts {
		p.Records = int(math.Max(1, math.Round(float64(p.Records)*scale)))
		out[i] = p
	}
	// A mirror never exceeds the lane it mirrors.
	byLane := map[string]int{}
	for _, p := range out {
		byLane[p.Lane()] = p.Records
	}
	for i, p := range out {
		if n, ok := byLane[p.MirrorOf]; ok && p.Records > n {
			out[i].Records = n
		}
	}
	return out
}

// shapeEpochBase anchors every generated epoch so a rebuild of the same shape
// produces byte-identical records (and CIDs) on every build and run.
var shapeEpochBase = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// BuildShapeRecord returns record seq of partition p: deterministic, valid for
// the standard, padded to the partition's mean size.
func BuildShapeRecord(p ShapePartition, seq int) ([]byte, error) {
	b := flatbuffers.NewBuilder(p.RecordBytes + 256)
	lane := p.SourceName
	switch p.Schema {
	case "IQC.fbs":
		return buildShapeIQC(b, lane, seq, p.RecordBytes), nil
	case "OMM.fbs":
		return buildShapeOMM(b, lane, seq, p.Records, p.Batches, p.RecordBytes), nil
	case "MPE.fbs":
		return buildShapeMPE(b, lane, seq, p.Records, p.Batches, p.RecordBytes), nil
	case "CAT.fbs":
		return buildShapeCAT(b, lane, seq, p.RecordBytes), nil
	}
	return nil, fmt.Errorf("no production-shape builder for %s", p.Schema)
}

func shapePad(prefix string, want int) string {
	if want <= len(prefix) {
		return prefix
	}
	var sb strings.Builder
	sb.Grow(want)
	sb.WriteString(prefix)
	for sb.Len() < want {
		sb.WriteString(" lorem-ipsum-dolor")
	}
	return sb.String()[:want]
}

// buildShapeIQC: a SigMF capture's metadata record, ~1.6 KB like host-02's.
func buildShapeIQC(b *flatbuffers.Builder, lane string, seq, size int) []byte {
	start := shapeEpochBase.Add(time.Duration(seq) * 37 * time.Second)
	fixed := 420 // measured overhead of the fields below, so DESCRIPTION pads to size
	id := b.CreateString(fmt.Sprintf("iqc-%s-%09d", lane, seq))
	capture := b.CreateString(fmt.Sprintf("capture-%09d", seq))
	source := b.CreateString(lane)
	url := b.CreateString(fmt.Sprintf("https://iqengine.example/api/datasources/gnuradio/iqengine/%09d.sigmf-meta", seq))
	recordID := b.CreateString(fmt.Sprintf("rec-%09d", seq))
	sha := b.CreateString(fmt.Sprintf("%064x", uint64(seq)*0x9e3779b97f4a7c15))
	retrieved := b.CreateString(start.Add(time.Hour).Format(time.RFC3339))
	title := b.CreateString(fmt.Sprintf("Capture %d", seq))
	desc := b.CreateString(shapePad(fmt.Sprintf("SigMF capture %d.", seq), size-fixed))
	author := b.CreateString("stress")
	version := b.CreateString("1.2.0")
	datatype := b.CreateString("cf32_le")
	startS := b.CreateString(start.Format(time.RFC3339))
	license := b.CreateString("CC-BY-4.0")
	IQC.IQCStart(b)
	IQC.IQCAddID(b, id)
	IQC.IQCAddCAPTURE_ID(b, capture)
	IQC.IQCAddSOURCE_NAME(b, source)
	IQC.IQCAddSOURCE_URL(b, url)
	IQC.IQCAddSOURCE_RECORD_ID(b, recordID)
	IQC.IQCAddSOURCE_SHA256(b, sha)
	IQC.IQCAddRETRIEVED_AT(b, retrieved)
	IQC.IQCAddTITLE(b, title)
	IQC.IQCAddDESCRIPTION(b, desc)
	IQC.IQCAddAUTHOR(b, author)
	IQC.IQCAddSIGMF_VERSION(b, version)
	IQC.IQCAddDATATYPE(b, datatype)
	IQC.IQCAddSAMPLE_RATE_HZ(b, 2.4e6)
	IQC.IQCAddNUM_CHANNELS(b, 1)
	IQC.IQCAddCENTER_FREQ_HZ(b, 1.0e8+float64(seq%1000)*1e3)
	IQC.IQCAddSAMPLE_COUNT(b, uint64(1_000_000+seq))
	IQC.IQCAddCAPTURE_START(b, startS)
	IQC.IQCAddLICENSE(b, license)
	root := IQC.IQCEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$IQC"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// shapeObject splits seq into (object, epoch step) the way a GP lane is laid
// out: every batch re-publishes each object at a newer epoch.
func shapeObject(seq, records, batches int) (norad uint32, step int) {
	if batches < 1 {
		batches = 1
	}
	perBatch := (records + batches - 1) / batches
	if perBatch < 1 {
		perBatch = 1
	}
	return uint32(seq%perBatch) + 1, seq / perBatch
}

func buildShapeOMM(b *flatbuffers.Builder, lane string, seq, records, batches, size int) []byte {
	norad, step := shapeObject(seq, records, batches)
	epoch := shapeEpochBase.Add(time.Duration(step)*12*time.Hour + time.Duration(norad)*time.Second)
	fixed := 250
	name := b.CreateString(fmt.Sprintf("OBJECT %d", norad))
	objectID := b.CreateString(fmt.Sprintf("2026-%05dA", norad%100000))
	epochS := b.CreateString(epoch.Format("2006-01-02T15:04:05.000000"))
	center := b.CreateString("EARTH")
	created := b.CreateString(epoch.Add(time.Hour).Format(time.RFC3339))
	originator := b.CreateString("18 SPCS")
	comment := b.CreateString(shapePad(lane, size-fixed))
	OMM.OMMStart(b)
	OMM.OMMAddOBJECT_NAME(b, name)
	OMM.OMMAddOBJECT_ID(b, objectID)
	OMM.OMMAddNORAD_CAT_ID(b, norad)
	OMM.OMMAddEPOCH(b, epochS)
	OMM.OMMAddCENTER_NAME(b, center)
	OMM.OMMAddCREATION_DATE(b, created)
	OMM.OMMAddORIGINATOR(b, originator)
	OMM.OMMAddCOMMENT(b, comment)
	OMM.OMMAddMEAN_MOTION(b, 15.0+float64(norad%100)/1000)
	OMM.OMMAddECCENTRICITY(b, 0.0001+float64(norad%97)/1e6)
	OMM.OMMAddINCLINATION(b, float64(norad%180))
	OMM.OMMAddRA_OF_ASC_NODE(b, float64((norad*7)%360))
	OMM.OMMAddARG_OF_PERICENTER(b, float64((norad*11)%360))
	OMM.OMMAddMEAN_ANOMALY(b, float64((norad*13+uint32(step))%360))
	OMM.OMMAddBSTAR(b, 1e-4)
	OMM.OMMAddELEMENT_SET_NO(b, uint32(999+step))
	OMM.OMMAddREV_AT_EPOCH(b, float64(step))
	OMM.OMMAddUSER_DEFINED_EPOCH_TIMESTAMP(b, float64(epoch.Unix()))
	root := OMM.OMMEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$OMM"))
	return append([]byte(nil), b.FinishedBytes()...)
}

func buildShapeMPE(b *flatbuffers.Builder, lane string, seq, records, batches, size int) []byte {
	norad, step := shapeObject(seq, records, batches)
	epoch := shapeEpochBase.Add(time.Duration(step)*12*time.Hour + time.Duration(norad)*time.Second)
	fixed := 88
	entity := b.CreateString(shapePad(fmt.Sprintf("%d", norad), size-fixed))
	MPE.MPEStart(b)
	MPE.MPEAddENTITY_ID(b, entity)
	MPE.MPEAddEPOCH(b, float64(epoch.Unix()))
	MPE.MPEAddMEAN_MOTION(b, 15.0+float64(norad%100)/1000)
	MPE.MPEAddECCENTRICITY(b, 0.0001+float64(norad%97)/1e6)
	MPE.MPEAddINCLINATION(b, float64(norad%180))
	MPE.MPEAddRA_OF_ASC_NODE(b, float64((norad*7)%360))
	MPE.MPEAddARG_OF_PERICENTER(b, float64((norad*11)%360))
	MPE.MPEAddMEAN_ANOMALY(b, float64((norad*13+uint32(step))%360))
	MPE.MPEAddBSTAR(b, 1e-4)
	root := MPE.MPEEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$MPE"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// buildShapeCAT: one object per record, so no record supersedes another in
// its (producer, source) lane — the host-02 lanes hold one row per object.
func buildShapeCAT(b *flatbuffers.Builder, lane string, seq, size int) []byte {
	norad := uint32(seq + 1)
	fixed := 110
	name := b.CreateString(shapePad(fmt.Sprintf("OBJECT %d %s", norad, lane), size-fixed))
	objectID := b.CreateString(fmt.Sprintf("1990-%05dA", norad%100000))
	launch := b.CreateString(shapeEpochBase.AddDate(0, 0, -int(norad%9000)).Format("2006-01-02"))
	CAT.CATStart(b)
	CAT.CATAddOBJECT_NAME(b, name)
	CAT.CATAddOBJECT_ID(b, objectID)
	CAT.CATAddNORAD_CAT_ID(b, norad)
	CAT.CATAddLAUNCH_DATE(b, launch)
	CAT.CATAddPERIOD(b, 90+float64(norad%600))
	CAT.CATAddINCLINATION(b, float64(norad%180))
	CAT.CATAddAPOGEE(b, 400+float64(norad%36000))
	CAT.CATAddPERIGEE(b, 300+float64(norad%500))
	root := CAT.CATEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$CAT"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// PopulateStats is what building a shape cost.
type PopulateStats struct {
	Records  int64
	Bytes    int64
	Duration time.Duration
	Restarts int
}

// PopulateOptions controls PopulateShape.
type PopulateOptions struct {
	// BatchSize is records per StoreBatchWithSourceTags call (default 2000).
	BatchSize int
	// RestartEvery closes and reopens the store after this many records
	// (0 = never). A node's process lifetime is bounded by restarts and
	// updates, and the engine's record arena depends on it: evicted rows keep
	// their arena bytes until a boot discards a mostly-dead arena, and one
	// lifetime that ingests ~1 GiB of routed records traps the engine (the
	// arena's doubling past 1 GiB does not fit in wasm32 memory).
	RestartEvery int
	// ResumeFile records per-lane progress after every batch, so a population
	// that dies is resumed instead of restarted.
	ResumeFile string
	// Progress (optional) is called after every batch.
	Progress func(store *storage.FlatSQLStore, lane string, done, total int)
}

func readShapeResume(path string) map[string]int {
	done := map[string]int{}
	if path == "" {
		return done
	}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &done)
	}
	return done
}

func writeShapeResume(path string, done map[string]int) error {
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(done)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PopulateShape writes every partition through StoreBatchWithSourceTags on a
// store it opens with open, mirror partitions after the lanes they mirror,
// and closes the store when done.
func PopulateShape(ctx context.Context, open func() (*storage.FlatSQLStore, error), parts []ShapePartition, opts PopulateOptions) (PopulateStats, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 2000
	}
	started := time.Now()
	var stats PopulateStats
	ordered := append([]ShapePartition(nil), parts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].MirrorOf == "" && ordered[j].MirrorOf != "" })
	byLane := map[string]ShapePartition{}
	for _, p := range parts {
		byLane[p.Lane()] = p
	}
	done := readShapeResume(opts.ResumeFile)
	store, err := open()
	if err != nil {
		return stats, err
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	sinceOpen := 0
	for _, p := range ordered {
		gen := p
		if p.MirrorOf != "" {
			src, ok := byLane[p.MirrorOf]
			if !ok {
				return stats, fmt.Errorf("partition %s mirrors unknown lane %s", p.Lane(), p.MirrorOf)
			}
			gen = src
		}
		for start := done[p.Lane()]; start < p.Records; {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			if opts.RestartEvery > 0 && sinceOpen >= opts.RestartEvery {
				if err := store.Close(); err != nil {
					store = nil
					return stats, fmt.Errorf("restart: close: %w", err)
				}
				if store, err = open(); err != nil {
					store = nil
					return stats, fmt.Errorf("restart: open: %w", err)
				}
				stats.Restarts++
				sinceOpen = 0
			}
			// A batch never straddles two batch ids.
			batchID := p.BatchID(start)
			end := start + batchSize
			if end > p.Records {
				end = p.Records
			}
			for end > start+1 && p.BatchID(end-1) != batchID {
				end--
			}
			records, err := buildShapeBatch(gen, start, end)
			if err != nil {
				return stats, err
			}
			tags := storage.SourceTags{ProviderID: p.ProviderID, SourceName: p.SourceName, BatchID: batchID}
			if _, err := store.StoreBatchWithSourceTags(p.Schema, records, p.Producer, nil, tags); err != nil {
				return stats, fmt.Errorf("store %s records [%d,%d): %w", p.Lane(), start, end, err)
			}
			for _, r := range records {
				stats.Bytes += int64(len(r))
			}
			stats.Records += int64(len(records))
			sinceOpen += len(records)
			start = end
			done[p.Lane()] = start
			if err := writeShapeResume(opts.ResumeFile, done); err != nil {
				return stats, err
			}
			if opts.Progress != nil {
				opts.Progress(store, p.Lane(), start, p.Records)
			}
		}
	}
	err = store.Close()
	store = nil
	stats.Duration = time.Since(started)
	return stats, err
}

// EngineMemory is the engine's linear memory in use and its cap, in bytes.
func EngineMemory(store *storage.FlatSQLStore) (used, max uint64) {
	rt, _ := store.EngineRuntime()
	if rt == nil {
		return 0, 0
	}
	m, err := rt.MemoryStats()
	if err != nil {
		return 0, 0
	}
	return m.Bytes, m.MaxBytes
}

// buildShapeBatch builds records [start,end) of p on every core; the write
// itself is serialized by the store.
func buildShapeBatch(p ShapePartition, start, end int) ([][]byte, error) {
	out := make([][]byte, end-start)
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	var next atomic.Int64
	next.Store(int64(start))
	var firstErr atomic.Value
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				seq := int(next.Add(1) - 1)
				if seq >= end {
					return
				}
				rec, err := BuildShapeRecord(p, seq)
				if err != nil {
					firstErr.CompareAndSwap(nil, err)
					return
				}
				out[seq-start] = rec
			}
		}()
	}
	wg.Wait()
	if err, ok := firstErr.Load().(error); ok && err != nil {
		return nil, err
	}
	return out, nil
}

// ── measurement ────────────────────────────────────────────────────────────

// LatencySummary is one metric's distribution.
type LatencySummary struct {
	N      int
	Errors int
	P50    time.Duration
	P99    time.Duration
	Max    time.Duration
	Total  time.Duration
}

// LatencySet collects call durations from any number of goroutines.
type LatencySet struct {
	mu     sync.Mutex
	d      []time.Duration
	errors int
}

// Add records one call.
func (l *LatencySet) Add(d time.Duration, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.errors++
		return
	}
	l.d = append(l.d, d)
}

// Summary computes the distribution (nearest-rank percentiles).
func (l *LatencySet) Summary() LatencySummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := LatencySummary{N: len(l.d), Errors: l.errors}
	if len(l.d) == 0 {
		return s
	}
	sorted := append([]time.Duration(nil), l.d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := func(p float64) time.Duration {
		i := int(math.Ceil(p*float64(len(sorted)))) - 1
		if i < 0 {
			i = 0
		}
		return sorted[i]
	}
	s.P50, s.P99, s.Max = rank(0.50), rank(0.99), sorted[len(sorted)-1]
	for _, d := range sorted {
		s.Total += d
	}
	return s
}

// EngineCall is one engine statement seen in flight.
type EngineCall struct {
	Elapsed time.Duration
	Phase   string
	SQL     string
}

// EngineWatch samples the engine's in-flight statement and keeps the longest
// ones. It reads the runtime without the store lock (EngineRuntime takes it,
// and a watch that blocks behind the writer it is timing sees nothing), so it
// must be started on a runtime that stays current for its span.
type EngineWatch struct {
	rt       *flatsqlrt.Runtime
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	longest  []EngineCall
	poisoned bool
}

// StartEngineWatch polls rt.InFlight every interval.
func StartEngineWatch(rt *flatsqlrt.Runtime, interval time.Duration) *EngineWatch {
	w := &EngineWatch{rt: rt, interval: interval, stop: make(chan struct{}), done: make(chan struct{})}
	go w.loop()
	return w
}

func (w *EngineWatch) loop() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	var curSQL string
	var curStart time.Time
	var curMax EngineCall
	flush := func() {
		if curMax.Elapsed > 0 {
			w.record(curMax)
		}
		curMax = EngineCall{}
	}
	for {
		select {
		case <-w.stop:
			flush()
			return
		case <-ticker.C:
		}
		if w.rt == nil {
			continue
		}
		if w.rt.Poisoned() {
			w.mu.Lock()
			w.poisoned = true
			w.mu.Unlock()
		}
		phase, sql, elapsed, ok := w.rt.InFlight()
		if !ok {
			flush()
			curSQL = ""
			continue
		}
		began := time.Now().Add(-elapsed)
		// A different statement, or the same text started again later.
		if sql != curSQL || began.Sub(curStart) > w.interval {
			flush()
			curSQL, curStart = sql, began
		}
		if elapsed > curMax.Elapsed {
			curMax = EngineCall{Elapsed: elapsed, Phase: phase, SQL: sql}
		}
	}
}

func (w *EngineWatch) record(c EngineCall) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.longest = append(w.longest, c)
	sort.Slice(w.longest, func(i, j int) bool { return w.longest[i].Elapsed > w.longest[j].Elapsed })
	if len(w.longest) > 5 {
		w.longest = w.longest[:5]
	}
}

// Stop ends the watch and returns the longest calls, longest first.
func (w *EngineWatch) Stop() (longest []EngineCall, poisoned bool) {
	close(w.stop)
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]EngineCall(nil), w.longest...), w.poisoned
}

// LogCapture reads the storage/flatsqlrt log lines that carry the boot
// evidence: per-statement holds, boot phases, engine record-state discards,
// poisonings, store-lock holds.
type LogCapture struct {
	pr   *logging.PipeReader
	done chan struct{}
	mu   sync.Mutex
	ev   LogEvidence
}

// LogEvidence is what a LogCapture saw.
type LogEvidence struct {
	MaxStatementHeld time.Duration
	MaxStatementSQL  string
	Statements30s    int // statements held past 30 s
	Phases           map[string]time.Duration
	Discarded        bool
	DiscardLines     []string
	Poisoned         bool
	MaxWriteLockHeld time.Duration
	MaxWriteLockSite string
	MaxReadLockWait  time.Duration
	Lines            int
}

var (
	reSlowStatement = regexp.MustCompile(`FlatSQL slow statement: held (\S+), waited (\S+) .*SQL: (.*)$`)
	reBootPhase     = regexp.MustCompile(`FlatSQL boot phase "([^"]+)" took (\S+)`)
	reSlowLock      = regexp.MustCompile(`FlatSQL slow lock: (write|read) "([^"]+)" held (\S+), waited (\S+)`)
	reDiscard       = regexp.MustCompile(`record state discarded|discarding the engine record state|is unusable .*discarding it|\(discarded: true\)`)
)

// StartLogCapture raises the storage and flatsqlrt loggers to INFO and reads
// them until Stop.
func StartLogCapture() *LogCapture {
	_ = logging.SetLogLevel("storage", "info")
	_ = logging.SetLogLevel("flatsqlrt", "info")
	c := &LogCapture{
		pr:   logging.NewPipeReader(logging.PipeFormat(logging.PlaintextOutput), logging.PipeLevel(logging.LevelInfo)),
		done: make(chan struct{}),
		ev:   LogEvidence{Phases: map[string]time.Duration{}},
	}
	go c.read()
	return c
}

func (c *LogCapture) read() {
	defer close(c.done)
	sc := bufio.NewScanner(c.pr)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		c.observe(sc.Text())
	}
}

func (c *LogCapture) observe(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ev.Lines++
	if m := reSlowStatement.FindStringSubmatch(line); m != nil {
		if held, err := time.ParseDuration(m[1]); err == nil {
			if held > c.ev.MaxStatementHeld {
				c.ev.MaxStatementHeld, c.ev.MaxStatementSQL = held, shortSQL(m[3])
			}
			if held > 30*time.Second {
				c.ev.Statements30s++
			}
		}
	}
	if m := reBootPhase.FindStringSubmatch(line); m != nil {
		if d, err := time.ParseDuration(m[2]); err == nil && d > c.ev.Phases[m[1]] {
			c.ev.Phases[m[1]] = d
		}
	}
	if m := reSlowLock.FindStringSubmatch(line); m != nil {
		held, _ := time.ParseDuration(m[3])
		waited, _ := time.ParseDuration(m[4])
		if m[1] == "write" && held > c.ev.MaxWriteLockHeld {
			c.ev.MaxWriteLockHeld, c.ev.MaxWriteLockSite = held, m[2]
		}
		if m[1] == "read" && waited > c.ev.MaxReadLockWait {
			c.ev.MaxReadLockWait = waited
		}
	}
	if reDiscard.MatchString(line) {
		c.ev.Discarded = true
		if len(c.ev.DiscardLines) < 4 {
			c.ev.DiscardLines = append(c.ev.DiscardLines, shortSQL(line))
		}
	}
	if strings.Contains(line, "engine poisoned") || strings.Contains(line, "module poisoned") {
		c.ev.Poisoned = true
	}
}

// Snapshot returns the evidence so far and resets it.
func (c *LogCapture) Snapshot() LogEvidence {
	c.mu.Lock()
	defer c.mu.Unlock()
	ev := c.ev
	c.ev = LogEvidence{Phases: map[string]time.Duration{}}
	return ev
}

// Stop detaches the capture.
func (c *LogCapture) Stop() {
	_ = c.pr.Close()
	<-c.done
}

func shortSQL(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

// EngineHeld is the engine's statement account between two snapshots:
// statements run and time the engine lock was HELD. With one caller and
// nothing else running it is that caller's engine hold time.
type EngineHeld struct {
	Queries int64
	Held    time.Duration
	Waited  time.Duration
}

// EngineStatsSnapshot reads rt's cumulative statement account.
func EngineStatsSnapshot(rt *flatsqlrt.Runtime) EngineHeld {
	if rt == nil {
		return EngineHeld{}
	}
	q, _, wait, held := rt.Stats()
	return EngineHeld{Queries: q, Held: held, Waited: wait}
}

// Sub is the account accrued since before.
func (e EngineHeld) Sub(before EngineHeld) EngineHeld {
	return EngineHeld{Queries: e.Queries - before.Queries, Held: e.Held - before.Held, Waited: e.Waited - before.Waited}
}

// LoadAverage1 is the 1-minute load average ("" when unknown). The box these
// runs share is loaded by other lanes, so every timing carries it.
func LoadAverage1() float64 {
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(raw)); len(f) > 0 {
			v, _ := strconv.ParseFloat(f[0], 64)
			return v
		}
	}
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return -1
	}
	f := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{}"))
	if len(f) == 0 {
		return -1
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// ShapeMetric is one reported measurement.
type ShapeMetric struct {
	Name       string
	Latency    LatencySummary `json:",omitempty"`
	EngineHeld LatencySummary `json:",omitempty"` // per-call engine hold (statement account)
	Value      string         `json:",omitempty"`
	LoadStart  float64
	LoadEnd    float64
	Threshold  string `json:",omitempty"`
	Pass       *bool  `json:",omitempty"`
	Note       string `json:",omitempty"`
}

// ShapeReport is one run's result file.
type ShapeReport struct {
	Build     string
	Scale     float64
	Shape     []ShapePartition
	Populate  PopulateStats
	CPUs      int
	StartedAt time.Time
	Metrics   []ShapeMetric
	Longest   []EngineCall
	Failures  []string

	// Path, when set, receives the report after every metric, so a run that
	// dies (a poisoned engine, a crash) still leaves what it measured.
	Path string `json:"-"`
}

// Add appends a metric.
func (r *ShapeReport) Add(m ShapeMetric) {
	r.Metrics = append(r.Metrics, m)
	_ = r.Write(r.Path)
}

// Write stores the report as JSON.
func (r *ShapeReport) Write(path string) error {
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// Table renders the metrics for a test log.
func (r *ShapeReport) Table() string {
	var b strings.Builder
	fmt.Fprintf(&b, "build=%s scale=%g cpus=%d populate=%d records/%d B in %s\n", r.Build, r.Scale, r.CPUs, r.Populate.Records, r.Populate.Bytes, r.Populate.Duration.Round(time.Second))
	fmt.Fprintf(&b, "%-44s %6s %10s %10s %10s %10s %10s  %-11s %s\n", "metric", "n", "p50", "p99", "max", "held p50", "held p99", "load", "verdict")
	for _, m := range r.Metrics {
		verdict := "report"
		if m.Pass != nil {
			verdict = map[bool]string{true: "PASS", false: "FAIL"}[*m.Pass] + " " + m.Threshold
		}
		if m.Value != "" {
			fmt.Fprintf(&b, "%-44s %s  load %.1f→%.1f  %s\n", m.Name, m.Value, m.LoadStart, m.LoadEnd, verdict)
			continue
		}
		fmt.Fprintf(&b, "%-44s %6d %10s %10s %10s %10s %10s  %4.1f→%-5.1f %s\n", m.Name, m.Latency.N,
			rd(m.Latency.P50), rd(m.Latency.P99), rd(m.Latency.Max), rd(m.EngineHeld.P50), rd(m.EngineHeld.P99), m.LoadStart, m.LoadEnd, verdict)
	}
	for _, c := range r.Longest {
		fmt.Fprintf(&b, "longest engine call %s [%s] %s\n", rd(c.Elapsed), c.Phase, c.SQL)
	}
	return b.String()
}

func rd(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(time.Millisecond).String()
	}
}

// boolPtr is a verdict for ShapeMetric.Pass.
func boolPtr(v bool) *bool { return &v }
