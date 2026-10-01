package storage

// Live record bytes: a counter per partition, kept on append.
//
// A partition is one (producer, SDS type) table, sds_p_<producer>__<TYPE>.
// Each has one row in sdn_partition_record_bytes (its record count and the sum
// of its record_length) that only writes to that partition move: SQLite
// triggers on the partition table update it in the same statement and the
// same transaction as the insert, delete or update that caused it, tagged or
// untagged, single or batch, supersede, delete, GC, reconcile. No row ever
// looks at another partition. When each partition gets its own writer, that
// writer owns its row.
//
// LiveRecordBytes used to be SELECT SUM(record_length) over every standard's
// union read source. record_length sits after the data BLOB in each row, so
// the sum walked every overflow page of every record: it held host-02's
// engine 21.9 s for OMM and past the 5-minute per-call budget for MPE
// (2026-09-26), which poisoned the engine and stopped CelesTrak ingest for
// 30 h. Now it adds up partition rows.
//
// A record two producers both hold is stored twice, once per partition, and
// is counted once per partition — the bytes each partition holds. (The union
// read source deduplicated by CID.)
//
// A store written before the counter existed is counted once, in the
// background, a bounded chunk per statement. A trigger counts a row only when
// its rowid is at or below the partition's cursor, and a chunk adds the rows
// it passes and moves the cursor in ONE UPDATE statement, so every row is
// counted exactly once however writes interleave. Until every partition is
// counted, readers get ErrLiveRecordBytesReconciling, never a partial sum.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// ErrLiveRecordBytesReconciling is returned while partitions written before
// the counter existed are still being counted. There is no total yet;
// callers must not treat this as zero.
var ErrLiveRecordBytesReconciling = errors.New("live record bytes: partitions written before the counter existed are still being counted; no total yet")

const (
	// partitionCountComplete is the cursor of a fully counted partition.
	//
	// 2^53-1, NOT math.MaxInt64. The engine hands every integer result back
	// to Go as a float64 (flatsql_result_cell_number), and MaxInt64 is not a
	// float64: it reads back as 2^63, which Go converts to int64 by the
	// CPU's rules — saturating to MaxInt64 on arm64, MinInt64 on amd64. So
	// the sentinel matched on the Mac and never on CI's x86-64 runners,
	// where no partition ever counted as done. Every integer up to 2^53 is
	// exact in a float64, and no rowid gets there.
	partitionCountComplete int64 = 1<<53 - 1
	// Counter triggers are named <family><version>_<ai|ad|au>_<partition>.
	// Bump the version whenever a trigger changes. The install drops older
	// triggers, and recounts every partition unless the older version counts
	// exactly what this one does (partitionTriggerCountCompatible).
	partitionTriggerFamily  = "sdn_prb"
	partitionTriggerVersion = "2"

	// Rebuild chunk bounds, in rows per statement. The chunk adapts toward
	// partitionChunkTarget per statement so it stays far below the engine's
	// per-call budget whatever the record size.
	partitionChunkMinRows     = 16
	partitionChunkStartRows   = 512
	partitionChunkMaxRows     = 16384
	partitionChunkTarget      = 100 * time.Millisecond
	partitionRebuildRetry     = 5 * time.Second
	partitionRebuildLogPeriod = 30 * time.Second
)

// partitionTriggerCountCompatible names older trigger versions that count
// exactly what the current one does: replacing them keeps every counter.
// Version 1 lacked only the `changed` marker.
var partitionTriggerCountCompatible = map[string]bool{"1": true}

// `changed` is total_changes() + 1 as of the row's last update: the snapshot
// refresh after a write reads only the rows that moved. (+1 because inside a
// statement total_changes() still reads what it was before the statement:
// SQLite adds a statement's changes when it completes.)
const partitionCounterTableSQL = `CREATE TABLE IF NOT EXISTS sdn_partition_record_bytes (
	table_name    TEXT PRIMARY KEY,
	scanned_rowid INTEGER NOT NULL DEFAULT 0,
	record_count  INTEGER NOT NULL DEFAULT 0,
	record_bytes  INTEGER NOT NULL DEFAULT 0,
	changed       INTEGER NOT NULL DEFAULT 0
)`

