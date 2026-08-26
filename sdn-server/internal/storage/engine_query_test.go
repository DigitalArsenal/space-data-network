package storage

// The public query surface is what an ANONYMOUS caller asks before it queries
// anything, and it names every routed standard. Two properties decide whether
// that listing is honest and whether it is affordable, and both are measured
// here rather than argued: the columns it advertises are the columns the
// engine actually answers with, and asking WHAT is queryable costs less than
// querying it.

import (
	"path/filepath"
	"testing"
	"time"
)

// TestSurfaceColumnsAreTheEngineColumns pins the derivation
// (engineRelationColumns) against the engine itself for EVERY routed standard
// and for every relation FORM that standard has: the base name, the default
// partition and a real per-source partition. The surface stopped probing the
// engine for columns (226 prepared statements over UNION ALL views per
// listing); this is what makes the cheap answer the same answer.
func TestSurfaceColumnsAreTheEngineColumns(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{ProviderID: "surface", SourceName: "surface-probe", BatchID: "surface@1"}
	if _, err := store.StoreWithSourceTags("OMM.fbs",
		buildEngineOMM(t, 5501, "SURFACE-1", time.Now().UTC().Unix()), "space-data-network-02", nil, tags); err != nil {
		t.Fatalf("store $OMM: %v", err)
	}

	routed := store.engineRoutedSchemaNames()
	if len(routed) == 0 {
		t.Fatal("no standard is routed — this guard has nothing to check")
	}
	for _, schemaName := range routed {
		base := engineRoutedSchemas[schemaName].Table
		derived, ok := engineRelationColumns(base)
		if !ok {
			t.Fatalf("%s is routed but the engine schema text declares no table %q", schemaName, base)
		}
		for _, relation := range []string{base, base + "@" + engineDefaultSource, base + "@surface-probe"} {
			res, err := store.engineDB.Query(`SELECT * FROM ` + quoteEngineRelation(relation) + ` LIMIT 0`)
			if err != nil {
				t.Fatalf("probe %s: %v", relation, err)
			}
			if len(res.Columns) != len(derived) {
				t.Fatalf("%s: engine answers %d columns %v, the surface would advertise %d %v",
					relation, len(res.Columns), res.Columns, len(derived), derived)
			}
			for i := range derived {
				if res.Columns[i] != derived[i] {
					t.Fatalf("%s column %d: engine %q, surface %q", relation, i, res.Columns[i], derived[i])
				}
			}
		}
	}

	// And what the surface REPORTS is that same list, for the base relation
	// and for the populated partitions alike.
	surface, err := store.PublicQuerySurface()
	if err != nil {
		t.Fatalf("PublicQuerySurface: %v", err)
	}
	if len(surface) == 0 {
		t.Fatal("the public query surface is empty")
	}
	for _, entry := range surface {
		base := entry.Name
		if entry.Source != "" {
			base = base[:len(base)-len(entry.Source)-1]
		}
		derived, ok := engineRelationColumns(base)
		if !ok {
			t.Fatalf("the surface lists %q, which the engine schema text does not declare", entry.Name)
		}
		if len(entry.Columns) != len(derived) {
			t.Fatalf("%s advertises %v, want %v", entry.Name, entry.Columns, derived)
		}
	}
}

