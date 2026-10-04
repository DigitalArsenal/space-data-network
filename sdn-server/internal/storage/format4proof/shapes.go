package format4proof

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// The read shapes: every benchset read R01–R24, as calls into SDN's storage
// functions (the layer the HTTP handlers and modules call). The same closures
// run on every arm; only the store under them differs.

// Measure is what a call returned, for the sample.
type Measure struct {
	Rows, Bytes int64
}

// Call is one storage call of a shape. Run makes the call (the timed part)
// and returns a Result that canonicalizes what it got (untimed), so hashing
// the answer never counts against an arm.
type Call struct {
	Name string
	Run  func(s *storage.FlatSQLStore) Result
	// Write marks a coverage call that changes the store (or prepares a
	// later write): the migration class XM runs only the other calls on the
	// migrated store (coverage.go).
	Write bool
}

// Result builds a call's answer after the timer stops.
type Result func() (Answer, Measure)

func answered(a Answer, m Measure) Result { return func() (Answer, Measure) { return a, m } }

func errResult(call string, err error) Result { return answered(errAnswer(call, err)) }

func recordsResult(call string, recs []*storage.Record, err error) Result {
	return func() (Answer, Measure) { return recordsAnswer(call, recs, err) }
}

func framesResult(call string, st *flatsqlrt.RawStream, err error) Result {
	return func() (Answer, Measure) { return framesAnswer(call, st, err) }
}

// Fixtures a shape runs on.
const (
	FixtureT6W    = "t6w"    // the host-02-sized fixture (every op)
	FixtureH2Copy = "h2copy" // the real host-02 copy (PNM, FTS, many partitions)
)

// Shape is one benchset parameter set.
type Shape struct {
	Class   string
	Name    string
	Schema  string
	Fixture string
	Policy  Policy
	Calls   []Call
	// Arms, when set, are the only arms that run the shape (a baseline
	// phrased for one format).
	Arms []string
	// Setup, when set, runs once after the store opens, untimed (what a
	// call needs from the store before it is timed).
	Setup func(s *storage.FlatSQLStore)
}

// ShapesForArm drops the shapes another arm alone runs.
func ShapesForArm(shapes []Shape, arm string) []Shape {
	var out []Shape
	for _, sh := range shapes {
		if len(sh.Arms) == 0 || contains(sh.Arms, arm) {
			out = append(out, sh)
		}
	}
	return out
}

// Inputs are the fixture-derived lists some shapes need (taken once from a
// format-1 clone by the prepare step; nil fields fall back as documented).
type Inputs struct {
	// B052CIDs: the CIDs of OMM batch OMM-celestrak-gp-b052 in CID order (R04).
	B052CIDs []string `json:"b052_cids,omitempty"`
	// B052Tags: that batch's tag, so a repeat (W03) and a retag (W04) carry
	// exactly the original's provenance.
	B052Tags storage.SourceTags `json:"b052_tags"`
	// R01Held: on a grown store, R01's CIDs per type that the store still
	// holds (the ingest wrote the list, HeldHitsPath); nil on the fixture.
	R01Held map[string][]string `json:"-"`
}

// bp is the union of the benchset reads' parameter keys. The SDS fields carry
// their SDS names (NORAD_CAT_ID, EPOCH); encoding/json matches keys without
// case, so benchset.json's "norad_cat_id" and "epoch" decode into them.
type bp struct {
	Schema          string          `json:"schema"`
	Limit           int             `json:"limit"`
	Offset          int             `json:"offset"`
	Page            int             `json:"page"`
	AfterRowID      int64           `json:"after_rowid"`
	MaxRowID        int64           `json:"max_rowid"`
	SourceName      string          `json:"source_name"`
	ProviderID      string          `json:"provider_id"`
	BatchID         string          `json:"batch_id"`
	SyncFilter      string          `json:"sync_filter"`
	ExpectedTotal   *int64          `json:"expected_total"`
	Norad           string          `json:"norad"`
	NoradCatID      *uint32         `json:"NORAD_CAT_ID"`
	EntityID        string          `json:"entity_id"`
	Day             string          `json:"day"`
	From            string          `json:"from"`
	To              string          `json:"to"`
	Profile         string          `json:"profile"`
	At              int64           `json:"at"`
	Epoch           float64         `json:"EPOCH"`
	Source          string          `json:"source"`
	MaxDeltaSeconds int64           `json:"max_delta_seconds"`
	MaxBytes        int64           `json:"max_bytes"`
	SQL             string          `json:"sql"`
	Want            string          `json:"want"`
	Params          []any           `json:"params"`
	Fn              string          `json:"fn"`
	Peer            string          `json:"peer"`
	Search          string          `json:"search"`
	Fixture         string          `json:"fixture"`
	Pages           int             `json:"pages"`
	Sizes           []int           `json:"sizes"`
	CIDs            json.RawMessage `json:"cids"` // a list (R24) or a description (R04)
}

func decodeParams(op BenchOp) ([]bp, error) {
	var list []bp
	if err := json.Unmarshal(op.Params, &list); err == nil {
		return list, nil
	}
	var one bp
	if err := json.Unmarshal(op.Params, &one); err != nil {
		return nil, fmt.Errorf("%s params: %w", op.ID, err)
	}
	return []bp{one}, nil
}

// hitLists decodes R01/R02's {schema: [cid...]} params.
func hitLists(op BenchOp) (map[string][]string, error) {
	var m map[string][]string
	if err := json.Unmarshal(op.Params, &m); err != nil {
		return nil, fmt.Errorf("%s params: %w", op.ID, err)
	}
	return m, nil
}

var pointSchemas = []string{"OMM.fbs", "MPE.fbs", "CAT.fbs", "IQC.fbs"}

func errAnswer(call string, err error) (Answer, Measure) {
	msg := err.Error()
	if len(msg) > 400 {
		msg = msg[:400]
	}
	return Answer{Call: call, Err: msg}, Measure{}
}

func recordsAnswer(call string, recs []*storage.Record, err error) (Answer, Measure) {
	if err != nil {
		return errAnswer(call, err)
	}
	var m Measure
	for _, r := range recs {
		m.Bytes += int64(len(r.Data))
	}
	m.Rows = int64(len(recs))
	return Answer{Call: call, Rows: RecordRows(recs)}, m
}

func framesAnswer(call string, st *flatsqlrt.RawStream, err error) (Answer, Measure) {
	if err != nil {
		return errAnswer(call, err)
	}
	if st == nil {
		return Answer{Call: call}, Measure{}
	}
	rows, ferr := FrameRows(st.Bytes)
	if ferr != nil {
		return errAnswer(call, ferr)
	}
	return Answer{Call: call, Rows: rows}, Measure{Rows: int64(len(rows)), Bytes: int64(len(st.Bytes))}
}

func parseTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func atUnix(v int64) time.Time { return time.Unix(v, 0).UTC() }

// isNotFound is format 1's miss: GetRecord's "not found: <cid>".
func isNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

// BuildShapes turns the benchset's reads into shapes. inputs may be nil.
func BuildShapes(bs *Benchset, inputs *Inputs) ([]Shape, error) {
	if inputs == nil {
		inputs = &Inputs{}
	}
	var out []Shape
	add := func(sh ...Shape) { out = append(out, sh...) }
	r01, ok := bs.Read("R01")
	if !ok {
		return nil, errors.New("benchset has no R01")
	}
	hits, err := hitLists(r01)
	if err != nil {
		return nil, err
	}
	for _, op := range bs.Reads {
		var sh []Shape
		var err error
		switch op.ID {
		case "R01":
			sh = getShapes(op.ID, hits)
		case "R02":
			var miss map[string][]string
			if miss, err = hitLists(op); err == nil {
				sh = getShapes(op.ID, miss)
			}
		case "R03":
			sh, err = refsShapes(op, hits, inputs)
		case "R04":
			sh, err = tagShapes(op, hits, inputs)
		case "R05":
			sh, err = scanFirstPageShapes(op)
		case "R06", "R08", "R09":
			sh, err = rawRefsShapes(op)
		case "R07":
			sh, err = chainedSourceShapes(op)
		case "R10":
			sh, err = countShapes(op)
		case "R11":
			sh, err = indexPageShapes(op)
		case "R12", "R13", "R14":
			sh, err = windowShapes(op)
		case "R15":
			sh, err = byteProbeShapes(op)
		case "R16":
			sh, err = epochShapes(op)
		case "R17":
			sh, err = a18Shapes(op)
		case "R18":
			sh, err = epochStreamShapes(op)
		case "R19":
			sh, err = sqlShapes(op)
		case "R20":
			sh, err = summaryShapes(op)
		case "R21":
			sh, err = recentShapes(op)
		case "R22":
			sh, err = searchShapes(op)
		case "R23":
			sh, err = tablePageShapes(op)
		case "R24":
			sh, err = h2GetShapes(op)
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op.ID, err)
		}
		for i := range sh {
			if sh[i].Fixture == "" {
				sh[i].Fixture = FixtureT6W
			}
		}
		add(sh...)
	}
	add(zzWindowShapes()...)
	return out, nil
}

// R01, R02: one shape per type, one call per CID (a miss answers "miss").
func getShapes(class string, lists map[string][]string) []Shape {
	var out []Shape
	for _, schema := range pointSchemas {
		cids := lists[schema]
		if len(cids) == 0 {
			continue
		}
		sh := Shape{Class: class, Name: schema, Schema: schema}
		for _, cid := range cids {
			sh.Calls = append(sh.Calls, getCall(schema, cid))
		}
		out = append(out, sh)
	}
	return out
}

func getCall(schema, cid string) Call {
	return Call{Name: cid, Run: func(s *storage.FlatSQLStore) Result {
		r, err := s.GetRecord(schema, cid)
		return func() (Answer, Measure) {
			if isNotFound(err) {
				return Answer{Call: cid, Rows: []Row{ValueRow("miss", "1")}}, Measure{}
			}
			if err != nil {
				return errAnswer(cid, err)
			}
			return Answer{Call: cid, Rows: []Row{RecordRow(r)}}, Measure{Rows: 1, Bytes: int64(len(r.Data))}
		}
	}}
}

func roundRobin(list []string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n && len(list) > 0; i++ {
		out = append(out, list[i%len(list)])
	}
	return out
}

// R03: QueryRawRecordRefsByRefs with R01's CIDs per type, repeated
// round-robin to N, tag fields empty. On a grown store the list is R01's
// CIDs that store still holds (Inputs.R01Held): the ingest supersedes some
// CAT ones, and one ref the store no longer holds fails the whole call on
// every arm, which would time an error.
func refsShapes(op BenchOp, hits map[string][]string, in *Inputs) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	if in.R01Held != nil {
		hits = in.R01Held
	}
	var out []Shape
	for _, size := range ps[0].Sizes {
		for _, schema := range pointSchemas {
			refs := make([]storage.RawRecordRef, 0, size)
			for _, cid := range roundRobin(hits[schema], size) {
				refs = append(refs, storage.RawRecordRef{CID: cid})
			}
			schema, name := schema, fmt.Sprintf("%s refs=%d", schema, size)
			out = append(out, Shape{Class: op.ID, Name: name, Schema: schema, Calls: []Call{{Name: name,
				Run: func(s *storage.FlatSQLStore) Result {
					recs, err := s.QueryRawRecordRefsByRefs(schema, refs)
					return recordsResult(name, recs, err)
				}}}})
		}
	}
	return out, nil
}

// R04: tags for a CID list. The engine's IN-list helper (sourceTagsForCIDs) is
// unexported; its exported callers are measured instead: GetSourceTags over
// the list in one timed call, and ExportDatasetWindow of batch b052 (the
// publication export that dominates host-02's slow-statement log).
func tagShapes(op BenchOp, hits map[string][]string, in *Inputs) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var pool []string
	schemaOf := map[string]string{}
	for _, schema := range pointSchemas {
		pool = append(pool, hits[schema]...)
		for _, c := range hits[schema] {
			schemaOf[c] = schema
		}
	}
	var out []Shape
	for _, size := range ps[0].Sizes {
		list := append([]string(nil), pool...)
		list = append(list, in.B052CIDs...)
		if len(list) > size {
			list = list[:size]
		}
		cids := roundRobin(list, size)
		name := fmt.Sprintf("GetSourceTags x%d", size)
		out = append(out, Shape{Class: op.ID, Name: name, Policy: Policy{}, Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				tags := make([]storage.SourceTags, len(cids))
				found := make([]bool, len(cids))
				for i, cid := range cids {
					schema := schemaOf[cid]
					if schema == "" {
						schema = "OMM.fbs" // b052's CIDs
					}
					t, err := s.GetSourceTags(schema, cid)
					if err != nil && !isNotFound(err) {
						return errResult(name, err)
					}
					tags[i], found[i] = t, err == nil
				}
				return func() (Answer, Measure) {
					rows := make([]Row, 0, len(cids))
					for i, cid := range cids {
						if !found[i] {
							rows = append(rows, ValueRow("cid", cid, "err", "not found"))
							continue
						}
						rows = append(rows, TagsRow(cid, tags[i]))
					}
					return Answer{Call: name, Rows: rows}, Measure{Rows: int64(len(rows))}
				}
			}}}})
	}
	return out, nil
}

