package storage

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/NEV"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// TestFormat4ConcurrentFlowLoad is an opt-in synthetic storage load on the
// daemon's format-4 store (threaded engine, precompiled, the linked runtime):
// writer goroutines repeat a flow service's storage calls while reader
// goroutines page an index. Records are made up; nothing is signed. It reports
// engine traps (count, messages, time of the first), refused calls, writes
// that are never answered (STALL) and throughput. Environment:
//
//	SDN_F4_STRESS    writer goroutines (required; 0 = none)
//	SDN_F4_READERS   index-page readers (default 2)
//	SDN_F4_SERIAL    "" | writes | all: the engine's call gate (format4.SerializeMode)
//	SDN_F4_SHAPE     snapshot (default) | marker | bigput | smallcurrent | supersedeonly, see below
//	SDN_F4_POPULATE  rounds ONE goroutine writes before the timed load, no reader running
//	SDN_F4_STATEKB   shapes snapshot and bigput: every state record this many KiB (default 50 to 89)
//	SDN_F4_DURATION  the timed load (default 5m)
//	SDN_F4_MAXTRAPS  end the load after this many traps (0: no bound)
//	SDN_F4_MAXMB     end the load when the store directory passes this size (default 3072)
//	SDN_F4_STALL     a write in flight this long is reported as a stall (default 30s)
//	SDN_F4_WRITEHANG the engine's Options.WriteHang (default format4.DefaultWriteHang)
//	SDN_F4_DUMPDIR   where a stall's goroutine dump is written (default: none)
//	SDN_F4_PREWARM=1 only prepare the engine's precompiled artifact
//
// Shape "snapshot" (the first observation): each round one 50 to 89 KiB state
// record written as the ingest host call writes a "current" batch (count,
// put, supersede); every sixteenth round 256 product records (2.3 MB) and 256
// index rows the same way. Shape "marker": each round one state marker of at
// most 344 bytes (put alone); every 256th round a compacting state snapshot
// (count, put, supersede), 256 product records (2.5 MB, put alone), 256 index
// rows (put alone) and a second compacting snapshot. Shape "bigput": the
// snapshot shape with every write a bare put (no count, no supersede). Shape
// "smallcurrent": the snapshot shape with the state record a marker. Shape
// "supersedeonly": only the per-object count, put and supersede of a marker.
func TestFormat4ConcurrentFlowLoad(t *testing.T) {
	ws, ok := os.LookupEnv("SDN_F4_STRESS")
	if !ok || strings.TrimSpace(ws) == "" {
		t.Skip("set SDN_F4_STRESS (the writer count) to run the storage load")
	}
	w, err := strconv.Atoi(strings.TrimSpace(ws))
	if err != nil || w < 0 {
		t.Fatalf("SDN_F4_STRESS=%q", ws)
	}
	readers := f4LoadInt(t, "SDN_F4_READERS", 2)
	populate := f4LoadInt(t, "SDN_F4_POPULATE", 0)
	stateKB := f4LoadInt(t, "SDN_F4_STATEKB", 0)
	maxTraps := f4LoadInt(t, "SDN_F4_MAXTRAPS", 0)
	maxBytes := int64(f4LoadInt(t, "SDN_F4_MAXMB", 3072)) << 20
	duration := f4LoadDuration(t, "SDN_F4_DURATION", 5*time.Minute)
	stallAfter := f4LoadDuration(t, "SDN_F4_STALL", 30*time.Second)
	writeHang := f4LoadDuration(t, "SDN_F4_WRITEHANG", format4.DefaultWriteHang)
	shape := os.Getenv("SDN_F4_SHAPE")
	if shape == "" {
		shape = "snapshot"
	}
	switch shape {
	case "snapshot", "marker", "bigput", "smallcurrent", "supersedeonly":
	default:
		t.Fatalf("SDN_F4_SHAPE=%q", shape)
	}
	serial := os.Getenv("SDN_F4_SERIAL")
	mode := format4.SerializeOff
	switch serial {
	case "":
	case "writes":
		mode = format4.SerializeWrites
	case "all":
		mode = format4.SerializeAll
	default:
		t.Fatalf("SDN_F4_SERIAL=%q", serial)
	}
	real := format4TestWasm(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache = filepath.Join(cache, "sdn-format4-test-aot")
	if os.Getenv("SDN_F4_PREWARM") == "1" {
		path, held, err := flatsqlrt.PrewarmP4ThreadsAOT(cache)
		t.Logf("prewarm path=%s existing=%v err=%v", path, held, err)
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	// Callbacks outlive the test body (a reopen runs on its own goroutine), so
	// they write to stderr, never through t.
	began := time.Now()
	var logMu sync.Mutex
	logf := func(format string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(os.Stderr, "F4LOAD +%.3fs "+format+"\n", append([]any{time.Since(began).Seconds()}, a...)...)
	}

	type trap struct {
		phase string
		at    time.Duration
		msg   string
	}
	type flight struct {
		op    string
		since time.Time
		write bool
	}
	var (
		phase      atomic.Value // "open", "populate", "load"
		phaseStart atomic.Int64
		trapMu     sync.Mutex
		traps      []trap
		opens      atomic.Int64
		answered   atomic.Int64 // the open whose first answered write was logged
		openAt     atomic.Int64
		active     sync.Map // id -> flight
		stop       = make(chan struct{})
		endOnce    sync.Once
		endReason  atomic.Value
	)
	setPhase := func(p string) {
		phase.Store(p)
		phaseStart.Store(time.Now().UnixNano())
	}
	setPhase("open")
	end := func(reason string) {
		endOnce.Do(func() {
			endReason.Store(reason)
			logf("END %s", reason)
			close(stop)
		})
	}
	loadTraps := func() (n int) {
		trapMu.Lock()
		defer trapMu.Unlock()
		for _, tr := range traps {
			if tr.phase == "load" {
				n++
			}
		}
		return n
	}

	prev := format4OpenEngine
	format4OpenEngine = func(ctx context.Context, opt format4.Options) (format4.API, error) {
		opt.Wasm = real
		opt.AOTCacheDir = cache
		opt.CompileOnMiss = false
		opt.Serialize = mode
		opt.WriteHang = writeHang
		failure := opt.OnFailure
		opt.OnFailure = func(err error) {
			p, _ := phase.Load().(string)
			tr := trap{phase: p, at: time.Duration(time.Now().UnixNano() - phaseStart.Load()), msg: err.Error()}
			trapMu.Lock()
			traps = append(traps, tr)
			trapMu.Unlock()
			logf("TRAP phase=%s at=%s opens=%d %v", tr.phase, tr.at.Round(time.Millisecond), opens.Load(), err)
			active.Range(func(k, v any) bool {
				f := v.(flight)
				logf("  inflight id=%v op=%s for=%s", k, f.op, time.Since(f.since).Round(time.Millisecond))
				return true
			})
			if failure != nil {
				failure(err)
			}
			if maxTraps > 0 && loadTraps() >= maxTraps {
				end(fmt.Sprintf("%d traps", maxTraps))
			}
		}
		api, err := prev(ctx, opt)
		if err == nil {
			openAt.Store(time.Now().UnixNano())
			logf("OPEN #%d", opens.Add(1))
		} else {
			logf("OPEN failed: %v", err)
		}
		return api, err
	}
	defer func() { format4OpenEngine = prev }()

	if n := len(flowMarkerRecord("synthetic", 7_999_999)); n > 344 {
		t.Fatalf("the state marker is %d bytes, over 344", n)
	}
	logf("RECORDS marker=%d snapshot=%d/%d product=%d index=%d bytes", len(flowMarkerRecord("synthetic", 7_999_999)),
		len(flowSnapshotRecord("synthetic", 1, 32<<10)), len(flowSnapshotRecord("synthetic", 1, 24<<10)), len(flowProductRecord(1, 1200)),
		len(flowIndexRecord(1)))

	root := t.TempDir()
	s := openFormat4ForTest(t, root)
	defer s.Close()

	type series struct {
		mu sync.Mutex
		d  []time.Duration
	}
	var (
		refusedWrites, refusedReads atomic.Int64
		puts, records, putBytes     atomic.Int64
		reads, stalls               atomic.Int64
		statMu                      sync.Mutex
		stats                       = map[string]*series{}
	)
	observe := func(kind string, d time.Duration) {
		statMu.Lock()
		sr := stats[kind]
		if sr == nil {
			sr = &series{}
			stats[kind] = sr
		}
		statMu.Unlock()
		sr.mu.Lock()
		if len(sr.d) < 2_000_000 {
			sr.d = append(sr.d, d)
		}
		sr.mu.Unlock()
	}
	call := func(id int, op string, write bool, fn func() error) error {
		began := time.Now()
		active.Store(id, flight{op: op, since: began, write: write})
		err := fn()
		active.Delete(id)
		if err != nil {
			n := refusedReads.Add(1)
			if write {
				n = refusedWrites.Add(1)
			}
			if n <= 12 {
				logf("REFUSED op=%s write=%v after=%s err=%v", op, write, time.Since(began).Round(time.Millisecond), err)
			}
			return err
		}
		observe(op, time.Since(began))
		return nil
	}
	put := func(id int, kind, schema string, tags SourceTags, recs [][]byte) error {
		err := call(id, "put/"+kind, true, func() error {
			_, e := s.StoreBatchWithSourceTags(schema, recs, "synthetic-peer", nil, tags)
			return e
		})
		if err != nil {
			return err
		}
		puts.Add(1)
		records.Add(int64(len(recs)))
		var n int64
		for _, r := range recs {
			n += int64(len(r))
		}
		putBytes.Add(n)
		if o := opens.Load(); o > 1 && answered.Load() < o && answered.Swap(o) < o {
			logf("WRITE_ANSWERED after OPEN #%d (+%s)", o, time.Duration(time.Now().UnixNano()-openAt.Load()).Round(time.Millisecond))
		}
		return nil
	}
	// current is the ingest host call with reconcile "current"
	// (modulert/caps handleIngestWithSource): count, put, supersede.
	current := func(id int, kind, schema string, tags SourceTags, recs [][]byte) error {
		if err := call(id, "count/"+kind, false, func() error {
			_, e := s.ReconcileSourceBatch(schema, tags.ProviderID, tags.SourceName, tags.BatchID, false)
			return e
		}); err != nil {
			return err
		}
		if err := put(id, kind, schema, tags, recs); err != nil {
			return err
		}
		return call(id, "supersede/"+kind, true, func() error {
			_, e := s.ReconcileSourceBatch(schema, tags.ProviderID, tags.SourceName, tags.BatchID, true)
			return e
		})
	}
	const provider = "synthetic"
	// bulkRound is the snapshot shape's round with its write (current or a
	// bare put) and its state record chosen: shapes bigput and smallcurrent
	// take one of the two apart.
	bulkRound := func(id, round int, write func(int, string, string, SourceTags, [][]byte) error, state []byte) error {
		tags := SourceTags{ProviderID: provider, SourceName: fmt.Sprintf("synthetic-state-%d", id), BatchID: fmt.Sprintf("b-%d-%d", id, round)}
		if err := write(id, "state", "NEV.fbs", tags, [][]byte{state}); err != nil {
			return err
		}
		if round%16 != 0 {
			return nil
		}
		for _, typ := range []string{"OCM", "CAT"} {
			recs := make([][]byte, 256)
			size, kind := 128, "index"
			if typ == "OCM" {
				size, kind = 9000, "product"
			}
			for j := range recs {
				recs[j] = flowLoadRecord(typ, id*1000000+round*256+j, size)
			}
			tags.SourceName = "synthetic-" + typ
			if err := write(id, kind, typ+".fbs", tags, recs); err != nil {
				return err
			}
		}
		return nil
	}
	stateSize := func(round int) int {
		if stateKB > 0 {
			return stateKB << 10
		}
		return 50*1024 + round%40*1024
	}
	snapshotRound := func(id, round int) error {
		return bulkRound(id, round, current, flowLoadRecord("NEV", id*1000000+round, stateSize(round)))
	}
	markerRound := func(id, round int) error {
		state := fmt.Sprintf("catalog-state-%d", id)
		n := id*1000000 + round
		marker := flowMarkerRecord(provider, n)
		if len(marker) > 344 {
			return fmt.Errorf("marker is %d bytes", len(marker))
		}
		if err := put(id, "marker", "NEV.fbs", SourceTags{ProviderID: provider, SourceName: state,
			BatchID: fmt.Sprintf("catalog-attempt-%d-%d", id, round)}, [][]byte{marker}); err != nil {
			return err
		}
		if round%256 != 255 {
			return nil
		}
		cycle := round / 256
		snapshot := func(part string, size int) error {
			return current(id, "snapshot", "NEV.fbs", SourceTags{ProviderID: provider, SourceName: state,
				BatchID: fmt.Sprintf("catalog-attempt-%d-%d-%s", id, cycle, part)}, [][]byte{flowSnapshotRecord(provider, n, size)})
		}
		if err := snapshot("prepared", 32<<10); err != nil {
			return err
		}
		products, index := make([][]byte, 256), make([][]byte, 256)
		for j := range products {
			products[j] = flowProductRecord(id*1000000+cycle*256+j, 1200)
			index[j] = flowIndexRecord(id*1000000 + cycle%64*256 + j)
		}
		batch := fmt.Sprintf("catalog-%d-%d", id, cycle)
		if err := put(id, "product", "OCM.fbs", SourceTags{ProviderID: provider, SourceName: fmt.Sprintf("catalog-products-%d", id),
			BatchID: batch}, products); err != nil {
			return err
		}
		if err := put(id, "index", "CAT.fbs", SourceTags{ProviderID: provider, SourceName: fmt.Sprintf("catalog-index-%d", id),
			BatchID: batch + ":complete"}, index); err != nil {
			return err
		}
		return snapshot("stored", 24<<10)
	}
	roundOf := snapshotRound
	switch shape {
	case "marker":
		roundOf = markerRound
	case "bigput":
		roundOf = func(id, round int) error {
			return bulkRound(id, round, put, flowLoadRecord("NEV", id*1000000+round, stateSize(round)))
		}
	case "smallcurrent":
		roundOf = func(id, round int) error {
			return bulkRound(id, round, current, flowMarkerRecord(provider, id*1000000+round))
		}
	case "supersedeonly":
		roundOf = func(id, round int) error {
			tags := SourceTags{ProviderID: provider, SourceName: fmt.Sprintf("synthetic-state-%d", id), BatchID: fmt.Sprintf("b-%d-%d", id, round)}
			return current(id, "state", "NEV.fbs", tags, [][]byte{flowMarkerRecord(provider, id*1000000+round)})
		}
	}
	writer := func(id, rounds int) (done int) {
		for round := 0; rounds == 0 || round < rounds; round++ {
			select {
			case <-stop:
				return done
			default:
			}
			if err := roundOf(id, round); err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			done++
		}
		return done
	}

	// The watchdog: disk bound, and writes that are never answered.
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		reported := map[string]bool{}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for n := 0; ; n++ {
			select {
			case <-watchDone:
				return
			case <-tick.C:
			}
			active.Range(func(k, v any) bool {
				f := v.(flight)
				key := fmt.Sprintf("%v/%d", k, f.since.UnixNano())
				if !f.write || time.Since(f.since) < stallAfter || reported[key] {
					return true
				}
				reported[key] = true
				logf("STALL id=%v op=%s unanswered for %s; opens=%d reopening=%v", k, f.op, time.Since(f.since).Round(time.Second),
					opens.Load(), s.f4.reopening.Load())
				if stalls.Add(1) == 1 {
					f4LoadDump(logf, "stall")
				}
				return true
			})
			if n%5 == 4 && maxBytes > 0 {
				if size := f4LoadDirBytes(root); size > maxBytes {
					end(fmt.Sprintf("store passed %d MiB", maxBytes>>20))
				}
			}
		}
	}()

	if populate > 0 {
		setPhase("populate")
		t0 := time.Now()
		done := writer(0, populate)
		trapMu.Lock()
		n := len(traps)
		trapMu.Unlock()
		logf("POPULATED rounds=%d of %d in %s traps=%d store=%d MiB", done, populate, time.Since(t0).Round(time.Millisecond), n,
			f4LoadDirBytes(root)>>20)
		for deadline := time.Now().Add(30 * time.Second); s.f4.reopening.Load() && time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
		}
		time.Sleep(2 * time.Second)
	}

	setPhase("load")
	basePuts, baseRecords, baseBytes := puts.Load(), records.Load(), putBytes.Load()
	statMu.Lock()
	stats = map[string]*series{}
	statMu.Unlock()
	start := time.Now()
	timer := time.AfterFunc(duration, func() { end("duration") })
	defer timer.Stop()
	var wg sync.WaitGroup
	for id := 0; id < w; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			writer(id, 0)
		}(id)
	}
	for id := w; id < w+readers; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if call(id, "index-page", false, func() error {
					_, _, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "CAT.fbs", Limit: 256})
					return err
				}) == nil {
					reads.Add(1)
				}
				time.Sleep(time.Millisecond)
			}
		}(id)
	}
	<-stop
	elapsed := time.Since(start)
	setPhase("drain") // a trap from here on is not the timed load's
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	hung := 0
	select {
	case <-finished:
	case <-time.After(45 * time.Second):
		active.Range(func(k, v any) bool {
			f := v.(flight)
			hung++
			logf("HUNG at the end: id=%v op=%s unanswered for %s; opens=%d reopening=%v", k, f.op, time.Since(f.since).Round(time.Second),
				opens.Load(), s.f4.reopening.Load())
			return true
		})
		f4LoadDump(logf, "end")
		// Cancellation of the store's context bounds the diagnostic.
		s.f4.cancel()
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			logf("calls still in flight 30 s after the store's context was canceled")
		}
	}
	watchDone <- struct{}{}

	trapMu.Lock()
	all := slices.Clone(traps)
	trapMu.Unlock()
	first, inLoad, inPopulate := "none", 0, 0
	for _, tr := range all {
		switch tr.phase {
		case "load":
			if inLoad == 0 {
				first = tr.at.Round(time.Millisecond).String()
			}
			inLoad++
		default:
			inPopulate++
		}
	}
	secs := elapsed.Seconds()
	reason, _ := endReason.Load().(string)
	dp, dr, db := puts.Load()-basePuts, records.Load()-baseRecords, putBytes.Load()-baseBytes
	logf("RESULT W=%d R=%d serial=%q shape=%s statekb=%d writehang=%s populate=%d elapsed=%s end=%q traps=%d first_trap=%s other_phase_traps=%d refused_writes=%d refused_reads=%d stalls=%d hung_at_end=%d opens=%d puts=%d records=%d MiB=%.1f puts_per_s=%.2f records_per_s=%.1f MiB_per_s=%.2f reads=%d reads_per_s=%.1f store_MiB=%d",
		w, readers, serial, shape, stateKB, writeHang, populate, elapsed.Round(time.Millisecond), reason, inLoad, first, inPopulate, refusedWrites.Load(),
		refusedReads.Load(), stalls.Load(), hung, opens.Load(), dp, dr, float64(db)/(1<<20), float64(dp)/secs, float64(dr)/secs,
		float64(db)/(1<<20)/secs, reads.Load(), float64(reads.Load())/secs, f4LoadDirBytes(root)>>20)
	for i, tr := range all {
		logf("TRAPLIST %d phase=%s at=%s %s", i+1, tr.phase, tr.at.Round(time.Millisecond), tr.msg)
	}
	statMu.Lock()
	kinds := make([]string, 0, len(stats))
	for k := range stats {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	for _, k := range kinds {
		d := stats[k].d
		slices.Sort(d)
		if len(d) == 0 {
			continue
		}
		var sum time.Duration
		for _, x := range d {
			sum += x
		}
		logf("LATENCY op=%s n=%d p50=%s p95=%s p99=%s max=%s mean=%s", k, len(d), d[len(d)/2], d[len(d)*95/100], d[len(d)*99/100],
			d[len(d)-1], sum/time.Duration(len(d)))
	}
	statMu.Unlock()
}

