// Command sds-stream-bench streams FlatBuffer records across EVERY embedded
// SDS standard into the real on-disk record store and reports what the node
// actually sustains.
//
// OWNER REQUIREMENT (verbatim): "we should be able to stream 1gb worth of
// flatbuffers (using all schemas) to disk as a test, and it should be able to
// be done at wire speed minus back pressure latency."
//
// "Wire speed minus back pressure" is a RATIO, so a bare MB/s answers nothing.
// The same synthesised stream is therefore driven four times — synthesis only,
// a buffered append (wire speed), the same append fsynced at the store's own
// commit cadence (the durable floor), and the store — and the headline is the
// store's share of each. Two facts decide whether the number means anything at
// all and are printed before it:
//
//   - THE ENGINE MODE. FlatSQL runs AOT-compiled or INTERPRETED, and
//     storage/flatsql.go logs which at every open because production has been
//     burned by a silent fallback twice. A throughput number that does not say
//     which one it measured is noise, so the run prewarms the AOT artifact and
//     refuses to report an interpreted number unless explicitly asked to.
//
//     The ~65-100x the codebase quotes is a QUERY-path figure and does NOT
//     reproduce here: measured on this write path, interpreted sustained
//     178 rec/s against ~493 rec/s for the same AOT configuration — about
//     2.8x, because this path is fsync-dominated rather than engine-CPU
//     dominated. The gate stays, because comparing runs across engine modes
//     is still meaningless; the reason is 2.8x, not 100x.
//
//   - PER-SCHEMA COVERAGE. "Using all schemas" means all 236, not a sample,
//     so the run reports how many standards actually took records.
// WHERE THE COST ACTUALLY IS, measured 2026-09-16 on a Mac Studio (APFS,
// arm64, libwasmedge 0.16.4, engine AOT, 236/236 standards). Recorded because
// three of these are NEGATIVE results that each look like an obvious fix:
//
//   - IT IS NOT PER-TRANSACTION. Sweeping storeWriteChunkSize 64 -> 256 ->
//     1024 gives 308 / 299 / 313 rec/s: a 16x larger commit window changes
//     nothing. So DO NOT raise that constant to chase throughput — it is 64 to
//     bound write-lock hold time after the 2026-07-06 blackout (API reads
//     waited >11 min on RLock), and raising it spends that latency budget for
//     no gain at all.
//   - IT IS NOT SQL ROUND-TRIP LATENCY, mostly. Measured through the WASM
//     engine: 38 us for a trivial SELECT, 95 us for an sdn_record_index
//     lookup. At ~5 statements per record that is ~0.25-0.5 ms against a
//     ~3 ms per-record cost — about 15%.
//   - IT IS NOT THE 236-SCHEMA FAN-OUT. Round-robin across every standard is
//     FASTER than concentrating on one (581 vs 423 rec/s for OMM alone).
//
//   - THE ENGINE VTAB MIRROR IS ~30-45%. An engine-routed standard sustains
//     461 rec/s; the control-rows-only standards, which skip the mirror
//     entirely, reach 599 (KMF) and 819 (VCM) rec/s.
//   - THE REST IS THE CONTROL-DB WRITE PATH ITSELF. Even with no mirror at
//     all, 819 rec/s is still ~10x under the fsync floor, because a store
//     write is not an append: it dedupes by CID, supersedes by key, inserts
//     the routed row, and maintains the record index.
//
// So the gap is structural and distributed, not a hotspot. Closing it means
// changing what an ingest does — an append-only log with indexing and the
// engine mirror moved off the write path — not tuning a constant.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func main() {
	var (
		totalBytes  = flag.String("bytes", "1GiB", "total record bytes to stream (e.g. 1GiB, 256MiB, 1073741824)")
		dir         = flag.String("dir", "", "target directory for the store and the baseline file (default: a fresh temp directory, removed on exit)")
		schemaList  = flag.String("schemas", "", "comma-separated schema subset (e.g. OMM.fbs,CAT.fbs); default: every embedded standard")
		schemaDir   = flag.String("schema-dir", "", "directory holding the embedded .fbs IDLs (default: located by walking up from the working directory)")
		recordBytes = flag.Int("record-bytes", 1024, "target size of one synthesised record in bytes")
		batch       = flag.Int("batch", 64, "records per StoreBatch call; 1 uses the per-record Store() path")
		producer    = flag.String("producer", "sds-stream-bench", "producer peer ID the records are attributed to")
		baseline    = flag.Bool("baseline", true, "measure the synthesis ceiling, wire speed and the durable disk floor as well as the store")
		requireAOT  = flag.Bool("require-aot", true, "fail when the FlatSQL engine is running interpreted (measured ~2.8x off on this write path; runs are not comparable across modes)")
		prewarm     = flag.Bool("prewarm", true, "AOT-compile the FlatSQL engine into the daemon's cache before opening the store")
		minRatio    = flag.Float64("min-ratio", 0, "fail when the store sustains less than this fraction of the durable disk floor (0 disables the gate)")
		verifyEng   = flag.Bool("verify-engine", true, "after the clock stops, count how many written records the engine actually holds")
		keep        = flag.Bool("keep", false, "keep the temp directory this run created (a directory named by -dir is never removed)")
		asJSON      = flag.Bool("json", false, "emit the report as JSON")
	)
	flag.Parse()

	if err := run(runConfig{
		totalBytes:  *totalBytes,
		dir:         *dir,
		schemaList:  *schemaList,
		schemaDir:   *schemaDir,
		recordBytes: *recordBytes,
		batch:       *batch,
		producer:    *producer,
		baseline:    *baseline,
		requireAOT:  *requireAOT,
		prewarm:     *prewarm,
		minRatio:    *minRatio,
		verifyEng:   *verifyEng,
		keep:        *keep,
		asJSON:      *asJSON,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "sds-stream-bench: %v\n", err)
		os.Exit(1)
	}
}

