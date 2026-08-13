package storage

// flatsql_untagged_page_test.go — the untagged raw-record page
// (task sdn-untagged-raw-record-page-sorts-the-whole-schema).
//
// The defect: the untagged half of queryRawRecords selected FROM the derived
// rawRecordReadSource table, whose projected rowid the engine can neither
// seek nor order on, so every rowid-cursor page sorted the ENTIRE schema
// through a temp B-tree (59.2 s held / 54 PRR pages over one host-01 boot).
// The fix drives the scan from sdn_record_index in rowid order via
// idx_sdn_record_index_schema_rowid and drops the NOT EXISTS anti-join for
// schemas that have no source tags at all.
//
// These tests pin BOTH halves of that: the paging contract stays correct,
// and the PLAN stays a seek — a reintroduced temp B-tree fails the test, it
// does not merely get slower.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func untaggedPageTestStore(t *testing.T) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestUntaggedRawRecordRowIDCursorPagesCoverTheSchema stores a wholly
// untagged schema (CAT.fbs — the PRR.fbs shape: zero source-tag rows)
// interleaved with a tagged schema so its sdn_record_index rowids are
// non-contiguous, then pages it exactly the way peers.loadProjection pages
// PRR: UseRowIDCursor + AfterRowID = max rowid seen. Every record must come
// back exactly once, in strictly ascending rowid order.
func TestUntaggedRawRecordRowIDCursorPagesCoverTheSchema(t *testing.T) {
	store := untaggedPageTestStore(t)

	tags := SourceTags{
		ProviderID:        "provider-x",
		SourceName:        "source-x",
		BatchID:           "batch-x",
		ProducerPeerID:    "peer-x",
		ProducerPublicKey: "public-x",
	}
	const n = 30
	wantCIDs := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		cat := sds.NewCATBuilder().WithNoradCatID(uint32(20000 + i)).WithObjectName(fmt.Sprintf("UNTAGGED-%02d", i)).Build()
		cid, err := store.Store("CAT.fbs", cat, "source:untagged", nil)
		if err != nil {
			t.Fatalf("store CAT %d: %v", i, err)
		}
		wantCIDs[cid] = false
		omm := sds.NewOMMBuilder().WithNoradCatID(uint32(30000 + i)).WithObjectName(fmt.Sprintf("TAGGED-%02d", i)).Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", omm, "source:tagged", nil, tags); err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
	}

	const pageSize = 7
	var afterRowID int64
	var order []string
	pages := 0
	for {
		page, err := store.QueryRawRecordRefs(RawRecordQuery{
			SchemaName:     "CAT.fbs",
			Limit:          pageSize,
			UseRowIDCursor: true,
			AfterRowID:     afterRowID,
		})
		if err != nil {
			t.Fatalf("page after rowid %d: %v", afterRowID, err)
		}
		if len(page) == 0 {
			break
		}
		pages++
		for _, rec := range page {
			if rec.RowID <= afterRowID {
				t.Fatalf("rowid %d not ascending past cursor %d", rec.RowID, afterRowID)
			}
			afterRowID = rec.RowID
			seen, ok := wantCIDs[rec.CID]
			if !ok {
				t.Fatalf("unexpected cid %s in CAT.fbs page", rec.CID)
			}
			if seen {
				t.Fatalf("cid %s returned twice", rec.CID)
			}
			wantCIDs[rec.CID] = true
			if rec.SourceTags.ProviderID != "" {
				t.Fatalf("untagged record %s carries provider %q", rec.CID, rec.SourceTags.ProviderID)
			}
			order = append(order, rec.CID)
		}
		if len(page) < pageSize {
			break
		}
	}
	if len(order) != n {
		t.Fatalf("paged %d records, want %d", len(order), n)
	}
	if want := (n + pageSize - 1) / pageSize; pages != want {
		t.Fatalf("took %d pages, want %d", pages, want)
	}
	for cid, seen := range wantCIDs {
		if !seen {
			t.Fatalf("cid %s never returned", cid)
		}
	}

	// One unpaged query must agree with the paged order exactly.
	all, err := store.QueryRawRecordRefs(RawRecordQuery{
		SchemaName:     "CAT.fbs",
		Limit:          1000,
		UseRowIDCursor: true,
	})
	if err != nil {
		t.Fatalf("unpaged query: %v", err)
	}
	if len(all) != n {
		t.Fatalf("unpaged query returned %d records, want %d", len(all), n)
	}
	for i, rec := range all {
		if rec.CID != order[i] {
			t.Fatalf("unpaged order diverges at %d: %s != %s", i, rec.CID, order[i])
		}
	}
}

