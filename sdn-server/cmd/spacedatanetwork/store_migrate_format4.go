package main

// store_migrate_format4.go: `spacedatanetwork store-migrate --to 4` (alias
// sqlite). A format-1 store becomes store format 4 (one SQLite file per
// partition and UTC month), offline and copy-based. Stack design
// docs/architecture/flatsql-sqlite-partitions.md §11 (gate G7); the interfaces
// are the build-out contract's (§2.3 activation, §5.6 this command).
//
// SOURCE. Format 1 is read through its own wasm engine
// (storage.MigrationSource), never through native SQLite.
//
// TARGET. <data>/fsql4, opened by the format-4 engine as a migration target
// (format4.CreateForMigration): nothing marks it a store until activation.
// Every type's first new seq is at least the gseq floor (migrateGseqFloor).
//
// COPY, per schema, in sdn_record_index rowid order, held rows only: every
// copy of a record (one per producer table holding it) is a migrate-mode PUT
// into its producer's partition, with the index rowid as the seq. The record's
// tag rows go with its FIRST copy (the first producer table by name), the copy
// format 1's lane counters attribute them to; each keeps its source_url,
// content_key_id and created_at (as the instance's at). An IQC record carries
// the ingest identity format 1 holds for it. Then the partition secondary
// indexes and the type indexes are built once (Rebuild).
//
// CHECK, a hard fail before activation (0 mismatches):
//   - every held index row's record is in format 4 under seq = the rowid;
//   - every unsealed copy re-hashes to its CID;
//   - the fields the engine extracted equal format 1's index columns: epoch
//     and object key on every record, every structured column on a sample;
//   - every copy's bytes, ts, signature, peer and producer, and every tag
//     instance (identity, source_url, at), equal what the copy sent
//     (order-free digests, so format 1's record bytes are read once);
//   - partition, type and lane counters equal a recount (lanes from the
//     tags); format 1's own counters are reported where they drifted;
//   - the engine's verify rebuild reports 0 mismatches.
// Orphan index rows (no producer table holds the record) are not migrated;
// they are counted per schema and reported.
//
// ACTIVATION (crash-safe, contract §2.3): the control tables are copied into
// fsql4/control.db; the engine writes MIGRATED, then STORE (the commit
// point); marker.FinishActivation moves control.flatsqldb* into pre-format4/
// and makes control.flatsqldb a directory.
//
// RESUME. Progress is journalled in <data>/fsql4-migrate.json after the PUTs
// it covers are durable. A rerun resends at most the pages after the last
// journal write; the engine stores a resent copy or tag instance once. A rerun
// after STORE finishes the activation. --verify-only re-reads the format-1
// store in pre-format4/ and re-checks an activated store against it.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

const (
	migrate4JournalName = "fsql4-migrate.json"
	// CopyControl writes into the source store's root under plain names; the
	// control copy then moves to fsql4/control.db. Format 4 builds its own
	// full-text indexes after activation, so the interim FTS copy is dropped.
	migrate4ControlTmp = "fsql4-control.tmp"
	migrate4FTSTmp     = "fsql4-fts.tmp"
	migrate4ControlDB  = "control.db"
	// migrate4SampleEvery: the records whose index rowid is a multiple of it
	// have every structured column checked through the engine (the rest
	// have epoch and object key checked).
	migrate4SampleEvery = 97
	// migrate4MaxRecord is C-6's largest storable record at the engine's
	// default write slot (8 MiB request area less 64 KiB).
	migrate4MaxRecord         = 8<<20 - 64<<10
	migrate4RejTooLarge int32 = -112
)

// migrateTargetFormat parses --to: "" or "2" is format 2 (today's path), "4"
// or "sqlite" (any case) is format 4.
func migrateTargetFormat(to string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(to)) {
	case "", "2":
		return 2, nil
	case "4", "sqlite":
		return 4, nil
	}
	return 0, fmt.Errorf("--to %q: the targets are 2 and 4 (sqlite)", to)
}

func runStoreMigrate4(cmd *cobra.Command, store string) error {
	switch {
	case storeMigrateOut != "", storeMigrateSnapshot != "", storeMigrateDelta, storeMigrateNoActive:
		return errors.New("--out, --from-snapshot, --delta and --no-activate are format-2 options; --to 4 migrates in place")
	case storeMigrateInventory && storeMigrateVerifyOnly:
		return errors.New("--inventory and --verify-only are separate runs")
	}
	out := cmd.OutOrStdout()
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if storeMigrateInventory {
		inv, err := migrate4Inventory(store)
		if err != nil {
			return err
		}
		return enc.Encode(inv)
	}
	rep, err := migrateStore4(cmd.Context(), migrate4Options{Store: store, PageRows: storeMigratePageRows,
		VerifyOnly: storeMigrateVerifyOnly, AOTCacheDir: storage.EngineAOTCacheDir()}, out)
	if rep != nil {
		_ = enc.Encode(rep)
	}
	return err
}

// migrate4Inventory is the format-2 inventory with format 4's record limit:
// partitions holding a record above it are listed (their records would be
// refused, C-6).
func migrate4Inventory(store string) (*inventoryReport, error) {
	inv, err := migrateInventory(store)
	if err != nil {
		return nil, err
	}
	inv.OverEntry = nil
	for _, p := range inv.Partitions {
		if p.Max > migrate4MaxRecord {
			inv.OverEntry = append(inv.OverEntry, p)
		}
	}
	if inv.Extra == nil {
		inv.Extra = map[string]interface{}{}
	}
	inv.Extra["target_format"] = 4
	inv.Extra["max_record_bytes"] = migrate4MaxRecord
	return inv, nil
}

// ---- options, journal, report -------------------------------------------------------

// format4Opener opens the format-4 engine; release runs after Close.
type format4Opener func(ctx context.Context, opt format4.Options) (api format4.API, release func(), err error)

// openFormat4Engine opens the real engine over the store's shared native I/O.
func openFormat4Engine(ctx context.Context, opt format4.Options) (format4.API, func(), error) {
	ns, err := flatsqlrt.OpenNativeStore(opt.DataRoot)
	if err != nil {
		return nil, nil, err
	}
	opt.Store = ns
	e, err := format4.Open(ctx, opt)
	if err != nil {
		ns.Release()
		return nil, nil, err
	}
	return e, ns.Release, nil
}

type migrate4Options struct {
	Store      string // the data root: the format-1 store, migrated in place
	PageRows   int
	VerifyOnly bool

	AOTCacheDir   string
	CompileOnMiss bool // tests only (the daemon never compiles)
	Tuning        format4.Tuning

	open format4Opener // nil = openFormat4Engine
	// testStep runs at every named step and may fail it (tests: a clean
	// stand-in for a kill at that point); testJournalEvery overrides
	// journalEvery.
	testStep         func(step string) error
	testJournalEvery time.Duration
}

// migrate4Journal is the durable progress of a run.
type migrate4Journal struct {
	Version   int    `json:"version"`
	Source    string `json:"source"`
	GseqFloor uint64 `json:"gseq_floor"`
	// The source's shape when the run began: a rerun refuses a source that
	// took writes since (its copy would be a mix of two states).
	MaxIndexRowID int64                        `json:"max_index_rowid"`
	MaxTagRowID   int64                        `json:"max_tag_rowid"`
	Schemas       map[string]*migrate4Progress `json:"schemas"`
	Rejected      []migrate4Reject             `json:"rejected,omitempty"`
	Built         bool                         `json:"built"`
	Verified      bool                         `json:"verified"`
	Activated     bool                         `json:"activated"`
}

