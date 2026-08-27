package caps

import "testing"

// Measured on host-02 2026-08-26: the cell-tower ingest module stamps "TBS",
// so 258,125 rows landed with sdn_record_index.schema_name "TBS". Readers
// normalize — the engine routes on "TBS.fbs" and a publication request is
// normalized to "TBS.fbs" — so the export join
// `idx.schema_name = "TBS.fbs"` matched nothing and every publish answered
// "no records match export query" against a quarter-million-row store. The
// control, RFB.fbs, published in about a second.
func TestBareStandardCodeIsStoredUnderItsCanonicalSchemaName(t *testing.T) {
	for _, spelling := range []string{"TBS", "tbs", "Tbs", "TBS.fbs", "tbs.fbs", "  TBS  "} {
		if got := canonicalStoredSchemaName(spelling); got != "TBS.fbs" {
			t.Fatalf("canonicalStoredSchemaName(%q) = %q, want TBS.fbs", spelling, got)
		}
	}
}

// The schemas that already published correctly must not move: their stored
// spelling is the canonical one and stays byte-identical.
func TestCanonicalSchemaNamesAreUnchanged(t *testing.T) {
	for _, name := range []string{"OMM.fbs", "RFB.fbs", "CAT.fbs", "IRM.fbs", "MPE.fbs"} {
		if got := canonicalStoredSchemaName(name); got != name {
			t.Fatalf("canonicalStoredSchemaName(%q) = %q, want it unchanged", name, got)
		}
	}
}

// Input that cannot be a schema name at all is passed through untouched, so
// the store's own validation refuses it with its own error instead of this
// helper inventing one — and so a traversal attempt is never rewritten into
// something that looks legitimate.
func TestUnusableSchemaInputIsPassedThroughForTheStoreToRefuse(t *testing.T) {
	for _, bad := range []string{"", "   ", "../etc/passwd", "a/b", "a\\b", "..fbs"} {
		if got := canonicalStoredSchemaName(bad); got != bad {
			t.Fatalf("canonicalStoredSchemaName(%q) = %q, want it passed through unchanged", bad, got)
		}
	}
}

// The two spellings of one standard must not be able to produce two different
// stored names — that divergence IS the defect.
func TestEverySpellingOfAStandardCollapsesToOneStoredName(t *testing.T) {
	for _, code := range []string{"TBS", "IRM", "OMM", "RFB"} {
		bare := canonicalStoredSchemaName(code)
		file := canonicalStoredSchemaName(code + ".fbs")
		lower := canonicalStoredSchemaName(lowerASCII(code))
		if bare != file || bare != lower {
			t.Fatalf("%s stores as %q / %q / %q — one standard must have ONE stored name", code, bare, file, lower)
		}
	}
}

func lowerASCII(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + 32
		}
	}
	return string(out)
}
