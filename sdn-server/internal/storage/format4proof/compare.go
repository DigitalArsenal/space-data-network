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
	// CollapseSkip exempts the calls whose name contains it from Collapse
	// (a coverage shape's refs reads: a repeated ref returns its record once
	// per ref, on every format).
	CollapseSkip string `json:"collapse_skip,omitempty"`
	// Accepted names a documented difference (a contract clause or an owner
	// decision). A shape that differs under it is reported as accepted, never
	// as equal.
	Accepted string `json:"accepted,omitempty"`
	// AcceptedFields limits Accepted to these fields: a difference in any
	// other field is still DIFFER. Empty = every field.
	AcceptedFields []string `json:"accepted_fields,omitempty"`
	// Superset (contract C-31, `<TYPE>@<source>`): format 1 answers the
	// type's newest N records that carry the source, format 4 the source's
	// newest N records. The first set always lies inside the second, so
	// every row of format 1's must be among the candidate's, which may hold
	// more, up to MaxRows (N). More rows is the accepted difference; a
	// missing row or a row past N is DIFFER.
	Superset bool `json:"superset,omitempty"`
	MaxRows  int  `json:"max_rows,omitempty"`
	// Strict (the coverage classes, coverage.go): a field the candidate
	// fills where format 1 leaves it empty is a difference, not an extra
	// field (optionalFields does not apply). Every field of every row must be
	// identical, the copy variants aside (C-12).
	Strict bool `json:"strict,omitempty"`
	// Calls are documented differences of single calls (a contract row or a
	// ruling that covers that call only): a difference in one of a ruling's
	// fields, in a call whose name contains the ruling's Call, is accepted.
	Calls []CallRuling `json:"calls,omitempty"`
	// TieOrdered names calls whose format-1 order is by one field only (ORDER
	// BY timestamp DESC, no tiebreak): rows tied on it may come in any order,
	// and a limit that cuts a tie may keep any of its members (alignTies).
	TieOrdered []TieRule `json:"tie_ordered,omitempty"`
	// CIDAliases names records format 1 holds under another text of their
	// CID (contract §3.8 (1): format 4 keeps only the CIDv1 raw sha2-256 of
	// the bytes; format 1 keeps an imported index's sha256-hex identity):
	// format 1's cid values are read as their alias before comparing.
	CIDAliases map[string]string `json:"cid_aliases,omitempty"`
	// Surface names the call that answers the public SQL surface listing
	// (PublicQuerySurface): it compares relation by relation (surfaceDiff).
	Surface string `json:"surface,omitempty"`
}

// TieRule is a partial order: the calls whose name contains Call are
// ordered by Key alone. Limit is the call's row limit as format 1 applies
// it (0: none): only an answer of exactly Limit rows can have its last tie
// cut.
type TieRule struct {
	Call  string `json:"call"`
	Key   string `json:"key"`
	Limit int    `json:"limit,omitempty"`
	// Schema is the call's standard when it is not the shape's (the copy
	// oracle's lookup).
	Schema string `json:"schema,omitempty"`
}

func (p Policy) tieRule(call string) *TieRule {
	for i := range p.TieOrdered {
		if strings.Contains(call, p.TieOrdered[i].Call) {
			return &p.TieOrdered[i]
		}
	}
	return nil
}

// tieCut names what alignTies accepts beyond format 1's own rows (C-39 E7:
// format 1's choice among rows tied on a timestamp is arbitrary).
const tieCut = "C-39 E7: format 1 orders by the timestamp alone: a limit cutting a tie keeps any of its members (each one checked against format 1's copies: CID, peer, timestamp)"