type runConfig struct {
	totalBytes  string
	dir         string
	schemaList  string
	schemaDir   string
	recordBytes int
	batch       int
	producer    string
	baseline    bool
	requireAOT  bool
	prewarm     bool
	minRatio    float64
	verifyEng   bool
	keep        bool
	asJSON      bool
}

// The four passes over the IDENTICAL stream. Named once because the report,
// the ratios and the CI gate all have to agree on which pass is the baseline.
const (
	passSynthesis    = "synthesis ceiling (no I/O)"
	passWireSpeed    = "wire speed (buffered append)"
	passDurableFloor = "durable floor (fsync per batch)"
	passStore        = "store (FlatSQLStore)"
)

// progressInterval is how often a pass reports where it is, on stderr.
const progressInterval = 15 * time.Second

// passResult is one sink's measurement over the identical stream.
type passResult struct {
	Name     string  `json:"name"`
	Seconds  float64 `json:"seconds"`
	Bytes    int64   `json:"bytes"`
	Records  int64   `json:"records"`
	MBPerSec float64 `json:"mb_per_sec"`
	RecPerS  float64 `json:"records_per_sec"`
}

type report struct {
	TargetBytes    int64            `json:"target_bytes"`
	RecordBytes    int              `json:"record_bytes_target"`
	MeanRecordSize int64            `json:"record_bytes_mean"`
	Batch          int              `json:"batch"`
	Schemas        int              `json:"schemas"`
	SchemasCovered int              `json:"schemas_covered"`
	MinPerSchema   int64            `json:"records_min_per_schema"`
	MaxPerSchema   int64            `json:"records_max_per_schema"`
	EngineMode     string           `json:"engine_mode"`
	EngineArtifact string           `json:"engine_artifact,omitempty"`
	EngineMiss     string           `json:"engine_miss_reason,omitempty"`
	StoreOpenSecs  float64          `json:"store_open_seconds"`
	Dir            string           `json:"dir"`
	Passes         []passResult     `json:"passes"`
	EngineMirrored int64            `json:"engine_records_mirrored,omitempty"`
	EngineRouted   int              `json:"engine_routed_schemas,omitempty"`
	EngineUnrouted int              `json:"engine_unrouted_schemas,omitempty"`
	StoreShare     float64          `json:"store_share_of_durable_floor,omitempty"`
	WireSpeedShare float64          `json:"store_share_of_wire_speed,omitempty"`
	PerSchema      map[string]int64 `json:"per_schema_records,omitempty"`
}