// TestPublicQuerySurfaceCostDoesNotScaleWithRoutedStandards is the COST gate,
// expressed as the statement account rather than as a stopwatch (the engine
// counts every statement it runs — flatsqlrt.Runtime.Stats).
//
// The listing is anonymous, it holds the store lock and the single-threaded
// engine, and it names 226 standards. Probing each standard's columns cost one
// prepared statement per standard against a UNION ALL view with one branch per
// source, which measured ~190x an actual `SELECT _data ... LIMIT 10` on the
// same store: asking what is queryable was far more expensive than querying,
// and a handful of listings per second saturated the engine every ingest
// queues behind. The property that fixes it is that the listing costs NOTHING
// for a standard with nothing resident — no matter how many are routed.
func TestPublicQuerySurfaceCostDoesNotScaleWithRoutedStandards(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	routed := store.engineRoutedSchemaNames()
	if len(routed) < 100 {
		t.Fatalf("%d standards routed — the fixture no longer measures a large catalog", len(routed))
	}

	before, _, _, _ := store.engine.Stats()
	surface, err := store.PublicQuerySurface()
	if err != nil {
		t.Fatalf("PublicQuerySurface: %v", err)
	}
	after, _, _, _ := store.engine.Stats()
	if len(surface) != len(routed) {
		t.Fatalf("surface lists %d relations on an empty store, want one per routed standard (%d)", len(surface), len(routed))
	}
	if spent := after - before; spent != 0 {
		t.Fatalf("listing %d routed standards cost %d engine statements, want 0 — an empty store has nothing to count",
			len(routed), spent)
	}

	// One populated standard costs one count per relation it actually has,
	// and NOTHING for the other 225.
	tags := SourceTags{ProviderID: "surface", SourceName: "surface-cost", BatchID: "cost@1"}
	if _, err := store.StoreWithSourceTags("OMM.fbs",
		buildEngineOMM(t, 5502, "SURFACE-2", time.Now().UTC().Unix()), "space-data-network-02", nil, tags); err != nil {
		t.Fatalf("store $OMM: %v", err)
	}

	before, _, _, _ = store.engine.Stats()
	surface, err = store.PublicQuerySurface()
	if err != nil {
		t.Fatalf("PublicQuerySurface: %v", err)
	}
	after, _, _, _ = store.engine.Stats()

	populated := 0
	for _, entry := range surface {
		base := entry.Name
		if entry.Source != "" {
			base = base[:len(base)-len(entry.Source)-1]
		}
		if store.engineResidentCount(base+".fbs") > 0 {
			populated++
		}
	}
	if populated == 0 {
		t.Fatal("the stored record left no populated relation — the fixture proves nothing")
	}
	if spent := after - before; spent != int64(populated) {
		t.Fatalf("listing cost %d engine statements for %d populated relation(s) out of %d listed — the count must be per POPULATED relation, not per routed standard",
			spent, populated, len(surface))
	}
}

// TestAStaleViewProjectionIsRebuiltSoTheDerivedColumnsStayTrue is what lets
// the surface derive columns instead of probing them.
//
// A unified view enumerates its projection EXPLICITLY, so it is a PERSISTED
// COPY of a column list, and the boot-time rebuild used to be conditional on
// SOURCE coverage alone. An upgraded binary whose catalog changed a standard's
// projected fields therefore kept serving the previous view: `SELECT *` on the
// view answered the OLD columns while the shadow vtabs — re-declared from the
// current schema text at connect — answered the new ones. Derived columns
// would then advertise something the view does not have. The boot compares the
// persisted projection against the current declaration and rebuilds on a
// mismatch, which is what this pins.
func TestAStaleViewProjectionIsRebuiltSoTheDerivedColumnsStayTrue(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if _, err := store.Store("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1700000000), "peer", nil); err != nil {
		t.Fatalf("store $OMM: %v", err)
	}

	// A PREVIOUS binary's view for $IRM: the same source branches (so the
	// source-coverage check is satisfied) projecting FEWER columns.
	sources := make([]string, 0, len(store.engineSources))
	for source := range store.engineSources {
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		t.Fatal("the store registered no engine source")
	}
	stale := ""
	for i, source := range sources {
		if i > 0 {
			stale += " UNION ALL "
		}
		stale += `SELECT "JOB_ID", "_source", "_rowid", "_offset", "_data" FROM "IRM@` + source + `"`
	}
	if _, err := store.engineDB.Query(`DROP VIEW IF EXISTS "IRM"`); err != nil {
		t.Fatalf("drop the current view: %v", err)
	}
	if _, err := store.engineDB.Query(`CREATE VIEW "IRM" AS ` + stale); err != nil {
		t.Fatalf("seed the stale view: %v", err)
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()

	derived, ok := engineRelationColumns("IRM")
	if !ok {
		t.Fatal("$IRM is not declared by the engine schema text")
	}
	res, err := reopened.engineDB.Query(`SELECT * FROM "IRM" LIMIT 0`)
	if err != nil {
		t.Fatalf("probe the reopened view: %v", err)
	}
	if len(res.Columns) != len(derived) {
		t.Fatalf("the stale view survived the boot: engine answers %v, the surface derives %v", res.Columns, derived)
	}
	for i := range derived {
		if res.Columns[i] != derived[i] {
			t.Fatalf("column %d: engine %q, derived %q", i, res.Columns[i], derived[i])
		}
	}
}
