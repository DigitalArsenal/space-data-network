package modulert

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoHardcodedRecordTypeOrdinals makes recurrence a BUILD FAILURE rather than
// a production failure.
//
// The defect this guards (sdn-rec-ordinal-hardcoded-mbl-80) was not one bad line
// — it was the same hand-copied literal duplicated into four files, plus test
// fixtures that carried the literal too and would therefore have kept passing
// after production broke. A fix that only edits today's four sites leaves the
// next author free to write `record.valueType() != 80` again.
//
// So: no source file in this package may compare a Record's value_type at all.
// Selection is by Record.standard. See the mblStandard comment in
// publication_signature.go for why the ordinal is unsafe in both directions.
func TestNoHardcodedRecordTypeOrdinals(t *testing.T) {
	// A comparison against value_type in any form.
	valueTypeCompare := regexp.MustCompile(`valueType\(\)\s*[!=]=`)
	// A resurrected named constant for the ordinal.
	namedOrdinal := regexp.MustCompile(`(?i)const\s+\w*recordTypeMBL\w*\s+byte\s*=`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// The fixtures legitimately WRITE an ordinal (a real publisher does);
		// what they must not do is let production READ one. Writing is
		// PrependByteSlot, which neither pattern matches, so no file is exempt
		// from the comparison ban.
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(source), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx] // comments may discuss the old literal
			}
			if valueTypeCompare.MatchString(code) {
				t.Errorf("%s:%d compares the RecordType union ordinal — select by Record.standard instead (see mblStandard):\n\t%s",
					filepath.Base(name), i+1, strings.TrimSpace(line))
			}
			if namedOrdinal.MatchString(code) && !strings.Contains(code, "Current") && !strings.Contains(code, "Legacy") {
				t.Errorf("%s:%d re-declares a RecordType ordinal constant for dispatch:\n\t%s",
					filepath.Base(name), i+1, strings.TrimSpace(line))
			}
		}
	}
}