func run(cfg runConfig) error {
	target, err := parseBytes(cfg.totalBytes)
	if err != nil {
		return err
	}
	if cfg.batch < 1 {
		return fmt.Errorf("-batch must be at least 1")
	}
	if cfg.recordBytes < 32 {
		return fmt.Errorf("-record-bytes must be at least 32")
	}

	// ONLY A DIRECTORY THIS RUN CREATED IS EVER REMOVED. A gigabyte of records
	// leaves a multi-gigabyte store behind, so the temp-dir case cleans up by
	// default — but -dir names a location the operator chose, possibly one
	// holding other things, and RemoveAll on it would be a footgun no
	// benchmark flag should carry.
	dir, owned := cfg.dir, false
	if dir == "" {
		dir, err = os.MkdirTemp("", "sds-stream-bench-*")
		if err != nil {
			return fmt.Errorf("create target directory: %w", err)
		}
		owned = true
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}
	if owned && !cfg.keep {
		defer os.RemoveAll(dir)
	}

	if cfg.schemaDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		if cfg.schemaDir, err = locateSchemaDir(wd); err != nil {
			return err
		}
	}

	schemas, err := selectSchemas(cfg.schemaList)
	if err != nil {
		return err
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("create validator: %w", err)
	}
	templates, err := buildTemplates(cfg.schemaDir, schemas, validator, cfg.recordBytes)
	if err != nil {
		return err
	}

	rep := report{
		TargetBytes: target,
		RecordBytes: cfg.recordBytes,
		Batch:       cfg.batch,
		Schemas:     len(templates),
		Dir:         dir,
	}

	// PREWARM BEFORE OPENING THE STORE. The daemon never compiles at startup:
	// it loads a precompiled artifact keyed by the engine bytes plus the
	// libwasmedge version, and a miss degrades silently to the interpreter.
	if cfg.prewarm {
		path, present, err := flatsqlrt.PrewarmEngineAOT(storage.EngineAOTCacheDir())
		if err != nil {
			return fmt.Errorf("prewarm FlatSQL engine AOT artifact: %w\n"+
				"the linked libwasmedge has no AOT compiler; re-run with -prewarm=false -require-aot=false to measure the interpreted floor knowingly", err)
		}
		status := "compiled"
		if present {
			status = "already present"
		}
		fmt.Fprintf(os.Stderr, "flatsql engine AOT artifact: %s (%s)\n", path, status)
	}

	openStart := time.Now()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
	if err != nil {
		return fmt.Errorf("open record store: %w", err)
	}
	defer store.Close()
	rep.StoreOpenSecs = time.Since(openStart).Seconds()

	engine, _ := store.EngineRuntime()
	if engine == nil {
		return errors.New("record store opened without an engine runtime")
	}
	mode := engine.Mode()
	if mode.AOT {
		rep.EngineMode = "AOT"
		rep.EngineArtifact = mode.ArtifactPath
	} else {
		rep.EngineMode = "INTERPRETED"
		rep.EngineMiss = mode.MissReason
		if cfg.requireAOT {
			return fmt.Errorf("FlatSQL engine is running INTERPRETED (%s): measured ~2.8x off on this fsync-dominated write path, and not comparable with an AOT run. Run `spacedatanetwork prewarm-aot`, or pass -require-aot=false to measure it deliberately", mode.MissReason)
		}
	}

	if cfg.baseline {
		ceiling, _, err := stream(passSynthesis, templates, target, cfg.batch, discardSink{})
		if err != nil {
			return err
		}
		ceiling.Name = passSynthesis
		rep.Passes = append(rep.Passes, ceiling)

		for _, floor := range []struct {
			name      string
			syncEvery bool
		}{
			{passWireSpeed, false},
			{passDurableFloor, true},
		} {
			path := filepath.Join(dir, "floor.bin")
			fs, err := newFileSink(path, floor.syncEvery)
			if err != nil {
				return fmt.Errorf("open %s file: %w", floor.name, err)
			}
			res, _, err := stream(floor.name, templates, target, cfg.batch, fs)
			if err != nil {
				return err
			}
			res.Name = floor.name
			rep.Passes = append(rep.Passes, res)
			// The floor file is the run's own scratch, not a result: remove it
			// so the next pass is not competing for the free space it just
			// consumed, and the store gets a clean filesystem.
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove %s file: %w", floor.name, err)
			}
		}
	}

	storeRes, perSchema, err := stream(passStore, templates, target, cfg.batch, &storeSink{
		store:    store,
		producer: cfg.producer,
		single:   cfg.batch == 1,
	})
	if err != nil {
		return err
	}
	storeRes.Name = passStore
	rep.Passes = append(rep.Passes, storeRes)

	rep.PerSchema = perSchema
	rep.MinPerSchema, rep.MaxPerSchema, rep.SchemasCovered = coverage(perSchema)
	if storeRes.Records > 0 {
		rep.MeanRecordSize = storeRes.Bytes / storeRes.Records
	}
	if cfg.baseline {
		for _, p := range rep.Passes {
			switch {
			case p.Name == passWireSpeed && p.MBPerSec > 0:
				rep.WireSpeedShare = storeRes.MBPerSec / p.MBPerSec
			case p.Name == passDurableFloor && p.MBPerSec > 0:
				rep.StoreShare = storeRes.MBPerSec / p.MBPerSec
			}
		}
	}

	// AFTER THE CLOCK, NEVER INSIDE IT. A throughput number is only about the
	// write path if the records actually travelled it: the engine mirror
	// SKIPS a record it cannot place and answers no error while doing it
	// (engineIngestablePayload exists because that silent drop once looked
	// like a successful ingest), so a run that measured 1 GiB of writes the
	// engine dropped would report a number for nothing.
	if cfg.verifyEng {
		for _, t := range templates {
			n, err := store.EngineRecordCount(t.schema)
			if err != nil {
				// Not routed (the field-sealed standard, the one with no file
				// identifier): its records are control rows only, by design.
				rep.EngineUnrouted++
				continue
			}
			rep.EngineRouted++
			rep.EngineMirrored += n
		}
	}

	if err := emit(rep, cfg.asJSON); err != nil {
		return err
	}
	if rep.SchemasCovered != len(templates) {
		return fmt.Errorf("only %d of %d standards took records: raise -bytes so every schema is exercised", rep.SchemasCovered, len(templates))
	}
	// THE GATE NEEDS A FLOOR TO COMPARE AGAINST. StoreShare is only computed
	// when the baseline passes run, so -min-ratio with -baseline=false used to
	// compare against a floor that was never measured and fail every time —
	// reporting "0.00% of the durable disk floor" for a floor that does not
	// exist. Refuse the combination instead of inventing a verdict.
	if cfg.minRatio > 0 && !cfg.baseline {
		return errors.New("-min-ratio needs the disk baselines it compares against: drop -baseline=false, or drop -min-ratio")
	}
	if cfg.minRatio > 0 && rep.StoreShare < cfg.minRatio {
		return fmt.Errorf("store sustained %.2f%% of the durable disk floor, below the -min-ratio gate of %.2f%%", rep.StoreShare*100, cfg.minRatio*100)
	}
	return nil
}

