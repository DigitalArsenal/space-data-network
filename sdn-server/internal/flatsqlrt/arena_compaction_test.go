package flatsqlrt

// Arena compaction in the host the fleet runs: the embedded no-exceptions
// engine under WasmEdge (AOT), disk-backed, WAL. The engine's own tests
// (flatsql cpp/test/arena_compaction_test.cpp) prove equivalence and crash
// safety natively; these re-measure it where it matters.

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
)

// ommRecord builds an unprefixed $OMM FlatBuffer (ommTestSchema): OBJECT_NAME
// (field 3) padded to nameBytes, NORAD_CAT_ID (field 27).
func ommRecord(norad uint32, nameBytes int) []byte {
	b := flatbuffers.NewBuilder(nameBytes + 64)
	name := make([]byte, nameBytes)
	copy(name, fmt.Sprintf("SAT-%d-", norad))
	for i := len(fmt.Sprintf("SAT-%d-", norad)); i < nameBytes; i++ {
		name[i] = byte('a' + norad%26)
	}
	nameOff := b.CreateByteString(name)
	b.StartObject(28)
	b.PrependUOffsetTSlot(3, nameOff, 0)
	b.PrependUint32Slot(27, norad, 0)
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte("$OMM"))
	return b.FinishedBytes()
}

func openArenaDB(t *testing.T, rt *Runtime, dbPath string, sources []string) *Database {
	t.Helper()
	db, err := rt.OpenDatabase(ommTestSchema, "control", dbPath, JournalWAL)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatalf("RegisterFileID: %v", err)
	}
	return db
}

// partitionRows is (rowid -> bytes) of one partition.
func partitionRows(t *testing.T, db *Database, partition string) map[int64]string {
	t.Helper()
	res, err := db.Query(`SELECT _rowid, _data FROM "` + partition + `"`)
	if err != nil {
		t.Fatalf("read %s: %v", partition, err)
	}
	out := make(map[int64]string, len(res.Rows))
	for _, row := range res.Rows {
		seq, _ := row[0].(int64)
		data, _ := row[1].([]byte)
		out[seq] = string(data)
	}
	return out
}

func arenaAnswers(t *testing.T, db *Database, sources []string) []string {
	t.Helper()
	var out []string
	for _, source := range sources {
		p := "OMM@" + source
		rows := partitionRows(t, db, p)
		keys := make([]int64, 0, len(rows))
		for k := range rows {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			out = append(out, p+":"+strconv.FormatInt(k, 10)+":"+rows[k])
		}
		for _, q := range []string{
			`SELECT COUNT(*), SUM(NORAD_CAT_ID), MIN(OBJECT_NAME) FROM "` + p + `"`,
			`SELECT _rowid, NORAD_CAT_ID FROM "` + p + `" WHERE NORAD_CAT_ID BETWEEN 100 AND 400 ORDER BY NORAD_CAT_ID, _rowid`,
		} {
			res, err := db.Query(q)
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			out = append(out, fmt.Sprint(res.Rows))
		}
	}
	res, err := db.Query(`SELECT _source, COUNT(*), MAX(_rowid) FROM OMM GROUP BY _source ORDER BY _source`)
	if err != nil {
		t.Fatalf("unified view: %v", err)
	}
	return append(out, fmt.Sprint(res.Rows))
}

