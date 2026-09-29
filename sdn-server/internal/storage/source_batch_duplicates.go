package storage

// source_batch_duplicates.go — the duplicate reconcile every source ingest
// runs (storage.ingest_with_source, reconcile "duplicates" — the default —
// once before the batch is stored and once after).
//
// Inside ONE (provider, source, batch), records that share a logical index key
// (norad_cat_id, entity_id, object_type, ops_status_code, epoch_unix,
// epoch_day) are duplicates: the reconcile keeps the record of each key's
// newest tag row (tag created_at, then record timestamp, then CID, all
// descending), deletes the batch's tags of every OTHER record of the key, and
// deletes a record only when no tag of the schema names its CID any more —
// from every (producer, standard) table, the record index and the engine hot
// window. A record is never its own duplicate: one CID can carry several tags
// of the batch (one per producer and content key), and none of them is ever
// evicted by another (sourceBatchDuplicateRank).
//
// A CALL COSTS ITS OWN BATCH, NEVER THE SCHEMA, AND NO SINGLE LOCK HOLD PAYS
// FOR THE WHOLE BATCH. host-02, 2026-09-28/29: this call held the store write
// lock 9.3 s where its ordinary holds were 8 ms. It ran under one write hold
// from start to end: the rank of the whole batch (twice: a count, then the
// staging), then `cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE
// schema_name = ?)` once for the count and once per partition mirror — SQLite
// answers a non-correlated NOT IN by materializing the list, every tag of the
// schema (~1.9 M $OMM tags on host-02) — then the anti-join of the whole
// residency ledger (the 400,000-row hot window) against the record index, then
// the lane's summary. On host-02's $OMM shape (internal/stress,
// TestReconcileDuplicatesShapeHost02) 256 duplicates in the newest celestrak
// batch held the lock 13.5 s, and the whole batch published again 31.3 s; the
// longest write hold of either is now 335 ms. sdn 1480b98e5 removed the same
// pattern from the dataset supersede (dataset_supersede.go); this is that
// pattern applied here:
//   - the rank reads the batch in slices of the batch's tag index, one READ
//     hold per slice, and picks each key's newest row in Go
//     (sourceBatchDuplicateRank reproduces the SQL window's order and its
//     partition equality, including its storage-class rules);
//   - the losers are evicted in chunks, one write hold + transaction each,
//     sized from the last hold by nextSupersedeChunk. A chunk computes its
//     orphan set ONCE (deleteStagedOrphansTx: a correlated NOT EXISTS seeking
//     the (schema_name, cid) prefix of the tag table's key); the count, the
//     partition-mirror deletes, the index delete and the engine tombstones all
//     use it;
//   - the whole-ledger residency sweep the old call ran runs once after the
//     chunks, in pages (sweepOrphanedEngineRowsPaged), so a residency row
//     orphaned by any other path is still tombstoned exactly as before;
//   - the lane's summary is rebuilt under a hold of its own.
//
// Between holds the batch can change. A row stored into it after the rank
// waits for the next call (every ingest runs one); a newer duplicate of a
// staged loser leaves that loser a loser; a loser another path evicted first
// is simply gone, and its tag delete matches nothing. If another path deletes
// a key's kept row between the rank and the chunk that evicts the key's
// losers, the key is left with no row: the outcome of the single hold
// followed by that delete (a supersede or current-batch reconcile evicting
// the batch, record GC, a record delete). So the call always ends where some
// order of the old call and the other writers would have.

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// duplicateRankSlice bounds the batch tags one rank slice reads under one
// read hold: a range of the batch's tag index plus, per tag, a seek of its
// index row and one CID seek per producer table. On host-02's $OMM shape the
// whole 32,000-row batch ranked in one statement took ~0.7 s on the owner's
// machine, so a slice is ~45 ms there. A variable so a test can cross slice
// boundaries with a small fixture.
var duplicateRankSlice = 2048