// migrate4Progress is one schema's copy: everything up to After is durable.
type migrate4Progress struct {
	After      int64                    `json:"after"` // the last index rowid copied
	Done       bool                     `json:"done"`
	Held       int64                    `json:"held"`    // held index rows copied (records)
	Orphans    int64                    `json:"orphans"` // index rows no producer table holds (not copied)
	MaxSeq     int64                    `json:"max_seq"` // the last held row's rowid
	Copies     map[string]migrate4Tally `json:"copies"`  // producer table -> copies sent, Σ stored bytes
	Tags       int64                    `json:"tags"`
	Identities int64                    `json:"identities"`
	Unified    int64                    `json:"unified,omitempty"` // copies stored with their record's first copy's ts and bytes
	CopyDigest migrateDigest            `json:"copy_digest"`
	TagDigest  migrateDigest            `json:"tag_digest"`
}

// migrate4Tally counts one producer table's copies: Bytes as format 4
// stores them, SourceBytes as the table held them (they differ only where
// format 1's copies of a record differed, see pageBatches).
type migrate4Tally struct {
	Rows        int64 `json:"rows"`
	Bytes       int64 `json:"bytes"`
	SourceBytes int64 `json:"source_bytes"`
}

// migrate4Reject is a record copy the engine did not store.
type migrate4Reject struct {
	Table  string `json:"table"`
	CID    string `json:"cid"`
	Seq    int64  `json:"seq"`
	Action uint8  `json:"action"`
	Code   int32  `json:"code"`
}

type migrate4Report struct {
	Store       string            `json:"store"`
	Format      int               `json:"format"`
	Mode        string            `json:"mode"` // "migrate" or "verify-only"
	Records     int64             `json:"records"`
	Copies      int64             `json:"copies"`
	RecordBytes int64             `json:"record_bytes"`
	SourceBytes int64             `json:"source_bytes"`
	GseqFloor   uint64            `json:"gseq_floor"`
	Took        string            `json:"took"`
	RecordMBps  float64           `json:"record_mb_per_s"`
	MaxRSS      int64             `json:"max_rss_bytes"`
	Load        [2]float64        `json:"load"` // 1-minute load average at the start and the end
	Machine     string            `json:"machine"`
	Resumed     bool              `json:"resumed"`
	Rejected    []migrate4Reject  `json:"rejected,omitempty"`
	Oversized   []migrate4Reject  `json:"oversized,omitempty"` // refused as above the record limit (C-6)
	Check       *migrate4Check    `json:"check,omitempty"`
	Activated   bool              `json:"activated"`
	Notes       []string          `json:"notes,omitempty"`
	Time        map[string]string `json:"time,omitempty"`
	EngineStats []uint64          `json:"engine_stats,omitempty"`
}

type migrate4Check struct {
	Schemas       int                  `json:"schemas"`
	Records       int64                `json:"records"` // held index rows checked
	Copies        int64                `json:"copies"`
	Rehashed      int64                `json:"rehashed"` // unsealed copies re-hashed to their CID
	Sealed        int64                `json:"sealed"`
	FieldChecks   int64                `json:"field_checks"`   // records whose epoch and object key were compared
	ColumnSamples int64                `json:"column_samples"` // records whose every column was compared
	TagInstances  int64                `json:"tag_instances"`
	Partitions    int                  `json:"partitions"`
	Lanes         int                  `json:"lanes"`
	Orphans       map[string]int64     `json:"orphan_index_rows,omitempty"`
	Rebuild       []format4.RebuildRow `json:"rebuild_verify,omitempty"`
	Mismatches    []string             `json:"mismatches,omitempty"` // the first 50
	MismatchCount int64                `json:"mismatch_count"`
	Took          string               `json:"took"`
}

func (c *migrate4Check) bad(format string, args ...interface{}) {
	c.MismatchCount++
	if len(c.Mismatches) < 50 {
		c.Mismatches = append(c.Mismatches, fmt.Sprintf(format, args...))
	}
}

// migrateDigest is an order-free digest of a set: the lane-wise sum of its
// elements' SHA-256 digests (four 64-bit lanes, each mod 2^64).
type migrateDigest [4]uint64

func (d *migrateDigest) add(h [32]byte) {
	for i := range d {
		d[i] += binary.LittleEndian.Uint64(h[8*i:])
	}
}

func (d *migrateDigest) merge(o migrateDigest) {
	for i := range d {
		d[i] += o[i]
	}
}

func (d migrateDigest) MarshalText() ([]byte, error) {
	var b [32]byte
	for i := range d {
		binary.LittleEndian.PutUint64(b[8*i:], d[i])
	}
	return []byte(hex.EncodeToString(b[:])), nil
}

func (d *migrateDigest) UnmarshalText(text []byte) error {
	raw, err := hex.DecodeString(string(text))
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("digest %q", text)
	}
	for i := range d {
		d[i] = binary.LittleEndian.Uint64(raw[8*i:])
	}
	return nil
}

// copyDigestOf is one stored copy: what format 4 must hold for it.
func copyDigestOf(seq int64, producer, peer string, ts int64, sig, stored []byte) [32]byte {
	h := sha256.New()
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(seq))
	h.Write(n[:])
	writeField(h, producer)
	writeField(h, peer)
	binary.LittleEndian.PutUint64(n[:], uint64(ts))
	h.Write(n[:])
	writeField(h, string(sig))
	d := sha256.Sum256(stored)
	h.Write(d[:])
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// tagDigestOf is one tag instance of a record.
func tagDigestOf(cidText string, t format4.Tag, at int64) [32]byte {
	h := sha256.New()
	for _, s := range []string{cidText, t.Provider, t.Source, t.SourceURL, t.Batch, t.ContentKeyID, t.ProducerPeer, t.ProducerPubkey} {
		writeField(h, s)
	}
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(at))
	h.Write(n[:])
	var out [32]byte
	h.Sum(out[:0])
	return out
}

func writeField(w io.Writer, s string) {
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(s)))
	w.Write(n[:])
	io.WriteString(w, s)
}

func formatTag(t storage.LegacyTag) format4.Tag {
	return format4.Tag{Provider: t.ProviderID, Source: t.SourceName, SourceURL: t.SourceURL, Batch: t.BatchID,
		ContentKeyID: t.ContentKeyID, ProducerPeer: t.ProducerPeerID, ProducerPubkey: t.ProducerPublicKey}
}

// ---- the run ------------------------------------------------------------------------

type migrator4 struct {
	opt   migrate4Options
	out   io.Writer
	root  string
	jpath string
	j     *migrate4Journal
	rep   *migrate4Report

	src      *storage.MigrationSource
	api      format4.API
	release  func()
	tables   []storage.LegacyTable
	bySchema map[string][]storage.LegacyTable
	schemas  []string
	specs    map[string]format4.TypeSpec
	index    []storage.IndexStats

	lastJournal time.Time
	times       map[string]time.Duration
}

