package storage

// The daemon's record API on store format 4 answers what format 1 answers
// (contract §6, backend): the same writes go through the storage API into a
// format-1 store and a fresh format-4 store, and every record read the node
// serves from is compared, with the accepted format-4 shapes (A16 and C-10:
// one row per record; C-12: the lowest-pid copy; A2: supersede per
// partition) normalized as format 2's parity test normalizes them.
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

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
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

// f4Script is the format-4 additions to the shared write sequence: a third
// producer's copies under a tag carrying every lane field, then the same tag
// instance again with a new source URL (C-3: the latest write's URL).
var f4ArchiveTag = SourceTags{ProviderID: "space-track", SourceName: "gp-archive", SourceURL: "https://archive.example/gp/1", BatchID: "a-1",
	ContentKeyID: "ck-1", ProducerPeerID: "archive-peer", ProducerPublicKey: "archive-key"}

const f4ArchivePeer = "16Uiu2HAmArchivePeerForFormat4Tests"

func runF4Additions(t *testing.T, s *FlatSQLStore, sc f2Script) {
	t.Helper()
	if n, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[0:10], f4ArchivePeer, []byte{9, 9}, f4ArchiveTag); err != nil || n != 0 {
		t.Fatalf("archive copies: %d inserted, %v", n, err)
	}
	moved := f4ArchiveTag
	moved.SourceURL = "https://archive.example/gp/2"
	if n, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[0:5], f4ArchivePeer, []byte{9, 9}, moved); err != nil || n != 0 {
		t.Fatalf("archive URL move: %d inserted, %v", n, err)
	}
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

// f4Collapse keeps each CID's first row (A16: format 1 repeated a record per
// tag row and per holding table).
func f4Collapse(recs []*Record) []*Record {
	seen := map[string]bool{}
	out := make([]*Record, 0, len(recs))
	for _, r := range recs {
		if !seen[r.CID] {
			seen[r.CID] = true
			out = append(out, r)
		}
	}
	return out
}

func f4Near(a, b time.Time) bool {
	d := a.Sub(b)
	return d >= -3*time.Second && d <= 3*time.Second
}