// Every query answers the same after a compaction, sequences included; the
// compacted state survives a teardown; the next row gets the next sequence.
func TestArenaCompactionInTheWasmHost(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "arena.db")
	sources := []string{"alpha", "beta", "gamma"}
	rt := newDiskRuntime(t, root)
	db := openArenaDB(t, rt, dbPath, sources)
	for _, s := range sources {
		if err := db.RegisterSource(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateUnifiedViews(); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	type row struct {
		partition string
		seq       uint64
	}
	var rows []row
	for batch := 0; batch < 30; batch++ {
		source := sources[batch%len(sources)]
		payloads := make([][]byte, 100)
		for i := range payloads {
			payloads[i] = ommRecord(uint32(batch*100+i), 40+rng.Intn(600))
		}
		seqs, err := db.IngestManyWithSource(payloads, source)
		if err != nil {
			t.Fatal(err)
		}
		for _, seq := range seqs {
			rows = append(rows, row{"OMM@" + source, uint64(seq)})
		}
		if batch == 14 {
			if err := db.FlushIndex(); err != nil {
				t.Fatal(err)
			}
		}
	}
	dead := map[string][]uint64{}
	for i, r := range rows {
		if i < 50 || i >= len(rows)-30 || rng.Intn(10) < 7 {
			dead[r.partition] = append(dead[r.partition], r.seq)
		}
	}
	for p, seqs := range dead {
		if err := db.MarkDeletedMany(p, seqs); err != nil {
			t.Fatal(err)
		}
	}
	before := arenaAnswers(t, db, sources)
	statsBefore, err := db.ArenaStats()
	if err != nil {
		t.Fatal(err)
	}
	if statsBefore.DeadBytes*2 < statsBefore.Size {
		t.Fatalf("dead bytes %d of %d: the test needs a mostly-dead arena", statsBefore.DeadBytes, statsBefore.Size)
	}
	steps := 0
	for {
		status, err := db.CompactArenaStep(16 << 10)
		steps++
		if err != nil {
			t.Fatalf("step %d: %v", steps, err)
		}
		if status == CompactDone {
			break
		}
		// Between steps the engine answers reads.
		if steps%5 == 0 {
			if got := arenaAnswers(t, db, sources); !reflect.DeepEqual(got, before) {
				t.Fatalf("reads between steps (step %d) differ from before the compaction", steps)
			}
		}
	}
	after := arenaAnswers(t, db, sources)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("a query answers differently after the compaction")
	}
	statsAfter, _ := db.ArenaStats()
	report, _ := db.LastArenaCompaction()
	if !report.Completed || statsAfter.Size >= statsBefore.Size/2 || statsAfter.DeadBytes != 0 {
		t.Fatalf("compaction: before %+v after %+v report %+v", statsBefore, statsAfter, report)
	}
	next, err := db.IngestManyWithSource([][]byte{ommRecord(99999, 100)}, "alpha")
	if err != nil || uint64(next[0]) != rows[len(rows)-1].seq+1 {
		t.Fatalf("next sequence %v (%v), want %d", next, err, rows[len(rows)-1].seq+1)
	}
	if err := db.FlushIndex(); err != nil {
		t.Fatal(err)
	}
	live := arenaAnswers(t, db, sources)
	db.Destroy()
	rt.Close()

	rt2 := newDiskRuntime(t, root)
	db2 := openArenaDB(t, rt2, dbPath, sources)
	n, err := db2.OpenState()
	if err != nil || int64(n) != report.KeptRecords+1 {
		t.Fatalf("OpenState = %d, %v; want %d rows", n, err, report.KeptRecords+1)
	}
	if got := arenaAnswers(t, db2, sources); !reflect.DeepEqual(got, live) {
		t.Fatal("the reopened compacted state answers differently")
	}
	again, err := db2.IngestManyWithSource([][]byte{ommRecord(99998, 100)}, "beta")
	if err != nil || uint64(again[0]) != rows[len(rows)-1].seq+2 {
		t.Fatalf("sequence after reopen %v (%v), want %d", again, err, rows[len(rows)-1].seq+2)
	}
	if _, err := os.Stat(dbPath + ".fsdata.compact"); !os.IsNotExist(err) {
		t.Fatalf("temp stream left behind: %v", err)
	}
	t.Logf("%d rows, %d dropped, %d kept, %d runs, %d steps; arena %d -> %d bytes, capacity %d -> %d",
		len(rows), report.DroppedRecords, report.KeptRecords, report.SequenceRuns, steps,
		statsBefore.Size, statsAfter.Size, statsBefore.Capacity, statsAfter.Capacity)
}