// TestUntaggedRawRecordPageQueryPlanSeeksInsteadOfSorting pins the plan the
// engine actually executes for the rowid-cursor untagged page: a seek on
// idx_sdn_record_index_schema_rowid (schema_name=? AND rowid>?) with NO temp
// B-tree. This is the exact statement shape that held the host-01 engine for
// 59.2 s per boot; if a change reintroduces the sort, this fails loudly.
func TestUntaggedRawRecordPageQueryPlanSeeksInsteadOfSorting(t *testing.T) {
	store := untaggedPageTestStore(t)

	// Materialize the CAT.fbs payload table so recordReadSource has a real
	// single-table read source (the PRR shape).
	cat := sds.NewCATBuilder().WithNoradCatID(uint32(1)).WithObjectName("SEED").Build()
	if _, err := store.Store("CAT.fbs", cat, "source:untagged", nil); err != nil {
		t.Fatalf("seed CAT: %v", err)
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	filter := RawRecordQuery{
		SchemaName:     "CAT.fbs",
		Limit:          100,
		UseRowIDCursor: true,
		AfterRowID:     1,
	}

	for _, tc := range []struct {
		name          string
		schemaHasTags bool
	}{
		{"zero-tag schema drops the anti-join", false},
		{"tagged schema keeps the anti-join", true},
	} {
		query, args, err := store.untaggedRawRecordPageQueryLocked(filter, rawRecordSyncFilter{}, tc.schemaHasTags, filter.Limit)
		if err != nil {
			t.Fatalf("%s: compose: %v", tc.name, err)
		}
		if got := strings.Contains(query, "NOT EXISTS"); got != tc.schemaHasTags {
			t.Fatalf("%s: NOT EXISTS presence = %v, want %v\n%s", tc.name, got, tc.schemaHasTags, query)
		}

		rows, err := store.db.Query("EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatalf("%s: explain: %v", tc.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				rows.Close()
				t.Fatalf("%s: scan plan: %v", tc.name, err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		planText := strings.Join(plan, "\n")
		if strings.Contains(strings.ToUpper(planText), "TEMP B-TREE") {
			t.Fatalf("%s: rowid-cursor untagged page still sorts:\n%s", tc.name, planText)
		}
		if !strings.Contains(planText, "idx_sdn_record_index_schema_rowid (schema_name=? AND rowid>?)") {
			t.Fatalf("%s: page is not a rowid seek on idx_sdn_record_index_schema_rowid:\n%s", tc.name, planText)
		}
	}
}

// TestMixedTagSchemaStillReturnsTaggedThenUntagged pins the untouched
// contract for schemas that have SOME tags: the tagged half still runs (with
// its tags attached), the untagged half still fills the remainder, nothing is
// duplicated or lost, and untagged rows still arrive in ascending rowid order.
func TestMixedTagSchemaStillReturnsTaggedThenUntagged(t *testing.T) {
	store := untaggedPageTestStore(t)

	tags := SourceTags{
		ProviderID:        "provider-m",
		SourceName:        "source-m",
		BatchID:           "batch-m",
		ProducerPeerID:    "peer-m",
		ProducerPublicKey: "public-m",
	}
	taggedCIDs := map[string]bool{}
	untaggedCIDs := map[string]bool{}
	for i := 0; i < 3; i++ {
		omm := sds.NewOMMBuilder().WithNoradCatID(uint32(40000 + i)).WithObjectName(fmt.Sprintf("MT-%d", i)).Build()
		cid, err := store.StoreWithSourceTags("OMM.fbs", omm, "source:tagged", nil, tags)
		if err != nil {
			t.Fatalf("store tagged %d: %v", i, err)
		}
		taggedCIDs[cid] = false
	}
	for i := 0; i < 4; i++ {
		omm := sds.NewOMMBuilder().WithNoradCatID(uint32(41000 + i)).WithObjectName(fmt.Sprintf("MU-%d", i)).Build()
		cid, err := store.Store("OMM.fbs", omm, "source:untagged", nil)
		if err != nil {
			t.Fatalf("store untagged %d: %v", i, err)
		}
		untaggedCIDs[cid] = false
	}

	records, err := store.QueryRawRecordRefs(RawRecordQuery{
		SchemaName:     "OMM.fbs",
		Limit:          100,
		UseRowIDCursor: true,
	})
	if err != nil {
		t.Fatalf("query mixed schema: %v", err)
	}
	if len(records) != 7 {
		t.Fatalf("got %d records, want 7", len(records))
	}
	var lastUntaggedRowID int64
	inUntaggedTail := false
	for _, rec := range records {
		if _, isTagged := taggedCIDs[rec.CID]; isTagged {
			if inUntaggedTail {
				t.Fatalf("tagged record %s after untagged tail began", rec.CID)
			}
			if rec.SourceTags.ProviderID != tags.ProviderID {
				t.Fatalf("tagged record %s lost its tags: %+v", rec.CID, rec.SourceTags)
			}
			if taggedCIDs[rec.CID] {
				t.Fatalf("tagged cid %s duplicated", rec.CID)
			}
			taggedCIDs[rec.CID] = true
			continue
		}
		if _, isUntagged := untaggedCIDs[rec.CID]; !isUntagged {
			t.Fatalf("unexpected cid %s", rec.CID)
		}
		inUntaggedTail = true
		if rec.SourceTags.ProviderID != "" {
			t.Fatalf("untagged record %s carries provider %q", rec.CID, rec.SourceTags.ProviderID)
		}
		if rec.RowID <= lastUntaggedRowID {
			t.Fatalf("untagged rowids not ascending: %d after %d", rec.RowID, lastUntaggedRowID)
		}
		lastUntaggedRowID = rec.RowID
		if untaggedCIDs[rec.CID] {
			t.Fatalf("untagged cid %s duplicated", rec.CID)
		}
		untaggedCIDs[rec.CID] = true
	}
	for cid, seen := range taggedCIDs {
		if !seen {
			t.Fatalf("tagged cid %s missing", cid)
		}
	}
	for cid, seen := range untaggedCIDs {
		if !seen {
			t.Fatalf("untagged cid %s missing", cid)
		}
	}
}
