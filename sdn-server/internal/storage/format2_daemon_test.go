package storage

// The daemon's record API on store format 2 answers what format 1 answers
// (T6 #7 through the storage API, the parity check A1 requires before any
// legacy path is deleted). The same write sequence runs through the
// storage API into a format-1 store and into a fresh format-2 store; every
// read the node serves from is then compared.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

func requireFormat2Engine(t testing.TB) {
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
}

// openFormat2ForTest opens (creating) a format-2 store at dir. Tests compile
// the partition-store artifact on a miss into their own cache.
func openFormat2ForTest(t testing.TB, dir string, opts ...StoreOption) *FlatSQLStore {
	t.Helper()
	requireFormat2Engine(t)
	prevCompile, prevDir := format2CompileOnMiss, format2AOTCacheDir
	format2CompileOnMiss = true
	format2AOTCacheDir = func() string {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		return filepath.Join(base, "sdn-format2-test-aot")
	}
	t.Cleanup(func() { format2CompileOnMiss, format2AOTCacheDir = prevCompile, prevDir })
	t.Setenv(format2.FormatEnv, "2")
	t.Setenv(checkpointIntervalEnv, "0")
	if os.Getenv(format2TopologyEnv) == "" {
		t.Setenv(format2TopologyEnv, "1/2/1") // host-02's §5.1 topology
	}
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewFlatSQLStore(dir, v, opts...)
	if err != nil {
		t.Fatalf("open format 2: %v", err)
	}
	if !s.Format2() {
		t.Fatal("SDN_STORE_FORMAT=2 opened a format-1 store")
	}
	t.Setenv(format2.FormatEnv, "")
	return s
}

func f2TestOMM(norad uint32, epoch time.Time, name string) []byte {
	data := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2026-%03dA", norad%1000)).
		WithEpoch(epoch.UTC().Format("2006-01-02T15:04:05.000000")).
		WithEpochTimestamp(float64(epoch.Unix())).
		WithMeanMotion(15.5).
		Build()
	return data[4:]
}

func f2TestCAT(norad uint32, name, objectType, status string) []byte {
	b := flatbuffers.NewBuilder(256)
	nm := b.CreateString(name)
	id := b.CreateString(fmt.Sprintf("1990-%05dA", norad))
	CAT.CATStart(b)
	CAT.CATAddOBJECT_NAME(b, nm)
	CAT.CATAddOBJECT_ID(b, id)
	CAT.CATAddNORAD_CAT_ID(b, norad)
	if objectType != "" {
		CAT.CATAddOBJECT_TYPE(b, CAT.EnumValuesspaceObjectClass[objectType])
	}
	if status != "" {
		CAT.CATAddOPS_STATUS_CODE(b, CAT.EnumValuesoperationalState[status])
	}
	root := CAT.CATEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$CAT"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// f2Script is the write sequence both stores receive.
type f2Script struct {
	omm, cat   [][]byte
	deleted    string
	reconciled SourceBatchReconcileResult
}

// newF2Script builds the records once (a builder may stamp the time, so
// both stores must receive the same bytes).
func newF2Script() f2Script {
	var sc f2Script
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 240; i++ {
		sc.omm = append(sc.omm, f2TestOMM(uint32(20000+i), base.Add(time.Duration(i)*7*time.Minute), fmt.Sprintf("OBJ-%d", i)))
	}
	for i := 0; i < 30; i++ {
		kind, status := "PAYLOAD", "OPERATIONAL"
		switch i % 5 {
		case 1:
			kind = "DEBRIS"
		case 2:
			status = "DECAYED"
		case 3:
			kind, status = "ROCKET_BODY", ""
		}
		sc.cat = append(sc.cat, f2TestCAT(uint32(100+i), fmt.Sprintf("SAT %d", i), kind, status))
	}
	for i := 0; i < 8; i++ {
		sc.cat = append(sc.cat, f2TestCAT(uint32(100+i), fmt.Sprintf("SAT %d (renamed)", i), "PAYLOAD", "OPERATIONAL"))
	}
	return sc
}

func runF2Script(t *testing.T, s *FlatSQLStore, sc f2Script) f2Script {
	t.Helper()
	gp := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-001",
		License: "CC-BY-4.0", LicenseURL: "https://creativecommons.org/licenses/by/4.0/"}
	n, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[:160], "source:celestrak", nil, gp)
	if err != nil || n != 160 {
		t.Fatalf("gp-001: %d inserted, %v", n, err)
	}
	gp2 := gp
	gp2.BatchID = "gp-002"
	if n, err = s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[:40], "source:celestrak", nil, gp2); err != nil || n != 0 {
		t.Fatalf("gp-002 re-tag: %d inserted, %v", n, err)
	}
	peer := SourceTags{ProviderID: "space-data-network-01", SourceName: "mirror", BatchID: "m-1"}
	if n, err = s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[120:220], "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", nil, peer); err != nil || n != 60 {
		t.Fatalf("mirror: %d inserted, %v", n, err)
	}
	// Untagged records (a relayed record, a direct Store) and one tag
	// attached after the fact.
	for _, d := range sc.omm[220:230] {
		if _, err := s.Store("OMM.fbs", d, "16Uiu2HAmRelayPeer", []byte{1, 2, 3}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StoreWithSourceTags("OMM.fbs", sc.omm[230], "source:spacetrack", nil,
		SourceTags{ProviderID: "space-track", SourceName: "gp-history", BatchID: "h-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSourceTags("OMM.fbs", ComputeCID(sc.omm[221]), SourceTags{ProviderID: "space-track", SourceName: "gp-history", BatchID: "h-2"}); err != nil {
		t.Fatal(err)
	}
	satcat := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-satcat", BatchID: "sc-1"}
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", sc.cat[:30], "source:celestrak", nil, satcat); err != nil {
		t.Fatal(err)
	}
	// A CAT record has no epoch: windows order it by its arrival second, so
	// the new edition lands in a later second on both stores.
	time.Sleep(1100 * time.Millisecond)
	satcat.BatchID = "sc-2"
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", sc.cat[30:], "source:celestrak", nil, satcat); err != nil {
		t.Fatal(err)
	}
	sc.deleted = ComputeCID(sc.omm[225])
	if err := s.Delete("OMM.fbs", sc.deleted); err != nil {
		t.Fatal(err)
	}
	// A current-snapshot flow keeps gp-002 only: gp-001 records not re-tagged
	// lose their last tag and go.
	dry, err := s.ReconcileSourceBatch("OMM.fbs", "space-data-network-02", "celestrak-gp", "gp-002", false)
	if err != nil {
		t.Fatal(err)
	}
	if sc.reconciled, err = s.ReconcileSourceBatch("OMM.fbs", "space-data-network-02", "celestrak-gp", "gp-002", true); err != nil {
		t.Fatal(err)
	}
	if dry.Matched != sc.reconciled.Matched {
		t.Fatalf("reconcile: dry run matched %d, apply %d", dry.Matched, sc.reconciled.Matched)
	}
	return sc
}

