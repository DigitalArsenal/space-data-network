package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	CATFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	MPEFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	OEMFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	"github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const gpArchiveTestHeader = "OBJECT_NAME,OBJECT_ID,EPOCH,MEAN_MOTION,ECCENTRICITY,INCLINATION,RA_OF_ASC_NODE,ARG_OF_PERICENTER,MEAN_ANOMALY,EPHEMERIS_TYPE,CLASSIFICATION_TYPE,NORAD_CAT_ID,ELEMENT_SET_NO,REV_AT_EPOCH,BSTAR,MEAN_MOTION_DOT,MEAN_MOTION_DDOT,DATA_SOURCE\n"

type gpArchiveMemorySink struct {
	mpe, catalogMPE, cat, oem [][]byte
	tags                      []storage.SourceTags
	deletedMPE, deletedOEM    []string
}

func (s *gpArchiveMemorySink) StoreHistoryMPE(records [][]byte) (int, error) {
	for _, record := range records {
		s.mpe = append(s.mpe, append([]byte(nil), record...))
	}
	return len(records), nil
}
func (s *gpArchiveMemorySink) UpsertHistoryLicense(storage.SourceBatchLicense) error { return nil }
func (s *gpArchiveMemorySink) StoreCatalogMPE(records [][]byte, tags storage.SourceTags) (int, error) {
	for _, r := range records {
		s.catalogMPE = append(s.catalogMPE, append([]byte(nil), r...))
	}
	s.tags = append(s.tags, tags)
	return len(records), nil
}
func (s *gpArchiveMemorySink) DeleteCatalogMPE(cid string) error {
	s.deletedMPE = append(s.deletedMPE, cid)
	return nil
}

func (s *gpArchiveMemorySink) StoreCatalogCAT(records [][]byte, tags storage.SourceTags) (int, error) {
	for _, record := range records {
		s.cat = append(s.cat, append([]byte(nil), record...))
	}
	s.tags = append(s.tags, tags)
	return len(records), nil
}
func (s *gpArchiveMemorySink) StoreOEMBatch(writes []gpOEMWrite) ([]string, error) {
	cids := make([]string, len(writes))
	for i, w := range writes {
		s.oem = append(s.oem, append([]byte(nil), w.Record...))
		s.tags = append(s.tags, w.Tags)
		cids[i] = storage.ComputeCID(w.Record)
	}
	return cids, nil
}
func (s *gpArchiveMemorySink) DeleteOEM(cid string) error {
	s.deletedOEM = append(s.deletedOEM, cid)
	return nil
}

func (s *gpArchiveMemorySink) Close() error { return nil }

func TestGPArchiveCSVRowPreservesFractionalEpochAndZeroFields(t *testing.T) {
	columns := gpArchiveTestColumns()
	row := []string{
		"", "1958-002B", "1959-05-23T00:19:59.047968", "0", "0", "0", "0", "0", "0",
		"0", "U", "5", "1", "0", "0", "0", "0", "SPACE-TRACK",
	}
	record, reason := gpArchiveRecordFromCSV(row, columns)
	if reason != "" {
		t.Fatalf("gpArchiveRecordFromCSV rejected row: %s", reason)
	}
	data := buildGPArchiveMPE(record.ObjectID, record)
	mpe := MPEFB.GetSizePrefixedRootAsMPE(data, 0)
	wantTime, err := parseGPArchiveEpoch("1959-05-23T00:19:59.047968")
	if err != nil {
		t.Fatal(err)
	}
	wantEpoch := float64(wantTime.Unix()) + float64(wantTime.Nanosecond())/1e9
	if delta := math.Abs(mpe.EPOCH() - wantEpoch); delta > 0.000001 {
		t.Fatalf("EPOCH lost microseconds: got %.9f want %.9f delta %.9f", mpe.EPOCH(), wantEpoch, delta)
	}
	if got := string(mpe.ENTITY_ID()); got != "1958-002B" {
		t.Fatalf("ENTITY_ID = %q, want 1958-002B", got)
	}
	table := mpe.Table()
	for slot := 2; slot <= 9; slot++ {
		if offset := table.Offset(flatbuffers.VOffsetT(4 + slot*2)); offset == 0 {
			t.Fatalf("MPE slot %d was elided even though zero is data", slot)
		}
	}
}

func TestGPArchiveJSONWindowAcceptsStringAndNumericValues(t *testing.T) {
	input := []byte(`{
		"GP_ID":"218213651","OBJECT_NAME":"CHINASAT 2E","OBJECT_ID":"2021-071A",
		"EPOCH":"2022-11-19T22:11:05.523072","MEAN_MOTION":"1.00271749",
		"ECCENTRICITY":0.000263,"INCLINATION":"0.0239","RA_OF_ASC_NODE":32.4905,
		"ARG_OF_PERICENTER":"268.1503","MEAN_ANOMALY":189.1205,
		"NORAD_CAT_ID":49062,"BSTAR":"0.00000000000000"
	}`)
	var raw gpJSONRecord
	if err := json.Unmarshal(input, &raw); err != nil {
		t.Fatal(err)
	}
	record, reason := gpArchiveRecordFromJSON(raw)
	if reason != "" {
		t.Fatalf("gpArchiveRecordFromJSON rejected row: %s", reason)
	}
	if record.NORAD != 49062 || record.MeanMotion != 1.00271749 || record.BSTAR != 0 {
		t.Fatalf("unexpected parsed record: %+v", record)
	}
}