func migrateStore4(ctx context.Context, opt migrate4Options, out io.Writer) (rep *migrate4Report, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opt.PageRows <= 0 {
		opt.PageRows = 2000
	}
	if opt.open == nil {
		opt.open = openFormat4Engine
	}
	root := strings.TrimSpace(opt.Store)
	if root == "" {
		return nil, errors.New("store-migrate --to 4 needs the store directory")
	}
	start := time.Now()
	m := &migrator4{opt: opt, out: out, root: root, jpath: filepath.Join(root, migrate4JournalName),
		times: map[string]time.Duration{}}
	m.rep = &migrate4Report{Store: root, Format: 4, Mode: "migrate", Load: [2]float64{migrateLoadAverage(), 0},
		Machine: fmt.Sprintf("%s/%s, %d CPUs", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())}
	if opt.VerifyOnly {
		m.rep.Mode = "verify-only"
	}
	defer func() {
		m.close(ctx)
		took := time.Since(start)
		m.rep.Took = took.Round(time.Millisecond).String()
		if s := took.Seconds(); s > 0 {
			m.rep.RecordMBps = float64(m.rep.RecordBytes) / 1e6 / s
		}
		m.rep.MaxRSS = maxRSSBytes()
		m.rep.Load[1] = migrateLoadAverage()
		m.rep.Time = map[string]string{}
		for k, v := range m.times {
			m.rep.Time[k] = v.Round(time.Millisecond).String()
		}
	}()
	return m.rep, m.run(ctx)
}

func (m *migrator4) logf(format string, args ...interface{}) {
	if m.out != nil {
		fmt.Fprintf(m.out, "store-migrate: "+format+"\n", args...)
	}
}

func (m *migrator4) note(format string, args ...interface{}) {
	m.rep.Notes = append(m.rep.Notes, fmt.Sprintf(format, args...))
}

func (m *migrator4) step(name string) error {
	if m.opt.testStep != nil {
		return m.opt.testStep(name)
	}
	return nil
}

func (m *migrator4) timed(name string, f func() error) error {
	t := time.Now()
	err := f()
	m.times[name] += time.Since(t)
	return err
}

func (m *migrator4) run(ctx context.Context) error {
	mk, err := marker.Read(m.root)
	if err != nil {
		return err
	}
	switch {
	case mk.Activated() && m.opt.VerifyOnly:
		if mk.NeedsFinish() {
			return errors.New("the format-4 activation is unfinished: run store-migrate --to 4 (without --verify-only) to finish it")
		}
		return m.verifyOnly(ctx)
	case mk.Activated():
		// STORE is the commit point: finish what a crash after it cut short.
		if mk.NeedsFinish() {
			if err := m.finishActivation(); err != nil {
				return err
			}
			m.note("finished an interrupted activation")
		}
		m.note("the store is already activated as format 4")
		m.rep.Activated = true
		return nil
	case m.opt.VerifyOnly:
		return errors.New("--verify-only checks an activated format-4 store; this one is not")
	case !mk.Format4():
		// Formats 2 and 3 are refused (format 4 opens formats 1 and 4).
		sf, err := update.ReadStoreFormat(m.root)
		if err != nil {
			return err
		}
		if sf.Format >= 2 {
			return fmt.Errorf("%s is a store of format %d (%s): format 4 migrates only format-1 stores", m.root, sf.Format, sf.Evidence)
		}
	}
	// Format 1, or a format-4 activation a crash cut short before STORE.
	if !mk.LegacyControlFile {
		return fmt.Errorf("%s holds no format-1 store (%s is not a file)", m.root, marker.LegacyControl)
	}
	if err := m.openSource(); err != nil {
		return err
	}
	if err := m.openJournal(mk); err != nil {
		return err
	}
	if err := m.openTarget(ctx, format4.CreateForMigration, mk); err != nil {
		return err
	}
	if err := m.step("open"); err != nil {
		return err
	}
	if err := m.checkJournalAgainstHeads(ctx); err != nil {
		return err
	}
	if !m.j.Built {
		for _, schema := range m.schemas {
			if err := m.copySchema(ctx, schema); err != nil {
				return err
			}
		}
		m.logf("copied; building the partition and type indexes")
		if err := m.timed("build", func() error {
			_, err := m.api.Rebuild(ctx, "", format4.RebuildPartitionIndexes|format4.RebuildTypeIndex)
			return err
		}); err != nil {
			return fmt.Errorf("build the indexes: %w", err)
		}
		if err := m.step("built"); err != nil {
			return err
		}
		m.j.Built = true
		if err := m.saveJournal(); err != nil {
			return err
		}
	}
	m.tallyReport()
	if !m.j.Verified {
		c, err := m.check(ctx, m.j.Schemas)
		m.rep.Check = c
		if err != nil {
			return err
		}
		if err := m.step("verified"); err != nil {
			return err
		}
		m.j.Verified = true
		if err := m.saveJournal(); err != nil {
			return err
		}
	} else {
		m.note("verified by an earlier run")
	}
	if err := m.timed("control", m.copyControl); err != nil {
		return err
	}
	if err := m.step("control"); err != nil {
		return err
	}
	return m.activate(ctx)
}

// openSource opens the format-1 store (it takes the store lock) and reads
// its shape.
func (m *migrator4) openSource() error {
	return m.openSourceAt(m.root)
}

func (m *migrator4) openSourceAt(dir string) error {
	var err error
	if m.src, err = storage.OpenMigrationSource(dir); err != nil {
		return fmt.Errorf("open the format-1 store (stop the daemon first): %w", err)
	}
	m.rep.SourceBytes = sourceBytes(dir)
	if m.tables, err = m.src.ProducerTables(); err != nil {
		return err
	}
	m.bySchema = map[string][]storage.LegacyTable{}
	m.specs = map[string]format4.TypeSpec{}
	for _, t := range m.tables {
		if _, ok := m.bySchema[t.Schema]; !ok {
			m.schemas = append(m.schemas, t.Schema)
			spec, err := format4.TypeSpecFor(t.Schema)
			if err != nil {
				return fmt.Errorf("table %s: %w", t.Name, err)
			}
			m.specs[t.Schema] = spec
		}
		m.bySchema[t.Schema] = append(m.bySchema[t.Schema], t)
	}
	sort.Strings(m.schemas)
	m.index, err = m.src.IndexSchemas()
	return err
}

func (m *migrator4) openJournal(mk marker.Markers) error {
	maxRow, err := m.src.MaxIndexRowID()
	if err != nil {
		return err
	}
	maxTag, err := m.src.MaxTagRowID()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(m.jpath)
	switch {
	case err == nil:
		var j migrate4Journal
		if err := json.Unmarshal(raw, &j); err != nil {
			return fmt.Errorf("migration journal %s: %w", m.jpath, err)
		}
		if j.Version != 1 {
			return fmt.Errorf("migration journal %s: version %d", m.jpath, j.Version)
		}
		if j.MaxIndexRowID != maxRow || j.MaxTagRowID != maxTag {
			return fmt.Errorf("the format-1 store changed since this migration began (index max rowid %d -> %d, tag max rowid %d -> %d): remove %s and %s, then migrate again",
				j.MaxIndexRowID, maxRow, j.MaxTagRowID, maxTag, filepath.Join(m.root, marker.Dir), m.jpath)
		}
		if j.Schemas == nil {
			j.Schemas = map[string]*migrate4Progress{}
		}
		m.j = &j
		m.rep.Resumed = true
		m.logf("resuming from %s", m.jpath)
	case errors.Is(err, os.ErrNotExist):
		if mk.Format4() {
			return fmt.Errorf("%s holds format-4 markers but %s is missing: not a migration this command can resume", m.root, m.jpath)
		}
		if ents, err := os.ReadDir(filepath.Join(m.root, marker.Dir)); err == nil && len(ents) > 0 {
			return fmt.Errorf("%s exists without a migration journal: it is not a migration this command started; remove it",
				filepath.Join(m.root, marker.Dir))
		}
		fts, err := m.src.FullTextProgress()
		if err != nil {
			return err
		}
		m.j = &migrate4Journal{Version: 1, Source: m.root, GseqFloor: migrateGseqFloor(maxRow, fts),
			MaxIndexRowID: maxRow, MaxTagRowID: maxTag, Schemas: map[string]*migrate4Progress{}}
		// The journal comes first: a target always has one.
		if err := m.saveJournal(); err != nil {
			return err
		}
	default:
		return err
	}
	m.rep.GseqFloor = m.j.GseqFloor
	return nil
}

