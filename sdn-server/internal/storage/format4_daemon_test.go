package storage

// The daemon's record API on store format 4 answers what format 1 answers
// (contract §6, backend): the writes' answers, sealed records, the log
// entries, the sync-filter predicates, the store selection and refusals, and
// the record paths' locking, each through the storage API into a format-1
// store and a fresh format-4 store. Every record read, field by field, is
// the proof harness's (format4proof: the benchset and the coverage classes,
// with C-38 (5)'s per-feed answers ruled call by call).
//
// Each test runs on the real engine (embedded, or SDN_P4_WASM; the patched
// runtime), the one implementation (C-33), and skips without one.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// format4TestWasm is the engine the format-4 tests run on (embedded, or
// SDN_P4_WASM names an artifact) on the patched runtime, or a skip.
func format4TestWasm(t *testing.T) []byte {
	t.Helper()
	if !flatsqlrt.NativeHostIOSupported() {
		t.Skip("the C host I/O module is not available on this platform")
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatalf("runtime is not patched: %+v", rep)
		}
		t.Skipf("linked libwasmedge lacks the SDN runtime patches (%+v)", rep)
	}
	wasm := flatsqlrt.P4ThreadsWasm()
	if p := os.Getenv("SDN_P4_WASM"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		wasm = b
	}
	if len(wasm) == 0 {
		t.Skip("no format-4 engine: none is embedded and SDN_P4_WASM is unset")
	}
	return wasm
}

// onFormat4Engine runs body with every format-4 open on the real engine.
func onFormat4Engine(t *testing.T, body func(t *testing.T)) {
	wasm := format4TestWasm(t)
	prev := format4OpenEngine
	format4OpenEngine = func(ctx context.Context, opt format4.Options) (format4.API, error) {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		opt.Wasm, opt.CompileOnMiss, opt.AOTCacheDir = wasm, true, filepath.Join(base, "sdn-format4-test-aot")
		return prev(ctx, opt)
	}
	t.Cleanup(func() { format4OpenEngine = prev })
	body(t)
}

// openFormat4ForTest opens (creating) a format-4 store at dir.
func openFormat4ForTest(t testing.TB, dir string, opts ...StoreOption) *FlatSQLStore {
	t.Helper()
	t.Setenv(format4.FormatEnv, "4")
	t.Setenv(checkpointIntervalEnv, "0")
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewFlatSQLStore(dir, v, opts...)
	if err != nil {
		t.Fatalf("open format 4: %v", err)
	}
	if !s.Format4() || !s.PartitionedRecords() || s.Format2() {
		t.Fatalf("SDN_STORE_FORMAT=4 opened format4=%v partitioned=%v format2=%v", s.Format4(), s.PartitionedRecords(), s.Format2())
	}
	t.Setenv(format4.FormatEnv, "")
	return s
}

// f4Rows is a record list keyed for comparison: CID, bytes and stored
// length (C-12: a copy's peer and signature are any one copy's).
func f4Rows(recs []*Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, fmt.Sprintf("%s|%x|%d", r.CID, r.Data, r.RecordLength))
	}
	return out
}

func encfieldIsSealed(b []byte) bool { return encfield.IsSealed(b) }

