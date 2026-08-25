package storage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/enginecatalog"
)

// embeddedSchemaDir is the IDL directory the generated catalog is derived
// from. The generator reads the same path.
const embeddedSchemaDir = "../sds/schemas"

// TestEveryEmbeddedStandardIsRoutedOrDeclaredUnroutable is the owner directive
// (2026-08-25) as a test: "$IRM just needs to be another one of the standards
// ingested like all the others". Every standard the node embeds is either
// routed into the engine or carries a stated reason why it cannot be — and a
// stale reason fails, so a standard cannot stay un-routed once its IDL is
// fixed.
func TestEveryEmbeddedStandardIsRoutedOrDeclaredUnroutable(t *testing.T) {
	for _, schemaName := range sds.SupportedSchemas {
		_, routed := engineRoutedSchemaFor(schemaName)
		reason, declared := engineUnroutableSchemas[schemaName]
		switch {
		case routed && declared:
			t.Errorf("%s is both routed and declared unroutable (%s)", schemaName, reason)
		case !routed && !declared:
			t.Errorf("%s is neither routed nor declared unroutable — every embedded standard must be one or the other", schemaName)
		}
	}
	for schemaName := range engineRoutedSchemas {
		if !sdsSupports(schemaName) {
			t.Errorf("%s is routed but is not an embedded standard", schemaName)
		}
	}
	for schemaName, reason := range engineUnroutableSchemas {
		if !sdsSupports(schemaName) {
			t.Errorf("%s is declared unroutable (%s) but is not an embedded standard", schemaName, reason)
		}
		// THE EXCLUSION MUST STILL BE TRUE, WHATEVER ITS REASON. Every reason
		// string is re-validated against the IDL here, and an UNKNOWN reason
		// fails rather than being waved through — a carve-out this test cannot
		// re-check is a permanently unverified exclusion, which is precisely
		// what the owner directive ("every standard ingested like all the
		// others") exists to prevent.
		switch reason {
		case enginecatalog.SkipNoFileIdentifier:
			// The moment Themis mints a file_identifier, this fails and the
			// standard routes with no code change here.
			src, err := os.ReadFile(filepath.Join(embeddedSchemaDir, schemaName))
			if err != nil {
				t.Fatalf("read %s: %v", schemaName, err)
			}
			if strings.Contains(string(src), "file_identifier") {
				t.Errorf("%s now declares a file_identifier: drop the exclusion and regenerate the catalog", schemaName)
			}
		default:
			t.Errorf("%s is declared unroutable for reason %q, which this test cannot re-validate against the IDL — add the check for that reason here, or the exclusion is unverifiable forever", schemaName, reason)
		}
	}
}

func sdsSupports(schemaName string) bool {
	for _, s := range sds.SupportedSchemas {
		if s == schemaName {
			return true
		}
	}
	return false
}

// TestRoutedBindingsMatchTheEmbeddedIDL pins the two facts ingest depends on:
// a routed standard's table is its STANDARD CODE (so the module contract is
// uniform — `SELECT _data FROM <CODE>`), and its file identifier is the one
// its own IDL declares (so a buffer's four header bytes route to its own
// table and no other).
func TestRoutedBindingsMatchTheEmbeddedIDL(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, nil)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	declared := make(map[string]string, len(catalog.Bindings))
	for _, binding := range catalog.Bindings {
		declared[binding.Schema] = binding.FileID
	}

	seen := map[string]string{}
	for schemaName, binding := range engineRoutedSchemas {
		code := strings.TrimSuffix(schemaName, ".fbs")
		if binding.Table != code {
			t.Errorf("%s routes to table %q, want the standard code %q", schemaName, binding.Table, code)
		}
		if want := declared[schemaName]; binding.FileID != want {
			t.Errorf("%s routes file id %q, but its IDL declares %q", schemaName, binding.FileID, want)
		}
		if prev, dup := seen[binding.FileID]; dup {
			t.Errorf("file id %q is routed by both %s and %s", binding.FileID, prev, schemaName)
		}
		seen[binding.FileID] = schemaName

		// The table MUST exist in the schema the engine database is created
		// from: FlatSQLDatabase::registerFileId throws on an unknown table,
		// and a throw in the -fignore-exceptions engine is a poisoned runtime.
		if strings.Count(engineDatabaseSchema, "table "+binding.Table+" {") != 1 {
			t.Errorf("table %s appears %d times in the engine schema, want exactly 1",
				binding.Table, strings.Count(engineDatabaseSchema, "table "+binding.Table+" {"))
		}
	}
}