// alignTies puts the candidate's rows in format 1's order where format 1's
// order leaves them free: the key of every position must be equal on both
// sides (the defined part of the order); within each run of one key, the
// candidate's rows match format 1's as a multiset; when the answer holds
// exactly limit rows, the last run may be cut by the limit, and a row of it
// format 1 did not return is taken when member(row) proves it a member of
// the tie (a copy format 1 holds with that key). A key
// that is empty, or "now" (the store's clock before the class's first write,
// which may conflate different seconds), is no tie: those rows keep their
// positions. "now@<j>" (stamped during write j) is a key like any other. It returns the rows in format 1's
// order and the number of rows taken by member; ok is false when a key
// differs.
func alignTies(ra, rb []Row, key string, limit int, member func(Row) bool) (out []Row, cut int, ok bool) {
	if len(ra) != len(rb) {
		return rb, 0, false
	}
	for i := range ra {
		if ra[i].Get(key) != rb[i].Get(key) {
			return rb, 0, false
		}
	}
	out = append([]Row(nil), rb...)
	for start := 0; start < len(ra); {
		k := ra[start].Get(key)
		end := start + 1
		for end < len(ra) && ra[end].Get(key) == k {
			end++
		}
		if k == "" || k == "now" || end-start < 2 {
			start = end
			continue
		}
		free := map[string][]int{} // format 1's rows of the run by content
		for i := start; i < end; i++ {
			t := strictText(ra[i])
			free[t] = append(free[t], i)
		}
		placed := make([]bool, end-start)
		var rest []Row
		for i := start; i < end; i++ {
			t := strictText(rb[i])
			if idx := free[t]; len(idx) > 0 {
				out[idx[0]], placed[idx[0]-start] = rb[i], true
				free[t] = idx[1:]
				continue
			}
			rest = append(rest, rb[i])
		}
		for _, r := range rest {
			if end != len(ra) || limit <= 0 || len(ra) != limit || !member(r) {
				return rb, 0, true // compared position by position
			}
			for j := range placed {
				if !placed[j] {
					// Format 1's row there is another member of the cut tie.
					out[start+j], placed[j] = ra[start+j], true
					cut++
					break
				}
			}
		}
		start = end
	}
	return out, cut, true
}

// schemaOfCall is the standard a call's name names (its first "<TYPE>.fbs"
// word), else def.
func schemaOfCall(call, def string) string {
	for _, w := range strings.Fields(call) {
		if strings.HasSuffix(w, ".fbs") {
			return w
		}
	}
	return def
}

// tieMember reports a row a copy format 1 holds: a record row's copy
// variant (~peer, ~ts, ~sig), or a routed row's peer and ts in its standard.
func tieMember(row Row, schema string, oracle CopyOracle, cache map[string][]Row) bool {
	if std, ok := row.lookup("standard"); ok {
		if oracle == nil {
			return false
		}
		copies, err := oracle(std+".fbs", row.Get("cid"))
		if err != nil {
			return false
		}
		for _, c := range copies {
			if c.Get("~peer") == row.Get("peer") && c.Get("~ts") == row.Get("ts") {
				return true
			}
		}
		return false
	}
	return variantMatchesCopy(row, schema, oracle, cache)
}

// aliased is rows with every cid value that names an alias replaced by it.
func aliased(rows []Row, aliases map[string]string) []Row {
	if len(aliases) == 0 {
		return rows
	}
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = r
		copied := false
		for j, f := range r {
			a, ok := aliases[f.V]
			if !ok || f.N != "cid" {
				continue
			}
			if !copied {
				out[i], copied = append(Row(nil), r...), true
			}
			out[i][j].V = a
		}
	}
	return out
}

// CallRuling is an intended difference of the calls whose name contains
// Call, on Fields (empty = every field, the row count included).
type CallRuling struct {
	Call   string   `json:"call"`
	Why    string   `json:"why"`
	Fields []string `json:"fields,omitempty"`
	// Standard limits the ruling to the rows (format 1's) that name this
	// standard or none (a store total): rowStandard.
	Standard string `json:"standard,omitempty"`
	// SameSet accepts the fields only when the call's rows are equal as a
	// multiset: their order alone differs.
	SameSet bool `json:"same_set,omitempty"`
	// Absent names a standard format 4 holds nothing of: format 1's rows
	// that name it are not compared (they are the difference); a row of it
	// on format 4 is compared as any row (and differs).
	Absent string `json:"absent,omitempty"`
}

