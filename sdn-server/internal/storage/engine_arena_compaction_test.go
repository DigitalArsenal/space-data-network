package storage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// withArenaSizes shrinks the arena budget, the compaction mark and the step
// for one test.
func withArenaSizes(t *testing.T, budget, mark, step int64) {
	t.Helper()
	prevBudget, prevMark, prevStep := engineArenaBudgetBytes, engineArenaCompactBytes, engineArenaCompactStepBytes
	engineArenaBudgetBytes, engineArenaCompactBytes, engineArenaCompactStepBytes = budget, mark, step
	t.Cleanup(func() {
		engineArenaBudgetBytes, engineArenaCompactBytes, engineArenaCompactStepBytes = prevBudget, prevMark, prevStep
	})
}

func waitForArenaCompactor(t *testing.T, s *FlatSQLStore) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for s.arenaCompactor().running.Load() {
		if time.Now().After(deadline) {
			t.Fatal("background arena compaction did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ommPartitionRows is (rowid -> record bytes) of every OMM partition.
func ommPartitionRows(t *testing.T, s *FlatSQLStore, sources []string) map[string]map[int64]string {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]map[int64]string{}
	for _, source := range sources {
		p := enginePartition("OMM", source)
		res, err := s.engineDB.Query(`SELECT _rowid, _data FROM "` + p + `"`)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		rows := map[int64]string{}
		for _, row := range res.Rows {
			seq, _ := row[0].(int64)
			data, _ := row[1].([]byte)
			rows[seq] = string(data)
		}
		out[p] = rows
	}
	return out
}

func ledgerBySource(t *testing.T, s *FlatSQLStore) map[string]int64 {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT source, COUNT(*) FROM sdn_engine_rows WHERE schema_name = 'OMM.fbs' GROUP BY source`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var source string
		var n int64
		if err := rows.Scan(&source, &n); err != nil {
			t.Fatal(err)
		}
		out[source] = n
	}
	return out
}

// The node mirrors every record into its hot window however much it ingests:
// dead rows are compacted away at runtime (past the mark in the background,
// before a mirror that would not fit synchronously), nothing is refused, the
// residency ledger matches the engine partition by partition, and the
// compacted arena opens warm after a restart with every sequence intact.
func TestEngineArenaCompactsAtRuntimeInsteadOfRefusing(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	withArenaSizes(t, 256<<10, 128<<10, 32<<10)
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(60))
	if !store.BootState().Durable {
		store.Close()
		t.Skip("engine has no filesystem on this host")
	}
	sources := []string{"churn-a", "churn-b"}
	for round := 0; round < 12; round++ {
		seedHardeningOMM(t, store, sources[round%2], uint32(100000+round*1000), 400)
	}
	waitForArenaCompactor(t, store)
	if refused := store.engineArenaRefused.Load(); refused != 0 {
		t.Fatalf("%d bytes refused: the arena should have been compacted instead", refused)
	}
	acct, runs := store.LastEngineArenaCompaction()
	if runs == 0 {
		t.Fatal("the arena was never compacted")
	}
	if bytes, budget := store.EngineArenaBytes(); bytes > budget {
		t.Fatalf("arena at %d bytes, past its %d-byte budget", bytes, budget)
	}
	ledger := ledgerBySource(t, store)
	var total int64
	for _, source := range sources {
		got := engineCount(t, store, enginePartition("OMM", source))
		if got != ledger[source] {
			t.Fatalf("%s: engine holds %d rows, the ledger %d", source, got, ledger[source])
		}
		total += got
	}
	if total != 60 {
		t.Fatalf("hot window holds %d rows, want 60", total)
	}
	// Whatever the last writes left dead is compacted now, so the arena
	// holds the live window only: a boot discards an arena that is past the
	// mark and mostly dead (the fallback), and this test is about the warm
	// open of a compacted one.
	if _, err := store.compactEngineArena("test: before close"); err != nil {
		t.Fatal(err)
	}
	before := ommPartitionRows(t, store, sources)
	if err := store.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d compactions; last: %d -> %d bytes, %d kept, %d dropped, %d steps, longest hold %s",
		runs, acct.ArenaBefore, acct.ArenaAfter, acct.Report.KeptRecords, acct.Report.DroppedRecords, acct.Steps, acct.LongestHold)

	reopened := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(60))
	defer reopened.Close()
	if !reopened.BootState().EngineWarm {
		t.Fatalf("the compacted arena did not open warm: %+v", reopened.BootState())
	}
	if _, err := reopened.HydrateEngineHotWindow(); err != nil {
		t.Fatal(err)
	}
	after := ommPartitionRows(t, reopened, sources)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("the reopened partitions differ from the compacted ones (sequences or bytes)")
	}
	ledger = ledgerBySource(t, reopened)
	for _, source := range sources {
		if got := engineCount(t, reopened, enginePartition("OMM", source)); got != ledger[source] {
			t.Fatalf("after reopen %s: engine %d rows, ledger %d", source, got, ledger[source])
		}
	}
}

// Reads never wait on a compaction: between two steps the store lock is free
// and a reader answers at once, from the rows the compaction keeps.
func TestReadersAnswerBetweenArenaCompactionSteps(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	withArenaSizes(t, 64<<20, 64<<10, 8<<10)
	engineArenaRuntimeCompaction = false
	t.Cleanup(func() { engineArenaRuntimeCompaction = true; engineArenaCompactStepHook = nil })
	store := newEngineRecordsStoreWithOptions(t, filepath.Join(t.TempDir(), "store"), WithEngineHotWindow(40))
	defer store.Close()
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	seedHardeningOMM(t, store, "readers", 200000, 1500)
	engineArenaRuntimeCompaction = true
	want := engineCount(t, store, enginePartition("OMM", "readers"))

	paused := make(chan struct{})
	resume := make(chan struct{})
	engineArenaCompactStepHook = func(step int) {
		if step == 3 {
			close(paused)
			<-resume
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.compactEngineArena("test")
		done <- err
	}()
	select {
	case <-paused:
	case err := <-done:
		t.Fatalf("compaction finished before its third step (%v): the test needs more steps", err)
	case <-time.After(60 * time.Second):
		t.Fatal("compaction never reached its third step")
	}
	started := time.Now()
	got := engineCount(t, store, enginePartition("OMM", "readers"))
	stream, err := store.QueryRawStream(`SELECT _data FROM "OMM@readers" ORDER BY _rowid`)
	waited := time.Since(started)
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || len(stream.Bytes) == 0 {
		t.Fatalf("mid-compaction read: %d rows (want %d), %d bytes", got, want, len(stream.Bytes))
	}
	if waited > 2*time.Second {
		t.Fatalf("a reader waited %s between compaction steps", waited)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if after := engineCount(t, store, enginePartition("OMM", "readers")); after != want {
		t.Fatalf("after the compaction: %d rows, want %d", after, want)
	}
	acct, _ := store.LastEngineArenaCompaction()
	t.Logf("reader answered in %s mid-compaction; %d steps, longest hold %s, %d -> %d bytes",
		waited, acct.Steps, acct.LongestHold, acct.ArenaBefore, acct.ArenaAfter)
}

// A tombstone whose ledger delete rolled back hid a row the ledger still
// holds. A warm boot used to heal that pair by resurrecting the row; a
// compaction drops it for good. The compaction's settle step finds the ledger
// row the engine no longer holds and mirrors the record again, so the ledger
// stays exact.
func TestCompactionMirrorsAgainALedgerRowWhoseTombstoneRolledBack(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	engineArenaRuntimeCompaction = false
	t.Cleanup(func() { engineArenaRuntimeCompaction = true })
	store := newEngineRecordsStoreWithOptions(t, filepath.Join(t.TempDir(), "store"), WithEngineHotWindow(30))
	defer store.Close()
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	seedHardeningOMM(t, store, "rollback", 300000, 200)
	engineArenaRuntimeCompaction = true
	var cid string
	var seq int64
	store.mu.Lock()
	err := store.db.QueryRow(`SELECT cid, seq FROM sdn_engine_rows WHERE schema_name = 'OMM.fbs' AND source = 'rollback' ORDER BY seq LIMIT 1`).Scan(&cid, &seq)
	if err == nil {
		err = store.engineDB.MarkDeleted(enginePartition("OMM", "rollback"), uint64(seq))
	}
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got, ledger := engineCount(t, store, "OMM@rollback"), ledgerBySource(t, store)["rollback"]; got != ledger-1 {
		t.Fatalf("setup: engine %d rows, ledger %d", got, ledger)
	}
	acct, err := store.compactEngineArena("test")
	if err != nil {
		t.Fatal(err)
	}
	if acct.LedgerRepaired != 1 {
		t.Fatalf("repaired %d ledger rows, want 1", acct.LedgerRepaired)
	}
	ledger := ledgerBySource(t, store)["rollback"]
	if got := engineCount(t, store, "OMM@rollback"); got != ledger || got != 30 {
		t.Fatalf("after the compaction: engine %d rows, ledger %d, want 30", got, ledger)
	}
	var newSeq int64
	store.mu.RLock()
	err = store.db.QueryRow(`SELECT seq FROM sdn_engine_rows WHERE schema_name = 'OMM.fbs' AND cid = ?`, cid).Scan(&newSeq)
	store.mu.RUnlock()
	if err != nil || newSeq == seq {
		t.Fatalf("record %s: ledger seq %d (was %d), %v", cid, newSeq, seq, err)
	}
}

// The host-02 shape, measured on a copy of a real host-02-sized format-1
// store (SDN_ARENA_FIXTURE_DIR: a directory holding control.flatsqldb and its
// .fsdata; the test works on an APFS/reflink clone and never writes the
// original). The boot's discard is turned off so the warm open keeps the
// mostly-dead arena; the hydration re-applies the tombstones the engine
// forgot, and the runtime compaction shrinks it. Reports the store-lock holds,
// the arena and the engine's linear memory before and after.
func TestArenaCompactionOnHost02Fixture(t *testing.T) {
	src := os.Getenv("SDN_ARENA_FIXTURE_DIR")
	if src == "" {
		t.Skip("SDN_ARENA_FIXTURE_DIR not set")
	}
	t.Setenv(checkpointIntervalEnv, "0")
	engineBootDiscardsMostlyDeadArena = false
	t.Cleanup(func() { engineBootDiscardsMostlyDeadArena = true })
	basePath := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{flatSQLControlDBName, flatSQLControlDBName + ".fsdata", "auxiliary.flatsqlmeta"} {
		if out, err := exec.Command("cp", "-c", filepath.Join(src, name), filepath.Join(basePath, name)).CombinedOutput(); err != nil {
			t.Fatalf("clone %s: %v: %s", name, err, out)
		}
	}
	opened := time.Now()
	store := newEngineRecordsStoreWithOptions(t, basePath)
	defer store.Close()
	t.Logf("open: %s, boot %+v", time.Since(opened).Round(time.Millisecond), store.BootState())
	hydrated := time.Now()
	if _, err := store.HydrateEngineHotWindow(); err != nil {
		t.Fatal(err)
	}
	t.Logf("hydration (re-applies the forgotten tombstones): %s", time.Since(hydrated).Round(time.Millisecond))
	waitForArenaCompactor(t, store)
	history := store.EngineArenaCompactions()
	if len(history) == 0 {
		t.Fatal("the fixture's arena was not compacted")
	}
	shrunk := false
	for i, acct := range history {
		t.Logf("compaction %d (%s; synchronous %v, deferred %v): %d steps in %s, longest store-lock hold %s; arena %d MiB -> %d MiB (capacity %d MiB -> %d MiB); rows kept %d, dropped %d, runs %d; engine linear memory %d MiB before, %d MiB peak, %d MiB after; ledger rows repaired %d",
			i+1, acct.Reason, acct.Synchronous, acct.Deferred, acct.Steps, acct.Total.Round(time.Millisecond), acct.LongestHold.Round(time.Millisecond),
			acct.ArenaBefore>>20, acct.ArenaAfter>>20, acct.CapacityBefore>>20, acct.CapacityAfter>>20,
			acct.Report.KeptRecords, acct.Report.DroppedRecords, acct.Report.SequenceRuns,
			acct.MemoryBefore>>20, acct.MemoryDuring>>20, acct.MemoryAfter>>20, acct.LedgerRepaired)
		if acct.ArenaBefore > 0 && acct.ArenaAfter < acct.ArenaBefore/2 {
			shrunk = true
		}
	}
	if !shrunk {
		t.Fatal("no compaction shrank the mostly-dead arena by more than half")
	}
	// The ledger is exact, partition by partition.
	store.mu.RLock()
	rows, err := store.db.Query(`SELECT schema_name, source, COUNT(*) FROM sdn_engine_rows GROUP BY schema_name, source`)
	store.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ schema, source string }
	ledger := map[key]int64{}
	for rows.Next() {
		var k key
		var n int64
		if err := rows.Scan(&k.schema, &k.source, &n); err != nil {
			t.Fatal(err)
		}
		ledger[k] = n
	}
	rows.Close()
	for k, n := range ledger {
		binding, ok := store.engineRoutedSchemaFor(k.schema)
		if !ok {
			continue
		}
		if got := engineCount(t, store, enginePartition(binding.Table, k.source)); got != n {
			t.Fatalf("%s@%s: engine %d rows, ledger %d", binding.Table, k.source, got, n)
		}
	}
	if err := store.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	size := engineStreamSize(filepath.Join(basePath, flatSQLControlDBName))
	t.Logf("record stream on disk after the compaction and a checkpoint: %d MiB; ledger partitions %d", size>>20, len(ledger))
}

// fixtureAnswers opens a clone of a store directory with the given engine
// bytes (nil: the embedded one) and returns a digest of what it answers: every
// engine partition's rows (rowid and record bytes), the unified views' counts,
// and the control tables' shape.
func fixtureAnswers(t *testing.T, src string, engine []byte) (map[string]string, time.Duration) {
	t.Helper()
	basePath := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{flatSQLControlDBName, flatSQLControlDBName + ".fsdata", "auxiliary.flatsqlmeta"} {
		if out, err := exec.Command("cp", "-c", filepath.Join(src, name), filepath.Join(basePath, name)).CombinedOutput(); err != nil {
			t.Fatalf("clone %s: %v: %s", name, err, out)
		}
	}
	engineWasmOverride = engine
	defer func() { engineWasmOverride = nil }()
	started := time.Now()
	rt, db, _, _, err := openControlEngine(basePath, filepath.Join(basePath, flatSQLControlDBName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	opened := time.Since(started)
	defer rt.Close()
	defer db.Destroy()
	query := func(q string, args ...any) *flatsqlrt.Result {
		res, err := db.Query(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return res
	}
	out := map[string]string{}
	res := query(`SELECT name FROM sqlite_master WHERE type = 'table' AND sql LIKE 'CREATE VIRTUAL TABLE%' AND name LIKE '%@%' ORDER BY name`)
	for _, row := range res.Rows {
		p, _ := row[0].(string)
		bounds := query(`SELECT COUNT(*), COALESCE(MIN(_rowid), 0), COALESCE(MAX(_rowid), 0) FROM "` + p + `"`)
		n, _ := bounds.Rows[0][0].(int64)
		if n == 0 {
			continue
		}
		lo, _ := bounds.Rows[0][1].(int64)
		hi, _ := bounds.Rows[0][2].(int64)
		h := sha256.New()
		for from := lo; from <= hi; from += 20000 {
			page := query(`SELECT _rowid, _data FROM "`+p+`" WHERE _rowid BETWEEN ? AND ? ORDER BY _rowid`, from, from+19999)
			for _, r := range page.Rows {
				seq, _ := r[0].(int64)
				data, _ := r[1].([]byte)
				fmt.Fprintf(h, "%d:%d:", seq, len(data))
				h.Write(data)
			}
		}
		out["partition "+p] = fmt.Sprintf("%d rows, sha256 %x", n, h.Sum(nil))
	}
	for _, q := range []string{
		`SELECT schema_name, COUNT(*) FROM sdn_record_index GROUP BY schema_name ORDER BY schema_name`,
		`SELECT schema_name, source, COUNT(*), MIN(seq), MAX(seq) FROM sdn_engine_rows GROUP BY schema_name, source ORDER BY schema_name, source`,
		`SELECT name, ord FROM _flatsql_sources ORDER BY name`,
		`SELECT "start", "stop", source FROM _flatsql_source_ranges ORDER BY CAST("start" AS INTEGER)`,
		`SELECT _source, COUNT(*) FROM OMM GROUP BY _source ORDER BY _source`,
		`SELECT _source, COUNT(*) FROM IQC GROUP BY _source ORDER BY _source`,
		`SELECT NORAD_CAT_ID, OBJECT_NAME, EPOCH, MEAN_MOTION FROM "OMM@celestrak-gp" ORDER BY _rowid LIMIT 3000`,
	} {
		r, err := db.Query(q)
		if err != nil {
			out[q] = "error: " + err.Error()
			continue
		}
		out[q] = fmt.Sprint(r.Rows)
	}
	return out, opened
}

// The engine upgrade answers exactly what the engine it replaces answers, on
// a copy of a real host-02-sized store: same partitions, same rows (rowids and
// bytes), same control tables. SDN_ARENA_FIXTURE_DIR as above;
// SDN_PREVIOUS_ENGINE_WASM names the engine being replaced.
func TestEngineUpgradeAnswersIdenticallyOnHost02Fixture(t *testing.T) {
	src := os.Getenv("SDN_ARENA_FIXTURE_DIR")
	prevPath := os.Getenv("SDN_PREVIOUS_ENGINE_WASM")
	if src == "" || prevPath == "" {
		t.Skip("SDN_ARENA_FIXTURE_DIR and SDN_PREVIOUS_ENGINE_WASM not set")
	}
	previous, err := os.ReadFile(prevPath)
	if err != nil {
		t.Fatal(err)
	}
	engineBootDiscardsMostlyDeadArena = false
	t.Cleanup(func() { engineBootDiscardsMostlyDeadArena = true })
	before, openedBefore := fixtureAnswers(t, src, previous)
	after, openedAfter := fixtureAnswers(t, src, nil)
	if len(before) != len(after) {
		t.Fatalf("%d answers on the previous engine, %d on this one", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s differs:\n  previous: %.300s\n  this:     %.300s", k, v, after[k])
		}
	}
	partitions := 0
	for k, v := range after {
		if strings.HasPrefix(k, "partition ") {
			partitions++
			t.Logf("%s: %s", k, v)
		}
	}
	t.Logf("%d partitions and %d control queries identical; open took %s on the previous engine, %s on this one",
		partitions, len(after)-partitions, openedBefore.Round(time.Millisecond), openedAfter.Round(time.Millisecond))
}

// The checkpoint loop polls for a compaction left between steps every 30 s.
// When none is, the poll takes no lock: it answers at once while a writer
// holds the store lock (a store-lock read there queued behind writers for up
// to 2.9 s per poll on host-02).
func TestArenaCompactionPollTakesNoLockWhenNothingIsPending(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	store := newEngineRecordsStoreWithOptions(t, filepath.Join(t.TempDir(), "store"), WithEngineHotWindow(40))
	defer store.Close()
	if !store.BootState().Durable {
		t.Skip("engine has no filesystem on this host")
	}
	seedHardeningOMM(t, store, "poll", 400000, 200)
	store.mu.Lock()
	returned := make(chan time.Duration, 1)
	go func() {
		started := time.Now()
		store.persistPendingArenaCompaction("test")
		returned <- time.Since(started)
	}()
	select {
	case took := <-returned:
		store.mu.Unlock()
		t.Logf("poll answered in %s with the store lock held by a writer", took)
	case <-time.After(5 * time.Second):
		store.mu.Unlock()
		<-returned
		t.Fatal("the pending-compaction poll waited for the store lock")
	}
}
