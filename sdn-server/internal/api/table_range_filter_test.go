package api

import "testing"

func TestParseRangeFilterReadsEpochAndUtcBounds(t *testing.T) {
	cases := []struct {
		term   string
		lo, hi float64
		ok     bool
	}{
		{"1788048000..1788134400", 1788048000, 1788134400, true},
		{"2026-08-30..2026-08-31", 1788048000, 1788134400, true},
		{"2026-08-30T14:08:27Z..2026-08-30T15:00:00Z", 1788098907, 1788102000, true},
		{">=2026-08-30", 1788048000, 0, true},
		{"<=1788134400", 0, 1788134400, true},
		{"2026-08-31..2026-08-30", 0, 0, false},
		{"ISS", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		lo, hi, ok := parseRangeFilter(c.term)
		if ok != c.ok {
			t.Fatalf("%q: ok=%v want %v", c.term, ok, c.ok)
		}
		if !ok {
			continue
		}
		if c.lo != 0 && lo != c.lo {
			t.Fatalf("%q: lo=%v want %v", c.term, lo, c.lo)
		}
		if c.hi != 0 && hi != c.hi {
			t.Fatalf("%q: hi=%v want %v", c.term, hi, c.hi)
		}
	}
	values := map[string]string{"EPOCH": "1788098907", "OBJECT_NAME": "ISS"}
	if !fullTableRecordMatches(values, []string{"EPOCH", "OBJECT_NAME"}, map[string]string{"EPOCH": "2026-08-30..2026-08-31"}, "") {
		t.Fatal("a record inside the UTC day range must match")
	}
	if fullTableRecordMatches(values, []string{"EPOCH", "OBJECT_NAME"}, map[string]string{"EPOCH": "2026-08-31..2026-09-01"}, "") {
		t.Fatal("a record outside the range must not match")
	}
	if !fullTableRecordMatches(values, []string{"EPOCH", "OBJECT_NAME"}, map[string]string{"OBJECT_NAME": "iss"}, "") {
		t.Fatal("plain contains filters keep working")
	}
}