// stream synthesises records until target bytes have been produced and hands
// them to the sink one schema-batch at a time. The stream is never
// materialized: at most `batch` records exist at once, which is what makes a
// 1 GiB run a stream and not an allocation.
func stream(name string, templates []*schemaTemplate, target int64, batch int, dst sink) (passResult, map[string]int64, error) {
	builder := flatbuffers.NewBuilder(8192)
	records := make([][]byte, 0, batch)
	// One arena, truncated per batch: FinishedBytes aliases the builder, which
	// the very next record overwrites, so each record is copied out. The arena
	// is only refilled after the sink has consumed the batch. A record larger
	// than the arena's capacity reallocates it, which is safe rather than
	// clever — the slices already handed out keep pointing at the old backing
	// array, and that array still holds exactly their bytes.
	arena := make([]byte, 0, batch*8192)
	perSchema := make(map[string]int64, len(templates))

	var produced, count int64
	var seq uint64
	started := time.Now()
	// A gigabyte through the store takes tens of minutes, and a benchmark that
	// prints nothing for that long is indistinguishable from a hung one.
	// Progress goes to stderr so -json stdout stays machine-readable.
	nextProgress := started.Add(progressInterval)
	for i := 0; produced < target; i++ {
		t := templates[i%len(templates)]
		records = records[:0]
		arena = arena[:0]
		for j := 0; j < batch && produced < target; j++ {
			seq++
			rec := t.build(builder, seq)
			start := len(arena)
			arena = append(arena, rec...)
			records = append(records, arena[start:len(arena):len(arena)])
			produced += int64(len(rec))
			count++
		}
		if len(records) == 0 {
			break
		}
		if err := dst.write(t.schema, records); err != nil {
			return passResult{}, nil, err
		}
		perSchema[t.schema] += int64(len(records))
		if now := time.Now(); now.After(nextProgress) {
			secs := now.Sub(started).Seconds()
			fmt.Fprintf(os.Stderr, "  %s: %.1f%% (%s of %s, %.1f MB/s, %.0f records/s)\n",
				name, float64(produced)*100/float64(target), humanBytes(produced), humanBytes(target),
				float64(produced)/secs/(1<<20), float64(count)/secs)
			nextProgress = now.Add(progressInterval)
		}
	}
	// CLOSED INSIDE THE CLOCK. The wire-speed pass buffers and fsyncs once at
	// close; stopping the timer before that would report the page cache's
	// throughput rather than the disk's.
	if err := dst.close(); err != nil {
		return passResult{}, nil, err
	}
	elapsed := time.Since(started)

	secs := elapsed.Seconds()
	res := passResult{
		Seconds: secs,
		Bytes:   produced,
		Records: count,
	}
	if secs > 0 {
		res.MBPerSec = float64(produced) / secs / (1 << 20)
		res.RecPerS = float64(count) / secs
	}
	return res, perSchema, nil
}

