package main

// store-migrate --to 4 (store_migrate_format4.go) against real format-1
// stores, read through the format-1 engine.
//
// The target is format4test's Fake until the format-4 engine lands: one Fake
// per data root, shared by every open of that root (as an engine's files
// are), so a run that stops at any step and a rerun see one durable store.
// The engine runs:
//   - TestStoreMigrateFormat4Kill9: kill -9 of a real migration at random
//     points (skipped until format4.Open opens the engine);
//   - TestStoreMigrateFormat4Fixture: the host-02-sized fixture
//     (SDN_F1_FIXTURE=<format-1 store dir>; SDN_MIGRATE4_TARGET=engine, the
//     default, or null to time the format-1 side alone).

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/format4test"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// ---- the target -----------------------------------------------------------------------

// fakeEngines opens format4test's Fake as the format-4 engine. The Fake
// continues a data root's store across opens in this process (as an engine's
// files do), so a run that stops at any step and a rerun see one durable
// store. Until the Fake has them, this double also applies two contract
// rules: C-20 (a migrate PUT supersedes nothing: CAT's supersede identity is
// left out of the Fake's extraction) and C-22 (a torn STORE beside a valid
// MIGRATED reopens as a migration target).
type fakeEngines struct {
	wrap func(format4.API) format4.API
}

func newFakeEngines() *fakeEngines { return &fakeEngines{} }

func (d *fakeEngines) open(ctx context.Context, opt format4.Options) (format4.API, error) {
	f, err := openFake4(ctx, opt)
	if err != nil {
		return nil, err
	}
	var api format4.API = f
	if d.wrap != nil {
		api = d.wrap(api)
	}
	return api, nil
}

// store reopens root's store (after a run closed it) for the test to read.
func (d *fakeEngines) store(root string) format4.API {
	f, err := openFake4(context.Background(), format4.Options{DataRoot: root, Create: format4.OpenExisting})
	if err != nil {
		panic(fmt.Sprintf("reopen the Fake at %s: %v", root, err))
	}
	return f
}

func openFake4(ctx context.Context, opt format4.Options) (*format4test.Fake, error) {
	// A test's earlier reopen for reading is closed first (one opener a
	// root).
	if held, ok := readers.LoadAndDelete(filepath.Clean(opt.DataRoot)); ok {
		_ = held.(*format4test.Fake).Close(ctx)
	}
	if opt.Create == format4.CreateForMigration {
		if mk, err := marker.Read(opt.DataRoot); err == nil && mk.MigratedValid && mk.StorePresent && !mk.StoreValid {
			torn := filepath.Join(opt.DataRoot, marker.Dir, marker.StoreFile)
			if err := os.Rename(torn, torn+".torn"); err != nil {
				return nil, err
			}
			defer os.Rename(torn+".torn", torn)
		}
	}
	f, err := format4test.Open(ctx, opt)
	if err != nil {
		return nil, err
	}
	f.Extract = migrateExtract
	if opt.Create == format4.OpenExisting {
		readers.Store(filepath.Clean(opt.DataRoot), f)
	}
	return f, nil
}

var readers sync.Map // data root -> the Fake a test reopened to read

// migrateExtract is the Fake's extraction without CAT's supersede identity
// (C-20, until the Fake skips supersede in migrate mode).
func migrateExtract(typ string, plain []byte) (format4test.Fields, error) {
	f, err := format4test.DefaultExtract(typ, plain)
	f.Supersede = ""
	return f, err
}

func migrate4TestOptions(store string, engines *fakeEngines) migrate4Options {
	return migrate4Options{Store: store, PageRows: 64, open: engines.open, testJournalEvery: time.Nanosecond}
}

// ---- legacy stores --------------------------------------------------------------------

const migrate4IQCSource = "IQEngine"

// migrate4TestIQC builds one $IQC record as the sigmf parser does (storage's
// record_ingest_identity_test.go): its fetch stamps are its ingest identity's
// masked fields.
func migrate4TestIQC(seq int, stamp string) []byte {
	b := flatbuffers.NewBuilder(512)
	id := b.CreateString(fmt.Sprintf("iqengine:local/local/capture-%06d", seq))
	capture := b.CreateString(fmt.Sprintf("capture-%06d", seq))
	source := b.CreateString(migrate4IQCSource)
	retrieved := b.CreateString(stamp)
	desc := b.CreateString(fmt.Sprintf("SigMF capture %d", seq))
	created := b.CreateString(stamp)
	IQC.IQCStart(b)
	IQC.IQCAddID(b, id)
	IQC.IQCAddCAPTURE_ID(b, capture)
	IQC.IQCAddSOURCE_NAME(b, source)
	IQC.IQCAddRETRIEVED_AT(b, retrieved)
	IQC.IQCAddDESCRIPTION(b, desc)
	IQC.IQCAddSAMPLE_RATE_HZ(b, 2.4e6)
	IQC.IQCAddCENTER_FREQ_HZ(b, 1.0e8+float64(seq)*1e3)
	IQC.IQCAddCREATED_AT(b, created)
	IQC.IQCAddUPDATED_AT(b, created)
	root := IQC.IQCEnd(b)
	b.FinishWithFileIdentifier(root, []byte(IQC.IQCIdentifier))
	return append([]byte(nil), b.FinishedBytes()...)
}