// R05: datasync.Scan, first page.
func scanFirstPageShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		req := datasync.QueryRequest{Schema: p.Schema, SourceName: p.SourceName, Limit: p.Limit}
		name := fmt.Sprintf("%s limit=%d", p.Schema, p.Limit)
		out = append(out, Shape{Class: op.ID, Name: name, Schema: p.Schema, Policy: Policy{Collapse: true},
			Calls: []Call{scanCall(name, req, nil)}})
	}
	return out, nil
}

// scanCall runs datasync.Scan; cursor (when set) chains pages: the call
// takes *cursor and stores the next one.
func scanCall(name string, req datasync.QueryRequest, cursor *string) Call {
	return Call{Name: name, Run: func(s *storage.FlatSQLStore) Result {
		r := req
		if cursor != nil {
			r.Cursor = *cursor
		}
		resp, recs, err := datasync.Scan(s, r, datasync.MaxSyncChunkLimit)
		if err != nil {
			return errResult(name, err)
		}
		if cursor != nil {
			*cursor = resp.NextCursor
		}
		return func() (Answer, Measure) { return scanAnswer(name, resp, recs) }
	}}
}

func scanAnswer(name string, resp *datasync.ScanResponse, recs []*storage.Record) (Answer, Measure) {
	a, m := recordsAnswer(name, recs, nil)
	head := ValueRow("total", i64(resp.TotalCount), "count", strconv.Itoa(resp.Count), "next_cursor", resp.NextCursor,
		"snapshot", resp.SnapshotID, "head", resp.Head, "hwm", resp.HighWaterMark, "~scan_hash", resp.ScanHash,
		"~chunk_hash", resp.ChunkHash)
	a.Rows = append([]Row{head}, a.Rows...)
	return a, m
}

// R06, R08, R09: QueryRawRecordRefs on the datasync rowid cursor.
func rawRefsShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		q := storage.RawRecordQuery{SchemaName: p.Schema, ProviderID: p.ProviderID, SourceName: p.SourceName, BatchID: p.BatchID,
			SyncFilter: p.SyncFilter, Limit: p.Limit, UseRowIDCursor: true, AfterRowID: p.AfterRowID, MaxRowID: p.MaxRowID}
		parts := []string{p.Schema}
		for _, kv := range [][2]string{{"after", i64(p.AfterRowID)}, {"filter", p.SyncFilter}, {"batch", p.BatchID}} {
			if kv[1] != "" && kv[1] != "0" {
				parts = append(parts, kv[0]+"="+kv[1])
			}
		}
		parts = append(parts, "limit="+strconv.Itoa(p.Limit))
		name := strings.Join(parts, " ")
		out = append(out, Shape{Class: op.ID, Name: name, Schema: p.Schema, Policy: Policy{Collapse: true}, Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				recs, err := s.QueryRawRecordRefs(q)
				return recordsResult(name, recs, err)
			}}}})
	}
	return out, nil
}

// R07: datasync by source, chained pages; each page is its own call and the
// chain restarts at page 0 every pass.
func chainedSourceShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	p := ps[0]
	after := new(int64)
	sh := Shape{Class: op.ID, Name: fmt.Sprintf("%s@%s %dx%d", p.Schema, p.SourceName, p.Pages, p.Limit), Schema: p.Schema,
		Policy: Policy{Collapse: true}}
	for page := 0; page < p.Pages; page++ {
		page, name := page, fmt.Sprintf("page%d", page)
		sh.Calls = append(sh.Calls, Call{Name: name, Run: func(s *storage.FlatSQLStore) Result {
			if page == 0 {
				*after = 0
			}
			recs, err := s.QueryRawRecordRefs(storage.RawRecordQuery{SchemaName: p.Schema, SourceName: p.SourceName,
				Limit: p.Limit, UseRowIDCursor: true, AfterRowID: *after})
			if err == nil && len(recs) > 0 {
				*after = recs[len(recs)-1].RowID
			}
			return recordsResult(name, recs, err)
		}})
	}
	return []Shape{sh}, nil
}

func headRow(h storage.RawRecordHead) Row {
	return ValueRow("bytes", i64(h.TotalBytes), "max_ts", i64(h.MaxRecordTimestampUnix), "max_updated", i64(h.MaxSourceUpdatedAtUnix),
		"max_created", i64(h.MaxCreatedAtUnix), "max_rowid", i64(h.MaxRowID))
}

// R10: counts and heads.
func countShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		q := storage.RawRecordQuery{SchemaName: p.Schema, SourceName: p.SourceName, BatchID: p.BatchID, SyncFilter: p.SyncFilter}
		base := p.Schema
		for _, kv := range [][2]string{{"source", p.SourceName}, {"batch", p.BatchID}, {"filter", p.SyncFilter}} {
			if kv[1] != "" {
				base += " " + kv[0] + "=" + kv[1]
			}
		}
		// C-10: a tag-filtered count counts records, not format 1's tag rows.
		// Coordinator ruling 2026-10-02 ~05:10: a tag-filtered head with no
		// cursor (every R10 shape) reports format 4's per-type seq as
		// max_rowid, the datasync cursor domain; format 1's per-producer
		// legacy row id is not kept.
		tagged := p.SourceName != "" || p.BatchID != ""
		mk := func(fn string, run func(s *storage.FlatSQLStore) (Row, error)) Shape {
			name := fn + " " + base
			pol := Policy{}
			if tagged {
				pol.Accepted = "C-10: format 4 counts a record once where format 1 counts its matching tag rows; " +
					"ruling 2026-10-02 ~05:10: with a tag filter and no cursor, max_rowid is format 4's per-type seq (format 1's per-producer row id is not kept)"
				pol.AcceptedFields = []string{"n", "max_rowid"}
			}
			return Shape{Class: op.ID, Name: name, Schema: p.Schema, Policy: pol, Calls: []Call{{Name: name,
				Run: func(s *storage.FlatSQLStore) Result {
					row, err := run(s)
					if err != nil {
						return errResult(name, err)
					}
					return answered(Answer{Call: name, Rows: []Row{row}}, Measure{Rows: 1})
				}}}}
		}
		out = append(out,
			mk("CountRawRecords", func(s *storage.FlatSQLStore) (Row, error) {
				n, err := s.CountRawRecords(q)
				return ValueRow("n", i64(n)), err
			}),
			mk("RawRecordHead", func(s *storage.FlatSQLStore) (Row, error) {
				h, err := s.RawRecordHead(q)
				return headRow(h), err
			}),
			mk("RawRecordSnapshot", func(s *storage.FlatSQLStore) (Row, error) {
				n, h, err := s.RawRecordSnapshot(q)
				return append(ValueRow("n", i64(n)), headRow(h)...), err
			}))
		if p.SourceName == "" && p.BatchID == "" && p.SyncFilter == "" {
			schema := p.Schema
			engine := mk("EngineRecordCount", func(s *storage.FlatSQLStore) (Row, error) {
				n, err := s.EngineRecordCount(schema)
				return ValueRow("n", i64(n)), err
			})
			// Format 1 counts its engine hot window (unhydrated: 0); format 4
			// has none and counts the type (contract §5.4: W-m does not run).
			engine.Policy.Accepted = "W-m: format 4 has no engine hot window; EngineRecordCount counts the type"
			engine.Policy.AcceptedFields = []string{"n"}
			out = append(out, engine,
				mk("Count", func(s *storage.FlatSQLStore) (Row, error) {
					n, err := s.Count(schema)
					return ValueRow("n", i64(n)), err
				}))
		}
	}
	return out, nil
}

