package format4proof

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	CATfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	OMMfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// flatsqlrt5m are the module-facing SQL limits the coverage classes read
// under (sandboxCaps).
var flatsqlrt5m = flatsqlrt.SandboxCaps{Timeout: 5 * time.Minute}

// ommNorad is an OMM record's NORAD_CAT_ID.
func ommNorad(rec []byte) (uint32, bool) {
	if len(rec) < 8 {
		return 0, false
	}
	return OMMfb.GetRootAsOMM(rec, 0).NORAD_CAT_ID(), true
}

// catNorad is a CAT record's NORAD_CAT_ID.
func catNorad(rec []byte) uint32 { return CATfb.GetRootAsCAT(rec, 0).NORAD_CAT_ID() }

// Coverage: the classes that exercise every recordBackend method and every
// parameter axis a caller can send that the benchset's reads (R01–R24) and
// writes (W01–W10) leave out. The method × axis table, with the shape that
// covers each cell, is COVERAGE.md beside the contract. Owner, 2026-10-02:
// "they should be proof of every possible query, because SDN software is
// limited to adding SDN records, purging databases, and direct queries,
// that's it."
//
// A coverage class is untimed (equivalence only). It runs in one process on
// a fresh clone of the arm's fixture: every shape of the class in order,
// every call once. V classes read the fixture as it is. X classes write and
// then read what the writes left (one scenario per class, so no scenario
// sees another's writes). Every call's answer is a list of rows, an error
// included (errRow: the error's kind, so error parity is compared and not
// waved through as "both error"). Answers are written where the read
// answers go (answers-<arm>-fixture-<class>), and DriveEquivalence compares
// them with format 1's field by field under Policy.Strict: a field the
// candidate fills where format 1 leaves it empty is a difference.
//
// Two values of a written record carry the arm's own clock or cursor, and
// are canonicalized identically on every arm (scen): a time at or after the
// class's start reads "now@<j>", j the class's last write that began at or
// before it (the store's clock: format 1's created_at and timestamp, format
// 4's at and ts; every write begins in a later second than the previous one
// ended in, so values stamped by different writes stay apart and the rows
// one write stamped tie), "now" before the first write; and a datasync
// cursor (rowid) above the
// type's highest before the class reads "new" (C-14: format 4 numbers new
// records from the migration's seq floor). Everything else, the order of
// rows by cursor included, must be identical. Sealed stored bytes (C-25,
// field encryption to the store's own random key) read "sealed" with their
// length; opened bytes compare as they are.

// KindCoverage is a coverage class's run.
const KindCoverage = "coverage"

// ModeCoverage is the coverage child.
const ModeCoverage = "coverage"

// CoverageSpec is one coverage class on one arm.
type CoverageSpec struct {
	Arm, Class, Store, Out, Work string
	// T0, Base and Writes, when set, are the scen of the run whose writes
	// the store holds (XM on format 4: the migrated format-1 store), so its
	// answers canonicalize as that run's did.
	T0     int64            `json:"t0,omitempty"`
	Base   map[string]int64 `json:"base,omitempty"`
	Writes []int64          `json:"writes,omitempty"`
}

// coverageSchemas are the types a coverage class takes its cursor base of:
// the fixture's four and the types the X classes write first.
var coverageSchemas = []string{"OMM.fbs", "MPE.fbs", "IQC.fbs", "CAT.fbs", "PNM.fbs", "KMF.fbs", "EPM.fbs", "PLOG.fbs"}

// scen is a coverage class's run context: when it started, and each type's
// highest datasync cursor before it.
type scen struct {
	t0        int64
	base      map[string]int64
	lastWrite int64   // the second the class's last write ended in (nextSecond)
	writes    []int64 // the second each of the class's writes began in, in order
}

func newScen() *scen { return &scen{base: map[string]int64{}} }

// start records the class's start and the cursor bases (untimed; before the
// first call).
func (sc *scen) start(s *storage.FlatSQLStore) {
	sc.t0 = time.Now().Unix() - 2
	for _, schema := range coverageSchemas {
		if h, err := s.RawRecordHead(storage.RawRecordQuery{SchemaName: schema}); err == nil {
			sc.base[schema] = h.MaxRowID
		}
	}
}

func (sc *scen) baseOf(schema string) int64 {
	if b, ok := sc.base[schema]; ok && schema != "" {
		return b
	}
	var m int64
	for _, b := range sc.base {
		if b > m {
			m = b
		}
	}
	return m
}

// timeField names the fields that may carry the store's clock.
func timeField(name string) bool {
	n := strings.ToLower(name)
	if n == "~ts" || n == "ts" || n == "materialized" || n == "max_ts" || n == "max_updated" || n == "max_created" {
		return true
	}
	for _, k := range []string{"seen", "updated", "created", "timestamp", "published", "time"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return strings.HasSuffix(n, "_at") || strings.HasSuffix(n, "atunix")
}

// unixValue reads a field value as Unix seconds: an integer (bare or JSON)
// or an RFC 3339 time (bare or JSON-quoted).
func unixValue(v string) (int64, bool) {
	v = strings.Trim(v, `"`)
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, true
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.Unix(), true
	}
	return 0, false
}

