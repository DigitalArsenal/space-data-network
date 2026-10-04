package format4proof

import (
	"fmt"
	"sort"
	"strconv"
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
	// Source limits it likewise to the rows that name this source or none
	// (rowSource), and Batch to the rows of this batch or none (rowBatch);
	// with Absent, Batch narrows the rows dropped to that batch's.
	Source string `json:"source,omitempty"`
	Batch  string `json:"batch,omitempty"`
	// SameSet accepts the fields only when the call's rows are equal as a
	// multiset: their order alone differs.
	SameSet bool `json:"same_set,omitempty"`
	// CID limits the ruling to the rows (format 1's) of this record.
	CID string `json:"cid,omitempty"`
	// Absent names a standard format 4 holds nothing of: format 1's rows
	// that name it are not compared (they are the difference); a row of it
	// on format 4 is compared as any row (and differs). AbsentCID names a
	// record likewise (C-6: the record the migration sets aside).
	Absent    string `json:"absent,omitempty"`
	AbsentCID string `json:"absent_cid,omitempty"`
	// TaggedFirst (a record listing; C-41 N3): format 1 lists its tagged
	// records before its untagged ones, the candidate every record by seq
	// (taggedFirstOrder). Cut is the call's limit: when both pages are cut
	// at it, format 1's order also changes which records its cut keeps
	// (C-43 G3, taggedFirstCut).
	TaggedFirst bool `json:"tagged_first,omitempty"`
	Cut         int  `json:"cut,omitempty"`
	// PerFeed (a record listing; C-38 (5), C-41 N8): the candidate lists a
	// record once per feed that holds it, format 1 once (perFeedRows).
	PerFeed bool `json:"per_feed,omitempty"`
	// SameLane (a lane's record listing; C-42): format 1 orders the page by
	// the timestamp of the copy it serves, an arbitrary one (C-12), so the
	// records a limit keeps may differ (sameLanePage).
	SameLane *LaneKey `json:"same_lane,omitempty"`
	// F1Empty (C-43 G4): format 1 answers the call with no rows, a format-1
	// artifact; the ruling applies only then (a format-1 answer with rows is
	// compared as any).
	F1Empty bool `json:"f1_empty,omitempty"`
	// CaseTwin (C-43 B3 residual): the call reads a "<TYPE>@<source>"
	// relation whose source equals another feed's but for case, and format
	// 1's answer is its own arbitrary choice between the twins. The
	// candidate's answer must be Want (the named feed's own) when Want is
	// set, else empty, a zero count or an error (twins with no exact
	// spelling): never another feed's records (caseTwinAnswer).
	CaseTwin bool  `json:"case_twin,omitempty"`
	Want     []Row `json:"want,omitempty"`
	// S4Only (C-44 G5): the candidate's answer also holds this one record,
	// which format 1's lacks; the candidate's rows lose it before comparing
	// (s4OnlyRows), and the rest must be equal.
	S4Only *OnlyRecord `json:"s4_only,omitempty"`
}

// OnlyRecord is one record as a call's rows show it: Rows are its rows
// (its frame in a stream, its object in an epoch stream's object list) and
// Counts the count fields that count it (a relation's COUNT(*), an object
// list's frames).
type OnlyRecord struct {
	CID    string   `json:"cid"`
	Rows   []Row    `json:"rows"`
	Counts []string `json:"counts"`
}

// LaneKey names a lane: the provider, source and batch its records carry.
// Members are the lane's records (CIDs) where the rows carry no provenance
// on either format (C-43 G1); nil when not known (never serialized: the
// policy is rebuilt from the inputs).
type LaneKey struct {
	Provider, Source, Batch string
	Members                 map[string]bool `json:"-"`
}

// rowRuling reports a ruling that rewrites the rows compared (Absent,
// AbsentCID, TaggedFirst, PerFeed, SameLane, S4Only) or decides a whole call
// (F1Empty, CaseTwin) rather than accepting fields.
func (r *CallRuling) rowRuling() bool {
	return r.Absent != "" || r.AbsentCID != "" || r.TaggedFirst || r.PerFeed || r.SameLane != nil || r.F1Empty || r.CaseTwin || r.S4Only != nil
}