func TestGPArchiveMissingDesignatorUsesNamespacedEntityAndReportsObject(t *testing.T) {
	report := newGPArchiveRunReport()
	record := gpArchiveRecord{NORAD: 42, Epoch: 1, MeanMotion: 1}
	canonicalizeGPRecord(&record, map[uint32]gpSATCATEntry{}, gpArchiveTestCheckpoint(), report)
	mpeData := buildGPArchiveMPE(record.EntityID, record)
	report.finalize()
	if report.ObjectsLackingDesignator != 1 || len(report.ObjectsWithoutDesignator) != 1 || report.ObjectsWithoutDesignator[0] != "NORAD:42" {
		t.Fatalf("missing-designator report = %+v", report)
	}
	if got := string(MPEFB.GetSizePrefixedRootAsMPE(mpeData, 0).ENTITY_ID()); got != "NORAD:42" {
		t.Fatalf("MPE ENTITY_ID = %q, want NORAD:42", got)
	}
	cat := CATFB.GetSizePrefixedRootAsCAT(buildGPArchiveCAT(record.EntityID, record.NORAD, ""), 0)
	if got := string(cat.OBJECT_ID()); got != "NORAD:42" || cat.NORAD_CAT_ID() != 42 {
		t.Fatalf("CAT crosswalk = (%q, %d), want (NORAD:42, 42)", got, cat.NORAD_CAT_ID())
	}
}

func TestGPArchiveSATCATCanonicalIdentityIgnoresRowObjectID(t *testing.T) {
	cp := gpArchiveTestCheckpoint()
	report := newGPArchiveRunReport()
	satcat := map[uint32]gpSATCATEntry{162: {EntityID: "1961-017A", ObjectName: "EXPLORER 10"}}
	for _, rowID := range []string{"1961-017A", "1961-RHO1", "", "1980-030K"} {
		r := gpArchiveRecord{NORAD: 162, ObjectID: rowID}
		canonicalizeGPRecord(&r, satcat, cp, report)
		if r.EntityID != "1961-017A" {
			t.Fatalf("row %q got entity %q", rowID, r.EntityID)
		}
	}
	if report.ObjectIDDisagreements.Blank != 1 || report.ObjectIDDisagreements.OldStyle != 1 || report.ObjectIDDisagreements.Different != 1 {
		t.Fatalf("disagreements = %+v", report.ObjectIDDisagreements)
	}
}

func TestGPArchiveLatestTieBreaksByRowOrderAndGPID(t *testing.T) {
	cp := gpArchiveTestCheckpoint()
	r := gpArchiveRecord{EntityID: "2000-001A", NORAD: 1, Epoch: 10}
	m := buildGPArchiveMPE(r.EntityID, r)
	observeGPLatest(cp, r, m, "zip-1", "s", "zip", 1)
	observeGPLatest(cp, r, m, "zip-2", "s", "zip", 2)
	if cp.Latest[r.EntityID].HistoryCID != "zip-2" {
		t.Fatal("later zip row did not win")
	}
	observeGPLatest(cp, r, m, "json-10", "s", "json", 10)
	observeGPLatest(cp, r, m, "json-9", "s", "json", 9)
	if cp.Latest[r.EntityID].HistoryCID != "json-10" {
		t.Fatal("larger GP_ID did not win equal epoch")
	}
}

func TestGPArchiveCSVExactRepeatIsDeduplicated(t *testing.T) {
	root := t.TempDir()
	zipPath := filepath.Join(root, "archive.zip")
	row := ",1958-002B,1959-05-23T00:19:59.047968,15.1,.001,34,1,2,3,0,U,5,1,1,0,0,0,NORAD\n"
	writeGPArchiveTestZip(t, zipPath, gpArchiveTestHeader+row+row)
	sink := &gpArchiveMemorySink{}
	report, err := importGPArchive(context.Background(), gpArchiveTestOptions(t, root, zipPath, ""), sink)
	if err != nil {
		t.Fatal(err)
	}
	if report.RecordsRead != 2 || report.MPEStored != 1 || report.DuplicatesSkipped != 1 {
		t.Fatalf("CSV report = %+v", report)
	}
}

