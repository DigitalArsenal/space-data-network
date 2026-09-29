package main

// T6 #7: the format-2 reads equal the legacy store's on the migrated copy.
// The legacy store stays active (store-migrate --no-activate), so both are
// read side by side: indexed windows (the legacy TestIndexedRecordWindow
// filter set: all, a page, CID order, provider, source and batch, a batch
// page, NORAD, a time range, an empty standard), their byte probes,
// GetRecord for every CID, the datasync v1 page sequence, and
// LiveRecordBytes.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

func TestFormat2ReadsEqualTheLegacyStoreOnTheMigratedCopy(t *testing.T) {
	requirePSEngine(t)
	dir := t.TempDir()
	buildLegacyStore(t, dir)
	if _, err := migrateStore(context.Background(), migrateOptions{Store: dir, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, NoActivate: true}, nil); err != nil {
		t.Fatal(err)
	}
	ps, err := format2.Open(format2.StoreConfig{Root: dir, EngineRoot: migrateStagingDir, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, Topology: format2.Topology{Writers: 1, InteractiveLanes: 2, BulkLanes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	v, _ := sds.NewValidator(nil)
	legacy, err := storage.NewFlatSQLStore(dir, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	ctx := context.Background()

	norad := uint32(20105)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	from, to := base.Add(40*time.Minute), base.Add(160*time.Minute)
	filters := map[string]storage.IndexedRecordQuery{
		"all":              {SchemaName: "OMM.fbs", Limit: 1000},
		"page":             {SchemaName: "OMM.fbs", Limit: 25, Offset: 70},
		"by cid":           {SchemaName: "OMM.fbs", Limit: 30, Offset: 11, OrderByCID: true},
		"provider":         {SchemaName: "OMM.fbs", ProviderID: "space-data-network-01", Limit: 1000},
		"source and batch": {SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: "gp-002", Limit: 1000},
		"batch page":       {SchemaName: "OMM.fbs", ProviderID: "space-data-network-02", BatchID: "gp-001", Limit: 17, Offset: 5},
		"norad":            {SchemaName: "OMM.fbs", NoradCatID: &norad, Limit: 10},
		"time range":       {SchemaName: "OMM.fbs", From: &from, To: &to, Limit: 1000},
		"empty standard":   {SchemaName: "RFM.fbs", Limit: 10},
		"cat":              {SchemaName: "CAT.fbs", Limit: 1000},
		"cat source page":  {SchemaName: "CAT.fbs", SourceName: "celestrak-satcat", Limit: 12, Offset: 7},
	}
	// Tag-filtered windows match ANY live tag instance of a record (the
	// legacy ANY-row semantics; flatsql 3.2.0 PARTITION-STORE.md §31.1).
	names := make([]string, 0, len(filters))
	for n := range filters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		f := filters[name]
		want, err := legacy.QueryIndexedRecords(f)
		if err != nil {
			t.Fatalf("%s: legacy: %v", name, err)
		}
		q := format2.WindowQuery{Schema: f.SchemaName, NoradCatID: f.NoradCatID, Provider: f.ProviderID, Source: f.SourceName,
			Batch: f.BatchID, From: f.From, To: f.To, Limit: f.Limit, Offset: f.Offset}
		if f.OrderByCID {
			q.Order = "cid"
		}
		start := time.Now()
		got, err := ps.Window(ctx, q)
		t.Logf("%s: %d rows in %s", name, len(got), time.Since(start))
		if err != nil {
			t.Fatalf("%s: format 2: %v", name, err)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: format 2 has %d rows, legacy %d", name, len(got), len(want))
		}
		var wantBytes int64
		for i := range want {
			if got[i].CID != want[i].CID || !bytes.Equal(got[i].Data, want[i].Data) {
				t.Fatalf("%s row %d: format 2 %s, legacy %s", name, i, got[i].CID, want[i].CID)
			}
			wantBytes += int64(len(want[i].Data))
		}
		n, b, err := ps.ByteProbe(ctx, q)
		if err != nil {
			t.Fatalf("%s: byte probe: %v", name, err)
		}
		if n != int64(len(want)) || b != wantBytes {
			t.Fatalf("%s: byte probe (%d, %d B), legacy window (%d, %d B)", name, n, b, len(want), wantBytes)
		}
	}

	// GetRecord for every CID of every schema, and the datasync v1 sequence.
	for _, schema := range []string{"OMM.fbs", "CAT.fbs"} {
		var legacySeq []string
		var after int64
		for {
			page, err := legacy.QueryRawRecords(storage.RawRecordQuery{SchemaName: schema, UseRowIDCursor: true, AfterRowID: after, Limit: 37})
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			for _, r := range page {
				// A legacy v1 page repeats a record once per tag row (its
				// source JOINs sdn_record_source_tags); the cid sequence is
				// the records in cursor order (§16.1 step 7). A format-2
				// page carries each record once (A16: TotalCount is the
				// type head's first_live_count).
				if n := len(legacySeq); n > 0 && legacySeq[n-1] == r.CID {
					continue
				}
				legacySeq = append(legacySeq, r.CID)
				got, err := ps.GetRecord(ctx, schema, r.CID)
				if err != nil {
					t.Fatalf("%s GetRecord %s: %v", schema, r.CID, err)
				}
				if !bytes.Equal(got.Data, r.Data) {
					t.Fatalf("%s GetRecord %s: data differs", schema, r.CID)
				}
			}
			after = page[len(page)-1].RowID
		}
		var seq []string
		var g, max int64
		for {
			recs, m, err := ps.SyncPage(ctx, format2.SyncQuery{Schema: schema, AfterGseq: g, MaxGseq: max, Limit: 41})
			if err != nil {
				t.Fatal(err)
			}
			max = m
			if len(recs) == 0 {
				break
			}
			for _, r := range recs {
				seq = append(seq, r.CID)
			}
			g = recs[len(recs)-1].Gseq
		}
		if fmt.Sprint(seq) != fmt.Sprint(legacySeq) {
			t.Fatalf("%s: datasync v1 sequence differs (%d vs %d cids)", schema, len(seq), len(legacySeq))
		}
	}
	wantRanges, err := legacy.SchemaDateRanges()
	if err != nil {
		t.Fatal(err)
	}
	gotRanges, err := ps.SchemaDateRanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fmtRange := func(schema string, n int64, oldest, newest *time.Time, b int64) string {
		f := func(t *time.Time) string {
			if t == nil {
				return "-"
			}
			return t.UTC().Format(time.RFC3339)
		}
		return fmt.Sprintf("%s n=%d %s..%s %dB", schema, n, f(oldest), f(newest), b)
	}
	var wantR, gotR []string
	for _, r := range wantRanges {
		wantR = append(wantR, fmtRange(r.Schema, r.RecordCount, r.OldestEpoch, r.NewestEpoch, r.TotalBytes))
	}
	for _, r := range gotRanges {
		gotR = append(gotR, fmtRange(r.Schema, r.RecordCount, r.OldestEpoch, r.NewestEpoch, r.TotalBytes))
	}
	if fmt.Sprint(gotR) != fmt.Sprint(wantR) {
		t.Fatalf("SchemaDateRanges:\n format 2 %v\n legacy   %v", gotR, wantR)
	}
	wantLRB, err := legacy.LiveRecordBytes()
	if err != nil {
		t.Fatal(err)
	}
	gotLRB, err := ps.LiveRecordBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotLRB != wantLRB {
		t.Fatalf("LiveRecordBytes: format 2 %d, legacy %d", gotLRB, wantLRB)
	}
}

// T6 #8 (A3): after migration a production write registers no new
// partition for an existing lane, and a CAT edition supersedes the migrated
// one (the live CAT count per source is unchanged).
func TestFlowsWriteIntoTheMigratedPartitions(t *testing.T) {
	requirePSEngine(t)
	dir := t.TempDir()
	buildLegacyStore(t, dir)
	if _, err := migrateStore(context.Background(), migrateOptions{Store: dir, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, NoActivate: true}, nil); err != nil {
		t.Fatal(err)
	}
	ps, err := format2.Open(format2.StoreConfig{Root: dir, EngineRoot: migrateStagingDir, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, Topology: format2.Topology{Writers: 1, InteractiveLanes: 2, BulkLanes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	ctx := context.Background()
	before, err := ps.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	liveCAT := func() (n int64) {
		parts, err := ps.Partitions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range parts {
			if p.Type == "CAT" {
				n += p.Live
			}
		}
		return n
	}
	catBefore := liveCAT()
	// The GP flow's next fetch, under the same producer ("source:celestrak").
	base := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	var omm []format2.Put
	for i := 0; i < 20; i++ {
		omm = append(omm, format2.Put{Data: migrateTestOMM(uint32(20000+i), base.Add(time.Duration(i)*time.Minute), fmt.Sprintf("OBJ-%d", i))})
	}
	if _, err := ps.PutBatch(ctx, "OMM.fbs", omm, "source:celestrak", nil,
		&format2.Tags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-003"}); err != nil {
		t.Fatal(err)
	}
	// The SATCAT flow's next edition: 15 objects changed.
	var cat []format2.Put
	for i := 0; i < 15; i++ {
		cat = append(cat, format2.Put{Data: migrateTestCAT(uint32(100+i), fmt.Sprintf("SAT %d (edition 3)", i))})
	}
	if _, err := ps.PutBatch(ctx, "CAT.fbs", cat, "source:celestrak", nil,
		&format2.Tags{ProviderID: "space-data-network-02", SourceName: "celestrak-satcat", BatchID: "sc-3"}); err != nil {
		t.Fatal(err)
	}
	after, err := ps.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the flows registered %d new partitions", len(after)-len(before))
	}
	if got := liveCAT(); got != catBefore {
		t.Fatalf("live CAT %d after a new edition, %d before (supersede did not retire the migrated edition)", got, catBefore)
	}
}

// T6 #7 on a host-02-sized copy, through the daemon's storage API: the
// store-migrate fixture opened as format 2 (SDN_STORE_FORMAT=2, the daemon's
// own open) against the same data opened as format 1. It covers what the
// first lane left: indexed windows per type, the A17 shard windows (every
// (offset, limit) export's ResultSHA256), every A16 sync-filter field with
// the peer and CID filters and the filtered TotalCount, epoch profiles, and
// the <TYPE>@<source> relations of the query surface (A18).
//
//	SDN_FORMAT2_FIXTURE_LEGACY=<dir>  a legacy store (the fixture, cloned)
//	SDN_FORMAT2_FIXTURE=<dir>         the same store migrated and activated
func TestFormat2DaemonReadsEqualTheLegacyFixture(t *testing.T) {
	legacyDir, f2Dir := os.Getenv("SDN_FORMAT2_FIXTURE_LEGACY"), os.Getenv("SDN_FORMAT2_FIXTURE")
	if legacyDir == "" || f2Dir == "" {
		t.Skip("SDN_FORMAT2_FIXTURE_LEGACY and SDN_FORMAT2_FIXTURE name a legacy fixture and its migrated copy")
	}
	requirePSEngine(t)
	if _, _, err := flatsqlrt.PrewarmPSThreadsAOT(storage.EngineAOTCacheDir()); err != nil {
		t.Fatal(err)
	}
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDN_FLATSQL_CHECKPOINT_INTERVAL", "0")
	opened := time.Now()
	legacy, err := storage.NewFlatSQLStore(legacyDir, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	t.Logf("format 1 open %s", time.Since(opened).Round(time.Millisecond))
	t.Setenv(format2.FormatEnv, "2")
	opened = time.Now()
	f2, err := storage.NewFlatSQLStore(f2Dir, v)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	t.Setenv(format2.FormatEnv, "")
	t.Logf("format 2 open %s (the daemon's open: control instance + partition store)", time.Since(opened).Round(time.Millisecond))

	seq := func(recs []*storage.Record) []string {
		var out []string
		seen := map[string]bool{}
		for _, r := range recs {
			if !seen[r.CID] {
				seen[r.CID] = true
				out = append(out, r.CID)
			}
		}
		return out
	}
	lanes, err := legacy.SourceBatchProgress()
	if err != nil {
		t.Fatal(err)
	}
	batchOf := map[string][]string{}
	for _, l := range lanes {
		batchOf[l.SchemaName] = append(batchOf[l.SchemaName], l.BatchID)
	}
	for k := range batchOf {
		sort.Strings(batchOf[k])
	}
	epoch0 := time.Unix(1788220801, 0).UTC()
	from, to := epoch0.Add(26*time.Hour), epoch0.Add(29*time.Hour)
	norad := uint32(25544)
	windows := []storage.IndexedRecordQuery{
		{SchemaName: "OMM.fbs", Limit: 1000},
		{SchemaName: "OMM.fbs", Limit: 100, Offset: 20000},
		{SchemaName: "OMM.fbs", Limit: 500, Offset: 1000, OrderByCID: true},
		{SchemaName: "OMM.fbs", NoradCatID: &norad, Limit: 100},
		{SchemaName: "OMM.fbs", From: &from, To: &to, Limit: 1000},
		{SchemaName: "OMM.fbs", Day: "2026-09-12", Limit: 1000},
		{SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: batchOf["OMM.fbs"][3], Limit: 1000, Offset: 5000},
		{SchemaName: "MPE.fbs", Limit: 1000},
		{SchemaName: "MPE.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: batchOf["MPE.fbs"][7], Limit: 1000},
		{SchemaName: "CAT.fbs", Limit: 1000},
		{SchemaName: "CAT.fbs", SourceName: "celestrak-satcat-csv", Limit: 1000, Offset: 30000},
		{SchemaName: "CAT.fbs", Limit: 200, Offset: 60000, OrderByCID: true},
		{SchemaName: "IQC.fbs", SourceName: "IQEngine", Limit: 1000},
		{SchemaName: "IQC.fbs", Limit: 1000, Offset: 100000, OrderByCID: true},
	}
	for _, q := range windows {
		start := time.Now()
		a, err := legacy.QueryIndexedRecords(q)
		if err != nil {
			t.Fatalf("window %+v: format 1: %v", q, err)
		}
		la := time.Since(start)
		start = time.Now()
		b, err := f2.QueryIndexedRecords(q)
		if err != nil {
			t.Fatalf("window %+v: format 2: %v", q, err)
		}
		lb := time.Since(start)
		if fmt.Sprint(seq(a)) != fmt.Sprint(seq(b)) {
			t.Fatalf("window %s %+v: format 2 %d rows, format 1 %d", q.SchemaName, q, len(b), len(a))
		}
		for i := range a {
			if !bytes.Equal(a[i].Data, b[i].Data) {
				t.Fatalf("window %+v row %d: data differs", q, i)
			}
		}
		t.Logf("window %s src=%q batch=%q off=%d lim=%d cid=%v: %d rows equal (format 1 %s, format 2 %s)", q.SchemaName, q.SourceName,
			q.BatchID, q.Offset, q.Limit, q.OrderByCID, len(a), la.Round(time.Millisecond), lb.Round(time.Millisecond))
	}

	// A17: each shard window's export hashes as format 1's.
	exports := 0
	for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
		for _, b := range []string{batchOf[schema][0], batchOf[schema][len(batchOf[schema])/2], batchOf[schema][len(batchOf[schema])-1]} {
			for _, off := range []int{0, 1000, 30000} {
				q := storage.IndexedRecordQuery{SchemaName: schema, ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: b,
					Limit: 1000, Offset: off, AllowLargeResultSet: true}
				ea, errA := legacy.ExportDatasetWindow(t.TempDir(), q)
				eb, errB := f2.ExportDatasetWindow(t.TempDir(), q)
				if (errA == nil) != (errB == nil) {
					t.Fatalf("export %+v: format 1 %v, format 2 %v", q, errA, errB)
				}
				if errA != nil {
					continue
				}
				if ea.ResultSHA256 != eb.ResultSHA256 || ea.RecordCount != eb.RecordCount {
					t.Fatalf("A17 export %+v: format 2 %s (%d), format 1 %s (%d)", q, eb.ResultSHA256, eb.RecordCount, ea.ResultSHA256, ea.RecordCount)
				}
				exports++
			}
		}
	}
	for _, q := range []storage.IndexedRecordQuery{
		{SchemaName: "CAT.fbs", SourceName: "celestrak-satcat", Limit: 5000, Offset: 10000, AllowLargeResultSet: true},
		{SchemaName: "CAT.fbs", Limit: 2000, Offset: 4000, OrderByCID: true, AllowLargeResultSet: true},
		{SchemaName: "IQC.fbs", SourceName: "IQEngine", Limit: 500, Offset: 2000, AllowLargeResultSet: true},
	} {
		ea, err := legacy.ExportDatasetWindow(t.TempDir(), q)
		if err != nil {
			t.Fatal(err)
		}
		eb, err := f2.ExportDatasetWindow(t.TempDir(), q)
		if err != nil {
			t.Fatal(err)
		}
		if ea.ResultSHA256 != eb.ResultSHA256 {
			t.Fatalf("A17 export %+v: format 2 %s, format 1 %s", q, eb.ResultSHA256, ea.ResultSHA256)
		}
		exports++
	}
	t.Logf("A17: %d shard windows export byte-identical shards", exports)

	// A16: every sync-filter field, the peer and CID filters, TotalCount and
	// the page records (each record once in format 2's pages).
	someCID := ""
	if w, err := legacy.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 1, Offset: 777}); err == nil && len(w) == 1 {
		someCID = w[0].CID
	}
	syncs := []storage.RawRecordQuery{
		{SchemaName: "OMM.fbs", SyncFilter: "EPOCH >= '2026-09-26T12:00:00Z'"},
		{SchemaName: "OMM.fbs", SyncFilter: "EPOCH BETWEEN 1788300000 AND 1788310000"},
		{SchemaName: "OMM.fbs", SyncFilter: "EPOCH_DAY = '2026-09-14'"},
		{SchemaName: "OMM.fbs", SyncFilter: "NORAD_CAT_ID < 60"},
		{SchemaName: "OMM.fbs", SyncFilter: "OBJECT_ID = '1998-067A'"},
		{SchemaName: "OMM.fbs", SyncFilter: "SOURCE_TIMESTAMP > 0 AND NORAD_CAT_ID = 25544"},
		{SchemaName: "MPE.fbs", SyncFilter: "EPOCH >= '2026-09-27T00:00:00Z'"},
		{SchemaName: "CAT.fbs", SyncFilter: "NORAD_CAT_ID <= 200"},
		{SchemaName: "CAT.fbs", SyncFilter: "OBJECT_TYPE = 'PAYLOAD'"},
		{SchemaName: "CAT.fbs", SyncFilter: "OPS_STATUS_CODE != 'OPERATIONAL' AND NORAD_CAT_ID < 50"},
		{SchemaName: "OMM.fbs", SourceName: "celestrak-gp", BatchID: batchOf["OMM.fbs"][5], SyncFilter: "NORAD_CAT_ID < 3000"},
		{SchemaName: "OMM.fbs", CID: someCID},
		{SchemaName: "OMM.fbs", PeerID: "source:celestrak", SyncFilter: "NORAD_CAT_ID = 25544"},
		{SchemaName: "IQC.fbs", SourceName: "IQEngine", SyncFilter: "SOURCE_TIMESTAMP > 0"},
	}
	for _, q := range syncs {
		start := time.Now()
		ca, err := legacy.CountRawRecords(q)
		if err != nil {
			t.Fatalf("count %+v: format 1: %v", q, err)
		}
		cb, err := f2.CountRawRecords(q)
		if err != nil {
			t.Fatalf("count %+v: format 2: %v", q, err)
		}
		if ca != cb {
			t.Fatalf("A16 TotalCount %+v: format 2 %d, format 1 %d", q, cb, ca)
		}
		// The cursor pages up to a cursor bound: format 1 pages up to 40
		// pages, and both sides are compared through the cursor it reached
		// (the cursor is the legacy rowid, which format 2 keeps as the gseq).
		pages := func(s *storage.FlatSQLStore, maxPages int, bound int64) ([]string, int64, time.Duration) {
			began := time.Now()
			var out []*storage.Record
			q := q
			q.UseRowIDCursor, q.Limit = true, 500
			last := int64(-1) // -1: the pages ended
			for p := 0; ; p++ {
				if p == maxPages {
					last = q.AfterRowID
					break
				}
				page, err := s.QueryRawRecordRefs(q)
				if err != nil {
					t.Fatalf("pages %+v: %v", q, err)
				}
				if len(page) == 0 {
					break
				}
				for _, r := range page {
					if bound < 0 || r.RowID <= bound {
						out = append(out, r)
					}
				}
				q.AfterRowID = page[len(page)-1].RowID
				if bound >= 0 && q.AfterRowID >= bound {
					break
				}
			}
			ids := seq(out)
			sort.Strings(ids)
			return ids, last, time.Since(began)
		}
		a, bound, la := pages(legacy, 40, -1)
		b, _, lb := pages(f2, -1, bound)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("A16 pages %+v (through cursor %d): format 2 %d records, format 1 %d", q, bound, len(b), len(a))
		}
		through := "every page"
		if bound >= 0 {
			through = fmt.Sprintf("pages through cursor %d", bound)
		}
		t.Logf("A16 %s %q src=%q batch=%q cid=%v peer=%q: count %d, %s: %d records equal (format 1 %s, format 2 %s; %s in all)",
			q.SchemaName, q.SyncFilter, q.SourceName, q.BatchID, q.CID != "", q.PeerID, ca, through, len(a),
			la.Round(time.Millisecond), lb.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	}

	// Epoch profiles.
	at := epoch0.Add(13*24*time.Hour + 7*time.Hour + 30*time.Minute)
	for _, q := range []storage.EpochRecordQuery{
		{SchemaName: "OMM.fbs", Profile: storage.EpochProfileNearest, At: at, Limit: 2000},
		{SchemaName: "OMM.fbs", Profile: storage.EpochProfileAsOf, At: at, Limit: 2000, SourceName: "celestrak-gp"},
		{SchemaName: "OMM.fbs", Profile: storage.EpochProfileForward, At: at, Limit: 2000},
		{SchemaName: "MPE.fbs", Profile: storage.EpochProfileNearest, At: at, Limit: 2000, MaxDeltaSeconds: 7200},
		{SchemaName: "OMM.fbs", Profile: storage.EpochProfileWindow, From: &from, To: &to, Limit: 5000},
		{SchemaName: "OMM.fbs", Profile: storage.EpochProfileDay, Day: "2026-09-20", Limit: 3000},
	} {
		start := time.Now()
		a, err := legacy.QueryEpochRecords(q)
		if err != nil {
			t.Fatalf("epoch %+v: format 1: %v", q, err)
		}
		la := time.Since(start)
		start = time.Now()
		b, err := f2.QueryEpochRecords(q)
		if err != nil {
			t.Fatalf("epoch %+v: format 2: %v", q, err)
		}
		lb := time.Since(start)
		key := func(ms []storage.EpochRecordMatch) string {
			var sb strings.Builder
			for _, m := range ms {
				fmt.Fprintf(&sb, "%s|%s|%d|%s\n", m.Record.CID, m.EntityKey, m.MatchedEpoch.Unix(), m.MatchType)
			}
			return fmt.Sprintf("%x", sha256.Sum256([]byte(sb.String())))
		}
		if key(a) != key(b) || len(a) != len(b) {
			t.Fatalf("epoch profile %s %+v: format 2 %d matches, format 1 %d (they differ)", q.Profile, q, len(b), len(a))
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
			t.Fatalf("epoch count %s: format 2 %d, format 1 %d", q.Profile, nb, na)
		}
		t.Logf("epoch %s %s: %d matches equal, count %d (format 1 %s, format 2 %s)", q.SchemaName, q.Profile, len(a), na,
			la.Round(time.Millisecond), lb.Round(time.Millisecond))
	}
	if os.Getenv("SDN_FORMAT2_FIXTURE_SURFACE") != "1" {
		return
	}
	// A18: <TYPE>@<source> through the public query sandbox, bounded to the
	// newest N as the legacy hot window was. The legacy window must be hydrated.
	start := time.Now()
	if _, err := legacy.HydrateEngineHotWindow(); err != nil {
		t.Fatal(err)
	}
	t.Logf("format 1 hot window hydrated in %s", time.Since(start).Round(time.Second))
	for _, rel := range []string{`"CAT@celestrak-satcat"`, `"CAT@celestrak-satcat-csv"`, `"IQC@IQEngine"`, `"MPE@celestrak-gp"`} {
		sql := "SELECT _data FROM " + rel
		a, err := legacy.QuerySandboxedStream(sql, flatsqlrt.SandboxCaps{Timeout: 5 * time.Minute})
		if err != nil {
			t.Fatalf("%s: format 1: %v", rel, err)
		}
		b, err := f2.QuerySandboxedStream(sql, flatsqlrt.SandboxCaps{Timeout: 5 * time.Minute})
		if err != nil {
			t.Fatalf("%s: format 2: %v", rel, err)
		}
		frames := func(buf []byte) string {
			var all []string
			for len(buf) >= 4 {
				n := int(buf[0]) | int(buf[1])<<8 | int(buf[2])<<16 | int(buf[3])<<24
				all = append(all, string(buf[4:4+n]))
				buf = buf[4+n:]
			}
			sort.Strings(all)
			h := sha256.New()
			for _, f := range all {
				h.Write([]byte(f))
			}
			return fmt.Sprintf("%d:%x", len(all), h.Sum(nil))
		}
		if frames(a.Bytes) != frames(b.Bytes) {
			t.Fatalf("A18 %s: format 2 %s, format 1 %s", rel, frames(b.Bytes), frames(a.Bytes))
		}
		t.Logf("A18 %s: %d records, the same frames", rel, a.FrameCount)
	}
}

// A6 through store-migrate: the legacy full-text index moves to its own file
// (fts.flatsqldb) keyed by the legacy rowid, which is each record's gseq, so
// format 2 searches it at once — the feed resumes from the migrated progress
// instead of re-indexing — and every search answers as format 1 did.
func TestFormat2SearchesTheMigratedFullTextIndex(t *testing.T) {
	requirePSEngine(t)
	if _, _, err := flatsqlrt.PrewarmPSThreadsAOT(storage.EngineAOTCacheDir()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	buildLegacyStore(t, dir)
	v, _ := sds.NewValidator(nil)
	legacy, err := storage.NewFlatSQLStore(dir, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	waitSearch := func(s *storage.FlatSQLStore) {
		deadline := time.Now().Add(60 * time.Second)
		for {
			err := s.CheckFullTextSearch("CAT.fbs", "sat")
			if err == nil {
				return
			}
			if !strings.Contains(err.Error(), "building") || time.Now().After(deadline) {
				t.Fatalf("search index: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitSearch(legacy)
	want := map[string]int64{}
	for _, q := range []string{"sat", "renamed", "sat 7"} {
		n, err := legacy.CountRawRecords(storage.RawRecordQuery{SchemaName: "CAT.fbs", Search: q})
		if err != nil {
			t.Fatal(err)
		}
		want[q] = n
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	rep, err := migrateStore(context.Background(), migrateOptions{Store: dir, AOTCacheDir: migrateTestAOTDir(t), CompileOnMiss: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(rep.Extra["control_copy"]), " FTS rows") || strings.Contains(fmt.Sprint(rep.Extra["control_copy"]), " 0 FTS rows") {
		t.Fatalf("the control copy carried no FTS rows: %v", rep.Extra["control_copy"])
	}
	t.Setenv(format2.FormatEnv, "2")
	f2, err := storage.NewFlatSQLStore(dir, v)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	waitSearch(f2)
	for q, n := range want {
		got, err := f2.CountRawRecords(storage.RawRecordQuery{SchemaName: "CAT.fbs", Search: q})
		if err != nil {
			t.Fatal(err)
		}
		if got != n {
			t.Fatalf("search %q on the migrated index: format 2 %d, format 1 %d", q, got, n)
		}
	}
	t.Logf("the migrated full-text index answers %v as format 1 did (%v)", want, rep.Extra["control_copy"])
}