// buildLegacyStore4 is buildLegacyStore (two OMM producers sharing CIDs,
// records with several tags, CAT editions that supersede, a licence) plus
// what format 4 carries beyond it: signed records, IQC records with ingest
// identities held by two producers, a tag with a source URL and a content key.
func buildLegacyStore4(t *testing.T, dir string) {
	t.Helper()
	buildLegacyStore(t, dir)
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(dir, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	signed := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-supgp", BatchID: "sup-1",
		SourceURL: "https://celestrak.org/NORAD/elements/supplemental/sup-gp.php?FILE=starlink", ContentKeyID: "public"}
	for i := 0; i < 5; i++ {
		rec := migrateTestOMM(uint32(30000+i), base.Add(time.Duration(i)*time.Hour), fmt.Sprintf("SIGNED-%d", i))
		if _, err := s.StoreWithSourceTags("OMM.fbs", rec, "source:celestrak", []byte{0xde, 0xad, byte(i)}, signed); err != nil {
			t.Fatal(err)
		}
	}
	var iqc [][]byte
	for i := 0; i < 70; i++ {
		iqc = append(iqc, migrate4TestIQC(i, "2026-09-15T02:11:22Z"))
	}
	lane := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: migrate4IQCSource, BatchID: "iq-1", ContentKeyID: "public"}
	if _, err := s.StoreBatchWithSourceTags("IQC.fbs", iqc, "source:sigmf", nil, lane); err != nil {
		t.Fatal(err)
	}
	// A second producer holds 20 of them (copies) and is untagged.
	if _, err := s.StoreBatch("IQC.fbs", iqc[50:], "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// cloneStore copies a store directory (APFS clones on macOS).
func cloneStore(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "store")
	cp := exec.Command("cp", "-cR", src, dst)
	if runtime.GOOS != "darwin" {
		cp = exec.Command("cp", "-R", "--reflink=auto", src, dst)
	}
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatalf("clone %s: %v: %s", src, err, out)
	}
	return dst
}

// ---- the oracle: format 4 equals format 1 -----------------------------------------------

// assertFormat4EqualsFormat1 reads the format-1 store at legacy through the
// format-1 engine (with format 2's readers, not the migrator's) and the
// format-4 store through api, and compares every
// held record copy (seq = index rowid, bytes, ts, signature, peer,
// producer), every tag (identity, source_url, content key, at), and every
// counter (partitions against a recount of each table, lanes against
// format 1's own lane recount, types). slice > 0 compares the records and
// tags of each schema's first slice held rows only (the counters always
// cover everything).
func assertFormat4EqualsFormat1(t *testing.T, api format4.API, legacy string, slice int) {
	t.Helper()
	ctx := context.Background()
	src, err := storage.OpenMigrationSource(legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	tables, err := src.ProducerTables()
	if err != nil {
		t.Fatal(err)
	}
	bySchema := map[string][]storage.LegacyTable{}
	var schemas []string
	for _, tb := range tables {
		if bySchema[tb.Schema] == nil {
			schemas = append(schemas, tb.Schema)
		}
		bySchema[tb.Schema] = append(bySchema[tb.Schema], tb)
	}
	types, err := api.Types(ctx)
	if err != nil {
		t.Fatal(err)
	}
	typeSummary := map[string]format4.TypeSummary{}
	for _, ts := range types {
		typeSummary[ts.Type] = ts
	}
	for _, schema := range schemas {
		typ, _ := format4.TypeOf(schema)
		// format2_source.go's readers: independent of the migrator's.
		limit := 1 << 30
		if slice > 0 {
			limit = slice
		}
		entries, err := src.HeldIndexPage(schema, bySchema[schema], 0, limit)
		if err != nil {
			t.Fatal(err)
		}
		cids := make([]string, len(entries))
		for i, e := range entries {
			cids[i] = e.CID
		}
		perTable := make([]map[string]storage.LegacyRecord, len(bySchema[schema]))
		for i, tb := range bySchema[schema] {
			if perTable[i], err = src.RecordsByCID(tb, cids); err != nil {
				t.Fatal(err)
			}
		}
		recs, err := api.Get(ctx, typ, cids, true, true)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]map[string]format4.Rec{} // cid -> producer -> copy
		for _, r := range recs {
			if got[r.CID] == nil {
				got[r.CID] = map[string]format4.Rec{}
			}
			got[r.CID][r.Producer] = r
		}
		var copies int64
		for _, e := range entries {
			var first *storage.LegacyRecord
			for i, tb := range bySchema[schema] {
				lr, ok := perTable[i][e.CID]
				if !ok {
					continue
				}
				copies++
				if first == nil {
					first = &lr
				}
				r, ok := got[e.CID][tb.Token]
				if !ok {
					t.Fatalf("%s %s: format 4 has no copy in partition %s", schema, e.CID, tb.Token)
				}
				wantPeer := lr.PeerID
				if wantPeer == "" {
					wantPeer = tb.Token
				}
				if r.Seq != e.RowID || r.Peer != wantPeer || !bytes.Equal(r.Sig, legacySignature(lr.SignatureHex)) ||
					r.TS != first.Timestamp || !bytes.Equal(r.Data, first.Stored) || r.Len != int64(len(first.Stored)) {
					t.Fatalf("%s %s copy %s: format 4 seq %d peer %q sig %x ts %d len %d; format 1 rowid %d peer %q sig %q ts %d len %d",
						schema, e.CID, tb.Token, r.Seq, r.Peer, r.Sig, r.TS, r.Len, e.RowID, lr.PeerID, lr.SignatureHex,
						first.Timestamp, len(first.Stored))
				}
			}
			if len(got[e.CID]) != countCopies(perTable, e.CID) {
				t.Fatalf("%s %s: %d copies in format 4, %d in format 1", schema, e.CID, len(got[e.CID]), countCopies(perTable, e.CID))
			}
		}
		if int64(len(recs)) != copies {
			t.Fatalf("%s: %d copies in format 4, %d in format 1", schema, len(recs), copies)
		}
		var typeCopies int64 // every copy of the schema, sliced or not
		for _, tb := range bySchema[schema] {
			c, err := src.TableCounter(tb)
			if err != nil {
				t.Fatal(err)
			}
			typeCopies += c.Count
		}
		// Tags: one row per (cid, identity) on both sides.
		want, err := src.TagsFor(schema, cids)
		if err != nil {
			t.Fatal(err)
		}
		gotTags, err := api.Tags(ctx, typ, cids)
		if err != nil {
			t.Fatal(err)
		}
		var wantRows, gotRows []string
		for cid, tags := range want {
			for _, lt := range tags {
				wantRows = append(wantRows, fmt.Sprintf("%s|%v|%d", cid, formatTag(lt), lt.CreatedAt))
			}
		}
		for _, tr := range gotTags {
			gotRows = append(gotRows, fmt.Sprintf("%s|%v|%d", tr.CID, tr.Tag, tr.At))
		}
		sort.Strings(wantRows)
		sort.Strings(gotRows)
		if strings.Join(wantRows, "\n") != strings.Join(gotRows, "\n") {
			t.Fatalf("%s tags differ:\nformat 1 %d rows\n%s\nformat 4 %d rows\n%s", schema, len(wantRows),
				strings.Join(wantRows, "\n"), len(gotRows), strings.Join(gotRows, "\n"))
		}
		ts := typeSummary[typ]
		if ts.Copies != typeCopies || (slice == 0 && (ts.Records != int64(len(entries)) ||
			(len(entries) > 0 && ts.MaxSeq != entries[len(entries)-1].RowID))) {
			t.Fatalf("type %s: %+v; format 1 holds %d records (slice %d), %d copies", typ, ts, len(entries), slice, typeCopies)
		}
		// Lanes: summed over partitions and content keys, format 1's recount.
		wantLanes, err := src.LaneRecount(schema, bySchema[schema])
		if err != nil {
			t.Fatal(err)
		}
		lanes, err := api.Lanes(ctx, typ)
		if err != nil {
			t.Fatal(err)
		}
		sum := map[string][2]int64{}
		for _, l := range lanes {
			k := strings.Join([]string{l.Provider, l.Source, l.Batch, l.ProducerPeer, l.ProducerPubkey}, "|")
			v := sum[k]
			sum[k] = [2]int64{v[0] + l.Records, v[1] + l.Bytes}
		}
		if len(sum) != len(wantLanes) {
			t.Fatalf("%s: %d lanes in format 4, %d in format 1's recount (%v / %v)", schema, len(sum), len(wantLanes), sum, wantLanes)
		}
		for _, w := range wantLanes {
			k := strings.Join([]string{w.ProviderID, w.SourceName, w.BatchID, w.ProducerPeerID, w.ProducerPublicKey}, "|")
			if g := sum[k]; g != [2]int64{w.Count, w.Bytes} {
				t.Fatalf("%s lane %s: format 4 %v, format 1 recount (%d, %d)", schema, k, g, w.Count, w.Bytes)
			}
		}
	}
	// Partitions: each table, recounted.
	parts, err := api.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotParts := map[string]format4.PartitionSummary{}
	for _, p := range parts {
		gotParts[p.Type+"/"+p.Producer] = p
	}
	if len(gotParts) != len(tables) {
		t.Fatalf("%d partitions in format 4, %d tables in format 1", len(gotParts), len(tables))
	}
	for _, tb := range tables {
		typ, _ := format4.TypeOf(tb.Schema)
		c, err := src.TableCounter(tb)
		if err != nil {
			t.Fatal(err)
		}
		if p := gotParts[typ+"/"+tb.Token]; p.Records != c.Count || p.Bytes != c.Bytes {
			t.Fatalf("partition %s: format 4 (%d, %d B), format 1 table (%d, %d B)", tb.Name, p.Records, p.Bytes, c.Count, c.Bytes)
		}
	}
}