func TestFormat4StoreAPIMatchesFormat1(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		legacy := reopenDeferred(t, t.TempDir())
		defer legacy.Close()
		f4 := openFormat4ForTest(t, t.TempDir())
		defer f4.Close()

		script := newF2Script()
		scL := runF2Script(t, legacy, script)
		scF := runF2Script(t, f4, script)
		if scL.reconciled != scF.reconciled {
			t.Fatalf("ReconcileSourceBatch: format 4 %+v, format 1 %+v", scF.reconciled, scL.reconciled)
		}
		runF4Additions(t, legacy, script)
		runF4Additions(t, f4, script)

		all := append(append([][]byte{}, script.omm...), script.cat...)
		schemaOf := func(d []byte) string {
			if CAT.CATBufferHasIdentifier(d) {
				return "CAT.fbs"
			}
			return "OMM.fbs"
		}

		t.Run("GetRecord", func(t *testing.T) {
			for _, d := range all {
				cid, schema := ComputeCID(d), schemaOf(d)
				a, errA := legacy.GetRecord(schema, cid)
				b, errB := f4.GetRecord(schema, cid)
				if (errA == nil) != (errB == nil) {
					t.Fatalf("GetRecord %s %s: format 1 %v, format 4 %v", schema, cid, errA, errB)
				}
				if errA != nil {
					if errA.Error() != errB.Error() {
						t.Fatalf("GetRecord %s: format 1 %q, format 4 %q", cid, errA, errB)
					}
					continue
				}
				if string(a.Data) != string(b.Data) || a.CID != b.CID || a.RecordLength != b.RecordLength || a.RowID != b.RowID {
					t.Fatalf("GetRecord %s: record differs:\n f4 %+v\n f1 %+v", cid, b, a)
				}
				if !f4Near(a.Timestamp, b.Timestamp) || a.Timestamp.Location().String() != b.Timestamp.Location().String() {
					t.Fatalf("GetRecord %s: timestamp %v, format 1 %v", cid, b.Timestamp, a.Timestamp)
				}
			}
			if _, err := f4.GetRecord("OMM.fbs", "bafkreinotacid"); err == nil || err.Error() != "not found: bafkreinotacid" {
				t.Fatalf("a malformed CID: %v", err)
			}
		})

		t.Run("Count", func(t *testing.T) {
			for _, schema := range []string{"OMM.fbs", "CAT.fbs", "MPE.fbs"} {
				a, errA := legacy.Count(schema)
				b, errB := f4.Count(schema)
				if errA != nil || errB != nil || a != b {
					t.Fatalf("Count %s: format 1 %d (%v), format 4 %d (%v)", schema, a, errA, b, errB)
				}
			}
		})

		norad := uint32(20150)
		base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		from, to := base.Add(3*time.Hour), base.Add(9*time.Hour)

		t.Run("IndexedWindows", func(t *testing.T) {
			windows := map[string]IndexedRecordQuery{
				"all":              {SchemaName: "OMM.fbs", Limit: 1000},
				"page":             {SchemaName: "OMM.fbs", Limit: 25, Offset: 70},
				"by cid":           {SchemaName: "OMM.fbs", Limit: 30, Offset: 11, OrderByCID: true},
				"provider":         {SchemaName: "OMM.fbs", ProviderID: "space-data-network-01", Limit: 1000},
				"source and batch": {SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: "gp-002", Limit: 1000},
				"batch only":       {SchemaName: "OMM.fbs", BatchID: "m-1", Limit: 17, Offset: 5},
				"archive":          {SchemaName: "OMM.fbs", ProviderID: "space-track", SourceName: "gp-archive", Limit: 1000},
				"norad":            {SchemaName: "OMM.fbs", NoradCatID: &norad, Limit: 10},
				"entity":           {SchemaName: "OMM.fbs", EntityID: "2026-150A", Limit: 10},
				"time range":       {SchemaName: "OMM.fbs", From: &from, To: &to, Limit: 1000},
				"day":              {SchemaName: "OMM.fbs", Day: "2026-09-01", Limit: 1000},
				"empty standard":   {SchemaName: "RFM.fbs", Limit: 10},
				"cat":              {SchemaName: "CAT.fbs", Limit: 1000},
				"cat source page":  {SchemaName: "CAT.fbs", SourceName: "celestrak-satcat", Limit: 12, Offset: 7},
				"cat payloads":     {SchemaName: "CAT.fbs", ObjectType: "PAYLOAD", Limit: 1000},
				"cat decayed":      {SchemaName: "CAT.fbs", OpsStatusCode: "DECAYED", Limit: 1000},
				"cat active":       {SchemaName: "CAT.fbs", ActivePayloads: true, Limit: 1000},
				"cat ca-ready":     {SchemaName: "CAT.fbs", CAReadyResidentSet: true, Limit: 1000},
				"omm object type":  {SchemaName: "OMM.fbs", ObjectType: "PAYLOAD", Limit: 10},
			}
			var names []string
			for n := range windows {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, name := range names {
				q := windows[name]
				a, err := legacy.QueryIndexedRecords(q)
				if err != nil {
					t.Fatalf("%s: format 1: %v", name, err)
				}
				b, err := f4.QueryIndexedRecords(q)
				if err != nil {
					t.Fatalf("%s: format 4: %v", name, err)
				}
				if fmt.Sprint(f4Rows(f4Collapse(a))) != fmt.Sprint(f4Rows(b)) {
					t.Fatalf("window %s:\n f4 %v\n f1 %v", name, cidSeq(b), cidSeq(a))
				}
				for i := range b {
					if a[i].RecordLength != b[i].RecordLength || a[i].RowID != b[i].RowID || !a[i].MaterializedAt.Equal(b[i].MaterializedAt) ||
						a[i].Timestamp.Location().String() != b[i].Timestamp.Location().String() {
						t.Fatalf("window %s row %d: f4 %+v, f1 %+v", name, i, b[i], a[i])
					}
				}
				for _, budget := range []int64{2_000, 20_000} {
					na, ta, err := legacy.IndexedRecordWindowLimitForBytes(q, budget)
					if err != nil {
						t.Fatal(err)
					}
					nb, tb, err := f4.IndexedRecordWindowLimitForBytes(q, budget)
					if err != nil {
						t.Fatal(err)
					}
					if na != nb || ta != tb {
						t.Fatalf("window %s byte probe %d: format 4 (%d, %v), format 1 (%d, %v)", name, budget, nb, tb, na, ta)
					}
				}
			}
		})

		t.Run("RawRecords", func(t *testing.T) {
			raws := map[string]RawRecordQuery{
				"schema":        {SchemaName: "OMM.fbs"},
				"provider":      {SchemaName: "OMM.fbs", ProviderID: "space-data-network-01"},
				"source":        {SchemaName: "OMM.fbs", SourceName: "celestrak-gp"},
				"batch":         {SchemaName: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-002"},
				"archive":       {SchemaName: "OMM.fbs", SourceName: "gp-archive", ProducerPeerID: "archive-peer", ProducerPublicKey: "archive-key"},
				"batch+filter":  {SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: "gp-002", SyncFilter: "NORAD_CAT_ID >= 20010"},
				"batch+epoch":   {SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: "gp-001", SyncFilter: "EPOCH >= '2026-09-01T02:00:00Z'"},
				"producer peer": {SchemaName: "OMM.fbs", ProducerPeerID: "space-data-network-01"},
				"peer":          {SchemaName: "OMM.fbs", PeerID: "16Uiu2HAmRelayPeer"},
				"cid":           {SchemaName: "OMM.fbs", CID: ComputeCID(scL.omm[130])},
				"bad cid":       {SchemaName: "OMM.fbs", CID: "not-a-cid"},
				"norad filter":  {SchemaName: "OMM.fbs", SyncFilter: "NORAD_CAT_ID >= 20100 AND NORAD_CAT_ID < 20130"},
				"epoch filter":  {SchemaName: "OMM.fbs", SyncFilter: "EPOCH >= '2026-09-01T12:00:00Z'"},
				"epoch between": {SchemaName: "OMM.fbs", SyncFilter: "EPOCH BETWEEN 1788220800 AND 1788240000"},
				"day filter":    {SchemaName: "OMM.fbs", SyncFilter: "EPOCH_DAY = '2026-09-01'"},
				"day range":     {SchemaName: "OMM.fbs", SyncFilter: "EPOCH_DAY > '2026-08-31' AND EPOCH_DAY <= '2026-09-01'"},
				"day between":   {SchemaName: "OMM.fbs", SyncFilter: "EPOCH_DAY BETWEEN '2026-08-30' AND '2026-09-01'"},
				"day ne":        {SchemaName: "OMM.fbs", SyncFilter: "EPOCH_DAY != '2026-09-01'"},
				"day like":      {SchemaName: "OMM.fbs", SyncFilter: "EPOCH_DAY LIKE '2026-09-%'"},
				"object id":     {SchemaName: "OMM.fbs", SyncFilter: "OBJECT_ID = '2026-111A'"},
				"cat type":      {SchemaName: "CAT.fbs", SyncFilter: "OBJECT_TYPE = 'PAYLOAD'"},
				"cat status ne": {SchemaName: "CAT.fbs", SyncFilter: "OPS_STATUS_CODE != 'OPERATIONAL'"},
				"cat type like": {SchemaName: "CAT.fbs", SyncFilter: "OBJECT_TYPE LIKE 'PAY%'"},
				"cat schema":    {SchemaName: "CAT.fbs"},
				"source+filter": {SchemaName: "OMM.fbs", SourceName: "mirror", SyncFilter: "NORAD_CAT_ID < 20180"},
				"ts filter":     {SchemaName: "OMM.fbs", SyncFilter: "SOURCE_TIMESTAMP > 0"},
			}
			var names []string
			for n := range raws {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, name := range names {
				q := raws[name]
				ca, ha, err := legacy.RawRecordSnapshot(q)
				if err != nil {
					t.Fatalf("%s: format 1 snapshot: %v", name, err)
				}
				cb, hb, err := f4.RawRecordSnapshot(q)
				if err != nil {
					t.Fatalf("%s: format 4 snapshot: %v", name, err)
				}
				// Format 1 counts a tag-filtered record once per matching tag row
				// (A16); its pages say how many records that is.
				pages := func(s *FlatSQLStore) []*Record {
					var out []*Record
					q := q
					q.UseRowIDCursor, q.Limit = true, 23
					for {
						page, err := s.QueryRawRecordRefs(q)
						if err != nil {
							t.Fatalf("%s: pages: %v", name, err)
						}
						if len(page) == 0 {
							break
						}
						out = append(out, page...)
						q.AfterRowID = page[len(page)-1].RowID
					}
					return out
				}
				pa, pb := pages(legacy), pages(f4)
				if n := int64(len(f4Collapse(pa))); cb != n || (ca != n && ca < n) {
					t.Fatalf("count %s: format 4 %d, format 1 %d over %d records", name, cb, ca, n)
				}
				for i := 1; i < len(pb); i++ {
					if pb[i].RowID <= pb[i-1].RowID {
						t.Fatalf("datasync %s: format-4 page order is not seq order at %d (%d after %d)", name, i, pb[i].RowID, pb[i-1].RowID)
					}
				}
				if hb.MaxRowID < pbMaxRowID(pb) {
					t.Fatalf("head %s: MaxRowID %d below a page's row %d", name, hb.MaxRowID, pbMaxRowID(pb))
				}
				var bytes int64
				for _, r := range pb {
					bytes += r.RecordLength
				}
				if hb.TotalBytes != bytes {
					t.Fatalf("head %s: format 4 %d bytes, its records %d", name, hb.TotalBytes, bytes)
				}
				if ca == int64(len(pa)) && ha.TotalBytes != hb.TotalBytes {
					t.Fatalf("head %s: format 4 %d bytes, format 1 %d", name, hb.TotalBytes, ha.TotalBytes)
				}
				a, b := cidSeq(pa), cidSeq(pb)
				sort.Strings(a)
				sort.Strings(b)
				if fmt.Sprint(a) != fmt.Sprint(b) {
					t.Fatalf("datasync %s: format 4 %d records, format 1 %d (the sets differ)", name, len(b), len(a))
				}
				// Every format-4 row's tag is one of the record's format-1 tag rows
				// (with its URL, content key and time), and meets the filter.
				f1Tags := map[string][]*Record{}
				for _, r := range pa {
					f1Tags[r.CID] = append(f1Tags[r.CID], r)
				}
				for _, r := range pb {
					if r.SourceTags == (SourceTags{}) {
						for _, x := range f1Tags[r.CID] {
							if x.SourceTags != (SourceTags{}) {
								t.Fatalf("datasync %s %s: format 4 projects no tag, format 1 %+v", name, r.CID, x.SourceTags)
							}
						}
						continue
					}
					found := false
					for _, x := range f1Tags[r.CID] {
						if x.SourceTags == r.SourceTags && f4Near(x.MaterializedAt, r.MaterializedAt) {
							found = true
						}
					}
					if !found {
						t.Fatalf("datasync %s %s: format-4 tag %+v at %v is none of format 1's", name, r.CID, r.SourceTags, r.MaterializedAt)
					}
					if !rawRecordMatchesRef(r, RawRecordRef{ProviderID: q.ProviderID, SourceName: q.SourceName, BatchID: q.BatchID,
						ProducerPeerID: q.ProducerPeerID, ProducerPublicKey: q.ProducerPublicKey}) {
						t.Fatalf("datasync %s %s: the projected tag %+v does not meet the filter", name, r.CID, r.SourceTags)
					}
				}
			}
		})

		t.Run("RefsByRefs", func(t *testing.T) {
			var refs []RawRecordRef
			for _, i := range []int{0, 3, 7, 130, 200, 221, 230} {
				refs = append(refs, RawRecordRef{CID: ComputeCID(script.omm[i])})
			}
			refs = append(refs, RawRecordRef{CID: ComputeCID(script.omm[4]), SourceName: "gp-archive", ProducerPublicKey: "archive-key"})
			refs = append(refs, RawRecordRef{CID: ComputeCID(script.omm[130]), PeerID: "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"})
			a, err := legacy.QueryRawRecordRefsByRefs("OMM.fbs", refs)
			if err != nil {
				t.Fatal(err)
			}
			b, err := f4.QueryRawRecordRefsByRefs("OMM.fbs", refs)
			if err != nil {
				t.Fatal(err)
			}
			for i := range refs {
				if a[i].CID != b[i].CID || string(a[i].Data) != string(b[i].Data) || !rawRecordMatchesRef(b[i], refs[i]) {
					t.Fatalf("ref %d %+v: format 4 %+v, format 1 %+v", i, refs[i], b[i], a[i])
				}
			}
			if got := b[7].SourceTags; got.SourceURL != "https://archive.example/gp/2" || got.ContentKeyID != "ck-1" {
				t.Fatalf("archive tag: %+v (C-3: the latest write's URL and the content key)", got)
			}
			if _, err := f4.QueryRawRecordRefsByRefs("OMM.fbs", []RawRecordRef{{CID: ComputeCID(script.omm[1]), SourceName: "nowhere"}}); err == nil {
				t.Fatal("a ref no tag meets resolved")
			}
		})

		t.Run("Tags", func(t *testing.T) {
			for _, i := range []int{0, 4, 130, 200, 221, 230, 235} {
				cid := ComputeCID(script.omm[i])
				a, errA := legacy.GetSourceTags("OMM.fbs", cid)
				b, errB := f4.GetSourceTags("OMM.fbs", cid)
				if (errA == nil) != (errB == nil) {
					t.Fatalf("GetSourceTags %d: format 1 %v, format 4 %v", i, errA, errB)
				}
				if errA == nil && a != b {
					t.Fatalf("GetSourceTags %d: format 4 %+v, format 1 %+v", i, b, a)
				}
			}
			var cids []string
			for _, d := range script.omm {
				cids = append(cids, ComputeCID(d))
			}
			a, err := legacy.sourceTagsForCIDs("OMM.fbs", cids)
			if err != nil {
				t.Fatal(err)
			}
			b, err := f4.sourceTagsForCIDs("OMM.fbs", cids)
			if err != nil {
				t.Fatal(err)
			}
			if len(a) != len(b) {
				t.Fatalf("sourceTagsForCIDs: format 4 %d records, format 1 %d", len(b), len(a))
			}
			for c, ta := range a {
				if b[c] != ta {
					// Format 1 picks one of a record's tag rows by its plan; the
					// newest is what format 4 reports.
					all, _ := legacy.QueryRawRecordRefs(RawRecordQuery{SchemaName: "OMM.fbs", CID: c, Limit: 100})
					ok := false
					for _, r := range all {
						ok = ok || r.SourceTags == b[c]
					}
					if !ok {
						t.Fatalf("sourceTagsForCIDs %s: format 4 %+v is none of format 1's", c, b[c])
					}
				}
			}
		})

		t.Run("Summaries", func(t *testing.T) {
			// Format 1's source summary keeps a superseded CAT record's bytes
			// (format 2's parity test explains the drift): its CAT lanes are
			// corrected to the bytes of the records they hold.
			catBytes := func(recs [][]byte) (n int64) {
				for _, d := range recs {
					n += int64(len(d))
				}
				return
			}
			trueCAT := map[string]int64{"sc-1": catBytes(script.cat[8:30]), "sc-2": catBytes(script.cat[30:])}
			sa, err := legacy.DataSummary()
			if err != nil {
				t.Fatal(err)
			}
			for i := range sa.Sources {
				if sa.Sources[i].SchemaName == "CAT.fbs" {
					if want, ok := trueCAT[sa.Sources[i].BatchID]; ok && want != sa.Sources[i].TotalBytes {
						delta := sa.Sources[i].TotalBytes - want
						sa.Sources[i].TotalBytes = want
						sa.TotalBytes -= delta
						for j := range sa.Schemas {
							if sa.Schemas[j].SchemaName == "CAT.fbs" {
								sa.Schemas[j].TotalBytes -= delta
							}
						}
					}
				}
			}
			sb, err := f4.DataSummary()
			if err != nil {
				t.Fatal(err)
			}
			if sa.TotalRecords != sb.TotalRecords || sa.TotalBytes != sb.TotalBytes || fmt.Sprint(sa.Schemas) != fmt.Sprint(sb.Schemas) ||
				fmt.Sprint(sa.Sources) != fmt.Sprint(sb.Sources) {
				t.Fatalf("DataSummary:\n format 4 %+v\n format 1 %+v", *sb, *sa)
			}
			pa, err := legacy.SourceBatchProgress()
			if err != nil {
				t.Fatal(err)
			}
			pb, err := f4.SourceBatchProgress()
			if err != nil {
				t.Fatal(err)
			}
			progressKey := func(ps []SourceBatchProgress) string {
				var b strings.Builder
				for _, p := range ps {
					if want, ok := trueCAT[p.BatchID]; ok && p.SchemaName == "CAT.fbs" {
						p.TotalBytes = want
					}
					fmt.Fprintf(&b, "%s/%s/%s/%s=%d,%d ", p.SchemaName, p.ProviderID, p.SourceName, p.BatchID, p.Count, p.TotalBytes)
				}
				return b.String()
			}
			if progressKey(pa) != progressKey(pb) {
				t.Fatalf("SourceBatchProgress:\n format 4 %s\n format 1 %s", progressKey(pb), progressKey(pa))
			}
			for i := range pb {
				if !f4Near(time.Unix(pa[i].FirstSeenUnix, 0), time.Unix(pb[i].FirstSeenUnix, 0)) ||
					!f4Near(time.Unix(pa[i].LastSeenUnix, 0), time.Unix(pb[i].LastSeenUnix, 0)) {
					t.Fatalf("SourceBatchProgress %d times: format 4 %+v, format 1 %+v", i, pb[i], pa[i])
				}
			}
			ppa, err := legacy.ProducerSourceProgress()
			if err != nil {
				t.Fatal(err)
			}
			ppb, err := f4.ProducerSourceProgress()
			if err != nil {
				t.Fatal(err)
			}
			ppKey := func(ps []ProducerSourceProgress) []string {
				var out []string
				for _, p := range ps {
					if p.SchemaName == "CAT.fbs" {
						p.TotalBytes = trueCAT["sc-1"] + trueCAT["sc-2"]
					}
					out = append(out, fmt.Sprintf("%s|%s|%s|%s batches=%d n=%d b=%d", p.ProducerPeerID, p.SchemaName, p.ProviderID, p.SourceName,
						p.BatchCount, p.Count, p.TotalBytes))
				}
				sort.Strings(out)
				return out
			}
			if a, b := ppKey(ppa), ppKey(ppb); fmt.Sprint(a) != fmt.Sprint(b) {
				t.Fatalf("ProducerSourceProgress:\n format 4 %v\n format 1 %v", b, a)
			}
			rca, err := legacy.SourceRecordCounts()
			if err != nil {
				t.Fatal(err)
			}
			rcb, err := f4.SourceRecordCounts()
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(rca) != fmt.Sprint(rcb) {
				t.Fatalf("SourceRecordCounts: format 4 %v, format 1 %v", rcb, rca)
			}
			// RECONCILE is per partition (A2): the celestrak partition's copies of
			// omm[120:160] lose their last celestrak tag and go, while the
			// mirror's copies keep the records live; format 1 left them in the
			// celestrak table untagged.
			var keptByF1 int64
			for _, d := range script.omm[120:160] {
				keptByF1 += int64(len(d))
			}
			lra, err := legacy.LiveRecordBytes()
			if err != nil {
				t.Fatal(err)
			}
			lrb, err := f4.LiveRecordBytes()
			if err != nil {
				t.Fatal(err)
			}
			if lra-keptByF1 != lrb {
				t.Fatalf("LiveRecordBytes: format 4 %d, format 1 %d less %d kept untagged by its reconcile", lrb, lra, keptByF1)
			}
			dra, err := legacy.SchemaDateRanges()
			if err != nil {
				t.Fatal(err)
			}
			drb, err := f4.SchemaDateRanges()
			if err != nil {
				t.Fatal(err)
			}
			for i := range dra {
				if dra[i].Schema == "OMM.fbs" {
					dra[i].TotalBytes -= keptByF1
				}
			}
			rangeKey := func(rs []SchemaDateRange) []string {
				var out []string
				for _, r := range rs {
					f := func(t *time.Time) string {
						if t == nil {
							return "-"
						}
						return t.UTC().Format(time.RFC3339)
					}
					out = append(out, fmt.Sprintf("%s n=%d %s..%s %dB", r.Schema, r.RecordCount, f(r.OldestEpoch), f(r.NewestEpoch), r.TotalBytes))
				}
				return out
			}
			if a, b := rangeKey(dra), rangeKey(drb); fmt.Sprint(a) != fmt.Sprint(b) {
				t.Fatalf("SchemaDateRanges:\n format 4 %v\n format 1 %v", b, a)
			}
			for _, peer := range []string{"source:celestrak", "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", f4ArchivePeer, "", "nobody"} {
				a, err := legacy.PeerStorageBytes(peer)
				if err != nil {
					t.Fatal(err)
				}
				b, err := f4.PeerStorageBytes(peer)
				if err != nil {
					t.Fatal(err)
				}
				if peer == "source:celestrak" {
					a -= keptByF1
				}
				if a != b {
					t.Fatalf("PeerStorageBytes %q: format 4 %d, format 1 %d", peer, b, a)
				}
			}
			for _, lane := range []DatasetPublicationLane{{SchemaName: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp"},
				{SchemaName: "OMM.fbs", ProviderID: "space-track", SourceName: "gp-archive"}} {
				for _, batch := range []string{"gp-001", "gp-002", "a-1", "x"} {
					a, err := legacy.laneBatchHoldsRecords(lane, batch)
					if err != nil {
						t.Fatal(err)
					}
					b, err := f4.laneBatchHoldsRecords(lane, batch)
					if err != nil {
						t.Fatal(err)
					}
					if a != b {
						t.Fatalf("laneBatchHoldsRecords %+v %s: format 4 %v, format 1 %v", lane, batch, b, a)
					}
				}
				for _, imported := range []string{"", "gp-002", "a-1"} {
					a, err := legacy.laneHasOtherUnledgeredBatch(lane.SchemaName, lane.ProviderID, lane.SourceName, imported, nil)
					if err != nil {
						t.Fatal(err)
					}
					b, err := f4.laneHasOtherUnledgeredBatch(lane.SchemaName, lane.ProviderID, lane.SourceName, imported, nil)
					if err != nil {
						t.Fatal(err)
					}
					if a != b {
						t.Fatalf("laneHasOtherUnledgeredBatch %+v: format 4 %v, format 1 %v", lane, b, a)
					}
				}
			}
		})

		t.Run("IndexPages", func(t *testing.T) {
			for _, q := range []RecordIndexPageQuery{
				{SchemaName: "OMM.fbs", Limit: 20},
				{SchemaName: "OMM.fbs", SourceName: "mirror", Limit: 15, Offset: 10},
				{SchemaName: "OMM.fbs", ProviderID: "space-track", Limit: 15},
				{SchemaName: "OMM.fbs", NoradLike: "201", Limit: 50},
				{SchemaName: "CAT.fbs", Limit: 50},
				{SchemaName: "CAT.fbs", NoradLike: "10", Limit: 5, Offset: 2},
			} {
				ra, na, err := legacy.RecordIndexPage(q)
				if err != nil {
					t.Fatal(err)
				}
				rb, nb, err := f4.RecordIndexPage(q)
				if err != nil {
					t.Fatal(err)
				}
				key := func(rows []RecordIndexRow) string {
					var b strings.Builder
					for _, r := range rows {
						n, e := int64(-1), int64(-1)
						if r.NoradCatID != nil {
							n = *r.NoradCatID
						}
						if r.EpochUnix != nil {
							e = *r.EpochUnix
						}
						fmt.Fprintf(&b, "%d/%d/%s ", n, e, r.CID)
					}
					return b.String()
				}
				if na != nb || key(ra) != key(rb) {
					t.Fatalf("RecordIndexPage %+v: format 4 total %d %s\n format 1 total %d %s", q, nb, key(rb), na, key(ra))
				}
			}
		})

		t.Run("TaggedAndRecent", func(t *testing.T) {
			for _, q := range []SourceTagQuery{
				{SchemaName: "OMM.fbs", ProviderID: "space-data-network-01", Limit: 1000},
				{SchemaName: "OMM.fbs", SourceName: "gp-history", Limit: 1000},
				{SchemaName: "OMM.fbs", Limit: 1000},
			} {
				a, err := legacy.QuerySourceTaggedRecords(q)
				if err != nil {
					t.Fatal(err)
				}
				b, err := f4.QuerySourceTaggedRecords(q)
				if err != nil {
					t.Fatal(err)
				}
				as, bs := cidSeq(a), cidSeq(b)
				sort.Strings(as)
				sort.Strings(bs)
				if fmt.Sprint(as) != fmt.Sprint(bs) {
					t.Fatalf("QuerySourceTaggedRecords %+v: format 4 %d, format 1 %d", q, len(bs), len(as))
				}
				f4SameRecords(t, "QuerySourceTaggedRecords", a, b)
			}
			for _, schema := range []string{"OMM.fbs", "CAT.fbs"} {
				a, err := legacy.QueryRecentRecords(schema, 1000)
				if err != nil {
					t.Fatal(err)
				}
				b, err := f4.QueryRecentRecords(schema, 1000)
				if err != nil {
					t.Fatal(err)
				}
				as, bs := cidSeq(a), cidSeq(b)
				sort.Strings(as)
				sort.Strings(bs)
				if fmt.Sprint(as) != fmt.Sprint(bs) {
					t.Fatalf("QueryRecentRecords %s: format 4 %d, format 1 %d", schema, len(bs), len(as))
				}
				f4SameRecords(t, "QueryRecentRecords "+schema, a, b)
			}
			if _, err := f4.GetRecord("OMM.fbs", scF.deleted); err == nil {
				t.Fatal("a deleted record is still served")
			}
			if err := f4.Delete("OMM.fbs", scF.deleted); err == nil || err.Error() != "not found: "+scF.deleted {
				t.Fatalf("delete of a deleted record: %v", err)
			}
		})

		t.Run("Epoch", func(t *testing.T) {
			at := base.Add(10*time.Hour + 3*time.Minute + 30*time.Second)
			wfrom, wto := base.Add(2*time.Hour), base.Add(8*time.Hour)
			for _, q := range []EpochRecordQuery{
				{SchemaName: "OMM.fbs", Profile: EpochProfileDay, Day: "2026-09-01"},
				{SchemaName: "OMM.fbs", Profile: EpochProfileWindow, From: &wfrom, To: &wto},
				{SchemaName: "OMM.fbs", Profile: EpochProfileWindow, From: &wfrom, To: &wto, SourceName: "mirror"},
				{SchemaName: "OMM.fbs", Profile: EpochProfileAsOf, At: at},
				{SchemaName: "OMM.fbs", Profile: EpochProfileForward, At: at, Limit: 25},
				{SchemaName: "OMM.fbs", Profile: EpochProfileNearest, At: at},
				{SchemaName: "OMM.fbs", Profile: EpochProfileNearest, At: at, MaxDeltaSeconds: 3600, ProviderID: "space-data-network-01"},
				{SchemaName: "OMM.fbs", Profile: EpochProfileNearest, At: at, MaxDeltaSeconds: 600, Limit: 3},
				{SchemaName: "OMM.fbs", Profile: EpochProfileAsOf, At: at, NoradCatID: &norad},
			} {
				a, err := legacy.QueryEpochRecords(q)
				if err != nil {
					t.Fatalf("epoch %+v: format 1: %v", q, err)
				}
				b, err := f4.QueryEpochRecords(q)
				if err != nil {
					t.Fatalf("epoch %+v: format 4: %v", q, err)
				}
				key := func(ms []EpochRecordMatch) []string {
					var out []string
					seen := map[string]bool{}
					for _, m := range ms {
						if seen[m.Record.CID] {
							continue
						}
						seen[m.Record.CID] = true
						out = append(out, fmt.Sprintf("%s|%s|%d|%s|%d|%d", m.Record.CID, m.EntityKey, m.MatchedEpoch.Unix(), m.MatchType, m.DeltaSeconds,
							m.Record.RecordLength))
					}
					return out
				}
				if fmt.Sprint(key(a)) != fmt.Sprint(key(b)) {
					t.Fatalf("epoch %s %+v:\n format 4 %v\n format 1 %v", q.Profile, q, key(b), key(a))
				}
				na, err := legacy.CountEpochRecords(q)
				if err != nil {
					t.Fatal(err)
				}
				nb, err := f4.CountEpochRecords(q)
				if err != nil {
					t.Fatal(err)
				}
				if q.SourceName == "" && q.ProviderID == "" && na != nb {
					t.Fatalf("CountEpochRecords %s: format 4 %d, format 1 %d", q.Profile, nb, na)
				}
				if nb > na {
					t.Fatalf("CountEpochRecords %s: format 4 %d above format 1's %d", q.Profile, nb, na)
				}
			}
			for _, q := range []EpochRecordQuery{{SchemaName: "OMM.fbs"}, {SchemaName: "OMM.fbs", SourceName: "mirror"}} {
				ca, err := legacy.QueryEpochCoverage(q)
				if err != nil {
					t.Fatal(err)
				}
				cb, err := f4.QueryEpochCoverage(q)
				if err != nil {
					t.Fatal(err)
				}
				if fmt.Sprint(ca) != fmt.Sprint(cb) {
					t.Fatalf("QueryEpochCoverage %+v:\n format 4 %v\n format 1 %v", q, cb, ca)
				}
			}
		})

		t.Run("Exports", func(t *testing.T) {
			// A17: a publication's ResultSHA256 is its shard's.
			for _, q := range []IndexedRecordQuery{
				{SchemaName: "OMM.fbs", ProviderID: "space-data-network-01", SourceName: "mirror", BatchID: "m-1", Limit: 40, Offset: 20},
				{SchemaName: "CAT.fbs", Limit: 1000},
				{SchemaName: "OMM.fbs", Limit: 64, OrderByCID: true},
			} {
				ea, err := legacy.ExportDatasetWindow(t.TempDir(), q)
				if err != nil {
					t.Fatal(err)
				}
				eb, err := f4.ExportDatasetWindow(t.TempDir(), q)
				if err != nil {
					t.Fatal(err)
				}
				if ea.ResultSHA256 != eb.ResultSHA256 || ea.RecordCount != eb.RecordCount {
					t.Fatalf("export %+v: format 4 %s (%d), format 1 %s (%d)", q, eb.ResultSHA256, eb.RecordCount, ea.ResultSHA256, ea.RecordCount)
				}
			}
			for _, l := range [][4]string{{"OMM.fbs", "space-data-network-01", "mirror", ""}, {"OMM.fbs", "space-track", "gp-archive", "a-1"},
				{"OMM.fbs", "space-data-network-02", "celestrak-gp", ""}} {
				fa, na, err := legacy.DatasetPublicationSetFingerprint(l[0], l[1], l[2], l[3])
				if err != nil {
					t.Fatal(err)
				}
				fb, nb, err := f4.DatasetPublicationSetFingerprint(l[0], l[1], l[2], l[3])
				if err != nil {
					t.Fatal(err)
				}
				if fa != fb || na != nb {
					t.Fatalf("publication set fingerprint %v: format 4 %s (%d), format 1 %s (%d)", l, fb, nb, fa, na)
				}
			}
		})

		t.Run("FullTablePage", func(t *testing.T) {
			var got []*Record
			q := FullTablePageQuery{SchemaName: "OMM.fbs", Limit: 40, IncludeSource: true}
			for {
				page, err := f4.FullTablePageWithCursor(q)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Records) == 0 {
					break
				}
				got = append(got, page.Records...)
				q.Cursor = page.NextCursor
			}
			n, err := legacy.Count("OMM.fbs")
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(got)) != n || len(cidSeq(got)) != len(got) {
				t.Fatalf("full table pages: %d rows (%d records), format 1 counts %d", len(got), len(cidSeq(got)), n)
			}
			for i := 1; i < len(got); i++ {
				if got[i].RowID >= got[i-1].RowID {
					t.Fatalf("full table page order at %d", i)
				}
			}
		})
	})
}