// canon canonicalizes one row of schema (scen's rules).
func (sc *scen) canon(row Row, schema string) Row {
	out := make(Row, len(row))
	for i, f := range row {
		out[i] = Field{f.N, sc.canonValue(f.N, f.V, schema)}
	}
	return out
}

func (sc *scen) canonValue(name, v, schema string) string {
	n := strings.ToLower(name)
	if strings.Contains(n, "rowid") {
		if x, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64); err == nil && x > sc.baseOf(schema) {
			return "new"
		}
		return v
	}
	if sc.t0 > 0 && timeField(n) {
		if x, ok := unixValue(v); ok && x >= sc.t0 {
			return sc.nowOf(x)
		}
	}
	return v
}

// nowOf is a store-clock time's canonical value: "now@<j>", j the last write
// that began at or before x, or "now" before the first write.
func (sc *scen) nowOf(x int64) string {
	j := -1
	for i, w := range sc.writes {
		if w <= x {
			j = i
		}
	}
	if j < 0 {
		return "now"
	}
	return "now@" + strconv.Itoa(j)
}

// errKind is an error's kind, the part two formats' texts share.
func errKind(err error) string {
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "poisoned"):
		return "engine poisoned"
	case strings.Contains(m, "wall-clock timeout") || strings.Contains(m, "deadline exceeded"):
		return "timeout"
	case strings.Contains(m, "not found"):
		return "not found"
	case strings.Contains(m, "unsupported") || strings.Contains(m, "not supported"):
		return "unsupported"
	case strings.Contains(m, "invalid schema") || strings.Contains(m, "unknown schema") || strings.Contains(m, "not registered"):
		return "invalid schema"
	case strings.Contains(m, "is required"):
		return "required"
	case strings.Contains(m, "cid mismatch"):
		return "cid mismatch"
	case strings.Contains(m, "sync_filter") || strings.Contains(m, "sync filter"):
		return "sync filter"
	case strings.Contains(m, "not-authorized") || strings.Contains(m, "not authorized"):
		return "not authorized"
	case strings.Contains(m, "multi-statement"):
		return "multi-statement"
	case strings.Contains(m, "row-cap") || strings.Contains(m, "byte-cap"):
		return "cap"
	case strings.Contains(m, "search"):
		return "search"
	}
	return "error"
}

// answerTimedOut reports an answer whose call ran past an engine budget or
// found the engine poisoned.
func answerTimedOut(a Answer) bool {
	for _, r := range a.Rows {
		if k := r.Get("err"); k == "timeout" || k == "engine poisoned" {
			return true
		}
	}
	return false
}

// errRow is an error as a row: its kind (compared) only; the text is logged.
func errRow(call string, err error) Row {
	fmt.Printf("coverage: %s: %v\n", call, err)
	return Row{{"err", errKind(err)}}
}

// covRecordRow is a record's canonical row (RecordRow) with sealed stored
// bytes read as "sealed" (their envelope is random per store).
func covRecordRow(r *storage.Record) Row {
	row := RecordRow(r)
	if r != nil && encfield.IsSealed(r.Data) {
		for i := range row {
			if row[i].N == "data" {
				row[i].V = "sealed"
			}
		}
	}
	return row
}

// recordFields are a record row's compared fields (RecordRow's; the copy
// variants are checked against format 1's copies, C-12).
func recordFields() []string {
	var out []string
	for _, f := range RecordRow(&storage.Record{}) {
		if !isVariant(f.N) {
			out = append(out, f.N)
		}
	}
	return out
}

// summaryCalls are summaries' calls whose rows carry the lanes' counts,
// bytes and times.
var summaryCalls = []string{"DataSummary", "SourceBatchProgress", "ProducerSourceProgress"}

// u1Rows is C-39 U1 on the summaries read after a write of a schema with
// no standard: format 1's rows of it (SchemaDateRanges) are the difference.
func u1Rows(sh Shape, suffix string) Shape {
	sh.Policy.Calls = append(sh.Policy.Calls, CallRuling{Call: "SchemaDateRanges" + suffix, Why: c39U1, Absent: "XYZ.fbs"})
	return sh
}

// u2Rows is C-39 U2 on the summaries read after UpsertSourceTags of CIDs
// not held (an OMM miss, a non-canonical OMM CID, a held CID under the
// unknown XYZ.fbs): format 1's dangling tags are its XYZ.fbs rows and one
// more in the counts of the OMM lane, of the type and of the store (their
// bytes are 0, so no byte field may differ).
func u2Rows(sh Shape, suffix string) Shape {
	for _, call := range summaryCalls {
		sh.Policy.Calls = append(sh.Policy.Calls,
			CallRuling{Call: call + suffix, Why: c39U2, Absent: "XYZ.fbs"},
			CallRuling{Call: call + suffix, Why: c39U2, Fields: []string{"n", "Count", "total_records"}, Standard: "OMM.fbs"})
	}
	sh.Policy.Calls = append(sh.Policy.Calls, CallRuling{Call: "SourceRecordCounts" + suffix, Why: c39U2, Fields: []string{"n"}})
	return sh
}

