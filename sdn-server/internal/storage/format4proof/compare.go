package format4proof

import (
	"fmt"
	"sort"
	"strings"
)

// Policy says how a shape's answers compare.
type Policy struct {
	// Unordered: rows compare as a multiset (the operation defines no order).
	Unordered bool `json:"unordered,omitempty"`
	// Collapse (contract C-10): where format 1 joins tag rows and repeats a
	// record once per matching tag row, format 4 returns it once; format 1's
	// rows collapse to the first row of each CID before comparing.
	Collapse bool `json:"collapse,omitempty"`
	// Accepted names a documented difference (a contract clause or an owner
	// decision). A shape that differs under it is reported as accepted, never
	// as equal.
	Accepted string `json:"accepted,omitempty"`
}

// Equivalence statuses.
const (
	EqEqual       = "equal"
	EqEqualC12    = "equal (C-12 copy)"
	EqExtra       = "equal (S adds fields)"
	EqAccepted    = "accepted difference"
	EqDiffer      = "DIFFER"
	EqSError      = "S ERROR"
	EqF1Error     = "F1 error"
	EqBothError   = "both error"
	EqMissing     = "missing"
	EqUnstable    = "UNSTABLE"
	maxDiffsShown = 5
)

// Diff is one differing field.
type Diff struct {
	Call  string `json:"call"`
	Row   int    `json:"row"`
	Field string `json:"field"`
	F1    string `json:"f1"`
	S     string `json:"s"`
}

// Verdict is one shape's comparison.
type Verdict struct {
	Class    string   `json:"class"`
	Shape    string   `json:"shape"`
	Status   string   `json:"status"`
	F1Rows   int      `json:"f1_rows"`
	SRows    int      `json:"s_rows"`
	Calls    int      `json:"calls"`
	Notes    []string `json:"notes,omitempty"`
	Extra    []string `json:"extra_fields,omitempty"`
	Diffs    []Diff   `json:"diffs,omitempty"`
	Accepted string   `json:"accepted,omitempty"`
}

// Passed reports whether the verdict is an equivalence (accepted differences
// are not).
func (v Verdict) Passed() bool {
	switch v.Status {
	case EqEqual, EqEqualC12, EqExtra, EqBothError:
		return true
	}
	return false
}

// CopyOracle returns the copy variants (rows of "~" fields) format 1 holds
// for a CID: one row per stored copy.
type CopyOracle func(schema, cid string) ([]Row, error)

func isVariant(name string) bool { return strings.HasPrefix(name, "~") }

func strictText(r Row) string {
	var b strings.Builder
	for _, f := range r {
		if isVariant(f.N) {
			continue
		}
		b.WriteString(f.N)
		b.WriteByte('=')
		b.WriteString(f.V)
		b.WriteByte('\x1f')
	}
	return b.String()
}

func sortRows(rows []Row) []Row {
	out := append([]Row(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strictText(out[i]), strictText(out[j])
		if a != b {
			return a < b
		}
		return out[i].text() < out[j].text()
	})
	return out
}

func collapseByCID(rows []Row) []Row {
	seen := map[string]bool{}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		c := r.Get("cid")
		if c != "" {
			if seen[c] {
				continue
			}
			seen[c] = true
		}
		out = append(out, r)
	}
	return out
}

// CompareShape compares format 1's and format 4's answers for one shape.
func CompareShape(f1, s *ShapeAnswers, oracle CopyOracle) Verdict {
	v := Verdict{}
	switch {
	case f1 == nil && s == nil:
		v.Status = EqMissing
		return v
	case f1 == nil:
		v.Class, v.Shape, v.Status = s.Class, s.Shape, EqMissing
		v.Notes = append(v.Notes, "format 1 did not run this shape")
		return v
	case s == nil:
		v.Class, v.Shape, v.Status = f1.Class, f1.Shape, EqMissing
		v.Notes = append(v.Notes, "format 4 did not run this shape")
		return v
	}
	v.Class, v.Shape, v.Accepted = f1.Class, f1.Shape, f1.Policy.Accepted
	pol := f1.Policy
	sCalls := map[string]Answer{}
	for _, a := range s.Calls {
		sCalls[a.Call] = a
	}
	worst := EqEqual
	rank := map[string]int{EqEqual: 0, EqEqualC12: 1, EqExtra: 2, EqBothError: 3, EqF1Error: 4, EqDiffer: 5, EqSError: 6, EqMissing: 7}
	bump := func(st string) {
		if rank[st] > rank[worst] {
			worst = st
		}
	}
	extra := map[string]bool{}
	variantCache := map[string][]Row{}
	for _, a := range f1.Calls {
		v.Calls++
		b, ok := sCalls[a.Call]
		if !ok {
			bump(EqMissing)
			v.Notes = append(v.Notes, "format 4 has no answer for call "+a.Call)
			continue
		}
		switch {
		case a.Err != "" && b.Err != "":
			bump(EqBothError)
			continue
		case a.Err != "":
			bump(EqF1Error)
			v.Notes = append(v.Notes, fmt.Sprintf("%s: format 1 error: %s", a.Call, a.Err))
			continue
		case b.Err != "":
			bump(EqSError)
			v.Notes = append(v.Notes, fmt.Sprintf("%s: format 4 error: %s", a.Call, b.Err))
			continue
		}
		ra, rb := a.Rows, b.Rows
		if pol.Collapse {
			if c := collapseByCID(ra); len(c) != len(ra) {
				v.Notes = append(v.Notes, fmt.Sprintf("%s: C-10 collapsed format 1's %d rows to %d", a.Call, len(ra), len(c)))
				ra = c
			}
		}
		if pol.Unordered {
			ra, rb = sortRows(ra), sortRows(rb)
		}
		v.F1Rows += len(ra)
		v.SRows += len(rb)
		if len(ra) != len(rb) {
			bump(EqDiffer)
			v.Diffs = appendDiff(v.Diffs, Diff{Call: a.Call, Row: -1, Field: "rows", F1: fmt.Sprint(len(ra)), S: fmt.Sprint(len(rb))})
			continue
		}
		orderOnly := !pol.Unordered
		callDiffers := false
		for i := range ra {
			st, diffs, ex := compareRow(ra[i], rb[i])
			for _, f := range ex {
				extra[f] = true
			}
			switch st {
			case EqEqual:
			case EqExtra:
				bump(EqExtra)
			case EqEqualC12:
				if variantMatchesCopy(rb[i], f1.Schema, oracle, variantCache) {
					bump(EqEqualC12)
				} else {
					bump(EqDiffer)
					callDiffers = true
					for _, d := range diffs {
						d.Call, d.Row = a.Call, i
						v.Diffs = appendDiff(v.Diffs, d)
					}
				}
			default:
				bump(EqDiffer)
				callDiffers = true
				for _, d := range diffs {
					d.Call, d.Row = a.Call, i
					v.Diffs = appendDiff(v.Diffs, d)
				}
			}
		}
		if callDiffers && orderOnly && sameMultiset(ra, rb) {
			v.Notes = append(v.Notes, a.Call+": the rows are equal as a set; only their order differs")
		}
	}
	if len(s.Unstable) > 0 || len(f1.Unstable) > 0 {
		v.Notes = append(v.Notes, fmt.Sprintf("answers changed between passes: f1 %v, s %v", f1.Unstable, s.Unstable))
		if len(s.Unstable) > 0 && worst == EqEqual {
			worst = EqUnstable
		}
	}
	for f := range extra {
		v.Extra = append(v.Extra, f)
	}
	sort.Strings(v.Extra)
	v.Status = worst
	if pol.Accepted != "" && !v.Passed() {
		v.Status = EqAccepted
	}
	return v
}