func countCopies(perTable []map[string]storage.LegacyRecord, cid string) int {
	n := 0
	for _, m := range perTable {
		if _, ok := m[cid]; ok {
			n++
		}
	}
	return n
}

// format4Dump is a format-4 store's logical content, for comparing runs:
// summaries, every copy, every tag, the markers and the files activation
// leaves.
func format4Dump(t *testing.T, api format4.API, root string) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	types, err := api.Types(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := api.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		fmt.Fprintf(&b, "P %s %s %s %d %d %d %d %d\n", p.Type, p.Producer, p.Peer, p.Records, p.Bytes, p.MinTS, p.MaxTS, p.MaxSeq)
	}
	lanes, err := api.Lanes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lanes {
		fmt.Fprintf(&b, "L %s %s %v %d %d %d %d\n", l.Type, l.Producer, l.Tag, l.Records, l.Bytes, l.MaxSeq, l.First)
	}
	for _, ty := range types {
		fmt.Fprintf(&b, "T %s %d %d %d %d %d\n", ty.Type, ty.Records, ty.Copies, ty.Bytes, ty.CopyBytes, ty.MaxSeq)
		scan, err := api.Scan(ctx, format4.Query{Type: ty.Type})
		if err != nil {
			t.Fatal(err)
		}
		cids := make([]string, len(scan))
		for i, r := range scan {
			cids[i] = r.CID
		}
		recs, err := api.Get(ctx, ty.Type, cids, true, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			fmt.Fprintf(&b, "R %d %s %s %s %d %v %d %s %x %x\n", r.Seq, r.CID, r.Producer, r.Peer, r.TS, r.HasEpoch, r.Epoch, r.Key, r.Sig, r.Data)
		}
		tags, err := api.Tags(ctx, ty.Type, cids)
		if err != nil {
			t.Fatal(err)
		}
		for _, tg := range tags {
			fmt.Fprintf(&b, "G %s %s %v %d\n", tg.CID, tg.Producer, tg.Tag, tg.At)
		}
	}
	mk, err := marker.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "M activated %v floor %d from %d legacy-dir %v legacy-file %v\n", mk.Activated(), mk.GseqFloor, mk.MigratedFrom,
		mk.LegacyControlDir, mk.LegacyControlFile)
	for _, name := range []string{filepath.Join(marker.PreFormat4Dir, "control.flatsqldb"), filepath.Join(marker.Dir, "control.db"),
		migrate4ControlTmp, migrate4FTSTmp} {
		_, err := os.Stat(filepath.Join(root, name))
		fmt.Fprintf(&b, "F %s %v\n", name, err == nil)
	}
	return b.String()
}

// ---- tests ------------------------------------------------------------------------------

