package storage

import (
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestEngineRelationSchemaTextContainsRootAndDependencies(t *testing.T) {
	for _, code := range []string{"OMM", "CAT", "OCM", "CDM", "EPM", "CNP"} {
		text, table, fileID, ok := EngineRelationSchemaText(code)
		if !ok || table != code || fileID != "$"+code {
			t.Fatalf("%s: invalid binding", code)
		}
		if !strings.Contains(text, "root_type "+table+";") || !strings.Contains(text, `file_identifier "`+fileID+`";`) {
			t.Fatalf("%s: missing root metadata", code)
		}
		roots, identifiers := 0, 0
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "include ") {
				t.Fatalf("%s: unresolved include", code)
			}
			if strings.HasPrefix(line, "root_type ") {
				roots++
			}
			if strings.HasPrefix(line, "file_identifier ") {
				identifiers++
			}
		}
		if roots != 1 || identifiers != 1 {
			t.Fatalf("%s: roots=%d identifiers=%d", code, roots, identifiers)
		}
	}
	text, _, _, _ := EngineRelationSchemaText("OMM")
	for _, declaration := range []string{"table RFM {", "enum timeSystem", "table OMM {"} {
		if !strings.Contains(text, declaration) {
			t.Fatalf("missing dependency declaration %s", declaration)
		}
	}
}

func TestEngineRelationSchemaTextAcceptsCodeSpellings(t *testing.T) {
	canonical, _, _, ok := EngineRelationSchemaText("CNP")
	if !ok {
		t.Fatal("CNP not routed")
	}
	for _, spelling := range []string{"cnp", "CNP.fbs", "cnp.fbs", " CNP.fbs "} {
		text, table, fileID, ok := EngineRelationSchemaText(spelling)
		if !ok || text != canonical || table != "CNP" || fileID != "$CNP" {
			t.Fatalf("%q: unexpected schema binding", spelling)
		}
	}
	for _, unknown := range []string{"NOPE.fbs", "NOPE", "", ".fbs", "sdn_record_index"} {
		if text, table, fileID, ok := EngineRelationSchemaText(unknown); ok || text != "" || table != "" || fileID != "" {
			t.Fatalf("%q: expected refusal", unknown)
		}
	}
}

func TestEngineRelationSchemaTextCoversEveryRoutedStandard(t *testing.T) {
	registry, err := sds.NewSchemaRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range engineRoutedSchemaNames() {
		text, table, _, ok := EngineRelationSchemaText(name)
		if !ok {
			t.Fatalf("%s: routed but no schema", name)
		}
		source, found := registry.Get(name)
		if !found {
			t.Fatalf("%s: embedded source missing", name)
		}
		// Inlining preserves the complete root declaration, including field order.
		want := parseEngineDeclaredColumns(string(source))[table]
		got := parseEngineDeclaredColumns(text)[table]
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: root fields changed", name)
		}
	}
}