// partitionCountRebuild is the background counting worker's state.
type partitionCountRebuild struct {
	kick     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	running  atomic.Bool
	stopOnce sync.Once
	// paused holds the worker before each chunk (tests).
	paused atomic.Bool
}

// partitionRebuildStartPaused opens stores with the counting worker held
// (tests drive the rebuild step by step).
var partitionRebuildStartPaused bool

func newPartitionCountRebuild() *partitionCountRebuild {
	r := &partitionCountRebuild{
		kick: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	r.paused.Store(partitionRebuildStartPaused)
	return r
}

func (r *partitionCountRebuild) request() {
	if r == nil {
		return
	}
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// partitionCount is one partition's counter.
type partitionCount struct {
	Table    string
	Producer string
	Standard string
	Counted  bool
	Records  int64
	Bytes    int64
}

func partitionTriggerNames(table string) [3]string {
	base := partitionTriggerFamily + partitionTriggerVersion
	return [3]string{base + "_ai_" + table, base + "_ad_" + table, base + "_au_" + table}
}

// partitionTriggerSQL returns the three CREATE TRIGGER statements for one
// partition. They read and write only that partition's counter row. Table
// names are validated identifiers (parseProducerStandardTable).
func partitionTriggerSQL(table string) [3]string {
	names := partitionTriggerNames(table)
	apply := func(ref, sign string) string {
		return fmt.Sprintf(`UPDATE sdn_partition_record_bytes SET `+
			`record_count = record_count %[2]s 1, record_bytes = record_bytes %[2]s %[1]s.record_length, changed = total_changes() + 1 `+
			`WHERE table_name = '%[3]s' AND %[1]s.rowid <= scanned_rowid;`, ref, sign, table)
	}
	return [3]string{
		fmt.Sprintf(`CREATE TRIGGER %s AFTER INSERT ON %s BEGIN %s END`, names[0], table, apply("NEW", "+")),
		fmt.Sprintf(`CREATE TRIGGER %s AFTER DELETE ON %s BEGIN %s END`, names[1], table, apply("OLD", "-")),
		fmt.Sprintf(`CREATE TRIGGER %s AFTER UPDATE ON %s `+
			`WHEN OLD.rowid IS NOT NEW.rowid OR OLD.record_length IS NOT NEW.record_length `+
			`BEGIN %s %s END`, names[2], table, apply("OLD", "-"), apply("NEW", "+")),
	}
}

// partitionChunkSQL counts a partition's rows in (?1, ?2] and moves its
// cursor to ?3 in one statement, guarded on the cursor still being ?1.
func partitionChunkSQL(table string) string {
	return fmt.Sprintf(`UPDATE sdn_partition_record_bytes SET (record_count, record_bytes, scanned_rowid, changed) = (
		SELECT sdn_partition_record_bytes.record_count + COUNT(*),
		       sdn_partition_record_bytes.record_bytes + COALESCE(SUM(chunk.record_length), 0),
		       ?3, total_changes() + 1
		FROM %s AS chunk WHERE chunk.rowid > ?1 AND chunk.rowid <= ?2
	) WHERE table_name = '%s' AND scanned_rowid = ?1`, table, table)
}

func execSingle(exec sqlQueryExecer, statement string) error {
	// One complete statement straight to SQLite: the driver's Exec splitter
	// would cut a trigger body at its inner semicolons.
	rows, err := exec.Query(statement)
	if err != nil {
		return err
	}
	return rows.Close()
}

// partitionTriggersInSchema lists the counter triggers present.
func partitionTriggersInSchema(exec sqlQueryExecer) (map[string]bool, error) {
	rows, err := exec.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'sdn\_prb%' ESCAPE '\'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func createPartitionTriggers(exec sqlQueryExecer, table string, existing map[string]bool) error {
	statements := partitionTriggerSQL(table)
	for i, name := range partitionTriggerNames(table) {
		if existing[name] {
			continue
		}
		if err := execSingle(exec, statements[i]); err != nil {
			return fmt.Errorf("create counter trigger %s: %w", name, err)
		}
	}
	return nil
}

// insertPartitionCounter adds a partition's counter row. An empty partition
// is counted by definition; one with rows starts uncounted.
func insertPartitionCounter(exec sqlQueryExecer, table string) error {
	var hasRows int
	if err := exec.QueryRow(fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s)`, table)).Scan(&hasRows); err != nil {
		return fmt.Errorf("probe %s: %w", table, err)
	}
	cursor := partitionCountComplete
	if hasRows != 0 {
		cursor = 0
	}
	if _, err := exec.Exec(`INSERT OR IGNORE INTO sdn_partition_record_bytes (table_name, scanned_rowid) VALUES (?, ?)`, table, cursor); err != nil {
		return fmt.Errorf("add counter for %s: %w", table, err)
	}
	return nil
}

// installPartitionCounters gives every partition on disk its triggers and
// counter row. Boot and engine recovery run it from initTables; it is cheap
// when nothing changed. A partition the counter has never seen starts
// uncounted and the background rebuild counts it.
func (s *FlatSQLStore) installPartitionCounters() error {
	if _, err := s.db.Exec(partitionCounterTableSQL); err != nil {
		return fmt.Errorf("create partition counter table: %w", err)
	}
	if err := s.ensureColumn("sdn_partition_record_bytes", "changed", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin partition counter install: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	existing, err := partitionTriggersInSchema(tx)
	if err != nil {
		return fmt.Errorf("list counter triggers: %w", err)
	}
	// A trigger of another version is dropped (dropping a partition drops its
	// own triggers). If it counted by other rules, every partition is counted
	// again.
	current := partitionTriggerFamily + partitionTriggerVersion + "_"
	stale := false
	for name := range existing {
		if !strings.HasPrefix(name, current) {
			version, _, _ := strings.Cut(strings.TrimPrefix(name, partitionTriggerFamily), "_")
			if !partitionTriggerCountCompatible[version] {
				stale = true
			}
			if err := execSingle(tx, `DROP TRIGGER IF EXISTS `+name); err != nil {
				return fmt.Errorf("drop counter trigger %s: %w", name, err)
			}
			delete(existing, name)
		}
	}
	if stale {
		if _, err := tx.Exec(`UPDATE sdn_partition_record_bytes SET scanned_rowid = 0, record_count = 0, record_bytes = 0`); err != nil {
			return fmt.Errorf("reset partition counters: %w", err)
		}
	}
	// A cursor written as math.MaxInt64 (the first release of this counter)
	// means "complete" too.
	if _, err := tx.Exec(`UPDATE sdn_partition_record_bytes SET scanned_rowid = ? WHERE scanned_rowid > ?`,
		partitionCountComplete, partitionCountComplete); err != nil {
		return fmt.Errorf("normalise partition counter cursors: %w", err)
	}

	partitions, err := s.listProducerStandardTables()
	if err != nil {
		return fmt.Errorf("list partitions: %w", err)
	}
	counted := map[string]bool{}
	rows, err := tx.Query(`SELECT table_name FROM sdn_partition_record_bytes`)
	if err != nil {
		return fmt.Errorf("read partition counters: %w", err)
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return err
		}
		counted[table] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Triggers first, then the row: a partition without a row is outside the
	// counted region, so every prefix of this is consistent.
	for _, p := range partitions {
		if err := createPartitionTriggers(tx, p.TableName, existing); err != nil {
			return err
		}
		if counted[p.TableName] {
			continue
		}
		if err := insertPartitionCounter(tx, p.TableName); err != nil {
			return err
		}
	}
	// Rows of partitions that are gone: their bytes went with them.
	if _, err := tx.Exec(`DELETE FROM sdn_partition_record_bytes WHERE table_name NOT IN (SELECT name FROM sqlite_master WHERE type = 'table')`); err != nil {
		return fmt.Errorf("drop counters of missing partitions: %w", err)
	}
	var pending int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sdn_partition_record_bytes WHERE scanned_rowid < ?`, partitionCountComplete).Scan(&pending); err != nil {
		return fmt.Errorf("count uncounted partitions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit partition counter install: %w", err)
	}
	committed = true
	s.refreshPartitionSnapshotFullLocked()
	if pending > 0 {
		log.Infof("Live record bytes: %d partition(s) written before the counter existed; counting them in the background (LiveRecordBytes reports %q until done)", pending, ErrLiveRecordBytesReconciling)
		s.partitionRebuild.request()
	}
	return nil
}

// registerPartition is the partition-creation hook: triggers, then the
// counter row, before the caller can write a row into it. Plain statements,
// so it joins the caller's transaction when there is one; a failure part-way
// leaves the partition uncounted — reported, never wrong — until the next
// install. Callers hold s.mu.
func (s *FlatSQLStore) registerPartition(tableName string) error {
	if _, _, ok := parseProducerStandardTable(tableName); !ok {
		return nil
	}
	if _, err := s.db.Exec(partitionCounterTableSQL); err != nil {
		return fmt.Errorf("create partition counter table: %w", err)
	}
	if err := createPartitionTriggers(s.db, tableName, map[string]bool{}); err != nil {
		return err
	}
	return insertPartitionCounter(s.db, tableName)
}

// partitionCountsLocked reads every partition's counter. A partition with no
// counter row, or one still being counted, has Counted false. Callers hold
// s.mu.
func (s *FlatSQLStore) partitionCountsLocked() ([]partitionCount, error) {
	if s.db == nil {
		return nil, ErrStoreClosed
	}
	partitions, err := s.listProducerStandardTables()
	if err != nil {
		return nil, fmt.Errorf("list partitions: %w", err)
	}
	rows, err := s.db.Query(`SELECT table_name, scanned_rowid, record_count, record_bytes FROM sdn_partition_record_bytes`)
	if err != nil {
		return nil, fmt.Errorf("read partition counters: %w", err)
	}
	type counter struct{ scanned, records, bytes int64 }
	counters := map[string]counter{}
	for rows.Next() {
		var table string
		var c counter
		if err := rows.Scan(&table, &c.scanned, &c.records, &c.bytes); err != nil {
			rows.Close()
			return nil, err
		}
		counters[table] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]partitionCount, 0, len(partitions))
	for _, p := range partitions {
		c, ok := counters[p.TableName]
		out = append(out, partitionCount{
			Table:    p.TableName,
			Producer: p.ProducerID,
			Standard: p.Standard,
			Counted:  ok && c.scanned == partitionCountComplete,
			Records:  c.records,
			Bytes:    c.bytes,
		})
	}
	return out, nil
}