// laneBytes is a C-39 ruling on the byte fields of std's lane rows and the
// store totals in the summaries (U5: KMF, U6: CAT).
func laneBytes(sh Shape, suffix, why, std string) Shape {
	for _, call := range summaryCalls {
		sh.Policy.Calls = append(sh.Policy.Calls,
			CallRuling{Call: call + suffix, Why: why, Fields: []string{"bytes", "TotalBytes", "total_bytes"}, Standard: std})
	}
	return sh
}

// getRow is GetRecord's answer: the bytes and the copy served. Format 1's
// GetRecord fills neither the cursor nor the tags (GetSourceTags and the
// raw reads compare those), so they are not part of it.
func getRow(r *storage.Record) Row {
	row := covRecordRow(r)
	out := Row{}
	for _, f := range row {
		switch f.N {
		case "cid", "len", "data", "rlen", "~peer", "~ts", "~sig":
			out = append(out, f)
		}
	}
	return out
}

// cov builds a coverage class's calls: every answer canonicalized by the
// class's scen.
type cov struct {
	sc   *scen
	in   *Inputs
	sets map[string][][]byte
	hits map[string][]string // R01's CIDs per type
	miss map[string][]string // R02's
}

// CoverageShapes builds every coverage class (V: reads, X: write scenarios)
// from the benchset's point lists and the inputs (Prepare); the X classes
// need the input sets and are left out without them. The returned scen is
// the one the shapes canonicalize with (RunCoverage starts it).
func CoverageShapes(bs *Benchset, in *Inputs, sets map[string][][]byte) ([]Shape, *scen, error) {
	if in == nil {
		in = &Inputs{}
	}
	r01, ok := bs.Read("R01")
	if !ok {
		return nil, nil, errors.New("benchset has no R01")
	}
	hits, err := hitLists(r01)
	if err != nil {
		return nil, nil, err
	}
	r02, ok := bs.Read("R02")
	if !ok {
		return nil, nil, errors.New("benchset has no R02")
	}
	miss, err := hitLists(r02)
	if err != nil {
		return nil, nil, err
	}
	for _, schema := range pointSchemas {
		if len(hits[schema]) < 16 || len(miss[schema]) == 0 {
			return nil, nil, fmt.Errorf("benchset R01/R02 name too few %s CIDs", schema)
		}
	}
	c := &cov{sc: newScen(), in: in, sets: sets, hits: hits, miss: miss}
	var shapes []Shape
	if len(in.B052CIDs) >= 1100 {
		shapes = append(shapes, c.readCoverage()...)
	}
	shapes = append(shapes, c.writeCoverage()...)
	return shapes, c.sc, nil
}

// rowsCall is a call whose answer is rows of schema (an error is one errRow).
func (c *cov) rowsCall(name, schema string, fn func(s *storage.FlatSQLStore) ([]Row, error)) Call {
	return Call{Name: name, Run: func(s *storage.FlatSQLStore) Result {
		rows, err := fn(s)
		return func() (Answer, Measure) {
			if err != nil {
				return Answer{Call: name, Rows: []Row{errRow(name, err)}}, Measure{}
			}
			out := make([]Row, 0, len(rows))
			for _, r := range rows {
				out = append(out, c.sc.canon(r, schema))
			}
			return Answer{Call: name, Rows: out}, Measure{Rows: int64(len(out))}
		}
	}}
}

// valueCall is a call whose answer is one row.
func (c *cov) valueCall(name, schema string, fn func(s *storage.FlatSQLStore) (Row, error)) Call {
	return c.rowsCall(name, schema, func(s *storage.FlatSQLStore) ([]Row, error) {
		r, err := fn(s)
		if err != nil {
			return nil, err
		}
		return []Row{r}, nil
	})
}

// recordsCall is a call whose answer is records (covRecordRow each).
func (c *cov) recordsCall(name, schema string, fn func(s *storage.FlatSQLStore) ([]*storage.Record, error)) Call {
	return c.rowsCall(name, schema, func(s *storage.FlatSQLStore) ([]Row, error) {
		recs, err := fn(s)
		if err != nil {
			return nil, err
		}
		rows := make([]Row, 0, len(recs))
		for _, r := range recs {
			rows = append(rows, covRecordRow(r))
		}
		return rows, nil
	})
}

// errOnly is a call made for its effect (a write, or a step a later write
// needs): its answer is "ok" or its error.
func (c *cov) errOnly(name string, fn func(s *storage.FlatSQLStore) error) Call {
	return c.writing(c.valueCall(name, "", func(s *storage.FlatSQLStore) (Row, error) {
		if err := fn(s); err != nil {
			return nil, err
		}
		return ValueRow("ok", "1"), nil
	}))
}

// writing marks a call as a write (Call.Write) and starts it in a later
// second than the class's previous write ended in (scen.nextSecond).
func (c *cov) writing(call Call) Call {
	run := call.Run
	call.Run = func(s *storage.FlatSQLStore) Result {
		c.sc.nextSecond()
		c.sc.writes = append(c.sc.writes, time.Now().Unix())
		res := run(s)
		c.sc.lastWrite = time.Now().Unix()
		return res
	}
	call.Write = true
	return call
}

// nextSecond waits until the clock has passed the second the previous write
// ended in. Stores stamp a tag's time in whole seconds; two tags of one
// record written in the same second tie, and which is "newest" (the tag
// GetSourceTags returns, an export prefers, a producer's last batch) is then
// arbitrary on format 1 (two format-1 runs of X02 disagreed). One second
// apart, the newest is defined on every format.
func (sc *scen) nextSecond() {
	for time.Now().Unix() <= sc.lastWrite {
		time.Sleep(20 * time.Millisecond)
	}
}