func i64p(p *int64) string {
	if p == nil {
		return "null"
	}
	return i64(*p)
}

// R11: RecordIndexPage (rows and total).
func indexPageShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		page := p.Page
		if page < 1 {
			page = 1
		}
		q := storage.RecordIndexPageQuery{SchemaName: p.Schema, SourceName: p.SourceName, NoradLike: p.Norad, Limit: p.Limit,
			Offset: (page - 1) * p.Limit}
		name := fmt.Sprintf("%s src=%s norad=%s page=%d", p.Schema, p.SourceName, p.Norad, page)
		out = append(out, indexPageShape(op.ID, name, q))
	}
	return out, nil
}

func indexPageShape(class, name string, q storage.RecordIndexPageQuery) Shape {
	return Shape{Class: class, Name: name, Schema: q.SchemaName, Calls: []Call{{Name: name,
		Run: func(s *storage.FlatSQLStore) Result {
			rows, total, err := s.RecordIndexPage(q)
			if err != nil {
				return errResult(name, err)
			}
			return func() (Answer, Measure) {
				out := []Row{ValueRow("total", i64(total))}
				for _, r := range rows {
					out = append(out, ValueRow("cid", r.CID, "norad", i64p(r.NoradCatID), "epoch", i64p(r.EpochUnix)))
				}
				return Answer{Call: name, Rows: out}, Measure{Rows: int64(len(rows))}
			}
		}}}}
}

// R12, R13, R14: QueryIndexedRecords windows.
func windowShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		from, err := parseTime(p.From)
		if err != nil {
			return nil, err
		}
		to, err := parseTime(p.To)
		if err != nil {
			return nil, err
		}
		q := storage.IndexedRecordQuery{SchemaName: p.Schema, Day: p.Day, NoradCatID: p.NoradCatID, EntityID: p.EntityID,
			From: from, To: to, ProviderID: p.ProviderID, SourceName: p.SourceName, BatchID: p.BatchID, Limit: p.Limit,
			Offset: p.Offset, OrderByCID: op.ID == "R13", AllowLargeResultSet: p.Limit > 1000}
		out = append(out, windowShape(op.ID, windowName(q), q))
	}
	return out, nil
}

func windowName(q storage.IndexedRecordQuery) string {
	parts := []string{q.SchemaName}
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("day", q.Day)
	if q.From != nil {
		add("from", q.From.UTC().Format(time.RFC3339))
	}
	if q.To != nil {
		add("to", q.To.UTC().Format(time.RFC3339))
	}
	if q.NoradCatID != nil {
		add("norad", strconv.FormatUint(uint64(*q.NoradCatID), 10))
	}
	add("entity", q.EntityID)
	add("source", q.SourceName)
	add("batch", q.BatchID)
	if q.OrderByCID {
		add("order", "cid")
	}
	add("off", strconv.Itoa(q.Offset))
	add("lim", strconv.Itoa(q.Limit))
	return strings.Join(parts, " ")
}

func windowShape(class, name string, q storage.IndexedRecordQuery) Shape {
	return Shape{Class: class, Name: name, Schema: q.SchemaName, Calls: []Call{{Name: name,
		Run: func(s *storage.FlatSQLStore) Result {
			recs, err := s.QueryIndexedRecords(q)
			return recordsResult(name, recs, err)
		}}}}
}

// zzWindowShapes are the earlier baselines' type windows (newest first, 100
// rows at three offsets per type; in R12) and source pages (100 rows by
// source at three offsets; in R14), so this harness's numbers continue them.
func zzWindowShapes() []Shape {
	var out []Shape
	for _, schema := range pointSchemas {
		for _, off := range []int{0, 1000, 20000} {
			q := storage.IndexedRecordQuery{SchemaName: schema, Limit: 100, Offset: off}
			out = append(out, windowShape("R12", "type window "+windowName(q), q))
		}
	}
	for _, p := range [][2]string{{"OMM.fbs", "celestrak-gp"}, {"MPE.fbs", "celestrak-gp"}, {"CAT.fbs", "celestrak-satcat"},
		{"CAT.fbs", "celestrak-satcat-csv"}, {"IQC.fbs", "IQEngine"}} {
		offs := []int{0, 1000, 50000}
		if p[0] == "CAT.fbs" {
			offs = []int{0, 1000, 30000}
		}
		for _, off := range offs {
			q := storage.IndexedRecordQuery{SchemaName: p[0], SourceName: p[1], Limit: 100, Offset: off}
			out = append(out, windowShape("R14", "source page "+windowName(q), q))
		}
	}
	for i := range out {
		out[i].Fixture = FixtureT6W
	}
	return out
}

// R15: IndexedRecordWindowLimitForBytes.
func byteProbeShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		q := storage.IndexedRecordQuery{SchemaName: p.Schema, SourceName: p.SourceName, BatchID: p.BatchID}
		maxBytes := p.MaxBytes
		name := fmt.Sprintf("%s source=%s batch=%s max=%d", p.Schema, p.SourceName, p.BatchID, maxBytes)
		out = append(out, Shape{Class: op.ID, Name: name, Schema: p.Schema, Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				n, more, err := s.IndexedRecordWindowLimitForBytes(q, maxBytes)
				if err != nil {
					return errResult(name, err)
				}
				return answered(Answer{Call: name, Rows: []Row{ValueRow("n", strconv.Itoa(n), "more", strconv.FormatBool(more))}}, Measure{Rows: 1})
			}}}})
	}
	return out, nil
}