// ruling is the call ruling that accepts field of row (format 1's; nil for
// the row count) in call, or nil. setEq reports the call's rows equal as a
// multiset (CallRuling.SameSet).
func (p Policy) ruling(call, field string, row Row, setEq bool) *CallRuling {
	for i := range p.Calls {
		r := &p.Calls[i]
		if !strings.Contains(call, r.Call) || r.Absent != "" || (r.SameSet && !setEq) {
			continue
		}
		if r.Standard != "" {
			if row == nil {
				continue
			}
			if std := rowStandard(row); std != "" && std != r.Standard {
				continue
			}
		}
		if len(r.Fields) == 0 {
			return r
		}
		for _, f := range r.Fields {
			if f == field {
				return r
			}
		}
	}
	return nil
}

// rowStandard is the standard a summary row names (schema, SchemaName or
// source_row; JSON-quoted or bare), or "".
func rowStandard(r Row) string {
	for _, k := range []string{"schema", "SchemaName", "source_row"} {
		if v, ok := r.lookup(k); ok {
			if v = strings.Trim(v, `"`); v != "" {
				return v
			}
		}
	}
	return ""
}

// absentRows drops format 1's rows that name a standard an Absent ruling of
// call names; it returns the rows kept and the rulings that dropped any.
func (p Policy) absentRows(call string, rows []Row) ([]Row, []string) {
	var why []string
	for _, r := range p.Calls {
		if r.Absent == "" || !strings.Contains(call, r.Call) {
			continue
		}
		kept := rows[:0:0]
		for _, row := range rows {
			if rowStandard(row) != r.Absent {
				kept = append(kept, row)
			}
		}
		if len(kept) != len(rows) {
			why = append(why, r.Why)
		}
		rows = kept
	}
	return rows, why
}

// surfaceDiff compares the public SQL surface listing (one row per
// relation: name, kind, source, columns, placeholders, records) relation by
// relation. Every relation format 4 lists must be format 1's, with the same
// source, columns and placeholder columns, and a type relation's record
// count must be equal. The intended differences (C-39 S1 listing and S2;
// C-38 (5)) are a type relation's kind (format 1 lists every routed
// standard as a view once the node knows a source), the "<TYPE>@<source>"
// relations format 4 has no feed table for, CLM (no relation: no embedded
// binary schema) and a "<TYPE>@<source>" relation's record count (format 4
// counts the feed's records; format 1 its resident rows, one per record in
// its first source's partition).
func surfaceDiff(call string, ra, rb []Row) (diffs []Diff, accepted map[string]bool, unaccepted bool) {
	accepted = map[string]bool{}
	byName := map[string]Row{}
	for _, r := range ra {
		byName[r.Get("name")] = r
	}
	seen := map[string]bool{}
	for i, b := range rb {
		name := b.Get("name")
		seen[name] = true
		a, ok := byName[name]
		if !ok {
			unaccepted = true
			diffs = append(diffs, Diff{Call: call, Row: i, Field: "name", S: name})
			continue
		}
		rel := strings.Contains(name, "@")
		for _, f := range a {
			bv := b.Get(f.N)
			if bv == f.V {
				continue
			}
			d := Diff{Call: call, Row: i, Field: name + " " + f.N, F1: f.V, S: bv}
			diffs = append(diffs, d)
			switch {
			case f.N == "kind" && !rel:
				accepted[c39S1Listing] = true
			case f.N == "records" && rel:
				accepted[c38PerFeedSurface] = true
			default:
				unaccepted = true
			}
		}
	}
	for i, a := range ra {
		name := a.Get("name")
		if seen[name] {
			continue
		}
		diffs = append(diffs, Diff{Call: call, Row: i, Field: "name", F1: name})
		switch {
		case name == "CLM" || strings.HasPrefix(name, "CLM@"):
			accepted[c39S2] = true
		case strings.Contains(name, "@"):
			accepted[c39S1Listing] = true
		default:
			unaccepted = true
		}
	}
	return diffs, accepted, unaccepted
}

