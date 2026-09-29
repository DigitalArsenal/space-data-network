package format2

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

func openTestStore(t testing.TB, root string) *Store {
	t.Helper()
	requireEngine(t)
	s, err := Open(StoreConfig{Root: root, AOTCacheDir: testAOTDir(t), CompileOnMiss: true, AllowFresh: true,
		Topology: Topology{Writers: 2, InteractiveLanes: 2, BulkLanes: 1}, GatePeriod: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ommPuts(n, offset int, base time.Time, tag string) []Put {
	out := make([]Put, n)
	for i := range out {
		out[i] = Put{Data: testOMM(uint32(30000+offset+i), base.Add(time.Duration(offset+i)*time.Minute), fmt.Sprintf("%s-%d", tag, offset+i))}
	}
	return out
}

// A5: format 2 refuses a store store-migrate did not activate.
func TestOpenRefusesALegacyStoreWithoutMigrated(t *testing.T) {
	requireEngine(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "control.flatsqldb"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(StoreConfig{Root: root, AOTCacheDir: testAOTDir(t), CompileOnMiss: true, AllowFresh: true}); err != ErrNotMigrated {
		t.Fatalf("open of a legacy store: %v, want ErrNotMigrated", err)
	}
	if _, err := os.Stat(filepath.Join(root, Dir)); !os.IsNotExist(err) {
		t.Fatalf("the refused open created %s: %v", Dir, err)
	}
}

// Router + reads: read-your-writes at ack (A20), windows, the byte probe,
// datasync pages by gseq, counters.
func TestRouterWritesAreReadableAtAck(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	tags := &Tags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "b1", License: "CC-BY-4.0"}
	var all []string
	misses, typeMisses := 0, 0
	for b := 0; b < 20; b++ {
		res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(50, b*50, base, "RYW"), "source:celestrak", nil, tags)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("record rejected: %v", r.Err)
			}
			all = append(all, r.CID)
			// Immediately after the ack: GetRecord finds it.
			if _, err := s.GetRecord(ctx, "OMM.fbs", r.CID); err != nil {
				misses++
			}
		}
		// A publish acks once labeled (A20): then a type-level read sees
		// every record of the batch.
		if err := s.WaitLabeled(ctx, "OMM.fbs", "source:celestrak"); err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			c, _ := CIDFromText(r.CID)
			got, err := s.Interactive().Query(ctx, Request{SQL: `SELECT _gseq FROM "OMM" WHERE _cid_bin = ?1 LIMIT 1`, Params: []Cell{Blob(c)}})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rows) != 1 {
				typeMisses++
			}
		}
	}
	if misses != 0 || typeMisses != 0 {
		t.Fatalf("of %d records, %d not readable at ack and %d not visible at type level once labeled", len(all), misses, typeMisses)
	}
	// A second producer: repeats of 100 CIDs and 50 new records.
	if _, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(150, 900, base, "RYW"), "16Uiu2HAm1Lbvw", nil,
		&Tags{ProviderID: "space-data-network-01", SourceName: "mirror", BatchID: "m1"}); err != nil {
		t.Fatal(err)
	}
	parts, err := s.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("%d partitions", len(parts))
	}
	var live int64
	for _, p := range parts {
		live += p.Live
	}
	if live != 1150 {
		t.Fatalf("live copies %d, want 1150 (1000 + 150)", live)
	}
	// Datasync v1: pages by gseq cover every FIRST copy once, in order.
	deadline := time.Now().Add(20 * time.Second)
	var seen map[string]bool
	for {
		seen = map[string]bool{}
		var after, max int64
		last := int64(0)
		ok := true
		for {
			recs, m, err := s.SyncPage(ctx, SyncQuery{Schema: "OMM.fbs", AfterGseq: after, MaxGseq: max, Limit: 97})
			if err != nil {
				t.Fatal(err)
			}
			max = m
			if len(recs) == 0 {
				break
			}
			for _, r := range recs {
				if r.Gseq <= last {
					ok = false
				}
				last = r.Gseq
				seen[r.CID] = true
			}
			after = recs[len(recs)-1].Gseq
		}
		if ok && len(seen) == 1050 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync saw %d CIDs (ordered %v), want 1050", len(seen), ok)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Windows and the byte probe.
	norad := uint32(30010)
	w, err := s.Window(ctx, WindowQuery{Schema: "OMM.fbs", NoradCatID: &norad})
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 || w[0].Source != "celestrak-gp" {
		t.Fatalf("NORAD window %+v", w)
	}
	src, err := s.Window(ctx, WindowQuery{Schema: "OMM.fbs", Source: "mirror", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	n, bytes, err := s.ByteProbe(ctx, WindowQuery{Schema: "OMM.fbs", Source: "mirror", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, r := range src {
		sum += int64(len(r.Data))
	}
	if n != int64(len(src)) || bytes != sum {
		t.Fatalf("byte probe (%d, %d B), window (%d, %d B)", n, bytes, len(src), sum)
	}
	lrb, err := s.LiveRecordBytes(ctx)
	if err != nil || lrb <= 0 {
		t.Fatalf("LiveRecordBytes %d %v", lrb, err)
	}
}

// A2: B1, RECONCILE(keep=B1), an identical B2, RECONCILE(keep=B2): 0 TOMB
// rows and 0 new arrivals; a B3 with one record removed tombstones exactly
// that record.
func TestReconcileKeepsUnchangedRecordsAndDropsTheRemovedOne(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	puts := ommPuts(100, 0, base, "R")
	lane := func(batch string) *Tags {
		return &Tags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: batch}
	}
	peer := "source:celestrak"
	if _, err := s.PutBatch(ctx, "OMM.fbs", puts, peer, nil, lane("B1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx, "OMM.fbs", peer, "space-data-network-02", "celestrak-gp", "B1"); err != nil {
		t.Fatal(err)
	}
	counts := func() (tombs, arrivals int64) {
		parts, err := s.Partitions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range parts {
			tombs += p.Tombs
		}
		types, err := s.Types(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, ty := range types {
			arrivals += ty.Arrivals
		}
		return
	}
	waitArrivals := func(want int64) {
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, a := counts(); a == want || time.Now().After(deadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitArrivals(100)
	t0, a0 := counts()
	if _, err := s.PutBatch(ctx, "OMM.fbs", puts, peer, nil, lane("B2")); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx, "OMM.fbs", peer, "space-data-network-02", "celestrak-gp", "B2"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	t1, a1 := counts()
	if t1 != t0 || a1 != a0 {
		t.Fatalf("an identical batch changed TOMB rows %d->%d and arrivals %d->%d", t0, t1, a0, a1)
	}
	// B3 drops record 42.
	b3 := append(append([]Put(nil), puts[:42]...), puts[43:]...)
	if _, err := s.PutBatch(ctx, "OMM.fbs", b3, peer, nil, lane("B3")); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx, "OMM.fbs", peer, "space-data-network-02", "celestrak-gp", "B3"); err != nil {
		t.Fatal(err)
	}
	gone := CIDText(CIDBytes(puts[42].Data))
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, err := s.GetRecord(ctx, "OMM.fbs", gone)
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the record B3 dropped is still live")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i, p := range puts {
		if i == 42 {
			continue
		}
		if _, err := s.GetRecord(ctx, "OMM.fbs", CIDText(CIDBytes(p.Data))); err != nil {
			t.Fatalf("record %d died with B3: %v", i, err)
		}
	}
}

// T6 #6: integers above 2^53 are exact through a lane (RB1), in both
// directions.
func TestLaneIntegersAreExactAbove2To53(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	vals := []int64{math.MinInt64, 1<<53 + 1, math.MaxInt64, -(1<<53 + 1)}
	params := make([]Cell, len(vals))
	for i, v := range vals {
		params[i] = Int(v)
	}
	res, err := s.Interactive().Query(context.Background(), Request{SQL: "SELECT ?1, ?2, ?3, ?4, 9007199254740993 + 0", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if c := res.Rows[0][i]; c.Type != CellInt || c.I != v {
			t.Fatalf("param %d came back %+v, want %d", i, c, v)
		}
	}
	if c := res.Rows[0][4]; c.Type != CellInt || c.I != 1<<53+1 {
		t.Fatalf("literal 2^53+1 came back %+v", c)
	}
}

var _ = flatsqlrt.PSRoleBulk

// A28: the counters read from the heads without a lane equal the engine's
// own flatsql_partitions and flatsql_types rows.
func TestHeadCountersEqualTheLaneCounters(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for p := 0; p < 5; p++ {
		if _, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(200, p*150, base, "H"), fmt.Sprintf("source:h%d", p), nil,
			&Tags{ProviderID: "prov", SourceName: fmt.Sprintf("h%d", p), BatchID: "b"}); err != nil {
			t.Fatal(err)
		}
	}
	heads, err := s.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Interactive().Query(ctx, Request{SQL: `SELECT pid, sql_name, live_count, live_bytes, total_count, disk_bytes FROM flatsql_partitions`})
	if err != nil {
		t.Fatal(err)
	}
	lane := map[int64]string{}
	for _, r := range res.Rows {
		lane[r[0].Int64()] = fmt.Sprintf("%s %d %d %d %d", r[1].String(), r[2].I, r[3].I, r[4].I, r[5].I)
	}
	if len(heads) != len(lane) || len(heads) != 5 {
		t.Fatalf("%d head partitions, %d lane partitions", len(heads), len(lane))
	}
	for _, h := range heads {
		got := fmt.Sprintf("%s %d %d %d %d", h.SQLName, h.Live, h.LiveBytes, h.Total, h.DiskBytes)
		if got != lane[h.PID] {
			t.Fatalf("partition %d: heads %q, lane %q", h.PID, got, lane[h.PID])
		}
	}
	// Types: wait for labeling, then compare.
	deadline := time.Now().Add(20 * time.Second)
	for {
		types, err := s.Types(ctx)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Interactive().Query(ctx, Request{SQL: `SELECT type, gseq_hi, arrivals, first_live_count, first_live_bytes FROM flatsql_types`})
		if err != nil {
			t.Fatal(err)
		}
		if len(types) == 1 && len(res.Rows) == 1 {
			r := res.Rows[0]
			got := fmt.Sprintf("%s %d %d %d %d", types[0].Type, types[0].GseqHi, types[0].Arrivals, types[0].FirstLive, types[0].FirstLiveBytes)
			want := fmt.Sprintf("%s %d %d %d %d", r[0].String(), r[1].I, r[2].I, r[3].I, r[4].I)
			if got == want && types[0].FirstLive == 800 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("type heads %q, lane %q", got, want)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An index-driven window reads the plan's millisecond epoch order only until
// the window and the epoch second of its last row are complete, then orders
// that prefix like the legacy window: whole seconds DESC, text CID ASC. Many
// records share each second here, with distinct milliseconds.
func TestSourceWindowKeepsTheLegacyOrderWithinASecond(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var puts []Put
	for i := 0; i < 600; i++ {
		// 12 records per second, 83 ms apart.
		ep := base.Add(time.Duration(i/12)*time.Second + time.Duration(i%12)*83*time.Millisecond)
		puts = append(puts, Put{Data: testOMM(uint32(70000+i), ep, fmt.Sprintf("S-%d", i))})
	}
	if _, err := s.PutBatch(ctx, "OMM.fbs", puts, "source:win", nil, &Tags{ProviderID: "p", SourceName: "win", BatchID: "b"}); err != nil {
		t.Fatal(err)
	}
	// Windows are type-level: they see a record once its type owner labeled
	// it (A20).
	if err := s.WaitLabeled(ctx, "OMM.fbs", "source:win"); err != nil {
		t.Fatal(err)
	}
	type ref struct {
		sec int64
		cid string
	}
	var all []ref
	for i, p := range puts {
		ep := base.Add(time.Duration(i/12)*time.Second + time.Duration(i%12)*83*time.Millisecond)
		all = append(all, ref{ep.Unix(), CIDText(CIDBytes(p.Data))})
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].sec != all[b].sec {
			return all[a].sec > all[b].sec
		}
		return all[a].cid < all[b].cid
	})
	for _, w := range []struct{ limit, offset int }{{10, 0}, {25, 7}, {100, 131}, {1000, 0}} {
		got, err := s.Window(ctx, WindowQuery{Schema: "OMM.fbs", Source: "win", Limit: w.limit, Offset: w.offset})
		if err != nil {
			t.Fatal(err)
		}
		end := w.offset + w.limit
		if end > len(all) {
			end = len(all)
		}
		want := all[w.offset:end]
		if len(got) != len(want) {
			t.Fatalf("window %+v: %d rows, want %d", w, len(got), len(want))
		}
		for i := range want {
			if got[i].CID != want[i].cid {
				t.Fatalf("window %+v row %d: %s, want %s", w, i, got[i].CID, want[i].cid)
			}
		}
		n, _, err := s.ByteProbe(ctx, WindowQuery{Schema: "OMM.fbs", Source: "win", Limit: w.limit, Offset: w.offset})
		if err != nil || n != int64(len(want)) {
			t.Fatalf("byte probe %+v: %d, %v", w, n, err)
		}
	}
}

// The §5.1 topology of a 24-core box (16 writers) starts: the writer
// instance's thread budget covers the writers, the sync pool, the builders
// and the urgent and checkpoint threads (16 + 8 + 1 + 2 = 27 of the cap of
// 32). Under the substrate's default of 24 the start trapped at 14 writers.
func TestSixteenWritersStart(t *testing.T) {
	requireEngine(t)
	start := time.Now()
	s, err := Open(StoreConfig{Root: t.TempDir(), AOTCacheDir: testAOTDir(t), CompileOnMiss: true, AllowFresh: true,
		Topology: Topology{Writers: 16, InteractiveLanes: 2, BulkLanes: 1}})
	if err != nil {
		t.Fatalf("16 writers: %v", err)
	}
	opened := time.Since(start)
	ctx := context.Background()
	if _, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(20, 0, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "T16"), "source:t16", nil, nil); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("16 writers: open %s, close %s", opened.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	if got := (WriterConfig{Writers: 24}).writerThreads(); got <= writerMaxThreads {
		t.Fatalf("24 writers need %d threads; the refusal above the cap is not exercised", got)
	}
	if _, err := OpenWriter(InstanceOptions{}, WriterConfig{Writers: 24}); err == nil || !strings.Contains(err.Error(), "guest threads") {
		t.Fatalf("24 writers: %v, want the thread-budget refusal", err)
	}
}

// §15: a trap fences its own reader instance, and the next call replaces
// it. The 30-minute soak through the daemon lost the point instance to a
// guest bad_alloc after 28 minutes; with no replacement every point read
// and every ingest (its presence check reads the point lanes) failed until
// restart.
func TestAFencedReaderInstanceIsReplaced(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(20, 0, base, "R"), "source:celestrak", nil, &Tags{SourceName: "celestrak-gp", BatchID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WaitLabeled(ctx, "OMM.fbs", "source:celestrak"); err != nil {
		t.Fatal(err)
	}
	cid := res[0].CID
	if _, err := s.GetRecord(ctx, "OMM.fbs", cid); err != nil {
		t.Fatal(err)
	}
	victim := s.Point().Instance()
	victim.Fence(errors.New("injected trap"))
	if victim.Failure() == nil {
		t.Fatal("the point instance was not fenced")
	}
	if s.Interactive().Instance().Failure() != nil || s.Writer().Instance().Failure() != nil {
		t.Fatal("a trap in the point instance fenced another instance")
	}
	if _, err := s.GetRecord(ctx, "OMM.fbs", cid); err != nil {
		t.Fatalf("GetRecord after the point instance's trap: %v", err)
	}
	if s.Point().Restarts() != 1 || s.Point().Instance() == victim {
		t.Fatalf("the point instance was not replaced (restarts %d)", s.Point().Restarts())
	}
	if _, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(20, 20, base, "R"), "source:celestrak", nil, &Tags{SourceName: "celestrak-gp", BatchID: "b1"}); err != nil {
		t.Fatalf("ingest after the trap: %v", err)
	}
}

// A reader instance whose memory reaches readerRecyclePages is replaced
// before it can run out; the statements it had end first.
func TestAFullReaderInstanceIsRecycled(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(20, 0, base, "F"), "source:celestrak", nil, &Tags{SourceName: "celestrak-gp", BatchID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	old := s.Point().Instance()
	saved := readerRecyclePages
	readerRecyclePages = old.Memory().Pages()
	defer func() { readerRecyclePages = saved }()
	for i := 0; i < 600; i++ {
		if _, err := s.GetRecord(ctx, "OMM.fbs", res[i%len(res)].CID); err != nil {
			t.Fatalf("GetRecord %d: %v", i, err)
		}
	}
	if s.Point().Restarts() == 0 || s.Point().Instance() == old {
		t.Fatal("the full point instance was not recycled")
	}
	deadline := time.Now().Add(10 * time.Second)
	for old.Enter() {
		old.Exit()
		if time.Now().After(deadline) {
			t.Fatal("the recycled instance never stopped")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if old.Failure() != nil {
		t.Fatalf("the recycled instance was fenced: %v", old.Failure())
	}
}