func f4LoadInt(t *testing.T, name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		t.Fatalf("%s=%q", name, v)
	}
	return n
}

func f4LoadDuration(t *testing.T, name string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		t.Fatalf("%s=%q", name, v)
	}
	return d
}

// f4LoadDirBytes is the size of the files under root (files that vanish
// meanwhile are skipped).
func f4LoadDirBytes(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}

// f4LoadDump writes every goroutine's stack to SDN_F4_DUMPDIR.
func f4LoadDump(logf func(string, ...any), what string) {
	dir := os.Getenv("SDN_F4_DUMPDIR")
	if dir == "" {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("goroutines-%s-%d.txt", what, time.Now().Unix()))
	f, err := os.Create(path)
	if err != nil {
		logf("goroutine dump: %v", err)
		return
	}
	defer f.Close()
	_ = pprof.Lookup("goroutine").WriteTo(f, 2)
	logf("goroutine dump: %s", path)
}

// flowLoadRecord is a record whose bulk is one text field of size bytes.
func flowLoadRecord(typ string, n, size int) []byte {
	b := flatbuffers.NewBuilder(size + 256)
	text := b.CreateString(fmt.Sprintf("synthetic-%d-", n) + strings.Repeat("x", size))
	var root flatbuffers.UOffsetT
	switch typ {
	case "NEV":
		NEV.NEVStart(b)
		NEV.NEVAddNODE_ID(b, text)
		NEV.NEVAddGENERATED_AT_MS(b, int64(n))
		root = NEV.NEVEnd(b)
	case "CAT":
		CAT.CATStart(b)
		CAT.CATAddOBJECT_NAME(b, text)
		CAT.CATAddNORAD_CAT_ID(b, uint32(n+1))
		root = CAT.CATEnd(b)
	case "OCM":
		OCM.MetadataStart(b)
		OCM.MetadataAddOBJECT_NAME(b, text)
		meta := OCM.MetadataEnd(b)
		OCM.OCMStart(b)
		OCM.OCMAddMETADATA(b, meta)
		root = OCM.OCMEnd(b)
	}
	b.FinishSizePrefixedWithFileIdentifier(root, []byte("$"+typ))
	return append([]byte(nil), b.FinishedBytes()...)
}