func matchesAnswer(call string, ms []storage.EpochRecordMatch, count int64) (Answer, Measure) {
	rows := []Row{ValueRow("count", i64(count))}
	var m Measure
	for _, x := range ms {
		r := RecordRow(x.Record)
		r = append(r, Field{"entity", x.EntityKey}, Field{"matched", unixOf(x.MatchedEpoch)}, Field{"requested", unixOf(x.RequestedEpoch)},
			Field{"delta", i64(x.DeltaSeconds)}, Field{"match", x.MatchType})
		rows = append(rows, r)
		if x.Record != nil {
			m.Bytes += int64(len(x.Record.Data))
		}
	}
	m.Rows = int64(len(ms))
	return Answer{Call: call, Rows: rows}, m
}

// R16: epoch profiles, as the /api/v1/data/epoch handler runs them
// (CountEpochRecords then QueryEpochRecords; coverage alone), plus the
// profiles for every object (allObjectsEpoch).
func epochShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	return epochShapesOf(op.ID, append(ps, allObjectsEpoch(ps)...))
}

// AllObjects ends the name of an EPOCH shape that answers every object of
// its type. The gate report lists these (p50 and p99) beside the gated
// shapes rather than as a bar of their own.
const AllObjects = "all objects"

// epochAllObjects is the limit that makes a point profile answer every
// object: the storage layer's cap on an epoch page (epochQueryLimit), above
// the fixture's OMM and MPE object counts. The answer's count row is the
// number of objects.
const epochAllObjects = 250000

// allObjectsEpoch is EPOCH nearest, as_of (on or before) and forward (on or
// after) for every OMM and every MPE object at the benchset's instant
// (owner, 2026-10-01: one index seek per object per partition, equal to
// format 1, fast).
func allObjectsEpoch(ps []bp) []bp {
	var at int64
	for _, p := range ps {
		if p.At != 0 {
			at = p.At
			break
		}
	}
	if at == 0 {
		return nil
	}
	var out []bp
	for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
		for _, profile := range []string{storage.EpochProfileNearest, storage.EpochProfileAsOf, storage.EpochProfileForward} {
			out = append(out, bp{Schema: schema, Profile: profile, At: at, Limit: epochAllObjects})
		}
	}
	return out
}

func epochShapesOf(class string, ps []bp) ([]Shape, error) {
	var out []Shape
	for _, p := range ps {
		from, err := parseTime(p.From)
		if err != nil {
			return nil, err
		}
		to, err := parseTime(p.To)
		if err != nil {
			return nil, err
		}
		q := storage.EpochRecordQuery{SchemaName: p.Schema, Profile: p.Profile, Day: p.Day, From: from, To: to,
			SourceName: p.SourceName, NoradCatID: p.NoradCatID, EntityID: p.EntityID, Limit: p.Limit, MaxDeltaSeconds: p.MaxDeltaSeconds}
		if p.At != 0 {
			q.At = atUnix(p.At)
		}
		parts := []string{p.Schema, p.Profile}
		if p.At != 0 {
			parts = append(parts, "at="+i64(p.At))
		}
		for _, kv := range [][2]string{{"day", p.Day}, {"from", p.From}, {"source", p.SourceName}} {
			if kv[1] != "" {
				parts = append(parts, kv[0]+"="+kv[1])
			}
		}
		if p.NoradCatID != nil {
			parts = append(parts, fmt.Sprintf("norad=%d", *p.NoradCatID))
		}
		if p.MaxDeltaSeconds != 0 {
			parts = append(parts, "max_delta="+i64(p.MaxDeltaSeconds))
		}
		switch {
		case p.Limit == epochAllObjects:
			parts = append(parts, AllObjects)
		case p.Limit != 0:
			parts = append(parts, "limit="+strconv.Itoa(p.Limit))
		}
		name := strings.Join(parts, " ")
		coverage := p.Profile == storage.EpochProfileCoverage
		pol := Policy{}
		if coverage && p.SourceName != "" {
			pol.Collapse = true
		}
		out = append(out, Shape{Class: class, Name: name, Schema: p.Schema, Policy: pol, Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				if coverage {
					bs, err := s.QueryEpochCoverage(q)
					if err != nil {
						return errResult(name, err)
					}
					return func() (Answer, Measure) {
						rows := make([]Row, 0, len(bs))
						for _, b := range bs {
							rows = append(rows, ValueRow("day", b.Day, "n", i64(b.Count), "oldest", unixOf(b.OldestEpoch), "newest", unixOf(b.NewestEpoch)))
						}
						return Answer{Call: name, Rows: rows}, Measure{Rows: int64(len(rows))}
					}
				}
				n, err := s.CountEpochRecords(q)
				if err != nil {
					return errResult(name, err)
				}
				ms, err := s.QueryEpochRecords(q)
				if err != nil {
					return errResult(name, err)
				}
				return func() (Answer, Measure) { return matchesAnswer(name, ms, n) }
			}}}})
	}
	return out, nil
}

// sandboxCaps are the module-facing limits a long query runs under (the
// earlier baselines' 5-minute guard).
var sandboxCaps = flatsqlrt.SandboxCaps{Timeout: 5 * time.Minute}

// c31Accepted names the intended difference of every `<TYPE>@<source>` shape.
const c31Accepted = "C-31: <TYPE>@<source> is the source's newest N records (format 1: the type's newest N, then the source)"

// SameQuestionSuffix names the baseline of a `<TYPE>@<source>` shape: a
// baseline engine answering the SAME question, every call executed. R17
// relations: format 1 by SQL over its control tables (c31SameQuestion).
// R18 epoch streams: formats 1 and 2 through their EPOCH API with the
// source filter (epochSameQuestion). The read gate holds the shape to it
// (coordinator rulings on review 1, item 1, on GATES-r2, item 2, and of
// 2026-10-02 ~10:00); the equivalence driver compares format 4's answer
// with format 1's exactly.
const SameQuestionSuffix = " [same question]"