// c12Copy names the intended difference of a ref that names a copy format 1
// does not serve: format 1 reads a CID through a GROUP BY cid over every
// producer's table and so finds one copy per CID (C-12: "format 1's GROUP BY
// cid returns an arbitrary copy"); a ref whose PeerID names another
// producer's copy is "not found" there. Format 4 serves the named copy. The
// copies each producer holds are compared by the record-set digests (copy
// set), not here.
const c12Copy = "C-12: format 1 serves one copy per CID (GROUP BY cid over the producers' tables); a ref naming another producer's copy finds nothing there"

// c12Shape is a coverage shape of refs that name a copy format 1 does not
// serve (c12Copy): every field accepted, reported as such.
func c12Shape(class, name, schema string, calls ...Call) Shape {
	sh := covShape(class, name, schema, calls...)
	sh.Policy.Accepted, sh.Policy.AcceptedFields = c12Copy, nil // every field
	return sh
}

// r10Ruling is R10's intended difference, carried to every tag-filtered
// count and head without a cursor (C-10, coordinator ruling 2026-10-02
// ~05:10): format 4 counts a record once where format 1 counts its matching
// tag rows, and reports its per-type seq as max_rowid where format 1 reports
// a per-producer row id. covHead names those two fields lane_n and
// lane_max_rowid; every other head field is compared.
const r10Ruling = "C-10 and the R10 ruling (2026-10-02 ~05:10): with a tag filter and no cursor, format 4 counts a record once and reports its per-type seq as max_rowid"

// tagHead reports a raw query whose count and head fall under r10Ruling.
func tagHead(q storage.RawRecordQuery) bool {
	return !q.UseRowIDCursor && (q.ProviderID != "" || q.SourceName != "" || q.BatchID != "" || q.ProducerPeerID != "" || q.ProducerPublicKey != "")
}

// covHead is a count and/or head row (n < 0: no count), with r10Ruling's
// field names for a tag-filtered query.
func covHead(q storage.RawRecordQuery, n int64, h *storage.RawRecordHead) Row {
	nName, rowidName := "n", "max_rowid"
	if tagHead(q) {
		nName, rowidName = "lane_n", "lane_max_rowid"
	}
	var r Row
	if n >= 0 {
		r = append(r, Field{nName, i64(n)})
	}
	if h != nil {
		for _, f := range headRow(*h) {
			if f.N == "max_rowid" {
				f.N = rowidName
			}
			r = append(r, f)
		}
	}
	return r
}

// accept adds an intended difference (a contract row and its fields) to a
// shape's policy.
func accept(sh *Shape, why string, fields ...string) {
	if sh.Policy.Accepted != "" {
		why = sh.Policy.Accepted + "; " + why
	}
	sh.Policy.Accepted = why
	sh.Policy.AcceptedFields = append(sh.Policy.AcceptedFields, fields...)
}

// covShape is one coverage shape: strict, ordered, with r10Ruling on the
// lane_ fields and C-10's collapse of format 1's rows per record (every call
// but the refs reads, whose repeated refs return a record per ref). A call name that repeats within the shape (the same read
// before and after a write) gets " #n".
func covShape(class, name, schema string, calls ...Call) Shape {
	seen := map[string]int{}
	for i := range calls {
		seen[calls[i].Name]++
		if n := seen[calls[i].Name]; n > 1 {
			calls[i].Name = fmt.Sprintf("%s #%d", calls[i].Name, n)
		}
	}
	return Shape{Class: class, Name: name, Schema: schema, Fixture: FixtureT6W, Calls: calls,
		Policy: Policy{Strict: true, Collapse: true, CollapseSkip: "refs ", Accepted: r10Ruling,
			AcceptedFields: []string{"lane_n", "lane_bytes", "lane_max_rowid"}, TieOrdered: timestampOrdered}}
}

// timestampOrdered are the calls format 1 orders by a timestamp alone:
// QuerySourceTaggedRecords (ORDER BY records.timestamp DESC; the lane reads'
// "lane tagged") and the routed listings (a UNION of the producer tables
// ORDER BY timestamp DESC). Rows tied on it have no order (alignTies); a
// shape whose call a limit may cut inside a tie names the call's limit
// (tieLimit).
var timestampOrdered = []TieRule{{Call: "QuerySourceTaggedRecords", Key: "~ts"}, {Call: "lane tagged ", Key: "~ts", Limit: laneReadLimit},
	{Call: "QueryRouted", Key: "ts"}}

// tieLimit is sh with call's tie rule given its limit (format 1's
// effective row limit for that call) and its standard ("" = the shape's).
func tieLimit(sh Shape, call, key string, limit int, schema string) Shape {
	sh.Policy.TieOrdered = append([]TieRule{{Call: call, Key: key, Limit: limit, Schema: schema}}, sh.Policy.TieOrdered...)
	return sh
}

