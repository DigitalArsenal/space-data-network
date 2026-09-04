package storage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage/enginecatalog"
)

// TestGeneratedColumnsReadTheirOwnFieldValue is the value assertion the
// generated catalog was missing.
//
// The other catalog tests pin the routed SET, the >=1-column invariant,
// determinism and byte-identical regeneration — none of which can catch the
// thing that actually silently corrupts data here: the LEADING-RUN SLOT
// ARITHMETIC. Vtable slots are positional, so an enum resolved to the wrong
// underlying scalar, a union that does not stop the run at its two-slot
// boundary, or a dropped deprecated field would make a column decode its
// NEIGHBOUR'S value — a wrong number, never a missing one.
//
// THE WRITE SIDE IS NOT THE CATALOG. An earlier version of this test built
// each synthetic record from the binding's own column list, which made it
// TAUTOLOGICAL: value_i was written at slot i with type T_i taken from the
// very declaration under test, so it passed for any self-consistent binding,
// including a wrong one. The record is now laid out by the flatc oracle
// (engine_standard_catalog_flatc_test.go) — flatc's slot numbers, flatc's
// field types, and live values in the slots PAST the projected run — and only
// the read side is the catalog. A column that reads the wrong slot now reads
// a real neighbouring value and fails.
func TestGeneratedColumnsReadTheirOwnFieldValue(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	roots := loadFlatcOracle(t, catalog.Bindings)

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	// A FRESH STORE EXCLUDES NOTHING, so the routed set is the whole catalog
	// and the loop below is exhaustive by construction. A floor ("at least
	// 100") would let a collapse back toward {OMM, TBS} pass quietly.
	if got, want := len(store.engineRoutedSchemaNames()), len(engineRoutedSchemas); got != want {
		t.Fatalf("fresh store routes %d schemas, want every routed schema (%d)", got, want)
	}
	if got, want := len(engineRoutedSchemas), len(catalog.Bindings)+len(enginePinnedSchemas)+len(enginePublishedBindingSchemas); got != want {
		t.Fatalf("%d routed schemas, want %d generated + %d pinned + %d published-binding",
			got, len(catalog.Bindings), len(enginePinnedSchemas), len(enginePublishedBindingSchemas))
	}

	checked, junk := 0, 0
	for _, binding := range catalog.Bindings {
		root, ok := roots[binding.Schema]
		if !ok {
			continue // unvendored: no oracle, asserted absent in loadFlatcOracle
		}
		if !store.engineRoutesSchema(binding.Schema) {
			t.Fatalf("%s is in the generated catalog but this store does not route it", binding.Schema)
		}
		// A junk column reads a slot whose real type is not representable, so
		// there is no field value to assert against. Its own guarantees —
		// fixed-width, JSON-safe, and never the field's value — are pinned by
		// TestUnprojectableFirstFieldColumnsAreJSONSafe.
		if binding.Columns[0].Junk {
			junk++
			continue
		}
		for _, problem := range flatcValueMismatch(store.engineDB, binding.Table, binding.Columns, root) {
			t.Error(problem)
		}
		checked++
	}

	// EXACT, not a floor: every vendored generated standard that is not a
	// junk fallback carries a value assertion.
	want := len(catalog.Bindings) - len(engineFlatcUnvendoredStandards) - junk
	if checked != want {
		t.Fatalf("value-checked %d standards, want %d", checked, want)
	}
	t.Logf("value-checked %d generated standards (%d junk-fallback, %d unvendored)",
		checked, junk, len(engineFlatcUnvendoredStandards))
}