func TestStoreMigrateFormat4TargetFlag(t *testing.T) {
	for in, want := range map[string]int{"": 2, "2": 2, " 4 ": 4, "sqlite": 4, "SQLite": 4} {
		if got, err := migrateTargetFormat(in); err != nil || got != want {
			t.Fatalf("--to %q: %d %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"1", "3", "5", "sqlite3"} {
		if _, err := migrateTargetFormat(in); err == nil {
			t.Fatalf("--to %q accepted", in)
		}
	}
}

// The CLI: format-2-only options and --verify-only with --inventory are
// refused with --to 4; --verify-only without --to 4 is refused.
func TestStoreMigrateFormat4CommandLine(t *testing.T) {
	reset := func() {
		storeMigrateOut, storeMigrateSnapshot, storeMigrateDelta, storeMigrateNoActive = "", "", false, false
		storeMigrateInventory, storeMigrateVerifyOnly, storeMigrateTo, storeMigrateStore = false, false, "", ""
	}
	defer reset()
	for name, set := range map[string]func(){
		"--out":           func() { storeMigrateOut = "/elsewhere" },
		"--from-snapshot": func() { storeMigrateSnapshot = "/snap" },
		"--delta":         func() { storeMigrateDelta = true },
		"--no-activate":   func() { storeMigrateNoActive = true },
		"--inventory and --verify-only": func() {
			storeMigrateInventory, storeMigrateVerifyOnly = true, true
		},
	} {
		reset()
		storeMigrateTo, storeMigrateStore = "sqlite", t.TempDir()
		set()
		if err := runStoreMigrate(storeMigrateCmd, nil); err == nil {
			t.Fatalf("--to sqlite with %s was accepted", name)
		}
	}
	reset()
	storeMigrateVerifyOnly, storeMigrateStore = true, t.TempDir()
	if err := runStoreMigrate(storeMigrateCmd, nil); err == nil || !strings.Contains(err.Error(), "--to 4") {
		t.Fatalf("--verify-only without --to 4: %v", err)
	}
}

// The migrated store equals format 1 on every record copy, tag and counter;
// it is activated; a rerun is a no-op; --verify-only passes; a format-1 open
// of the activated store fails.
func TestStoreMigrateFormat4EqualsFormat1(t *testing.T) {
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	pristine := cloneStore(t, legacy)
	engines := newFakeEngines()
	var mu sync.Mutex
	var puts []format4.Batch
	engines.wrap = func(api format4.API) format4.API { return &recordingAPI{API: api, mu: &mu, puts: &puts} }
	var log bytes.Buffer
	rep, err := migrateStore4(context.Background(), migrate4TestOptions(legacy, engines), &log)
	if err != nil {
		t.Fatalf("migrate: %v\n%s\n%+v\ncheck %+v", err, log.String(), rep, rep.Check)
	}
	c := rep.Check
	// 300 OMM + 5 signed OMM + 40 CAT + 70 IQC records; 100 OMM and 20 IQC
	// held by a second producer.
	if c == nil || c.MismatchCount != 0 || c.Records != 415 || c.Copies != 535 || rep.Records != 415 || rep.Copies != 535 {
		t.Fatalf("check %+v; report records %d copies %d", c, rep.Records, rep.Copies)
	}
	if c.Rehashed != 535 || c.FieldChecks != 415 || c.ColumnSamples == 0 || c.TagInstances != 450+5+40+70 || c.Partitions != 5 {
		t.Fatalf("check coverage %+v", c)
	}
	if !rep.Activated || len(rep.Rejected) != 0 || rep.MaxRSS <= 0 || rep.Took == "" {
		t.Fatalf("report %+v", rep)
	}
	assertFormat4EqualsFormat1(t, engines.store(legacy), pristine, 0)

	// Every PUT was migrate mode, seq = the index rowid; IQC records carried
	// format 1's ingest identity (on their tagged first copy).
	identities := 0
	for _, b := range puts {
		if b.Mode != format4.ModeMigrate {
			t.Fatalf("a %s PUT in mode %d", b.Type, b.Mode)
		}
		for _, in := range b.Records {
			if in.Seq <= 0 {
				t.Fatalf("%s %s without a seq", b.Type, in.CID)
			}
			if in.Ident != nil {
				if b.Type != "IQC" || len(in.Tags) == 0 {
					t.Fatalf("an identity on %s %s (tags %v)", b.Type, in.CID, in.Tags)
				}
				identities++
			}
		}
	}
	if identities != 70 {
		t.Fatalf("%d IQC records carried an identity, want 70", identities)
	}

	// Activation (contract §2.3).
	mk, err := marker.Read(legacy)
	if err != nil || !mk.Activated() || mk.MigratedFrom != 1 || mk.GseqFloor != rep.GseqFloor || !mk.LegacyControlDir {
		t.Fatalf("markers %+v %v", mk, err)
	}
	if _, err := os.Stat(filepath.Join(legacy, marker.PreFormat4Dir, "control.flatsqldb")); err != nil {
		t.Fatalf("format 1's control database is not in pre-format4/: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(legacy, marker.Dir, "control.db")); err != nil || fi.Size() == 0 {
		t.Fatalf("fsql4/control.db: %v", err)
	}
	for _, name := range []string{migrate4ControlTmp, migrate4FTSTmp} {
		if _, err := os.Stat(filepath.Join(legacy, name)); err == nil {
			t.Fatalf("%s left behind", name)
		}
	}
	t.Logf("migrated %d records (%d copies) in %s, max RSS %d B, load %v; notes %q; time %v",
		rep.Records, rep.Copies, rep.Took, rep.MaxRSS, rep.Load, rep.Notes, rep.Time)

	// A format-1 open of the activated store fails.
	sv, _ := sds.NewValidator(nil)
	if s, err := storage.NewFlatSQLStore(legacy, sv, storage.WithDeferredBootRebuilds()); err == nil {
		s.Close()
		t.Fatal("a format-1 open of the activated store succeeded")
	}
	// A rerun is a no-op.
	before := format4Dump(t, engines.store(legacy), legacy)
	rep2, err := migrateStore4(context.Background(), migrate4TestOptions(legacy, engines), nil)
	if err != nil || !rep2.Activated {
		t.Fatalf("rerun on an activated store: %v %+v", err, rep2)
	}
	if after := format4Dump(t, engines.store(legacy), legacy); after != before {
		t.Fatal("a rerun on an activated store changed it")
	}
	// --verify-only re-reads pre-format4/ and passes.
	vo := migrate4TestOptions(legacy, engines)
	vo.VerifyOnly = true
	rep3, err := migrateStore4(context.Background(), vo, nil)
	if err != nil || rep3.Check == nil || rep3.Check.MismatchCount != 0 || rep3.Check.Records != 415 || rep3.Records != 415 {
		t.Fatalf("--verify-only: %v %+v", err, rep3.Check)
	}
}

// recordingAPI records the PUTs it forwards.
type recordingAPI struct {
	format4.API
	mu   *sync.Mutex
	puts *[]format4.Batch
}

func (r *recordingAPI) Put(ctx context.Context, b format4.Batch) ([]format4.Outcome, error) {
	r.mu.Lock()
	*r.puts = append(*r.puts, b)
	r.mu.Unlock()
	return r.API.Put(ctx, b)
}

var errTestCrash = errors.New("test crash")

// A run that stops at any step (every PUT, page, journal write, the index
// build, the check, the control copy, the engine's activation) and a rerun
// leave the same store as an uninterrupted run. So does a torn STORE (the
// engine cut short inside its activation), and a crash inside the final
// file moves. Every step is stopped at in the patched-substrate CI lane
// (SDN_WASM_REQUIRE_PATCHED=1) or with SDN_MIGRATE4_EVERY_STEP=1; elsewhere
// the first stop of each kind, and the middle journal write.
func TestStoreMigrateFormat4ResumesAfterEveryStep(t *testing.T) {
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	ctx := context.Background()

	clean := cloneStore(t, legacy)
	engines := newFakeEngines()
	var steps []string
	opt := migrate4TestOptions(clean, engines)
	opt.testStep = func(step string) error {
		steps = append(steps, step)
		return nil
	}
	if _, err := migrateStore4(ctx, opt, nil); err != nil {
		t.Fatalf("clean migration: %v", err)
	}
	want := format4Dump(t, engines.store(clean), clean)
	t.Logf("a clean migration passes %d steps", len(steps))

	resume := func(name string, fail func(step string, n int) bool, between func(root string)) {
		t.Helper()
		root := cloneStore(t, legacy)
		engines := newFakeEngines()
		var mu sync.Mutex
		n := 0
		o := migrate4TestOptions(root, engines)
		o.testStep = func(step string) error {
			mu.Lock()
			defer mu.Unlock()
			n++
			if fail(step, n) {
				return errTestCrash
			}
			return nil
		}
		if _, err := migrateStore4(ctx, o, nil); !errors.Is(err, errTestCrash) {
			t.Fatalf("%s: the run did not stop: %v", name, err)
		}
		if between != nil {
			between(root)
		}
		rep, err := migrateStore4(ctx, migrate4TestOptions(root, engines), nil)
		if err != nil {
			t.Fatalf("%s: resume: %v\n%+v", name, err, rep.Check)
		}
		if !rep.Activated {
			t.Fatalf("%s: the resumed run did not activate", name)
		}
		if got := format4Dump(t, engines.store(root), root); got != want {
			t.Fatalf("%s: the resumed store differs from an uninterrupted migration:\n%s", name, firstDiff(want, got))
		}
	}
	picks := migrate4StepSample(steps)
	if os.Getenv("SDN_MIGRATE4_EVERY_STEP") == "1" || os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
		picks = picks[:0]
		for k := range steps {
			picks = append(picks, k)
		}
	}
	for _, k := range picks {
		k := k
		resume(fmt.Sprintf("stop at step %d (%s)", k+1, steps[k]), func(_ string, n int) bool { return n == k+1 }, nil)
	}
	t.Logf("stopped at %d of %d steps, then a torn STORE and cut-short file moves", len(picks), len(steps))
	// The engine's activation cut short: MIGRATED written, STORE torn.
	resume("torn STORE", func(step string, _ int) bool { return step == "control" }, func(root string) {
		writeTestMarker(t, root, marker.MigratedFile, testMigratedBytes([16]byte{1}, time.Now().UnixMilli()))
		writeTestMarker(t, root, marker.StoreFile, []byte("FSQ4"))
	})
	// The file moves cut short: control.flatsqldb in pre-format4/, no
	// directory in its place yet.
	resume("file moves cut short", func(step string, _ int) bool { return step == "activated" }, func(root string) {
		if err := os.MkdirAll(filepath.Join(root, marker.PreFormat4Dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "control.flatsqldb"), filepath.Join(root, marker.PreFormat4Dir, "control.flatsqldb")); err != nil {
			t.Fatal(err)
		}
	})
}

// migrate4StepSample is the first step of each kind ("put:<table>" is one
// kind) and the middle journal write (a copy resumed from its journal).
func migrate4StepSample(steps []string) []int {
	var out []int
	seen := map[string]bool{}
	var journals []int
	for k, step := range steps {
		kind, _, _ := strings.Cut(step, ":")
		if kind == "journal" {
			journals = append(journals, k)
		}
		if !seen[kind] {
			seen[kind] = true
			out = append(out, k)
		}
	}
	if len(journals) > 2 {
		out = append(out, journals[len(journals)/2])
	}
	sort.Ints(out)
	return out
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d:\nwant %s\ngot  %s", i+1, x, y)
		}
	}
	return ""
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// testMigratedBytes is the contract's MIGRATED layout (§2.2).
func testMigratedBytes(uuid [16]byte, writtenMs int64) []byte {
	b := make([]byte, 40)
	binary.LittleEndian.PutUint32(b[0:], 0x4D515346)
	binary.LittleEndian.PutUint16(b[4:], 4)
	copy(b[8:], uuid[:])
	binary.LittleEndian.PutUint64(b[24:], uint64(writtenMs))
	binary.LittleEndian.PutUint32(b[32:], crc32.Checksum(b[:32], castagnoli))
	return b
}

func TestStoreMigrateFormat4MarkerLayout(t *testing.T) {
	var uuid [16]byte
	for i := range uuid {
		uuid[i] = byte(i)
	}
	if got := hex.EncodeToString(testMigratedBytes(uuid, 1790000000123)); got != "4653514d04000000000102030405060708090a0b0c0d0e0f7b6c50c4a001000086bbeba000000000" {
		t.Fatalf("MIGRATED %s differs from the contract's golden vector", got)
	}
}

func writeTestMarker(t *testing.T, root, name string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, marker.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, marker.Dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Orphan index rows (no producer table holds the record) are reported per
// schema and not migrated; a record whose FIRST copy is gone moves its tags to
// the next copy. The check passes and the store equals format 1.
func TestStoreMigrateFormat4ReportsOrphans(t *testing.T) {
	legacy := t.TempDir()
	buildLegacyStore(t, legacy)
	orphans := orphanLegacyIndexRows(t, legacy)
	pristine := cloneStore(t, legacy)
	engines := newFakeEngines()
	rep, err := migrateStore4(context.Background(), migrate4TestOptions(legacy, engines), nil)
	if err != nil {
		t.Fatalf("migrate: %v\n%+v", err, rep.Check)
	}
	c := rep.Check
	if c.MismatchCount != 0 || len(c.Orphans) != 1 || c.Orphans["OMM.fbs"] != int64(len(orphans)) || c.Records != 340-int64(len(orphans)) {
		t.Fatalf("check %+v, want %d OMM orphans", c, len(orphans))
	}
	assertFormat4EqualsFormat1(t, engines.store(legacy), pristine, 0)
	got, err := engines.store(legacy).Get(context.Background(), "OMM", mapKeys(orphans), true, false)
	if err != nil || len(got) != 0 {
		t.Fatalf("orphans in format 4: %d (%v)", len(got), err)
	}
}

func mapKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The check is not vacuous: a target that lost or changed anything fails the
// run before activation, naming the difference.
func TestStoreMigrateFormat4CheckFailsOnMismatch(t *testing.T) {
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	for _, tc := range []struct {
		name string
		wrap func(format4.API) format4.API
		want string
	}{
		{"changed bytes", func(a format4.API) format4.API { return &tamperAPI{API: a, get: flipData} }, "do not hash to the CID"},
		{"changed epoch", func(a format4.API) format4.API { return &tamperAPI{API: a, get: shiftEpoch} }, "epoch"},
		{"changed key", func(a format4.API) format4.API { return &tamperAPI{API: a, get: changeKey} }, "object key"},
		{"changed seq", func(a format4.API) format4.API { return &tamperAPI{API: a, get: shiftSeq} }, "seq"},
		{"changed signature", func(a format4.API) format4.API { return &tamperAPI{API: a, get: changeSig} }, "the copies"},
		{"lost tag", func(a format4.API) format4.API { return &tamperAPI{API: a, tags: dropTag} }, "tag instances"},
		{"lost copy", func(a format4.API) format4.API { return &tamperAPI{API: a, get: dropCopy} }, "copies"},
		{"column", func(a format4.API) format4.API { return &tamperAPI{API: a, head: true} }, "every present column"},
		{"verify rebuild", func(a format4.API) format4.API { return &tamperAPI{API: a, rebuild: true} }, "verify rebuild"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := cloneStore(t, legacy)
			engines := newFakeEngines()
			engines.wrap = tc.wrap
			rep, err := migrateStore4(context.Background(), migrate4TestOptions(root, engines), nil)
			if err == nil || rep.Activated || rep.Check == nil || rep.Check.MismatchCount == 0 {
				t.Fatalf("a %s passed: %v %+v", tc.name, err, rep.Check)
			}
			if !strings.Contains(strings.Join(rep.Check.Mismatches, "\n"), tc.want) {
				t.Fatalf("mismatches do not name %q: %v", tc.want, rep.Check.Mismatches)
			}
			if mk, _ := marker.Read(root); mk.Format4() || !mk.LegacyControlFile {
				t.Fatalf("a failed check activated or moved something: %+v", mk)
			}
		})
	}
}

// tamperAPI changes what the target answers (after the copy wrote it right).
type tamperAPI struct {
	format4.API
	get     func([]format4.Rec) []format4.Rec
	tags    func([]format4.TagRow) []format4.TagRow
	head    bool
	rebuild bool
}

func (a *tamperAPI) Get(ctx context.Context, typ string, cids []string, all, hydrate bool) ([]format4.Rec, error) {
	r, err := a.API.Get(ctx, typ, cids, all, hydrate)
	if err == nil && a.get != nil && typ == "OMM" {
		r = a.get(r)
	}
	return r, err
}

func (a *tamperAPI) Tags(ctx context.Context, typ string, cids []string) ([]format4.TagRow, error) {
	r, err := a.API.Tags(ctx, typ, cids)
	if err == nil && a.tags != nil && typ == "OMM" {
		r = a.tags(r)
	}
	return r, err
}

func (a *tamperAPI) Head(ctx context.Context, q format4.Query) (format4.Head, error) {
	h, err := a.API.Head(ctx, q)
	if a.head && q.Type == "OMM" && len(q.Preds) > 0 && q.Preds[0].Op == format4.OpEq {
		h.N = 0
	}
	return h, err
}

func (a *tamperAPI) Rebuild(ctx context.Context, typ string, what format4.RebuildWhat) ([]format4.RebuildRow, error) {
	rows, err := a.API.Rebuild(ctx, typ, what)
	if a.rebuild && what == format4.RebuildVerify && len(rows) > 0 {
		rows[0].Mismatches = 1
	}
	return rows, err
}

func flipData(r []format4.Rec) []format4.Rec {
	if len(r) > 0 && len(r[0].Data) > 0 {
		r[0].Data = append([]byte(nil), r[0].Data...)
		r[0].Data[len(r[0].Data)-1] ^= 1
	}
	return r
}

func shiftEpoch(r []format4.Rec) []format4.Rec {
	if len(r) > 0 {
		r[0].Epoch++
	}
	return r
}

func changeKey(r []format4.Rec) []format4.Rec {
	if len(r) > 0 {
		r[0].Key += "0"
	}
	return r
}

func shiftSeq(r []format4.Rec) []format4.Rec {
	if len(r) > 0 {
		r[0].Seq++
	}
	return r
}

func changeSig(r []format4.Rec) []format4.Rec {
	if len(r) > 0 {
		r[0].Sig = []byte{1}
	}
	return r
}

func dropCopy(r []format4.Rec) []format4.Rec {
	if len(r) > 1 {
		return r[1:]
	}
	return r
}

func dropTag(r []format4.TagRow) []format4.TagRow {
	if len(r) > 1 {
		return r[1:]
	}
	return r
}

// Formats 2 and 3 are refused; so are a root with no format-1 store,
// --verify-only before activation, and a source that changed under a
// half-done migration.
func TestStoreMigrateFormat4Refusals(t *testing.T) {
	ctx := context.Background()
	legacy := t.TempDir()
	buildLegacyStore(t, legacy)
	for _, level := range []uint16{2, 3} {
		root := cloneStore(t, legacy)
		sf, err := format2.NewStoreFile(1, 1)
		if err != nil {
			t.Fatal(err)
		}
		sf.Format = level
		if err := os.MkdirAll(filepath.Join(root, format2.Dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := format2.WriteStoreFile(root, sf); err != nil {
			t.Fatal(err)
		}
		if err := format2.WriteMigrated(root, sf.UUID); err != nil {
			t.Fatal(err)
		}
		_, err = migrateStore4(ctx, migrate4TestOptions(root, newFakeEngines()), nil)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("format %d", level)) {
			t.Fatalf("a format-%d store: %v", level, err)
		}
		if _, err := os.Stat(filepath.Join(root, marker.Dir)); err == nil {
			t.Fatalf("a refused format-%d store got an fsql4 directory", level)
		}
	}
	if _, err := migrateStore4(ctx, migrate4TestOptions(t.TempDir(), newFakeEngines()), nil); err == nil ||
		!strings.Contains(err.Error(), "no format-1 store") {
		t.Fatalf("an empty root: %v", err)
	}
	vo := migrate4TestOptions(legacy, newFakeEngines())
	vo.VerifyOnly = true
	if _, err := migrateStore4(ctx, vo, nil); err == nil || !strings.Contains(err.Error(), "not") {
		t.Fatalf("--verify-only before activation: %v", err)
	}
	// A half-done migration, then the daemon (format 1) took writes.
	engines := newFakeEngines()
	o := migrate4TestOptions(legacy, engines)
	o.testStep = func(step string) error {
		if step == "page" {
			return errTestCrash
		}
		return nil
	}
	if _, err := migrateStore4(ctx, o, nil); !errors.Is(err, errTestCrash) {
		t.Fatalf("first run: %v", err)
	}
	v, _ := sds.NewValidator(nil)
	s, err := storage.NewFlatSQLStore(legacy, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreWithSourceTags("OMM.fbs", migrateTestOMM(29999, time.Now(), "LATE"), "source:celestrak", nil,
		storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "late"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateStore4(ctx, migrate4TestOptions(legacy, engines), nil); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("a source that changed under the migration: %v", err)
	}
}

// ---- the real engine ---------------------------------------------------------------------

func migrate4AOTDir(t testing.TB) string {
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return filepath.Join(base, "sdn-format4-test-aot")
}

// engineWasm is the format-4 engine under test: SDN_P4_WASM (a build of the
// engine's task branch, during development), else the embedded release
// (nil).
func engineWasm(t testing.TB) []byte {
	p := os.Getenv("SDN_P4_WASM")
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// requireFormat4Engine skips unless format4.Open opens the engine here (it
// needs the patched runtime and an engine: embedded, or SDN_P4_WASM).
func requireFormat4Engine(t *testing.T) {
	t.Helper()
	api, err := openFormat4Engine(context.Background(), format4.Options{DataRoot: t.TempDir(), Create: format4.CreateFresh,
		AOTCacheDir: migrate4AOTDir(t), CompileOnMiss: true, Wasm: engineWasm(t)})
	if err != nil {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" && os.Getenv("SDN_P4_WASM") != "" {
			t.Fatalf("the format-4 engine does not open: %v", err)
		}
		t.Skipf("the format-4 engine does not open here: %v", err)
	}
	_ = api.Close(context.Background())
}

func engineOptions(t testing.TB, store string) migrate4Options {
	return migrate4Options{Store: store, PageRows: 64, AOTCacheDir: migrate4AOTDir(t), CompileOnMiss: true, Wasm: engineWasm(t)}
}

// openEngineAt opens a migrated store on the engine, its types registered.
func openEngineAt(t *testing.T, root string, schemas ...string) format4.API {
	t.Helper()
	api, err := openFormat4Engine(context.Background(), format4.Options{DataRoot: root, Create: format4.OpenExisting,
		AOTCacheDir: migrate4AOTDir(t), CompileOnMiss: true, Wasm: engineWasm(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		spec, err := format4.TypeSpecFor(schema)
		if err != nil {
			t.Fatal(err)
		}
		if err := api.RegisterType(spec); err != nil {
			t.Fatal(err)
		}
	}
	return api
}

// engineDump opens a migrated store on the engine and dumps it.
func engineDump(t *testing.T, root string) string {
	t.Helper()
	api := openEngineAt(t, root, "OMM.fbs", "CAT.fbs", "IQC.fbs")
	return format4Dump(t, api, root)
}

const migrate4KillChildEnv = "SDN_MIGRATE4_KILL_CHILD_STORE"

// TestStoreMigrateFormat4KillChild is the child the kill -9 test kills.
func TestStoreMigrateFormat4KillChild(t *testing.T) {
	dir := os.Getenv(migrate4KillChildEnv)
	if dir == "" {
		t.Skip("runs only as the kill -9 test's child")
	}
	_, _ = migrateStore4(context.Background(), engineOptions(t, dir), nil)
}

// kill -9 at random points of a real migration (format-1 engine reading,
// format-4 engine writing), then a rerun: the store equals an uninterrupted
// migration of the same source and format 1. SDN_MIGRATE4_KILLS sets the
// number of kills.
func TestStoreMigrateFormat4Kill9(t *testing.T) {
	requireFormat4Engine(t)
	kills := 12
	if v := os.Getenv("SDN_MIGRATE4_KILLS"); v != "" {
		fmt.Sscanf(v, "%d", &kills)
	}
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	pristine := cloneStore(t, legacy)
	clean := cloneStore(t, legacy)
	if _, err := migrateStore4(context.Background(), engineOptions(t, clean), nil); err != nil {
		t.Fatalf("clean migration: %v", err)
	}
	want := engineDump(t, clean)
	rng := uint64(time.Now().UnixNano())
	var delays []time.Duration
	for done := 0; done < kills; {
		killed := cloneStore(t, legacy)
		for i := 0; i < 6 && done < kills; i++ {
			cmd := exec.Command(os.Args[0], "-test.run", "^TestStoreMigrateFormat4KillChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), migrate4KillChildEnv+"="+killed)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			rng = rng*6364136223846793005 + 1442695040888963407
			d := 200*time.Millisecond + time.Duration(rng>>33)%(2*time.Second)
			delays = append(delays, d)
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case <-exited:
			case <-time.After(d):
				_ = cmd.Process.Signal(syscall.SIGKILL)
				<-exited
			}
			done++
		}
		rep, err := migrateStore4(context.Background(), engineOptions(t, killed), nil)
		if err != nil || !rep.Activated {
			if keep := os.Getenv("SDN_MIGRATE4_KEEP"); keep != "" {
				// The killed store, as the failed resume left it, for the
				// engine's maintainers.
				_ = exec.Command("cp", "-cR", killed, filepath.Join(keep, fmt.Sprintf("killed-%d", time.Now().Unix()))).Run()
			}
			t.Fatalf("resume after kill %d (delays %v): %v %+v", done, delays, err, rep)
		}
		if got := engineDump(t, killed); got != want {
			t.Fatalf("after kill %d the store differs from an uninterrupted migration:\n%s", done, firstDiff(want, got))
		}
	}
	api := openEngineAt(t, clean, "OMM.fbs", "CAT.fbs", "IQC.fbs")
	defer api.Close(context.Background())
	assertFormat4EqualsFormat1(t, api, pristine, 0)
	t.Logf("MEASURED %d kill -9 points (delays %v): resumed stores identical to an uninterrupted migration", kills, delays)
}

// nullTarget accepts every PUT and holds nothing: it times the format-1 side
// of a migration (reads, page building, journalling) alone.
type nullTarget struct{ format4.API }

func (nullTarget) RegisterType(format4.TypeSpec) error { return nil }
func (nullTarget) Put(_ context.Context, b format4.Batch) ([]format4.Outcome, error) {
	out := make([]format4.Outcome, len(b.Records))
	for i, in := range b.Records {
		out[i] = format4.Outcome{Action: format4.ActMigrated, Seq: in.Seq}
	}
	return out, nil
}
func (nullTarget) Partitions(context.Context) ([]format4.PartitionSummary, error) { return nil, nil }
func (nullTarget) Rebuild(context.Context, string, format4.RebuildWhat) ([]format4.RebuildRow, error) {
	return nil, nil
}
func (nullTarget) Stats() ([]uint64, error)    { return nil, nil }
func (nullTarget) Close(context.Context) error { return nil }

var errCopyTimed = errors.New("copy timed")

// TestStoreMigrateFormat4Fixture migrates a clone of a host-02-sized
// format-1 store (SDN_F1_FIXTURE=<store dir>) and reports time, RSS and the
// load. SDN_MIGRATE4_TARGET picks the target: engine (the default), fake
// (format4test's in-memory Fake: the whole run, check and activation, on the
// real data, without the engine's time and memory), or null (the copy pass
// into a target that keeps nothing: the format-1 side alone).
// SDN_MIGRATE4_RESUME=1 resumes the run already in SDN_MIGRATE_WORK.
func TestStoreMigrateFormat4Fixture(t *testing.T) {
	src := strings.TrimSpace(os.Getenv("SDN_F1_FIXTURE"))
	if src == "" {
		src = strings.TrimSpace(os.Getenv("P4_FIXTURE"))
	}
	if src == "" {
		t.Skip("SDN_F1_FIXTURE names a format-1 store directory")
	}
	target := os.Getenv("SDN_MIGRATE4_TARGET")
	if target == "" {
		target = "engine"
	}
	work := os.Getenv("SDN_MIGRATE_WORK")
	if work == "" {
		work = t.TempDir()
	}
	dst := filepath.Join(work, "store")
	if _, err := os.Stat(dst); err == nil && os.Getenv("SDN_MIGRATE4_RESUME") == "1" {
		t.Logf("resuming the migration in %s", dst)
	} else if out, err := exec.Command("cp", "-cR", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("clone fixture: %v: %s", err, out)
	}
	opt := migrate4Options{Store: dst, AOTCacheDir: migrate4AOTDir(t), CompileOnMiss: true, Wasm: engineWasm(t)}
	engines := newFakeEngines()
	switch target {
	case "engine":
		requireFormat4Engine(t)
	case "fake":
		opt.open = engines.open
		opt.testNoSamples = true // the Fake answers HEAD by sorting every seq
	case "null":
		opt.open = func(context.Context, format4.Options) (format4.API, error) { return nullTarget{}, nil }
		opt.testStep = func(step string) error {
			if step == "built" {
				return errCopyTimed
			}
			return nil
		}
	default:
		t.Fatalf("SDN_MIGRATE4_TARGET=%q: engine, fake or null", target)
	}
	var log bytes.Buffer
	rep, err := migrateStore4(context.Background(), opt, &log)
	if target == "null" && errors.Is(err, errCopyTimed) {
		err = nil
	}
	if rep == nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Logf("MEASURED store-migrate --to 4 (%s target): %d records, %d copies, %d B of records, source %d B in %s (%.1f MB/s); max RSS %.0f MB; load %.1f -> %.1f; %s",
		target, rep.Records, rep.Copies, rep.RecordBytes, rep.SourceBytes, rep.Took, rep.RecordMBps, float64(rep.MaxRSS)/1e6,
		rep.Load[0], rep.Load[1], rep.Machine)
	t.Logf("time by phase: %v", rep.Time)
	if c := rep.Check; c != nil {
		t.Logf("check: %d records, %d copies, %d re-hashed, %d field checks, %d column samples, %d tags, %d partitions, %d lanes, orphans %v, mismatches %d, %s",
			c.Records, c.Copies, c.Rehashed, c.FieldChecks, c.ColumnSamples, c.TagInstances, c.Partitions, c.Lanes, c.Orphans, c.MismatchCount, c.Took)
		for _, mm := range c.Mismatches {
			t.Logf("mismatch: %s", mm)
		}
	}
	t.Logf("notes %q; rejected %d; oversized %d", rep.Notes, len(rep.Rejected), len(rep.Oversized))
	if err != nil {
		t.Fatalf("migrate: %v\n%s", err, log.String())
	}
	if target == "fake" {
		// A slice of the fixture, against format 1 (now in pre-format4/),
		// with format 2's readers; every counter in full.
		t0 := time.Now()
		assertFormat4EqualsFormat1(t, engines.store(dst), filepath.Join(dst, marker.PreFormat4Dir), 5000)
		t.Logf("the first 5,000 held records of each schema and every counter equal format 1 (%s)", time.Since(t0).Round(time.Second))
	}
}