// R17: A18 TYPE@source relations through the sandbox. The relation has no
// ORDER BY, so frames compare as a multiset; a `<TYPE>@<source>` relation
// compares under C-31 (Policy.Superset) with N = the type's A18 bound, and
// has a format-1 same-question baseline (c31SameQuestion).
func a18Shapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		sql, name := p.SQL, strings.TrimPrefix(p.SQL, "SELECT _data FROM ")
		pol := Policy{Unordered: true}
		if typ, source, ok := strings.Cut(a18Relation(sql), "@"); ok {
			spec, err := format4.TypeSpecFor(typ + ".fbs")
			if err != nil {
				return nil, fmt.Errorf("%s: %w", sql, err)
			}
			pol.Superset, pol.MaxRows, pol.Accepted = true, int(spec.A18Bound), c31Accepted
			same, err := c31SameQuestion(op.ID, name, sql, typ+".fbs", source, int64(spec.A18Bound))
			if err != nil {
				return nil, err
			}
			out = append(out, same)
		}
		out = append(out, Shape{Class: op.ID, Name: name, Policy: pol, Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				st, err := s.QuerySandboxedStream(sql, sandboxCaps)
				return framesResult(name, st, err)
			}}}})
	}
	return out, nil
}

// c31SameQuestion is format 1 answering a `<TYPE>@<source>` relation's own
// question: the type's newest N records (arrival order: the index rowid,
// format 4's seq) that carry a tag of the source and that a producer table
// holds, each record's bytes from its first producer table by name (C-12),
// then the relation's WHERE. The call carries the relation's call name, so
// the equivalence driver pairs it with format 4's answer. Every call runs
// the statement: a per-call parameter keeps the host mirror and the
// engine's response cache from answering a repeat (the relations a warm pass
// times execute too).
func c31SameQuestion(class, name, sql, schema, source string, n int64) (Shape, error) {
	where := strings.TrimSpace(sql[strings.Index(sql, `"`+a18Relation(sql)+`"`)+len(a18Relation(sql))+2:])
	filter, norad := " WHERE ?4 IS NOT NULL", int64(-1)
	if where != "" {
		if _, err := fmt.Sscanf(where, "WHERE NORAD_CAT_ID = %d", &norad); err != nil || where != fmt.Sprintf("WHERE NORAD_CAT_ID = %d", norad) {
			return Shape{}, fmt.Errorf("%s: no format-1 same-question SQL for %q (only a NORAD_CAT_ID equality)", sql, where)
		}
		filter += " AND w.norad_cat_id = ?5"
	}
	var query string
	var setupErr error
	var calls int64
	return Shape{Class: class, Name: name + SameQuestionSuffix, Schema: schema, Fixture: FixtureT6W, Arms: []string{ArmF1},
		Policy: Policy{Unordered: true},
		Setup: func(s *storage.FlatSQLStore) {
			var walk string
			var tables []string
			if walk, tables, setupErr = format1IndexWalk(s, schema); setupErr == nil {
				query = c31Format1SQL(walk, tables) + filter
			}
		},
		Calls: []Call{{Name: name, Run: func(s *storage.FlatSQLStore) Result {
			if setupErr != nil {
				return errResult(name, setupErr)
			}
			calls++
			params := []any{schema, source, n, calls}
			if norad >= 0 {
				params = append(params, norad)
			}
			st, err := s.QueryRawStream(query, params...)
			return framesResult(name, st, err)
		}}}}, nil
}

// SameQuestionA18 is format 1 answering `SELECT _data FROM "<TYPE>@<source>"`
// (no WHERE) by SQL, as c31SameQuestion does, for a probe outside the read
// harness (sds-tb-gen's count-scaled growth steps, format 1 only). prepare
// reads format 1's index walk and producer tables once per opened store
// (untimed); run executes the statement with a per-call parameter, so no
// cache answers a repeat, and returns the frames.
func SameQuestionA18(schema, source string) (prepare func(s *storage.FlatSQLStore) error,
	run func(s *storage.FlatSQLStore) (int64, error), err error) {
	spec, err := format4.TypeSpecFor(schema)
	if err != nil {
		return nil, nil, err
	}
	n := int64(spec.A18Bound)
	var query string
	var calls int64
	prepare = func(s *storage.FlatSQLStore) error {
		walk, tables, err := format1IndexWalk(s, schema)
		if err != nil {
			query = ""
			return err
		}
		query = c31Format1SQL(walk, tables) + " WHERE ?4 IS NOT NULL"
		return nil
	}
	run = func(s *storage.FlatSQLStore) (int64, error) {
		if query == "" {
			return 0, fmt.Errorf("format 1's same-question statement for %s@%s is not prepared", schema, source)
		}
		calls++
		st, err := s.QueryRawStream(query, schema, source, n, calls)
		if err != nil {
			return 0, err
		}
		return int64(st.FrameCount), nil
	}
	return prepare, run, nil
}

// c31Format1SQL selects ?3 newest records of schema ?1 tagged with source ?2
// from format 1's control tables: sdn_record_index walked newest-first by
// rowid (walk: format 1's (schema_name, rowid) full-text scan index when the
// store has one, else the table itself; the planner's choice sorts the whole
// type), the source-name tag index, and the producer tables' cid keys.
func c31Format1SQL(walk string, tables []string) string {
	held := make([]string, len(tables))
	pick := make([]string, len(tables))
	for i, t := range tables {
		held[i] = fmt.Sprintf(`EXISTS (SELECT 1 FROM "%s" h WHERE h.cid = i.cid)`, t)
		pick[i] = fmt.Sprintf(`(SELECT data FROM "%s" WHERE cid = w.cid)`, t)
	}
	data := pick[0]
	if len(pick) > 1 {
		data = "COALESCE(" + strings.Join(pick, ", ") + ")"
	}
	return `SELECT ` + data + ` FROM (SELECT i.cid, i.norad_cat_id FROM sdn_record_index i ` + walk + `
		WHERE i.schema_name = ?1
		  AND EXISTS (SELECT 1 FROM sdn_record_source_tags t WHERE t.schema_name = ?1 AND t.source_name = ?2 AND t.cid = i.cid)
		  AND (` + strings.Join(held, " OR ") + `)
		ORDER BY i.rowid DESC LIMIT ?3) w`
}