func encfieldIsSealed(b []byte) bool { return encfield.IsSealed(b) }

// f4SameRecords checks each format-4 record against format 1's record of the
// same CID: the bytes, stored length, RowID, the timestamp's zone (format 1
// reports some reads in local time, some in UTC) and, when format 1 carries
// one tag row for the record, that tag.
func f4SameRecords(t *testing.T, what string, f1, f4 []*Record) {
	t.Helper()
	byCID := map[string][]*Record{}
	for _, r := range f1 {
		byCID[r.CID] = append(byCID[r.CID], r)
	}
	for _, r := range f4 {
		rows := byCID[r.CID]
		if len(rows) == 0 {
			t.Fatalf("%s: format 4 returned %s, format 1 did not", what, r.CID)
		}
		a := rows[0]
		if string(a.Data) != string(r.Data) || a.RecordLength != r.RecordLength || a.RowID != r.RowID ||
			a.Timestamp.Location().String() != r.Timestamp.Location().String() || !f4Near(a.Timestamp, r.Timestamp) {
			t.Fatalf("%s %s:\n f4 %+v\n f1 %+v", what, r.CID, *r, *a)
		}
		if len(rows) == 1 && (a.SourceTags != r.SourceTags || !f4Near(a.MaterializedAt, r.MaterializedAt) || a.MaterializedAt.IsZero() != r.MaterializedAt.IsZero()) {
			t.Fatalf("%s %s tag:\n f4 %+v %v\n f1 %+v %v", what, r.CID, r.SourceTags, r.MaterializedAt, a.SourceTags, a.MaterializedAt)
		}
	}
}

func pbMaxRowID(recs []*Record) int64 {
	var m int64
	for _, r := range recs {
		m = max(m, r.RowID)
	}
	return m
}

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
		if _, err := f4.QueryRoutedAll(10); !errors.Is(err, ErrFormat4Unsupported) {
			t.Fatalf("QueryRoutedAll: %v", err)
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