func TestGPArchiveZipShardSelectsOrdinalsAndIsHistoryOnly(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "archive.zip")
	entries := make([]string, 4)
	for i := range entries {
		entries[i] = gpArchiveTestHeader + fmt.Sprintf(",1958-002B,2022-01-0%dT00:00:00.000001,15.1,.001,34,1,2,3,0,U,5,1,1,0,0,0,NORAD\n", i+1)
	}
	writeGPArchiveTestZipEntries(t, zipPath, entries)
	for shard := 0; shard < 2; shard++ {
		root := t.TempDir()
		opts := gpArchiveTestOptions(t, root, zipPath, "")
		opts.ZipShard = fmt.Sprintf("%d/2", shard)
		sink := &gpArchiveMemorySink{}
		report, err := importGPArchive(context.Background(), opts, sink)
		if err != nil {
			t.Fatalf("shard %d: %v", shard, err)
		}
		if report.RecordsRead != 2 || report.MPEStored != 2 || len(sink.catalogMPE) != 0 || len(sink.cat) != 0 {
			t.Fatalf("shard %d report=%+v catalogMPE=%d CAT=%d", shard, report, len(sink.catalogMPE), len(sink.cat))
		}
		cp, err := loadGPArchiveCheckpoint(opts.CheckpointPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(cp.CompletedZipEntries) != 2 || cp.ZipCSVEntries != 2 {
			t.Fatalf("shard %d checkpoint completed=%d selected=%d", shard, len(cp.CompletedZipEntries), cp.ZipCSVEntries)
		}
		if err := opts.normalize(); err != nil {
			t.Fatal(err)
		}
		if !opts.HistoryOnly || opts.historySourceName() != fmt.Sprintf("gp-history-zip-shard-%d-of-2", shard) {
			t.Fatalf("shard %d options not normalized: %+v", shard, opts)
		}
	}
}

func TestGPArchiveZipShardUsesBulkBatchDefault(t *testing.T) {
	if got := effectiveGPArchiveBatchSize("3/8", 2000, false); got != 100000 {
		t.Fatalf("shard default batch = %d, want 100000", got)
	}
	if got := effectiveGPArchiveBatchSize("3/8", 20000, true); got != 20000 {
		t.Fatalf("explicit shard batch = %d, want 20000", got)
	}
	if got := effectiveGPArchiveBatchSize("", 2000, false); got != 2000 {
		t.Fatalf("normal default batch = %d, want 2000", got)
	}
}

func TestGPArchiveTwoZipShardsFoldToUnshardedCatalog(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "archive.zip")
	rows := []string{
		",1958-002B,2022-01-01T00:00:00.000001,15.1,.001,34,1,2,3,0,U,5,1,1,0,0,0,NORAD\n",
		",1958-002C,2022-01-04T00:00:00.000001,15.2,.002,35,2,3,4,0,U,6,1,1,0,0,0,NORAD\n",
		",1958-002B,2022-01-01T00:00:00.000001,15.1,.001,34,1,2,9,0,U,5,1,1,0,0,0,NORAD\n",
		",2000-001A,2022-01-02T00:00:00.000001,15.3,.003,36,3,4,5,0,U,99,1,1,0,0,0,NORAD\n",
		",1958-002C,2022-01-05T00:00:00.000001,15.2,.002,35,2,3,8,0,U,6,1,1,0,0,0,NORAD\n",
	}
	entries := make([]string, len(rows))
	for i := range rows {
		entries[i] = gpArchiveTestHeader + rows[i]
	}
	writeGPArchiveTestZipEntries(t, zipPath, entries)

	unshardedRoot := t.TempDir()
	unshardedSink := &gpArchiveMemorySink{}
	if _, err := importGPArchive(context.Background(), gpArchiveTestOptions(t, unshardedRoot, zipPath, ""), unshardedSink); err != nil {
		t.Fatal(err)
	}

	mainRoot := t.TempDir()
	mainOpts := gpArchiveTestOptions(t, mainRoot, zipPath, "")
	for shard := 0; shard < 2; shard++ {
		shardOpts := mainOpts
		shardOpts.Out = filepath.Join(mainOpts.Out, "zip-shards", strconv.Itoa(shard))
		shardOpts.CheckpointPath = filepath.Join(shardOpts.Out, "gp-archive-checkpoint.json")
		shardOpts.ReportPath = filepath.Join(shardOpts.Out, "gp-archive-report.json")
		shardOpts.ZipShard = fmt.Sprintf("%d/2", shard)
		if _, err := importGPArchive(context.Background(), shardOpts, &gpArchiveMemorySink{}); err != nil {
			t.Fatalf("shard %d: %v", shard, err)
		}
	}
	mainOpts.SkipZip = true
	mainSink := &gpArchiveMemorySink{}
	report, err := importGPArchive(context.Background(), mainOpts, mainSink)
	if err != nil {
		t.Fatal(err)
	}
	if report.ShardCheckpointsFolded != 2 || report.ShardEntitiesUpdated == 0 {
		t.Fatalf("fold report = %+v", report)
	}
	want := gpArchiveCatalogMPEByEntity(t, unshardedSink.catalogMPE)
	got := gpArchiveCatalogMPEByEntity(t, mainSink.catalogMPE)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("folded catalog differs from unsharded\n got: %#v\nwant: %#v", got, want)
	}
	if len(mainSink.tags) == 0 || !strings.HasPrefix(mainSink.tags[0].BatchID, "zip:") {
		t.Fatalf("folded catalog did not retain shard source batch: %+v", mainSink.tags)
	}

	secondSink := &gpArchiveMemorySink{}
	second, err := importGPArchive(context.Background(), mainOpts, secondSink)
	if err != nil {
		t.Fatal(err)
	}
	if second.ShardCheckpointsSkipped != 2 || len(secondSink.catalogMPE) != 0 {
		t.Fatalf("unchanged shard checkpoints were not skipped: report=%+v writes=%d", second, len(secondSink.catalogMPE))
	}
}