// The writes keep format 1's answers: new counts, copies, retags, the
// ingest identity, the licence row, the supersede counts and the refusals.
func TestFormat4WritesMatchFormat1(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		legacy := reopenDeferred(t, t.TempDir())
		defer legacy.Close()
		f4 := openFormat4ForTest(t, t.TempDir())
		defer f4.Close()
		sc := newF2Script()
		tags := SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b1", License: "CC-BY-4.0"}
		for _, s := range []*FlatSQLStore{legacy, f4} {
			if n, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[:20], "peer-a", nil, tags); err != nil || n != 20 {
				t.Fatalf("first batch: %d, %v", n, err)
			}
			// peer-b's batch copies omm[15:20] under its own tag.
			if n, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[15:30], "peer-b", nil, SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b2"}); err != nil || n != 10 {
				t.Fatalf("copies: %d, %v", n, err)
			}
			if n, err := s.StoreBatch("OMM.fbs", sc.omm[28:32], "peer-c", nil); err != nil || n != 2 {
				t.Fatalf("untagged copies: %d, %v", n, err)
			}
			cid, err := s.Store("OMM.fbs", sc.omm[0], "peer-a", nil)
			if err != nil || cid != ComputeCID(sc.omm[0]) {
				t.Fatalf("repeat Store: %s, %v", cid, err)
			}
			lic, found, err := s.SourceBatchLicenseFor("OMM.fbs", "p", "s", "b1")
			if err != nil || !found || lic.License != "CC-BY-4.0" {
				t.Fatalf("licence: %+v %v, %v", lic, found, err)
			}
			if err := s.UpsertSourceTags("OMM.fbs", ComputeCID(sc.omm[5]), SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b2"}); err != nil {
				t.Fatal(err)
			}
			if err := s.Delete("OMM.fbs", ComputeCID(sc.omm[40])); err == nil || !strings.HasPrefix(err.Error(), "not found: ") {
				t.Fatalf("delete of a record never stored: %v", err)
			}
			if _, err := s.StoreWithSourceTags("OMM.fbs", sc.omm[50], "peer-a", nil, SourceTags{SourceName: "s"}); err == nil {
				t.Fatal("a tag without a provider was stored")
			}
		}
		for _, apply := range []bool{false, true} {
			a, err := legacy.ReconcileSourceBatch("OMM.fbs", "p", "s", "b2", apply)
			if err != nil {
				t.Fatal(err)
			}
			b, err := f4.ReconcileSourceBatch("OMM.fbs", "p", "s", "b2", apply)
			if err != nil {
				t.Fatal(err)
			}
			if a != b {
				t.Fatalf("reconcile apply=%v: format 4 %+v, format 1 %+v", apply, b, a)
			}
		}
		for _, s := range []*FlatSQLStore{legacy, f4} {
			if _, err := s.GarbageCollect(time.Hour); (err == nil) == s.Format4() {
				t.Fatalf("GarbageCollect format4=%v: %v (C-11)", s.Format4(), err)
			}
		}
		if _, err := f4.GarbageCollect(time.Hour); !errors.Is(err, ErrFormat4Unsupported) {
			t.Fatalf("GarbageCollect: %v", err)
		}
		// The routed listings: every stored copy, as format 1 lists its
		// (producer, standard) table rows (the copies' source timestamps
		// aside: a copy keeps its holder's, C-12).
		routed := func(s *FlatSQLStore) []string {
			rs, err := s.QueryRoutedByStandard("OMM.fbs", 0)
			if err != nil {
				t.Fatalf("QueryRoutedByStandard format4=%v: %v", s.Format4(), err)
			}
			var out []string
			for _, r := range rs {
				out = append(out, r.CID+"/"+r.ProducerID+"/"+r.Standard+"/"+r.PeerID)
			}
			sort.Strings(out)
			return out
		}
		// Format 1 stored omm[50] before refusing its tag (a tag without a
		// provider); format 4 refuses the whole write.
		refused := ComputeCID(sc.omm[50]) + "/"
		var f1Routed []string
		for _, r := range routed(legacy) {
			if !strings.HasPrefix(r, refused) {
				f1Routed = append(f1Routed, r)
			}
		}
		if a, b := f1Routed, routed(f4); fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("QueryRoutedByStandard: format 4 %d rows, format 1 %d (the sets differ):\n format 4 %v\n format 1 %v", len(b), len(a), b, a)
		}
		if _, err := f4.Query("OMM.fbs", "1=1"); !errors.Is(err, ErrFormat4Unsupported) {
			t.Fatalf("Query with a WHERE: %v", err)
		}
		for _, s := range []*FlatSQLStore{legacy, f4} {
			got, err := s.QueryAllBounded("OMM.fbs", 5, 0)
			if err != nil || len(got) != 5 {
				t.Fatalf("QueryAllBounded format4=%v: %d, %v", s.Format4(), len(got), err)
			}
		}
	})
}

