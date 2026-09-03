package storage

import (
	"strings"
	"testing"
)

// TestEngineRelationSchemaTextIsTheEngineTableBlock pins that the text handed
// to a browser-hosted engine is the node's own table declaration: the same
// declared columns, in the same order, that engineRelationColumns derives for
// the node's relation — nothing more (no meta columns, no comments, no
// neighbouring tables) and nothing less.
func TestEngineRelationSchemaTextIsTheEngineTableBlock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		table  string
		fileID string
	}{
		{name: "OMM", table: "OMM", fileID: "$OMM"},
		{name: "CNP", table: "CNP", fileID: "$CNP"},
	} {
		text, table, fileID, ok := EngineRelationSchemaText(tc.name)
		if !ok {
			t.Fatalf("%s: not routed", tc.name)
		}
		if table != tc.table || fileID != tc.fileID {
			t.Fatalf("%s: table=%q fileID=%q, want %q/%q", tc.name, table, fileID, tc.table, tc.fileID)
		}
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) < 3 {
			t.Fatalf("%s: block too short: %q", tc.name, text)
		}
		if got := strings.TrimSpace(lines[0]); got != "table "+tc.table+" {" {
			t.Fatalf("%s: first line %q, want `table %s {`", tc.name, got, tc.table)
		}
		if !strings.HasPrefix(lines[0], "  table ") {
			t.Fatalf("%s: indentation not preserved: %q", tc.name, lines[0])
		}
		if got := strings.TrimSpace(lines[len(lines)-1]); got != "}" {
			t.Fatalf("%s: last line %q, want `}`", tc.name, got)
		}
		if !strings.HasSuffix(text, "}\n") {
			t.Fatalf("%s: block must end with a single newline: %q", tc.name, text[len(text)-4:])
		}
		tables := 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.Contains(trimmed, "//") {
				t.Fatalf("%s: comment line in engine DDL: %q", tc.name, line)
			}
			if strings.HasPrefix(trimmed, "table ") {
				tables++
			}
		}
		if tables != 1 {
			t.Fatalf("%s: %d table declarations in one block", tc.name, tables)
		}

		declared := parseEngineDeclaredColumns(text)[tc.table]
		relation, ok := engineRelationColumns(tc.table)
		if !ok {
			t.Fatalf("%s: engineRelationColumns missing", tc.table)
		}
		want := relation[:len(relation)-len(engineRelationMetaColumns)]
		if strings.Join(declared, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: declared columns\n got %v\nwant %v", tc.name, declared, want)
		}
		for _, meta := range engineRelationMetaColumns {
			for _, col := range declared {
				if col == meta {
					t.Fatalf("%s: meta column %q must not be declared in the DDL", tc.name, meta)
				}
			}
		}
	}
}

// TestEngineRelationSchemaTextAcceptsCodeSpellings pins the accepted name
// forms: code, lower-case code, schema file name — all the same block — and
// refuses an unknown standard.
func TestEngineRelationSchemaTextAcceptsCodeSpellings(t *testing.T) {
	canonical, _, _, ok := EngineRelationSchemaText("CNP")
	if !ok {
		t.Fatal("CNP not routed")
	}
	if !strings.Contains(canonical, "ID:string;") {
		t.Fatalf("CNP block lacks ID:string;: %q", canonical)
	}
	for _, spelling := range []string{"cnp", "CNP.fbs", "cnp.fbs", " CNP.fbs "} {
		text, table, fileID, ok := EngineRelationSchemaText(spelling)
		if !ok || text != canonical || table != "CNP" || fileID != "$CNP" {
			t.Fatalf("%q: ok=%v table=%q fileID=%q same=%v", spelling, ok, table, fileID, text == canonical)
		}
	}
	for _, unknown := range []string{"NOPE.fbs", "NOPE", "", ".fbs", "sdn_record_index"} {
		if text, table, fileID, ok := EngineRelationSchemaText(unknown); ok || text != "" || table != "" || fileID != "" {
			t.Fatalf("%q: ok=%v text=%q table=%q fileID=%q, want refused", unknown, ok, text, table, fileID)
		}
	}
}

// TestEngineRelationSchemaTextCoversEveryRoutedStandard: every standard the
// node routes into its engine has a block a browser engine can be created
// from, and the block's columns are the node's columns.
func TestEngineRelationSchemaTextCoversEveryRoutedStandard(t *testing.T) {
	for _, name := range engineRoutedSchemaNames() {
		text, table, _, ok := EngineRelationSchemaText(name)
		if !ok {
			t.Fatalf("%s: routed but no DDL block", name)
		}
		declared := parseEngineDeclaredColumns(text)[table]
		relation, _ := engineRelationColumns(table)
		want := relation[:len(relation)-len(engineRelationMetaColumns)]
		if strings.Join(declared, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: block columns %v != engine columns %v", name, declared, want)
		}
	}
}
