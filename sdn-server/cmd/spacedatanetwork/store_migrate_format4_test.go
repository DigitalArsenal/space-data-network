package main

// store-migrate --to 4 (store_migrate_format4.go) end to end: real format-1
// stores, read through the format-1 engine, migrated into the format-4 engine
// (embedded, or SDN_P4_WASM; these tests skip where it does not open). Tests
// are end to end and few (contract C-33); beside the command-line checks:
//   - TestStoreMigrateFormat4Kill9: kill -9 of a real migration at random
//     points, then a rerun: identical to an uninterrupted migration, which
//     equals format 1;
//   - TestStoreMigrateFormat4ResumesAfterEveryStep: a run stopped at each
//     step, a torn STORE and cut-short file moves, then a rerun;
//   - TestStoreMigrateFormat4Fixture: the host-02-sized fixture
//     (SDN_F1_FIXTURE=<format-1 store dir>);
//   - TestStoreMigrateFormat4UnregisteredTables: records of a schema with no
//     standard are never dropped without --drop-unregistered (C-39 U1);
//   - TestStoreMigrateFormat4OversizedRecords: nor records above the largest
//     storable record without --drop-oversized (C-6, by U1's rule).
//
// The test store holds feeds whose names need escaping in a file name
// (migrate4OddFeeds); every migrated store's feed files are checked
// (assertFeedFiles, C-37 (1)).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

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
	// A record with no peer (format 1's "unattributed" table keeps its empty
	// peer), and a tag UpsertSourceTags took for the upper-case text of a
	// held CID: format 1 joins tags by the CID text, so it names no record.
	nopeer := migrateTestOMM(30100, base, "NO-PEER")
	if _, err := s.StoreBatch("OMM.fbs", [][]byte{nopeer}, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSourceTags("OMM.fbs", strings.ToUpper(storage.ComputeCID(nopeer)), signed); err != nil {
		t.Fatal(err)
	}
	// Feeds whose provider or source needs escaping in a file name (C-37
	// (1)), two records each; the first record of each is also in the
	// signed feed (a record in two feeds' files).
	for i, f := range migrate4OddFeeds {
		recs := [][]byte{
			migrateTestOMM(uint32(30200+2*i), base.Add(time.Duration(i)*time.Minute), fmt.Sprintf("ODD-%d-A", i)),
			migrateTestOMM(uint32(30201+2*i), base.Add(time.Duration(i)*time.Minute), fmt.Sprintf("ODD-%d-B", i)),
		}
		tags := storage.SourceTags{ProviderID: f[0], SourceName: f[1], BatchID: "odd-1"}
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", recs, "source:celestrak", nil, tags); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StoreWithSourceTags("OMM.fbs", recs[0], "source:celestrak", nil, signed); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// migrate4OddFeeds are (provider, source) feeds whose names need escaping in
// a file name: a space, '/', "/../", '%', '?', '#', non-ASCII bytes.
var migrate4OddFeeds = [][2]string{
	{"space-data-network-02", "feed name with spaces"},
	{"other provider", "other source/x"},
	{"space-data-network-02", "x/../local"},
	{"space-data-network-02", "50%25 off %zz"},
	{"space-data-network-02", "gp?share=0&mode=ro#frag"},
	{"prövider-ü", "源-é/ñ"},
}

// assertFeedFiles checks a migrated store's feed files (C-37 (1): one file
// per source feed x standard, its name a safe encoding of the feed): no
// directory under fsql4/P/<TYPE> (a name is one path element), no empty
// database file (the records are in the file the engine named), and as many
// feed files as the type has feeds (local.db aside).
func assertFeedFiles(t *testing.T, root string, schemas ...string) {
	t.Helper()
	api := openEngineAt(t, root, schemas...)
	lanes, err := api.Lanes(context.Background(), "")
	_ = api.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	feeds := map[string]map[[2]string]bool{}
	for _, l := range lanes {
		if l.Records > 0 && (l.Provider != "" || l.Source != "") {
			if feeds[l.Type] == nil {
				feeds[l.Type] = map[[2]string]bool{}
			}
			feeds[l.Type][[2]string{l.Provider, l.Source}] = true
		}
	}
	for typ, fs := range feeds {
		dir := filepath.Join(root, marker.Dir, "P", typ)
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		files := 0
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() {
				t.Errorf("%s: P/%s/%s is a directory: a feed file name is one path element", root, typ, name)
				continue
			}
			if !strings.HasSuffix(name, ".db") || name == "local.db" {
				continue
			}
			files++
			if fi, err := e.Info(); err != nil || fi.Size() == 0 {
				t.Errorf("%s: P/%s/%s is empty: the feed's records are not in the file the engine named", root, typ, name)
			}
		}
		if files != len(fs) {
			t.Errorf("%s: P/%s holds %d feed files for %d feeds", root, typ, files, len(fs))
		}
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
	// C-38 (5): a record whose tags name N feeds (distinct provider and
	// source) is a row set in each feed file, so the type and partition
	// counters count it, its copies and their bytes N times.
	partExtra := map[string][2]int64{} // table -> rows, bytes beyond one row set per record
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
		var recs []format4.Rec
		var gotTags []format4.TagRow
		for k := 0; k < len(cids); k += migrate4ReadCIDs { // within a read slot
			chunk := cids[k:min(k+migrate4ReadCIDs, len(cids))]
			r, err := api.Get(ctx, typ, chunk, true, true)
			if err != nil {
				t.Fatal(err)
			}
			tg, err := api.Tags(ctx, typ, chunk)
			if err != nil {
				t.Fatal(err)
			}
			recs, gotTags = append(recs, r...), append(gotTags, tg...)
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
				// A copy keeps its own peer, none included, where format 1
				// routes it to its table; elsewhere an empty peer is the
				// table's token (partitionPeer).
				wantPeer := lr.PeerID
				if name, err := storage.RoutedTableName(wantPeer, schema); wantPeer == "" && (err != nil || name != tb.Name) {
					wantPeer = tb.Token
				}
				if r.Seq != e.RowID || r.Peer != wantPeer || !bytes.Equal(r.Sig, legacySignature(lr.SignatureHex)) ||
					r.TS != lr.Timestamp || !bytes.Equal(r.Data, first.Stored) || r.Len != int64(len(first.Stored)) {
					t.Fatalf("%s %s copy %s: format 4 seq %d peer %q sig %x ts %d len %d; format 1 rowid %d peer %q sig %q ts %d len %d",
						schema, e.CID, tb.Token, r.Seq, r.Peer, r.Sig, r.TS, r.Len, e.RowID, lr.PeerID, lr.SignatureHex,
						lr.Timestamp, len(first.Stored))
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
		feeds := map[string]map[[2]string]bool{}
		if err := src.SchemaTags(schema, func(cid string, lt storage.LegacyTag) error {
			if feeds[cid] == nil {
				feeds[cid] = map[[2]string]bool{}
			}
			feeds[cid][[2]string{lt.ProviderID, lt.SourceName}] = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var multi []string
		for cid, f := range feeds {
			if len(f) > 1 {
				multi = append(multi, cid)
			}
		}
		var extraRecords, extraCopies int64
		heldMulti := map[string]bool{}
		for _, tb := range bySchema[schema] {
			held, err := src.RecordsByCID(tb, multi)
			if err != nil {
				t.Fatal(err)
			}
			for cid, r := range held {
				x := int64(len(feeds[cid]) - 1)
				extraCopies += x
				pe := partExtra[tb.Name]
				partExtra[tb.Name] = [2]int64{pe[0] + x, pe[1] + x*int64(len(r.Stored))}
				if !heldMulti[cid] {
					heldMulti[cid] = true
					extraRecords += x
				}
			}
		}
		// Tags: one row per (cid, identity) on both sides.
		want, err := src.TagsFor(schema, cids)
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
		if ts.Copies != typeCopies+extraCopies || (slice == 0 && (ts.Records != int64(len(entries))+extraRecords ||
			(len(entries) > 0 && ts.MaxSeq != entries[len(entries)-1].RowID))) {
			t.Fatalf("type %s: %+v; format 1 holds %d records (slice %d), %d copies, and %d records, %d copies more in further feed files",
				typ, ts, len(entries), slice, typeCopies, extraRecords, extraCopies)
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
		x := partExtra[tb.Name]
		if p := gotParts[typ+"/"+tb.Token]; p.Records != c.Count+x[0] || p.Bytes != c.Bytes+x[1] {
			t.Fatalf("partition %s: format 4 (%d, %d B), format 1 table (%d, %d B) and (%d, %d B) more in further feed files",
				tb.Name, p.Records, p.Bytes, c.Count, c.Bytes, x[0], x[1])
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
		migrate4ControlTmp} {
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

// --inventory --to 4 is the format-2 inventory with format 4's record limit
// (C-6) and changes nothing.
func TestStoreMigrateFormat4Inventory(t *testing.T) {
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	inv, err := migrate4Inventory(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Extra["target_format"] != 4 || inv.Extra["max_record_bytes"] != migrate4MaxRecord || len(inv.OverEntry) != 0 {
		t.Fatalf("inventory %+v", inv)
	}
	if len(inv.Partitions) != 6 || inv.GseqFloor == 0 {
		t.Fatalf("inventory partitions %d, floor %d", len(inv.Partitions), inv.GseqFloor)
	}
	if mk, _ := marker.Read(legacy); mk.Format4() || !mk.LegacyControlFile {
		t.Fatalf("the inventory changed the store: %+v", mk)
	}
	if _, err := os.Stat(filepath.Join(legacy, migrate4JournalName)); err == nil {
		t.Fatal("the inventory wrote a migration journal")
	}
}

// C-39 U1: a format-1 table of a schema SDN has no standard for (StoreBatch
// to an unknown name writes one) is listed by --inventory, and the migration
// refuses, changing nothing, until --drop-unregistered names exactly such
// tables; then it migrates everything else and activates.
func TestStoreMigrateFormat4UnregisteredTables(t *testing.T) {
	requireFormat4Engine(t)
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(legacy, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	rec := migrateTestOMM(31000, time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), "UNKNOWN-SCHEMA")
	if _, err := s.StoreBatch("XYZ.fbs", [][]byte{rec}, "source:celestrak", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	inv, err := migrate4Inventory(legacy)
	if err != nil {
		t.Fatal(err)
	}
	unregistered, _ := inv.Extra["unregistered_tables"].(map[string]int64)
	var table, registered string
	for name, n := range unregistered {
		table = name
		if n != 1 || !strings.HasSuffix(name, "XYZ") {
			t.Fatalf("inventory unregistered_tables %v", unregistered)
		}
	}
	for _, p := range inv.Partitions {
		if p.Schema == "OMM.fbs" {
			registered = p.Table
		}
	}
	if len(unregistered) != 1 || registered == "" {
		t.Fatalf("inventory unregistered_tables %v, an OMM table %q", unregistered, registered)
	}
	ctx := context.Background()
	root := cloneStore(t, legacy)
	for name, drop := range map[string][]string{"no --drop-unregistered": nil, "--drop-unregistered naming an OMM table": {registered},
		"--drop-unregistered naming the table and an OMM table": {table, registered}} {
		o := engineOptions(t, root)
		o.DropUnregistered = drop
		if rep, err := migrateStore4(ctx, o, nil); err == nil || rep.Activated {
			t.Fatalf("%s: the migration ran: %v", name, err)
		} else {
			t.Logf("%s: %v", name, err)
		}
		if mk, err := marker.Read(root); err != nil || mk.Format4() || !mk.LegacyControlFile {
			t.Fatalf("%s: the refused migration changed the store: %+v %v", name, mk, err)
		}
		for _, p := range []string{migrate4JournalName, marker.Dir} {
			if _, err := os.Stat(filepath.Join(root, p)); err == nil {
				t.Fatalf("%s: the refused migration wrote %s", name, p)
			}
		}
	}
	o := engineOptions(t, root)
	o.DropUnregistered = []string{table}
	rep, err := migrateStore4(ctx, o, nil)
	if err != nil {
		t.Fatalf("--drop-unregistered %s: %v", table, err)
	}
	if !rep.Activated || rep.Unregistered[table] != 1 {
		t.Fatalf("--drop-unregistered %s: activated %v, unregistered %v", table, rep.Activated, rep.Unregistered)
	}
}

// C-6 by C-39 U1's rule: a record above format 4's largest storable record
// is listed by --inventory, and the migration refuses, changing nothing,
// until --drop-oversized names it; then the record is set aside (format 4
// holds none of it) and everything else migrates and activates.
func TestStoreMigrateFormat4OversizedRecords(t *testing.T) {
	requireFormat4Engine(t)
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(legacy, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	big := append(migrateTestOMM(31001, time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), "OVERSIZED"), make([]byte, 9<<20)...)
	tag := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "big-1"}
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", [][]byte{big}, "source:celestrak", nil, tag); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	inv, err := migrate4Inventory(legacy)
	if err != nil {
		t.Fatal(err)
	}
	over, _ := inv.Extra["oversized_records"].([]migrate4Oversized)
	if len(over) != 1 || over[0].CID != storage.ComputeCID(big) || over[0].Bytes != int64(len(big)) {
		t.Fatalf("inventory oversized_records %+v", inv.Extra["oversized_records"])
	}
	ctx := context.Background()
	root := cloneStore(t, legacy)
	for name, drop := range map[string][]string{"no --drop-oversized": nil, "--drop-oversized naming a stored record": {storage.ComputeCID(migrate4TestIQC(1, "2026-09-15T02:11:22Z"))}} {
		o := engineOptions(t, root)
		o.DropOversized = drop
		if rep, err := migrateStore4(ctx, o, nil); err == nil || rep.Activated {
			t.Fatalf("%s: the migration ran: %v", name, err)
		} else {
			t.Logf("%s: %v", name, err)
		}
		if mk, err := marker.Read(root); err != nil || mk.Format4() || !mk.LegacyControlFile {
			t.Fatalf("%s: the refused migration changed the store: %+v %v", name, mk, err)
		}
		for _, p := range []string{migrate4JournalName, marker.Dir} {
			if _, err := os.Stat(filepath.Join(root, p)); err == nil {
				t.Fatalf("%s: the refused migration wrote %s", name, p)
			}
		}
	}
	o := engineOptions(t, root)
	o.DropOversized = []string{over[0].CID}
	rep, err := migrateStore4(ctx, o, nil)
	if err != nil {
		t.Fatalf("--drop-oversized %s: %v\n%+v", over[0].CID, err, rep.Check)
	}
	if !rep.Activated || len(rep.SetAside) != 1 || len(rep.Oversized) != 0 {
		t.Fatalf("--drop-oversized: activated %v, set aside %+v, refused by the engine %+v", rep.Activated, rep.SetAside, rep.Oversized)
	}
	api := openEngineAt(t, root, "OMM.fbs")
	defer api.Close(ctx)
	if recs, err := api.Get(ctx, "OMM", []string{over[0].CID}, false, false); err != nil || len(recs) != 0 {
		t.Fatalf("format 4 holds the set-aside record: %d copies, %v", len(recs), err)
	}
}

var errTestCrash = errors.New("test crash")

// A run that stops at any step (every PUT, page, journal write, the index
// build, the check, the control copy, the engine's activation) and a rerun
// leave the same store as an uninterrupted run. So does a torn STORE (the
// engine cut short inside its activation, C-22), and a crash inside the final
// file moves. Every step is stopped at in the patched-substrate CI lane
// (SDN_WASM_REQUIRE_PATCHED=1) or with SDN_MIGRATE4_EVERY_STEP=1; elsewhere
// the first stop of each kind, and the middle journal write.
func TestStoreMigrateFormat4ResumesAfterEveryStep(t *testing.T) {
	requireFormat4Engine(t)
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	ctx := context.Background()

	clean := cloneStore(t, legacy)
	var steps []string
	opt := resumeOptions(t, clean)
	opt.testStep = func(step string) error {
		steps = append(steps, step)
		return nil
	}
	if _, err := migrateStore4(ctx, opt, nil); err != nil {
		t.Fatalf("clean migration: %v", err)
	}
	want := engineDump(t, clean)
	assertFeedFiles(t, clean, "OMM.fbs", "CAT.fbs", "IQC.fbs")
	t.Logf("a clean migration passes %d steps", len(steps))

	resume := func(name string, fail func(step string, n int) bool, between func(root string)) {
		t.Helper()
		root := cloneStore(t, legacy)
		var mu sync.Mutex
		n := 0
		o := resumeOptions(t, root)
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
		rep, err := migrateStore4(ctx, resumeOptions(t, root), nil)
		if err != nil {
			t.Fatalf("%s: resume: %v\n%+v", name, err, rep.Check)
		}
		if !rep.Activated {
			t.Fatalf("%s: the resumed run did not activate", name)
		}
		if got := engineDump(t, root); got != want {
			t.Fatalf("%s: the resumed store differs from an uninterrupted migration:\n%s", name, firstDiff(want, got))
		}
		assertFeedFiles(t, root, "OMM.fbs", "CAT.fbs", "IQC.fbs")
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
	// The engine's activation cut short inside STORE's write: its MIGRATED
	// whole, STORE torn.
	resume("torn STORE", func(step string, _ int) bool { return step == "activated" }, func(root string) {
		if err := os.Truncate(filepath.Join(root, marker.Dir, marker.StoreFile), 4); err != nil {
			t.Fatal(err)
		}
		if mk, err := marker.Read(root); err != nil || !mk.MigratedValid || mk.StoreValid || !mk.StorePresent {
			t.Fatalf("not a torn STORE beside a whole MIGRATED: %+v %v", mk, err)
		}
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

// resumeOptions journals after every page, so a stop resumes mid-copy.
func resumeOptions(t *testing.T, root string) migrate4Options {
	o := engineOptions(t, root)
	o.testJournalEvery = time.Nanosecond
	return o
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
// needs the patched runtime and an engine: embedded, or SDN_P4_WASM). On an
// unpatched runtime it skips like every partition-store test (the quick CI
// lane links upstream WasmEdge; the substrate lane runs these).
func requireFormat4Engine(t *testing.T) {
	t.Helper()
	requirePSEngine(t)
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
	assertFeedFiles(t, clean, "OMM.fbs", "CAT.fbs", "IQC.fbs")
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
		assertFeedFiles(t, killed, "OMM.fbs", "CAT.fbs", "IQC.fbs")
	}
	api := openEngineAt(t, clean, "OMM.fbs", "CAT.fbs", "IQC.fbs")
	defer api.Close(context.Background())
	assertFormat4EqualsFormat1(t, api, pristine, 0)
	t.Logf("MEASURED %d kill -9 points (delays %v): resumed stores identical to an uninterrupted migration", kills, delays)
}

// TestStoreMigrateFormat4Fixture migrates a clone of a host-02-sized
// format-1 store (SDN_F1_FIXTURE=<store dir>) on the engine and reports time,
// RSS and the load. SDN_MIGRATE4_RESUME=1 resumes the run already in
// SDN_MIGRATE_WORK, and with SDN_MIGRATE4_VERIFY_ONLY=1 re-checks it
// (--verify-only) once activated.
func TestStoreMigrateFormat4Fixture(t *testing.T) {
	src := strings.TrimSpace(os.Getenv("SDN_F1_FIXTURE"))
	if src == "" {
		src = strings.TrimSpace(os.Getenv("P4_FIXTURE"))
	}
	if src == "" {
		t.Skip("SDN_F1_FIXTURE names a format-1 store directory")
	}
	requireFormat4Engine(t)
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
	opt := migrate4Options{Store: dst, AOTCacheDir: migrate4AOTDir(t), CompileOnMiss: true, Wasm: engineWasm(t),
		VerifyOnly: os.Getenv("SDN_MIGRATE4_VERIFY_ONLY") == "1"}
	var log bytes.Buffer
	rep, err := migrateStore4(context.Background(), opt, &log)
	if rep == nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Logf("MEASURED store-migrate --to 4: %d records, %d copies, %d B of records, source %d B in %s (%.1f MB/s); max RSS %.0f MB; load %.1f -> %.1f; %s",
		rep.Records, rep.Copies, rep.RecordBytes, rep.SourceBytes, rep.Took, rep.RecordMBps, float64(rep.MaxRSS)/1e6,
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
	// A slice of the fixture, against format 1 (now in pre-format4/), with
	// format 2's readers; every counter in full.
	t0 := time.Now()
	api := openEngineAt(t, dst, "CAT.fbs", "IQC.fbs", "MPE.fbs", "OMM.fbs")
	defer api.Close(context.Background())
	assertFormat4EqualsFormat1(t, api, filepath.Join(dst, marker.PreFormat4Dir), 5000)
	t.Logf("the first 5,000 held records of each schema and every counter equal format 1 (%s)", time.Since(t0).Round(time.Second))
}