// flowNEV is a node event log of the given events.
func flowNEV(provider string, n int, events int, detail func(i int) string) []byte {
	b := flatbuffers.NewBuilder(1024)
	offs := make([]flatbuffers.UOffsetT, events)
	for i := range offs {
		subject := b.CreateString(fmt.Sprintf("object-%d", n+i))
		text := b.CreateString(detail(i))
		NEV.NEVEventStart(b)
		NEV.NEVEventAddTIME_MS(b, int64(1_800_000_000_000+n+i))
		NEV.NEVEventAddSUBJECT(b, subject)
		NEV.NEVEventAddCOUNT(b, uint64(i+1))
		NEV.NEVEventAddDETAIL(b, text)
		offs[i] = NEV.NEVEventEnd(b)
	}
	NEV.NEVStartEVENTSVector(b, events)
	for i := events - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	vec := b.EndVector(events)
	node := b.CreateString(provider)
	NEV.NEVStart(b)
	NEV.NEVAddNODE_ID(b, node)
	NEV.NEVAddGENERATED_AT_MS(b, int64(1_800_000_000_000+n))
	NEV.NEVAddEVENTS(b, vec)
	b.FinishSizePrefixedWithFileIdentifier(NEV.NEVEnd(b), []byte("$NEV"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// flowMarkerRecord is one attempt marker: a log of one event, under 344 bytes.
func flowMarkerRecord(provider string, n int) []byte {
	return flowNEV(provider, n, 1, func(int) string {
		return fmt.Sprintf("attempt=%d state=dispatched seed=%016x generation=%08x note=%s", n, uint64(n)*0x9e3779b97f4a7c15, n, strings.Repeat("m", 104))
	})
}

// flowSnapshotRecord is a compacting state snapshot of about size bytes: one
// event per object of a 256-object batch.
func flowSnapshotRecord(provider string, n, size int) []byte {
	per := max(size/256-72, 16)
	return flowNEV(provider, n, 256, func(i int) string {
		return fmt.Sprintf("attempt=%d state=stored ", n+i) + strings.Repeat("s", per)
	})
}

// flowProductRecord is an orbit product whose bulk is numeric state data.
func flowProductRecord(n, doubles int) []byte {
	b := flatbuffers.NewBuilder(doubles*8 + 512)
	OCM.OCMStartSTATE_DATAVector(b, doubles)
	for i := doubles - 1; i >= 0; i-- {
		b.PrependFloat64(float64(n) + float64(i)*0.001953125)
	}
	data := b.EndVector(doubles)
	name := b.CreateString(fmt.Sprintf("SYNTHETIC OBJECT %d", n))
	OCM.MetadataStart(b)
	OCM.MetadataAddOBJECT_NAME(b, name)
	meta := OCM.MetadataEnd(b)
	OCM.OCMStart(b)
	OCM.OCMAddMETADATA(b, meta)
	OCM.OCMAddSTATE_DATA(b, data)
	b.FinishSizePrefixedWithFileIdentifier(OCM.OCMEnd(b), []byte("$OCM"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// flowIndexRecord is one catalog index row.
func flowIndexRecord(n int) []byte {
	b := flatbuffers.NewBuilder(256)
	name := b.CreateString(fmt.Sprintf("SYNTHETIC OBJECT %d", n))
	id := b.CreateString(fmt.Sprintf("2026-%03dA", n%1000))
	CAT.CATStart(b)
	CAT.CATAddOBJECT_NAME(b, name)
	CAT.CATAddOBJECT_ID(b, id)
	CAT.CATAddNORAD_CAT_ID(b, uint32(n+1))
	CAT.CATAddPERIOD(b, 90+float64(n%600)/10)
	CAT.CATAddINCLINATION(b, float64(n%180))
	b.FinishSizePrefixedWithFileIdentifier(CAT.CATEnd(b), []byte("$CAT"))
	return append([]byte(nil), b.FinishedBytes()...)
}