func (m *migrator4) saveJournal() error {
	m.lastJournal = time.Now()
	return m.timed("journal", func() error {
		if err := writeDurableJSON(m.jpath, m.j); err != nil {
			return err
		}
		return m.step("journal")
	})
}

// openTarget opens the format-4 engine and registers every schema's type.
func (m *migrator4) openTarget(ctx context.Context, mode format4.CreateMode, mk marker.Markers) error {
	if mode == format4.CreateForMigration && mk.MigratedValid && mk.StorePresent && !mk.StoreValid {
		// A torn STORE beside a valid MIGRATED: the engine's activation was
		// cut short before its commit point. A migration target has no
		// STORE, so the torn one goes and the activation runs again.
		if err := os.Remove(filepath.Join(m.root, marker.Dir, marker.StoreFile)); err != nil {
			return err
		}
		m.note("removed a torn fsql4/STORE (an activation cut short before its commit point)")
	}
	opt := format4.Options{DataRoot: m.root, Create: mode, AOTCacheDir: m.opt.AOTCacheDir,
		CompileOnMiss: m.opt.CompileOnMiss, Tuning: m.opt.Tuning}
	if m.j != nil {
		opt.GseqFloor = m.j.GseqFloor
	}
	var err error
	if m.api, m.release, err = m.opt.open(ctx, opt); err != nil {
		return fmt.Errorf("open the format-4 engine (run prewarm-aot as this user first): %w", err)
	}
	for _, schema := range m.schemas {
		if err := m.api.RegisterType(m.specs[schema]); err != nil {
			return fmt.Errorf("register %s: %w", schema, err)
		}
	}
	return nil
}

func (m *migrator4) closeTarget(ctx context.Context) error {
	if m.api == nil {
		return nil
	}
	if st, err := m.api.Stats(); err == nil {
		m.rep.EngineStats = st
	}
	err := m.api.Close(ctx)
	m.api = nil
	if m.release != nil {
		m.release()
		m.release = nil
	}
	return err
}

func (m *migrator4) close(ctx context.Context) {
	_ = m.closeTarget(ctx)
	if m.src != nil {
		_ = m.src.Close()
		m.src = nil
	}
}

func (m *migrator4) progress(schema string) *migrate4Progress {
	p := m.j.Schemas[schema]
	if p == nil {
		p = &migrate4Progress{Copies: map[string]migrate4Tally{}}
		m.j.Schemas[schema] = p
	}
	if p.Copies == nil {
		p.Copies = map[string]migrate4Tally{}
	}
	return p
}

// checkJournalAgainstHeads: the partitions are the authority. A journal that
// counts more copies than the partitions hold means acked data is gone; the
// target cannot be trusted.
func (m *migrator4) checkJournalAgainstHeads(ctx context.Context) error {
	var want int64
	for _, p := range m.j.Schemas {
		for _, t := range p.Copies {
			want += t.Rows
		}
	}
	if want == 0 {
		return nil
	}
	parts, err := m.api.Partitions(ctx)
	if err != nil {
		return err
	}
	var held int64
	for _, p := range parts {
		held += p.Records
	}
	if held < want-int64(len(m.j.Rejected)) {
		return fmt.Errorf("the journal counts %d copies but the format-4 partitions hold %d: remove %s and %s, then migrate again",
			want, held, filepath.Join(m.root, marker.Dir), m.jpath)
	}
	return nil
}

// ---- copy ---------------------------------------------------------------------------

// migrate4Page is one page of a schema's held index rows with every copy of
// their records, their tags and identities, read one page ahead.
type migrate4Page struct {
	entries []storage.IndexCopies // copies per table of the schema (name order)
	tags    map[string][]storage.LegacyTag
	last    int64
	short   bool
	err     error
}

// readPages4 runs next on its own goroutine, one page ahead of the caller.
func readPages4(ctx context.Context, next func() *migrate4Page) <-chan *migrate4Page {
	ch := make(chan *migrate4Page, 1)
	go func() {
		defer close(ch)
		for {
			pg := next()
			if pg == nil {
				return
			}
			select {
			case ch <- pg:
			case <-ctx.Done():
				return
			}
			if pg.err != nil || pg.short {
				return
			}
		}
	}()
	return ch
}

// readSchema reads schema's held pages after `after`: the index rows, every
// table's copies, the tags. stop ends the reader and waits for it, so no read
// is in flight when the source closes.
func (m *migrator4) readSchema(ctx context.Context, schema string, after int64) (pages <-chan *migrate4Page, stop func()) {
	tables := m.bySchema[schema]
	ctx, cancel := context.WithCancel(ctx)
	ch := readPages4(ctx, func() *migrate4Page {
		t0 := time.Now()
		defer func() { m.addTime("read", time.Since(t0)) }()
		entries, err := m.src.IndexPageCopies(schema, tables, after, m.opt.PageRows, true)
		if err != nil {
			return &migrate4Page{err: err}
		}
		if len(entries) == 0 {
			return nil
		}
		pg := &migrate4Page{entries: entries, last: entries[len(entries)-1].RowID, short: len(entries) < m.opt.PageRows}
		after = pg.last
		cids := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.Orphan() {
				cids = append(cids, e.CID)
			}
		}
		if pg.tags, err = m.src.TagsFor(schema, cids); err != nil {
			return &migrate4Page{err: err}
		}
		return pg
	})
	return ch, func() {
		cancel()
		for range ch {
		}
	}
}

var timesMu sync.Mutex

func (m *migrator4) addTime(name string, d time.Duration) {
	timesMu.Lock()
	m.times[name] += d
	timesMu.Unlock()
}

// pageBatches turns a page into one migrate-mode batch per producer table
// and accumulates the page's progress into p (copies, tags, digests).
func (m *migrator4) pageBatches(schema string, pg *migrate4Page, idents map[string][32]byte, p *migrate4Progress) ([]*format4.Batch, error) {
	tables := m.bySchema[schema]
	typ, err := format4.TypeOf(schema)
	if err != nil {
		return nil, err
	}
	batches := make([]*format4.Batch, len(tables))
	tagIndex := make([]map[format4.Tag]int, len(tables))
	for _, e := range pg.entries {
		if e.Orphan() {
			p.Orphans++ // its record is gone: format 1 never serves it
			continue
		}
		var first *storage.LegacyRecord
		for ti, t := range tables {
			if !e.Held[ti] {
				continue
			}
			r := e.Copies[ti]
			isFirst := first == nil
			if isFirst {
				first = &r
			} else if r.Timestamp != first.Timestamp || string(r.Stored) != string(first.Stored) {
				p.Unified++
			}
			b := batches[ti]
			if b == nil {
				b = &format4.Batch{Type: typ, Peer: partitionPeer(t, r.PeerID), Mode: format4.ModeMigrate}
				batches[ti] = b
				tagIndex[ti] = map[format4.Tag]int{}
			}
			// Every copy carries its record's FIRST copy's bytes and ts: format
			// 4 keeps one record per seq (a copy holds the holder's bytes and
			// ts, as format 1's repeat-copy mirror wrote them), and the
			// partition, peer and signature stay the copy's own.
			in := format4.In{CID: r.CID, Plain: first.Plain, TS: first.Timestamp, Sig: legacySignature(r.SignatureHex), Seq: e.RowID}
			if first.Sealed {
				in.Sealed = first.Stored
			}
			peer := b.Peer
			if r.PeerID != b.Peer && r.PeerID != "" {
				in.Peer, peer = r.PeerID, r.PeerID
			}
			if isFirst {
				// The record's tags and identity go with its FIRST copy.
				for _, lt := range pg.tags[e.CID] {
					tag := formatTag(lt)
					k, ok := tagIndex[ti][tag]
					if !ok {
						k = len(b.Tags)
						b.Tags = append(b.Tags, tag)
						tagIndex[ti][tag] = k
					}
					in.Tags = append(in.Tags, format4.TagAt{Tag: k, At: lt.CreatedAt})
					p.TagDigest.add(tagDigestOf(e.CID, tag, lt.CreatedAt))
					p.Tags++
				}
				if id, ok := idents[e.CID]; ok {
					id := id
					in.Ident = &id
					p.Identities++
				}
			}
			b.Records = append(b.Records, in)
			c := p.Copies[t.Name]
			c.Rows++
			c.Bytes += int64(len(first.Stored))
			c.SourceBytes += int64(len(r.Stored))
			p.Copies[t.Name] = c
			p.CopyDigest.add(copyDigestOf(e.RowID, t.Token, peer, first.Timestamp, in.Sig, first.Stored))
		}
		p.Held++
		p.MaxSeq = e.RowID
	}
	return batches, nil
}