// §5.5 and §2.4: which store each selector opens, and every refusal before
// a file is touched.
func TestFormat4StoreSelectionAndRefusals(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		t.Setenv(checkpointIntervalEnv, "0")
		v, err := sds.NewValidator(nil)
		if err != nil {
			t.Fatal(err)
		}
		listing := func(dir string) []string {
			var out []string
			_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
				if err == nil {
					out = append(out, strings.TrimPrefix(p, dir))
				}
				return nil
			})
			return out
		}

		// A fresh format-4 store: markers, the control database, and
		// control.flatsqldb as a directory.
		dir := t.TempDir()
		s := openFormat4ForTest(t, dir)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		m, err := marker.Read(dir)
		if err != nil || !m.Activated() || !m.LegacyControlDir || m.LegacyControlFile {
			t.Fatalf("a fresh format-4 store: %+v, %v", m, err)
		}
		if _, err := os.Stat(filepath.Join(dir, marker.Dir, format4ControlDBName)); err != nil {
			t.Fatalf("the control database: %v", err)
		}
		// It reopens (OpenExisting), under the alias too.
		t.Setenv(format4.FormatEnv, " SQLite ")
		s, err = NewFlatSQLStore(dir, v)
		if err != nil || !s.Format4() {
			t.Fatalf("reopen with SDN_STORE_FORMAT=sqlite: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		// Formats 1 and 2 refuse it before touching a file.
		before := listing(dir)
		for _, sel := range []string{"", "2"} {
			t.Setenv(format4.FormatEnv, sel)
			if _, err := NewFlatSQLStore(dir, v); !errors.Is(err, ErrFormat4Store) {
				t.Fatalf("SDN_STORE_FORMAT=%q on a format-4 store: %v", sel, err)
			}
			if after := listing(dir); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("SDN_STORE_FORMAT=%q touched the store:\n before %v\n after %v", sel, before, after)
			}
		}

		// Format 4 refuses an unmigrated format-1 store.
		t.Setenv(format4.FormatEnv, "")
		legacyDir := t.TempDir()
		l := reopenDeferred(t, legacyDir)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv(format4.FormatEnv, "4")
		if _, err := NewFlatSQLStore(legacyDir, v); !errors.Is(err, format4.ErrNotMigrated) {
			t.Fatalf("format 4 on an unmigrated format-1 store: %v", err)
		}
		if _, err := os.Stat(filepath.Join(legacyDir, marker.Dir, marker.StoreFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("format 4 wrote markers into a format-1 store: %v", err)
		}
		// ... and a format-2 store by name.
		f2Dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(f2Dir, "fsql2"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f2Dir, "fsql2", "STORE"), make([]byte, 64), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFlatSQLStore(f2Dir, v); !errors.Is(err, format4.ErrWrongFormat) {
			t.Fatalf("format 4 on a format-2 store: %v", err)
		}

		// A store-migrate activation cut short after the markers (§2.3): the
		// legacy control file is still there. The daemon finishes it.
		half := t.TempDir()
		var uuid [16]byte
		uuid[0] = 7
		if err := os.MkdirAll(filepath.Join(half, marker.Dir), 0o700); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UnixMilli()
		for name, b := range map[string][]byte{marker.MigratedFile: marker.MigratedBytes(uuid, now), marker.StoreFile: marker.StoreBytes(uuid, now, 5000, 1)} {
			if err := os.WriteFile(filepath.Join(half, marker.Dir, name), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(half, marker.LegacyControl), []byte("format 1"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err = NewFlatSQLStore(half, v)
		if err != nil {
			t.Fatalf("open a half-activated store: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(filepath.Join(half, marker.PreFormat4Dir, marker.LegacyControl)); err != nil || string(b) != "format 1" {
			t.Fatalf("the legacy control file was not kept in %s: %q, %v", marker.PreFormat4Dir, b, err)
		}
		if m, err := marker.Read(half); err != nil || !m.LegacyControlDir || m.NeedsFinish() {
			t.Fatalf("after the finish: %+v, %v", m, err)
		}
	})
}

// No record path takes s.mu (contract §5.4): with the store write lock held,
// every record read and write still completes.
func TestFormat4RecordPathsTakeNoStoreLock(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		s := openFormat4ForTest(t, t.TempDir())
		defer s.Close()
		sc := newF2Script()
		tags := SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b1"}
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[:50], "peer", nil, tags); err != nil {
			t.Fatal(err)
		}
		cid := ComputeCID(sc.omm[3])
		ops := map[string]func() error{
			"Store":      func() error { _, err := s.Store("OMM.fbs", sc.omm[60], "peer", nil); return err },
			"StoreBatch": func() error { _, err := s.StoreBatch("OMM.fbs", sc.omm[61:70], "peer", nil); return err },
			"Upsert": func() error {
				return s.UpsertSourceTags("OMM.fbs", cid, SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b2"})
			},
			"GetRecord": func() error { _, err := s.GetRecord("OMM.fbs", cid); return err },
			"Tags":      func() error { _, err := s.GetSourceTags("OMM.fbs", cid); return err },
			"Count":     func() error { _, err := s.Count("OMM.fbs"); return err },
			"Snapshot": func() error {
				_, _, err := s.RawRecordSnapshot(RawRecordQuery{SchemaName: "OMM.fbs", SourceName: "s"})
				return err
			},
			"Raw": func() error {
				_, err := s.QueryRawRecordRefs(RawRecordQuery{SchemaName: "OMM.fbs", UseRowIDCursor: true})
				return err
			},
			"Window": func() error {
				_, err := s.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
				return err
			},
			"IndexPage": func() error { _, _, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "OMM.fbs"}); return err },
			"Epoch": func() error {
				_, err := s.CountEpochRecords(EpochRecordQuery{SchemaName: "OMM.fbs", Profile: EpochProfileDay, Day: "2026-09-01"})
				return err
			},
			"Progress":    func() error { _, err := s.SourceBatchProgress(); return err },
			"Counts":      func() error { _, err := s.SourceRecordCounts(); return err },
			"Live":        func() error { _, err := s.LiveRecordBytes(); return err },
			"Ranges":      func() error { _, err := s.SchemaDateRanges(); return err },
			"Peer":        func() error { _, err := s.PeerStorageBytes("peer"); return err },
			"Reconcile":   func() error { _, err := s.ReconcileSourceBatch("OMM.fbs", "p", "s", "b1", false); return err },
			"Fingerprint": func() error { _, _, err := s.DatasetPublicationSetFingerprint("OMM.fbs", "p", "s", ""); return err },
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for name, op := range ops {
			done := make(chan error, 1)
			go func() { done <- op() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s waited on the store lock", name)
			}
		}
	})
}