// cidSeq is the record sequence of a result: a legacy page repeats a record
// once per tag row and once per producer table holding it; a format-2 page
// carries each record once (A16). The first occurrence keeps its position.
func cidSeq(recs []*Record) []string {
	out := make([]string, 0, len(recs))
	seen := map[string]bool{}
	for _, r := range recs {
		if seen[r.CID] {
			continue
		}
		seen[r.CID] = true
		out = append(out, r.CID)
	}
	return out
}

func TestFormat2DaemonAPIMatchesFormat1(t *testing.T) {
	requireFormat2Engine(t)
	legacy := reopenDeferred(t, t.TempDir())
	defer legacy.Close()
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()

	script := newF2Script()
	scL := runF2Script(t, legacy, script)
	scF := runF2Script(t, f2, script)
	if scL.reconciled != scF.reconciled {
		t.Fatalf("ReconcileSourceBatch: format 2 %+v, format 1 %+v", scF.reconciled, scL.reconciled)
	}
	t.Logf("reconcile: %+v", scF.reconciled)

	// GetRecord for every record either store was given.
	for _, d := range append(append([][]byte{}, scL.omm...), scL.cat...) {
		cid := ComputeCID(d)
		schema := "OMM.fbs"
		if CAT.CATBufferHasIdentifier(d) {
			schema = "CAT.fbs"
		}
		a, errA := legacy.GetRecord(schema, cid)
		b, errB := f2.GetRecord(schema, cid)
		if (errA == nil) != (errB == nil) {
			copies, cerr := f2.PartitionStore().CopiesOf(f2.f2ctx(), schema, cid)
			t.Fatalf("GetRecord %s %s: format 1 %v, format 2 %v (format-2 copies %d: %+v %v)", schema, cid, errA, errB, len(copies), copies, cerr)
		}
		if errA == nil && (string(a.Data) != string(b.Data) || a.PeerID != b.PeerID) {
			t.Fatalf("GetRecord %s %s: record differs (peer %q vs %q)", schema, cid, a.PeerID, b.PeerID)
		}
	}
	for _, schema := range []string{"OMM.fbs", "CAT.fbs", "MPE.fbs"} {
		a, errA := legacy.Count(schema)
		b, errB := f2.Count(schema)
		if errA != nil || errB != nil || a != b {
			t.Fatalf("Count %s: format 1 %d (%v), format 2 %d (%v)", schema, a, errA, b, errB)
		}
	}

	// Indexed windows (the /api/v1/data window and the export's).
	norad := uint32(20150)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	from, to := base.Add(3*time.Hour), base.Add(9*time.Hour)
	windows := map[string]IndexedRecordQuery{
		"all":              {SchemaName: "OMM.fbs", Limit: 1000},
		"page":             {SchemaName: "OMM.fbs", Limit: 25, Offset: 70},
		"by cid":           {SchemaName: "OMM.fbs", Limit: 30, Offset: 11, OrderByCID: true},
		"provider":         {SchemaName: "OMM.fbs", ProviderID: "space-data-network-01", Limit: 1000},
		"source and batch": {SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: "gp-002", Limit: 1000},
		"batch only":       {SchemaName: "OMM.fbs", BatchID: "m-1", Limit: 17, Offset: 5},
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
		b, err := f2.QueryIndexedRecords(q)
		if err != nil {
			t.Fatalf("%s: format 2: %v", name, err)
		}
		if fmt.Sprint(cidSeq(a)) != fmt.Sprint(cidSeq(b)) {
			t.Fatalf("window %s: format 2 %d rows, format 1 %d rows (sequences differ)\n f2 %v\n f1 %v", name, len(b), len(a), cidSeq(b), cidSeq(a))
		}
		for i := range a {
			if string(a[i].Data) != string(b[i].Data) {
				t.Fatalf("window %s row %d: data differs", name, i)
			}
		}
		for _, budget := range []int64{2_000, 20_000} {
			na, ta, err := legacy.IndexedRecordWindowLimitForBytes(q, budget)
			if err != nil {
				t.Fatal(err)
			}
			nb, tb, err := f2.IndexedRecordWindowLimitForBytes(q, budget)
			if err != nil {
				t.Fatal(err)
			}
			if na != nb || ta != tb {
				t.Fatalf("window %s byte probe %d: format 2 (%d, %v), format 1 (%d, %v)", name, budget, nb, tb, na, ta)
			}
		}
	}

	// Raw records: counts, heads and the datasync v1 cursor sequence.
	raws := map[string]RawRecordQuery{
		"schema":        {SchemaName: "OMM.fbs"},
		"provider":      {SchemaName: "OMM.fbs", ProviderID: "space-data-network-01"},
		"source":        {SchemaName: "OMM.fbs", SourceName: "celestrak-gp"},
		"batch":         {SchemaName: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-002"},
		"batch+filter":  {SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: "gp-002", SyncFilter: "NORAD_CAT_ID >= 20010"},
		"batch+epoch":   {SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: "gp-001", SyncFilter: "EPOCH >= '2026-09-01T02:00:00Z'"},
		"producer peer": {SchemaName: "OMM.fbs", ProducerPeerID: "space-data-network-01"},
		"peer":          {SchemaName: "OMM.fbs", PeerID: "16Uiu2HAmRelayPeer"},
		"cid":           {SchemaName: "OMM.fbs", CID: ComputeCID(scL.omm[130])},
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
		"cat schema":    {SchemaName: "CAT.fbs"},
		"source+filter": {SchemaName: "OMM.fbs", SourceName: "mirror", SyncFilter: "NORAD_CAT_ID < 20180"},
	}
	names = names[:0]
	for n := range raws {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		q := raws[name]
		ca, err := legacy.CountRawRecords(q)
		if err != nil {
			t.Fatalf("%s: format 1 count: %v", name, err)
		}
		cb, err := f2.CountRawRecords(q)
		if err != nil {
			t.Fatalf("%s: format 2 count: %v", name, err)
		}
		if ca != cb {
			t.Fatalf("CountRawRecords %s: format 2 %d, format 1 %d", name, cb, ca)
		}
		ha, err := legacy.RawRecordHead(q)
		if err != nil {
			t.Fatal(err)
		}
		hb, err := f2.RawRecordHead(q)
		if err != nil {
			t.Fatal(err)
		}
		if ha.TotalBytes != hb.TotalBytes {
			t.Fatalf("RawRecordHead %s: format 2 %d bytes, format 1 %d", name, hb.TotalBytes, ha.TotalBytes)
		}
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
		// The same records; format 2 in strictly increasing gseq order (a
		// legacy page appends its untagged rows after its tagged ones).
		pa, pb := pages(legacy), pages(f2)
		for i := 1; i < len(pb); i++ {
			if pb[i].RowID <= pb[i-1].RowID {
				t.Fatalf("datasync v1 %s: format-2 page order is not gseq order at %d (%d after %d)", name, i, pb[i].RowID, pb[i-1].RowID)
			}
		}
		a, b := cidSeq(pa), cidSeq(pb)
		sort.Strings(a)
		sort.Strings(b)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("datasync v1 %s: format 2 %d records, format 1 %d (the sets differ)", name, len(b), len(a))
		}
	}

	// Summaries. Format 1's source summary does not drop a superseded CAT
	// record's bytes (removeRecordIfOrphanedTx reads record_length after the
	// row is gone and decrements 0): its CAT lane bytes are corrected here to
	// the bytes of the records the lanes hold, which is what format 2 reports.
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
				t.Logf("format 1 source summary CAT %s holds %d bytes for %d bytes of live records (the supersede drift)", sa.Sources[i].BatchID, sa.Sources[i].TotalBytes, want)
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
	sb, err := f2.DataSummary()
	if err != nil {
		t.Fatal(err)
	}
	if sa.TotalRecords != sb.TotalRecords || sa.TotalBytes != sb.TotalBytes || fmt.Sprint(sa.Schemas) != fmt.Sprint(sb.Schemas) ||
		fmt.Sprint(sa.Sources) != fmt.Sprint(sb.Sources) {
		t.Fatalf("DataSummary:\n format 2 %+v\n format 1 %+v", *sb, *sa)
	}
	pa, err := legacy.SourceBatchProgress()
	if err != nil {
		t.Fatal(err)
	}
	pb, err := f2.SourceBatchProgress()
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
		t.Fatalf("SourceBatchProgress:\n format 2 %s\n format 1 %s", progressKey(pb), progressKey(pa))
	}
	ppa, err := legacy.ProducerSourceProgress()
	if err != nil {
		t.Fatal(err)
	}
	ppb, err := f2.ProducerSourceProgress()
	if err != nil {
		t.Fatal(err)
	}
	ppKey := func(ps []ProducerSourceProgress) []string {
		var out []string
		for _, p := range ps {
			if p.SchemaName == "CAT.fbs" {
				p.TotalBytes = trueCAT["sc-1"] + trueCAT["sc-2"]
			}
			out = append(out, fmt.Sprintf("%s|%s|%s|%s batches=%d last=%s n=%d b=%d", p.ProducerPeerID, p.SchemaName, p.ProviderID, p.SourceName,
				p.BatchCount, p.LastBatchID, p.Count, p.TotalBytes))
		}
		sort.Strings(out)
		return out
	}
	if a, b := ppKey(ppa), ppKey(ppb); fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("ProducerSourceProgress:\n format 2 %v\n format 1 %v", b, a)
	}
	rca, err := legacy.SourceRecordCounts()
	if err != nil {
		t.Fatal(err)
	}
	rcb, err := f2.SourceRecordCounts()
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(rca) != fmt.Sprint(rcb) {
		t.Fatalf("SourceRecordCounts: format 2 %v, format 1 %v", rcb, rca)
	}
	lra, err := legacy.LiveRecordBytes()
	if err != nil {
		t.Fatal(err)
	}
	lrb, err := f2.LiveRecordBytes()
	if err != nil {
		t.Fatal(err)
	}
	// RECONCILE is per partition (A2): the celestrak partition's copies of
	// omm[120:160] lose their last celestrak tag and go, while the mirror's
	// copies keep the records live. Format 1 reconciled per record across
	// producers and left those copies in the celestrak table untagged.
	var keptByF1 int64
	for _, d := range script.omm[120:160] {
		keptByF1 += int64(len(d))
	}
	if lra-keptByF1 != lrb {
		t.Fatalf("LiveRecordBytes: format 2 %d, format 1 %d less %d kept untagged by its reconcile", lrb, lra, keptByF1)
	}
	dra, err := legacy.SchemaDateRanges()
	if err != nil {
		t.Fatal(err)
	}
	drb, err := f2.SchemaDateRanges()
	if err != nil {
		t.Fatal(err)
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
	for i := range dra {
		if dra[i].Schema == "OMM.fbs" {
			dra[i].TotalBytes -= keptByF1 // Σ partition bytes, as LiveRecordBytes above
		}
	}
	if a, b := rangeKey(dra), rangeKey(drb); fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("SchemaDateRanges:\n format 2 %v\n format 1 %v", b, a)
	}
	for _, peer := range []string{"source:celestrak", "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", "nobody"} {
		a, err := legacy.PeerStorageBytes(peer)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f2.PeerStorageBytes(peer)
		if err != nil {
			t.Fatal(err)
		}
		if peer == "source:celestrak" {
			a -= keptByF1
		}
		if a != b {
			t.Fatalf("PeerStorageBytes %s: format 2 %d, format 1 %d", peer, b, a)
		}
	}

	// The /api/v1/data/index page.
	for _, q := range []RecordIndexPageQuery{
		{SchemaName: "OMM.fbs", Limit: 20},
		{SchemaName: "OMM.fbs", SourceName: "mirror", Limit: 15, Offset: 10},
		{SchemaName: "OMM.fbs", NoradLike: "201", Limit: 50},
		{SchemaName: "CAT.fbs", Limit: 50},
	} {
		ra, na, err := legacy.RecordIndexPage(q)
		if err != nil {
			t.Fatal(err)
		}
		rb, nb, err := f2.RecordIndexPage(q)
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
			t.Fatalf("RecordIndexPage %+v: format 2 total %d %s\n format 1 total %d %s", q, nb, key(rb), na, key(ra))
		}
	}

	// Tagged-record lists and a record's tags.
	for _, q := range []SourceTagQuery{
		{SchemaName: "OMM.fbs", ProviderID: "space-data-network-01", Limit: 1000},
		{SchemaName: "OMM.fbs", SourceName: "gp-history", Limit: 1000},
	} {
		a, err := legacy.QuerySourceTaggedRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f2.QuerySourceTaggedRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		as, bs := cidSeq(a), cidSeq(b)
		sort.Strings(as)
		sort.Strings(bs)
		if fmt.Sprint(as) != fmt.Sprint(bs) {
			t.Fatalf("QuerySourceTaggedRecords %+v: format 2 %d, format 1 %d", q, len(bs), len(as))
		}
	}
	for _, i := range []int{0, 130, 200, 221, 230} {
		cid := ComputeCID(scL.omm[i])
		a, errA := legacy.GetSourceTags("OMM.fbs", cid)
		b, errB := f2.GetSourceTags("OMM.fbs", cid)
		if (errA == nil) != (errB == nil) {
			t.Fatalf("GetSourceTags %d: format 1 %v, format 2 %v", i, errA, errB)
		}
		if errA == nil && (a.ProviderID != b.ProviderID || a.SourceName != b.SourceName) {
			t.Fatalf("GetSourceTags %d: format 2 %+v, format 1 %+v", i, b, a)
		}
	}
	for _, schema := range []string{"OMM.fbs", "CAT.fbs"} {
		a, err := legacy.QueryRecentRecords(schema, 1000)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f2.QueryRecentRecords(schema, 1000)
		if err != nil {
			t.Fatal(err)
		}
		as, bs := cidSeq(a), cidSeq(b)
		sort.Strings(as)
		sort.Strings(bs)
		if fmt.Sprint(dedupeStrings(as)) != fmt.Sprint(dedupeStrings(bs)) {
			t.Fatalf("QueryRecentRecords %s: format 2 %d, format 1 %d", schema, len(bs), len(as))
		}
	}
	if _, err := f2.GetRecord("OMM.fbs", scF.deleted); err == nil {
		t.Fatal("a deleted record is still served")
	}

	// Epoch profiles (the /api/v1/data epoch surface).
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
		{SchemaName: "OMM.fbs", Profile: EpochProfileAsOf, At: at, NoradCatID: &norad},
	} {
		a, err := legacy.QueryEpochRecords(q)
		if err != nil {
			t.Fatalf("epoch %+v: format 1: %v", q, err)
		}
		b, err := f2.QueryEpochRecords(q)
		if err != nil {
			t.Fatalf("epoch %+v: format 2: %v", q, err)
		}
		key := func(ms []EpochRecordMatch) []string {
			var out []string
			for _, m := range ms {
				out = append(out, fmt.Sprintf("%s|%s|%d|%s", m.Record.CID, m.EntityKey, m.MatchedEpoch.Unix(), m.MatchType))
			}
			return out
		}
		if fmt.Sprint(key(a)) != fmt.Sprint(key(b)) {
			t.Fatalf("epoch %s %+v:\n format 2 %v\n format 1 %v", q.Profile, q, key(b), key(a))
		}
		na, err := legacy.CountEpochRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		nb, err := f2.CountEpochRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		if na != nb {
			t.Fatalf("CountEpochRecords %s: format 2 %d, format 1 %d", q.Profile, nb, na)
		}
	}
	ca, err := legacy.QueryEpochCoverage(EpochRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatal(err)
	}
	cb, err := f2.QueryEpochCoverage(EpochRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ca) != fmt.Sprint(cb) {
		t.Fatalf("QueryEpochCoverage:\n format 2 %v\n format 1 %v", cb, ca)
	}

	// Dataset exports (A17: a publication's ResultSHA256 is its shard's).
	for _, q := range []IndexedRecordQuery{
		{SchemaName: "OMM.fbs", ProviderID: "space-data-network-01", SourceName: "mirror", BatchID: "m-1", Limit: 40, Offset: 20},
		{SchemaName: "CAT.fbs", Limit: 1000},
		{SchemaName: "OMM.fbs", Limit: 64, OrderByCID: true},
	} {
		ea, err := legacy.ExportDatasetWindow(t.TempDir(), q)
		if err != nil {
			t.Fatal(err)
		}
		eb, err := f2.ExportDatasetWindow(t.TempDir(), q)
		if err != nil {
			t.Fatal(err)
		}
		if ea.ResultSHA256 != eb.ResultSHA256 || ea.RecordCount != eb.RecordCount {
			t.Fatalf("export %+v: format 2 %s (%d), format 1 %s (%d)", q, eb.ResultSHA256, eb.RecordCount, ea.ResultSHA256, ea.RecordCount)
		}
	}
	fa, na, err := legacy.DatasetPublicationSetFingerprint("OMM.fbs", "space-data-network-01", "mirror", "")
	if err != nil {
		t.Fatal(err)
	}
	fb, nb, err := f2.DatasetPublicationSetFingerprint("OMM.fbs", "space-data-network-01", "mirror", "")
	if err != nil {
		t.Fatal(err)
	}
	if fa != fb || na != nb {
		t.Fatalf("publication set fingerprint: format 2 %s (%d), format 1 %s (%d)", fb, nb, fa, na)
	}
}