// partitionPeer is the peer a table's PUTs carry: the record's own peer when
// it routes to the table (the engine derives the partition token from it, and
// stores a row's peer only when it differs), else the table's token.
func partitionPeer(t storage.LegacyTable, peer string) string {
	if name, err := storage.ProducerStandardTableName(peer, t.Schema); err == nil && name == t.Name {
		return peer
	}
	return t.Token
}

func (m *migrator4) copySchema(ctx context.Context, schema string) error {
	p := m.progress(schema)
	if p.Done {
		return nil
	}
	var idents map[string][32]byte
	if m.specs[schema].Identity {
		var err error
		if idents, err = m.src.IngestIdentities(schema); err != nil {
			return err
		}
	}
	tables := m.bySchema[schema]
	m.logf("copying %s (%d producer tables) from index rowid %d", schema, len(tables), p.After)
	pages, stop := m.readSchema(ctx, schema, p.After)
	defer stop()
	for pg := range pages {
		if pg.err != nil {
			return pg.err
		}
		// The page's progress is staged and committed only once its PUTs
		// are durable.
		staged := *p
		staged.Copies = make(map[string]migrate4Tally, len(p.Copies))
		for k, v := range p.Copies {
			staged.Copies[k] = v
		}
		batches, err := m.pageBatches(schema, pg, idents, &staged)
		if err != nil {
			return err
		}
		rejected, err := m.put(ctx, tables, batches)
		if err != nil {
			return err
		}
		if err := m.step("page"); err != nil {
			return err
		}
		staged.After = pg.last
		*p = staged
		m.j.Rejected = append(m.j.Rejected, rejected...)
		every := journalEvery
		if m.opt.testJournalEvery > 0 {
			every = m.opt.testJournalEvery
		}
		if time.Since(m.lastJournal) >= every {
			if err := m.saveJournal(); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.Done = true
	return m.saveJournal()
}

// put sends a page's batches, one per producer table, in table-name order
// (so partitions are created in an order a rerun repeats), and returns the
// copies the engine refused.
func (m *migrator4) put(ctx context.Context, tables []storage.LegacyTable, batches []*format4.Batch) ([]migrate4Reject, error) {
	t0 := time.Now()
	defer func() { m.addTime("put", time.Since(t0)) }()
	var rejected []migrate4Reject
	for i, b := range batches {
		if b == nil {
			continue
		}
		outs, err := m.api.Put(ctx, *b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tables[i].Name, err)
		}
		if len(outs) != len(b.Records) {
			return nil, fmt.Errorf("%s: %d outcomes for %d records", tables[i].Name, len(outs), len(b.Records))
		}
		for k, o := range outs {
			switch o.Action {
			case format4.ActMigrated, format4.ActCopy, format4.ActDup, format4.ActRetag:
				// Stored (a resent copy is a DUP, a resent tag a RETAG or DUP).
			default:
				r := b.Records[k]
				rejected = append(rejected, migrate4Reject{Table: tables[i].Name, CID: r.CID, Seq: r.Seq,
					Action: uint8(o.Action), Code: o.Reject})
			}
		}
		if err := m.step("put:" + tables[i].Name); err != nil {
			return nil, err
		}
	}
	return rejected, nil
}

func (m *migrator4) tallyReport() {
	m.rep.Records, m.rep.Copies, m.rep.RecordBytes = 0, 0, 0
	for _, p := range m.j.Schemas {
		m.rep.Records += p.Held
		for _, t := range p.Copies {
			m.rep.Copies += t.Rows
			m.rep.RecordBytes += t.Bytes
		}
	}
	var unified int64
	for _, p := range m.j.Schemas {
		unified += p.Unified
	}
	if unified > 0 {
		m.note("%d copies differed from their record's first copy in ts or bytes in format 1; format 4 stores the first copy's (C-12)", unified)
	}
	m.rep.Rejected, m.rep.Oversized = nil, nil
	for _, r := range m.j.Rejected {
		if r.Code == migrate4RejTooLarge {
			m.rep.Oversized = append(m.rep.Oversized, r)
		} else {
			m.rep.Rejected = append(m.rep.Rejected, r)
		}
	}
}

// ---- check --------------------------------------------------------------------------

// typeFields is what a type's rules (format 2's rule text, contract §2.3)
// extract: the legacy index columns COL0..COL3, the epoch, the object key.
type typeFields struct {
	cols     [4]bool
	epoch    bool
	epochDay bool
	object   []int
}

func parseTypeFields(rules string) (typeFields, error) {
	var f typeFields
	for _, line := range strings.Split(rules, "\n") {
		w := strings.Fields(line)
		if len(w) < 2 {
			continue
		}
		switch w[0] {
		case "epoch":
			f.epoch = true
		case "epoch_day":
			f.epochDay = true
		case "col":
			n, err := strconv.Atoi(w[1])
			if err != nil || n < 0 || n > 4 {
				return f, fmt.Errorf("rule %q", line)
			}
			if n < 4 {
				f.cols[n] = true
			}
		case "object":
			for _, s := range strings.Split(w[1], ",") {
				n, err := strconv.Atoi(s)
				if err != nil || n < 0 || n > 3 {
					return f, fmt.Errorf("rule %q", line)
				}
				f.object = append(f.object, n)
			}
		}
	}
	return f, nil
}

// legacyColumn is format 1's value of COLn (NULL = absent).
func legacyColumn(e storage.IndexEntry, n int) (format2.Cell, bool) {
	switch n {
	case 0:
		return format2.Int(e.NoradCatID.Int64), e.NoradCatID.Valid
	case 1:
		return format2.Text(e.EntityID.String), e.EntityID.Valid
	case 2:
		return format2.Text(e.ObjectType.String), e.ObjectType.Valid
	case 3:
		return format2.Text(e.OpsStatusCode.String), e.OpsStatusCode.Valid
	}
	return format2.Cell{}, false
}

// expectedKey is the object key the engine must hold for a record with
// format 1's columns: the first present column of the object rule.
func (f typeFields) expectedKey(e storage.IndexEntry) string {
	for _, n := range f.object {
		if v, ok := legacyColumn(e, n); ok {
			if n == 0 {
				return strconv.FormatInt(v.I, 10)
			}
			if s := string(v.B); s != "" {
				return s
			}
		}
	}
	return ""
}

// laneKey5 is format 1's lane (sdn_record_source_summary): a tag identity
// without its content key.
type laneKey5 struct{ typ, provider, source, batch, peer, pubkey string }

type laneCount struct{ n, bytes int64 }

// check compares the target with the format-1 store (want: what the copy
// sent, per schema).
func (m *migrator4) check(ctx context.Context, want map[string]*migrate4Progress) (*migrate4Check, error) {
	t0 := time.Now()
	c := &migrate4Check{}
	defer func() {
		c.Took = time.Since(t0).Round(time.Millisecond).String()
		m.addTime("check", time.Since(t0))
	}()
	m.logf("checking the format-4 store against format 1")
	lanes := map[laneKey5]laneCount{}
	for _, schema := range m.schemas {
		got, err := m.checkSchema(ctx, schema, c, lanes)
		if err != nil {
			return c, err
		}
		c.Schemas++
		w := want[schema]
		if w == nil {
			w = &migrate4Progress{}
		}
		if got.Held != w.Held || got.Orphans != w.Orphans {
			c.bad("%s: %d held records and %d orphans; the copy sent %d and skipped %d", schema, got.Held, got.Orphans, w.Held, w.Orphans)
		}
		if got.Orphans > 0 {
			if c.Orphans == nil {
				c.Orphans = map[string]int64{}
			}
			c.Orphans[schema] = got.Orphans
			m.note("%s: %d index rows are orphans (no producer table holds the record); not migrated", schema, got.Orphans)
		}
		if got.CopyDigest != w.CopyDigest {
			c.bad("%s: the copies (bytes, ts, signature, peer, producer, seq) differ from what the copy sent", schema)
		}
		if got.TagDigest != w.TagDigest || got.Tags != w.Tags {
			c.bad("%s: the tag instances (%d) differ from what the copy sent (%d)", schema, got.Tags, w.Tags)
		}
	}
	// Index rows of a schema no producer table holds are orphans too.
	for _, s := range m.index {
		if _, ok := m.bySchema[s.Schema]; !ok && s.Rows > 0 {
			if c.Orphans == nil {
				c.Orphans = map[string]int64{}
			}
			c.Orphans[s.Schema] = s.Rows
			m.note("%s: %d index rows and no producer table: orphans, not migrated", s.Schema, s.Rows)
		}
	}
	if err := m.checkCounters(ctx, c, want, lanes); err != nil {
		return c, err
	}
	rows, err := m.api.Rebuild(ctx, "", format4.RebuildVerify)
	if err != nil {
		return c, fmt.Errorf("verify rebuild: %w", err)
	}
	c.Rebuild = rows
	for _, r := range rows {
		if r.Mismatches != 0 {
			c.bad("verify rebuild %s: %d mismatches in %d entries", r.Type, r.Mismatches, r.Entries)
		}
	}
	if len(m.j.Rejected) > 0 {
		n := map[int32]int{}
		for _, r := range m.j.Rejected {
			n[r.Code]++
		}
		for code, k := range n {
			c.bad("%d record copies refused by the engine, code %d (%s)", k, code, format4.RejectReason(code))
		}
	}
	if c.MismatchCount > 0 {
		return c, fmt.Errorf("store-migrate --to 4: the check failed (%d mismatches); format 4 was not activated", c.MismatchCount)
	}
	return c, nil
}

// checkSchema reads schema's held index rows from format 1 and their records
// from format 4, compares them, and returns the digests of what format 4
// holds. lanes accumulates the recount of the schema's lanes.
func (m *migrator4) checkSchema(ctx context.Context, schema string, c *migrate4Check, lanes map[laneKey5]laneCount) (*migrate4Progress, error) {
	got := &migrate4Progress{}
	typ, err := format4.TypeOf(schema)
	if err != nil {
		return nil, err
	}
	fields, err := parseTypeFields(m.specs[schema].Rules)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", schema, err)
	}
	tables := m.bySchema[schema]
	tableOf := make(map[string]int, len(tables)) // producer token -> table index (name order)
	for i, t := range tables {
		tableOf[t.Token] = i
	}
	var after int64
	for {
		entries, err := m.src.IndexPageCopies(schema, tables, after, m.opt.PageRows, false)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			break
		}
		after = entries[len(entries)-1].RowID
		cids := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.Orphan() {
				got.Orphans++
			} else {
				cids = append(cids, e.CID)
			}
		}
		recs, err := m.api.Get(ctx, typ, cids, true, true)
		if err != nil {
			return nil, fmt.Errorf("%s get: %w", typ, err)
		}
		tags, err := m.api.Tags(ctx, typ, cids)
		if err != nil {
			return nil, fmt.Errorf("%s tags: %w", typ, err)
		}
		copies := map[string][]format4.Rec{}
		for _, r := range recs {
			copies[r.CID] = append(copies[r.CID], r)
		}
		firstLen := map[string]int64{}
		var samples []storage.IndexEntry
		for _, e := range entries {
			if e.Orphan() {
				if len(copies[e.CID]) > 0 {
					c.bad("%s %s (index rowid %d): an orphan in format 1, %d copies in format 4", schema, e.CID, e.RowID, len(copies[e.CID]))
				}
				continue
			}
			got.Held++
			cs := copies[e.CID]
			want, err := cidDigest(e.CID)
			if err != nil {
				c.bad("%s %s: %v", schema, e.CID, err)
				continue
			}
			c.Records++
			// Exactly the tables holding the record hold a copy.
			byTable := make([]*format4.Rec, len(tables))
			for i := range cs {
				r := &cs[i]
				c.Copies++
				ti, ok := tableOf[r.Producer]
				switch {
				case !ok:
					c.bad("%s %s: a copy in partition %q, which no format-1 table names", schema, e.CID, r.Producer)
					continue
				case !e.Held[ti]:
					c.bad("%s %s: a copy in partition %q; format 1's %s does not hold the record", schema, e.CID, r.Producer, tables[ti].Name)
				case byTable[ti] != nil:
					c.bad("%s %s: two copies in partition %q", schema, e.CID, r.Producer)
				}
				byTable[ti] = r
				if r.Seq != e.RowID {
					c.bad("%s %s (%s): seq %d, format-1 index rowid %d", schema, e.CID, r.Producer, r.Seq, e.RowID)
				}
				if encfield.IsSealed(r.Data) {
					c.Sealed++
				} else {
					c.Rehashed++
					if sha256.Sum256(r.Data) != want {
						c.bad("%s %s (%s): the stored bytes do not hash to the CID", schema, e.CID, r.Producer)
					}
				}
				if int64(len(r.Data)) != r.Len {
					c.bad("%s %s (%s): len %d, %d bytes", schema, e.CID, r.Producer, r.Len, len(r.Data))
				}
				got.CopyDigest.add(copyDigestOf(r.Seq, r.Producer, r.Peer, r.TS, r.Sig, r.Data))
			}
			var first *format4.Rec
			for ti, held := range e.Held {
				if held && byTable[ti] == nil {
					c.bad("%s %s: format 1's %s holds the record, format 4 has no copy in partition %q", schema, e.CID, tables[ti].Name, tables[ti].Token)
				}
				if first == nil && byTable[ti] != nil {
					first = byTable[ti]
				}
			}
			if first != nil {
				firstLen[e.CID] = first.Len
				m.checkFields(schema, fields, e.IndexEntry, *first, c)
			}
			if e.RowID%migrate4SampleEvery == 0 {
				samples = append(samples, e.IndexEntry)
			}
		}
		for _, t := range tags {
			got.Tags++
			got.TagDigest.add(tagDigestOf(t.CID, t.Tag, t.At))
			k := laneKey5{typ, t.Provider, t.Source, t.Batch, t.ProducerPeer, t.ProducerPubkey}
			lc := lanes[k]
			lc.n++
			lc.bytes += firstLen[t.CID]
			lanes[k] = lc
		}
		c.TagInstances += int64(len(tags))
		if err := m.checkColumns(ctx, schema, typ, fields, samples, c); err != nil {
			return nil, err
		}
		if len(entries) < m.opt.PageRows {
			break
		}
	}
	return got, nil
}