// recognizedStandards maps each validator schema's table component to the
// schema name ("OMM" -> "OMM.fbs"): the standards the totals cover, exactly
// as the old per-schema scans did.
func (s *FlatSQLStore) recognizedStandards() map[string]string {
	if cached := s.recognizedCache.Load(); cached != nil {
		return *cached
	}
	out := map[string]string{}
	if s.validator == nil {
		return out
	}
	defer func() { s.recognizedCache.Store(&out) }()
	for _, schemaName := range s.validator.Schemas() {
		standard, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			continue
		}
		out[standard] = schemaName
	}
	return out
}

// partitionSnapshot is the partition counters as of the last write-lock
// release. Counter reads take it without s.mu or the engine lock: a counter
// only moves inside a write, and every write-lock release refreshes it, so
// between writes it is exactly the counters on disk.
type partitionSnapshot struct {
	counts []partitionCount
	closed bool
	// changes and schema are total_changes() and schema_version when it was
	// taken: unchanged marks mean nothing moved; a new schema (a partition
	// created or dropped) means a full re-read; otherwise only rows whose
	// `changed` passed the mark are read.
	changes int64
	schema  int64
}

// refreshPartitionSnapshotLocked brings the snapshot current. Callers hold
// s.mu (read side is enough). It costs one tiny statement when nothing moved
// and reads only the counter rows that did. An engine that cannot answer — a
// poisoned one, say — keeps the last snapshot: nothing can have moved a
// counter since, because no write can land on it either.
func (s *FlatSQLStore) refreshPartitionSnapshotLocked() {
	s.refreshPartitionSnapshot(false)
}

