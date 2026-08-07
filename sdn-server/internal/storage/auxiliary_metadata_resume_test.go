package storage

// auxiliary_metadata_resume_test.go — the auxiliary journal's resume mark and
// its batched replay.
//
// The defect these tests pin down (task flatsql-aux-replay-resume-mark) was not
// a wrong row: it was 211 seconds of store-open, measured A/B/A on the canary,
// because the auxiliary replay had no resume mark and committed one transaction
// per event against a TRUNCATE-journalled disk database. So the assertions here
// are about WORK: how many frames a warm boot applies (zero), and — the part
// that actually needs proving — that doing the same work in chunk transactions
// lands the exact same state as doing it one event at a time.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// auxiliaryStateTables are every control table the auxiliary journal owns.
// A replay's whole observable output is the rows in these.
var auxiliaryStateTables = []string{
	"sdn_directory",
	"sdn_local_epms",
	"sdn_pin_ledger",
	"sdn_dataset_shard_publications",
	"sdn_dataset_publication_replay_state",
	"sdn_source_batch_license",
	"sdn_asset_oidc_receipts",
	"sdn_asset_pin_refs",
	"sdn_asset_pin_events",
}

// auxiliaryReplayClockColumns are the columns whose value comes from the
// REPLAY'S OWN wall clock rather than from the frame:
// applyDatasetShardPublicationUpsert writes `updated_at` as
// strftime('%s','now'). Any two replays of the same journal disagree on it —
// per-event or batched, before this change or after — so the equivalence claim
// excludes it, and nothing else. (It is bookkeeping only: no query reads it.)
var auxiliaryReplayClockColumns = map[string]bool{
	"sdn_dataset_shard_publications.updated_at": true,
}