func cidDigest(text string) ([32]byte, error) {
	var d [32]byte
	c, err := cid.Decode(text)
	if err != nil {
		return d, err
	}
	b := c.Bytes()
	if len(b) != 36 || b[0] != 0x01 || b[1] != 0x55 || b[2] != 0x12 || b[3] != 0x20 {
		return d, fmt.Errorf("not a CIDv1 raw sha2-256")
	}
	copy(d[:], b[4:])
	return d, nil
}

// checkFields compares the epoch and object key the engine extracted with
// format 1's index columns, and the columns the type's rules cannot serve.
func (m *migrator4) checkFields(schema string, f typeFields, e storage.IndexEntry, r format4.Rec, c *migrate4Check) {
	c.FieldChecks++
	switch {
	case r.HasEpoch != e.EpochUnix.Valid:
		c.bad("%s %s: epoch present %v in format 4, %v in format 1", schema, e.CID, r.HasEpoch, e.EpochUnix.Valid)
	case r.HasEpoch && r.Epoch != e.EpochUnix.Int64:
		c.bad("%s %s: epoch %d in format 4, %d in format 1", schema, e.CID, r.Epoch, e.EpochUnix.Int64)
	}
	if want := f.expectedKey(e); r.Key != want {
		c.bad("%s %s: object key %q in format 4, %q from format 1's columns", schema, e.CID, r.Key, want)
	}
	// Format 4 derives the day from the epoch (EPOCH_DAY); format 1 stored it.
	wantDay := ""
	if e.EpochUnix.Valid && f.epochDay {
		wantDay = time.Unix(e.EpochUnix.Int64, 0).UTC().Format("2006-01-02")
	}
	if e.EpochDay.String != wantDay || e.EpochDay.Valid != (wantDay != "") {
		c.bad("%s %s: format 1's epoch_day %q is not the UTC day of its epoch (%q)", schema, e.CID, e.EpochDay.String, wantDay)
	}
	for n := 0; n < 4; n++ {
		if _, ok := legacyColumn(e, n); ok && !f.cols[n] {
			c.bad("%s %s: format 1 indexes COL%d, which %s's rules do not extract", schema, e.CID, n, schema)
		}
	}
}