// The sync filter's grammar compiles to the engine's predicates on the
// fields format 1 indexed.
func TestFormat4SyncFilterPredicates(t *testing.T) {
	cases := map[string]string{
		"NORAD_CAT_ID >= 20010":                          "[{10 6 [int 20010]}]",
		"EPOCH BETWEEN 1788220800 AND 1788240000":        "[{1 7 [int 1788220800 int 1788240000]}]",
		"EPOCH_DAY LIKE '2026-09-%'":                     "[{4 8 [text 2026-09-%]}]",
		"OBJECT_TYPE != 'payload'":                       "[{12 2 [text PAYLOAD]}]",
		"x.OBJECT_ID = '2026-111A' AND NORAD_CAT_ID < 5": "[{11 1 [text 2026-111A]} {10 3 [int 5]}]",
		"SOURCE_TIMESTAMP <> 0":                          "[{2 2 [int 0]}]",
		"OPS_STATUS_CODE LIKE 'oper%'":                   "[{13 8 [text OPER%]}]",
	}
	for in, want := range cases {
		got, err := f4SyncPreds(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		var b strings.Builder
		b.WriteString("[")
		for i, p := range got {
			if i > 0 {
				b.WriteString(" ")
			}
			fmt.Fprintf(&b, "{%d %d [", p.Field, p.Op)
			for j, v := range p.Values {
				if j > 0 {
					b.WriteString(" ")
				}
				if v.Type == format2.CellInt {
					fmt.Fprintf(&b, "int %d", v.I)
				} else {
					fmt.Fprintf(&b, "text %s", v.B)
				}
			}
			b.WriteString("]}")
		}
		b.WriteString("]")
		if b.String() != want {
			t.Fatalf("%s: %s, want %s", in, b.String(), want)
		}
	}
	for _, bad := range []string{"OBJECT_ID > 'a'", "OBJECT_TYPE BETWEEN 'A' AND 'B'", "NORAD_CAT_ID LIKE '1%'", "FOO = 1", "EPOCH = 'nope'"} {
		if _, err := f4SyncPreds(bad); err == nil {
			t.Fatalf("%s compiled", bad)
		}
	}
}

// Field-sealed records (KMF): the engine stores the sealed bytes, the CID is
// the plaintext's, and reads open them as format 1 does.
func TestFormat4SealedRecordsMatchFormat1(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		legacy := reopenDeferred(t, t.TempDir())
		defer legacy.Close()
		f4 := openFormat4ForTest(t, t.TempDir())
		defer f4.Close()
		var recs [][]byte
		for i := 0; i < 24; i++ {
			key := make([]byte, 32)
			for j := range key {
				key[j] = byte(0x5a ^ (i*31 + j*7))
			}
			recs = append(recs, buildKMFRecordForTest(t, fmt.Sprintf("kmf-key-%02d", i), key, uint32(i+1)))
		}
		for _, s := range []*FlatSQLStore{legacy, f4} {
			n, err := s.StoreBatchWithSourceTags("KMF.fbs", recs, "source:keys", nil, SourceTags{ProviderID: "local", SourceName: "keys", BatchID: "k-1"})
			if err != nil || n != len(recs) {
				t.Fatalf("KMF ingest format4=%v: %d inserted, %v", s.Format4(), n, err)
			}
		}
		for _, q := range []IndexedRecordQuery{{SchemaName: "KMF.fbs", Limit: 1000}, {SchemaName: "KMF.fbs", Limit: 7, Offset: 5}, {SchemaName: "KMF.fbs", Limit: 50, OrderByCID: true}} {
			a, err := legacy.QueryIndexedRecords(q)
			if err != nil {
				t.Fatal(err)
			}
			b, err := f4.QueryIndexedRecords(q)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(f4Rows(a)) != fmt.Sprint(f4Rows(b)) {
				t.Fatalf("KMF window %+v:\n format 4 %v\n format 1 %v", q, cidSeq(b), cidSeq(a))
			}
		}
		refs := make([]RawRecordRef, len(recs))
		for i, d := range recs {
			got, err := f4.GetRecord("KMF.fbs", ComputeCID(d))
			if err != nil || string(got.Data) != string(d) {
				t.Fatalf("KMF GetRecord: %v", err)
			}
			refs[i] = RawRecordRef{CID: ComputeCID(d)}
		}
		// Datasync reads carry the STORED (sealed) bytes.
		stored, err := f4.QueryRawRecordRefsByRefs("KMF.fbs", refs)
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range stored {
			if string(r.Data) == string(recs[i]) || !encfieldIsSealed(r.Data) {
				t.Fatalf("KMF ref %d: the stored bytes are not sealed", i)
			}
		}
	})
}