// refreshPartitionSnapshotFullLocked re-reads every counter: after an install,
// on a connection whose total_changes() starts again from zero.
func (s *FlatSQLStore) refreshPartitionSnapshotFullLocked() {
	s.refreshPartitionSnapshot(true)
}

func (s *FlatSQLStore) refreshPartitionSnapshot(full bool) {
	if s.rb != nil {
		return // a partitioned store: its own counters
	}
	if s.db == nil {
		return
	}
	var changes, schema int64
	if err := s.db.QueryRow(`SELECT total_changes(), (SELECT schema_version FROM pragma_schema_version)`).Scan(&changes, &schema); err != nil {
		return
	}
	prev := s.partitionSnap.Load()
	if !full && prev != nil && !prev.closed && prev.counts != nil && prev.schema == schema {
		if prev.changes == changes {
			return
		}
		if changes > prev.changes {
			if next, ok := s.partitionSnapshotDeltaLocked(prev, changes); ok {
				s.partitionSnap.Store(next)
				return
			}
		}
	}
	counts, err := s.partitionCountsLocked()
	if err != nil {
		return
	}
	if counts == nil {
		counts = []partitionCount{}
	}
	s.partitionSnap.Store(&partitionSnapshot{counts: counts, changes: changes, schema: schema})
}