// A format-1 open of a store store-migrate activated fails before it
// touches a file; format 2 refuses a legacy store it did not migrate.
func TestFormat2StoreFormatGuards(t *testing.T) {
	requireFormat2Engine(t)
	dir := t.TempDir()
	s := openFormat2ForTest(t, dir)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before := listFormat2Dir(t, dir)
	if _, err := NewFlatSQLStore(dir, bootTestValidator(t)); err == nil || !strings.Contains(err.Error(), "format 2") {
		t.Fatalf("format-1 open of a format-2 store: %v", err)
	}
	if after := listFormat2Dir(t, dir); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("the refused open changed the store:\n before %v\n after  %v", before, after)
	}
	legacyDir := t.TempDir()
	l := reopenDeferred(t, legacyDir)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(format2.FormatEnv, "2")
	if _, err := NewFlatSQLStore(legacyDir, bootTestValidator(t)); err == nil || err != format2.ErrNotMigrated && !strings.Contains(err.Error(), "not MIGRATED") {
		t.Fatalf("format-2 open of an unmigrated legacy store: %v", err)
	}
}

func listFormat2Dir(t *testing.T, dir string) []string {
	var out []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && !strings.HasSuffix(p, "store.lock") {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, fmt.Sprintf("%s:%d", rel, fi.Size()))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// A tag carried by a REPEAT copy (a record two producers stored under
// different tags) matches as it did on format 1, where tags were per
// record: every tag read runs per partition while the type holds REPEAT
// copies (A2: any live tag instance of a live copy).
func TestFormat2TagFiltersSeeEveryCopy(t *testing.T) {
	requireFormat2Engine(t)
	legacy := reopenDeferred(t, t.TempDir())
	defer legacy.Close()
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()
	sc := newF2Script()
	for _, s := range []*FlatSQLStore{legacy, f2} {
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[:160], "source:celestrak", nil,
			SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-001"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[120:220], "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", nil,
			SourceTags{ProviderID: "space-data-network-01", SourceName: "mirror", BatchID: "m-1"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []IndexedRecordQuery{
		{SchemaName: "OMM.fbs", ProviderID: "space-data-network-01", Limit: 1000},
		{SchemaName: "OMM.fbs", SourceName: "mirror", Limit: 30, Offset: 20},
		{SchemaName: "OMM.fbs", SourceName: "mirror", Limit: 1000, OrderByCID: true},
		{SchemaName: "OMM.fbs", SourceName: "celestrak-gp", Limit: 1000},
	} {
		a, err := legacy.QueryIndexedRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f2.QueryIndexedRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(cidSeq(a)) != fmt.Sprint(cidSeq(b)) {
			t.Fatalf("window %+v: format 2 %d rows, format 1 %d", q, len(b), len(a))
		}
	}
	for _, q := range []RawRecordQuery{
		{SchemaName: "OMM.fbs", ProviderID: "space-data-network-01"},
		{SchemaName: "OMM.fbs", SourceName: "mirror", SyncFilter: "NORAD_CAT_ID >= 20100"},
		{SchemaName: "OMM.fbs", ProducerPeerID: "space-data-network-01"},
	} {
		ca, err := legacy.CountRawRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		cb, err := f2.CountRawRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		if ca != cb {
			t.Fatalf("count %+v: format 2 %d, format 1 %d", q, cb, ca)
		}
		pages := func(s *FlatSQLStore) []string {
			var out []*Record
			q := q
			q.UseRowIDCursor, q.Limit = true, 17
			for {
				page, err := s.QueryRawRecordRefs(q)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				out = append(out, page...)
				q.AfterRowID = page[len(page)-1].RowID
			}
			ids := cidSeq(out)
			sort.Strings(ids)
			return ids
		}
		if a, b := pages(legacy), pages(f2); fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("pages %+v: format 2 %d, format 1 %d", q, len(b), len(a))
		}
	}
	ra, na, err := legacy.RecordIndexPage(RecordIndexPageQuery{SchemaName: "OMM.fbs", SourceName: "mirror", Limit: 40, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	rb, nb, err := f2.RecordIndexPage(RecordIndexPageQuery{SchemaName: "OMM.fbs", SourceName: "mirror", Limit: 40, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if na != nb || len(ra) != len(rb) {
		t.Fatalf("index page: format 2 %d/%d, format 1 %d/%d", len(rb), nb, len(ra), na)
	}
	for i := range ra {
		if ra[i].CID != rb[i].CID {
			t.Fatalf("index page row %d differs", i)
		}
	}
	a, err := legacy.QuerySourceTaggedRecords(SourceTagQuery{SchemaName: "OMM.fbs", SourceName: "mirror", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f2.QuerySourceTaggedRecords(SourceTagQuery{SchemaName: "OMM.fbs", SourceName: "mirror", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(cidSeq(a)) != len(cidSeq(b)) {
		t.Fatalf("tagged records: format 2 %d, format 1 %d", len(cidSeq(b)), len(cidSeq(a)))
	}
}

// Module SQL, the public query surface and the table pager answer as on
// format 1 (the relations keep their names, A18).
func TestFormat2SQLSurfacesMatchFormat1(t *testing.T) {
	requireFormat2Engine(t)
	legacy := reopenDeferred(t, t.TempDir())
	defer legacy.Close()
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()
	sc := newF2Script()
	for _, s := range []*FlatSQLStore{legacy, f2} {
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", sc.omm[:120], "source:celestrak", nil,
			SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-001"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StoreBatchWithSourceTags("CAT.fbs", sc.cat[:30], "source:celestrak", nil,
			SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-satcat", BatchID: "sc-1"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()
	for _, q := range []string{
		"SELECT NORAD_CAT_ID, OBJECT_NAME, MEAN_MOTION FROM OMM WHERE NORAD_CAT_ID = 20050",
		"SELECT COUNT(*) FROM OMM",
		"SELECT OBJECT_NAME, _data FROM CAT WHERE NORAD_CAT_ID = 105",
	} {
		a, err := legacy.SandboxedSelect(ctx, q, SandboxSelectCaps{})
		if err != nil {
			t.Fatalf("%s: format 1: %v", q, err)
		}
		b, err := f2.SandboxedSelect(ctx, q, SandboxSelectCaps{})
		if err != nil {
			t.Fatalf("%s: format 2: %v", q, err)
		}
		if fmt.Sprint(a.Columns, a.Rows) != fmt.Sprint(b.Columns, b.Rows) {
			t.Fatalf("SandboxedSelect %s:\n format 2 %v %v\n format 1 %v %v", q, b.Columns, b.Rows, a.Columns, a.Rows)
		}
	}
	for _, q := range []struct {
		sql    string
		params []interface{}
	}{
		{"SELECT _data FROM OMM WHERE NORAD_CAT_ID = ?", []interface{}{int64(20050)}},
		{"SELECT _data FROM OMM WHERE _source = ? AND NORAD_CAT_ID < ?", []interface{}{"OMM@celestrak-gp", int64(20010)}},
	} {
		a, err := legacy.QueryRawStream(q.sql, q.params...)
		if err != nil {
			t.Fatalf("%s: format 1: %v", q.sql, err)
		}
		b, err := f2.QueryRawStream(q.sql, q.params...)
		if err != nil {
			t.Fatalf("%s: format 2: %v", q.sql, err)
		}
		if a.FrameCount != b.FrameCount || len(a.Bytes) != len(b.Bytes) {
			t.Fatalf("QueryRawStream %s: format 2 %d frames %d B, format 1 %d frames %d B", q.sql, b.FrameCount, len(b.Bytes), a.FrameCount, len(a.Bytes))
		}
		sa, err := legacy.QuerySandboxedStream(q.sql, flatsqlrt.SandboxCaps{MaxRows: 1000}, q.params...)
		if err != nil {
			t.Fatal(err)
		}
		sb, err := f2.QuerySandboxedStream(q.sql, flatsqlrt.SandboxCaps{MaxRows: 1000}, q.params...)
		if err != nil {
			t.Fatal(err)
		}
		// No ORDER BY: the same frames, in either store's plan order.
		frames := func(b []byte) []string {
			var out []string
			for len(b) >= 4 {
				n := int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24
				out = append(out, string(b[4:4+n]))
				b = b[4+n:]
			}
			sort.Strings(out)
			return out
		}
		if fmt.Sprint(frames(sa.Bytes)) != fmt.Sprint(frames(sb.Bytes)) || sa.FrameCount != sb.FrameCount {
			t.Fatalf("QuerySandboxedStream %s: frames differ (%d vs %d B)", q.sql, len(sb.Bytes), len(sa.Bytes))
		}
	}
	for _, profile := range []string{"nearest", "as_of", "forward"} {
		for _, src := range []string{"", "celestrak-gp"} {
			at := float64(time.Date(2026, 9, 1, 5, 0, 0, 0, time.UTC).Unix())
			a, err := legacy.QueryEpochRawStream("OMM.fbs", src, profile, at, 0)
			if err != nil {
				t.Fatalf("epoch stream %s: format 1: %v", profile, err)
			}
			b, err := f2.QueryEpochRawStream("OMM.fbs", src, profile, at, 0)
			if err != nil {
				t.Fatalf("epoch stream %s: format 2: %v", profile, err)
			}
			if a.FrameCount != b.FrameCount || len(a.Bytes) != len(b.Bytes) {
				t.Fatalf("epoch stream %s %q: format 2 %d frames, format 1 %d", profile, src, b.FrameCount, a.FrameCount)
			}
		}
	}
	ja, _, _, err := legacy.QuerySandboxedJSON("SELECT NORAD_CAT_ID, OBJECT_NAME, MEAN_MOTION FROM OMM WHERE NORAD_CAT_ID < 20003 ORDER BY NORAD_CAT_ID", flatsqlrt.SandboxCaps{MaxRows: 100})
	if err != nil {
		t.Fatal(err)
	}
	jb, _, _, err := f2.QuerySandboxedJSON("SELECT NORAD_CAT_ID, OBJECT_NAME, MEAN_MOTION FROM OMM WHERE NORAD_CAT_ID < 20003 ORDER BY NORAD_CAT_ID", flatsqlrt.SandboxCaps{MaxRows: 100})
	if err != nil {
		t.Fatal(err)
	}
	if string(ja) != string(jb) {
		t.Fatalf("QuerySandboxedJSON:\n format 2 %s\n format 1 %s", jb, ja)
	}
	if _, _, _, err := f2.QuerySandboxedJSON("SELECT NORAD_CAT_ID FROM OMM", flatsqlrt.SandboxCaps{MaxRows: 10}); err == nil {
		t.Fatal("a sandboxed result over its row cap was not refused")
	}
	if _, err := f2.SandboxedSelect(ctx, "DELETE FROM OMM", SandboxSelectCaps{}); err == nil {
		t.Fatal("a sandboxed DELETE was admitted")
	}
	// The table pager: newest first, cursor pages, the whole standard.
	pages := func(s *FlatSQLStore, q FullTablePageQuery) []string {
		var out []*Record
		for {
			page, err := s.FullTablePageWithCursor(q)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Records) == 0 {
				break
			}
			out = append(out, page.Records...)
			q.Cursor = page.NextCursor
		}
		return cidSeq(out)
	}
	for _, q := range []FullTablePageQuery{{SchemaName: "OMM.fbs", Limit: 37}, {SchemaName: "CAT.fbs", Limit: 7, SourceName: "celestrak-satcat"}} {
		if a, b := pages(legacy, q), pages(f2, q); fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("FullTablePage %+v: format 2 %d, format 1 %d", q, len(b), len(a))
		}
	}
	surface, err := f2.PublicQuerySurface()
	if err != nil {
		t.Fatal(err)
	}
	var omm *QuerySurfaceTable
	for i := range surface {
		if surface[i].Name == "OMM" {
			omm = &surface[i]
		}
	}
	if omm == nil || omm.Records != 120 {
		t.Fatalf("PublicQuerySurface OMM: %+v", omm)
	}
}

// The interim full-text index on format 2 (A6): keyed by gseq, fed from the
// arrivals, and searched in gseq order with dead hits dropped. Search answers
// what format 1 answers on the same writes, including a record written after
// the index caught up and a record deleted after it was indexed.
func TestFormat2SearchMatchesFormat1(t *testing.T) {
	requireFormat2Engine(t)
	prev := format2FTSPoll
	format2FTSPoll = 50 * time.Millisecond
	defer func() { format2FTSPoll = prev }()
	legacy := reopenDeferred(t, t.TempDir())
	defer legacy.Close()
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()
	var cat [][]byte
	for i := 0; i < 120; i++ {
		name := fmt.Sprintf("Orbit object %d", i)
		switch i % 10 {
		case 3:
			name = fmt.Sprintf("München optical payload %d", i)
		case 7:
			name = fmt.Sprintf("Kourou relay %d", i)
		}
		cat = append(cat, f2TestCAT(uint32(500+i), name, "PAYLOAD", "OPERATIONAL"))
	}
	tags := SourceTags{ProviderID: "provider-a", SourceName: "satcat", BatchID: "edition"}
	for _, s := range []*FlatSQLStore{legacy, f2} {
		if _, err := s.StoreBatchWithSourceTags("CAT.fbs", cat[:100], "source-peer", nil, tags); err != nil {
			t.Fatal(err)
		}
		waitFullTextIndex(t, s, "CAT.fbs")
		// After the index caught up: new records, and one indexed record gone.
		if _, err := s.StoreBatchWithSourceTags("CAT.fbs", cat[100:], "source-peer", nil, tags); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete("CAT.fbs", ComputeCID(cat[13])); err != nil {
			t.Fatal(err)
		}
	}
	// The format-2 feed follows new arrivals within its poll.
	deadline := time.Now().Add(20 * time.Second)
	for {
		n, err := f2.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs", Search: "kourou"})
		if err == nil && n == 12 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the format-2 feed did not follow new arrivals (%d, %v)", n, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, q := range []RawRecordQuery{
		{SchemaName: "CAT.fbs", Search: "munchen optical", Limit: 100},
		{SchemaName: "CAT.fbs", Search: "kourou", Limit: 5},
		{SchemaName: "CAT.fbs", Search: "orbit", Limit: 1000},
		{SchemaName: "CAT.fbs", Search: "orbit", ProviderID: "provider-a", Limit: 1000},
	} {
		ca, err := legacy.CountRawRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		cb, err := f2.CountRawRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		if ca != cb {
			t.Fatalf("search count %q: format 2 %d, format 1 %d", q.Search, cb, ca)
		}
		pages := func(s *FlatSQLStore) []string {
			var out []*Record
			q := q
			q.UseRowIDCursor = true
			for {
				page, err := s.QueryRawRecords(q)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				out = append(out, page...)
				q.AfterRowID = page[len(page)-1].RowID
			}
			ids := cidSeq(out)
			sort.Strings(ids)
			return ids
		}
		if a, b := pages(legacy), pages(f2); fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("search pages %q: format 2 %d, format 1 %d", q.Search, len(b), len(a))
		}
	}
	if st := f2.FullTextIndexState("CAT.fbs"); st != "ready" {
		t.Fatalf("FullTextIndexState: %s", st)
	}
}

// A19: $KMF carries an (encrypted) field, so format 2 stores its sealed
// frame (the plaintext only verified, extracted and hashed). KMF ingest has
// 0 rejects, no plaintext key byte reaches a partition file, and every KMF
// read answers the plaintext record format 1 answers.
func TestFormat2KMFSealedRecordsMatchFormat1(t *testing.T) {
	requireFormat2Engine(t)
	legacy := reopenDeferred(t, t.TempDir())
	defer legacy.Close()
	dir := t.TempDir()
	f2 := openFormat2ForTest(t, dir)
	defer f2.Close()
	var recs, keys [][]byte
	for i := 0; i < 24; i++ {
		key := make([]byte, 32)
		for j := range key {
			key[j] = byte(0x5a ^ (i*31 + j*7))
		}
		keys = append(keys, key)
		recs = append(recs, buildKMFRecordForTest(t, fmt.Sprintf("kmf-key-%02d", i), key, uint32(i+1)))
	}
	for _, s := range []*FlatSQLStore{legacy, f2} {
		n, err := s.StoreBatchWithSourceTags("KMF.fbs", recs, "source:keys", nil, SourceTags{ProviderID: "local", SourceName: "keys", BatchID: "k-1"})
		if err != nil || n != len(recs) {
			t.Fatalf("KMF ingest: %d inserted, %v", n, err)
		}
	}
	st, err := f2.PartitionStore().Writer().Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Rejects != 0 {
		t.Fatalf("KMF ingest: %d rejects", st.Rejects)
	}
	for _, q := range []IndexedRecordQuery{{SchemaName: "KMF.fbs", Limit: 1000}, {SchemaName: "KMF.fbs", Limit: 7, Offset: 5}, {SchemaName: "KMF.fbs", Limit: 50, OrderByCID: true}} {
		a, err := legacy.QueryIndexedRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f2.QueryIndexedRecords(q)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(cidSeq(a)) != fmt.Sprint(cidSeq(b)) {
			t.Fatalf("KMF window %+v: format 2 %v, format 1 %v", q, cidSeq(b), cidSeq(a))
		}
		for i := range a {
			if string(a[i].Data) != string(b[i].Data) {
				t.Fatalf("KMF window row %d: the plaintext differs", i)
			}
		}
	}
	for _, d := range recs {
		got, err := f2.GetRecord("KMF.fbs", ComputeCID(d))
		if err != nil || string(got.Data) != string(d) {
			t.Fatalf("KMF GetRecord: %v", err)
		}
	}
	// No plaintext key byte in any partition file.
	_ = filepath.Walk(filepath.Join(dir, format2.Dir), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		for i, k := range keys {
			if strings.Contains(string(b), string(k)) {
				t.Fatalf("plaintext KEY_BYTES of record %d found in %s", i, p)
			}
		}
		return nil
	})
}

// A6: identity, directory and EPM reads (control reads) while the full-text
// feed indexes a standard and ingest saturates the writer. Record ingest
// takes no control lock on format 2; the feed's windows are bounded, so a
// control read waits for at most one of them.
func TestFormat2ControlReadsWhileTheFeedAndIngestRun(t *testing.T) {
	requireFormat2Engine(t)
	prev := format2FTSPoll
	format2FTSPoll = 20 * time.Millisecond
	defer func() { format2FTSPoll = prev }()
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()
	if err := f2.UpsertDirectoryRecord(DirectoryRecord{Kind: "peer", PeerID: "12D3KooWDirectoryPeer", Source: "test", EPMJSON: "{}", UpdatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	var cat [][]byte
	for i := 0; i < 20000; i++ {
		cat = append(cat, f2TestCAT(uint32(100000+i), fmt.Sprintf("Searchable object %d", i), "PAYLOAD", "OPERATIONAL"))
	}
	if _, err := f2.StoreBatchWithSourceTags("CAT.fbs", cat, "source:catalog", nil, SourceTags{ProviderID: "p", SourceName: "cat", BatchID: "b"}); err != nil {
		t.Fatal(err)
	}
	// The feed indexes 20,000 records while 20 producers ingest OMM.
	if err := f2.CheckFullTextSearch("CAT.fbs", "searchable"); err != nil && !errors.Is(err, ErrSearchIndexBuilding) {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for p := 0; p < 20; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			for k := 0; ; k += 50 {
				select {
				case <-stop:
					return
				default:
				}
				var recs [][]byte
				for i := 0; i < 50; i++ {
					recs = append(recs, f2TestOMM(uint32(3_000_000+p*100_000+k+i), base.Add(time.Duration(k+i)*time.Second), fmt.Sprintf("C%d-%d", p, k+i)))
				}
				if _, err := f2.StoreBatchWithSourceTags("OMM.fbs", recs, fmt.Sprintf("source:c%02d", p), nil,
					SourceTags{ProviderID: "p", SourceName: fmt.Sprintf("c%02d", p), BatchID: "live"}); err != nil {
					return
				}
			}
		}(p)
	}
	var lat []time.Duration
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		start := time.Now()
		if _, err := f2.QueryDirectory(DirectoryQuery{Kind: "peer", Limit: 10}); err != nil {
			t.Fatal(err)
		}
		if _, err := f2.GetLocalEPMRecord("12D3KooWNoLocalEPM"); err != nil && !strings.Contains(err.Error(), "not found") {
			t.Fatal(err)
		}
		if _, _, err := f2.DatastoreIdentity(); err != nil {
			t.Fatal(err)
		}
		lat = append(lat, time.Since(start))
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p99 := lat[int(float64(len(lat)-1)*0.99)]
	t.Logf("MEASURED control reads (directory + local EPM + datastore identity) during the full-text feed and 20-producer ingest: n=%d p50=%s p99=%s max=%s (%s)",
		len(lat), lat[len(lat)/2].Round(time.Microsecond), p99.Round(time.Microsecond), lat[len(lat)-1].Round(time.Microsecond), f2.FullTextIndexState("CAT.fbs"))
	if os.Getenv("SDN_PS_ACCEPTANCE") == "1" && p99 > 10*time.Millisecond {
		t.Fatalf("control reads p99 %s > 10 ms", p99)
	}
}