// auxiliaryStateDigest is a canonical fingerprint of everything the auxiliary
// replay produces: every row of every auxiliary table, column-ordered and
// row-sorted so it does not depend on insertion order or rowid allocation.
//
// It deliberately does NOT hash control.flatsqldb itself. Two replays that
// produce identical ROWS can produce different FILES — page allocation, free
// lists and the journal's high-water mark all depend on how many transactions
// were used, which is precisely the variable under test. "Byte-equivalent
// state" can only mean the rows.
func auxiliaryStateDigest(t *testing.T, store *FlatSQLStore) string {
	t.Helper()
	h := sha256.New()
	for _, table := range auxiliaryStateTables {
		h.Write([]byte(auxiliaryTableDigest(t, store, table)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// assertAuxiliaryStateEqual compares the two replays table by table, so a
// failure names WHICH applier disagreed instead of just reporting two hashes.
func assertAuxiliaryStateEqual(t *testing.T, want, got *FlatSQLStore, wantName, gotName string) {
	t.Helper()
	mismatched := false
	for _, table := range auxiliaryStateTables {
		w := auxiliaryTableDigest(t, want, table)
		g := auxiliaryTableDigest(t, got, table)
		if w != g {
			mismatched = true
			t.Errorf("%s differs:\n  %s: %s\n  %s: %s", table, wantName, w, gotName, g)
		}
	}
	if mismatched {
		t.FailNow()
	}
}

func auxiliaryTableDigest(t *testing.T, store *FlatSQLStore, table string) string {
	t.Helper()
	h := sha256.New()
	{
		rows, err := store.db.Query(fmt.Sprintf("SELECT * FROM %s", table))
		if err != nil {
			t.Fatalf("select %s: %v", table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("columns %s: %v", table, err)
		}
		var lines []string
		for rows.Next() {
			cells := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range cells {
				ptrs[i] = &cells[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", table, err)
			}
			var b strings.Builder
			for i, c := range cells {
				if auxiliaryReplayClockColumns[table+"."+cols[i]] {
					continue
				}
				fmt.Fprintf(&b, "%s=%v\x1f", cols[i], c)
			}
			lines = append(lines, b.String())
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s: %v", table, err)
		}
		rows.Close()
		sort.Strings(lines)
		fmt.Fprintf(h, "table:%s rows:%d\n", table, len(lines))
		for _, l := range lines {
			h.Write([]byte(l))
			h.Write([]byte("\n"))
		}
	}
	return fmt.Sprintf("%s", hex.EncodeToString(h.Sum(nil)))
}

// writeAuxiliaryFixture writes at least one frame of EVERY auxiliary event kind
// the replay knows how to apply, so an equivalence claim covers all twelve
// appliers rather than the two easy ones.
func writeAuxiliaryFixture(t *testing.T, store *FlatSQLStore, generation int) {
	t.Helper()
	ctx := context.Background()
	tag := fmt.Sprintf("g%d", generation)
	now := time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC).Add(time.Duration(generation) * time.Hour)

	if err := store.UpsertDirectoryRecord(DirectoryRecord{
		Kind:           "node",
		PeerID:         "16Uiu2HAmFixture" + tag,
		DN:             "SDN Fixture " + tag,
		BitcoinAddress: "bc1qfixture" + tag,
		EPMCID:         "bafyfixture" + tag,
		Source:         "local",
		EPMJSON:        `{"dn":"SDN Fixture"}`,
		UpdatedAt:      now.Unix(),
	}); err != nil {
		t.Fatalf("UpsertDirectoryRecord(%s): %v", tag, err)
	}

	if err := store.SaveLocalEPM("16Uiu2HAmFixtureLocal"+tag, []byte("EPM-bytes-"+tag)); err != nil {
		t.Fatalf("SaveLocalEPM(%s): %v", tag, err)
	}

	if err := store.UpsertPinLedgerEntry(PinLedgerEntry{
		CID:               "bafypin" + tag,
		SchemaName:        "OMM.fbs",
		ProviderPeerID:    "16Uiu2HAmProvider" + tag,
		ProviderID:        "provider-" + tag,
		SourceName:        "source-" + tag,
		BatchID:           "batch-" + tag,
		QueryProfile:      "default",
		Role:              "mirror",
		RowCount:          42,
		ByteCount:         4096,
		TTL:               24 * time.Hour,
		VerificationState: "verified",
		VerifiedAt:        now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("UpsertPinLedgerEntry(%s): %v", tag, err)
	}

	pub := DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "provider-" + tag,
		SourceName:   "source-" + tag,
		BatchID:      "batch-" + tag,
		QueryProfile: "default",
		Offset:       0,
		Limit:        100,
		RecordCount:  100,
		ByteCount:    8192,
		ShardCID:     "bafyshard" + tag,
		IndexCID:     "bafyindex" + tag,
		PublishedAt:  now,
	}
	if err := store.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatalf("UpsertDatasetShardPublication(%s): %v", tag, err)
	}
	doomed := pub
	doomed.Offset = 100
	doomed.ShardCID = "bafyshard-doomed" + tag
	doomed.IndexCID = "bafyindex-doomed" + tag
	if err := store.UpsertDatasetShardPublication(doomed); err != nil {
		t.Fatalf("UpsertDatasetShardPublication(doomed %s): %v", tag, err)
	}
	// A DESTRUCTIVE frame, so the equivalence claim covers the one applier that
	// removes rows rather than upserting them.
	if _, err := store.DeleteDatasetShardPublicationsAtOrAfterOffset(DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "provider-" + tag,
		SourceName:   "source-" + tag,
		BatchID:      "batch-" + tag,
		QueryProfile: "default",
	}, 100); err != nil {
		t.Fatalf("DeleteDatasetShardPublicationsAtOrAfterOffset(%s): %v", tag, err)
	}

	if err := store.UpsertDatasetPublicationReplayState(DatasetPublicationReplayState{
		PNMKey:     "pnm-" + tag,
		SchemaName: "OMM.fbs",
		PNMCID:     "bafypnm" + tag,
		FileID:     "$OMM",
		State:      "applied",
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("UpsertDatasetPublicationReplayState(%s): %v", tag, err)
	}

	if err := store.UpsertSourceBatchLicense(SourceBatchLicense{
		SchemaName: "OMM.fbs",
		ProviderID: "provider-" + tag,
		SourceName: "source-" + tag,
		BatchID:    "batch-" + tag,
		License:    "CC-BY-4.0",
		LicenseURL: "https://creativecommons.org/licenses/by/4.0/",
		Citation:   "Fixture " + tag,
		UpdatedAt:  now.Unix(),
	}); err != nil {
		t.Fatalf("UpsertSourceBatchLicense(%s): %v", tag, err)
	}

	if err := store.ConsumeAssetOIDCToken(ctx, AssetOIDCReceipt{
		Digest:      fmt.Sprintf("%064x", generation),
		ExpiresAt:   now.Add(time.Hour),
		Repository:  "SpaceDataNetwork/asset-models",
		Ref:         "refs/heads/main",
		WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:       "asset-bot",
		RunID:       "run-" + tag,
		RunAttempt:  "1",
		SHA:         strings.Repeat("3", 40),
		ConsumedAt:  now,
	}); err != nil {
		t.Fatalf("ConsumeAssetOIDCToken(%s): %v", tag, err)
	}

	staged := testAssetPinReference("reference-"+tag, "candidate-"+tag, "bafyasset"+tag,
		strings.Repeat("b", 64), AssetReferenceStaged, now, time.Time{})
	if err := store.UpsertAssetPinReference(ctx, staged,
		testAssetPinEvent("event-upsert-"+tag, "reference_upsert", staged, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference(%s): %v", tag, err)
	}
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: staged.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		GitHubIssue:  4242,
		UpdatedAt:    now.Add(time.Minute),
		ExpiresAt:    now.Add(90 * 24 * time.Hour),
	}, testAssetPinEvent("event-transition-"+tag, "reference_transition", staged, now.Add(time.Minute))); err != nil {
		t.Fatalf("TransitionAssetPinReference(%s): %v", tag, err)
	}
	if err := store.AppendAssetPinAuditEvent(ctx,
		testAssetPinEvent("event-audit-"+tag, "pin_verify", staged, now.Add(2*time.Minute))); err != nil {
		t.Fatalf("AppendAssetPinAuditEvent(%s): %v", tag, err)
	}
}

// replayAuxiliaryPerEvent is the PRE-FIX replay: one autocommit transaction per
// event, no batch writer, no resume. It is kept in the test file — and only
// there — as the reference implementation the batched replay must agree with.
func replayAuxiliaryPerEvent(t *testing.T, m *auxiliaryMetadataStore, store *FlatSQLStore) int {
	t.Helper()
	size := m.validLength()
	var off int64
	var hdr [8]byte
	count := 0
	for off < size {
		if _, err := m.f.ReadAt(hdr[:], off); err != nil {
			t.Fatalf("read auxiliary frame header at %d: %v", off, err)
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		crc := binary.LittleEndian.Uint32(hdr[4:])
		if n == 0 || off+8+n > size {
			break
		}
		payload := make([]byte, n)
		if _, err := m.f.ReadAt(payload, off+8); err != nil {
			t.Fatalf("read auxiliary frame payload at %d: %v", off, err)
		}
		if crc32.ChecksumIEEE(payload) != crc {
			break
		}
		event, err := decodeAuxiliaryMetadataEvent(payload)
		if err != nil {
			t.Fatalf("decode auxiliary frame at %d: %v", off, err)
		}
		if err := store.applyAuxiliaryMetadataEvent(event); err != nil {
			t.Fatalf("apply auxiliary frame at %d: %v", off, err)
		}
		count++
		off += 8 + n
	}
	return count
}

func newFixtureStore(t *testing.T, basePath string) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator(): %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore(%s): %v", basePath, err)
	}
	return store
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read dir %s: %v", src, err)
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDir(t, s, d)
			continue
		}
		if e.Name() == "store.lock" {
			continue // a liveness lock is not state; never copy one
		}
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatalf("read %s: %v", s, err)
		}
		if err := os.WriteFile(d, b, 0o600); err != nil {
			t.Fatalf("write %s: %v", d, err)
		}
	}
}