// The C-39 rulings (contract v17, coordinator 2026-10-03) the comparator
// accepts, and C-38 (5) as the surface listing shows it. C-39's must-fix
// items (B1, E1-E6, S1's empty relation, U1's migration refusal) are never
// accepted.
const (
	c39E7 = "C-39 E7: format 1 orders QueryAll/QueryAllBounded by the source timestamp alone; its choice among records tied on it is arbitrary"
	// c39S1Listing and c39S2: surfaceDiff.
	c39S1Listing = "C-39 S1 listing: the SQL surface lists only a type's real feed tables; format 1 lists every routed standard as a view and a relation for every source the node knows"
	c39S2        = "C-39 S2: CLM, a published-binding standard with no embedded binary schema, has no SQL relation"
	c39U1        = "C-39 U1: format 4 stores SDS standards only; a record of a schema with no standard is refused (format 1 stored it)"
	c39U2        = "C-39 U2: UpsertSourceTags of a CID not held answers not found; format 1 wrote a dangling tag and counted it in its summaries"
	c39U3        = "C-39 U3: the summary-maintenance verbs are no-ops on format 4; format 1 restamped the lane times with its clock"
	c39U4        = "C-39 U4: the table cursor is the type's seq; format 1 compared a producer table's own rowid (as the R10 ruling)"
	c39U5        = "C-39 U5: lane bytes are the exact stored length; format 1 counted a retagged sealed record's plaintext length"
	c39U6        = "C-39 U6: lane counters are the records the feed holds; format 1's incremental counters drifted after CAT supersede-on-ingest"
	// c38PerFeedSurface is C-38 (5) on the surface listing.
	c38PerFeedSurface = "C-38 (5): a \"<TYPE>@<source>\" relation counts the feed's records (a record in N feeds counts in each); format 1 counts its resident rows, one per record in its first source's partition"
)

// laneField is a head field's name under C-10: a tag-filtered count or head
// (a row carrying lane_n or lane_max_rowid, covHead) sums its bytes over the
// rows format 1 repeats per matching tag, so its bytes compare as lane_bytes.
func laneField(row Row, field string) string {
	if field != "bytes" {
		return field
	}
	for _, f := range row {
		if strings.HasPrefix(f.N, "lane_") {
			return "lane_bytes"
		}
	}
	return field
}

func (p Policy) accepts(field string) bool {
	if len(p.AcceptedFields) == 0 {
		return true
	}
	for _, f := range p.AcceptedFields {
		if f == field {
			return true
		}
	}
	return false
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
	// Prov counts the provenance cells (provider, source, batch) of the
	// rows compared: format 4's must never be blank where format 1's are not
	// (C-37: they come from the feed file and the row's batch).
	Prov Provenance `json:"provenance"`
}

// Provenance counts provenance cells: Cells compared, blank on format 1,
// blank on the candidate, and blank on the candidate where format 1 has a
// value (must be 0).
type Provenance struct {
	Cells, F1Blank, SBlank, SBlankF1Set int
}

func (p *Provenance) add(o Provenance) {
	p.Cells += o.Cells
	p.F1Blank += o.F1Blank
	p.SBlank += o.SBlank
	p.SBlankF1Set += o.SBlankF1Set
}

// provFields are the provenance fields of a record row.
var provFields = []string{"provider", "source", "batch"}