func TestGPArchiveShardFoldReportsSATCATMismatch(t *testing.T) {
	out := t.TempDir()
	shardDir := filepath.Join(out, "zip-shards", "0")
	shard := gpArchiveTestCheckpoint()
	shard.SATCATSHA256 = "shard-hash"
	if err := saveGPArchiveCheckpoint(filepath.Join(shardDir, "gp-archive-checkpoint.json"), shard); err != nil {
		t.Fatal(err)
	}
	cp := gpArchiveTestCheckpoint()
	cp.SATCATSHA256 = "main-hash"
	report := newGPArchiveRunReport()
	if err := foldGPArchiveShardCheckpoints(gpArchiveOptions{Out: out}, cp, map[string]gpArchiveSource{}, report); err != nil {
		t.Fatal(err)
	}
	if len(report.ShardSATCATMismatches) != 1 || report.ShardCheckpointsFolded != 0 {
		t.Fatalf("SATCAT mismatch report = %+v", report)
	}
}

func TestGPArchiveCheckpointCadenceAndForcedSave(t *testing.T) {
	cp := gpArchiveTestCheckpoint()
	base := time.Date(2026, 9, 26, 15, 0, 0, 0, time.UTC)
	now := base
	checkpointWrites, sourceWrites := 0, 0
	p := &gpArchiveProgress{
		checkpointPath: "checkpoint", sourcesPath: "sources", cp: cp,
		sources: map[string]gpArchiveSource{}, lastSaved: base, now: func() time.Time { return now },
		saveCheckpoint: func(string, *gpArchiveCheckpoint) error { checkpointWrites++; return nil },
		saveSources:    func(string, map[string]gpArchiveSource) error { sourceWrites++; return nil },
	}
	if err := p.save(false); err != nil {
		t.Fatal(err)
	}
	now = base.Add(59 * time.Second)
	if err := p.save(false); err != nil {
		t.Fatal(err)
	}
	if checkpointWrites != 0 || sourceWrites != 0 {
		t.Fatalf("saved before cadence elapsed: checkpoint=%d sources=%d", checkpointWrites, sourceWrites)
	}
	now = base.Add(60 * time.Second)
	if err := p.save(false); err != nil {
		t.Fatal(err)
	}
	if checkpointWrites != 1 || sourceWrites != 1 {
		t.Fatalf("60-second save count: checkpoint=%d sources=%d", checkpointWrites, sourceWrites)
	}
	if err := p.save(true); err != nil {
		t.Fatal(err)
	}
	if checkpointWrites != 2 || sourceWrites != 2 {
		t.Fatalf("forced phase save count: checkpoint=%d sources=%d", checkpointWrites, sourceWrites)
	}
}

func TestGPArchiveCompletedZipFastPathDoesNotOpenOrHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, []byte("intentionally not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cp := gpArchiveTestCheckpoint()
	cp.ZipSize = st.Size()
	cp.ZipModTimeUnixNano = st.ModTime().UnixNano()
	cp.ZipCSVEntries = 1
	cp.CompletedZipEntries["Archives2/sat000000005.csv"] = "already-complete"
	report := newGPArchiveRunReport()
	if err := importGPArchiveZip(context.Background(), gpArchiveOptions{ZipPath: path}, cp, nil, nil, report, &gpArchiveMemorySink{}, nil); err != nil {
		t.Fatalf("fast path opened or hashed the invalid zip: %v", err)
	}
	if report.CompletedZipEntriesSkipped != 1 {
		t.Fatalf("completed entries skipped = %d, want 1", report.CompletedZipEntriesSkipped)
	}
}