// partitionSnapshotDeltaLocked applies the counter rows changed since prev.
// ok is false when a changed row names a partition prev does not hold.
func (s *FlatSQLStore) partitionSnapshotDeltaLocked(prev *partitionSnapshot, changes int64) (*partitionSnapshot, bool) {
	rows, err := s.db.Query(`SELECT table_name, scanned_rowid, record_count, record_bytes FROM sdn_partition_record_bytes WHERE changed > ?`, prev.changes)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	next := &partitionSnapshot{counts: append([]partitionCount(nil), prev.counts...), changes: changes, schema: prev.schema}
	index := make(map[string]int, len(next.counts))
	for i, c := range next.counts {
		index[c.Table] = i
	}
	for rows.Next() {
		var table string
		var scanned, records, bytes int64
		if err := rows.Scan(&table, &scanned, &records, &bytes); err != nil {
			return nil, false
		}
		i, ok := index[table]
		if !ok {
			return nil, false
		}
		next.counts[i].Counted = scanned == partitionCountComplete
		next.counts[i].Records = records
		next.counts[i].Bytes = bytes
	}
	if rows.Err() != nil {
		return nil, false
	}
	return next, true
}

// closePartitionSnapshot makes every later counter read answer ErrStoreClosed.
func (s *FlatSQLStore) closePartitionSnapshot() {
	s.partitionSnap.Store(&partitionSnapshot{closed: true})
}

// partitionCountsSnapshot returns the snapshot's counters, lock-free.
func (s *FlatSQLStore) partitionCountsSnapshot() ([]partitionCount, error) {
	snap := s.partitionSnap.Load()
	switch {
	case snap == nil:
		return nil, fmt.Errorf("%w (no counter read yet)", ErrLiveRecordBytesReconciling)
	case snap.closed:
		return nil, ErrStoreClosed
	}
	return snap.counts, nil
}