const (
	// duplicateStageValues bounds one INSERT's VALUES list when a chunk stages
	// its CIDs.
	duplicateStageValues = 500
	// duplicateReconcileSite names the reconcile's store-lock holds.
	duplicateReconcileSite = "ReconcileSourceBatchIndexedDuplicates"
)

// ReconcileSourceBatchIndexedDuplicates removes duplicate indexed records
// inside a single provider/source/batch. It keeps the record of each logical
// indexed key's newest source-tagged row, with every tag that record has in the
// batch, and only deletes a record row when no source tags remain for that CID.
// Matched counts the tag rows of the records that lose their key to another
// record; Deleted counts the records deleted. See the file comment for how the
// work is split into bounded lock holds.
func (s *FlatSQLStore) ReconcileSourceBatchIndexedDuplicates(schemaName, providerID, sourceName, batchID string, apply bool) (SourceBatchDuplicateReconcileResult, error) {
	if apply {
		if err := s.requireWritable("reconcile source batch duplicates"); err != nil {
			return SourceBatchDuplicateReconcileResult{}, err
		}
	}
	result := SourceBatchDuplicateReconcileResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		BatchID:    strings.TrimSpace(batchID),
		Apply:      apply,
	}
	if result.SchemaName == "" {
		return result, errors.New("schema name is required")
	}
	if result.ProviderID == "" {
		return result, errors.New("provider id is required")
	}
	if result.SourceName == "" {
		return result, errors.New("source name is required")
	}
	if result.BatchID == "" {
		return result, errors.New("batch id is required")
	}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("invalid schema name: %w", err)
	}

	started := time.Now()
	noteHold := func(held time.Duration) {
		if held > result.MaxLockHold {
			result.MaxLockHold = held
		}
	}
	rank, err := s.rankSourceBatchDuplicates(result, noteHold)
	if err != nil {
		return result, err
	}
	result.Matched = rank.matched
	if !apply || result.Matched == 0 {
		return result, nil
	}

	losers := rank.loserCIDs()
	chunk := nextSupersedeChunk(0, 0)
	for start := 0; start < len(losers); {
		end := start + chunk
		if end > len(losers) {
			end = len(losers)
		}
		deleted, held, err := s.evictSourceBatchDuplicates(result, tableName, losers[start:end])
		noteHold(held)
		if err != nil {
			return result, err
		}
		result.Deleted += deleted
		result.Chunks++
		chunk = nextSupersedeChunk(chunk, held)
		start = end
	}

	if err := s.sweepOrphanedEngineRowsPaged(duplicateReconcileSite+": engine residency sweep", result.SchemaName, noteHold); err != nil {
		return result, err
	}
	held, err := s.holdWrite(duplicateReconcileSite+": source summary", func() error {
		if err := s.closedErr(); err != nil {
			return err
		}
		return s.rebuildSourceSummaryForSourceBatch(result.SchemaName, tableName, result.ProviderID, result.SourceName, result.BatchID)
	})
	noteHold(held)
	if err != nil {
		return result, err
	}
	log.Infof("Source batch duplicates %s %s/%s/%s: %d duplicate tags, %d distinct CIDs evicted in %d chunks, %d records deleted, in %s; longest store-lock hold %s",
		result.SchemaName, result.ProviderID, result.SourceName, result.BatchID,
		result.Matched, len(losers), result.Chunks, result.Deleted,
		time.Since(started).Round(time.Millisecond), result.MaxLockHold.Round(time.Millisecond))
	return result, nil
}

// sourceBatchDuplicateRow is one tag row of the batch, with the two values
// the rank orders it by besides its CID.
type sourceBatchDuplicateRow struct {
	cid       string
	createdAt any
	timestamp any
}

