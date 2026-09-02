package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// A routed (producer, standard) table must answer a bare `timestamp < ?`
// predicate from an index: that predicate is the age-based GC delete and the
// catalog replay's retention frame, and a scan there holds the store write
// lock for the whole table.
func TestRoutedTablesAnswerTimestampPredicatesFromAnIndex(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	data := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	if _, err := store.StoreRoutedByProducer("OMM.fbs", data, "peerA", nil); err != nil {
		t.Fatalf("StoreRoutedByProducer() error = %v", err)
	}
	const table = "sds_p_peerA__OMM"
	index := routedTimestampIndexName(table)

	probe := 0
	planUsesIndex := func() bool {
		// Distinct SQL text per probe so no prepared-statement cache can hand
		// back a plan compiled before the index was dropped or rebuilt.
		probe++
		rows, err := store.db.Query(fmt.Sprintf("EXPLAIN QUERY PLAN SELECT cid FROM %s WHERE timestamp < %d", table, probe))
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan plan: %v", err)
			}
			for _, v := range vals {
				if s, ok := v.(string); ok && strings.Contains(s, index) {
					return true
				}
				if b, ok := v.([]byte); ok && strings.Contains(string(b), index) {
					return true
				}
			}
		}
		return false
	}

	// Created with the table.
	if ok, err := store.indexExists(index); err != nil || !ok {
		t.Fatalf("index %s after table creation: exists=%v err=%v", index, ok, err)
	}
	if !planUsesIndex() {
		t.Fatalf("timestamp predicate on %s does not use %s", table, index)
	}

	// A table from before the index existed is backfilled on boot.
	if _, err := store.db.Exec("DROP INDEX " + index); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if planUsesIndex() {
		t.Fatal("plan still reports the dropped index")
	}
	created, err := store.EnsureRoutedTimestampIndexes(context.Background())
	if err != nil {
		t.Fatalf("EnsureRoutedTimestampIndexes: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
	if !planUsesIndex() {
		t.Fatalf("timestamp predicate on %s does not use %s after backfill", table, index)
	}
	// Idempotent.
	if created, err := store.EnsureRoutedTimestampIndexes(context.Background()); err != nil || created != 0 {
		t.Fatalf("second backfill: created=%d err=%v", created, err)
	}
}