// c12PeerFilter names the intended difference of a raw page filtered by a
// copy's peer, for a record two producers hold: format 1 reads its records
// through the copy it serves (C-12: one copy per CID, GROUP BY cid over the
// producers' tables), so a peer filter naming the other producer's copy
// finds none of them; format 4 filters every copy.
const c12PeerFilter = "C-12: format 1 reads a raw page through the one copy it serves per CID; a peer filter naming another producer's copy finds nothing there"

// c38PerFeed names C-38 (5)'s intended difference (owner model, contract
// v16): there is no cross-feed identity, so a record that arrives through a
// second source feed is a new row set in that feed's file (C-38 (3)), and a
// record held by N feeds counts once per feed: a write counts it new to its
// feed, a supersede counts it as leaving its feed. Format 1 kept one record
// with N tags and counted it once.
const c38PerFeed = "C-38 (5): a record held by N feeds counts once per feed (new to a second feed; leaving one feed while another keeps it)"

// c38OwnFeed names C-38 (5)'s provenance rule: a row is the record in one
// feed file, and every provenance field of it is that feed's ("all
// provenance fields of each row are its own feed's"). A window projects the
// newest tag of the row's own feed; format 1 projected the record's newest
// tag of any feed, which for a record held by two feeds may be the other's.
const c38OwnFeed = "C-38 (5): a row's provenance is its own feed's; format 1 projects the record's newest tag of any feed"

// c38Hex names the contract's CID form (§3.8 (1), §3.7: a record is keyed by
// the CIDv1 raw sha2-256 of its bytes; non-bafkrei CIDs are refused): an
// imported index that names a record by its sha256-hex text keeps that text
// as the record's identity on format 1, and the CIDv1 on format 4, so a read
// by one name finds it on one format and not the other.
const c38Hex = "§3.8 (1): format 4 keys a record by the CIDv1 of its bytes; format 1 keeps an imported index's sha256-hex identity"

// c9Rowid names C-9's intended difference: a relation's _rowid is the
// record's seq (format 1's is its position in the engine hot window).
const c9Rowid = "C-9: a relation's _rowid is the record's seq (format 1: the row's position in its engine hot window)"

// rule adds a call ruling to a shape's policy.
func rule(sh Shape, call, why string, fields ...string) Shape {
	sh.Policy.Calls = append(sh.Policy.Calls, CallRuling{Call: call, Why: why, Fields: fields})
	return sh
}

// RunCoverage runs one coverage class on one arm: open, record the class's
// start, every call in order, close, then (format 1) record the copies of
// every CID a variant row names, and write the run and the answers.
func RunCoverage(spec CoverageSpec, shapes []Shape, sc *scen) (*Run, error) {
	r := &Run{Kind: KindCoverage, Arm: spec.Arm, Format: ArmFormat(spec.Arm), Label: LabelFixture, Class: spec.Class,
		Started: time.Now().UTC().Format(time.RFC3339), Machine: ThisMachine(), LoadStart: Load(), Extra: map[string]any{}}
	s, openMs, err := OpenArm(spec.Arm, spec.Store)
	r.OpenMs = openMs
	if err != nil {
		return r, err
	}
	if spec.Arm == ArmF1 && coverageNeedsSQL[spec.Class] {
		st := time.Now()
		n, err := s.HydrateEngineHotWindow()
		r.Extra["f1_hot_window_hydrate_s"], r.Extra["f1_hot_window_records"] = time.Since(st).Seconds(), n
		if err != nil {
			r.Extra["f1_hot_window_error"] = err.Error()
		}
	}
	sc.start(s)
	if spec.T0 > 0 {
		sc.t0, sc.base, sc.writes = spec.T0, spec.Base, spec.Writes
	}
	r.Extra["t0"], r.Extra["base"] = sc.t0, sc.base
	for _, sh := range shapes {
		if sh.Setup != nil {
			sh.Setup(s)
		}
	}
	answers := &AnswerFile{Arm: spec.Arm, Label: LabelFixture, Class: spec.Class}
	var poisoned []string
	st := time.Now()
	for _, sh := range shapes {
		sa := ShapeAnswers{Class: sh.Class, Shape: sh.Name, Schema: sh.Schema, Policy: sh.Policy}
		for _, call := range sh.Calls {
			cs := time.Now()
			var res Result
			d, ok := guard(30*time.Minute, func() { res = call.Run(s) })
			var a Answer
			var m Measure
			switch {
			case !ok:
				a = Answer{Call: call.Name, Err: fmt.Sprintf("timeout after %s", d.Round(time.Second))}
			case res != nil:
				a, m = res()
			}
			a.Call = call.Name
			fmt.Printf("coverage: call %s / %s: %s, %d rows\n", sh.Name, call.Name, time.Since(cs).Round(time.Millisecond), len(a.Rows))
			// A call that ran its engine past its budget poisons it (format
			// 1's control engine): replace it, as the node does, so the
			// class's other calls are answered; the call is named in the run.
			if !ok || answerTimedOut(a) {
				poisoned = append(poisoned, sh.Name+" / "+call.Name)
				if _, err := s.RecoverPoisonedEngine(); err != nil {
					r.Extra["recover_error"] = err.Error()
				}
			}
			r.Samples = append(r.Samples, Sample{Class: spec.Class, Shape: sh.Name, Ms: float64(time.Since(cs).Microseconds()) / 1000,
				Rows: m.Rows, Bytes: m.Bytes, Err: a.Err})
			sa.Calls = append(sa.Calls, a)
			if !ok {
				break
			}
		}
		answers.Shapes = append(answers.Shapes, sa)
	}
	r.Extra["calls_s"] = time.Since(st).Seconds()
	r.Extra["writes"] = append([]int64{}, sc.writes...)
	r.Extra["timed_out_calls"] = append([]string{}, poisoned...)
	if err := s.Close(); err != nil {
		r.Extra["close_error"] = err.Error()
	}
	if spec.Arm == ArmF1 {
		if err := recordFormat1Copies(spec.Store, answers, sc); err != nil {
			r.Extra["copies_error"] = err.Error()
		}
	}
	r.LoadEnd = Load()
	if spec.Out != "" {
		if _, err := WriteRun(spec.Out, r); err != nil {
			return r, err
		}
		if err := WriteAnswers(spec.Out, answers); err != nil {
			return r, err
		}
	}
	return r, nil
}