// newerThan reports whether r ranks before o in the reconcile's order:
// tags.created_at DESC, record timestamp DESC, tags.cid DESC.
func (r sourceBatchDuplicateRow) newerThan(o sourceBatchDuplicateRow) bool {
	if c := compareSQLiteValues(r.createdAt, o.createdAt); c != 0 {
		return c > 0
	}
	if c := compareSQLiteValues(r.timestamp, o.timestamp); c != 0 {
		return c > 0
	}
	return r.cid > o.cid
}

// sourceBatchDuplicateRank picks each index key's newest tag row in the order
// ROW_NUMBER() OVER (PARTITION BY <index key> ORDER BY created_at DESC,
// timestamp DESC, cid DESC) gives it, one row at a time. The key's winner is
// that row's RECORD: every other CID of the key loses, with all of its tag
// rows, and no row of the winning CID ever does.
//
// A RECORD IS NEVER ITS OWN DUPLICATE. The tag key includes the producer and
// the content key, so one CID can carry two tags of one batch (two producers
// of it, a second content key). The window ranked those rows like any other
// two rows and staged every rn > 1 CID, so the CID of the second row was
// "evicted": a record with no duplicate at all was deleted, and a key's
// newest record holding two tags was deleted along with the rows it beat
// (TestReconcileDuplicatesNeverEvictsARecordForItsOwnSecondTag; the rework in
// sdn b75d1e89b kept the window's behaviour exactly, defect included).
type sourceBatchDuplicateRank struct {
	keys map[string]*sourceBatchDuplicateKey
	// rows counts each CID's tag rows in the batch. A CID has one index row
	// (primary key schema_name, cid), so all of its rows share one key.
	rows    map[string]int64
	losers  []string
	matched int64
}

// sourceBatchDuplicateKey is one index key of the batch: its newest row so
// far and every distinct CID ranked under it, the newest's included.
type sourceBatchDuplicateKey struct {
	newest sourceBatchDuplicateRow
	cids   []string
}

func newSourceBatchDuplicateRank() *sourceBatchDuplicateRank {
	return &sourceBatchDuplicateRank{keys: map[string]*sourceBatchDuplicateKey{}, rows: map[string]int64{}}
}

func (r *sourceBatchDuplicateRank) add(key string, row sourceBatchDuplicateRow) {
	seen := r.rows[row.cid]
	r.rows[row.cid] = seen + 1
	k, ok := r.keys[key]
	if !ok {
		r.keys[key] = &sourceBatchDuplicateKey{newest: row, cids: []string{row.cid}}
		return
	}
	if seen == 0 {
		k.cids = append(k.cids, row.cid)
	}
	if row.newerThan(k.newest) {
		k.newest = row
	}
}

// finish settles every key once the whole batch is ranked: each CID of a key
// other than its newest row's loses, and matched counts the losing CIDs' tag
// rows.
func (r *sourceBatchDuplicateRank) finish() {
	r.losers, r.matched = r.losers[:0], 0
	for _, k := range r.keys {
		for _, cid := range k.cids {
			if cid == k.newest.cid {
				continue
			}
			r.losers = append(r.losers, cid)
			r.matched += r.rows[cid]
		}
	}
}

// loserCIDs is the distinct loser CIDs in CID order, so a chunk's seeks walk
// the tag, index and producer-table keys in order.
func (r *sourceBatchDuplicateRank) loserCIDs() []string {
	out := append([]string(nil), r.losers...)
	sort.Strings(out)
	n := 0
	for i, cid := range out {
		if i > 0 && cid == out[n-1] {
			continue
		}
		out[n] = cid
		n++
	}
	return out[:n]
}

// sqliteStorageClass orders storage classes the way SQLite compares values of
// different classes: NULL < INTEGER/REAL < TEXT < BLOB.
func sqliteStorageClass(v any) int {
	switch v.(type) {
	case nil:
		return 0
	case int64, float64, int, int32, bool:
		return 1
	case string:
		return 2
	default:
		return 3
	}
}