// ruling is the call ruling that accepts field of row (format 1's; nil for
// the row count) in call, or nil. setEq reports the call's rows equal as a
// multiset (CallRuling.SameSet).
func (p Policy) ruling(call, field string, row Row, setEq bool) *CallRuling {
	for i := range p.Calls {
		r := &p.Calls[i]
		if !strings.Contains(call, r.Call) || r.rowRuling() || (r.SameSet && !setEq) {
			continue
		}
		if r.Standard != "" || r.Source != "" || r.Batch != "" || r.CID != "" {
			if row == nil || (r.CID != "" && row.Get("cid") != r.CID) {
				continue
			}
			if std := rowStandard(row); r.Standard != "" && std != "" && std != r.Standard {
				continue
			}
			if src := rowSource(row); r.Source != "" && src != "" && src != r.Source {
				continue
			}
			if b := rowBatch(row); r.Batch != "" && b != "" && b != r.Batch {
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

// rowSource is the source a summary row names (SourceName, JSON-quoted or
// bare), or "".
func rowSource(r Row) string {
	v, _ := r.lookup("SourceName")
	return strings.Trim(v, `"`)
}

// rowBatch is the batch a summary row names (BatchID, JSON-quoted or bare),
// or "".
func rowBatch(r Row) string {
	v, _ := r.lookup("BatchID")
	return strings.Trim(v, `"`)
}

// absentRows drops format 1's rows that name a standard an Absent ruling of
// call names, or the record an AbsentCID ruling names; it returns the rows
// kept and the rulings that dropped any.
func (p Policy) absentRows(call string, rows []Row) ([]Row, []string) {
	var why []string
	for _, r := range p.Calls {
		if (r.Absent == "" && r.AbsentCID == "") || !strings.Contains(call, r.Call) {
			continue
		}
		kept := rows[:0:0]
		for _, row := range rows {
			absent := r.Absent != "" && rowStandard(row) == r.Absent && (r.Batch == "" || rowBatch(row) == r.Batch)
			if !absent && (r.AbsentCID == "" || row.Get("cid") != r.AbsentCID) {
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

// s4OnlyRuling is call's S4Only ruling (nil: none).
func (p Policy) s4OnlyRuling(call string) *CallRuling {
	for i := range p.Calls {
		if r := &p.Calls[i]; r.S4Only != nil && strings.Contains(call, r.Call) {
			return r
		}
	}
	return nil
}

// s4OnlyRows is the candidate's rows rb without the record x, which format
// 1's rows ra lack: each of x's rows that rb holds once and ra not at all
// is dropped, and each count row of x (a row of one field, named in
// x.Counts) is taken one less where it is ra's count plus one. ok is false,
// and rb comes back unchanged, unless the call shows x and shows it on the
// candidate's side alone: a row of x on format 1, or twice on format 4, or a
// count other than format 1's plus one, rules it out.
func s4OnlyRows(ra, rb []Row, x *OnlyRecord) ([]Row, bool) {
	holds := func(rows []Row, text string) (n, at int) {
		at = -1
		for i, r := range rows {
			if strictText(r) == text {
				if n++; at < 0 {
					at = i
				}
			}
		}
		return n, at
	}
	countRow := func(rows []Row, field string) int {
		for i, r := range rows {
			if len(r) == 1 && r[0].N == field {
				return i
			}
		}
		return -1
	}
	out := append([]Row(nil), rb...)
	shown := false
	for _, row := range x.Rows {
		t := strictText(row)
		nb, at := holds(out, t)
		na, _ := holds(ra, t)
		switch {
		case nb == 0 && na == 0:
			continue // the call does not list this form of the record
		case nb != 1 || na != 0:
			return rb, false
		}
		out = append(out[:at:at], out[at+1:]...)
		shown = true
	}
	for _, field := range x.Counts {
		ib, ia := countRow(out, field), countRow(ra, field)
		if ib < 0 && ia < 0 {
			continue
		}
		if ib < 0 || ia < 0 {
			return rb, false
		}
		nb, errB := strconv.Atoi(out[ib][0].V)
		na, errA := strconv.Atoi(ra[ia][0].V)
		if errB != nil || errA != nil || nb != na+1 {
			return rb, false
		}
		out[ib] = Row{{N: field, V: strconv.Itoa(na)}}
		shown = true
	}
	if !shown {
		return rb, false
	}
	return out, true
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
	// c6SetAside is C-6 on a migrated store: the record above format 4's
	// largest storable record is set aside (store-migrate --drop-oversized),
	// so format 4's summaries lack it.
	c6SetAside = "C-6: the migration sets aside the record above format 4's largest storable record (--drop-oversized); format 4's summaries do not count it"
	// c38PerFeedSurface is C-38 (5) on the surface listing.
	c38PerFeedSurface = "C-38 (5): a \"<TYPE>@<source>\" relation counts the feed's records (a record in N feeds counts in each); format 1 counts its resident rows, one per record in its first source's partition"
)

// The C-41 rulings (contract v19, coordinator 2026-10-03 ~10:30) the
// comparator accepts, each on the calls, fields and rows it names. C-41's
// must-fix item N9 (NEWEST/RECENT pages project the delivery the record is
// ordered by) is never accepted.
const (
	c41N3 = "C-41 N3: format 1's cursor page lists its tagged records before its untagged ones (a format-1 bug that can skip records in datasync); format 4 pages strictly by seq"
	c41N4 = "C-41 N4: a url re-delivery restamps the lane's time on format 4 (lane metadata only); format 1's DUP leaves its summary alone"
	c41N5 = "C-41 N5: a record new to a second feed takes that delivery's ts (C-38: one row per feed); format 1 keeps the record's first ts"
	c41N6 = "C-41 N6: format 1's SQL relation is its engine hot window, which a delete shrinks; format 4 counts the true newest N"
	c41N7 = "C-41 N7: the migration does not carry the lane times format 1 restamped by its delete verbs (as U3)"
	c41N8 = "C-41 N8: a migrated record's copies go into every feed file of the record (format 1 never recorded which copy came with which tag)"
)

// c42 is C-42's ruling (contract v20, coordinator 2026-10-03 ~13:10).
const c42 = "C-42: format 1 orders the lane page by the timestamp of the copy it happens to serve (an arbitrary one, C-12); format 4 pages the lane's records by its own order"

// The C-43 rulings (contract v21, coordinator 2026-10-03 ~19:00) the
// comparator accepts. C-43's must-fix items (B2: "<TYPE>@local" answers the
// type's untagged records; B3: a relation never reads another feed) are
// never accepted. G1 is C-42 (c42) on rows whose provenance is blank on both
// formats (LaneKey.Members); G2 is N7 (c41N7) on X15's odd-named feed.
const (
	c43G3 = "C-43 G3: N3 on a cursor page cut at its limit: format 1's tagged-first order changes which records its cut page keeps; format 4's page is the seq-ordered prefix"
	c43G4 = "C-43 G4: format 1 answers the SQL stream with no rows once odd-named feeds exist (its SQLite table names break on those characters), a format-1 artifact"
	c43B3 = "C-43 B3 residual: format 1 answers a relation whose source equals another feed's but for case with its own order-dependent choice between the twins; format 4 reads the exact spelling's feed, else none"
)

// c44G5 is C-44's ruling (contract v22, coordinator 2026-10-03 ~21:00).
const c44G5 = "C-44 G5: format 1's StoreRoutedByProducer stores without its engine mirror, which its SQL relations and epoch stream read, so they miss a routed record until a restart rehydrates the mirror; format 4 answers it at once"

// wholeCallRuling is call's F1Empty or CaseTwin ruling (nil: none).
func (p Policy) wholeCallRuling(call string) *CallRuling {
	for i := range p.Calls {
		if r := &p.Calls[i]; (r.F1Empty || r.CaseTwin) && strings.Contains(call, r.Call) {
			return r
		}
	}
	return nil
}

// caseTwinAnswer reports a candidate answer CallRuling.CaseTwin accepts:
// equal to want when want is set, else no rows, one error row, or one row
// whose every value is 0 (a count of nothing).
func caseTwinAnswer(rb, want []Row) bool {
	if len(want) > 0 {
		if len(rb) != len(want) {
			return false
		}
		for i := range rb {
			if strictText(rb[i]) != strictText(want[i]) {
				return false
			}
		}
		return true
	}
	if len(rb) == 0 {
		return true
	}
	if len(rb) != 1 {
		return false
	}
	if _, isErr := rb[0].lookup("err"); isErr {
		return true
	}
	for _, f := range rb[0] {
		if f.V != "0" {
			return false
		}
	}
	return len(rb[0]) > 0
}

// sameLaneRuling is call's SameLane ruling (nil: none).
func (p Policy) sameLaneRuling(call string) *CallRuling {
	for i := range p.Calls {
		if r := &p.Calls[i]; r.SameLane != nil && strings.Contains(call, r.Call) {
			return r
		}
	}
	return nil
}

// tsRank orders canonical timestamps: a number is its value; a store-clock
// time ("now", "now@<j>": stamped during the class, after every fixture
// time) ranks above every number, in write order.
func tsRank(v string) (int64, bool) {
	if v == "now" {
		return 1 << 62, true
	}
	if j, ok := strings.CutPrefix(v, "now@"); ok {
		n, err := strconv.ParseInt(j, 10, 64)
		return 1<<62 + 1 + n, err == nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil
}

// sameLanePage is CallRuling.SameLane on one call (C-42): format 1 orders a
// lane's page by the timestamp of the copy it serves (key, "~ts"), and that
// copy is arbitrary (C-12). The candidate's page holds the same number of
// rows (the call's limit); each is a record of the lane (its provenance),
// carries one of format 1's copies of it (member: the copy oracle), and is
// at or after the timestamp format 1's page ends at (a tie the limit cut);
// every candidate row above that timestamp is one of format 1's rows, and
// every one of format 1's rows above it the candidate lacks is ranked by a
// copy the class wrote (a store-clock timestamp: format 4 serves another
// copy of that record); a row of a record both pages hold is format 1's row
// but for the copy. A row is a record of the lane by its provenance, or,
// where the rows carry none on either format (C-43 G1: every row of both
// pages blank), by its CID among the lane's records (lane.Members). It
// returns "" when the page is such a page, else why not.
func sameLanePage(ra, rb []Row, lane LaneKey, key string, member func(Row) bool) string {
	if len(ra) == 0 || len(ra) != len(rb) {
		return fmt.Sprintf("%d rows, format 1 %d", len(rb), len(ra))
	}
	blank := func(r Row) bool { return r.Get("provider") == "" && r.Get("source") == "" && r.Get("batch") == "" }
	byCID := lane.Members != nil
	for _, r := range append(append([]Row(nil), ra...), rb...) {
		byCID = byCID && blank(r)
	}
	cut, ok := tsRank(ra[len(ra)-1].Get(key))
	if !ok {
		return "format 1's last row has no timestamp"
	}
	f1 := map[string]Row{}
	for _, r := range ra {
		f1[r.Get("cid")] = r
	}
	in := map[string]bool{}
	for i, r := range rb {
		cid := r.Get("cid")
		in[cid] = true
		switch {
		case byCID:
			if !lane.Members[cid] {
				return fmt.Sprintf("row %d (%s) is not among the lane's records", i, cid)
			}
		case r.Get("provider") != lane.Provider || r.Get("source") != lane.Source || r.Get("batch") != lane.Batch:
			return fmt.Sprintf("row %d (%s) is not a record of the lane", i, cid)
		}
		if !member(r) {
			return fmt.Sprintf("row %d (%s) carries no copy format 1 holds", i, cid)
		}
		ts, ok := tsRank(r.Get(key))
		if !ok || ts < cut {
			return fmt.Sprintf("row %d (%s) is older than format 1's page", i, cid)
		}
		a, held := f1[cid]
		if ts > cut && !held {
			return fmt.Sprintf("row %d (%s) is above format 1's last timestamp and not among its rows", i, cid)
		}
		if held && strictText(a) != strictText(r) {
			return fmt.Sprintf("row %d (%s) differs from format 1's row of the record", i, cid)
		}
	}
	for i, r := range ra {
		if ts, _ := tsRank(r.Get(key)); ts > cut && !in[r.Get("cid")] && !strings.HasPrefix(r.Get(key), "now") {
			return fmt.Sprintf("format 1's row %d (%s) is above its last timestamp, ranked by a fixture copy, and missing", i, r.Get("cid"))
		}
	}
	return ""
}

// listingRulings are call's TaggedFirst and PerFeed rulings (nil: none).
func (p Policy) listingRulings(call string) (taggedFirst, perFeed *CallRuling) {
	for i := range p.Calls {
		r := &p.Calls[i]
		if !strings.Contains(call, r.Call) {
			continue
		}
		if r.TaggedFirst && taggedFirst == nil {
			taggedFirst = r
		}
		if r.PerFeed && perFeed == nil {
			perFeed = r
		}
	}
	return taggedFirst, perFeed
}

// perFeedRows is CallRuling.PerFeed on a record listing: the candidate lists
// a record once per feed that holds it (C-38 (5); C-41 N8: a migrated
// record's copies sit in every feed file of the record), format 1 once
// (C-10's collapse of its tag rows). Each listing of a record the candidate
// repeats must be a different one of format 1's tag rows of that record
// (alts: format 1's rows by CID before the collapse). It returns the
// candidate's rows with each such record once, at its first listing's
// position (the listing equal to format 1's collapsed row, else the first),
// and the number of listings dropped; ok is false when a repeated record's
// listings are not distinct tag rows format 1 holds for it (the rows are
// then compared as they are).
func perFeedRows(ra []Row, alts map[string][]Row, rb []Row, strict bool) (out []Row, dropped int, ok bool) {
	same := func(a, b Row) bool {
		st, _, _ := compareRow(a, b, strict)
		return st == EqEqual || st == EqExtra
	}
	first := map[string]Row{} // format 1's collapsed row of each CID
	for _, r := range ra {
		if c := r.Get("cid"); c != "" {
			first[c] = r
		}
	}
	listings := map[string][]int{}
	for i, r := range rb {
		if c := r.Get("cid"); c != "" {
			listings[c] = append(listings[c], i)
		}
	}
	keep := map[string]int{} // a repeated record's kept listing
	for c, idx := range listings {
		if len(idx) < 2 {
			continue
		}
		cand := alts[c]
		if len(cand) < len(idx) {
			return rb, 0, false
		}
		used := make([]bool, len(cand))
		for _, i := range idx {
			matched := false
			for j := range cand {
				if !used[j] && same(cand[j], rb[i]) {
					used[j], matched = true, true
					break
				}
			}
			if !matched {
				return rb, 0, false
			}
		}
		keep[c] = idx[0]
		if f, ok := first[c]; ok {
			for _, i := range idx {
				if same(f, rb[i]) {
					keep[c] = i
					break
				}
			}
		}
		dropped += len(idx) - 1
	}
	if dropped == 0 {
		return rb, 0, true
	}
	out = make([]Row, 0, len(rb)-dropped)
	placed := map[string]bool{}
	for _, r := range rb {
		c := r.Get("cid")
		k, repeated := keep[c]
		switch {
		case !repeated:
			out = append(out, r)
		case !placed[c]:
			out, placed[c] = append(out, rb[k]), true
		}
	}
	return out, dropped, true
}

// taggedFirstOrder is CallRuling.TaggedFirst on a record listing (C-41 N3):
// format 1's cursor page is two queries, its tagged records by rowid, then
// its untagged ones by rowid; format 4 lists every record by seq. The
// candidate's rows may come in any order that keeps format 1's tagged rows
// in their order and its untagged rows in theirs (an interleaving of the
// two, records matched by CID) and that ascends by cursor (rowid: a number,
// or "new", above every number). It returns the candidate's rows in format
// 1's order and whether any moved; ok is false when the candidate's order
// is no such interleaving (the rows are then compared as they are). The
// rows themselves are compared afterwards, field by field.
func taggedFirstOrder(ra, rb []Row) (out []Row, moved, ok bool) {
	if len(ra) != len(rb) {
		return rb, false, false
	}
	var tagged, untagged []int
	seen := map[string]bool{}
	for i, r := range ra {
		c := r.Get("cid")
		if c == "" || seen[c] {
			return rb, false, false
		}
		seen[c] = true
		if r.Get("provider") != "" || r.Get("source") != "" || r.Get("batch") != "" {
			tagged = append(tagged, i)
		} else {
			untagged = append(untagged, i)
		}
	}
	out = make([]Row, len(rb))
	ti, ui := 0, 0
	prev, prevNew := int64(-1), false
	for k, r := range rb {
		switch id := r.Get("rowid"); {
		case id == "new":
			prevNew = true
		case prevNew:
			return rb, false, false
		default:
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil || n < prev {
				return rb, false, false
			}
			prev = n
		}
		c := r.Get("cid")
		var pos int
		switch {
		case ti < len(tagged) && ra[tagged[ti]].Get("cid") == c:
			pos, ti = tagged[ti], ti+1
		case ui < len(untagged) && ra[untagged[ui]].Get("cid") == c:
			pos, ui = untagged[ui], ui+1
		default:
			return rb, false, false
		}
		out[pos] = r
		moved = moved || pos != k
	}
	return out, moved, true
}

// taggedFirstCut is CallRuling.TaggedFirst with Cut on a page both formats
// cut at the call's limit (C-43 G3). Format 1's page is its tagged records
// by rowid, then its untagged ones in the rows the limit leaves; format 4's
// is the records by seq up to the limit. So each page may hold records the
// other cut: format 4's are untagged records format 1's tagged rows crowded
// out (after every untagged record format 1 lists), or tagged records past
// all of format 1's (then format 1 lists no record format 4 lacks); format
// 1's are tagged records past format 4's cut (the tail of its tagged part).
// The records both hold must come in an interleaving of format 1's tagged
// and untagged orders (taggedFirstOrder). rawA and rawB are the answers' row
// counts before the collapse and the per-feed alignment. It returns both
// pages' common records, the candidate's in format 1's order, to be compared
// field by field; ok is false when the pages are not such pages.
func taggedFirstCut(ra, rb []Row, rawA, rawB, limit int) (fa, fb []Row, note string, ok bool) {
	if limit <= 0 || rawA != limit || rawB != limit {
		return nil, nil, "", false
	}
	tagged := func(r Row) bool { return r.Get("provider") != "" || r.Get("source") != "" || r.Get("batch") != "" }
	inA, inB := map[string]int{}, map[string]bool{}
	for i, r := range ra {
		c := r.Get("cid")
		if _, dup := inA[c]; c == "" || dup {
			return nil, nil, "", false
		}
		inA[c] = i
	}
	for _, r := range rb {
		c := r.Get("cid")
		if c == "" || inB[c] {
			return nil, nil, "", false
		}
		inB[c] = true
	}
	// Format 1's records both pages hold, in its tagged and untagged orders;
	// what only format 1 holds must be the tail of its tagged part.
	var tc, uc []int
	onlyA, tail := 0, false
	for i, r := range ra {
		common := inB[r.Get("cid")]
		switch {
		case !common && !tagged(r):
			return nil, nil, "", false // an untagged record format 4 lacks
		case !common:
			onlyA, tail = onlyA+1, true
		case tagged(r) && tail:
			return nil, nil, "", false // a common tagged record after one format 4 lacks
		case tagged(r):
			tc = append(tc, i)
		default:
			uc = append(uc, i)
		}
	}
	pos := make([]int, 0, len(tc)+len(uc)) // format 1's index of each candidate common row
	ti, ui, onlyB := 0, 0, 0
	sawOnlyUntagged, sawOnlyTagged := false, false
	for _, r := range rb {
		i, common := inA[r.Get("cid")]
		switch {
		case !common && !tagged(r):
			onlyB++
			sawOnlyUntagged = true
		case !common:
			onlyB++
			sawOnlyTagged = true
		case ti < len(tc) && tc[ti] == i:
			if sawOnlyTagged {
				return nil, nil, "", false // format 4's own tagged record before format 1's
			}
			pos, ti = append(pos, i), ti+1
		case ui < len(uc) && uc[ui] == i:
			if sawOnlyUntagged {
				return nil, nil, "", false // format 4's own untagged record before format 1's
			}
			pos, ui = append(pos, i), ui+1
		default:
			return nil, nil, "", false // not an interleaving of format 1's two orders
		}
	}
	if sawOnlyTagged && onlyA > 0 {
		return nil, nil, "", false
	}
	sort.Ints(pos)
	byCID := map[string]Row{}
	for _, r := range rb {
		byCID[r.Get("cid")] = r
	}
	for _, i := range pos {
		fa = append(fa, ra[i])
		fb = append(fb, byCID[ra[i].Get("cid")])
	}
	return fa, fb, fmt.Sprintf("both pages cut at %d rows: %d records in both, %d only format 4 lists (records format 1's tagged rows crowded out), %d only format 1 lists (tagged records past format 4's cut)",
		limit, len(pos), onlyB, onlyA), true
}

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
	// standard or record dropped; a record listing aligned): never reported
	// as equal.
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
		if wr := pol.wholeCallRuling(a.Call); wr != nil {
			switch {
			case wr.F1Empty && len(ra) == 0 && len(rb) > 0:
				v.F1Rows, v.SRows = v.F1Rows+len(ra), v.SRows+len(rb)
				forced, rulings[wr.Why] = true, true
				v.Notes = append(v.Notes, fmt.Sprintf("%s: format 1 answers no rows, format 4 %d: %s", a.Call, len(rb), wr.Why))
				continue
			case wr.CaseTwin && !sameMultiset(ra, rb) && caseTwinAnswer(rb, wr.Want):
				v.F1Rows, v.SRows = v.F1Rows+len(ra), v.SRows+len(rb)
				forced, rulings[wr.Why] = true, true
				v.Notes = append(v.Notes, fmt.Sprintf("%s: format 1 answers another case twin's rows; format 4's answer is the exact spelling's (or none): %s", a.Call, wr.Why))
				continue
			}
		}
		if kept, why := pol.absentRows(a.Call, ra); len(why) > 0 {
			v.Notes = append(v.Notes, fmt.Sprintf("%s: %d of format 1's rows name a standard or a record format 4 holds nothing of", a.Call, len(ra)-len(kept)))
			ra, forced = kept, true
			for _, w := range why {
				rulings[w] = true
			}
		}
		if r := pol.s4OnlyRuling(a.Call); r != nil {
			if out, ok := s4OnlyRows(ra, rb, r.S4Only); ok {
				rb, forced, rulings[r.Why] = out, true, true
				v.Notes = append(v.Notes, fmt.Sprintf("%s: format 4 also answers record %s, which format 1 lacks: %s", a.Call, r.S4Only.CID, r.Why))
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
		// A record listing's rulings (C-38 (5), C-41 N8: a record once per
		// feed; C-41 N3: format 1's tagged records first): the candidate's
		// rows aligned to format 1's, then compared field by field.
		if tf, pf := pol.listingRulings(a.Call); tf != nil || pf != nil {
			if pf != nil {
				alts := repeated
				if alts == nil {
					alts = rowsByCID(ra)
				}
				if out, n, ok := perFeedRows(ra, alts, rb, pol.Strict); ok && n > 0 {
					rb, forced, rulings[pf.Why] = out, true, true
					v.Notes = append(v.Notes, fmt.Sprintf("%s: %d more listings of records held by several feeds, each another of format 1's tag rows of the record", a.Call, n))
				}
			}
			if tf != nil {
				out, moved, ok := taggedFirstOrder(ra, rb)
				switch {
				case ok && moved:
					rb, forced, rulings[tf.Why] = out, true, true
					v.Notes = append(v.Notes, a.Call+": format 4's seq order interleaves format 1's tagged and untagged parts, each in format 1's order")
				case !ok && tf.Cut > 0:
					if fa, fb, note, cut := taggedFirstCut(ra, rb, len(a.Rows), len(b.Rows), tf.Cut); cut {
						ra, rb, forced = fa, fb, true
						rulings[tf.Why], rulings[c43G3] = true, true
						v.Notes = append(v.Notes, a.Call+": "+note)
					}
				}
			}
		}
		if sl := pol.sameLaneRuling(a.Call); sl != nil {
			schema := schemaOfCall(a.Call, f1.Schema)
			why := sameLanePage(ra, rb, *sl.SameLane, "~ts", func(r Row) bool {
				return variantMatchesCopy(r, schema, shapeOracle(f1, oracle), variantCache)
			})
			if why == "" {
				v.F1Rows += len(ra)
				v.SRows += len(rb)
				forced, rulings[sl.Why] = true, true
				v.Notes = append(v.Notes, a.Call+": "+sl.Why+" (each row a record of the lane with one of format 1's copies; the pages differ within the cut tie and by the copy format 1 ranked a record by)")
				continue
			}
			v.Notes = append(v.Notes, a.Call+": not a C-42 page: "+why)
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
		// A cut tie compared other members' rows, format 1's rows of an
		// absent standard or record were dropped, or a record listing was
		// aligned: an intended difference, reported as such rather than as
		// equal.
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