// The churn soak at host-02 shape: ingest plus tombstone (a per-source FIFO
// window), a flush every "checkpoint", compaction past the mark when mostly
// dead and before any ingest that would pass the budget. The arena stays
// under its budget, nothing is refused, nothing traps, and the partitions
// hold exactly the window. SDN_FLATSQL_ARENA_SOAK_MIB sets the cumulative
// ingest (default 48 MiB at 1/16 scale; 2600 for the host-02-shape run:
// budget 512 MiB, mark 256 MiB, ~150 MB live).
func TestArenaCompactionSoakInTheWasmHost(t *testing.T) {
	totalMiB := int64(48)
	if v := os.Getenv("SDN_FLATSQL_ARENA_SOAK_MIB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		totalMiB = n
	}
	full := totalMiB >= 1024
	budget, liveTarget := int64(32<<20), int64(9_500_000)
	if full {
		budget, liveTarget = 512<<20, 150_000_000
	}
	mark := budget / 2
	step := int64(16 << 20)
	sources := []string{"IQEngine", "celestrak-gp", "celestrak-satcat"}
	window := int(liveTarget / 1700 / int64(len(sources)))

	root := t.TempDir()
	rt := newDiskRuntime(t, root)
	db := openArenaDB(t, rt, filepath.Join(root, "soak.db"), sources)
	for _, s := range sources {
		if err := db.RegisterSource(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateUnifiedViews(); err != nil {
		t.Fatal(err)
	}
	memStart, _ := rt.MemoryStats()
	rng := rand.New(rand.NewSource(42))
	windows := map[string][]uint64{}
	var ingested, compactions, batches int64
	var longest, compacting time.Duration
	var peakSize, peakCapacity int64
	var peakMem uint64
	norad := uint32(0)
	type memPoint struct{ before, peak, after, liveCapacity uint64 }
	var memSeries []memPoint
	var maxGrowth, growthBound uint64
	compact := func() {
		t0 := time.Now()
		memBefore, _ := rt.MemoryStats()
		point := memPoint{before: memBefore.Bytes, peak: memBefore.Bytes}
		var stepTimes []time.Duration
		for {
			s0 := time.Now()
			status, err := db.CompactArenaStep(step)
			held := time.Since(s0)
			stepTimes = append(stepTimes, held.Round(time.Millisecond))
			if held > longest {
				longest = held
			}
			if err != nil {
				t.Fatalf("compaction step: %v", err)
			}
			if mem, err := rt.MemoryStats(); err == nil {
				if mem.Bytes > peakMem {
					peakMem = mem.Bytes
				}
				if mem.Bytes > point.peak {
					point.peak = mem.Bytes
				}
			}
			if status == CompactDone {
				break
			}
		}
		memAfter, _ := rt.MemoryStats()
		point.after = memAfter.Bytes
		if report, err := db.LastArenaCompaction(); err == nil {
			point.liveCapacity = uint64(report.AfterCapacity)
		}
		// What a compaction may add to linear memory: the new arena (its
		// capacity) beside the old, the plan (8 bytes a row) and the maps of
		// the kept rows; never a second old arena.
		if growth := point.peak - point.before; growth > maxGrowth {
			maxGrowth, growthBound = growth, point.liveCapacity+(64<<20)
		}
		if point.peak-point.before > point.liveCapacity+(64<<20) {
			t.Fatalf("a compaction grew linear memory by %d MiB for a %d MiB arena",
				(point.peak-point.before)>>20, point.liveCapacity>>20)
		}
		memSeries = append(memSeries, point)
		compacting += time.Since(t0)
		compactions++
		if os.Getenv("SDN_FLATSQL_ARENA_SOAK_STEPS") != "" {
			t.Logf("  compaction %d steps: %v", compactions, stepTimes)
		}
	}
	started := time.Now()
	for ingested < totalMiB<<20 {
		source := sources[batches%int64(len(sources))]
		batches++
		payloads := make([][]byte, 256)
		var batchBytes int64
		for i := range payloads {
			size := 600 + rng.Intn(2200) // IQC-like captures, ~1.7 KB
			if rng.Intn(50) == 0 {
				size = 20000
			}
			norad++
			payloads[i] = ommRecord(norad, size)
			batchBytes += int64(len(payloads[i]) + 4)
		}
		stats, err := db.ArenaStats()
		if err != nil {
			t.Fatal(err)
		}
		if stats.Size+batchBytes > budget && stats.DeadBytes > 0 {
			compact()
		}
		seqs, err := db.IngestManyWithSource(payloads, source)
		if err != nil {
			t.Fatalf("ingest after %d MiB: %v", ingested>>20, err)
		}
		ingested += batchBytes
		for _, seq := range seqs {
			windows[source] = append(windows[source], uint64(seq))
		}
		if over := len(windows[source]) - window; over > 0 {
			if err := db.MarkDeletedMany("OMM@"+source, windows[source][:over]); err != nil {
				t.Fatal(err)
			}
			windows[source] = windows[source][over:]
		}
		if batches%60 == 0 {
			if err := db.FlushIndex(); err != nil {
				t.Fatal(err)
			}
		}
		stats, err = db.ArenaStats()
		if err != nil {
			t.Fatal(err)
		}
		if stats.Size > peakSize {
			peakSize = stats.Size
		}
		if stats.Capacity > peakCapacity {
			peakCapacity = stats.Capacity
		}
		if stats.Size > mark && stats.DeadBytes*2 > stats.Size {
			compact()
		}
	}
	elapsed := time.Since(started)
	memSoakEnd, _ := rt.MemoryStats()
	if rt.Poisoned() {
		t.Fatal("the engine was poisoned")
	}
	if compactions == 0 {
		t.Fatal("the soak never compacted")
	}
	if peakCapacity > budget {
		t.Fatalf("arena capacity peaked at %d bytes, past the %d-byte budget", peakCapacity, budget)
	}
	for _, source := range sources {
		got := partitionRows(t, db, "OMM@"+source)
		if len(got) != len(windows[source]) {
			t.Fatalf("%s holds %d rows, want the %d-row window", source, len(got), len(windows[source]))
		}
		for _, seq := range windows[source] {
			if _, ok := got[int64(seq)]; !ok {
				t.Fatalf("%s lost row %d", source, seq)
			}
		}
	}
	final, _ := db.ArenaStats()
	t.Logf("soak: %d MiB ingested in %s (%d batches); %d compactions, %s compacting, longest step %s; arena peak %d MiB (final %d MiB, capacity peak %d MiB), budget %d MiB; engine memory %d MiB at start, %d MiB peak during the soak, %d MiB at its end; largest growth in one compaction %d MiB (bound %d MiB); live rows %d",
		ingested>>20, elapsed.Round(time.Millisecond), batches, compactions, compacting.Round(time.Millisecond),
		longest.Round(time.Millisecond), peakSize>>20, final.Size>>20, peakCapacity>>20, budget>>20,
		memStart.Bytes>>20, peakMem>>20, memSoakEnd.Bytes>>20, maxGrowth>>20, growthBound>>20,
		len(windows[sources[0]])*len(sources))
	for i, p := range memSeries {
		if i < 3 || i >= len(memSeries)-2 {
			t.Logf("  compaction %d: engine memory %d MiB before, %d MiB peak, %d MiB after; new arena capacity %d MiB",
				i+1, p.before>>20, p.peak>>20, p.after>>20, p.liveCapacity>>20)
		}
	}
	// Freed arenas are reused: memory does not climb compaction after
	// compaction once the first cycles have sized it.
	if n := len(memSeries); n >= 4 && memSeries[n-1].peak > memSeries[2].peak+(64<<20) {
		t.Fatalf("engine memory keeps climbing: %d MiB at the third compaction, %d MiB at the last",
			memSeries[2].peak>>20, memSeries[n-1].peak>>20)
	}
}
