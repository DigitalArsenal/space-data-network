package sourcemetrics

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openLedger(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func sourceByName(t *testing.T, store *Store, name string) Source {
	t.Helper()
	rows, err := store.Sources()
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	for _, row := range rows {
		if row.SourceName == name {
			return row
		}
	}
	t.Fatalf("no ledger row for %q; rows=%+v", name, rows)
	return Source{}
}

func TestSourceIDIsTheProvenancePair(t *testing.T) {
	if got := SourceID("space-data-network-02", "celestrak-gp"); got != "space-data-network-02/celestrak-gp" {
		t.Fatalf("SourceID = %q", got)
	}
	// Degenerate inputs still produce SOMETHING — a malformed ingest must be
	// visible in the ledger, not silently dropped.
	if got := SourceID("", "celestrak-gp"); got != "celestrak-gp" {
		t.Fatalf("SourceID with no provider = %q", got)
	}
	if got := SourceID("  ", "  "); got != "" {
		t.Fatalf("SourceID of blanks = %q, want empty", got)
	}
}

// The ledger lives in its OWN file beside the record store — never inside it.
func TestLedgerIsItsOwnDatabaseFile(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(dir, DBFileName)); err != nil {
		t.Fatalf("expected %s beside the record store: %v", DBFileName, err)
	}
	if DBFileName == "sdn.db" {
		t.Fatal("the operational ledger must never share the record store's file")
	}
}

// last_pull_size_bytes is the RETRIEVED payload. When the flow archives the
// raw bytes, that is the number; otherwise the ledger falls back to what the
// egress connector actually read for the same URL.
func TestPullSizePrefersRawPayloadThenFetchedBytes(t *testing.T) {
	store := openLedger(t)
	const url = "https://celestrak.org/pub/satcat.csv"

	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 900, DurationMs: 12})
	store.RecordIngest(Ingest{
		AppID: "app", ProviderID: "p", SourceName: "with-raw", SourceURL: url,
		Schema: "CAT.fbs", BatchID: "b1", PullBytes: 1234, Records: 2, Inserted: 2,
	})
	if got := sourceByName(t, store, "with-raw").LastPullSizeBytes; got != 1234 {
		t.Fatalf("with raw archive: last_pull_size_bytes = %d, want 1234", got)
	}

	store.RecordIngest(Ingest{
		AppID: "app", ProviderID: "p", SourceName: "no-raw", SourceURL: url,
		Schema: "CAT.fbs", BatchID: "b1", Records: 2, Inserted: 2,
	})
	if got := sourceByName(t, store, "no-raw").LastPullSizeBytes; got != 900 {
		t.Fatalf("without raw archive: last_pull_size_bytes = %d, want the 900 fetched bytes", got)
	}
}

// One payload can land as several schemas under ONE batch id. That is one
// PULL, not a republication — mistaking it for one would make every GP cycle
// look like the publisher served stale data.
func TestRepeatedMeansTheSourceRepublishedNothing(t *testing.T) {
	store := openLedger(t)
	const url = "https://celestrak.org/NORAD/elements/gp.php"

	// Pull 1: one fetch, two schemas.
	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 500})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "gp", SourceURL: url, Schema: "OMM.fbs", BatchID: "hash-a"})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "gp", SourceURL: url, Schema: "MPE.fbs", BatchID: "hash-a"})
	if row := sourceByName(t, store, "gp"); row.LastBatchRepeated {
		t.Fatal("a second schema from the SAME payload was reported as a repeat")
	}

	// Pull 2: new fetch, identical content.
	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 500})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "gp", SourceURL: url, Schema: "OMM.fbs", BatchID: "hash-a"})
	if row := sourceByName(t, store, "gp"); !row.LastBatchRepeated {
		t.Fatal("a new pull of identical content was not reported as a repeat")
	}

	// Pull 3: new fetch, new content.
	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 600})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "gp", SourceURL: url, Schema: "OMM.fbs", BatchID: "hash-b"})
	row := sourceByName(t, store, "gp")
	if row.LastBatchRepeated {
		t.Fatal("fresh content was reported as a repeat")
	}
	if row.LastBatchID != "hash-b" {
		t.Fatalf("last_batch_id = %q, want hash-b", row.LastBatchID)
	}
	if row.FetchCount < 2 {
		t.Fatalf("fetch_count = %d after three pulls, want >= 2", row.FetchCount)
	}
}