func appendDiff(d []Diff, x Diff) []Diff {
	if len(d) >= maxDiffsShown {
		return d
	}
	return append(d, x)
}

// compareRow: EqEqual; EqExtra (S fills fields format 1 leaves empty);
// EqEqualC12 (only copy variants differ; the caller checks the copies); or
// EqDiffer with the differing fields.
func compareRow(a, b Row) (string, []Diff, []string) {
	bv := map[string]string{}
	for _, f := range b {
		bv[f.N] = f.V
	}
	status := EqEqual
	var diffs []Diff
	var extra []string
	seen := map[string]bool{}
	for _, f := range a {
		seen[f.N] = true
		y, ok := bv[f.N]
		if ok && y == f.V {
			continue
		}
		switch {
		case ok && optionalFields[f.N] && isEmptyValue(f.V):
			extra = append(extra, f.N)
			if status == EqEqual {
				status = EqExtra
			}
		case isVariant(f.N):
			if status != EqDiffer {
				status = EqEqualC12
			}
			diffs = append(diffs, Diff{Field: f.N, F1: f.V, S: y})
		default:
			status = EqDiffer
			diffs = append(diffs, Diff{Field: f.N, F1: f.V, S: y})
		}
	}
	for _, f := range b {
		if !seen[f.N] && optionalFields[f.N] && !isEmptyValue(f.V) {
			extra = append(extra, f.N)
			if status == EqEqual {
				status = EqExtra
			}
		}
	}
	if status == EqEqualC12 {
		// Variant diffs only: return them so the caller can report them if
		// no copy of format 1 matches.
		return status, diffs, extra
	}
	if status == EqDiffer {
		var strict []Diff
		for _, d := range diffs {
			if !isVariant(d.Field) {
				strict = append(strict, d)
			}
		}
		return status, strict, extra
	}
	return status, nil, extra
}

func isEmptyValue(v string) bool { return v == "" || v == "0" || v == "false" }

// optionalFields are the record fields some format-1 storage functions leave
// unset (GetRecord fills neither the cursor nor the tags). Format 4 filling
// one of them where format 1 left it empty is reported as an extra field, not
// as a difference; every other field, and every scalar answer, must be equal.
var optionalFields = map[string]bool{
	"rowid": true, "rlen": true, "materialized": true,
	"provider": true, "source": true, "url": true, "batch": true, "ckey": true, "ppeer": true, "ppk": true,
	"license": true, "license_url": true, "citation": true, "share_alike": true,
	"~sig": true,
}

// variantMatchesCopy reports whether row's copy variant equals one of format
// 1's copies of its CID.
func variantMatchesCopy(row Row, schema string, oracle CopyOracle, cache map[string][]Row) bool {
	cid := row.Get("cid")
	if oracle == nil || cid == "" {
		return false
	}
	copies, ok := cache[cid]
	if !ok {
		var err error
		copies, err = oracle(schema, cid)
		if err != nil {
			copies = nil
		}
		cache[cid] = copies
	}
	for _, c := range copies {
		match := true
		for _, f := range row {
			if !isVariant(f.N) {
				continue
			}
			if c.Get(f.N) != f.V {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func sameMultiset(a, b []Row) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, r := range a {
		m[strictText(r)]++
	}
	for _, r := range b {
		k := strictText(r)
		if m[k] == 0 {
			return false
		}
		m[k]--
	}
	return true
}