// liveRecordTotals sums the snapshot's partitions of every recognized
// standard, lock-free. It refuses with ErrLiveRecordBytesReconciling while
// any of them is uncounted.
func (s *FlatSQLStore) liveRecordTotals() (records, bytes int64, err error) {
	counts, err := s.partitionCountsSnapshot()
	if err != nil {
		return 0, 0, err
	}
	return s.sumRecognizedPartitions(counts)
}

// liveRecordTotalsLocked is liveRecordTotals from a fresh read, for a caller
// that holds the write lock and has just moved the counters itself
// (GarbageCollectToQuota between evictions).
func (s *FlatSQLStore) liveRecordTotalsLocked() (records, bytes int64, err error) {
	counts, err := s.partitionCountsLocked()
	if err != nil {
		return 0, 0, err
	}
	return s.sumRecognizedPartitions(counts)
}

func (s *FlatSQLStore) sumRecognizedPartitions(counts []partitionCount) (records, bytes int64, err error) {
	recognized := s.recognizedStandards()
	pending, total := 0, 0
	for _, c := range counts {
		if _, ok := recognized[c.Standard]; !ok {
			continue
		}
		total++
		if !c.Counted {
			pending++
			continue
		}
		records += c.Records
		bytes += c.Bytes
	}
	if pending > 0 {
		return 0, 0, fmt.Errorf("%w (%d of %d partitions still to count)", ErrLiveRecordBytesReconciling, pending, total)
	}
	return records, bytes, nil
}

// schemaPartitionTotals sums one schema's partitions from a counter read. ok
// is false when counts is nil or any of them is still being counted.
func schemaPartitionTotals(counts []partitionCount, schemaName string) (records, bytes int64, ok bool) {
	if counts == nil {
		return 0, 0, false
	}
	standard, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return 0, 0, false
	}
	for _, c := range counts {
		if c.Standard != standard {
			continue
		}
		if !c.Counted {
			return 0, 0, false
		}
		records += c.Records
		bytes += c.Bytes
	}
	return records, bytes, true
}

// LiveRecordBytesReconciled reports whether every partition is counted, i.e.
// whether LiveRecordBytes can answer. Lock-free.
func (s *FlatSQLStore) LiveRecordBytesReconciled() bool {
	if s.rb != nil {
		return s.rb.liveRecordBytesReconciled()
	}
	_, _, err := s.liveRecordTotals()
	return err == nil
}

// partitionRebuildStep counts at most maxRows rows of one uncounted
// partition; done is true when none is left. Each chunk is one
// self-contained statement over one partition, so it takes the store's READ
// lock only: readers keep going, and writers — the only thing that could
// move the partition under it — hold the write lock.
func (s *FlatSQLStore) partitionRebuildStep(maxRows int) (done bool, counted int64, err error) {
	defer s.lockRead("partition counter rebuild")()
	if s.db == nil {
		return false, 0, ErrStoreClosed
	}
	if s.engine != nil && s.engine.Poisoned() {
		return false, 0, errors.New("engine poisoned; waiting for its replacement")
	}
	var table string
	var cursor, before int64
	err = s.db.QueryRow(`SELECT table_name, scanned_rowid, record_count FROM sdn_partition_record_bytes WHERE scanned_rowid < ? ORDER BY table_name LIMIT 1`,
		partitionCountComplete).Scan(&table, &cursor, &before)
	if errors.Is(err, sql.ErrNoRows) {
		return true, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("pick uncounted partition: %w", err)
	}
	exists, err := s.tableExists(table)
	if err != nil {
		return false, 0, err
	}
	if !exists {
		_, err := s.db.Exec(`DELETE FROM sdn_partition_record_bytes WHERE table_name = ?`, table)
		return false, 0, err
	}

	upper, next := partitionCountComplete, partitionCountComplete
	var edge int64
	err = s.db.QueryRow(fmt.Sprintf(`SELECT rowid FROM %s WHERE rowid > ? ORDER BY rowid LIMIT 1 OFFSET ?`, table),
		cursor, maxRows-1).Scan(&edge)
	switch {
	case err == nil:
		upper, next = edge, edge
	case errors.Is(err, sql.ErrNoRows):
	default:
		return false, 0, fmt.Errorf("find chunk end in %s: %w", table, err)
	}
	if _, err := s.db.Exec(partitionChunkSQL(table), cursor, upper, next); err != nil {
		return false, 0, fmt.Errorf("count %s rows (%d, %d]: %w", table, cursor, upper, err)
	}
	var after int64
	if err := s.db.QueryRow(`SELECT record_count FROM sdn_partition_record_bytes WHERE table_name = ?`, table).Scan(&after); err != nil {
		return false, 0, fmt.Errorf("read %s counter: %w", table, err)
	}
	s.refreshPartitionSnapshotLocked()
	return false, after - before, nil
}