// One retrieved payload commonly lands as SEVERAL schemas. The pull totals
// must sum them; reporting only the final ingest call made a 500-record GP
// pull read as a 2-record pull on the $APPS board.
func TestPullTotalsSumEverySchemaFromOnePayload(t *testing.T) {
	store := openLedger(t)
	const url = "https://celestrak.org/NORAD/elements/gp.php"

	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 80007})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "gp", SourceURL: url,
		Schema: "OMM.fbs", BatchID: "hash-a", Records: 500, Inserted: 500})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "gp", SourceURL: url,
		Schema: "MPE.fbs", BatchID: "hash-a", Records: 500, Inserted: 2})

	row := sourceByName(t, store, "gp")
	if got := row.LastSchemas; len(got) != 2 || got[0] != "OMM.fbs" || got[1] != "MPE.fbs" {
		t.Fatalf("last_schemas = %v, want [OMM.fbs MPE.fbs] in arrival order", got)
	}
	if row.LastRecords != 1000 || row.LastInserted != 502 {
		t.Fatalf("pull totals = %d records / %d inserted, want 1000 / 502", row.LastRecords, row.LastInserted)
	}
	if row.LastPullSizeBytes != 80007 {
		t.Fatalf("last_pull_size_bytes = %d, want the single fetched payload size", row.LastPullSizeBytes)
	}

	// The NEXT pull starts the totals over rather than accumulating forever.
	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 80007})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "gp", SourceURL: url,
		Schema: "OMM.fbs", BatchID: "hash-b", Records: 3, Inserted: 3})
	row = sourceByName(t, store, "gp")
	if row.LastRecords != 3 || row.LastInserted != 3 {
		t.Fatalf("second pull totals = %d/%d, want 3/3 (not accumulated across pulls)", row.LastRecords, row.LastInserted)
	}
	if len(row.LastSchemas) != 1 || row.LastSchemas[0] != "OMM.fbs" {
		t.Fatalf("second pull schemas = %v, want [OMM.fbs]", row.LastSchemas)
	}
}

// A fetch that fails still updates the row: an app that keeps trying and keeps
// failing must look different from an app that never ran.
func TestFailedFetchIsRecordedAgainstTheSource(t *testing.T) {
	store := openLedger(t)
	const url = "https://celestrak.org/pub/satcat.txt"

	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 42})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "satcat", SourceURL: url, Schema: "CAT.fbs", BatchID: "b"})
	first := sourceByName(t, store, "satcat")

	time.Sleep(1100 * time.Millisecond) // ledger timestamps are second-resolution
	store.RecordFetch(Fetch{URL: url, Status: 503, Err: "service unavailable"})

	row := sourceByName(t, store, "satcat")
	if row.LastStatus != 503 || row.LastError == "" {
		t.Fatalf("failed fetch not attributed: status=%d err=%q", row.LastStatus, row.LastError)
	}
	if row.LastRetrievedAt == nil || first.LastRetrievedAt == nil ||
		!row.LastRetrievedAt.After(*first.LastRetrievedAt) {
		t.Fatalf("last_retrieved_at did not advance on a new attempt: %v -> %v", first.LastRetrievedAt, row.LastRetrievedAt)
	}
	// The last SUCCESSFUL batch is untouched by a failed attempt.
	if row.LastBatchID != "b" {
		t.Fatalf("failed fetch clobbered last_batch_id: %q", row.LastBatchID)
	}
}