// TestGeneratedEngineCatalogIsUpToDate regenerates the committed catalog from
// the embedded IDLs and byte-compares. This is what makes a COMMITTED
// generated artifact safe: it cannot drift from the schemas it was derived
// from, so an SDS pin bump that adds a standard fails here until the catalog
// is regenerated.
func TestGeneratedEngineCatalogIsUpToDate(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	want := catalog.Render()
	got, err := os.ReadFile("engine_standard_catalog.go")
	if err != nil {
		t.Fatalf("read generated catalog: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("engine_standard_catalog.go is stale — run `go generate ./sdn-server/internal/storage/...`")
	}
}

// TestPinnedEngineSchemaIsUnchangedByTheCatalog guards the cross-host
// contract: shared-test-vectors/flatsql-parity.json pins engineRecordSchema
// byte-for-byte and both hosts build their parity database from it, so the
// generated catalog may only ever be APPENDED.
func TestPinnedEngineSchemaIsUnchangedByTheCatalog(t *testing.T) {
	if !strings.HasPrefix(engineDatabaseSchema, engineRecordSchema) {
		t.Fatal("engineRecordSchema is no longer the PREFIX of the engine database schema: the OMM parity contract is broken")
	}
	if strings.Contains(engineStandardCatalogGraph, "table OMM {") ||
		strings.Contains(engineStandardCatalogGraph, "table TBS {") {
		t.Fatal("the generated catalog re-emits a pinned standard's table")
	}
}

// TestEveryRoutedTableDeclaresAtLeastOneColumn is a FATALITY guard, not a
// style rule. A table with no columns makes the engine emit
// `CREATE TABLE x(, "_source" TEXT, ...)` and `SELECT , "_source", ... FROM`
// — both malformed — from the THROWING updateSQLiteTable / createUnifiedView.
// The embedded engine is the -fignore-exceptions build, where a throw lowers
// to `unreachable` and poisons the runtime, so a zero-column table would be a
// daemon that cannot open its own store.
func TestEveryRoutedTableDeclaresAtLeastOneColumn(t *testing.T) {
	columns := map[string][]string{}
	current := ""
	for _, line := range strings.Split(engineDatabaseSchema, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "table ") && strings.HasSuffix(trimmed, "{"):
			current = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "table "), "{"))
			columns[current] = nil
		case trimmed == "}":
			current = ""
		case current != "" && strings.Contains(trimmed, ":"):
			columns[current] = append(columns[current], strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[0]))
		}
	}

	meta := map[string]bool{"_source": true, "_rowid": true, "_offset": true, "_data": true}
	for _, binding := range engineRoutedSchemas {
		cols, ok := columns[binding.Table]
		if !ok {
			t.Errorf("routed table %s is missing from the engine schema", binding.Table)
			continue
		}
		if len(cols) == 0 {
			t.Errorf("routed table %s declares no columns: its vtab DDL would be malformed and would TRAP the engine", binding.Table)
		}
		seen := map[string]bool{}
		for _, col := range cols {
			if meta[col] {
				t.Errorf("%s column %q collides with an engine meta column", binding.Table, col)
			}
			if seen[col] {
				t.Errorf("%s declares column %q twice", binding.Table, col)
			}
			seen[col] = true
		}
	}
}

// TestEngineSchemaTextExcludingDropsOnlyTheNamedTables pins the fail-closed
// collision guard's mechanics: an excluded standard's table is REMOVED from
// the schema the database is created from (so createUnifiedView never DROPs
// the plain control table of that name), and nothing else moves.
func TestEngineSchemaTextExcludingDropsOnlyTheNamedTables(t *testing.T) {
	if got := engineSchemaTextExcluding(nil); got != engineDatabaseSchema {
		t.Fatal("an empty exclusion set must return the schema text verbatim")
	}
	reduced := engineSchemaTextExcluding(map[string]bool{"IRM.fbs": true, "CDM.fbs": true})
	if strings.Contains(reduced, "table IRM {") || strings.Contains(reduced, "table CDM {") {
		t.Fatal("excluded standards are still in the schema text")
	}
	if !strings.Contains(reduced, "table OMM {") || !strings.Contains(reduced, "table TBS {") {
		t.Fatal("excluding a standard removed an unrelated table")
	}
	before := strings.Count(engineDatabaseSchema, "  table ")
	if after := strings.Count(reduced, "  table "); after != before-2 {
		t.Fatalf("reduced schema declares %d tables, want %d", after, before-2)
	}
	// JOB_ID is IRM's first column and CCSDS_CDM_VERS is CDM's; neither may
	// survive its table's removal.
	for _, col := range []string{"JOB_ID:string", "CCSDS_CDM_VERS:double"} {
		if strings.Contains(reduced, col) {
			t.Fatalf("column %q outlived its excluded table", col)
		}
	}
}

// TestGeneratedCatalogProjectionIsDeterministic pins the two properties the
// slot arithmetic depends on: tables are emitted in sorted schema order, and
// the fallback set (standards whose slot 0 is not representable) is exactly
// what the generator reports.
func TestGeneratedCatalogProjectionIsDeterministic(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	names := make([]string, 0, len(catalog.Bindings))
	for _, binding := range catalog.Bindings {
		names = append(names, binding.Schema)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatal("generated bindings are not in sorted schema order: the artifact would churn on every regeneration")
	}
	for _, binding := range catalog.Bindings {
		fallback := len(binding.Columns) == 1 && binding.Columns[0].Junk
		_, declared := engineUnprojectableFirstFields[binding.Schema]
		if fallback != declared {
			t.Errorf("%s: fallback column = %v but engineUnprojectableFirstFields says %v", binding.Schema, fallback, declared)
		}
	}
}