func sqliteNumeric(v any) (int64, float64, bool) {
	switch n := v.(type) {
	case int64:
		return n, 0, true
	case int:
		return int64(n), 0, true
	case int32:
		return int64(n), 0, true
	case bool:
		if n {
			return 1, 0, true
		}
		return 0, 0, true
	case float64:
		return 0, n, false
	}
	return 0, 0, true
}

// compareIntFloat compares an INTEGER with a REAL exactly, as SQLite does.
func compareIntFloat(i int64, f float64) int {
	switch {
	case math.IsNaN(f):
		return 1
	case f < -9.223372036854775e18:
		return 1
	case f >= 9.223372036854775e18:
		return -1
	}
	whole := int64(f)
	switch {
	case i < whole:
		return -1
	case i > whole:
		return 1
	}
	switch frac := f - float64(whole); {
	case frac > 0:
		return -1
	case frac < 0:
		return 1
	}
	return 0
}

// compareSQLiteValues compares two result values the way SQLite's ORDER BY
// does with the BINARY collation.
func compareSQLiteValues(a, b any) int {
	ca, cb := sqliteStorageClass(a), sqliteStorageClass(b)
	if ca != cb {
		if ca < cb {
			return -1
		}
		return 1
	}
	switch ca {
	case 0:
		return 0
	case 1:
		ai, af, aInt := sqliteNumeric(a)
		bi, bf, bInt := sqliteNumeric(b)
		switch {
		case aInt && bInt:
			switch {
			case ai < bi:
				return -1
			case ai > bi:
				return 1
			}
			return 0
		case aInt:
			return compareIntFloat(ai, bf)
		case bInt:
			return -compareIntFloat(bi, af)
		}
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		}
		return 0
	case 2:
		return strings.Compare(a.(string), b.(string))
	}
	return bytes.Compare(sqliteBlob(a), sqliteBlob(b))
}

func sqliteBlob(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	return []byte(fmt.Sprint(v))
}

// duplicateKeyPart appends one index-key value to key so that two values are
// written alike exactly when SQLite's partition comparison finds them equal:
// an integral REAL equals the INTEGER of the same value, and no value of one
// storage class equals a value of another.
func duplicateKeyPart(key []byte, v any) []byte {
	var part string
	switch sqliteStorageClass(v) {
	case 0:
		part = "n"
	case 1:
		i, f, isInt := sqliteNumeric(v)
		if !isInt && f == math.Trunc(f) && f >= -9.223372036854775e18 && f < 9.223372036854775e18 {
			i, isInt = int64(f), true
		}
		if isInt {
			part = "i" + strconv.FormatInt(i, 10)
		} else {
			part = "r" + strconv.FormatFloat(f, 'g', -1, 64)
		}
	case 2:
		part = "t" + v.(string)
	default:
		part = "b" + string(sqliteBlob(v))
	}
	key = strconv.AppendInt(key, int64(len(part)), 10)
	key = append(key, ':')
	return append(key, part...)
}

// rankSourceBatchDuplicates ranks the batch in slices of duplicateRankSlice
// tags, one read hold each. A slice is a CLOSED cid range of the batch's tag
// index, so every tag row of a CID lands in one slice.
func (s *FlatSQLStore) rankSourceBatchDuplicates(scope SourceBatchDuplicateReconcileResult, noteHold func(time.Duration)) (*sourceBatchDuplicateRank, error) {
	rank := newSourceBatchDuplicateRank()
	lane := []any{scope.SchemaName, scope.ProviderID, scope.SourceName, scope.BatchID}
	boundarySQL := fmt.Sprintf(`
		SELECT cid FROM sdn_record_source_tags INDEXED BY idx_sdn_record_source_tags_batch_cid
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ? AND cid > ?
		ORDER BY cid
		LIMIT 1 OFFSET %d`, duplicateRankSlice-1)
	after := ""
	for {
		bound := ""
		var rows []sourceBatchDuplicateRankRow
		release := s.lockRead(duplicateReconcileSite + ": rank")
		started := time.Now()
		err := func() error {
			if err := s.closedErr(); err != nil {
				return err
			}
			if err := s.db.QueryRow(boundarySQL, append(lane, after)...).Scan(&bound); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("slice source batch duplicates: %w", err)
			}
			var err error
			rows, err = s.readSourceBatchDuplicateSlice(scope, after, bound)
			return err
		}()
		noteHold(time.Since(started))
		release()
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			rank.add(row.key, row.row)
		}
		if bound == "" {
			rank.finish()
			return rank, nil
		}
		if bound <= after {
			return nil, fmt.Errorf("rank source batch duplicates of %s/%s/%s/%s: slice cursor did not advance past %q",
				scope.SchemaName, scope.ProviderID, scope.SourceName, scope.BatchID, after)
		}
		after = bound
	}
}