func TestGPArchiveSkipZipImportsJSONAndMaterializesCatalog(t *testing.T) {
	root := t.TempDir()
	windowDir := filepath.Join(root, "spacetrack", "gp_history", "by-creation", "2022")
	if err := os.MkdirAll(windowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	windowPath := filepath.Join(windowDir, "2022-11-20.json.gz")
	hash := writeGPArchiveTestGzip(t, windowPath, []byte("["+gpArchiveTestJSONRecord("100", "1958-002B", "5")+"]"))
	ledgerPath := filepath.Join(root, "state", "spacetrack-ledger.json")
	writeGPArchiveTestLedger(t, ledgerPath, []gpArchiveLedgerWindow{{File: "spacetrack/gp_history/by-creation/2022/2022-11-20.json.gz", SHA256: hash, RetrievedAt: "2026-09-26T15:00:00Z"}})
	opts := gpArchiveTestOptions(t, root, filepath.Join(root, "missing.zip"), ledgerPath)
	opts.SkipZip = true
	sink := &gpArchiveMemorySink{}
	report, err := importGPArchive(context.Background(), opts, sink)
	if err != nil {
		t.Fatal(err)
	}
	if report.WindowFilesImported != 1 || report.MPEStored != 1 || len(sink.catalogMPE) != 1 {
		t.Fatalf("windows-only run did not materialize history and catalog: report=%+v catalog=%d", report, len(sink.catalogMPE))
	}
}

func TestGPArchiveCancellationSavesStoppedReport(t *testing.T) {
	root := t.TempDir()
	zipPath := filepath.Join(root, "archive.zip")
	writeGPArchiveTestZip(t, zipPath, gpArchiveTestHeader)
	opts := gpArchiveTestOptions(t, root, zipPath, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := importGPArchive(ctx, opts, &gpArchiveMemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "stopped" {
		t.Fatalf("status = %q, want stopped", report.Status)
	}
	if _, err := os.Stat(opts.CheckpointPath); err != nil {
		t.Fatalf("checkpoint not saved on cancellation: %v", err)
	}
	b, err := os.ReadFile(opts.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"status": "stopped"`)) {
		t.Fatalf("report does not contain stopped status: %s", b)
	}
}

func TestGPArchiveRunLockRejectsLiveHolderAndReplacesStalePID(t *testing.T) {
	out := t.TempDir()
	locked, release, err := acquireGPArchiveRunLock(out)
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("first lock was not acquired")
	}
	lockedAgain, _, err := acquireGPArchiveRunLock(out)
	if err != nil {
		t.Fatal(err)
	}
	if lockedAgain {
		t.Fatal("second acquisition accepted a live holder")
	}
	release()

	lockPath := filepath.Join(out, "gp-archive-import.lock")
	if err := os.WriteFile(lockPath, []byte("999999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked, release, err = acquireGPArchiveRunLock(out)
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("stale PID lock was not replaced")
	}
	release()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock remained after release: %v", err)
	}
}

func TestGPArchiveGPIDOverlapAndIncrementalWindows(t *testing.T) {
	root := t.TempDir()
	windowDir := filepath.Join(root, "spacetrack", "gp_history", "by-creation", "2022")
	if err := os.MkdirAll(windowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	w1Path := filepath.Join(windowDir, "2022-11-20.json.gz")
	w2Path := filepath.Join(windowDir, "2022-11-21.json.gz")
	record1 := gpArchiveTestJSONRecord("100", "1958-002B", "5")
	record2 := gpArchiveTestJSONRecord("101", "1958-002C", "6")
	hash1 := writeGPArchiveTestGzip(t, w1Path, []byte("["+record1+"]"))
	hash2 := writeGPArchiveTestGzip(t, w2Path, []byte("["+record1+","+record2+"]"))
	ledgerPath := filepath.Join(root, "state", "spacetrack-ledger.json")
	writeGPArchiveTestLedger(t, ledgerPath, []gpArchiveLedgerWindow{{
		CreationFrom: "2022-11-20T00:00:00Z", File: "spacetrack/gp_history/by-creation/2022/2022-11-20.json.gz", SHA256: hash1, RetrievedAt: "2026-09-26T13:00:32Z",
	}})
	opts := gpArchiveTestOptions(t, root, "", ledgerPath)

	first, err := importGPArchive(context.Background(), opts, &gpArchiveMemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if first.MPEStored != 1 || first.WindowFilesImported != 1 {
		t.Fatalf("first run = %+v", first)
	}
	second, err := importGPArchive(context.Background(), opts, &gpArchiveMemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if second.RecordsRead != 0 || second.MPEStored != 0 || second.CompletedWindowFilesSkipped != 1 {
		t.Fatalf("incremental no-op run = %+v", second)
	}

	writeGPArchiveTestLedger(t, ledgerPath, []gpArchiveLedgerWindow{
		{CreationFrom: "2022-11-20T00:00:00Z", File: "spacetrack/gp_history/by-creation/2022/2022-11-20.json.gz", SHA256: hash1, RetrievedAt: "2026-09-26T13:00:32Z"},
		{CreationFrom: "2022-11-21T00:00:00Z", File: "spacetrack/gp_history/by-creation/2022/2022-11-21.json.gz", SHA256: hash2, RetrievedAt: "2026-09-26T13:02:01Z"},
	})
	thirdSink := &gpArchiveMemorySink{}
	third, err := importGPArchive(context.Background(), opts, thirdSink)
	if err != nil {
		t.Fatal(err)
	}
	if third.RecordsRead != 2 || third.MPEStored != 1 || third.DuplicatesSkipped != 1 || third.WindowFilesImported != 1 {
		t.Fatalf("incremental new-window run = %+v", third)
	}
	if len(thirdSink.tags) == 0 || thirdSink.tags[0].ProviderID != "space-track" || thirdSink.tags[0].SourceURL == "" {
		t.Fatalf("source tags missing: %+v", thirdSink.tags)
	}
	if len(thirdSink.catalogMPE) != 1 {
		t.Fatalf("new window catalog writes=%d, want only changed entity", len(thirdSink.catalogMPE))
	}
}

func TestGPArchiveCurrentGPSnapshotDedupesWindowAndIsIncremental(t *testing.T) {
	root := t.TempDir()
	windowPath := filepath.Join(root, "spacetrack", "gp_history", "by-creation", "2026", "window.json.gz")
	gpPath := filepath.Join(root, "spacetrack", "gp", "2026", "2026-09-26.json.gz")
	if err := os.MkdirAll(filepath.Dir(windowPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(gpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	record := gpArchiveTestJSONRecord("9001", "1958-002B", "5")
	windowHash := writeGPArchiveTestGzip(t, windowPath, []byte("["+record+"]"))
	gpHash := writeGPArchiveTestGzip(t, gpPath, []byte("["+record+"]"))
	ledgerPath := filepath.Join(root, "state", "spacetrack-ledger.json")
	writeGPArchiveTestLedger(t, ledgerPath, []gpArchiveLedgerWindow{{File: "spacetrack/gp_history/by-creation/2026/window.json.gz", SHA256: windowHash, RetrievedAt: "2026-09-26T15:20:00Z"}})
	setGPArchiveTestLedgerSnapshots(t, ledgerPath, []gpArchiveLedgerSATCAT{{Date: "2026-09-26", File: "spacetrack/gp/2026/2026-09-26.json.gz", SHA256: gpHash, RetrievedAt: "2026-09-26T15:22:00Z"}})
	opts := gpArchiveTestOptions(t, root, "", ledgerPath)

	first, err := importGPArchive(context.Background(), opts, &gpArchiveMemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if first.RecordsRead != 2 || first.MPEStored != 1 || first.DuplicatesSkipped != 1 || first.GPFilesImported != 1 {
		t.Fatalf("window/snapshot overlap report = %+v", first)
	}
	foundGPSource := false
	for _, src := range first.Sources {
		if src.Kind == "gp" && src.SHA256 == gpHash && src.RowsRead == 1 && src.RowsStored == 0 {
			foundGPSource = true
		}
	}
	if !foundGPSource {
		t.Fatalf("GP snapshot lineage missing: %+v", first.Sources)
	}

	second, err := importGPArchive(context.Background(), opts, &gpArchiveMemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if second.RecordsRead != 0 || second.CompletedGPFilesSkipped != 1 {
		t.Fatalf("incremental snapshot run = %+v", second)
	}
}

func TestGPArchiveCurrentGPSnapshotHashMismatchIsSkipped(t *testing.T) {
	root := t.TempDir()
	gpPath := filepath.Join(root, "spacetrack", "gp", "2026", "2026-09-26.json.gz")
	if err := os.MkdirAll(filepath.Dir(gpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGPArchiveTestGzip(t, gpPath, []byte("["+gpArchiveTestJSONRecord("9001", "1958-002B", "5")+"]"))
	ledgerPath := filepath.Join(root, "state", "spacetrack-ledger.json")
	writeGPArchiveTestLedger(t, ledgerPath, nil)
	setGPArchiveTestLedgerSnapshots(t, ledgerPath, []gpArchiveLedgerSATCAT{{File: "spacetrack/gp/2026/2026-09-26.json.gz", SHA256: strings.Repeat("0", 64), RetrievedAt: "2026-09-26T15:22:00Z"}})
	opts := gpArchiveTestOptions(t, root, "", ledgerPath)
	report, err := importGPArchive(context.Background(), opts, &gpArchiveMemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.HashMismatches) != 1 || report.GPFilesImported != 0 || report.MPEStored != 0 {
		t.Fatalf("hash mismatch snapshot report = %+v", report)
	}
}

func TestGPArchiveHashMismatchSkipsWindow(t *testing.T) {
	root := t.TempDir()
	windowDir := filepath.Join(root, "spacetrack", "gp_history", "by-creation", "2022")
	if err := os.MkdirAll(windowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	windowPath := filepath.Join(windowDir, "bad.json.gz")
	writeGPArchiveTestGzip(t, windowPath, []byte("["+gpArchiveTestJSONRecord("200", "2000-001A", "99")+"]"))
	ledgerPath := filepath.Join(root, "state", "spacetrack-ledger.json")
	writeGPArchiveTestLedger(t, ledgerPath, []gpArchiveLedgerWindow{{
		CreationFrom: "2022-11-20T00:00:00Z", File: "spacetrack/gp_history/by-creation/2022/bad.json.gz", SHA256: string(make([]byte, 64)),
	}})
	sink := &gpArchiveMemorySink{}
	report, err := importGPArchive(context.Background(), gpArchiveTestOptions(t, root, "", ledgerPath), sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.HashMismatches) != 1 || report.MPEStored != 0 || len(sink.mpe) != 0 {
		t.Fatalf("hash mismatch report = %+v, sink records=%d", report, len(sink.mpe))
	}
}

func TestGPArchiveEpochStateRealWASM(t *testing.T) {
	path := os.Getenv("SDN_EPOCH_STATE_WASM")
	if path == "" {
		t.Skip("SDN_EPOCH_STATE_WASM is unset")
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := modulert.NewModule(wasm, modulert.NewCapabilityRegistry(), &modulert.NodeContext{})
	if err != nil {
		t.Fatal(err)
	}
	d := &gpWASMEpochDeriver{module: mod}
	defer d.Close()
	var cases []struct {
		Satnum       uint32     `json:"satnum"`
		EntityID     string     `json:"entityId"`
		Epoch        float64    `json:"epochUnix"`
		MeanMotion   float64    `json:"MEAN_MOTION"`
		Eccentricity float64    `json:"ECCENTRICITY"`
		Inclination  float64    `json:"INCLINATION"`
		RAAN         float64    `json:"RA_OF_ASC_NODE"`
		Arg          float64    `json:"ARG_OF_PERICENTER"`
		Anomaly      float64    `json:"MEAN_ANOMALY"`
		BSTAR        float64    `json:"BSTAR"`
		R, V         [3]float64 `json:"-"`
		GCRFR        [3]float64 `json:"gcrfR"`
		GCRFV        [3]float64 `json:"gcrfV"`
	}
	b, err := os.ReadFile("testdata/gp_archive/vallado_epoch_state.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	inputs := make([][]byte, 100)
	for i := range inputs {
		c := cases[i%len(cases)]
		inputs[i] = buildGPArchiveMPE(c.EntityID, gpArchiveRecord{EntityID: c.EntityID, NORAD: c.Satnum, Epoch: c.Epoch, MeanMotion: c.MeanMotion, Eccentricity: c.Eccentricity, Inclination: c.Inclination, RAOfAscNode: c.RAAN, ArgOfPericenter: c.Arg, MeanAnomaly: c.Anomaly, BSTAR: c.BSTAR})
	}
	outputs, report, err := d.Derive(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 0 || report.Derived != 100 || len(outputs) != 100 {
		t.Fatalf("derive report=%+v outputs=%d", report, len(outputs))
	}
	sink := &gpArchiveMemorySink{}
	for i, out := range outputs {
		c := cases[i%len(cases)]
		if _, err = sink.StoreOEMBatch([]gpOEMWrite{{Record: out}}); err != nil {
			t.Fatal(err)
		}
		r, v := gpArchiveOEMState(t, sink.oem[i])
		for axis := 0; axis < 3; axis++ {
			if math.Abs(r[axis]-c.GCRFR[axis]) > 5e-8 {
				t.Errorf("case %d r[%d]=%.12g want %.12g", i, axis, r[axis], c.GCRFR[axis])
			}
			if math.Abs(v[axis]-c.GCRFV[axis]) > 5e-9 {
				t.Errorf("case %d v[%d]=%.12g want %.12g", i, axis, v[axis], c.GCRFV[axis])
			}
		}
	}
}

func gpArchiveOEMState(t *testing.T, data []byte) ([3]float64, [3]float64) {
	t.Helper()
	root := OEMFB.GetSizePrefixedRootAsOEM(data, 0)
	ot := root.Table()
	o := flatbuffers.UOffsetT(ot.Offset(12))
	if o == 0 {
		t.Fatal("OEM has no data block")
	}
	bp := ot.Indirect(ot.Vector(o))
	bt := flatbuffers.Table{Bytes: data, Pos: bp}
	o = flatbuffers.UOffsetT(bt.Offset(36))
	if o == 0 {
		t.Fatal("OEM has no data line")
	}
	lp := bt.Indirect(bt.Vector(o))
	lt := flatbuffers.Table{Bytes: data, Pos: lp}
	read := func(v flatbuffers.VOffsetT) float64 {
		x := flatbuffers.UOffsetT(lt.Offset(v))
		if x == 0 {
			return 0
		}
		return lt.GetFloat64(x + lt.Pos)
	}
	return [3]float64{read(6), read(8), read(10)}, [3]float64{read(12), read(14), read(16)}
}

func gpArchiveTestColumns() map[string]int {
	names := []string{"OBJECT_NAME", "OBJECT_ID", "EPOCH", "MEAN_MOTION", "ECCENTRICITY", "INCLINATION", "RA_OF_ASC_NODE", "ARG_OF_PERICENTER", "MEAN_ANOMALY", "EPHEMERIS_TYPE", "CLASSIFICATION_TYPE", "NORAD_CAT_ID", "ELEMENT_SET_NO", "REV_AT_EPOCH", "BSTAR", "MEAN_MOTION_DOT", "MEAN_MOTION_DDOT", "DATA_SOURCE"}
	columns := make(map[string]int, len(names))
	for i, name := range names {
		columns[name] = i
	}
	return columns
}

func gpArchiveCatalogMPEByEntity(t *testing.T, records [][]byte) map[string]string {
	t.Helper()
	out := make(map[string]string, len(records))
	for _, record := range records {
		mpe := MPEFB.GetSizePrefixedRootAsMPE(record, 0)
		entity := string(mpe.ENTITY_ID())
		if entity == "" {
			t.Fatal("catalog MPE has empty entity")
		}
		out[entity] = storage.ComputeCID(record)
	}
	return out
}

func gpArchiveTestCheckpoint() *gpArchiveCheckpoint {
	return &gpArchiveCheckpoint{
		Version:              2,
		CompletedZipEntries:  make(map[string]string),
		CompletedWindowFiles: make(map[string]string),
		CompletedGPFiles:     make(map[string]string),
		SnapshotGPIDs:        make(map[string]bool),
		FoldedShardMTime:     make(map[string]int64),
		CanonicalIDs:         make(map[string]string), Latest: make(map[string]*gpLatestState), CATNames: make(map[string]string),
	}
}

func gpArchiveTestOptions(t *testing.T, root, zipPath, ledgerPath string) gpArchiveOptions {
	t.Helper()
	if ledgerPath == "" {
		ledgerPath = filepath.Join(root, "state", "spacetrack-ledger.json")
		writeGPArchiveTestLedger(t, ledgerPath, nil)
	}
	satcatPath := filepath.Join(root, "spacetrack", "satcat", "2026", "2026-09-26.json.gz")
	if err := os.MkdirAll(filepath.Dir(satcatPath), 0755); err != nil {
		t.Fatal(err)
	}
	satcatRaw := []byte(`[{"NORAD_CAT_ID":"5","INTLDES":"1958-002B","OBJECT_NAME":"VANGUARD 1"},{"NORAD_CAT_ID":"6","INTLDES":"1958-002C","OBJECT_NAME":"TEST 6"},{"NORAD_CAT_ID":"99","INTLDES":"2000-001A","OBJECT_NAME":"TEST 99"}]`)
	satHash := writeGPArchiveTestGzip(t, satcatPath, satcatRaw)
	b, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var ledger gpArchiveLedger
	if err = json.Unmarshal(b, &ledger); err != nil {
		t.Fatal(err)
	}
	ledger.SATCAT = []gpArchiveLedgerSATCAT{{Date: "2026-09-26", File: "spacetrack/satcat/2026/2026-09-26.json.gz", SHA256: satHash, RetrievedAt: "2026-09-26T14:08:00Z"}}
	payload, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(ledgerPath, payload, 0644); err != nil {
		t.Fatal(err)
	}
	return gpArchiveOptions{
		Out:            filepath.Join(root, "out"),
		ZipPath:        zipPath,
		WindowsRoot:    filepath.Join(root, "spacetrack", "gp_history", "by-creation"),
		LedgerPath:     ledgerPath,
		SATCATPath:     satcatPath,
		CheckpointPath: filepath.Join(root, "checkpoint.json"),
		ReportPath:     filepath.Join(root, "report.json"),
		BatchSize:      2,
	}
}

func writeGPArchiveTestZip(t *testing.T, path, csvData string) {
	writeGPArchiveTestZipEntries(t, path, []string{csvData})
}

func writeGPArchiveTestZipEntries(t *testing.T, path string, csvData []string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	for i, data := range csvData {
		entry, createErr := zw.Create(fmt.Sprintf("Archives2/sat%09d.csv", i+5))
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write([]byte(data)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeGPArchiveTestGzip(t *testing.T, path string, uncompressed []byte) string {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(file)
	if _, err := zw.Write(uncompressed); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(uncompressed)
	return hex.EncodeToString(sum[:])
}

func writeGPArchiveTestLedger(t *testing.T, path string, windows []gpArchiveLedgerWindow) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := gpArchiveLedger{Windows: windows}
	if existing, readErr := os.ReadFile(path); readErr == nil {
		var old gpArchiveLedger
		if json.Unmarshal(existing, &old) == nil {
			ledger.SATCAT = old.SATCAT
			ledger.GP = old.GP
		}
	}
	payload, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
}

func setGPArchiveTestLedgerSnapshots(t *testing.T, path string, snapshots []gpArchiveLedgerSATCAT) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ledger gpArchiveLedger
	if err = json.Unmarshal(b, &ledger); err != nil {
		t.Fatal(err)
	}
	ledger.GP = snapshots
	b, err = json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func gpArchiveTestJSONRecord(gpid, objectID, norad string) string {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"GP_ID": gpid, "OBJECT_ID": objectID, "OBJECT_NAME": "TEST SAT", "NORAD_CAT_ID": norad,
		"EPOCH": "2022-11-19T22:11:05.523072", "MEAN_MOTION": "1.00271749", "ECCENTRICITY": "0.00026300",
		"INCLINATION": "0.0239", "RA_OF_ASC_NODE": "32.4905", "ARG_OF_PERICENTER": "268.1503",
		"MEAN_ANOMALY": "189.1205", "BSTAR": "0",
	})
	return string(bytes.TrimSpace(buf.Bytes()))
}