// maxCopyCIDs bounds the CIDs a class records copies of.
const maxCopyCIDs = 20000

// recordFormat1Copies records, per shape, format 1's copies (peer, ts,
// signature) of every CID whose row carries copy variants, read from the
// closed store through its own engine, canonicalized as the rows are.
func recordFormat1Copies(store string, answers *AnswerFile, sc *scen) error {
	want := map[string]bool{}
	for _, sa := range answers.Shapes {
		for _, a := range sa.Calls {
			for _, row := range a.Rows {
				if row.Get("~peer") != "" && row.Get("cid") != "" {
					want[row.Get("cid")] = true
				}
			}
		}
	}
	if len(want) == 0 {
		return nil
	}
	if len(want) > maxCopyCIDs {
		return fmt.Errorf("%d CIDs carry copy variants (bound %d): copies not recorded", len(want), maxCopyCIDs)
	}
	src, err := storage.OpenMigrationSource(store)
	if err != nil {
		return err
	}
	defer src.Close()
	tables, err := src.ProducerTables()
	if err != nil {
		return err
	}
	cids := make([]string, 0, len(want))
	for c := range want {
		cids = append(cids, c)
	}
	sort.Strings(cids)
	copies := map[string][]Row{}
	for _, t := range tables {
		recs, err := src.RecordsByCID(t, cids)
		if err != nil {
			return err
		}
		for cid, rec := range recs {
			var sig []byte
			if rec.SignatureHex != "" {
				sig, _ = hex.DecodeString(rec.SignatureHex)
			}
			row := Row{{"~peer", rec.PeerID}, {"~ts", unixOf(time.Unix(rec.Timestamp, 0))}, {"~sig", digest(sig)}}
			copies[cid] = append(copies[cid], sc.canon(row, t.Schema))
		}
	}
	for i := range answers.Shapes {
		sa := &answers.Shapes[i]
		for _, a := range sa.Calls {
			for _, row := range a.Rows {
				cid := row.Get("cid")
				if row.Get("~peer") == "" || cid == "" {
					continue
				}
				if sa.Copies == nil {
					sa.Copies = map[string][]Row{}
				}
				sa.Copies[cid] = copies[cid]
			}
		}
	}
	return nil
}

// coverageNeedsSQL are the classes that read through format 1's engine hot
// window (SQL relations, the epoch stream): format 1 fills it first.
var coverageNeedsSQL = map[string]bool{}

// CoverageClasses lists the coverage classes in order.
func CoverageClasses(shapes []Shape) []string { return ClassesOf(shapes) }