// coverage reports the thinnest and thickest per-standard record counts and
// how many standards took any records at all. "Using all schemas" is a claim
// the run has to be able to substantiate, and a count is the substantiation.
func coverage(perSchema map[string]int64) (lowest, highest int64, covered int) {
	for _, n := range perSchema {
		if n == 0 {
			continue
		}
		covered++
		if lowest == 0 || n < lowest {
			lowest = n
		}
		if n > highest {
			highest = n
		}
	}
	return lowest, highest, covered
}

func selectSchemas(list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return append([]string(nil), sds.SupportedSchemas...), nil
	}
	known := make(map[string]bool, len(sds.SupportedSchemas))
	for _, s := range sds.SupportedSchemas {
		known[s] = true
	}
	var out []string
	for _, raw := range strings.Split(list, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if !strings.HasSuffix(name, ".fbs") {
			name += ".fbs"
		}
		if !known[name] {
			return nil, fmt.Errorf("unknown schema %q", name)
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, errors.New("-schemas selected no schemas")
	}
	return out, nil
}

// parseBytes accepts a plain byte count or a binary-prefixed size. BINARY
// ONLY, on purpose: the reported MB/s divides by 1<<20, so admitting a decimal
// "GB" here would mix the two readings inside one report and make the target
// and the rate disagree by 7%.
func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), 64)
			if err != nil {
				return 0, fmt.Errorf("parse -bytes %q: %w", s, err)
			}
			return int64(n * float64(u.mult)), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse -bytes %q: %w", s, err)
	}
	return n, nil
}

func emit(rep report, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	fmt.Printf("target        : %s across %d SDS standards\n", humanBytes(rep.TargetBytes), rep.Schemas)
	fmt.Printf("record size   : %d B target, %d B mean actual\n", rep.RecordBytes, rep.MeanRecordSize)
	fmt.Printf("write path    : %s\n", writePathLabel(rep.Batch))
	fmt.Printf("store dir     : %s\n", rep.Dir)
	if rep.EngineMode == "AOT" {
		fmt.Printf("engine mode   : AOT (%s)\n", rep.EngineArtifact)
	} else {
		fmt.Printf("engine mode   : INTERPRETED (%s) — ~100x off, this number is not the node's\n", rep.EngineMiss)
	}
	fmt.Printf("store open    : %.2fs\n\n", rep.StoreOpenSecs)

	fmt.Printf("%-38s %10s %12s %12s %12s %14s\n", "pass", "elapsed", "bytes", "records", "MB/s", "records/s")
	for _, p := range rep.Passes {
		fmt.Printf("%-38s %9.2fs %12s %12d %12.1f %14.0f\n",
			p.Name, p.Seconds, humanBytes(p.Bytes), p.Records, p.MBPerSec, p.RecPerS)
	}
	fmt.Println()

	if rep.StoreShare > 0 {
		fmt.Printf("vs wire speed : %.2f%% (%.0fx slower than an unsynced sequential append of the same records)\n",
			rep.WireSpeedShare*100, 1/rep.WireSpeedShare)
		fmt.Printf("vs disk floor : %.2f%% (%.0fx slower than the same records fsynced at the same cadence)\n",
			rep.StoreShare*100, 1/rep.StoreShare)
	}
	fmt.Printf("coverage      : %d/%d standards took records (%d min, %d max per standard)\n",
		rep.SchemasCovered, rep.Schemas, rep.MinPerSchema, rep.MaxPerSchema)
	if rep.EngineRouted > 0 {
		fmt.Printf("engine mirror : %d records resident across %d routed standards (%d standards are control-rows-only)\n",
			rep.EngineMirrored, rep.EngineRouted, rep.EngineUnrouted)
	}

	// Name the THINNEST standard, not a mean: the round robin is what makes
	// "all schemas" true, and the schema it served least is the one that
	// falsifies the claim first if the rotation ever stops holding.
	names := make([]string, 0, len(rep.PerSchema))
	for name := range rep.PerSchema {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return rep.PerSchema[names[i]] < rep.PerSchema[names[j]] })
	if len(names) > 0 {
		fmt.Printf("thinnest      : %s (%d records)\n", names[0], rep.PerSchema[names[0]])
	}
	return nil
}

func writePathLabel(batch int) string {
	if batch == 1 {
		return "FlatSQLStore.Store (one record, one transaction)"
	}
	return fmt.Sprintf("FlatSQLStore.StoreBatch (%d records per call)", batch)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
