package main

// store_migrate.go: `spacedatanetwork store-migrate` (store format 2, stack
// design docs/architecture/flatsql-partition-store.md §16, A2, A3, A5, A16,
// §22.3a-2, §22.4-2; task T6 step 1).
//
// OFFLINE AND COPY-BASED. The legacy store is read through the legacy engine
// (storage.MigrationSource: the daemon's own open, no hot-window hydration),
// and every record goes through the partition store's real append path: the
// published engine's writer instance, ring entries, durable acks. The output
// is built in <out>/fsql2.migrating/ and becomes <out>/fsql2/ only at
// activation, after every check below has passed. There is no dual write and
// no boot reconciliation: after activation the partition store is the only
// record store.
//
// ORDER (§16.1 step 5, A2):
//   - phase A, per schema, in sdn_record_index rowid order: each record's
//     FIRST copy (the copy in the first producer table by name) with its
//     first source tag; every further tag row of the record is the same
//     record again with that tag (the engine appends a RETAG). A partition
//     switch inside a schema waits for the type owner to label what came
//     before, so the arrivals log keeps the legacy cid order.
//   - phase B, per table: the REPEAT copies (a table holding a CID an
//     earlier table by name also holds).
//   Licences are LICENCE entries, enqueued in a partition before its records.
//
// VERIFICATION (§16.1 step 7), a hard fail before activation:
//   - every partition's head (live_count, live_bytes) equals a recount of its
//     legacy table (the sdn_partition_record_bytes counters are checked
//     against it);
//   - lane counters, summed per tag tuple, equal a recount of the legacy tag
//     rows of live records (sdn_record_source_summary drift is reported);
//   - per schema, the v1 cid sequence (arrivals in gseq order) equals
//     sdn_record_index in rowid order, position by position with gseq = the
//     legacy rowid (each FIRST copy carries its rowid as RecordAttr
//     migrated_gseq, flatsql 3.2.0), so MaxRowID equals and every FTS rowid
//     resolves to the same CID; the engine's migrated-gseq fallback counter
//     must be 0.
//
// RESUME. Progress is journalled only after the entries it covers are acked
// (durable). A rerun continues from the journal; resent entries dedupe by CID
// in their partition (0 new rows). A journal that claims more records than
// the engine heads hold means the store lost acked data: the staging store is
// discarded and the migration starts over.
//
// ACTIVATION (A5): fsync the staging store; move control.flatsqldb (and its
// -wal, -journal, .fsdata) into pre-format2/; create a DIRECTORY named
// control.flatsqldb, so a pre-T6 binary fails instead of recreating empty
// tables; rename fsql2.migrating/fsql2 to fsql2 (its MIGRATED marker is inside);
// fsync <out>. Every step is idempotent, so a crash between them is finished
// by the next run.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

const (
	migrateStagingDir  = "fsql2.migrating"
	migrateJournalName = "migrate.json"
	migratePreFormat2  = "pre-format2"
	migrateControlName = "control.flatsqldb"
	// migrateGseqGap is 22.3a-2's margin above every legacy rowid.
	migrateGseqGap = 1 << 20
)

var (
	storeMigrateStore     string
	storeMigrateOut       string
	storeMigrateInventory bool
	storeMigrateSnapshot  string
	storeMigrateDelta     bool
	storeMigrateNoActive  bool
	storeMigratePageRows  int
)

var storeMigrateCmd = &cobra.Command{
	Use:   "store-migrate",
	Short: "Migrate a legacy record store to the FlatSQL partition store (format 2), offline",
	Long: `Copy the record store named by --config (or --store) into the FlatSQL
partition store (store format 2), verify it against the legacy store, and
activate it. The daemon must be stopped: the command takes the store lock.

  --inventory       print partitions, bytes per partition, the record-size
                    distribution per type and the datasync index shape; change
                    nothing
  --out DIR         build the partition store under DIR instead of in place
  --from-snapshot C migrate the consistent copy C while the daemon keeps
                    running (the output goes to the live store's root; nothing
                    is activated); then stop the daemon and run --delta
  --delta           copy what changed on the stopped live store since the
                    snapshot pass, verify, activate
  --no-activate     verify but leave the legacy store active

Progress is journalled; rerun the same command to resume after a failure.
Format 2 runs only with SDN_STORE_FORMAT=2, and only on an activated store.`,
	RunE: runStoreMigrate,
}

func init() {
	storeMigrateCmd.Flags().StringVar(&storeMigrateStore, "store", "", "legacy store directory (default: storage.path from --config)")
	storeMigrateCmd.Flags().StringVar(&storeMigrateOut, "out", "", "output store root (default: the store itself)")
	storeMigrateCmd.Flags().BoolVar(&storeMigrateInventory, "inventory", false, "print the inventory and change nothing")
	storeMigrateCmd.Flags().StringVar(&storeMigrateSnapshot, "from-snapshot", "", "migrate this consistent copy of the store while the daemon runs")
	storeMigrateCmd.Flags().BoolVar(&storeMigrateDelta, "delta", false, "copy what changed since the snapshot pass (daemon stopped), verify, activate")
	storeMigrateCmd.Flags().BoolVar(&storeMigrateNoActive, "no-activate", false, "verify, but do not activate format 2")
	storeMigrateCmd.Flags().IntVar(&storeMigratePageRows, "page-rows", 2000, "records per read page")
	rootCmd.AddCommand(storeMigrateCmd)
}