// sourceBatchDuplicateRankRow is one row of a rank slice.
type sourceBatchDuplicateRankRow struct {
	key string
	row sourceBatchDuplicateRow
}

// readSourceBatchDuplicateSlice reads the batch's tag rows with cid in
// (after, bound] (bound "" = no upper bound) that the reconcile ranks: rows
// whose record is held by a producer table and whose index key is not empty.
// Each tag's record is read by CID from the producer tables that hold it;
// joining the union read source materialised every record of the standard on
// each call (plan guard). Caller holds s.mu.
func (s *FlatSQLStore) readSourceBatchDuplicateSlice(scope SourceBatchDuplicateReconcileResult, after, bound string) ([]sourceBatchDuplicateRankRow, error) {
	tables, err := s.recordTablesForSchema(scope.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("record tables: %w", err)
	}
	args := []any{scope.SchemaName, scope.ProviderID, scope.SourceName, scope.BatchID, after}
	upper := ""
	if bound != "" {
		upper = " AND tags.cid <= ?"
		args = append(args, bound)
	}
	query := fmt.Sprintf(`
		SELECT tags.cid, tags.created_at, %s,
		       COALESCE(idx.norad_cat_id, -1),
		       COALESCE(idx.entity_id, ''),
		       COALESCE(idx.object_type, ''),
		       COALESCE(idx.ops_status_code, ''),
		       COALESCE(idx.epoch_unix, -1),
		       COALESCE(idx.epoch_day, '')
		FROM sdn_record_source_tags tags INDEXED BY idx_sdn_record_source_tags_batch_cid
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = tags.schema_name AND idx.cid = tags.cid%s
		WHERE tags.schema_name = ?
		  AND tags.provider_id = ?
		  AND tags.source_name = ?
		  AND tags.batch_id = ?
		  AND tags.cid > ?%s
		  AND %s
		  -- AN EMPTY INDEX IS NOT AN IDENTITY.
		  --
		  -- The partition key is the SATELLITE index. A standard that
		  -- populates none of it — $TBS cell sites, $IRM marks, every
		  -- non-satellite record type — would land every row in the single
		  -- partition (-1, '', '', '', -1, '') and all but one of them would
		  -- be a duplicate. The duplicates mode is the DEFAULT, so a batch of
		  -- six distinct cell towers reconciled down to ONE and reported
		  -- success (graph: sdn-cellular-ingest-lands-no-batch, measured: 6
		  -- sites in, 1 stored). Records that share an ACTUAL key still
		  -- collapse; a row with no key at all is no longer anyone's
		  -- duplicate.
		  AND (
		    COALESCE(idx.norad_cat_id, -1) <> -1
		    OR COALESCE(idx.entity_id, '') <> ''
		    OR COALESCE(idx.object_type, '') <> ''
		    OR COALESCE(idx.ops_status_code, '') <> ''
		    OR COALESCE(idx.epoch_unix, -1) <> -1
		    OR COALESCE(idx.epoch_day, '') <> ''
		  )
	`, recordColumnSQL(tables, "timestamp"), recordColumnJoinsSQL(tables, "tags.cid"), upper, recordJoinedHeldSQL(tables))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("rank source batch duplicates: %w", err)
	}
	defer rows.Close()
	var out []sourceBatchDuplicateRankRow
	vals := make([]any, 9)
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	key := make([]byte, 0, 96)
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan source batch duplicate rank: %w", err)
		}
		cid, ok := vals[0].(string)
		if !ok {
			return nil, fmt.Errorf("scan source batch duplicate rank: cid is %T", vals[0])
		}
		key = key[:0]
		for _, v := range vals[3:] {
			key = duplicateKeyPart(key, v)
		}
		out = append(out, sourceBatchDuplicateRankRow{
			key: string(key),
			row: sourceBatchDuplicateRow{cid: cid, createdAt: vals[1], timestamp: vals[2]},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rank source batch duplicates: %w", err)
	}
	return out, nil
}