// TestCatalogValueGuardFailsOnAMutatedProjection is the guard's own test.
//
// A read-back assertion is only worth what it REJECTS, and the tautological
// version of this file rejected nothing: both generator mutations below left
// it green. So both mutations are applied here — to the projection RULE that
// TestGeneratedCatalogMatchesTheVendoredFlatcBindings proves reproduces the
// committed catalog exactly — and the guard must fail on each:
//
//   - continue-past-unprojectable: `if !ok { continue }` in place of
//     `if !ok { break }` in enginecatalog.Build's projection loop, which
//     skips a non-representable field and slides every later column onto its
//     neighbour's slot;
//   - drop-union-terminal: dropping Column.Terminal, which lets the
//     projection run onto the value-offset slot flatc gives a union.
//
// Each mutated table is created in a SEPARATE engine database from its own
// schema text, so the mutation is measured by the real engine reading a real
// record — not by comparing declarations.
//
// WHAT THIS RESTS ON, STATED SO IT CANNOT ROT QUIETLY: the mutations are
// applied to projectFromFlatc, a reimplementation of the projection rule, not
// to enginecatalog.Build itself. That is only worth something while the
// reimplementation still reproduces the committed catalog EXACTLY — which is
// not assumed here, it is asserted for every non-junk binding by
// TestGeneratedCatalogMatchesTheVendoredFlatcBindings. If that assertion is
// ever weakened, these mutations stop standing in for the generator's and
// this file goes back to being decoration. (Verified out of band that
// mutating the REAL generator and regenerating fails both this guard and
// TestGeneratedColumnsReadTheirOwnFieldValue.)
func TestCatalogValueGuardFailsOnAMutatedProjection(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	roots := loadFlatcOracle(t, catalog.Bindings)

	engine, err := flatsqlrt.New(flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()))
	if err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer engine.Close()

	for _, mutation := range []flatcMutation{flatcMutationContinue, flatcMutationNoTerminal} {
		t.Run(string(mutation), func(t *testing.T) {
			type mutant struct {
				binding enginecatalog.Binding
				cols    []enginecatalog.Column
				root    *flatcRoot
			}
			var mutants []mutant
			var schema string
			for _, binding := range catalog.Bindings {
				root, ok := roots[binding.Schema]
				if !ok || binding.Columns[0].Junk {
					continue
				}
				cols := projectFromFlatc(root, mutation)
				if sameColumns(cols, binding.Columns) {
					continue // this standard's shape cannot express the mutation
				}
				mutants = append(mutants, mutant{binding: binding, cols: cols, root: root})
				schema += mutatedSchemaText(binding.Table, cols)
			}
			if len(mutants) == 0 {
				t.Fatalf("mutation %q changed no standard's projection: it cannot be tested", mutation)
			}

			db, err := engine.CreateDatabase(schema, "mutation-"+string(mutation))
			if err != nil {
				t.Fatalf("create mutated database: %v", err)
			}
			defer db.Destroy()
			for _, m := range mutants {
				if err := db.RegisterFileID(m.binding.FileID, m.binding.Table); err != nil {
					t.Fatalf("%s: register mutated table: %v", m.binding.Schema, err)
				}
			}
			if err := db.RegisterSource(engineDefaultSource); err != nil {
				t.Fatalf("register source: %v", err)
			}
			if err := db.CreateUnifiedViews(); err != nil {
				t.Fatalf("create views: %v", err)
			}

			// BOTH HALVES OF THE GUARD ARE MEASURED SEPARATELY. The half
			// that was tautological is the VALUE half, so it is not enough
			// for the structural oracle to catch the mutation: the engine
			// read-back must reject it on its own.
			survivedValue := make([]string, 0, len(mutants))
			survivedStructural := make([]string, 0, len(mutants))
			for _, m := range mutants {
				if len(flatcColumnMismatch(enginecatalog.Binding{
					Schema:  m.binding.Schema,
					Table:   m.binding.Table,
					Root:    m.binding.Root,
					FileID:  m.binding.FileID,
					Columns: m.cols,
				}, m.root)) == 0 {
					survivedStructural = append(survivedStructural, m.binding.Schema)
				}
				if len(flatcValueMismatch(db, m.binding.Table, m.cols, m.root)) == 0 {
					survivedValue = append(survivedValue, m.binding.Schema)
				}
			}
			if len(survivedValue) > 0 {
				t.Errorf("mutation %q survived the VALUE guard on %d/%d standards: %v",
					mutation, len(survivedValue), len(mutants), survivedValue[:min(5, len(survivedValue))])
			}
			if len(survivedStructural) > 0 {
				t.Errorf("mutation %q survived the STRUCTURAL guard on %d/%d standards: %v",
					mutation, len(survivedStructural), len(mutants), survivedStructural[:min(5, len(survivedStructural))])
			}
			t.Logf("mutation %q rejected by both halves on all %d standards whose projection it changed",
				mutation, len(mutants))
		})
	}
}

func sameColumns(a, b []enginecatalog.Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || canonicalEngineType(a[i].Type) != canonicalEngineType(b[i].Type) {
			return false
		}
	}
	return true
}

// mutatedSchemaText renders one engine table exactly as Catalog.SchemaText
// does, so the mutated database differs from the real one ONLY in the columns
// the mutation moved.
func mutatedSchemaText(table string, cols []enginecatalog.Column) string {
	var b strings.Builder
	b.WriteString("  table " + table + " {\n")
	for _, col := range cols {
		b.WriteString("    " + col.Name + ":" + col.Type + ";\n")
	}
	b.WriteString("  }\n")
	return b.String()
}