// format1IndexWalk reads from format 1's sqlite_master how its index rows
// are walked newest-first (as storage.MigrationSource does: INDEXED BY the
// full-text scan index when the store has it, else NOT INDEXED) and the
// schema's producer tables (sds_p_<token>__<STD>) in name order.
func format1IndexWalk(s *storage.FlatSQLStore, schema string) (string, []string, error) {
	names := func(sql string, args ...any) ([]string, error) {
		st, err := s.QueryRawStream(sql, args...)
		if err != nil {
			return nil, err
		}
		var out []string
		for b := st.Bytes; len(b) > 0; {
			if len(b) < 4 {
				return nil, fmt.Errorf("%d trailing bytes", len(b))
			}
			n := int(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
			if len(b) < 4+n {
				return nil, fmt.Errorf("a frame past the stream end")
			}
			if n > 0 {
				out = append(out, string(b[4:4+n]))
			}
			b = b[4+n:]
		}
		return out, nil
	}
	tables, err := names(`SELECT CAST(name AS BLOB) FROM sqlite_master WHERE type = 'table' AND name GLOB ?1 ORDER BY name`,
		"sds_p_*__"+strings.TrimSuffix(schema, ".fbs"))
	if err != nil {
		return "", nil, fmt.Errorf("list %s producer tables: %w", schema, err)
	}
	if len(tables) == 0 {
		return "", nil, fmt.Errorf("format 1 holds no %s producer table", schema)
	}
	scan, err := names(`SELECT CAST(name AS BLOB) FROM sqlite_master WHERE type = 'index' AND name = 'idx_sdn_record_fts_scan' AND tbl_name = 'sdn_record_index'`)
	if err != nil {
		return "", nil, fmt.Errorf("format 1's index walk: %w", err)
	}
	if len(scan) > 0 {
		return "INDEXED BY idx_sdn_record_fts_scan", tables, nil
	}
	return "NOT INDEXED", tables, nil
}

// a18Relation is the first double-quoted relation a statement names
// ("CAT@celestrak-satcat"), or "".
func a18Relation(sql string) string {
	_, rest, ok := strings.Cut(sql, `"`)
	if !ok {
		return ""
	}
	rel, _, ok := strings.Cut(rest, `"`)
	if !ok {
		return ""
	}
	return rel
}

// R18: the engine epoch stream (module flatsql_epoch_stream). Each shape is
// a `<TYPE>@<source>` question: per object, the record of that source
// nearest the epoch, on or before it (as_of) or on or after it (forward),
// ranked by the type's epoch rule (coordinator ruling 2026-10-02 ~10:00).
// Format 1's and format 2's own epoch stream ranks by
// USER_DEFINED_EPOCH_TIMESTAMP (r18Accepted); their same-question baseline
// is their EPOCH API (epochSameQuestion).
func epochStreamShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		p := p
		name := fmt.Sprintf("OMM.fbs@%s %s epoch=%.0f limit=%d", p.Source, p.Profile, p.Epoch, p.Limit)
		out = append(out, epochSameQuestion(op.ID, name, p), Shape{Class: op.ID, Name: name, Schema: "OMM.fbs",
			Policy: Policy{Unordered: true, Accepted: r18Accepted}, Calls: []Call{{Name: name,
				Run: func(s *storage.FlatSQLStore) Result {
					st, err := s.QueryEpochRawStream("OMM.fbs", p.Source, p.Profile, p.Epoch, p.Limit)
					return framesResult(name, st, err)
				}}}})
	}
	return out, nil
}

// r18Accepted names the intended difference of format 1's own R18 answer
// (coordinator ruling 2026-10-02 ~10:00).
const r18Accepted = "R18: formats 1 and 2 rank their epoch stream by USER_DEFINED_EPOCH_TIMESTAMP (a format-1 quirk); " +
	"format 4 ranks by the type's epoch rule, as every EPOCH read and format 1's EPOCH API do"

// epochSameQuestion is a baseline engine answering an R18 shape's question:
// its EPOCH API (QueryEpochRecords, as GET /api/v1/data/epoch runs it) with
// the source filter, on formats 1 and 2. The answer is the matched records'
// stored bytes, one frame row each, so the equivalence driver holds format
// 4's stream equal to format 1's. The EPOCH API counts whole seconds: the
// benchset's epochs are whole seconds.
func epochSameQuestion(class, name string, p bp) Shape {
	limit := p.Limit
	if limit <= 0 {
		limit = epochAllObjects // the stream's "no limit"
	}
	q := storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: "epoch." + strings.TrimPrefix(p.Profile, "epoch."),
		At: time.Unix(int64(p.Epoch), 0).UTC(), SourceName: p.Source, Limit: limit}
	return Shape{Class: class, Name: name + SameQuestionSuffix, Schema: "OMM.fbs", Fixture: FixtureT6W, Arms: []string{ArmF1, ArmF2},
		Policy: Policy{Unordered: true},
		Calls: []Call{{Name: name, Run: func(s *storage.FlatSQLStore) Result {
			ms, err := s.QueryEpochRecords(q)
			if err != nil {
				return errResult(name, err)
			}
			return func() (Answer, Measure) {
				rows := make([]Row, 0, len(ms))
				var m Measure
				for _, x := range ms {
					if x.Record == nil {
						return errAnswer(name, fmt.Errorf("epoch match %s without its record", x.EntityKey))
					}
					rows = append(rows, frameRow(x.Record.Data))
					m.Bytes += 4 + int64(len(x.Record.Data))
				}
				m.Rows = int64(len(rows))
				return Answer{Call: name, Rows: rows}, m
			}
		}}}}
}

// R19: sandboxed and module SQL. "rows" runs QuerySandboxedJSON, "stream"
// QuerySandboxedStream. A statement without ORDER BY compares unordered.
func sqlShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		sql, params, want := p.SQL, normalizeParams(p.Params), p.Want
		name := sql
		if len(params) > 0 {
			name += fmt.Sprintf(" %v", params)
		}
		pol := Policy{Unordered: !strings.Contains(strings.ToUpper(sql), "ORDER BY")}
		out = append(out, Shape{Class: op.ID, Name: name, Policy: pol, Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				if want == "stream" {
					st, err := s.QuerySandboxedStream(sql, sandboxCaps, params...)
					return framesResult(name, st, err)
				}
				payload, rows, _, err := s.QuerySandboxedJSON(sql, sandboxCaps, params...)
				if err != nil {
					return errResult(name, err)
				}
				return func() (Answer, Measure) {
					rs, err := JSONRows(payload)
					if err != nil {
						return errAnswer(name, err)
					}
					return Answer{Call: name, Rows: rs}, Measure{Rows: int64(rows), Bytes: int64(len(payload))}
				}
			}}}})
	}
	return out, nil
}

// normalizeParams turns JSON numbers that are integers into int64 (a NORAD
// id binds as an integer, as the HTTP handler binds it).
func normalizeParams(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			out[i] = int64(f)
			continue
		}
		out[i] = v
	}
	return out
}