// The publication log (QueryLogEntries): the log index is a control table
// and its entries are PLOG records, which format 4 reads from the engine.
func TestFormat4LogEntriesMatchFormat1(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		legacy := reopenDeferred(t, t.TempDir())
		defer legacy.Close()
		f4 := openFormat4ForTest(t, t.TempDir())
		defer f4.Close()
		spec, err := format4.TypeSpecFor("PLOG.fbs")
		if err != nil {
			t.Fatal(err)
		}
		entry := func(seq int) []byte {
			b := flatbuffers.NewBuilder(128)
			s := b.CreateString(fmt.Sprintf("entry-%d", seq))
			b.StartObject(2)
			b.PrependUint64Slot(0, uint64(seq), 0)
			b.PrependUOffsetTSlot(1, s, 0)
			b.FinishWithFileIdentifier(b.EndObject(), spec.FID[:])
			return append([]byte(nil), b.FinishedBytes()...)
		}
		for _, s := range []*FlatSQLStore{legacy, f4} {
			for seq := 1; seq <= 6; seq++ {
				cid, err := s.Store("PLOG.fbs", entry(seq), "12D3KooWPublisher", nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.UpsertLogIndex("12D3KooWPublisher", "OMM.fbs", uint64(seq), fmt.Sprintf("h%d", seq), "bafkreirecord", cid, "2026-09-01", int64(1000+seq)); err != nil {
					t.Fatal(err)
				}
			}
			// An index row whose entry was never stored is left out.
			if err := s.UpsertLogIndex("12D3KooWPublisher", "OMM.fbs", 7, "h7", "bafkreirecord", ComputeCID(entry(99)), "2026-09-01", 1007); err != nil {
				t.Fatal(err)
			}
		}
		for _, q := range [][2]int{{0, 100}, {2, 3}, {6, 10}} {
			a, err := legacy.QueryLogEntries("12D3KooWPublisher", "OMM.fbs", uint64(q[0]), q[1])
			if err != nil {
				t.Fatal(err)
			}
			b, err := f4.QueryLogEntries("12D3KooWPublisher", "OMM.fbs", uint64(q[0]), q[1])
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", a) != fmt.Sprintf("%x", b) || (q[0] == 0 && len(b) != 6) {
				t.Fatalf("log entries after %d (limit %d): format 4 %d, format 1 %d", q[0], q[1], len(b), len(a))
			}
		}
	})
}