// checkColumns asks the engine, for each sampled record, whether its
// extracted columns equal format 1's: every present column at once (exactly
// the record matches), then each absent column (NOTNULL matches nothing).
func (m *migrator4) checkColumns(ctx context.Context, schema, typ string, f typeFields, samples []storage.IndexEntry, c *migrate4Check) error {
	if len(samples) == 0 {
		return nil
	}
	type probe struct {
		e     storage.IndexEntry
		preds []format4.Pred
		want  int64
		what  string
	}
	var probes []probe
	for _, e := range samples {
		var eq []format4.Pred
		for n := 0; n < 4; n++ {
			if !f.cols[n] {
				continue
			}
			field := format4.Field(int(format4.FieldCol0) + n)
			if v, ok := legacyColumn(e, n); ok {
				eq = append(eq, format4.Pred{Field: field, Op: format4.OpEq, Values: []format2.Cell{v}})
			} else {
				probes = append(probes, probe{e: e, want: 0, what: fmt.Sprintf("COL%d absent", n),
					preds: []format4.Pred{{Field: field, Op: format4.OpNotNull}}})
			}
		}
		if len(eq) > 0 {
			probes = append(probes, probe{e: e, preds: eq, want: 1, what: "every present column"})
		}
		c.ColumnSamples++
	}
	t0 := time.Now()
	defer func() { m.addTime("check_columns", time.Since(t0)) }()
	var mu sync.Mutex
	var firstErr error
	work := make(chan probe)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				h, err := m.api.Head(ctx, format4.Query{Type: typ, CID: p.e.CID, Preds: p.preds})
				mu.Lock()
				switch {
				case err != nil:
					if firstErr == nil {
						firstErr = fmt.Errorf("%s column check of %s: %w", typ, p.e.CID, err)
					}
				case h.N != p.want:
					c.bad("%s %s: %s: the engine matches %d, format 1's columns say %d", schema, p.e.CID, p.what, h.N, p.want)
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range probes {
		work <- p
	}
	close(work)
	wg.Wait()
	return firstErr
}

// checkCounters compares format 4's partition, type and lane counters with
// what the copy sent and with format 1's.
func (m *migrator4) checkCounters(ctx context.Context, c *migrate4Check, want map[string]*migrate4Progress, lanes map[laneKey5]laneCount) error {
	// Partitions: (type, producer) = a producer table.
	parts, err := m.api.Partitions(ctx)
	if err != nil {
		return err
	}
	type pk struct{ typ, producer string }
	got := map[pk]format4.PartitionSummary{}
	for _, p := range parts {
		got[pk{p.Type, p.Producer}] = p
	}
	oracle, err := m.src.PartitionCounters()
	if err != nil {
		return err
	}
	seen := map[pk]bool{}
	for _, t := range m.tables {
		typ, _ := format4.TypeOf(t.Schema)
		k := pk{typ, t.Token}
		seen[k] = true
		c.Partitions++
		var sent migrate4Tally
		if w := want[t.Schema]; w != nil {
			sent = w.Copies[t.Name]
		}
		if g := got[k]; g.Records != sent.Rows || g.Bytes != sent.Bytes {
			c.bad("partition %s/%s: %d records, %d B; the copy sent %d, %d B", typ, t.Token, g.Records, g.Bytes, sent.Rows, sent.Bytes)
		}
		// Every row of the table was copied: format 1's counter, or a
		// recount where the counter drifted, holds exactly what was read.
		if o, ok := oracle[t.Name]; ok && o.Count == sent.Rows && o.Bytes == sent.SourceBytes {
			continue
		}
		rc, err := m.src.TableCounter(t)
		if err != nil {
			return err
		}
		if o, ok := oracle[t.Name]; ok && (o.Count != rc.Count || o.Bytes != rc.Bytes) {
			m.note("format 1's counter of %s (%d, %d B) differs from a recount (%d, %d B); the recount is the oracle",
				t.Name, o.Count, o.Bytes, rc.Count, rc.Bytes)
		}
		if rc.Count != sent.Rows || rc.Bytes != sent.SourceBytes {
			c.bad("table %s holds %d rows (%d B), %d (%d B) were migrated: rows without a datasync index row are not migrated",
				t.Name, rc.Count, rc.Bytes, sent.Rows, sent.SourceBytes)
		}
	}
	for k, g := range got {
		if !seen[k] && g.Records > 0 {
			c.bad("partition %s/%s holds %d records that no format-1 table names", k.typ, k.producer, g.Records)
		}
	}
	// Types: unique records, copies and bytes, the last seq.
	types, err := m.api.Types(ctx)
	if err != nil {
		return err
	}
	byType := map[string]format4.TypeSummary{}
	for _, t := range types {
		byType[t.Type] = t
	}
	for _, schema := range m.schemas {
		typ, _ := format4.TypeOf(schema)
		w := want[schema]
		if w == nil {
			w = &migrate4Progress{}
		}
		var copies, bytes int64
		for _, t := range w.Copies {
			copies += t.Rows
			bytes += t.Bytes
		}
		g := byType[typ]
		if g.Records != w.Held || g.Copies != copies || g.CopyBytes != bytes || g.MaxSeq != w.MaxSeq {
			c.bad("type %s: %d records, %d copies, %d B, max seq %d; the copy sent %d, %d, %d B, %d",
				typ, g.Records, g.Copies, g.CopyBytes, g.MaxSeq, w.Held, copies, bytes, w.MaxSeq)
		}
	}
	// Lanes: per format-1 lane, summed over partitions and content keys, the
	// recount from the tags (each tag counts its record's FIRST copy).
	engineLanes, err := m.api.Lanes(ctx, "")
	if err != nil {
		return err
	}
	gotLanes := map[laneKey5]laneCount{}
	for _, l := range engineLanes {
		k := laneKey5{l.Type, l.Provider, l.Source, l.Batch, l.ProducerPeer, l.ProducerPubkey}
		lc := gotLanes[k]
		lc.n += l.Records
		lc.bytes += l.Bytes
		gotLanes[k] = lc
	}
	for k, w := range lanes {
		c.Lanes++
		if g := gotLanes[k]; g != w {
			c.bad("lane %v: %d records, %d B in format 4; the tags recount %d, %d B", k, g.n, g.bytes, w.n, w.bytes)
		}
	}
	for k, g := range gotLanes {
		if _, ok := lanes[k]; !ok && g.n > 0 {
			c.bad("lane %v: %d records in format 4, none in the recount", k, g.n)
		}
	}
	// Format 1's lane counters are incremental and can drift: report it.
	summary, err := m.src.SourceSummaries()
	if err != nil {
		return err
	}
	drift := 0
	for _, s := range summary {
		typ, err := format4.TypeOf(s.Schema)
		if err != nil {
			continue
		}
		k := laneKey5{typ, s.ProviderID, s.SourceName, s.BatchID, s.ProducerPeerID, s.ProducerPublicKey}
		if w := lanes[k]; w.n != s.Count || w.bytes != s.Bytes {
			if drift++; drift <= 5 {
				m.note("format 1's lane %v (%d, %d B) differs from the recount (%d, %d B); the recount is the oracle", k, s.Count, s.Bytes, w.n, w.bytes)
			}
		}
	}
	if drift > 5 {
		m.note("%d format-1 lane counters differ from the recount in all", drift)
	}
	return nil
}

// ---- verify-only --------------------------------------------------------------------

// verifyOnly re-checks an activated store against the format-1 store kept in
// pre-format4/: the copy pass runs without writing, to rebuild what the
// copy sent, then the check runs against the engine.
func (m *migrator4) verifyOnly(ctx context.Context) error {
	m.rep.Activated = true
	lock, err := storage.LockStoreForMaintenance(m.root)
	if err != nil {
		return fmt.Errorf("--verify-only needs the store lock (stop the daemon): %w", err)
	}
	defer lock.Release()
	if err := m.openSourceAt(filepath.Join(m.root, marker.PreFormat4Dir)); err != nil {
		return err
	}
	if raw, err := os.ReadFile(m.jpath); err == nil {
		var j migrate4Journal
		if json.Unmarshal(raw, &j) == nil {
			m.rep.GseqFloor = j.GseqFloor
		}
	}
	m.j = &migrate4Journal{Version: 1, Schemas: map[string]*migrate4Progress{}}
	if err := m.openTarget(ctx, format4.OpenExisting, marker.Markers{}); err != nil {
		return err
	}
	for _, schema := range m.schemas {
		if err := m.scanSchema(ctx, schema); err != nil {
			return err
		}
	}
	m.tallyReport()
	c, err := m.check(ctx, m.j.Schemas)
	m.rep.Check = c
	return err
}

// scanSchema is the copy pass without the PUTs: it rebuilds what the copy
// sent from the format-1 store.
func (m *migrator4) scanSchema(ctx context.Context, schema string) error {
	p := m.progress(schema)
	var idents map[string][32]byte
	if m.specs[schema].Identity {
		var err error
		if idents, err = m.src.IngestIdentities(schema); err != nil {
			return err
		}
	}
	pages, stop := m.readSchema(ctx, schema, 0)
	defer stop()
	for pg := range pages {
		if pg.err != nil {
			return pg.err
		}
		if _, err := m.pageBatches(schema, pg, idents, p); err != nil {
			return err
		}
		p.After = pg.last
	}
	p.Done = true
	return ctx.Err()
}

// ---- control copy and activation ----------------------------------------------------

// copyControl writes the control tables into fsql4/control.db (the format-1
// engine's own copy, as format 2's interim control database).
func (m *migrator4) copyControl() error {
	st, err := m.src.CopyControl(migrate4ControlTmp, migrate4FTSTmp)
	if err != nil {
		return fmt.Errorf("control copy: %w", err)
	}
	dst := filepath.Join(m.root, marker.Dir, migrate4ControlDB)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(m.root, migrate4ControlTmp), dst); err != nil {
		return fmt.Errorf("move the control copy to %s: %w", dst, err)
	}
	// Leftovers: the interim full-text index (format 4 builds its own) and
	// any journal file the attach left beside the copies.
	for _, base := range []string{migrate4ControlTmp, migrate4FTSTmp} {
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			if base == migrate4ControlTmp && suffix == "" {
				continue
			}
			if err := os.Remove(filepath.Join(m.root, base+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
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
	for _, d := range []string{filepath.Join(m.root, marker.Dir), m.root} {
		if err := syncDirectory(d); err != nil {
			return err
		}
	}
	m.note("control copy: %d tables, %d indexes, %d rows, %d bytes (FTS rebuilt by format 4 after activation)",
		st.Tables, st.Indexes, st.Rows, st.Bytes)
	return nil
}

// activate is contract §2.3: the engine writes MIGRATED then STORE (the
// commit point); then control.flatsqldb* move to pre-format4/ and
// control.flatsqldb becomes a directory.
func (m *migrator4) activate(ctx context.Context) error {
	if err := m.timed("activate", func() error { return m.api.Activate(ctx) }); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	if err := m.step("activated"); err != nil {
		return err
	}
	if err := m.closeTarget(ctx); err != nil {
		return err
	}
	if err := m.src.Close(); err != nil {
		return err
	}
	m.src = nil
	if err := m.finishActivation(); err != nil {
		return err
	}
	m.j.Activated = true
	if err := m.saveJournal(); err != nil {
		return err
	}
	m.rep.Activated = true
	m.logf("activated format 4 in %s; format 1's control database is in %s", m.root, filepath.Join(m.root, marker.PreFormat4Dir))
	return nil
}

func (m *migrator4) finishActivation() error {
	lock, err := storage.LockStoreForMaintenance(m.root)
	if err != nil {
		return fmt.Errorf("activation needs the store lock: %w", err)
	}
	defer lock.Release()
	return marker.FinishActivation(m.root)
}

// ---- machine numbers ----------------------------------------------------------------

func maxRSSBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) // bytes
	}
	return int64(ru.Maxrss) * 1024 // KiB
}

// migrateLoadAverage is the 1-minute load average (-1 when unknown).
func migrateLoadAverage() float64 {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		if raw, err = exec.Command("sysctl", "-n", "vm.loadavg").Output(); err != nil {
			return -1
		}
	}
	var a float64
	if _, err := fmt.Sscanf(strings.Trim(strings.TrimSpace(string(raw)), "{} "), "%f", &a); err != nil {
		return -1
	}
	return a
}
