package storage

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unionIndexedWindowForTest is the window the store used to run: the index
// joined to the standard's union read source and to every tag row of the
// schema grouped by cid. It is the reference for WHICH rows, in WHICH order;
// it is also the plan the host-02 $IQC reads could not afford.
func unionIndexedWindowForTest(filter IndexedRecordQuery, readSource, projection string) (string, []interface{}) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM %s d
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = ? AND idx.cid = d.cid
		LEFT JOIN (
			SELECT schema_name, cid, provider_id, source_name, batch_id
			FROM sdn_record_source_tags
			WHERE schema_name = ?
			GROUP BY schema_name, cid
		) tags ON tags.schema_name = idx.schema_name AND tags.cid = idx.cid
		WHERE 1=1`, projection, readSource)
	args := []interface{}{filter.SchemaName, filter.SchemaName}
	if filter.Day != "" {
		query += ` AND idx.epoch_day = ?`
		args = append(args, filter.Day)
	}
	if filter.NoradCatID != nil {
		query += ` AND idx.norad_cat_id = ?`
		args = append(args, int64(*filter.NoradCatID))
	}
	if filter.From != nil {
		query += ` AND COALESCE(idx.epoch_unix, idx.source_timestamp) >= ?`
		args = append(args, filter.From.Unix())
	}
	if filter.To != nil {
		query += ` AND COALESCE(idx.epoch_unix, idx.source_timestamp) <= ?`
		args = append(args, filter.To.Unix())
	}
	if filter.ProviderID != "" || filter.SourceName != "" || filter.BatchID != "" {
		query += ` AND EXISTS (SELECT 1 FROM sdn_record_source_tags ft WHERE ft.schema_name = idx.schema_name AND ft.cid = idx.cid`
		if filter.ProviderID != "" {
			query += ` AND ft.provider_id = ?`
			args = append(args, filter.ProviderID)
		}
		if filter.SourceName != "" {
			query += ` AND ft.source_name = ?`
			args = append(args, filter.SourceName)
		}
		if filter.BatchID != "" {
			query += ` AND ft.batch_id = ?`
			args = append(args, filter.BatchID)
		}
		query += `)`
	}
	if filter.OrderByCID {
		query += ` ORDER BY d.cid ASC LIMIT ?`
	} else {
		query += ` ORDER BY COALESCE(idx.epoch_unix, idx.source_timestamp) DESC, d.cid ASC LIMIT ?`
	}
	args = append(args, filter.Limit)
	if filter.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, filter.Offset)
	}
	return query, args
}

type windowRow struct {
	cid, provider, source, batch string
	data                         []byte
	length                       int64
}

func readWindowForTest(t *testing.T, s *FlatSQLStore, query string, args []interface{}, withRecord bool) []windowRow {
	t.Helper()
	rows, err := s.db.Query(query, args...)
	if err != nil {
		t.Fatalf("window query: %v\n%s", err, query)
	}
	defer rows.Close()
	var out []windowRow
	for rows.Next() {
		var r windowRow
		if withRecord {
			var peer, sig, prov, src, batch *string
			var ts int64
			if err := rows.Scan(&r.cid, &peer, &ts, &r.data, &sig, &prov, &src, &batch); err != nil {
				t.Fatal(err)
			}
			for dst, v := range map[*string]*string{&r.provider: prov, &r.source: src, &r.batch: batch} {
				if v != nil {
					*dst = *v
				}
			}
		} else if err := rows.Scan(&r.length); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedIndexedWindowStore holds one standard in two producer tables with
// overlapping CIDs, several tag rows per CID, and distinct epochs.
func seedIndexedWindowStore(t *testing.T) *FlatSQLStore {
	t.Helper()
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	t.Cleanup(func() { store.Close() })
	for i := 0; i < 24; i++ {
		data := buildEngineOMM(t, uint32(44000+i), fmt.Sprintf("SAT %d", i), 1_700_000_000+int64(i%8)*43_200)
		batch := fmt.Sprintf("b%d", i%3)
		if _, err := store.StoreWithSourceTags("OMM.fbs", data, "peer-a", nil,
			SourceTags{ProviderID: "prov-a", SourceName: "gp", BatchID: batch}); err != nil {
			t.Fatal(err)
		}
		if i%4 == 0 { // the same record from a second producer and source
			if _, err := store.StoreWithSourceTags("OMM.fbs", data, "peer-b", nil,
				SourceTags{ProviderID: "prov-b", SourceName: "mirror", BatchID: "m1"}); err != nil {
				t.Fatal(err)
			}
		}
		if i%5 == 0 { // re-tagged under a later batch
			if err := store.UpsertSourceTags("OMM.fbs", computeCID(data), SourceTags{ProviderID: "prov-a", SourceName: "gp", BatchID: "b9"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if tables, _ := store.recordTablesForSchema("OMM.fbs"); len(tables) != 2 {
		t.Fatalf("fixture holds OMM in %d tables, want 2", len(tables))
	}
	return store
}

func indexedWindowFiltersForTest() map[string]IndexedRecordQuery {
	norad := uint32(44005)
	from := time.Unix(1_700_043_200, 0)
	to := time.Unix(1_700_216_000, 0)
	return map[string]IndexedRecordQuery{
		"all":              {SchemaName: "OMM.fbs", Limit: 100},
		"page":             {SchemaName: "OMM.fbs", Limit: 5, Offset: 7},
		"by cid":           {SchemaName: "OMM.fbs", Limit: 9, Offset: 3, OrderByCID: true},
		"provider":         {SchemaName: "OMM.fbs", ProviderID: "prov-b", Limit: 100},
		"source and batch": {SchemaName: "OMM.fbs", SourceName: "gp", BatchID: "b9", Limit: 100},
		"batch page":       {SchemaName: "OMM.fbs", ProviderID: "prov-a", BatchID: "b1", Limit: 3, Offset: 1},
		"norad":            {SchemaName: "OMM.fbs", NoradCatID: &norad, Limit: 10},
		"time range":       {SchemaName: "OMM.fbs", From: &from, To: &to, Limit: 100},
		"empty standard":   {SchemaName: "RFM.fbs", Limit: 10},
	}
}

// tagRowsForTest lists every provider/source/batch a record is tagged with.
func tagRowsForTest(t *testing.T, s *FlatSQLStore, cid string) map[string]bool {
	t.Helper()
	rows, err := s.db.Query(`SELECT provider_id, source_name, batch_id FROM sdn_record_source_tags WHERE schema_name = 'OMM.fbs' AND cid = ?`, cid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p, src, b string
		if err := rows.Scan(&p, &src, &b); err != nil {
			t.Fatal(err)
		}
		out[p+"/"+src+"/"+b] = true
	}
	return out
}

// The index-driven window returns exactly the rows, order and bytes of the
// union window it replaced, and projects one of each record's own tag rows.
func TestIndexedRecordWindowMatchesTheUnionWindow(t *testing.T) {
	store := seedIndexedWindowStore(t)
	for name, filter := range indexedWindowFiltersForTest() {
		store.mu.RLock()
		readSource, err := store.recordReadSource(filter.SchemaName)
		tables, terr := store.recordTablesForSchema(filter.SchemaName)
		store.mu.RUnlock()
		if err != nil || terr != nil {
			t.Fatal(err, terr)
		}
		wantQ, wantArgs := unionIndexedWindowForTest(filter, readSource,
			`d.cid, d.peer_id, d.timestamp, d.data, d.signature_hex, tags.provider_id, tags.source_name, tags.batch_id`)
		gotQ, gotArgs := indexedRecordWindowSQL(filter, tables, indexedRecordProjection)
		want := readWindowForTest(t, store, wantQ, wantArgs, true)
		got := readWindowForTest(t, store, gotQ, gotArgs, true)
		if len(got) != len(want) {
			t.Fatalf("%s: %d rows, union window has %d", name, len(got), len(want))
		}
		for i := range want {
			if got[i].cid != want[i].cid || !bytes.Equal(got[i].data, want[i].data) {
				t.Fatalf("%s row %d: got %s, union window has %s", name, i, got[i].cid, want[i].cid)
			}
			tag := got[i].provider + "/" + got[i].source + "/" + got[i].batch
			if own := tagRowsForTest(t, store, got[i].cid); !own[tag] {
				t.Fatalf("%s row %d: projected tag %s is not one of the record's own %v", name, i, tag, own)
			}
		}

		wantQ, wantArgs = unionIndexedWindowForTest(filter, readSource, `d.record_length`)
		gotQ, gotArgs = indexedRecordWindowSQL(filter, tables, indexedRecordLengthProjection)
		wantLen := readWindowForTest(t, store, wantQ, wantArgs, false)
		gotLen := readWindowForTest(t, store, gotQ, gotArgs, false)
		if fmt.Sprint(gotLen) != fmt.Sprint(wantLen) {
			t.Fatalf("%s: byte probe %v, union window %v", name, gotLen, wantLen)
		}
	}
	// The public paths agree with the SQL they run.
	recs, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: "prov-b", Limit: 100})
	if err != nil || len(recs) != 6 {
		t.Fatalf("QueryIndexedRecords(prov-b) = %d records, %v; want 6", len(recs), err)
	}
	if n, truncated, err := store.IndexedRecordWindowLimitForBytes(IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 100}, 1); err != nil || n != 1 || !truncated {
		t.Fatalf("IndexedRecordWindowLimitForBytes(1 byte) = %d, %v, %v; want 1 oversized record, truncated", n, truncated, err)
	}
}

func explainForTest(t *testing.T, s *FlatSQLStore, query string, args []interface{}) []string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int64
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	return plan
}

// planReadsWholeTables reports a plan line that walks a record table or the
// tag table — a SCAN, or a SEARCH not keyed by CID or rowid — or a compound
// (UNION) over them.
func planReadsWholeTables(plan []string) (string, bool) {
	for _, line := range plan {
		if strings.Contains(line, "UNION") || strings.Contains(line, "COMPOUND") {
			return line, true
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "SCAN" && fields[0] != "SEARCH") {
			continue
		}
		name := fields[1]
		recordOrTag := strings.HasPrefix(name, "sds_p_") || name == "sdn_record_source_tags" ||
			name == "d" || name == "held" || name == "pt" || name == "ft" || name == "tags" ||
			(len(name) > 1 && name[0] == 'r' && name[1] >= '0' && name[1] <= '9')
		if !recordOrTag {
			continue
		}
		if fields[0] == "SCAN" || (!strings.Contains(line, "cid=?") && !strings.Contains(line, "rowid=?")) {
			return line, true
		}
	}
	return "", false
}

// No window plan may scan a record table or the tag table: every access to
// them is a seek by CID. The union window materialised both in full.
func TestIndexedRecordWindowPlanNeverScansTheRecordTables(t *testing.T) {
	store := seedIndexedWindowStore(t)
	tables, err := store.recordTablesForSchema("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	readSource, err := store.recordReadSource("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	// The detector must see the defect it guards against.
	unionQ, unionArgs := unionIndexedWindowForTest(IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: "prov-b", Limit: 10}, readSource, `d.record_length`)
	if _, bad := planReadsWholeTables(explainForTest(t, store, unionQ, unionArgs)); !bad {
		t.Fatalf("the plan check does not flag the union window:\n  %s", strings.Join(explainForTest(t, store, unionQ, unionArgs), "\n  "))
	}
	for name, filter := range indexedWindowFiltersForTest() {
		for _, projection := range []func(func(string) string) string{indexedRecordProjection, indexedRecordLengthProjection} {
			query, args := indexedRecordWindowSQL(filter, tables, projection)
			plan := explainForTest(t, store, query, args)
			if line, bad := planReadsWholeTables(plan); bad {
				t.Fatalf("%s: plan reads whole tables at %q:\n  %s", name, line, strings.Join(plan, "\n  "))
			}
		}
	}
}
