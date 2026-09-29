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
	"fmt"
	"sort"
	"testing"
	"time"

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