// startPartitionCountRebuild runs the counting worker for the store's
// lifetime. It sleeps until installPartitionCounters finds an uncounted
// partition.
func (s *FlatSQLStore) startPartitionCountRebuild() {
	r := s.partitionRebuild
	if r == nil || !r.running.CompareAndSwap(false, true) {
		return
	}
	go s.runPartitionCountRebuild(r)
}

// stopPartitionCountRebuild stops and joins the worker. Call it WITHOUT the
// store lock: the worker may be waiting for it.
func (s *FlatSQLStore) stopPartitionCountRebuild() {
	r := s.partitionRebuild
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stop)
		if r.running.Load() {
			<-r.done
		}
	})
}

func (s *FlatSQLStore) runPartitionCountRebuild(r *partitionCountRebuild) {
	defer close(r.done)
	for {
		select {
		case <-r.stop:
			return
		case <-r.kick:
		}
		if !s.drainPartitionCountRebuild(r) {
			return
		}
	}
}

func (r *partitionCountRebuild) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.stop:
		return false
	case <-t.C:
		return true
	}
}

// drainPartitionCountRebuild counts until nothing is left, yielding at least
// as long as each chunk ran so the engine stays at least half free. Returns
// false when the store is stopping.
func (s *FlatSQLStore) drainPartitionCountRebuild(r *partitionCountRebuild) bool {
	chunk := partitionChunkStartRows
	started := time.Now()
	lastLog := started
	var counted int64
	for {
		for r.paused.Load() {
			if !r.sleep(10 * time.Millisecond) {
				return false
			}
		}
		select {
		case <-r.stop:
			return false
		default:
		}
		stepStart := time.Now()
		done, n, err := s.partitionRebuildStep(chunk)
		held := time.Since(stepStart)
		if err != nil {
			if errors.Is(err, ErrStoreClosed) {
				return false
			}
			log.Warnf("Live record bytes: counting paused (%v); retrying in %s", err, partitionRebuildRetry)
			if !r.sleep(partitionRebuildRetry) {
				return false
			}
			continue
		}
		if done {
			log.Infof("Live record bytes: every partition counted (%d rows in %s); LiveRecordBytes reads the partition counters",
				counted, time.Since(started).Round(time.Millisecond))
			return true
		}
		counted += n
		switch {
		case held < partitionChunkTarget/2 && chunk < partitionChunkMaxRows:
			chunk *= 2
		case held > 2*partitionChunkTarget && chunk > partitionChunkMinRows:
			chunk /= 2
		}
		if time.Since(lastLog) >= partitionRebuildLogPeriod {
			lastLog = time.Now()
			log.Infof("Live record bytes: counted %d rows so far (%s, chunk %d rows)", counted, time.Since(started).Round(time.Second), chunk)
		}
		pause := held
		if pause < 2*time.Millisecond {
			pause = 2 * time.Millisecond
		}
		if !r.sleep(pause) {
			return false
		}
	}
}