// The ledger survives a restart: it is the durable answer to "when did this
// node last pull, and what did it publish".
func TestLedgerPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.RecordIngest(Ingest{
		AppID: "app", ProviderID: "p", SourceName: "gp",
		SourceURL: "https://celestrak.org/x", Schema: "OMM.fbs", BatchID: "b", PullBytes: 77,
	})
	store.RecordPNM("p", "gp", PNM{ID: "b", CID: "bafycid", Schema: "OMM.fbs", RecordCount: 5,
		PublishedAt: time.Unix(1785000000, 0).UTC()})
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	row := sourceByName(t, reopened, "gp")
	if row.LastPullSizeBytes != 77 {
		t.Fatalf("last_pull_size_bytes = %d after reopen, want 77", row.LastPullSizeBytes)
	}
	if row.LastPNM == nil || row.LastPNM.CID != "bafycid" || row.LastPNM.RecordCount != 5 {
		t.Fatalf("last PNM lost across reopen: %+v", row.LastPNM)
	}
	if row.DebounceHours != DefaultDebounceHours {
		t.Fatalf("debounce_hours = %v after reopen", row.DebounceHours)
	}
}

// A nil store is a no-op everywhere: bookkeeping must never be able to take
// down a retrieval.
func TestNilStoreIsInert(t *testing.T) {
	var store *Store
	store.RecordFetch(Fetch{URL: "https://celestrak.org/x", Status: 200})
	store.RecordIngest(Ingest{ProviderID: "p", SourceName: "s"})
	store.RecordPNM("p", "s", PNM{CID: "c"})
	rows, err := store.Sources()
	if err != nil || len(rows) != 0 {
		t.Fatalf("nil store: rows=%v err=%v", rows, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

// A flow whose pulls keep FAILING writes no source row, so a
// success-only debounce left it permanently "due" and it retried on every
// restart. Observed live: a CelesTrak endpoint that had already returned 403
// was hit again on boot. What a publisher is owed is a limit on ATTEMPTS.
func TestAttemptLogBoundsRetriesThatNeverSucceed(t *testing.T) {
	store := openLedger(t)
	const appID = "com.digitalarsenal.flows.celestrak-gp-ingest"

	if last := store.LastAttempt(appID); last != nil {
		t.Fatalf("a never-run app reported an attempt at %v", last)
	}

	store.RecordAttempt(appID)
	first := store.LastAttempt(appID)
	if first == nil {
		t.Fatal("attempt was not recorded")
	}
	if time.Since(*first) > time.Minute {
		t.Fatalf("recorded attempt timestamp is wrong: %v", first)
	}

	// No source row is ever written (the pull kept failing) — the attempt log
	// is the ONLY thing standing between a restart loop and a retry storm.
	rows, err := store.Sources()
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("failing pulls must not write source rows; got %+v", rows)
	}

	store.RecordAttempt(appID)
	if second := store.LastAttempt(appID); second == nil || second.Before(*first) {
		t.Fatalf("second attempt did not advance the marker: %v -> %v", first, second)
	}

	// Survives a restart: otherwise every reboot is a fresh licence to retry.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestAttemptLogIgnoresBlankAppAndNilStore(t *testing.T) {
	store := openLedger(t)
	store.RecordAttempt("   ")
	if last := store.LastAttempt("   "); last != nil {
		t.Fatal("a blank app id must not create an attempt row")
	}
	var nilStore *Store
	nilStore.RecordAttempt("x")
	if last := nilStore.LastAttempt("x"); last != nil {
		t.Fatal("nil store must report no attempt")
	}
}

// A publisher that has started refusing us is asking to be asked LESS often.
// Retrying a refusing endpoint on the same cadence forever is how a node earns
// a longer ban rather than a shorter one — CelesTrak went 403 then 503 while we
// kept knocking at 3h.
func TestBackoffDoublesPerFailureAndCaps(t *testing.T) {
	cases := map[int]float64{
		0:  3,  // healthy
		1:  6,  // one failed attempt
		2:  12, //
		3:  24, // cap
		4:  24, // stays capped
		99: 24, //
	}
	for failures, want := range cases {
		if got := EffectiveDebounceHours(failures); got != want {
			t.Fatalf("EffectiveDebounceHours(%d) = %v, want %v", failures, got, want)
		}
	}
	if MaxDebounceHours <= DefaultDebounceHours {
		t.Fatal("the cap must be wider than the base window")
	}
}

// The streak advances on attempts and resets the moment a batch lands.
func TestFailureStreakAdvancesAndResetsOnSuccess(t *testing.T) {
	store := openLedger(t)
	const appID = "com.digitalarsenal.flows.celestrak-gp-ingest"

	if _, failures := store.AttemptState(appID); failures != 0 {
		t.Fatalf("fresh app has %d failures, want 0", failures)
	}

	store.RecordAttempt(appID)
	if _, f := store.AttemptState(appID); f != 1 {
		t.Fatalf("after one attempt failures = %d, want 1 (failure assumed until proven)", f)
	}
	if got := EffectiveDebounceHours(1); got != 6 {
		t.Fatalf("window after 1 failure = %vh, want 6h", got)
	}

	store.RecordAttempt(appID)
	store.RecordAttempt(appID)
	if _, f := store.AttemptState(appID); f != 3 {
		t.Fatalf("failures = %d, want 3", f)
	}

	// A batch lands: back to the normal cadence at once, not after serving out
	// a punishment window.
	store.RecordIngest(Ingest{
		AppID: appID, ProviderID: "p", SourceName: "celestrak-gp",
		SourceURL: "https://celestrak.org/x", Schema: "OMM.fbs", BatchID: "b", Records: 10, Inserted: 10,
	})
	if _, f := store.AttemptState(appID); f != 0 {
		t.Fatalf("failures after a successful ingest = %d, want 0", f)
	}
	if got := EffectiveDebounceHours(0); got != DefaultDebounceHours {
		t.Fatalf("recovered window = %vh, want the base %vh", got, DefaultDebounceHours)
	}

	// A DIFFERENT app's success must not clear this one's streak.
	store.RecordAttempt(appID)
	store.RecordIngest(Ingest{
		AppID: "other.app", ProviderID: "p", SourceName: "other",
		SourceURL: "https://example.com/x", Schema: "OMM.fbs", BatchID: "b2",
	})
	if _, f := store.AttemptState(appID); f != 1 {
		t.Fatalf("another app's success cleared this app's streak: failures = %d, want 1", f)
	}
}

// A ledger created by an older build has app_attempts WITHOUT
// consecutive_failures. CREATE TABLE IF NOT EXISTS does not add it, so the
// escalating backoff shipped inert on every existing host: AttemptState's
// SELECT failed, the caller read "never attempted", and the debounce it was
// meant to widen was skipped entirely.
func TestOpenAddsBackoffColumnToAnOlderLedger(t *testing.T) {
	dir := t.TempDir()

	// Build the OLD schema by hand, exactly as the previous build left it.
	old, err := sql.Open("sqlite", filepath.Join(dir, DBFileName))
	if err != nil {
		t.Fatalf("open legacy ledger: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE app_attempts (
		app_id          TEXT PRIMARY KEY,
		last_attempt_at INTEGER NOT NULL DEFAULT 0,
		attempt_count   INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create legacy app_attempts: %v", err)
	}
	if _, err := old.Exec(
		`INSERT INTO app_attempts (app_id, last_attempt_at, attempt_count) VALUES (?, ?, ?)`,
		"com.digitalarsenal.flows.celestrak-satcat-ingest", time.Now().Add(-30*time.Minute).Unix(), 4); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close legacy ledger: %v", err)
	}

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open over a legacy ledger: %v", err)
	}
	defer store.Close()

	// The pre-existing attempt must still be readable — a migration that lost
	// the retrieval history would let a moved node re-hammer the publisher.
	last, failures := store.AttemptState("com.digitalarsenal.flows.celestrak-satcat-ingest")
	if last == nil {
		t.Fatal("legacy attempt history lost: the node would treat the source as never fetched and pull immediately")
	}
	if failures != 0 {
		t.Fatalf("failures = %d, want 0 for a migrated row", failures)
	}

	// And the backoff must now actually work on this ledger.
	store.RecordAttempt("com.digitalarsenal.flows.celestrak-satcat-ingest")
	if _, failures = store.AttemptState("com.digitalarsenal.flows.celestrak-satcat-ingest"); failures != 1 {
		t.Fatalf("failures after one attempt = %d, want 1 — the backoff is still inert", failures)
	}
}