// countRow counts one row pair's provenance cells: the same record's rows
// (a pair whose CIDs differ is another difference, not a blank cell; an
// error row is no record).
func (p *Provenance) countRow(a, b Row) {
	if c := a.Get("cid"); c != "" && c != b.Get("cid") {
		return
	}
	if _, isErr := b.lookup("err"); isErr {
		return
	}
	if _, isErr := a.lookup("err"); isErr {
		return
	}
	for _, f := range provFields {
		av, ok := a.lookup(f)
		if !ok {
			continue
		}
		bv := b.Get(f)
		p.Cells++
		if av == "" {
			p.F1Blank++
		}
		if bv == "" {
			p.SBlank++
			if av != "" {
				p.SBlankF1Set++
			}
		}
	}
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
	unaccepted := false // a difference outside the policy's accepted fields
	rulings := map[string]bool{}
	// forced: an accepted difference that leaves no differing field (a cut
	// tie compared other members' rows; format 1's rows of an absent
	// standard dropped): never reported as equal.
	forced := false
	accepts := func(call, field string, row Row, setEq bool) bool {
		if pol.accepts(field) && pol.Accepted != "" {
			return true
		}
		if r := pol.ruling(call, field, row, setEq); r != nil {
			rulings[r.Why] = true
			return true
		}
		return false
	}
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
		ra, rb := aliased(a.Rows, pol.CIDAliases), b.Rows
		if kept, why := pol.absentRows(a.Call, ra); len(why) > 0 {
			v.Notes = append(v.Notes, fmt.Sprintf("%s: %d of format 1's rows name a standard format 4 holds nothing of", a.Call, len(ra)-len(kept)))
			ra, forced = kept, true
			for _, w := range why {
				rulings[w] = true
			}
		}
		if pol.Surface != "" && strings.Contains(a.Call, pol.Surface) {
			v.F1Rows += len(ra)
			v.SRows += len(rb)
			diffs, why, bad := surfaceDiff(a.Call, ra, rb)
			if len(diffs) > 0 {
				bump(EqDiffer)
				unaccepted = unaccepted || bad
				for w := range why {
					rulings[w] = true
				}
				for _, d := range diffs {
					v.Diffs = appendDiff(v.Diffs, d)
				}
			}
			continue
		}
		// repeated: format 1's rows of each CID it repeated (C-10). The
		// record appears once on format 4, with one of those rows' tags.
		var repeated map[string][]Row
		if pol.Collapse && (pol.CollapseSkip == "" || !strings.Contains(a.Call, pol.CollapseSkip)) {
			if c := collapseByCID(ra); len(c) != len(ra) {
				v.Notes = append(v.Notes, fmt.Sprintf("%s: C-10 collapsed format 1's %d rows to %d", a.Call, len(ra), len(c)))
				repeated = rowsByCID(ra)
				ra = c
			}
		}
		if pol.Unordered {
			ra, rb = sortRows(ra), sortRows(rb)
		}
		if tr := pol.tieRule(a.Call); tr != nil {
			schema := tr.Schema
			if schema == "" {
				schema = schemaOfCall(a.Call, f1.Schema)
			}
			if aligned, cut, ok := alignTies(ra, rb, tr.Key, tr.Limit, func(r Row) bool {
				return tieMember(r, schema, shapeOracle(f1, oracle), map[string][]Row{})
			}); ok {
				rb = aligned
				if cut > 0 {
					rulings[tieCut] = true
					v.Notes = append(v.Notes, fmt.Sprintf("%s: %d rows are other members of a tie the limit cut (checked against format 1's copies)", a.Call, cut))
				}
			}
		}
		v.F1Rows += len(ra)
		v.SRows += len(rb)
		if pol.Superset {
			if d, ok := supersetDiff(a.Call, ra, rb, pol.MaxRows); !ok {
				bump(EqDiffer)
				unaccepted = true
				v.Diffs = appendDiff(v.Diffs, d)
			} else if len(rb) > len(ra) {
				bump(EqDiffer) // accepted below
				v.Notes = append(v.Notes, fmt.Sprintf("%s: C-31: %d rows, format 1's %d all among them", a.Call, len(rb), len(ra)))
			}
			continue
		}
		if len(ra) != len(rb) {
			bump(EqDiffer)
			unaccepted = unaccepted || !accepts(a.Call, "rows", nil, false)
			v.Diffs = appendDiff(v.Diffs, Diff{Call: a.Call, Row: -1, Field: "rows", F1: fmt.Sprint(len(ra)), S: fmt.Sprint(len(rb))})
			continue
		}
		orderOnly := !pol.Unordered
		setEq := sameMultiset(ra, rb)
		callDiffers := false
		for i := range ra {
			v.Prov.countRow(ra[i], rb[i])
			st, diffs, ex := compareRow(ra[i], rb[i], pol.Strict)
			if st == EqDiffer {
				if alt, ok := matchRepeated(repeated[ra[i].Get("cid")], rb[i], pol.Strict); ok {
					st, diffs, ex = alt, nil, nil
				}
			}
			for _, f := range ex {
				extra[f] = true
			}
			switch st {
			case EqEqual:
			case EqExtra:
				bump(EqExtra)
			case EqEqualC12:
				if variantMatchesCopy(rb[i], f1.Schema, shapeOracle(f1, oracle), variantCache) {
					bump(EqEqualC12)
				} else {
					bump(EqDiffer)
					callDiffers = true
					for _, d := range diffs {
						d.Call, d.Row = a.Call, i
						unaccepted = unaccepted || !accepts(a.Call, laneField(ra[i], d.Field), ra[i], setEq)
						v.Diffs = appendDiff(v.Diffs, d)
					}
				}
			default:
				bump(EqDiffer)
				callDiffers = true
				for _, d := range diffs {
					d.Call, d.Row = a.Call, i
					unaccepted = unaccepted || !accepts(a.Call, laneField(ra[i], d.Field), ra[i], setEq)
					v.Diffs = appendDiff(v.Diffs, d)
				}
			}
		}
		if callDiffers && orderOnly && setEq {
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
	if (forced || rulings[tieCut]) && (worst == EqEqual || worst == EqEqualC12 || worst == EqExtra) {
		// A cut tie compared other members' rows, or format 1's rows of an
		// absent standard were dropped: an intended difference, reported as
		// such rather than as equal.
		worst, v.Status = EqDiffer, EqDiffer
	}
	if worst == EqDiffer && !unaccepted && (pol.Accepted != "" || len(rulings) > 0) {
		v.Status = EqAccepted
		for why := range rulings {
			if !strings.Contains(v.Accepted, why) {
				if v.Accepted != "" {
					v.Accepted += "; "
				}
				v.Accepted += why
			}
		}
	}
	return v
}

// rowsByCID groups rows by their CID.
func rowsByCID(rows []Row) map[string][]Row {
	out := map[string][]Row{}
	for _, r := range rows {
		if c := r.Get("cid"); c != "" {
			out[c] = append(out[c], r)
		}
	}
	return out
}

// matchRepeated reports a candidate row equal to one of format 1's repeated
// rows of its CID (C-10: the record once, with one of its matching tags).
func matchRepeated(alts []Row, row Row, strict bool) (string, bool) {
	if len(alts) < 2 {
		return "", false
	}
	for _, alt := range alts {
		if st, _, _ := compareRow(alt, row, strict); st == EqEqual || st == EqExtra {
			return st, true
		}
	}
	return "", false
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
func compareRow(a, b Row, strict bool) (string, []Diff, []string) {
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
		case ok && !strict && optionalFields[f.N] && isEmptyValue(f.V):
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
		if !seen[f.N] && (strict || optionalFields[f.N]) && !isEmptyValue(f.V) {
			if strict {
				status = EqDiffer
				diffs = append(diffs, Diff{Field: f.N, S: f.V})
				continue
			}
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
		var kept []Diff
		for _, d := range diffs {
			if !isVariant(d.Field) {
				kept = append(kept, d)
			}
		}
		return status, kept, extra
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

// shapeOracle is the copy oracle a shape's variants are checked against: the
// copies format 1 held when the shape ran (ShapeAnswers.Copies, recorded by a
// coverage class after its writes), else the fixture's.
func shapeOracle(f1 *ShapeAnswers, fixture CopyOracle) CopyOracle {
	if len(f1.Copies) == 0 {
		return fixture
	}
	return func(schema, cid string) ([]Row, error) {
		if rows, ok := f1.Copies[cid]; ok {
			return rows, nil
		}
		if fixture == nil {
			return nil, nil
		}
		return fixture(schema, cid)
	}
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

// supersetDiff checks Policy.Superset: every row of a (format 1) is in b,
// and b has at most max rows (0 = no bound).
func supersetDiff(call string, a, b []Row, max int) (Diff, bool) {
	if max > 0 && len(b) > max {
		return Diff{Call: call, Row: -1, Field: "rows", F1: fmt.Sprint(len(a)), S: fmt.Sprintf("%d, above the bound %d", len(b), max)}, false
	}
	have := map[string]int{}
	for _, r := range b {
		have[strictText(r)]++
	}
	for i, r := range a {
		k := strictText(r)
		if have[k] == 0 {
			return Diff{Call: call, Row: i, Field: "row", F1: r.text(), S: "absent"}, false
		}
		have[k]--
	}
	return Diff{}, true
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