// TestAuxiliaryReplayBatchedMatchesPerEventState is THE acceptance test for the
// batching half: the same journal, replayed one-transaction-per-event and
// replayed in chunk transactions, must leave identical auxiliary state.
func TestAuxiliaryReplayBatchedMatchesPerEventState(t *testing.T) {
	root := t.TempDir()
	basePath := filepath.Join(root, "store")
	store := newFixtureStore(t, basePath)
	for gen := 1; gen <= 3; gen++ {
		writeAuxiliaryFixture(t, store, gen)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	// LEG A — the batched replay: a copy of the store with its control database
	// removed, so opening it replays the whole journal in chunk transactions.
	batchedPath := filepath.Join(root, "batched")
	copyDir(t, basePath, batchedPath)
	if err := removeControlDatabaseFiles(filepath.Join(batchedPath, flatSQLControlDBName)); err != nil {
		t.Fatalf("discard control database in %s: %v", batchedPath, err)
	}
	batched := newFixtureStore(t, batchedPath)
	defer batched.Close()
	if batched.bootAuxWarm {
		t.Fatalf("batched fixture should be a COLD auxiliary boot, got warm at %d", batched.bootAuxFrom)
	}
	if batched.bootAuxFrames == 0 {
		t.Fatal("batched cold boot applied 0 auxiliary frames; the fixture wrote none")
	}
	batchedDigest := auxiliaryStateDigest(t, batched)

	// LEG B — the pre-fix replay: a store that never saw the journal at all
	// (so no applier has run and no rowid has been allocated), fed the SAME
	// journal file through a READ-ONLY handle, one autocommit per event.
	//
	// Opening the journal into a populated store and clearing the tables first
	// would NOT be equivalent: DELETE leaves SQLite's autoincrement high-water
	// marks behind, so the audit-event rowids would differ for a reason that
	// has nothing to do with batching.
	perEvent := newFixtureStore(t, filepath.Join(root, "per-event"))
	defer perEvent.Close()
	if perEvent.bootAuxFrames != 0 {
		t.Fatalf("per-event leg started from a non-empty store (%d frames)", perEvent.bootAuxFrames)
	}
	fixtureJournal, err := openAuxiliaryMetadataStore(filepath.Join(basePath, auxiliaryMetadataFileName), true)
	if err != nil {
		t.Fatalf("open fixture auxiliary journal read-only: %v", err)
	}
	defer fixtureJournal.Close()
	perEventFrames := replayAuxiliaryPerEvent(t, fixtureJournal, perEvent)
	if perEventFrames != batched.bootAuxFrames {
		t.Fatalf("per-event replay applied %d frames, batched applied %d", perEventFrames, batched.bootAuxFrames)
	}
	if perEventDigest := auxiliaryStateDigest(t, perEvent); perEventDigest != batchedDigest {
		assertAuxiliaryStateEqual(t, perEvent, batched, "per-event", "batched")
		t.Fatalf("batched auxiliary state %s != per-event auxiliary state %s", batchedDigest, perEventDigest)
	}
}

// TestAuxiliaryReplayWarmBootAppliesNothing is the acceptance test for the mark
// half. Cold and warm being identical was the canary's signature of a missing
// mark, so the assertion is on FRAMES, not on wall clock — a fixture this small
// would be fast either way.
func TestAuxiliaryReplayWarmBootAppliesNothing(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newFixtureStore(t, basePath)
	writeAuxiliaryFixture(t, store, 1)
	writeAuxiliaryFixture(t, store, 2)
	coldDigest := auxiliaryStateDigest(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	warm := newFixtureStore(t, basePath)
	defer warm.Close()
	if !warm.bootAuxWarm {
		t.Fatal("reopen was not a WARM auxiliary boot; the mark did not survive Close")
	}
	if warm.bootAuxFrames != 0 {
		t.Fatalf("warm auxiliary boot applied %d frames, want 0", warm.bootAuxFrames)
	}
	if warm.bootAuxFrom != warm.auxiliaryMetadata.validLength() {
		t.Fatalf("warm resume offset %d != journal length %d", warm.bootAuxFrom, warm.auxiliaryMetadata.validLength())
	}
	// Skipping the prefix must not skip the STATE: a warm boot's tables are the
	// ones the previous process committed.
	if got := auxiliaryStateDigest(t, warm); got != coldDigest {
		t.Fatalf("warm auxiliary state %s != pre-close state %s", got, coldDigest)
	}

	// And a third boot after more writes resumes at the NEW end, applying only
	// what arrived after the previous mark.
	writeAuxiliaryFixture(t, warm, 3)
	afterDigest := auxiliaryStateDigest(t, warm)
	previousEnd := warm.bootAuxFrom
	if err := warm.Close(); err != nil {
		t.Fatalf("Close() warm: %v", err)
	}
	third := newFixtureStore(t, basePath)
	defer third.Close()
	if !third.bootAuxWarm || third.bootAuxFrames != 0 {
		t.Fatalf("third boot warm=%v frames=%d, want warm with 0 frames", third.bootAuxWarm, third.bootAuxFrames)
	}
	if third.bootAuxFrom <= previousEnd {
		t.Fatalf("third boot resumed at %d, which did not advance past %d", third.bootAuxFrom, previousEnd)
	}
	if got := auxiliaryStateDigest(t, third); got != afterDigest {
		t.Fatalf("third boot auxiliary state %s != state at close %s", got, afterDigest)
	}
}

// TestAuxiliaryResumeMarkRefusesForeignJournalDigest proves the two marks
// cannot be crossed: a mark whose digest was computed over the OTHER journal's
// domain must fall back to a full replay rather than skipping real frames.
func TestAuxiliaryResumeMarkRefusesForeignJournalDigest(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newFixtureStore(t, basePath)
	writeAuxiliaryFixture(t, store, 1)
	end := store.auxiliaryMetadata.validLength()

	honest, err := store.auxiliaryMetadata.digestPrefix(end)
	if err != nil {
		t.Fatalf("digestPrefix(): %v", err)
	}
	// The same bytes, fingerprinted in the record catalog's domain.
	foreign, err := digestRecordCatalogPrefix(store.auxiliaryMetadata.f, end)
	if err != nil {
		t.Fatalf("digestRecordCatalogPrefix(): %v", err)
	}
	if honest == foreign {
		t.Fatal("auxiliary and record-catalog digests of the same bytes are equal; the domain prefix is not doing its job")
	}
	if got := auxiliaryResumeOffset(bootMark{AuxOffset: end, AuxDigest: foreign}, true, store.auxiliaryMetadata); got != 0 {
		t.Fatalf("resume with a foreign digest = %d, want 0 (full replay)", got)
	}
	if got := auxiliaryResumeOffset(bootMark{AuxOffset: end, AuxDigest: honest}, true, store.auxiliaryMetadata); got != end {
		t.Fatalf("resume with the honest digest = %d, want %d", got, end)
	}
	if got := auxiliaryResumeOffset(bootMark{AuxOffset: end + 1, AuxDigest: honest}, true, store.auxiliaryMetadata); got != 0 {
		t.Fatalf("resume past the journal end = %d, want 0", got)
	}
	if got := auxiliaryResumeOffset(bootMark{AuxOffset: end, AuxDigest: honest}, false, store.auxiliaryMetadata); got != 0 {
		t.Fatalf("resume from a COLD database = %d, want 0", got)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

// TestAuxiliaryMarkFreezesWhileAssetPinLedgerNeedsRecovery covers the one
// writer that journals BEFORE it commits. While its ledger is poisoned there
// may be a frame on disk whose rows never landed, so the mark must stop moving
// entirely — under-marking costs replay, over-marking loses the mutation.
func TestAuxiliaryMarkFreezesWhileAssetPinLedgerNeedsRecovery(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newFixtureStore(t, basePath)
	defer store.Close()
	writeAuxiliaryFixture(t, store, 1)

	frozenAt := store.auxAppliedOffset.Load()
	if frozenAt <= 0 {
		t.Fatal("fixture did not advance the auxiliary applied offset")
	}

	// A poisoned ledger refuses every public write verb, so the frame that
	// models "journaled but never committed" is appended at the journal level —
	// which is exactly the state the asset-pin lane leaves behind when its
	// commit fails after its append.
	store.assetPinLedgerRecovery.Store(true)
	if err := store.auxiliaryMetadata.Append(auxiliaryMetadataEvent{
		Kind: auxiliaryEventDirectoryUpsert,
		Directory: &DirectoryRecord{
			Kind: "node", PeerID: "16Uiu2HAmOrphan", DN: "orphan", Source: "local",
			EPMJSON: "{}", UpdatedAt: 1,
		},
	}); err != nil {
		t.Fatalf("append orphan frame: %v", err)
	}
	grown := store.auxiliaryMetadata.validLength()
	if grown <= frozenAt {
		t.Fatalf("orphan frame did not grow the journal (%d <= %d)", grown, frozenAt)
	}
	store.noteAuxiliaryApplied()
	if got := store.auxAppliedOffset.Load(); got != frozenAt {
		t.Fatalf("auxiliary applied offset advanced to %d under ledger recovery; want it frozen at %d", got, frozenAt)
	}
	if _, _, ok, err := store.auxiliaryMarkCandidate(); err != nil || ok {
		t.Fatalf("auxiliaryMarkCandidate() under recovery = ok %v, err %v; want no candidate", ok, err)
	}

	// Recovery is a store reopen, and the reopened store replays the orphan
	// frame — which is the whole point of refusing to mark past it.
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	recovered := newFixtureStore(t, basePath)
	defer recovered.Close()
	if recovered.bootAuxFrames == 0 {
		t.Fatal("reopen applied 0 auxiliary frames; the orphan frame was skipped")
	}
	var orphanDN string
	if err := recovered.db.QueryRow(
		`SELECT dn FROM sdn_directory WHERE peer_id = ?`, "16Uiu2HAmOrphan").Scan(&orphanDN); err != nil {
		t.Fatalf("orphan frame was not replayed: %v", err)
	}
	if orphanDN != "orphan" {
		t.Fatalf("replayed orphan dn = %q, want %q", orphanDN, "orphan")
	}
}

// TestAuxiliaryReplayChunkBoundaryIsExact crosses the chunk seam so an
// off-by-one there cannot hide: a frame dropped or applied twice at a chunk
// boundary is the failure mode batching introduces, and it is invisible in any
// fixture smaller than one chunk.
//
// The frames are appended STRAIGHT TO THE JOURNAL rather than written through
// the public verbs. That is deliberate on two counts: a live write costs two
// fsyncs (~25 ms), so 1,000 of them would put a minute on the go-host tier for
// nothing; and appending without applying is precisely the claim under test —
// the journal is the source of truth, and a replay is what turns it into rows.
func TestAuxiliaryReplayChunkBoundaryIsExact(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newFixtureStore(t, basePath)

	// Two full chunks and a partial one, so the seam is crossed twice.
	frames := 2*auxiliaryReplayChunkFrames + 37
	for i := 0; i < frames; i++ {
		if err := store.auxiliaryMetadata.Append(auxiliaryMetadataEvent{
			Kind: auxiliaryEventDirectoryUpsert,
			Directory: &DirectoryRecord{
				Kind:      "node",
				PeerID:    fmt.Sprintf("16Uiu2HAmSeam%06d", i),
				DN:        fmt.Sprintf("seam %d", i),
				Source:    "local",
				EPMJSON:   "{}",
				UpdatedAt: int64(1700000000 + i),
			},
		}); err != nil {
			t.Fatalf("append frame %d: %v", i, err)
		}
	}
	journalEnd := store.auxiliaryMetadata.validLength()
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	if err := removeControlDatabaseFiles(filepath.Join(basePath, flatSQLControlDBName)); err != nil {
		t.Fatalf("discard control database: %v", err)
	}
	cold := newFixtureStore(t, basePath)
	defer cold.Close()
	if cold.bootAuxFrames != frames {
		t.Fatalf("cold replay applied %d frames, want %d (%d chunks + %d)",
			cold.bootAuxFrames, frames, frames/auxiliaryReplayChunkFrames, frames%auxiliaryReplayChunkFrames)
	}
	if got := cold.auxAppliedOffset.Load(); got != journalEnd {
		t.Fatalf("cold replay applied through %d, want the whole journal %d", got, journalEnd)
	}
	var rows int
	if err := cold.db.QueryRow(
		`SELECT COUNT(*) FROM sdn_directory WHERE peer_id LIKE '16Uiu2HAmSeam%'`).Scan(&rows); err != nil {
		t.Fatalf("count replayed rows: %v", err)
	}
	if rows != frames {
		t.Fatalf("replayed %d directory rows across %d chunks, want %d", rows, frames/auxiliaryReplayChunkFrames+1, frames)
	}

	// And the reopen after it is warm: the seam-crossing replay marked the whole
	// journal, not just the last chunk.
	if err := cold.Close(); err != nil {
		t.Fatalf("Close() cold: %v", err)
	}
	warm := newFixtureStore(t, basePath)
	defer warm.Close()
	if !warm.bootAuxWarm || warm.bootAuxFrames != 0 || warm.bootAuxFrom != journalEnd {
		t.Fatalf("reopen after a multi-chunk replay: warm=%v frames=%d from=%d, want warm with 0 frames at %d",
			warm.bootAuxWarm, warm.bootAuxFrames, warm.bootAuxFrom, journalEnd)
	}
}