// evictSourceBatchDuplicates removes the batch's tags of cids, then the
// records no tag of the schema names any more, under one write hold and one
// transaction, and tombstones those records' engine rows before it releases
// the lock.
func (s *FlatSQLStore) evictSourceBatchDuplicates(scope SourceBatchDuplicateReconcileResult, tableName string, cids []string) (deleted int64, held time.Duration, err error) {
	held, err = s.holdWrite(duplicateReconcileSite, func() error {
		if err := s.closedErr(); err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin source batch duplicate reconciliation: %w", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		for _, stmt := range []string{
			`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_duplicate_cids (cid TEXT PRIMARY KEY)`,
			`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_duplicate_orphans (cid TEXT PRIMARY KEY)`,
			`DELETE FROM temp_sdn_reconcile_duplicate_cids`,
			`DELETE FROM temp_sdn_reconcile_duplicate_orphans`,
		} {
			if _, err := tx.Exec(flatsqldrv.WithoutJournal(stmt)); err != nil {
				return fmt.Errorf("prepare duplicate reconcile tables: %w", err)
			}
		}
		for start := 0; start < len(cids); start += duplicateStageValues {
			end := start + duplicateStageValues
			if end > len(cids) {
				end = len(cids)
			}
			args := make([]any, 0, end-start)
			for _, cid := range cids[start:end] {
				args = append(args, cid)
			}
			if _, err := tx.Exec(flatsqldrv.WithoutJournal(`INSERT OR IGNORE INTO temp_sdn_reconcile_duplicate_cids (cid) VALUES `+
				strings.TrimSuffix(strings.Repeat("(?), ", len(args)), ", ")), args...); err != nil {
				return fmt.Errorf("stage source batch duplicate cids: %w", err)
			}
		}
		if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
			DELETE FROM sdn_record_source_tags
			WHERE schema_name = ?
			  AND provider_id = ?
			  AND source_name = ?
			  AND batch_id = ?
			  AND cid IN (SELECT cid FROM temp_sdn_reconcile_duplicate_cids)
		`), scope.SchemaName, scope.ProviderID, scope.SourceName, scope.BatchID); err != nil {
			return fmt.Errorf("delete source batch duplicate tags: %w", err)
		}
		// Deleted counts LOGICAL records (per cid), independent of table layout.
		if deleted, err = s.deleteStagedOrphansTx(tx, scope.SchemaName, tableName,
			"temp_sdn_reconcile_duplicate_cids", "temp_sdn_reconcile_duplicate_orphans"); err != nil {
			return fmt.Errorf("duplicate records: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit source batch duplicate reconciliation: %w", err)
		}
		committed = true
		if deleted > 0 {
			if _, err := s.tombstoneStagedEngineRowsLocked(scope.SchemaName, "temp_sdn_reconcile_duplicate_orphans"); err != nil {
				return err
			}
		}
		return nil
	})
	return deleted, held, err
}