func runStoreMigrate(cmd *cobra.Command, args []string) error {
	store := strings.TrimSpace(storeMigrateStore)
	if store == "" {
		if strings.TrimSpace(configPath) == "" {
			return errors.New("--config or --store is required")
		}
		cfg, _, err := config.LoadResolved(configPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		store = strings.TrimSpace(cfg.Storage.Path)
		if store == "" {
			return errors.New("config has no storage.path")
		}
	}
	opts := migrateOptions{
		Store: store, Out: storeMigrateOut, Snapshot: storeMigrateSnapshot, Delta: storeMigrateDelta,
		NoActivate: storeMigrateNoActive, PageRows: storeMigratePageRows, AOTCacheDir: storage.EngineAOTCacheDir(),
	}
	out := cmd.OutOrStdout()
	if storeMigrateInventory {
		src := store
		if opts.Snapshot != "" {
			src = opts.Snapshot
		}
		inv, err := migrateInventory(src)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(inv)
	}
	rep, err := migrateStore(cmd.Context(), opts, out)
	if rep != nil {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	}
	return err
}

type migrateOptions struct {
	Store      string // the legacy store (source of a full or delta pass)
	Out        string // output root (default Store)
	Snapshot   string // --from-snapshot: the copy read by the snapshot pass
	Delta      bool
	NoActivate bool
	PageRows   int

	AOTCacheDir   string
	CompileOnMiss bool // tests only (A30: the daemon never compiles)
	Writers       uint32

	// Tests: stop after this many records have been acked (a clean abort
	// standing in for a kill between pages).
	testAbortAfter int64
}

// ---- inventory (§16.1 step 1) -------------------------------------------------------

type inventoryPartition struct {
	Table  string `json:"table"`
	Token  string `json:"producer"`
	Schema string `json:"schema"`
	storage.TableSizes
}

type inventoryReport struct {
	Store       string                 `json:"store"`
	SourceBytes int64                  `json:"source_bytes"`
	FreeBytes   int64                  `json:"free_bytes"`
	Partitions  []inventoryPartition   `json:"partitions"`
	Types       []inventoryType        `json:"types"`
	Index       []storage.IndexStats   `json:"datasync_index"`
	MaxRowID    int64                  `json:"max_rowid"`
	FTS         map[string]int64       `json:"fts_progress,omitempty"`
	GseqFloor   uint64                 `json:"gseq_floor"`
	OverEntry   []inventoryPartition   `json:"over_ring_entry,omitempty"`
	Licences    int                    `json:"licences"`
	Took        string                 `json:"took"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
}

type inventoryType struct {
	Schema     string `json:"schema"`
	Partitions int    `json:"partitions"`
	Rows       int64  `json:"rows"`
	Bytes      int64  `json:"bytes"`
	P50        int64  `json:"p50"`
	P99        int64  `json:"p99"`
	Max        int64  `json:"max"`
}

// maxEngineEntryPayload is the largest record the published engine's ring
// takes (EngineConfig::maxEntryBytes, 1 MiB + 4 KiB, less the entry header
// and a typical RecordAttr): anything above it cannot be migrated until the
// A27 jumbo path or the maxEntryBytes TLV lands.
const maxEngineEntryPayload = (1<<20 + 4096) - 72 - 4 - 1024

func migrateInventory(store string) (*inventoryReport, error) {
	start := time.Now()
	src, err := storage.OpenMigrationSource(store)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	rep := &inventoryReport{Store: store, SourceBytes: sourceBytes(store), FreeBytes: freeBytes(store)}
	tables, err := src.ProducerTables()
	if err != nil {
		return nil, err
	}
	byType := map[string]*inventoryType{}
	var lens = map[string][]int64{}
	for _, t := range tables {
		ts, err := src.TableSizes(t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}
		p := inventoryPartition{Table: t.Name, Token: t.Token, Schema: t.Schema, TableSizes: ts}
		rep.Partitions = append(rep.Partitions, p)
		if ts.Max > maxEngineEntryPayload {
			rep.OverEntry = append(rep.OverEntry, p)
		}
		it := byType[t.Schema]
		if it == nil {
			it = &inventoryType{Schema: t.Schema}
			byType[t.Schema] = it
		}
		it.Partitions++
		it.Rows += ts.Rows
		it.Bytes += ts.Bytes
		if ts.Max > it.Max {
			it.Max = ts.Max
		}
		// A type's percentiles are the partitions' at their row weight
		// (exact per partition; the type row is an upper bound for p99).
		lens[t.Schema] = append(lens[t.Schema], ts.P50, ts.P99)
		if ts.P50 > it.P50 {
			it.P50 = ts.P50
		}
		if ts.P99 > it.P99 {
			it.P99 = ts.P99
		}
	}
	for _, it := range byType {
		rep.Types = append(rep.Types, *it)
	}
	sort.Slice(rep.Types, func(i, j int) bool { return rep.Types[i].Schema < rep.Types[j].Schema })
	if rep.Index, err = src.IndexSchemas(); err != nil {
		return nil, err
	}
	if rep.MaxRowID, err = src.MaxIndexRowID(); err != nil {
		return nil, err
	}
	if rep.FTS, err = src.FullTextProgress(); err != nil {
		return nil, err
	}
	rep.GseqFloor = migrateGseqFloor(rep.MaxRowID, rep.FTS)
	lic, err := src.Licences()
	if err != nil {
		return nil, err
	}
	rep.Licences = len(lic)
	rep.Took = time.Since(start).Round(time.Millisecond).String()
	return rep, nil
}

// migrateGseqFloor is 22.3a-2: max(MAX(rowid), FTS progress) + 1 + 2^20.
func migrateGseqFloor(maxRowID int64, fts map[string]int64) uint64 {
	m := maxRowID
	for _, v := range fts {
		if v > m {
			m = v
		}
	}
	if m < 0 {
		m = 0
	}
	return uint64(m) + 1 + migrateGseqGap
}

func sourceBytes(store string) int64 {
	var n int64
	for _, name := range []string{"control.flatsqldb", "control.flatsqldb-wal", "control.flatsqldb.fsdata", "control.flatsqldb-journal"} {
		if fi, err := os.Stat(filepath.Join(store, name)); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
	}
	return n
}

func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// ---- the journal ----------------------------------------------------------------------

type migrateJournal struct {
	Version    int              `json:"version"`
	Source     string           `json:"source"`
	Mode       string           `json:"mode"` // "full" or "snapshot"
	GseqFloor  uint64           `json:"gseq_floor"`
	PhaseA     map[string]int64 `json:"phase_a"`      // schema -> last index rowid copied
	PhaseADone map[string]bool  `json:"phase_a_done"` // schema
	PhaseB     map[string]int64 `json:"phase_b"`      // table -> last rowid scanned
	PhaseBDone map[string]bool  `json:"phase_b_done"` // table
	Licences   map[string]bool  `json:"licences"`     // partition sql name -> licences enqueued
	Acked      int64            `json:"acked"`        // entries acked (records, retags, licences)
	Records    int64            `json:"records"`      // record copies acked (FIRST + REPEAT)
	Rejected   []migrateReject  `json:"rejected,omitempty"`
	// Snapshot pass: what the delta starts after.
	Watermarks map[string]int64 `json:"watermarks,omitempty"` // table -> max rowid at the snapshot
	IndexMarks map[string]int64 `json:"index_marks,omitempty"`
	TagMark    int64            `json:"tag_mark,omitempty"`  // max tag rowid at the snapshot
	TagsDone   int64            `json:"tags_done,omitempty"` // delta: tag rows applied through this rowid
	Delta      bool             `json:"delta,omitempty"`
	Verified   bool             `json:"verified"`
	Activated  bool             `json:"activated"`
}

type migrateReject struct {
	Table string `json:"table"`
	CID   string `json:"cid"`
	Code  int32  `json:"code"`
}

func newMigrateJournal(source, mode string, floor uint64) *migrateJournal {
	return &migrateJournal{Version: 1, Source: source, Mode: mode, GseqFloor: floor,
		PhaseA: map[string]int64{}, PhaseADone: map[string]bool{}, PhaseB: map[string]int64{},
		PhaseBDone: map[string]bool{}, Licences: map[string]bool{}}
}

func readMigrateJournal(path string) (*migrateJournal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var j migrateJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("migrate journal %s: %w", path, err)
	}
	for _, m := range []*map[string]int64{&j.PhaseA, &j.PhaseB} {
		if *m == nil {
			*m = map[string]int64{}
		}
	}
	for _, m := range []*map[string]bool{&j.PhaseADone, &j.PhaseBDone, &j.Licences} {
		if *m == nil {
			*m = map[string]bool{}
		}
	}
	return &j, nil
}

func (m *migrator) saveJournal() error {
	start := time.Now()
	err := m.j.write(m.jpath)
	m.journalNs += time.Since(start)
	return err
}

func (j *migrateJournal) write(path string) error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// ---- the migration --------------------------------------------------------------------

type migrateReport struct {
	Store        string                 `json:"store"`
	Out          string                 `json:"out"`
	Mode         string                 `json:"mode"`
	Records      int64                  `json:"records"`
	Entries      int64                  `json:"entries"`
	RecordBytes  int64                  `json:"record_bytes"`
	SourceBytes  int64                  `json:"source_bytes"`
	Took         string                 `json:"took"`
	RecordMBps   float64                `json:"record_mb_per_s"`
	SourceMBps   float64                `json:"source_mb_per_s"`
	Resumed      bool                   `json:"resumed"`
	Rejected     []migrateReject        `json:"rejected,omitempty"`
	Verification *migrateVerification   `json:"verification,omitempty"`
	Activated    bool                   `json:"activated"`
	Notes        []string               `json:"notes,omitempty"`
	Engine       format2.WriterStats    `json:"engine"`
	Extra        map[string]interface{} `json:"extra,omitempty"`
}

// migrator carries one run.
type migrator struct {
	opt     migrateOptions
	out     io.Writer
	srcPath string
	outRoot string
	staging string // <out>/fsql2.migrating
	jpath   string
	j       *migrateJournal

	src      *storage.MigrationSource
	native   *flatsqlrt.NativeStore
	w        *format2.Writer
	r        *format2.Reader
	tables   []storage.LegacyTable
	bySchema map[string][]storage.LegacyTable
	specs    map[string]format2.TypeSpec
	licences map[string][]storage.SourceBatchLicense // schema -> licences
	licKeys  map[string]bool                         // schema\x1fprovider\x1fsource\x1fbatch

	parts       map[string]*format2.Partition // table name -> partition
	pending     map[*format2.Partition]*pendingAcks
	rep         *migrateReport
	recordBytes int64
	// Where the time goes (reported under extra).
	readNs, enqueueNs, drainNs, journalNs, labelNs time.Duration
	lastJournal                                    time.Time
}

type pendingAcks struct {
	last    uint64
	entries map[uint64]pendingEntry
}

type pendingEntry struct {
	table  string
	cid    string
	record bool
}

func migrateStore(ctx context.Context, opt migrateOptions, out io.Writer) (*migrateReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opt.PageRows <= 0 {
		opt.PageRows = 2000
	}
	m := &migrator{opt: opt, out: out, outRoot: opt.Out, parts: map[string]*format2.Partition{},
		pending: map[*format2.Partition]*pendingAcks{}}
	if m.outRoot == "" {
		m.outRoot = opt.Store
	}
	mode := "full"
	m.srcPath = opt.Store
	switch {
	case opt.Snapshot != "" && opt.Delta:
		return nil, errors.New("--from-snapshot and --delta are separate passes")
	case opt.Snapshot != "":
		mode = "snapshot"
		m.srcPath = opt.Snapshot
	case opt.Delta:
		mode = "delta"
	}
	m.staging = filepath.Join(m.outRoot, migrateStagingDir)
	m.jpath = filepath.Join(m.staging, migrateJournalName)
	m.rep = &migrateReport{Store: m.srcPath, Out: m.outRoot, Mode: mode}
	start := time.Now()

	if ok, err := format2.Migrated(m.outRoot); err != nil {
		return nil, err
	} else if ok {
		// Already activated: finish any activation step a crash cut short.
		if err := m.finishActivation(); err != nil {
			return m.rep, err
		}
		m.rep.Activated = true
		m.rep.Notes = append(m.rep.Notes, "the store was already activated as format 2")
		return m.rep, nil
	}
	if j, err := readMigrateJournal(m.jpath); err == nil && j.Verified && !opt.NoActivate {
		if fi, err := os.Lstat(filepath.Join(m.outRoot, migrateControlName)); err == nil && fi.IsDir() {
			// A crash between retiring the legacy control database and the
			// rename: finish the activation (the staging store was verified).
			m.j = j
			if err := m.activate(); err != nil {
				return m.rep, err
			}
			m.rep.Activated = true
			m.rep.Notes = append(m.rep.Notes, "finished an interrupted activation")
			return m.rep, nil
		}
	}
	if err := os.MkdirAll(m.outRoot, 0o700); err != nil {
		return nil, err
	}
	m.rep.SourceBytes = sourceBytes(m.srcPath)
	if free := freeBytes(m.outRoot); free >= 0 && float64(free) < 1.2*float64(m.rep.SourceBytes) {
		return m.rep, fmt.Errorf("store-migrate needs %.1f GB free under %s (1.2x the %.1f GB store), has %.1f GB: use --out on another disk",
			1.2*float64(m.rep.SourceBytes)/1e9, m.outRoot, float64(m.rep.SourceBytes)/1e9, float64(free)/1e9)
	}

	var err error
	if m.src, err = storage.OpenMigrationSource(m.srcPath); err != nil {
		return m.rep, fmt.Errorf("open the legacy store (stop the daemon first): %w", err)
	}
	defer func() {
		if m.src != nil {
			m.src.Close()
		}
	}()
	if err := m.loadLegacyShape(); err != nil {
		return m.rep, err
	}
	if err := m.openJournal(mode); err != nil {
		return m.rep, err
	}
	if err := m.openEngine(); err != nil {
		return m.rep, err
	}
	defer m.closeEngine()
	if err := m.checkJournalAgainstHeads(ctx); err != nil {
		return m.rep, err
	}

	if mode == "delta" {
		err = m.runDelta(ctx)
	} else {
		err = m.runCopy(ctx)
	}
	if err != nil {
		return m.rep, err
	}
	if st, err := m.w.Stats(); err == nil {
		m.rep.Engine = st
	}
	m.rep.Extra = map[string]interface{}{"read": m.readNs.Round(time.Millisecond).String(),
		"enqueue": m.enqueueNs.Round(time.Millisecond).String(), "drain": m.drainNs.Round(time.Millisecond).String(),
		"journal": m.journalNs.Round(time.Millisecond).String(), "label_wait": m.labelNs.Round(time.Millisecond).String()}
	took := time.Since(start)
	m.rep.Took = took.Round(time.Millisecond).String()
	m.rep.Records = m.j.Records
	m.rep.Entries = m.j.Acked
	m.rep.RecordBytes = m.recordBytes
	m.rep.Rejected = m.j.Rejected
	if s := took.Seconds(); s > 0 {
		m.rep.RecordMBps = float64(m.recordBytes) / 1e6 / s
		m.rep.SourceMBps = float64(m.rep.SourceBytes) / 1e6 / s
	}
	if mode == "snapshot" {
		// The snapshot pass never activates: the delta finishes it.
		m.rep.Notes = append(m.rep.Notes, "snapshot pass complete; stop the daemon and run --delta")
		return m.rep, nil
	}

	v, err := m.verify(ctx)
	m.rep.Verification = v
	if err != nil {
		return m.rep, err
	}
	m.j.Verified = true
	if err := m.j.write(m.jpath); err != nil {
		return m.rep, err
	}
	if opt.NoActivate {
		m.rep.Notes = append(m.rep.Notes, "verified; not activated (--no-activate)")
		return m.rep, nil
	}
	// The interim control database (§14): the control tables, their indexes
	// and the interim FTS, without the record tables.
	if err := m.copyControl(); err != nil {
		return m.rep, err
	}
	// Activation needs the legacy files closed and the store lock free.
	m.closeEngine()
	m.src.Close()
	m.src = nil
	if err := m.activate(); err != nil {
		return m.rep, err
	}
	m.rep.Activated = true
	return m.rep, nil
}

func (m *migrator) logf(format string, args ...interface{}) {
	if m.out != nil {
		fmt.Fprintf(m.out, "store-migrate: "+format+"\n", args...)
	}
}

func (m *migrator) loadLegacyShape() error {
	var err error
	if m.tables, err = m.src.ProducerTables(); err != nil {
		return err
	}
	m.bySchema = map[string][]storage.LegacyTable{}
	m.specs = map[string]format2.TypeSpec{}
	for _, t := range m.tables {
		m.bySchema[t.Schema] = append(m.bySchema[t.Schema], t)
		if _, ok := m.specs[t.Schema]; !ok {
			spec, err := format2.TypeSpecFor(t.Schema)
			if err != nil {
				return fmt.Errorf("table %s: %w", t.Name, err)
			}
			m.specs[t.Schema] = spec
		}
	}
	lics, err := m.src.Licences()
	if err != nil {
		return err
	}
	m.licences = map[string][]storage.SourceBatchLicense{}
	m.licKeys = map[string]bool{}
	for _, l := range lics {
		m.licences[l.SchemaName] = append(m.licences[l.SchemaName], l)
		m.licKeys[l.SchemaName+"\x1f"+licenceKey(l.ProviderID, l.SourceName, l.BatchID)] = true
	}
	return nil
}

// licenceKey is the RecordAttr licence key of a (provider, source, batch).
func licenceKey(provider, source, batch string) string {
	return provider + "\x1f" + source + "\x1f" + batch
}

func (m *migrator) openJournal(mode string) error {
	j, err := readMigrateJournal(m.jpath)
	switch {
	case err == nil:
		if mode == "delta" {
			if j.Mode != "snapshot" {
				return fmt.Errorf("--delta needs a snapshot pass; the journal in %s is a %s pass", m.staging, j.Mode)
			}
			j.Delta = true
		} else if j.Mode != mode {
			return fmt.Errorf("the journal in %s is a %s pass; finish it (or remove %s) before a %s pass", m.staging, j.Mode, m.staging, mode)
		}
		m.j = j
		m.rep.Resumed = true
		m.logf("resuming from %s (%d records acked)", m.jpath, j.Records)
		return m.ensureStagingStore()
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if mode == "delta" {
		return fmt.Errorf("--delta needs the snapshot pass's journal at %s", m.jpath)
	}
	if _, err := os.Stat(m.staging); err == nil {
		// Killed between creating the staging directory and its first journal:
		// it holds no engine file yet, so it is ours and empty. Anything else
		// without a journal is not a migration this command started.
		if _, err := os.Stat(filepath.Join(m.staging, format2.Dir)); err == nil {
			return fmt.Errorf("%s exists without a journal: it is not a migration this command started; remove it", m.staging)
		}
	}
	maxRowID, err := m.src.MaxIndexRowID()
	if err != nil {
		return err
	}
	fts, err := m.src.FullTextProgress()
	if err != nil {
		return err
	}
	// The journal comes first: a staging directory always has one, so a
	// kill at any point leaves a state this command resumes.
	if err := os.MkdirAll(m.staging, 0o700); err != nil {
		return err
	}
	m.j = newMigrateJournal(m.srcPath, mode, migrateGseqFloor(maxRowID, fts))
	if err := m.j.write(m.jpath); err != nil {
		return err
	}
	if err := syncDirectory(m.outRoot); err != nil {
		return err
	}
	return m.ensureStagingStore()
}

// ensureStagingStore writes the staging store's STORE (the journal's gseq
// floor, migratedFrom = 1) and MIGRATED when absent. They sit inside the
// staging root, so the engine and its readers open it; nothing outside the
// staging directory names it until activation renames it into place.
func (m *migrator) ensureStagingStore() error {
	sf, err := format2.ReadStoreFile(m.staging)
	if errors.Is(err, os.ErrNotExist) {
		if sf, err = format2.NewStoreFile(m.j.GseqFloor, 1); err != nil {
			return err
		}
		if err := format2.WriteStoreFile(m.staging, sf); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if ok, err := format2.Migrated(m.staging); err != nil {
		return err
	} else if !ok {
		return format2.WriteMigrated(m.staging, sf.UUID)
	}
	return nil
}

func (m *migrator) openEngine() error {
	var err error
	if m.native, err = flatsqlrt.OpenNativeStore(m.outRoot); err != nil {
		return err
	}
	opt := format2.InstanceOptions{Store: m.native, AOTCacheDir: m.opt.AOTCacheDir, CompileOnMiss: m.opt.CompileOnMiss}
	writers := m.opt.Writers
	if writers == 0 {
		writers = 1
	}
	if m.w, err = format2.OpenWriter(opt, format2.WriterConfig{Root: migrateStagingDir, Writers: writers, Create: true,
		RequireMigrated: true}); err != nil {
		return fmt.Errorf("open the partition-store writer (run prewarm-aot as this user first): %w", err)
	}
	if m.r, err = format2.OpenReader(opt, flatsqlrt.PSRoleBulk, format2.ReaderConfig{Root: migrateStagingDir, Lanes: 1}); err != nil {
		return err
	}
	for schema, spec := range m.specs {
		if err := m.w.RegisterType(spec); err != nil {
			return fmt.Errorf("register %s: %w", schema, err)
		}
	}
	return nil
}

func (m *migrator) closeEngine() {
	if m.r != nil {
		_ = m.r.Stop()
		m.r = nil
	}
	if m.w != nil {
		_ = m.w.Stop()
		m.w = nil
	}
	if m.native != nil {
		m.native.Release()
		m.native = nil
	}
}

// checkJournalAgainstHeads: the heads are the authority (A5). A journal that
// counts more acked record copies than the partitions hold means acked data
// was lost; the staging store cannot be trusted and the run starts over.
func (m *migrator) checkJournalAgainstHeads(ctx context.Context) error {
	if m.j.Records == 0 {
		return nil
	}
	res, err := m.r.Query(ctx, format2.Request{SQL: "SELECT COALESCE(SUM(total_count), 0) FROM flatsql_partitions"})
	if err != nil {
		return err
	}
	var held int64
	if len(res.Rows) == 1 {
		held = res.Rows[0][0].Int64()
	}
	if held < m.j.Records-int64(len(m.j.Rejected)) {
		return fmt.Errorf("the journal counts %d acked record copies but the partition heads hold %d: remove %s and migrate again",
			m.j.Records, held, m.staging)
	}
	return nil
}

// partition returns (registering) a table's partition, enqueueing its
// licences before its first record.
func (m *migrator) partition(ctx context.Context, t storage.LegacyTable) (*format2.Partition, error) {
	if p := m.parts[t.Name]; p != nil {
		return p, nil
	}
	spec := m.specs[t.Schema]
	p, err := m.w.Partition([]byte(t.Token), spec.FID)
	if err != nil {
		return nil, err
	}
	m.parts[t.Name] = p
	if !m.j.Licences[t.Name] {
		for _, l := range m.licences[t.Schema] {
			body, _ := json.Marshal(l)
			attr := format2.BuildRecordAttr(format2.RecordAttr{PeerID: []byte(t.Token), LicenceKey: licenceKey(l.ProviderID, l.SourceName, l.BatchID)})
			if _, err := m.enqueue(ctx, p, t.Name, "", false, &format2.Entry{Kind: format2.EntLicence,
				ArrivalMs: l.UpdatedAt * 1000, Attr: attr, Frame: frameBytes(body)}); err != nil {
				return nil, err
			}
		}
		if err := m.drain(ctx); err != nil {
			return nil, err
		}
		m.j.Licences[t.Name] = true
	}
	return p, nil
}

func frameBytes(data []byte) []byte {
	f := make([]byte, 4+len(data))
	f[0], f[1], f[2], f[3] = byte(len(data)), byte(len(data)>>8), byte(len(data)>>16), byte(len(data)>>24)
	copy(f[4:], data)
	return f
}

func (m *migrator) enqueue(ctx context.Context, p *format2.Partition, table, cidText string, record bool, e *format2.Entry) (uint64, error) {
	start := time.Now()
	rseq, err := p.Enqueue(ctx, e)
	m.enqueueNs += time.Since(start)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", table, cidText, err)
	}
	pa := m.pending[p]
	if pa == nil {
		pa = &pendingAcks{entries: map[uint64]pendingEntry{}}
		m.pending[p] = pa
	}
	pa.last = rseq
	pa.entries[rseq] = pendingEntry{table: table, cid: cidText, record: record}
	return rseq, nil
}

// drain waits for every enqueued entry to be durable and records rejects.
func (m *migrator) drain(ctx context.Context) error {
	start := time.Now()
	defer func() { m.drainNs += time.Since(start) }()
	for p, pa := range m.pending {
		if err := p.WaitAck(ctx, pa.last); err != nil {
			var rej *format2.RejectError
			if !errors.As(err, &rej) {
				return err
			}
			// The last entry itself was rejected; it is collected below.
			m.noteReject(pa, rej.Rseq, rej.Code)
		}
		for rseq, code := range p.Rejects() {
			m.noteReject(pa, rseq, code)
		}
		for _, e := range pa.entries {
			m.j.Acked++
			if e.record {
				m.j.Records++
			}
		}
		delete(m.pending, p)
	}
	return nil
}

func (m *migrator) noteReject(pa *pendingAcks, rseq uint64, code int32) {
	e, ok := pa.entries[rseq]
	if !ok {
		return
	}
	delete(pa.entries, rseq)
	m.j.Rejected = append(m.j.Rejected, migrateReject{Table: e.table, CID: e.cid, Code: code})
}

// recordEntry builds the ring entry of one legacy record copy with tag t.
func (m *migrator) recordEntry(tbl storage.LegacyTable, r storage.LegacyRecord, tag *storage.LegacyTag, gseq int64) (*format2.Entry, error) {
	c, err := cid.Decode(r.CID)
	if err != nil {
		return nil, fmt.Errorf("%s: CID %q: %w", tbl.Name, r.CID, err)
	}
	cidBin := c.Bytes()
	if len(cidBin) != 36 {
		return nil, fmt.Errorf("%s: CID %s is not a CIDv1 raw sha2-256", tbl.Name, r.CID)
	}
	attr := format2.RecordAttr{PeerID: []byte(r.PeerID), SupersedeKey: r.SupersedeKey, SourceTimestamp: r.Timestamp}
	if gseq > 0 {
		// The FIRST copy's legacy sdn_record_index.rowid: the engine keeps it
		// as the gseq (flatsql 3.2.0), so datasync cursors, MaxRowID and the
		// FTS rowids carry over.
		attr.MigratedGseq = uint64(gseq)
	}
	if r.SignatureHex != "" {
		if sig, err := hex.DecodeString(r.SignatureHex); err == nil {
			attr.Signature = sig
		} else {
			attr.Signature = []byte(r.SignatureHex)
		}
	}
	if tag != nil {
		attr.Tag = format2.SourceTag{ProviderID: tag.ProviderID, SourceName: tag.SourceName, BatchID: tag.BatchID,
			ContentKeyID: tag.ContentKeyID, ProducerPeerID: tag.ProducerPeerID, ProducerPublicKey: tag.ProducerPublicKey}
		if lk := licenceKey(tag.ProviderID, tag.SourceName, tag.BatchID); m.licKeys[tbl.Schema+"\x1f"+lk] {
			attr.LicenceKey = lk
		}
	}
	e := &format2.Entry{Kind: format2.EntRecord, Flags: format2.FlagCidPresent, ArrivalMs: r.Timestamp * 1000,
		CID: cidBin, Attr: format2.BuildRecordAttr(attr), Frame: frameBytes(r.Plain)}
	if r.Sealed {
		e.Flags |= format2.FlagSealed
		e.Sealed = r.Stored
	}
	return e, nil
}

// runCopy is a full or snapshot pass: phase A, phase B, then the snapshot
// watermarks.
func (m *migrator) runCopy(ctx context.Context) error {
	schemas := make([]string, 0, len(m.bySchema))
	for s := range m.bySchema {
		schemas = append(schemas, s)
	}
	sort.Strings(schemas)
	if m.j.Mode == "snapshot" && m.j.Watermarks == nil {
		// Watermarks first: everything at or below them is in this pass.
		m.j.Watermarks = map[string]int64{}
		m.j.IndexMarks = map[string]int64{}
		for _, t := range m.tables {
			ts, err := m.src.TableSizes(t)
			if err != nil {
				return err
			}
			m.j.Watermarks[t.Name] = ts.MaxRowID
		}
		idx, err := m.src.IndexSchemas()
		if err != nil {
			return err
		}
		for _, s := range idx {
			m.j.IndexMarks[s.Schema] = s.MaxRowID
		}
		if m.j.TagMark, err = m.src.MaxTagRowID(); err != nil {
			return err
		}
		if err := m.j.write(m.jpath); err != nil {
			return err
		}
	}
	for _, schema := range schemas {
		if err := m.phaseA(ctx, schema, 0); err != nil {
			return err
		}
	}
	if err := m.waitLabeled(ctx, schemas); err != nil {
		return err
	}
	for _, t := range m.tables {
		if err := m.phaseB(ctx, t, 0); err != nil {
			return err
		}
	}
	return m.waitLabeled(ctx, schemas)
}

// A read page, produced by the reader goroutine while the previous page is
// enqueued and committed (reads overlap the engine's syncs).
type migratePage struct {
	last  int64 // the page's last rowid (index rowid in phase A, table rowid in phase B)
	short bool  // fewer rows than a full page: the end
	// phase A
	index []storage.IndexRow
	first map[string]storage.LegacyTable
	recs  map[string]storage.LegacyRecord
	tags  map[string][]storage.LegacyTag
	// phase B
	rows   []storage.LegacyRecord
	repeat map[string]bool
	err    error
}

// readPages runs next() on its own goroutine, one page ahead.
func readPages(ctx context.Context, next func() (*migratePage, bool)) <-chan *migratePage {
	ch := make(chan *migratePage, 1)
	go func() {
		defer close(ch)
		for {
			pg, more := next()
			if pg != nil {
				select {
				case ch <- pg:
				case <-ctx.Done():
					return
				}
			}
			if !more || (pg != nil && pg.err != nil) {
				return
			}
		}
	}()
	return ch
}

// journalEvery bounds how often the journal is synced (each sync is an
// fsync; a resume resends at most this much, which dedupes).
const journalEvery = 2 * time.Second

func (m *migrator) maybeJournal(force bool) error {
	if !force && time.Since(m.lastJournal) < journalEvery {
		return nil
	}
	m.lastJournal = time.Now()
	return m.saveJournal()
}

// phaseA copies the FIRST copies of schema in index rowid order.
func (m *migrator) phaseA(ctx context.Context, schema string, upto int64) error {
	if m.j.PhaseADone[schema] {
		return nil
	}
	tables := m.bySchema[schema]
	after := m.j.PhaseA[schema]
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pages := readPages(ctx, func() (*migratePage, bool) {
		readStart := time.Now()
		defer func() { m.readNs += time.Since(readStart) }()
		page, err := m.src.IndexPage(schema, after, m.opt.PageRows)
		if err != nil {
			return &migratePage{err: err}, false
		}
		short := len(page) < m.opt.PageRows
		if upto > 0 {
			for i, r := range page {
				if r.RowID > upto {
					page, short = page[:i], true
					break
				}
			}
		}
		if len(page) == 0 {
			return nil, false
		}
		pg := &migratePage{index: page, last: page[len(page)-1].RowID, short: short,
			first: map[string]storage.LegacyTable{}, recs: map[string]storage.LegacyRecord{}}
		after = pg.last
		cids := make([]string, len(page))
		for i, r := range page {
			cids[i] = r.CID
		}
		// The FIRST copy of a CID is in the first table by name holding it.
		missing := cids
		for _, t := range tables {
			if len(missing) == 0 {
				break
			}
			got, err := m.src.RecordsByCID(t, missing)
			if err != nil {
				return &migratePage{err: err}, false
			}
			var still []string
			for _, c := range missing {
				if r, ok := got[c]; ok {
					pg.first[c] = t
					pg.recs[c] = r
				} else {
					still = append(still, c)
				}
			}
			missing = still
		}
		if pg.tags, err = m.src.TagsFor(schema, cids); err != nil {
			return &migratePage{err: err}, false
		}
		return pg, !short
	})
	var lastPart *format2.Partition
	for pg := range pages {
		if pg.err != nil {
			return pg.err
		}
		for _, ir := range pg.index {
			t, ok := pg.first[ir.CID]
			if !ok {
				continue // an index row without a record (reported by verification)
			}
			p, err := m.partition(ctx, t)
			if err != nil {
				return err
			}
			if lastPart != nil && p != lastPart {
				// A partition switch: the type owner labels what came before
				// first, so arrivals keep the legacy order.
				if err := m.waitLabeled(ctx, []string{schema}); err != nil {
					return err
				}
			}
			lastPart = p
			r := pg.recs[ir.CID]
			rt := pg.tags[ir.CID]
			var tag0 *storage.LegacyTag
			if len(rt) > 0 {
				tag0 = &rt[0]
			}
			e, err := m.recordEntry(t, r, tag0, ir.RowID)
			if err != nil {
				return err
			}
			if _, err := m.enqueue(ctx, p, t.Name, r.CID, true, e); err != nil {
				return err
			}
			m.recordBytes += int64(len(r.Stored))
			for i := 1; i < len(rt); i++ {
				e, err := m.recordEntry(t, r, &rt[i], 0)
				if err != nil {
					return err
				}
				if _, err := m.enqueue(ctx, p, t.Name, r.CID, false, e); err != nil {
					return err
				}
			}
		}
		if err := m.drain(ctx); err != nil {
			return err
		}
		m.j.PhaseA[schema] = pg.last
		if err := m.maybeJournal(false); err != nil {
			return err
		}
		if m.opt.testAbortAfter > 0 && m.j.Records >= m.opt.testAbortAfter {
			return errMigrateTestAbort
		}
	}
	if upto == 0 {
		m.j.PhaseADone[schema] = true
	}
	return m.maybeJournal(true)
}

var errMigrateTestAbort = errors.New("store-migrate: test abort")

// phaseB copies table's REPEAT copies (rows with rowid <= upto when upto > 0).
func (m *migrator) phaseB(ctx context.Context, t storage.LegacyTable, upto int64) error {
	if m.j.PhaseBDone[t.Name] {
		return nil
	}
	var earlier []storage.LegacyTable
	for _, o := range m.bySchema[t.Schema] {
		if o.Name < t.Name {
			earlier = append(earlier, o)
		}
	}
	if len(earlier) == 0 {
		// The first table by name holds only FIRST copies (phase A).
		if upto == 0 {
			m.j.PhaseBDone[t.Name] = true
		}
		return m.maybeJournal(true)
	}
	after := m.j.PhaseB[t.Name]
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pages := readPages(ctx, func() (*migratePage, bool) {
		readStart := time.Now()
		defer func() { m.readNs += time.Since(readStart) }()
		rows, err := m.src.ScanRecords(t, after, m.opt.PageRows)
		if err != nil {
			return &migratePage{err: err}, false
		}
		short := len(rows) < m.opt.PageRows
		if upto > 0 {
			for i, r := range rows {
				if r.RowID > upto {
					rows, short = rows[:i], true
					break
				}
			}
		}
		if len(rows) == 0 {
			return nil, false
		}
		pg := &migratePage{rows: rows, last: rows[len(rows)-1].RowID, short: short, repeat: map[string]bool{}}
		after = pg.last
		cids := make([]string, len(rows))
		for i, r := range rows {
			cids[i] = r.CID
		}
		for _, o := range earlier {
			has, err := m.src.HasCIDs(o, cids)
			if err != nil {
				return &migratePage{err: err}, false
			}
			for c := range has {
				pg.repeat[c] = true
			}
		}
		return pg, !short
	})
	for pg := range pages {
		if pg.err != nil {
			return pg.err
		}
		for _, r := range pg.rows {
			if !pg.repeat[r.CID] {
				continue // FIRST (phase A), or a row the index does not know
			}
			p, err := m.partition(ctx, t)
			if err != nil {
				return err
			}
			e, err := m.recordEntry(t, r, nil, 0)
			if err != nil {
				return err
			}
			if _, err := m.enqueue(ctx, p, t.Name, r.CID, true, e); err != nil {
				return err
			}
			m.recordBytes += int64(len(r.Stored))
		}
		if err := m.drain(ctx); err != nil {
			return err
		}
		m.j.PhaseB[t.Name] = pg.last
		if err := m.maybeJournal(false); err != nil {
			return err
		}
		if m.opt.testAbortAfter > 0 && m.j.Records >= m.opt.testAbortAfter {
			return errMigrateTestAbort
		}
	}
	if upto == 0 {
		m.j.PhaseBDone[t.Name] = true
	}
	return m.maybeJournal(true)
}

// waitLabeled waits until the type owners have labeled every committed row
// of the schemas: the arrivals count stops moving once the writer is idle.
func (m *migrator) waitLabeled(ctx context.Context, schemas []string) error {
	if err := m.drain(ctx); err != nil {
		return err
	}
	start := time.Now()
	defer func() { m.labelNs += time.Since(start) }()
	names := make([]string, len(schemas))
	for i, s := range schemas {
		names[i] = "'" + strings.ReplaceAll(strings.TrimSuffix(s, ".fbs"), "'", "''") + "'"
	}
	q := "SELECT COALESCE(SUM(arrivals), 0), COALESCE(SUM(commit_seq), 0) FROM flatsql_types WHERE type IN (" + strings.Join(names, ",") + ")"
	var lastA, lastC int64 = -1, -1
	stable := 0
	for stable < 8 {
		res, err := m.r.Query(ctx, format2.Request{SQL: q})
		if err != nil {
			return err
		}
		a, c := res.Rows[0][0].Int64(), res.Rows[0][1].Int64()
		if a == lastA && c == lastC {
			stable++
		} else {
			stable = 0
			lastA, lastC = a, c
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
	return nil
}

// ---- verification (§16.1 step 7) ------------------------------------------------------

type migrateVerification struct {
	Partitions      int      `json:"partitions"`
	PartitionsEqual bool     `json:"partitions_equal"`
	Lanes           int      `json:"lanes"`
	LanesEqual      bool     `json:"lanes_equal"`
	Schemas         int      `json:"schemas"`
	CIDSequences    bool     `json:"cid_sequences_equal"`
	CIDsCompared    int64    `json:"cids_compared"`
	MaxRowID        []string `json:"max_rowid,omitempty"`
	MigratedGseqs   uint64   `json:"migrated_gseqs"`
	GseqFallbacks   uint64   `json:"migrated_gseq_fallbacks"`
	FTS             string   `json:"fts_rowids"`
	Mismatches      []string `json:"mismatches,omitempty"`
	Took            string   `json:"took"`
}

func (m *migrator) verify(ctx context.Context) (*migrateVerification, error) {
	start := time.Now()
	v := &migrateVerification{}
	bad := func(format string, args ...interface{}) {
		if len(v.Mismatches) < 50 {
			v.Mismatches = append(v.Mismatches, fmt.Sprintf(format, args...))
		}
	}
	// 1. Partition heads = sdn_partition_record_bytes.
	oracle, err := m.src.PartitionCounters()
	if err != nil {
		return v, err
	}
	res, err := m.r.Query(ctx, format2.Request{SQL: "SELECT sql_name, live_count, live_bytes FROM flatsql_partitions"})
	if err != nil {
		return v, err
	}
	heads := map[string][2]int64{}
	for _, row := range res.Rows {
		heads[row[0].String()] = [2]int64{row[1].Int64(), row[2].Int64()}
	}
	v.PartitionsEqual = true
	for _, t := range m.tables {
		// The trigger counters are the design's oracle; a recount of the
		// table is the ground truth they must equal (a counter row that the
		// background count has not finished would otherwise pass a wrong
		// head).
		want, err := m.src.TableCounter(t)
		if err != nil {
			return v, err
		}
		if c, ok := oracle[t.Name]; ok && c != want {
			m.rep.Notes = append(m.rep.Notes, fmt.Sprintf("legacy counter of %s (%d, %d B) differs from a recount (%d, %d B); the recount is the oracle",
				t.Name, c.Count, c.Bytes, want.Count, want.Bytes))
		}
		got := heads[t.Name]
		v.Partitions++
		if got[0] != want.Count || got[1] != want.Bytes {
			v.PartitionsEqual = false
			bad("partition %s: head (%d, %d B), legacy (%d, %d B)", t.Name, got[0], got[1], want.Count, want.Bytes)
		}
	}
	// 2. Lanes (per tag tuple, summed over partitions) = the legacy lanes:
	// a recount of the tag rows of live records, which the incremental
	// sdn_record_source_summary is maintained to equal (drift is reported).
	summary, err := m.src.SourceSummaries()
	if err != nil {
		return v, err
	}
	var sums []storage.SourceSummaryRow
	for schema, tables := range m.bySchema {
		rc, err := m.src.LaneRecount(schema, tables)
		if err != nil {
			return v, err
		}
		sums = append(sums, rc...)
	}
	{
		type laneVal struct{ n, b int64 }
		key := func(r storage.SourceSummaryRow) string {
			return strings.Join([]string{r.Schema, r.ProviderID, r.SourceName, r.BatchID, r.ProducerPeerID, r.ProducerPublicKey}, "\x1f")
		}
		rc := map[string]laneVal{}
		for _, r := range sums {
			rc[key(r)] = laneVal{r.Count, r.Bytes}
		}
		drift := 0
		for _, r := range summary {
			if got := rc[key(r)]; got.n != r.Count || got.b != r.Bytes {
				if r.Count == 0 && got.n == 0 {
					continue
				}
				drift++
				if drift <= 5 {
					m.rep.Notes = append(m.rep.Notes, fmt.Sprintf("legacy summary lane %q (%d, %d B) differs from a recount (%d, %d B); the recount is the oracle",
						key(r), r.Count, r.Bytes, got.n, got.b))
				}
			}
		}
		if drift > 5 {
			m.rep.Notes = append(m.rep.Notes, fmt.Sprintf("%d legacy summary lanes differ from the recount in all", drift))
		}
	}
	res, err = m.r.Query(ctx, format2.Request{SQL: `SELECT type, provider, source, batch, peer, pubkey, SUM(count), SUM(bytes)
		FROM flatsql_lanes GROUP BY type, provider, source, batch, peer, pubkey`})
	if err != nil {
		return v, err
	}
	laneKey := func(typ, provider, source, batch, peer, pubkey string) string {
		return strings.Join([]string{typ, provider, source, batch, peer, pubkey}, "\x1f")
	}
	lanes := map[string][2]int64{}
	for _, row := range res.Rows {
		if row[6].Int64() == 0 {
			continue
		}
		lanes[laneKey(row[0].String(), row[1].String(), row[2].String(), row[3].String(), row[4].String(), row[5].String())] =
			[2]int64{row[6].Int64(), row[7].Int64()}
	}
	v.LanesEqual = true
	seen := map[string]bool{}
	for _, s := range sums {
		if s.Count == 0 {
			continue
		}
		k := laneKey(strings.TrimSuffix(s.Schema, ".fbs"), s.ProviderID, s.SourceName, s.BatchID, s.ProducerPeerID, s.ProducerPublicKey)
		seen[k] = true
		v.Lanes++
		if got := lanes[k]; got[0] != s.Count || got[1] != s.Bytes {
			v.LanesEqual = false
			bad("lane %q: engine (%d, %d B), legacy (%d, %d B)", k, got[0], got[1], s.Count, s.Bytes)
		}
	}
	for k, got := range lanes {
		if !seen[k] && got[0] > 0 {
			// An untagged lane (no provider/source) has no legacy summary row.
			parts := strings.Split(k, "\x1f")
			if parts[1] == "" && parts[2] == "" && parts[3] == "" {
				continue
			}
			v.LanesEqual = false
			bad("lane %q: engine (%d, %d B), legacy none", k, got[0], got[1])
		}
	}
	// 3. Per schema: the v1 cid sequence.
	v.CIDSequences = true
	schemas, err := m.src.IndexSchemas()
	if err != nil {
		return v, err
	}
	for _, s := range schemas {
		if _, ok := m.bySchema[s.Schema]; !ok {
			if s.Rows > 0 {
				v.CIDSequences = false
				bad("schema %s: %d index rows but no producer table", s.Schema, s.Rows)
			}
			continue
		}
		v.Schemas++
		n, gseqHi, err := m.compareCIDSequence(ctx, s.Schema, bad)
		v.CIDsCompared += n
		if err != nil {
			if errors.Is(err, errSequenceMismatch) {
				v.CIDSequences = false
				continue
			}
			return v, err
		}
		if gseqHi != s.MaxRowID {
			v.CIDSequences = false
			bad("%s: MaxRowID: legacy %d, gseq_hi %d", s.Schema, s.MaxRowID, gseqHi)
		}
		v.MaxRowID = append(v.MaxRowID, fmt.Sprintf("%s: %d", s.Schema, gseqHi))
	}
	// Every migrated FIRST copy kept its legacy rowid as its gseq (checked
	// position by position above), so each FTS row, keyed by that rowid,
	// resolves to the same CID. The engine counts every copy that could not
	// keep it.
	st, err := m.w.Stats()
	if err != nil {
		return v, err
	}
	v.MigratedGseqs, v.GseqFallbacks = st.MigratedGseqs, st.MigratedGseqFallbacks
	if st.MigratedGseqFallbacks != 0 {
		bad("%d migrated gseqs fell back to allocated ones (flatsql_ps_stats entry 25)", st.MigratedGseqFallbacks)
	}
	v.FTS = "equal: every FTS rowid is the gseq of the same CID (gseq = legacy rowid)"
	v.Took = time.Since(start).Round(time.Millisecond).String()
	if len(m.j.Rejected) > 0 {
		bad("%d record copies were rejected by the engine (first: %s %s code %d)", len(m.j.Rejected),
			m.j.Rejected[0].Table, m.j.Rejected[0].CID, m.j.Rejected[0].Code)
	}
	if !v.PartitionsEqual || !v.LanesEqual || !v.CIDSequences || len(m.j.Rejected) > 0 || v.GseqFallbacks != 0 {
		return v, fmt.Errorf("store-migrate verification failed (%d mismatches); format 2 was not activated", len(v.Mismatches))
	}
	return v, nil
}

var errSequenceMismatch = errors.New("cid sequence mismatch")

func (m *migrator) compareCIDSequence(ctx context.Context, schema string, bad func(string, ...interface{})) (int64, int64, error) {
	typ := strings.TrimSuffix(schema, ".fbs")
	var after int64
	var gseq int64
	var n int64
	const page = 5000
	for {
		legacy, err := m.src.IndexPage(schema, after, page)
		if err != nil {
			return n, gseq, err
		}
		res, err := m.r.Query(ctx, format2.Request{SQL: fmt.Sprintf(`SELECT _gseq, _cid_bin FROM "%s" WHERE _gseq > ? ORDER BY _gseq LIMIT %d`, typ, page),
			Params: []format2.Cell{format2.Int(gseq)}})
		if err != nil {
			return n, gseq, fmt.Errorf("%s arrivals: %w", typ, err)
		}
		if len(legacy) != len(res.Rows) {
			// Compare what both have, then report the length difference.
			k := len(legacy)
			if len(res.Rows) < k {
				k = len(res.Rows)
			}
			for i := 0; i < k; i++ {
				if !cidEqual(legacy[i].CID, res.Rows[i][1].B) {
					bad("%s: position %d: legacy %s, engine %x", schema, n+int64(i), legacy[i].CID, res.Rows[i][1].B)
					return n, gseq, errSequenceMismatch
				}
			}
			bad("%s: legacy has %d more cids than the engine after position %d", schema, len(legacy)-len(res.Rows), n+int64(k))
			return n + int64(k), gseq, errSequenceMismatch
		}
		for i := range legacy {
			if !cidEqual(legacy[i].CID, res.Rows[i][1].B) {
				bad("%s: position %d: legacy %s, engine %x", schema, n+int64(i), legacy[i].CID, res.Rows[i][1].B)
				return n, gseq, errSequenceMismatch
			}
			// gseq = the legacy rowid (flatsql 3.2.0 migrated gseqs): the
			// datasync cursor, MaxRowID and the FTS rowids carry over.
			if g := res.Rows[i][0].Int64(); g != legacy[i].RowID {
				bad("%s: position %d (%s): gseq %d, legacy rowid %d", schema, n+int64(i), legacy[i].CID, g, legacy[i].RowID)
				return n, gseq, errSequenceMismatch
			}
		}
		n += int64(len(legacy))
		if len(legacy) == 0 {
			break
		}
		after = legacy[len(legacy)-1].RowID
		gseq = res.Rows[len(res.Rows)-1][0].Int64()
		if len(legacy) < page {
			break
		}
	}
	return n, gseq, nil
}

func cidEqual(text string, bin []byte) bool {
	c, err := cid.Decode(text)
	if err != nil {
		return false
	}
	return string(c.Bytes()) == string(bin)
}

// ---- delta pass (§22.4-2) -------------------------------------------------------------

// runDelta copies what changed on the stopped live store after the snapshot
// pass: rows past each table's watermark (new FIRST copies in index order,
// then new REPEAT copies), then deletions (a CID the staging partition holds
// that the live table no longer does), reconciled by CID.
func (m *migrator) runDelta(ctx context.Context) error {
	if m.j.Watermarks == nil {
		return errors.New("the snapshot journal has no watermarks")
	}
	schemas := make([]string, 0, len(m.bySchema))
	for s := range m.bySchema {
		schemas = append(schemas, s)
	}
	sort.Strings(schemas)
	// New index rows (past the snapshot's MaxRowID per schema) are new FIRST
	// copies: phase A continues from the snapshot's index mark.
	for _, schema := range schemas {
		if m.j.PhaseA[schema] < m.j.IndexMarks[schema] {
			m.j.PhaseA[schema] = m.j.IndexMarks[schema]
		}
		m.j.PhaseADone[schema] = false
		if err := m.phaseA(ctx, schema, 0); err != nil {
			return err
		}
	}
	if err := m.waitLabeled(ctx, schemas); err != nil {
		return err
	}
	for _, t := range m.tables {
		if m.j.PhaseB[t.Name] < m.j.Watermarks[t.Name] {
			m.j.PhaseB[t.Name] = m.j.Watermarks[t.Name]
		}
		m.j.PhaseBDone[t.Name] = false
		if err := m.phaseB(ctx, t, 0); err != nil {
			return err
		}
	}
	if err := m.waitLabeled(ctx, schemas); err != nil {
		return err
	}
	// Tags gained after the snapshot (a batch that re-tagged records the
	// snapshot already held): each is the record's FIRST copy again with that
	// tag, which the engine appends as a RETAG (a tag it holds is a no-op).
	if err := m.deltaTags(ctx); err != nil {
		return err
	}
	// Deletions and supersedes: a CID the partition holds that its live table
	// no longer does is killed (TOMB_CID; the engine's supersede already
	// killed a superseded CAT copy, so its TOMB is a no-op).
	for _, t := range m.tables {
		if err := m.deltaDeletes(ctx, t); err != nil {
			return err
		}
	}
	if err := m.drain(ctx); err != nil {
		return err
	}
	return m.waitLabeled(ctx, schemas)
}

func (m *migrator) deltaTags(ctx context.Context) error {
	after := m.j.TagMark
	if m.j.TagsDone > after {
		after = m.j.TagsDone
	}
	for {
		rows, err := m.src.TagsAfter(after, m.opt.PageRows)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		bySchema := map[string][]storage.TagRow{}
		for _, r := range rows {
			bySchema[r.Schema] = append(bySchema[r.Schema], r)
		}
		for schema, trs := range bySchema {
			cids := make([]string, 0, len(trs))
			for _, r := range trs {
				cids = append(cids, r.CID)
			}
			first := map[string]storage.LegacyTable{}
			recs := map[string]storage.LegacyRecord{}
			missing := cids
			for _, t := range m.bySchema[schema] {
				if len(missing) == 0 {
					break
				}
				got, err := m.src.RecordsByCID(t, missing)
				if err != nil {
					return err
				}
				var still []string
				for _, c := range missing {
					if r, ok := got[c]; ok {
						first[c], recs[c] = t, r
					} else {
						still = append(still, c)
					}
				}
				missing = still
			}
			for _, tr := range trs {
				t, ok := first[tr.CID]
				if !ok {
					continue // the record is gone (its deletion is reconciled below)
				}
				p, err := m.partition(ctx, t)
				if err != nil {
					return err
				}
				tag := tr.LegacyTag
				e, err := m.recordEntry(t, recs[tr.CID], &tag, 0)
				if err != nil {
					return err
				}
				if _, err := m.enqueue(ctx, p, t.Name, tr.CID, false, e); err != nil {
					return err
				}
			}
		}
		if err := m.drain(ctx); err != nil {
			return err
		}
		after = rows[len(rows)-1].RowID
		m.j.TagsDone = after
		if err := m.maybeJournal(false); err != nil {
			return err
		}
		if len(rows) < m.opt.PageRows {
			break
		}
	}
	return m.maybeJournal(true)
}

func (m *migrator) deltaDeletes(ctx context.Context, t storage.LegacyTable) error {
	p, err := m.partition(ctx, t)
	if err != nil {
		return err
	}
	var gone []string
	var after int64
	for {
		res, err := m.r.Query(ctx, format2.Request{SQL: fmt.Sprintf(`SELECT _pseq, _cid FROM "%s" WHERE _pseq > ? ORDER BY _pseq LIMIT 2000`, t.Name),
			Params: []format2.Cell{format2.Int(after)}})
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		if len(res.Rows) == 0 {
			break
		}
		cids := make([]string, len(res.Rows))
		for i, row := range res.Rows {
			cids[i] = row[1].String()
		}
		has, err := m.src.HasCIDs(t, cids)
		if err != nil {
			return err
		}
		for _, c := range cids {
			if !has[c] {
				gone = append(gone, c)
			}
		}
		after = res.Rows[len(res.Rows)-1][0].Int64()
	}
	for _, text := range gone {
		c, err := cid.Decode(text)
		if err != nil {
			return err
		}
		if _, err := m.enqueue(ctx, p, t.Name, text, false, &format2.Entry{Kind: format2.EntTombCid,
			Flags: format2.FlagCidPresent, CID: c.Bytes()}); err != nil {
			return err
		}
	}
	if len(gone) > 0 {
		m.logf("%s: %d records deleted since the snapshot", t.Name, len(gone))
	}
	return nil
}

// migrateControl2 is the interim control database's name (A5 step 1), and
// migrateFTS the interim full-text index's (A6: its own instance and file).
const (
	migrateControl2 = "control2.flatsqldb"
	migrateFTS      = "fts.flatsqldb"
)

func (m *migrator) copyControl() error {
	start := time.Now()
	st, err := m.src.CopyControl(migrateControl2, migrateFTS)
	if err != nil {
		return fmt.Errorf("control copy: %w", err)
	}
	for _, name := range []string{migrateControl2, migrateFTS} {
		src := filepath.Join(m.srcPath, name)
		dst := filepath.Join(m.outRoot, name)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue // no legacy full-text index: the daemon builds one
		}
		if src != dst {
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("move the control copy to %s: %w", dst, err)
			}
		}
		f, err := os.OpenFile(dst, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	if m.rep.Extra == nil {
		m.rep.Extra = map[string]interface{}{}
	}
	m.rep.Extra["control_copy"] = fmt.Sprintf("%d tables, %d indexes, %d rows, %d FTS rows, %d bytes in %s",
		st.Tables, st.Indexes, st.Rows, st.FTSRows, st.Bytes, time.Since(start).Round(time.Millisecond))
	return syncDirectory(m.outRoot)
}

// ---- activation (A5) ------------------------------------------------------------------

func (m *migrator) activate() error {
	lock, err := storage.LockStoreForMaintenance(m.outRoot)
	if err != nil {
		return fmt.Errorf("activation needs the store lock: %w", err)
	}
	defer lock.Release()
	// 1. The staging store is durable (every file the engine wrote was
	// synced by the engine; the directories are synced here).
	for _, d := range []string{filepath.Join(m.staging, format2.Dir), m.staging} {
		if err := syncDirectory(d); err != nil {
			return err
		}
	}
	// 2. The legacy control database leaves; a DIRECTORY takes its name.
	if err := m.retireLegacyControl(); err != nil {
		return err
	}
	// 3. The partition store takes its place (MIGRATED is inside it).
	if _, err := os.Stat(filepath.Join(m.outRoot, format2.Dir)); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(filepath.Join(m.staging, format2.Dir), filepath.Join(m.outRoot, format2.Dir)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// 4. fsync the store root.
	if err := syncDirectory(m.outRoot); err != nil {
		return err
	}
	m.j.Activated = true
	_ = m.j.write(m.jpath)
	m.logf("activated format 2 in %s; the legacy control database is in %s", m.outRoot, filepath.Join(m.outRoot, migratePreFormat2))
	return nil
}

// retireLegacyControl moves control.flatsqldb and its companions into
// pre-format2/ and creates the placeholder directory. Idempotent.
func (m *migrator) retireLegacyControl() error {
	if m.outRoot != m.srcPath {
		// --out elsewhere: the legacy store is untouched; the new root gets
		// the placeholder so a pre-T6 binary pointed at it fails too.
		return os.MkdirAll(filepath.Join(m.outRoot, migrateControlName), 0o700)
	}
	pre := filepath.Join(m.outRoot, migratePreFormat2)
	if err := os.MkdirAll(pre, 0o700); err != nil {
		return err
	}
	for _, name := range []string{migrateControlName, migrateControlName + "-wal", migrateControlName + "-journal",
		migrateControlName + ".fsdata", migrateControlName + "-shm"} {
		src := filepath.Join(m.outRoot, name)
		fi, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.IsDir() && name == migrateControlName {
			continue // the placeholder: already moved
		}
		if err := os.Rename(src, filepath.Join(pre, name)); err != nil {
			return err
		}
	}
	if err := syncDirectory(pre); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(m.outRoot, migrateControlName), 0o700); err != nil {
		return err
	}
	return syncDirectory(m.outRoot)
}

// finishActivation completes a crash-interrupted activation of an already
// MIGRATED store (the rename happened; the legacy files may not have moved).
func (m *migrator) finishActivation() error {
	fi, err := os.Lstat(filepath.Join(m.outRoot, migrateControlName))
	if err == nil && fi.IsDir() {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock, err := storage.LockStoreForMaintenance(m.outRoot)
	if err != nil {
		return err
	}
	defer lock.Release()
	m.srcPath = m.outRoot
	return m.retireLegacyControl()
}