// R20: summaries and storage accounting.
func summaryShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		fn, peer := p.Fn, p.Peer
		name := fn
		if peer != "" {
			name += " " + peer
		}
		pol := Policy{Unordered: true}
		if fn == "DiskUsageBytes" {
			pol.Accepted = "gate 1: format 4 holds the same records in fewer bytes"
			pol.AcceptedFields = []string{"bytes"}
		}
		out = append(out, Shape{Class: op.ID, Name: name, Policy: pol, Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				v, err := summaryFetch(s, fn, peer)
				if err != nil {
					return errResult(name, err)
				}
				return func() (Answer, Measure) {
					rows, err := summaryRows(v)
					if err != nil {
						return errAnswer(name, err)
					}
					return Answer{Call: name, Rows: rows}, Measure{Rows: int64(len(rows))}
				}
			}}}})
	}
	return out, nil
}

// summaryFetch makes a summary call (the timed part).
func summaryFetch(s *storage.FlatSQLStore, fn, peer string) (any, error) {
	switch fn {
	case "DataSummary":
		return s.DataSummary()
	case "SourceBatchProgress":
		return s.SourceBatchProgress()
	case "ProducerSourceProgress":
		return s.ProducerSourceProgress()
	case "SourceRecordCounts":
		return s.SourceRecordCounts()
	case "SchemaDateRanges":
		return s.SchemaDateRanges()
	case "LiveRecordBytes":
		n, err := s.LiveRecordBytes()
		return byteCount(n), err
	case "DiskUsageBytes":
		n, err := s.DiskUsageBytes()
		return byteCount(n), err
	case "PeerStorageBytes":
		n, err := s.PeerStorageBytes(peer)
		return byteCount(n), err
	}
	return nil, fmt.Errorf("unknown summary %q", fn)
}

// byteCount is a summary that is one byte total.
type byteCount int64

// summaryRows canonicalizes a summary.
func summaryRows(v any) ([]Row, error) {
	switch x := v.(type) {
	case *storage.DataSummary:
		if x == nil {
			return nil, nil
		}
		rows := []Row{ValueRow("total_records", i64(x.TotalRecords), "total_bytes", i64(x.TotalBytes))}
		for _, sc := range x.Schemas {
			rows = append(rows, ValueRow("schema", sc.SchemaName, "n", i64(sc.Count), "bytes", i64(sc.TotalBytes)))
		}
		for _, src := range x.Sources {
			b, _ := json.Marshal(src)
			rows = append(rows, append(ValueRow("source_row", src.SchemaName), flatJSON(jsonObject(b))...))
		}
		return rows, nil
	case []storage.SourceBatchProgress, []storage.ProducerSourceProgress:
		return jsonListRows(x, nil)
	case map[string]int64:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rows := make([]Row, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, ValueRow("source", k, "n", i64(x[k])))
		}
		return rows, nil
	case []storage.SchemaDateRange:
		rows := make([]Row, 0, len(x))
		for _, d := range x {
			var o, n string
			if d.OldestEpoch != nil {
				o = unixOf(*d.OldestEpoch)
			}
			if d.NewestEpoch != nil {
				n = unixOf(*d.NewestEpoch)
			}
			rows = append(rows, ValueRow("schema", d.Schema, "n", i64(d.RecordCount), "oldest", o, "newest", n, "bytes", i64(d.TotalBytes)))
		}
		return rows, nil
	case byteCount:
		return []Row{ValueRow("bytes", i64(int64(x)))}, nil
	}
	return nil, fmt.Errorf("summary of type %T", v)
}

func jsonObject(b []byte) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func jsonListRows(v any, err error) ([]Row, error) {
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return JSONRows(b)
}

// R21: recent records.
func recentShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		schema, limit := p.Schema, p.Limit
		name := fmt.Sprintf("%s limit=%d", schema, limit)
		out = append(out, Shape{Class: op.ID, Name: name, Schema: schema, Fixture: fixtureOf(p), Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				recs, err := s.QueryRecentRecords(schema, limit)
				return recordsResult(name, recs, err)
			}}}})
	}
	return out, nil
}

func fixtureOf(p bp) string {
	if p.Fixture == FixtureH2Copy {
		return FixtureH2Copy
	}
	return FixtureT6W
}

// R22: full-text search (h2copy).
func searchShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		q := storage.RawRecordQuery{SchemaName: p.Schema, Search: p.Search, Limit: p.Limit, UseRowIDCursor: true}
		name := fmt.Sprintf("%s search=%s limit=%d", p.Schema, p.Search, p.Limit)
		out = append(out, Shape{Class: op.ID, Name: name, Schema: p.Schema, Fixture: fixtureOf(p), Policy: Policy{Collapse: true},
			Calls: []Call{{Name: name, Run: func(s *storage.FlatSQLStore) Result {
				recs, err := s.QueryRawRecordRefs(q)
				return recordsResult(name, recs, err)
			}}}})
	}
	return out, nil
}

// R23: the admin full-table page (records; the per-table cursor is format
// specific and is not compared).
func tablePageShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, p := range ps {
		q := storage.FullTablePageQuery{SchemaName: p.Schema, Limit: p.Limit}
		name := fmt.Sprintf("%s limit=%d", p.Schema, p.Limit)
		out = append(out, Shape{Class: op.ID, Name: name, Schema: p.Schema, Calls: []Call{{Name: name,
			Run: func(s *storage.FlatSQLStore) Result {
				page, err := s.FullTablePageWithCursor(q)
				return recordsResult(name, page.Records, err)
			}}}})
	}
	return out, nil
}

// R24: many-partition point gets (h2copy, PNM).
func h2GetShapes(op BenchOp) ([]Shape, error) {
	ps, err := decodeParams(op)
	if err != nil {
		return nil, err
	}
	var cids []string
	if err := json.Unmarshal(ps[0].CIDs, &cids); err != nil {
		return nil, fmt.Errorf("R24 cids: %w", err)
	}
	sh := Shape{Class: op.ID, Name: "PNM.fbs", Schema: "PNM.fbs", Fixture: FixtureH2Copy}
	for _, cid := range cids {
		sh.Calls = append(sh.Calls, getCall("PNM.fbs", cid))
	}
	return []Shape{sh}, nil
}

// ClassesOf lists the classes of shapes in benchset order.
func ClassesOf(shapes []Shape) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range shapes {
		if !seen[s.Class] {
			seen[s.Class] = true
			out = append(out, s.Class)
		}
	}
	return out
}

// ShapesOf filters shapes by class and fixture, and by name when only is set.
func ShapesOf(shapes []Shape, class, fixture string, only *regexp.Regexp) []Shape {
	var out []Shape
	for _, s := range shapes {
		if s.Class == class && s.Fixture == fixture && (only == nil || only.MatchString(s.Name)) {
			out = append(out, s)
		}
	}
	return out
}