// DriveCoverage runs every coverage class (or c.Classes, those that are
// coverage classes) on every arm, each on a fresh clone.
func DriveCoverage(ctx context.Context, c Config, logf Logf) error {
	in, sets, err := LoadInputs(c.Work)
	if err != nil {
		return err
	}
	bs, err := LoadBenchset(c.Benchset)
	if err != nil {
		return err
	}
	shapes, _, err := CoverageShapes(bs, in, sets)
	if err != nil {
		return err
	}
	classes := CoverageClasses(shapes)
	if len(c.Classes) > 0 {
		var keep []string
		for _, cl := range classes {
			if contains(c.Classes, cl) {
				keep = append(keep, cl)
			}
		}
		classes = keep
	}
	if len(classes) == 0 {
		return errors.New("no coverage classes selected")
	}
	var firstErr error
	for _, class := range classes {
		if class == ClassXM {
			if err := c.driveXM(ctx, logf); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, arm := range c.Arms {
			src := c.Fixtures[arm]
			if src == "" {
				logf("coverage %s %s: no fixture, skipped", arm, class)
				continue
			}
			name := "coverage-" + arm + "-" + class
			err := c.withClone(src, name, func(clone string) error {
				spec := CoverageSpec{Arm: arm, Class: class, Store: clone, Out: c.Out, Work: c.Work}
				cr, err := RunChild(ctx, ChildSpec{Mode: ModeCoverage, Benchset: c.Benchset, Work: c.Work, Coverage: &spec}, c.logPath(name))
				logf("coverage %s %s: %s, max RSS %.0f MB, load %s, err %v", class, arm, cr.Wall.Round(time.Second), cr.MaxRSSMB, Load(), err)
				return err
			})
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// driveXM runs the migration class: format 2 on a fresh clone as any class;
// format 1 on a clone it keeps, then (with format 4 among the arms and an
// SDN binary) that clone's record sets digested, store-migrate --to 4
// --inventory, the migration, its checks and --verify-only, the format-4
// digests, and the class's reads on the migrated store.
func (c Config) driveXM(ctx context.Context, logf Logf) error {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	child := func(spec CoverageSpec, name string) error {
		cr, err := RunChild(ctx, ChildSpec{Mode: ModeCoverage, Benchset: c.Benchset, Work: c.Work, Coverage: &spec}, c.logPath(name))
		logf("coverage %s %s: %s, max RSS %.0f MB, load %s, err %v", ClassXM, spec.Arm, cr.Wall.Round(time.Second), cr.MaxRSSMB, Load(), err)
		return err
	}
	if contains(c.Arms, ArmF2) && c.Fixtures[ArmF2] != "" {
		note(c.withClone(c.Fixtures[ArmF2], "coverage-f2-XM", func(clone string) error {
			return child(CoverageSpec{Arm: ArmF2, Class: ClassXM, Store: clone, Out: c.Out, Work: c.Work}, "coverage-f2-XM")
		}))
	}
	if !contains(c.Arms, ArmF1) || c.Fixtures[ArmF1] == "" {
		if contains(c.Arms, ArmS) {
			logf("coverage XM: format 4 reads the migrated format-1 store; format 1 is not among the arms, skipped")
		}
		return firstErr
	}
	store := c.workPath("coverage-xm-store")
	_ = os.RemoveAll(store)
	if err := CloneStore(c.Fixtures[ArmF1], store); err != nil {
		return err
	}
	defer os.RemoveAll(store)
	if err := child(CoverageSpec{Arm: ArmF1, Class: ClassXM, Store: store, Out: c.Out, Work: c.Work}, "coverage-f1-XM"); err != nil {
		return err
	}
	if !contains(c.Arms, ArmS) || c.SDNBin == "" {
		logf("coverage XM: no format-4 arm or no %s: the migration is not run", EnvSDNBin)
		return firstErr
	}
	f1Run, err := readRunFile(filepath.Join(c.Out, (&Run{Kind: KindCoverage, Arm: ArmF1, Label: LabelFixture, Class: ClassXM}).FileName()))
	if err != nil {
		return err
	}
	spec := CoverageSpec{Arm: ArmS, Class: ClassXM, Store: store, Out: c.Out, Work: c.Work}
	if v, ok := f1Run.Extra["t0"].(float64); ok {
		spec.T0 = int64(v)
	}
	if ws, ok := f1Run.Extra["writes"].([]any); ok {
		for _, w := range ws {
			if f, ok := w.(float64); ok {
				spec.Writes = append(spec.Writes, int64(f))
			}
		}
	}
	if m, ok := f1Run.Extra["base"].(map[string]any); ok {
		spec.Base = map[string]int64{}
		for k, v := range m {
			if f, ok := v.(float64); ok {
				spec.Base[k] = int64(f)
			}
		}
	}
	// Format 1's record sets, oversized records aside (C-6).
	d1, err := DigestFormat1Limit(store, xmTypes, maxStorableRecord)
	w1 := &Run{Kind: KindWrites, Arm: ArmF1, Format: "", Label: LabelFixture, Class: ClassXM, Started: time.Now().UTC().Format(time.RFC3339),
		Machine: ThisMachine(), LoadStart: Load(), Extra: map[string]any{}}
	if err != nil {
		w1.Extra["digest_error"] = err.Error()
	} else {
		w1.Extra["digest"] = d1
	}
	w1.LoadEnd = Load()
	if _, err := WriteRun(c.Out, w1); err != nil {
		return err
	}
	ws := &Run{Kind: KindWrites, Arm: ArmS, Format: "4", Label: LabelFixture, Class: ClassXM, Started: time.Now().UTC().Format(time.RFC3339),
		Machine: ThisMachine(), LoadStart: Load(), Extra: map[string]any{}}
	var probs []string
	logs := filepath.Join(c.Out, "logs")
	invLog := filepath.Join(logs, "coverage-xm-inventory.log")
	if code, err := migrateVerb(ctx, c.SDNBin, store, invLog, "--inventory"); err != nil {
		probs = append(probs, fmt.Sprintf("store-migrate --to 4 --inventory exited %d: %v", code, err))
	}
	// C-39 U1: XM writes a record of a schema with no standard (X01); the
	// inventory lists its table, the migration refuses to run without
	// --drop-unregistered and changes nothing, then runs naming it.
	var drop []string
	if unregistered, err := inventoryUnregistered(invLog); err != nil {
		probs = append(probs, err.Error())
	} else if len(unregistered) == 0 {
		probs = append(probs, "C-39 U1: the inventory lists no table of a schema with no standard (XM writes one: X01 StoreBatch unknown type)")
	} else {
		ws.Extra["unregistered_tables"] = unregistered
		probs = append(probs, xmRefusal(ctx, c.SDNBin, store, filepath.Join(logs, "coverage-xm-refused.log"))...)
		names := make([]string, 0, len(unregistered))
		for t := range unregistered {
			names = append(names, t)
		}
		sort.Strings(names)
		drop = []string{"--drop-unregistered", strings.Join(names, ",")}
	}
	mr, _, err := migrateRun(ctx, c.SDNBin, store, filepath.Join(logs, "coverage-xm-migrate.log"), 0, drop...)
	ws.Extra["migrate_s"] = mr.Wall.Seconds()
	if err != nil {
		probs = append(probs, err.Error())
	} else {
		probs = append(probs, checkMigrated(store)...)
		if code, err := migrateVerb(ctx, c.SDNBin, store, filepath.Join(logs, "coverage-xm-verify-only.log"), "--verify-only"); err != nil {
			probs = append(probs, fmt.Sprintf("store-migrate --to 4 --verify-only exited %d: %v", code, err))
		}
		if d4, err := digestFormat4(store, xmTypes); err != nil {
			ws.Extra["digest_error"] = err.Error()
		} else {
			ws.Extra["digest"] = d4
		}
	}
	ws.Extra["migrate_problems"] = append([]string{}, probs...)
	ws.LoadEnd = Load()
	if _, err := WriteRun(c.Out, ws); err != nil {
		return err
	}
	if err == nil {
		note(child(spec, "coverage-s-XM"))
	}
	return firstErr
}

// inventoryUnregistered reads store-migrate --to 4 --inventory's
// unregistered_tables (table -> records) from its log.
func inventoryUnregistered(logPath string) (map[string]int64, error) {
	b, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}
	i := strings.IndexByte(string(b), '{')
	if i < 0 {
		return nil, fmt.Errorf("C-39 U1: no inventory in %s", logPath)
	}
	var inv struct {
		Extra struct {
			Unregistered map[string]int64 `json:"unregistered_tables"`
		} `json:"extra"`
	}
	if err := json.NewDecoder(strings.NewReader(string(b[i:]))).Decode(&inv); err != nil {
		return nil, fmt.Errorf("C-39 U1: the inventory in %s: %w", logPath, err)
	}
	return inv.Extra.Unregistered, nil
}

// xmRefusal runs store-migrate --to 4 without --drop-unregistered on a
// store whose inventory lists unregistered tables: it must fail, name the
// flag, and leave the store a format-1 store with no migration journal and
// no fsql4/ (C-39 U1). It returns the problems.
func xmRefusal(ctx context.Context, bin, store, logPath string) []string {
	var probs []string
	if _, _, err := migrateRun(ctx, bin, store, logPath, 0); err == nil {
		return []string{"C-39 U1: store-migrate --to 4 ran without --drop-unregistered on a store holding records of a schema with no standard"}
	}
	if b, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(b), "--drop-unregistered") {
		probs = append(probs, fmt.Sprintf("C-39 U1: the refusal does not name --drop-unregistered (log %s)", logPath))
	}
	if m, err := marker.Read(store); err != nil || m.Format4() || !m.LegacyControlFile {
		probs = append(probs, fmt.Sprintf("C-39 U1: the refused migration changed the store's markers: %+v %v", m, err))
	}
	for _, p := range []string{"fsql4-migrate.json", marker.Dir} {
		if _, err := os.Stat(filepath.Join(store, p)); err == nil {
			probs = append(probs, "C-39 U1: the refused migration wrote "+p)
		}
	}
	return probs
}

// maxStorableRecord is the largest record format 4 stores: the write slot's
// default request area (8 MiB) − 64 KiB (C-6).
const maxStorableRecord = 8<<20 - 64<<10

// migrateVerb runs store-migrate --to 4 <verb> --store store (no kill).
func migrateVerb(ctx context.Context, bin, store, logPath, verb string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return -1, err
	}
	log, err := os.Create(logPath)
	if err != nil {
		return -1, err
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, bin, "store-migrate", "--to", "4", verb, "--store", store)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = envWithout(format2.FormatEnv)
	err = cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return code, err
}

// readRunFile reads one run.
func readRunFile(path string) (*Run, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Run
	return &r, json.Unmarshal(b, &r)
}

// execCoverage is the coverage child.
func execCoverage(spec *ChildSpec) error {
	in, sets, err := LoadInputs(spec.Work)
	if err != nil {
		return err
	}
	bs, err := LoadBenchset(spec.Benchset)
	if err != nil {
		return err
	}
	shapes, sc, err := CoverageShapes(bs, in, sets)
	if err != nil {
		return err
	}
	var mine []Shape
	for _, sh := range shapes {
		if sh.Class == spec.Coverage.Class && (len(sh.Arms) == 0 || contains(sh.Arms, spec.Coverage.Arm)) {
			mine = append(mine, sh)
		}
	}
	if len(mine) == 0 {
		return fmt.Errorf("no coverage shapes for class %s", spec.Coverage.Class)
	}
	cs := *spec.Coverage
	cs.Work = spec.Work
	if err := os.MkdirAll(coverageScratch(cs), 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(coverageScratch(cs))
	scratchDir = coverageScratch(cs)
	_, err = RunCoverage(cs, mine, sc)
	return err
}

// scratchDir is the running coverage class's scratch directory (exports,
// shard files), outside the store.
var scratchDir string

func coverageScratch(spec CoverageSpec) string {
	return spec.Work + string(os.PathSeparator) + "coverage-scratch-" + spec.Arm + "-" + spec.Class
}
