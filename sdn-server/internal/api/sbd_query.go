package api

// Small-body field filters on POST /api/v1/data/query (owner 2026-10-13: the
// numbered asteroids "listed on the network (a catalog) and that should be
// available / queryable from the data provider the same way that starlink
// is"). A lane is asked for the way every lane is (schema, provider_id,
// source_name, limit, offset, search); for SBD.fbs the sync_filter may also
// name the small-body record's own fields:
//
//	NUMBER            = != < <= > >= BETWEEN a AND b
//	NAME, DESIGNATION = 'text', LIKE 'pat%' (case-insensitive)
//	ORBIT_CLASS       = 'APO', != 'MBA', IN ('ATE','APO','AMO')
//	NEO, PHA          = true | false
//	ABSOLUTE_MAGNITUDE (or H), DIAMETER (km)
//	                  = != < <= > >= BETWEEN a AND b (a record without the
//	                  value meets no comparison)
//
// clauses joined by AND. The engine indexes none of these fields, so the
// lane is read in sequence order and each record's fields are read from its
// own bytes, by the field ids of the embedded binary schema. The matching
// sequence numbers are kept for a few minutes per (lane, filter), so paging
// through a result does not read the lane again. Pages are in sequence
// (arrival) order; X-SDN-Total-Count carries the match count.

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// sbdOrbitClassNames is the SDS sbdOrbitClass enum (SBD.fbs, SDS 1.248.0),
// in declaration order: the value is the index.
var sbdOrbitClassNames = []string{"UNSPECIFIED", "IEO", "ATE", "APO", "AMO", "MCA", "IMB", "MBA", "OMB", "TJN", "CEN",
	"TNO", "PAA", "HYA", "AST", "ETC", "JFC", "HTC", "CTC", "PAR", "HYP", "COM"}

type sbdClause struct {
	field  string // NUMBER, NAME, DESIGNATION, ORBIT_CLASS, NEO, PHA, ABSOLUTE_MAGNITUDE, DIAMETER
	op     string // = != < <= > >= BETWEEN LIKE IN
	lo, hi float64
	text   string
	like   *regexp.Regexp
	set    map[int]bool
}

type sbdFilter struct {
	clauses []sbdClause
	key     string
}

var (
	sbdBetween     = regexp.MustCompile(`(?is)^\s*([A-Za-z_.]+)\s+BETWEEN\s+(\S+)\s+AND\s+(\S+)\s*$`)
	sbdIn          = regexp.MustCompile(`(?is)^\s*([A-Za-z_.]+)\s+IN\s*\((.*)\)\s*$`)
	sbdLike        = regexp.MustCompile(`(?is)^\s*([A-Za-z_.]+)\s+LIKE\s+'(.*)'\s*$`)
	sbdCompare     = regexp.MustCompile(`(?is)^\s*([A-Za-z_.]+)\s*(=|!=|<>|<=|>=|<|>)\s*(.+?)\s*$`)
	sbdAnd         = regexp.MustCompile(`(?i)\s+AND\s+`)
	sbdOpenBetween = regexp.MustCompile(`(?i)\sBETWEEN\s+\S+\s*$`)
)

func sbdFieldName(raw string) (string, bool) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	switch name {
	case "H":
		return "ABSOLUTE_MAGNITUDE", true
	case "NUMBER", "NAME", "DESIGNATION", "ORBIT_CLASS", "NEO", "PHA", "ABSOLUTE_MAGNITUDE", "DIAMETER":
		return name, true
	}
	return name, false
}

// sbdSplit splits a filter on AND, keeping BETWEEN a AND b whole.
func sbdSplit(text string) []string {
	parts := sbdAnd.Split(strings.TrimSpace(text), -1)
	out := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if sbdOpenBetween.MatchString(p) && i+1 < len(parts) {
			p += " AND " + parts[i+1]
			i++
		}
		out = append(out, p)
	}
	return out
}

func sbdUnquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func sbdClassValue(raw string) (int, error) {
	name := strings.ToUpper(sbdUnquote(raw))
	for i, n := range sbdOrbitClassNames {
		if n == name {
			return i, nil
		}
	}
	if n, err := strconv.Atoi(name); err == nil && n >= 0 && n < len(sbdOrbitClassNames) {
		return n, nil
	}
	return 0, fmt.Errorf("sync_filter ORBIT_CLASS %q is not an SBD orbit class (%s)", raw, strings.Join(sbdOrbitClassNames[1:], ", "))
}

func sbdNumber(field, raw string) (float64, error) {
	v, err := strconv.ParseFloat(sbdUnquote(raw), 64)
	if err != nil || math.IsNaN(v) {
		return 0, fmt.Errorf("sync_filter %s value %q is not a number", field, raw)
	}
	return v, nil
}

func sbdLikePattern(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?is)^")
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// parseSBDFilter reads a sync_filter over small-body fields. ok is false when
// the schema is not SBD.fbs or the filter is empty (the ordinary query path);
// a filter on SBD.fbs naming a field outside this list is refused.
func parseSBDFilter(schemaName, text string) (*sbdFilter, bool, error) {
	if !strings.EqualFold(strings.TrimSpace(schemaName), "SBD.fbs") || strings.TrimSpace(text) == "" {
		return nil, false, nil
	}
	f := &sbdFilter{}
	for _, clause := range sbdSplit(text) {
		c, err := parseSBDClause(clause)
		if err != nil {
			return nil, true, err
		}
		f.clauses = append(f.clauses, c)
	}
	keys := make([]string, 0, len(f.clauses))
	for _, c := range f.clauses {
		set := make([]int, 0, len(c.set))
		for v := range c.set {
			set = append(set, v)
		}
		sort.Ints(set)
		keys = append(keys, fmt.Sprintf("%s|%s|%v|%v|%s|%v", c.field, c.op, c.lo, c.hi, strings.ToLower(c.text), set))
	}
	sort.Strings(keys)
	f.key = strings.Join(keys, "&")
	return f, true, nil
}

func parseSBDClause(clause string) (sbdClause, error) {
	field := func(raw string) (string, error) {
		name, ok := sbdFieldName(raw)
		if !ok {
			return "", fmt.Errorf("sync_filter field %q is not an SBD field (NUMBER, NAME, DESIGNATION, ORBIT_CLASS, NEO, PHA, ABSOLUTE_MAGNITUDE, DIAMETER)", raw)
		}
		return name, nil
	}
	numeric := func(name string) bool { return name == "NUMBER" || name == "ABSOLUTE_MAGNITUDE" || name == "DIAMETER" }
	if m := sbdBetween.FindStringSubmatch(clause); m != nil {
		name, err := field(m[1])
		if err != nil {
			return sbdClause{}, err
		}
		if !numeric(name) {
			return sbdClause{}, fmt.Errorf("sync_filter BETWEEN is not supported for %s", name)
		}
		lo, err := sbdNumber(name, m[2])
		if err != nil {
			return sbdClause{}, err
		}
		hi, err := sbdNumber(name, m[3])
		if err != nil {
			return sbdClause{}, err
		}
		return sbdClause{field: name, op: "BETWEEN", lo: lo, hi: hi}, nil
	}
	if m := sbdIn.FindStringSubmatch(clause); m != nil {
		name, err := field(m[1])
		if err != nil {
			return sbdClause{}, err
		}
		c := sbdClause{field: name, op: "IN", set: map[int]bool{}}
		for _, raw := range strings.Split(m[2], ",") {
			var v int
			switch name {
			case "ORBIT_CLASS":
				v, err = sbdClassValue(raw)
			case "NUMBER":
				var f float64
				f, err = sbdNumber(name, raw)
				v = int(f)
			default:
				err = fmt.Errorf("sync_filter IN is supported for ORBIT_CLASS and NUMBER, not %s", name)
			}
			if err != nil {
				return sbdClause{}, err
			}
			c.set[v] = true
		}
		return c, nil
	}
	if m := sbdLike.FindStringSubmatch(clause); m != nil {
		name, err := field(m[1])
		if err != nil {
			return sbdClause{}, err
		}
		if name != "NAME" && name != "DESIGNATION" {
			return sbdClause{}, fmt.Errorf("sync_filter LIKE is supported for NAME and DESIGNATION, not %s", name)
		}
		return sbdClause{field: name, op: "LIKE", text: m[2], like: sbdLikePattern(m[2])}, nil
	}
	if m := sbdCompare.FindStringSubmatch(clause); m != nil {
		name, err := field(m[1])
		if err != nil {
			return sbdClause{}, err
		}
		op := m[2]
		if op == "<>" {
			op = "!="
		}
		switch {
		case numeric(name):
			v, err := sbdNumber(name, m[3])
			if err != nil {
				return sbdClause{}, err
			}
			return sbdClause{field: name, op: op, lo: v}, nil
		case op != "=" && op != "!=":
			return sbdClause{}, fmt.Errorf("sync_filter operator %s is not supported for %s", op, name)
		case name == "ORBIT_CLASS":
			v, err := sbdClassValue(m[3])
			if err != nil {
				return sbdClause{}, err
			}
			return sbdClause{field: name, op: op, set: map[int]bool{v: true}}, nil
		case name == "NEO" || name == "PHA":
			switch strings.ToLower(sbdUnquote(m[3])) {
			case "true", "1":
				return sbdClause{field: name, op: op, lo: 1}, nil
			case "false", "0":
				return sbdClause{field: name, op: op, lo: 0}, nil
			}
			return sbdClause{}, fmt.Errorf("sync_filter %s value %q is not true or false", name, m[3])
		default:
			return sbdClause{field: name, op: op, text: sbdUnquote(m[3])}, nil
		}
	}
	return sbdClause{}, fmt.Errorf("unsupported sync_filter clause %q", strings.TrimSpace(clause))
}

// sbdSlots are the vtable slots of the filtered fields, from the embedded
// binary schema's field ids.
type sbdSlots struct {
	number, designation, name, orbitClass, neo, pha, h, diameter flatbuffers.VOffsetT
}

var (
	sbdSlotsOnce sync.Once
	sbdSlotsVal  sbdSlots
	sbdSlotsErr  error
)

func loadSBDSlots() (sbdSlots, error) {
	sbdSlotsOnce.Do(func() {
		fields, ok := format2.RootFields("SBD")
		if !ok {
			sbdSlotsErr = errors.New("SBD.fbs has no embedded binary schema")
			return
		}
		ids := map[string]flatbuffers.VOffsetT{}
		for _, f := range fields {
			ids[f.Name] = flatbuffers.VOffsetT(4 + 2*int(f.ID))
		}
		want := []string{"NUMBER", "DESIGNATION", "NAME", "ORBIT_CLASS", "NEO", "PHA", "ABSOLUTE_MAGNITUDE", "DIAMETER"}
		for _, n := range want {
			if _, ok := ids[n]; !ok {
				sbdSlotsErr = fmt.Errorf("SBD.fbs binary schema has no %s", n)
				return
			}
		}
		sbdSlotsVal = sbdSlots{ids["NUMBER"], ids["DESIGNATION"], ids["NAME"], ids["ORBIT_CLASS"], ids["NEO"], ids["PHA"],
			ids["ABSOLUTE_MAGNITUDE"], ids["DIAMETER"]}
	})
	return sbdSlotsVal, sbdSlotsErr
}

// sbdTable opens a stored $SBD record, bare or size-prefixed.
func sbdTable(data []byte) (flatbuffers.Table, bool) {
	switch {
	case len(data) >= 8 && string(data[4:8]) == "$SBD":
		return flatbuffers.Table{Bytes: data, Pos: flatbuffers.GetUOffsetT(data)}, true
	case len(data) >= 12 && string(data[8:12]) == "$SBD":
		return flatbuffers.Table{Bytes: data, Pos: flatbuffers.GetUOffsetT(data[4:]) + 4}, true
	}
	return flatbuffers.Table{}, false
}

func sbdString(t flatbuffers.Table, slot flatbuffers.VOffsetT) string {
	if o := flatbuffers.UOffsetT(t.Offset(slot)); o != 0 {
		return string(t.ByteVector(o + t.Pos))
	}
	return ""
}

func compareFloat(op string, v, lo, hi float64) bool {
	if math.IsNaN(v) {
		return false
	}
	switch op {
	case "=":
		return v == lo
	case "!=":
		return v != lo
	case "<":
		return v < lo
	case "<=":
		return v <= lo
	case ">":
		return v > lo
	case ">=":
		return v >= lo
	case "BETWEEN":
		return v >= lo && v <= hi
	}
	return false
}

// matches reports whether one stored record meets every clause.
func (f *sbdFilter) matches(data []byte, s sbdSlots) bool {
	t, ok := sbdTable(data)
	if !ok {
		return false
	}
	for _, c := range f.clauses {
		switch c.field {
		case "NUMBER":
			n := float64(t.GetUint32Slot(s.number, 0))
			if c.op == "IN" {
				if !c.set[int(n)] {
					return false
				}
			} else if !compareFloat(c.op, n, c.lo, c.hi) {
				return false
			}
		case "ABSOLUTE_MAGNITUDE":
			if !compareFloat(c.op, t.GetFloat64Slot(s.h, math.NaN()), c.lo, c.hi) {
				return false
			}
		case "DIAMETER":
			if !compareFloat(c.op, t.GetFloat64Slot(s.diameter, math.NaN()), c.lo, c.hi) {
				return false
			}
		case "ORBIT_CLASS":
			in := c.set[int(t.GetByteSlot(s.orbitClass, 0))]
			if (c.op == "!=") == in {
				return false
			}
		case "NEO", "PHA":
			slot := s.neo
			if c.field == "PHA" {
				slot = s.pha
			}
			v := 0.0
			if t.GetBoolSlot(slot, false) {
				v = 1
			}
			if (v == c.lo) != (c.op == "=") {
				return false
			}
		case "NAME", "DESIGNATION":
			slot := s.name
			if c.field == "DESIGNATION" {
				slot = s.designation
			}
			v := sbdString(t, slot)
			switch c.op {
			case "LIKE":
				if !c.like.MatchString(v) {
					return false
				}
			case "=":
				if !strings.EqualFold(v, c.text) {
					return false
				}
			case "!=":
				if strings.EqualFold(v, c.text) {
					return false
				}
			}
		}
	}
	return true
}

// The matching sequence numbers of recent (lane, filter) pairs: a page of a
// result already counted reads only its own records.
type sbdMatchSet struct {
	seqs     []int64
	head     storage.RawRecordHead
	computed time.Time
}

const (
	sbdMatchTTL      = 5 * time.Minute
	sbdMatchEntries  = 8
	sbdScanChunk     = 4096
	sbdMaxCachedSeqs = 1 << 21 // 16 MiB of sequence numbers at most per entry
)

var sbdMatches = struct {
	sync.Mutex
	m map[string]*sbdMatchSet
}{m: map[string]*sbdMatchSet{}}

func sbdCacheKey(base storage.RawRecordQuery, f *sbdFilter) string {
	return strings.Join([]string{base.ProviderID, base.SourceName, base.BatchID, base.ProducerPeerID, base.ProducerPublicKey,
		base.PeerID, base.Search, f.key}, "\x00")
}

type sbdStore interface {
	QueryRawRecords(filter storage.RawRecordQuery) ([]*storage.Record, error)
	RawRecordHead(filter storage.RawRecordQuery) (storage.RawRecordHead, error)
}

// querySBD answers one page of a filtered small-body query and the total.
func querySBD(store sbdStore, base storage.RawRecordQuery, f *sbdFilter, offset, limit int) ([]*storage.Record, int, error) {
	slots, err := loadSBDSlots()
	if err != nil {
		return nil, 0, err
	}
	base.SyncFilter, base.Limit, base.Offset = "", 0, 0
	head, err := store.RawRecordHead(base)
	if err != nil {
		return nil, 0, err
	}
	key := sbdCacheKey(base, f)
	sbdMatches.Lock()
	set := sbdMatches.m[key]
	if set != nil && (time.Since(set.computed) > sbdMatchTTL || set.head != head) {
		delete(sbdMatches.m, key)
		set = nil
	}
	sbdMatches.Unlock()
	if set == nil {
		seqs, page, err := scanSBD(store, base, f, slots, offset, limit)
		if err != nil {
			return nil, 0, err
		}
		if len(seqs) <= sbdMaxCachedSeqs {
			sbdMatches.Lock()
			if len(sbdMatches.m) >= sbdMatchEntries {
				var oldest string
				for k, v := range sbdMatches.m {
					if oldest == "" || v.computed.Before(sbdMatches.m[oldest].computed) {
						oldest = k
					}
				}
				delete(sbdMatches.m, oldest)
			}
			sbdMatches.m[key] = &sbdMatchSet{seqs: seqs, head: head, computed: time.Now()}
			sbdMatches.Unlock()
		}
		return page, len(seqs), nil
	}
	total := len(set.seqs)
	if offset >= total || limit <= 0 {
		return nil, total, nil
	}
	want := set.seqs[offset:min(offset+limit, total)]
	page, err := readSBDSeqs(store, base, want)
	return page, total, err
}

// scanSBD reads the lane in sequence order, returning every matching
// sequence number and the records of the [offset, offset+limit) window.
func scanSBD(store sbdStore, base storage.RawRecordQuery, f *sbdFilter, slots sbdSlots, offset, limit int) ([]int64, []*storage.Record, error) {
	q := base
	q.UseRowIDCursor, q.Limit = true, sbdScanChunk
	var seqs []int64
	var page []*storage.Record
	for {
		records, err := store.QueryRawRecords(q)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range records {
			if !f.matches(r.Data, slots) {
				continue
			}
			if n := len(seqs); n >= offset && n < offset+limit {
				page = append(page, r)
			}
			seqs = append(seqs, r.RowID)
		}
		if len(records) < q.Limit {
			return seqs, page, nil
		}
		q.AfterRowID = records[len(records)-1].RowID
	}
}

// readSBDSeqs reads the records of known sequence numbers (ascending).
func readSBDSeqs(store sbdStore, base storage.RawRecordQuery, seqs []int64) ([]*storage.Record, error) {
	want := make(map[int64]bool, len(seqs))
	for _, s := range seqs {
		want[s] = true
	}
	q := base
	q.UseRowIDCursor, q.Limit = true, sbdScanChunk
	q.AfterRowID, q.MaxRowID = seqs[0]-1, seqs[len(seqs)-1]
	out := make([]*storage.Record, 0, len(seqs))
	for len(out) < len(seqs) {
		records, err := store.QueryRawRecords(q)
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			if want[r.RowID] {
				out = append(out, r)
			}
		}
		if len(records) < q.Limit {
			break
		}
		q.AfterRowID = records[len(records)-1].RowID
	}
	return out, nil
}

// serveSBDQuery answers handleRawQuery for a small-body field filter, in the
// same two shapes (the raw FlatBuffer stream with its window headers, or
// JSON rows).
func (h *DataQueryHandler) serveSBDQuery(w http.ResponseWriter, r *http.Request, store sbdStore, schemaName string,
	base storage.RawRecordQuery, f *sbdFilter, offset, limit int, includeData bool) {
	records, total, err := querySBD(store, base, f, offset, limit)
	if err != nil {
		if errors.Is(err, storage.ErrEngineRebuilding) {
			writeEngineRebuilding(w, r)
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set(rawDataTotalCountHeader, strconv.Itoa(total))
	w.Header().Set("X-SDN-Filter-Applied", "sbd-fields-v1")
	if requestedRawFlatBufferStream(r) {
		w.Header().Set(rawDataOffsetHeader, strconv.Itoa(offset))
		w.Header().Set(rawDataLimitHeader, strconv.Itoa(limit))
		w.Header().Set(rawDataSourceRunsHeader, encodeRawRecordSourceRuns(records))
		writeFlatBufferPayloadStreamWithContentType(w, schemaName, recordsToPayloads(records), rawFlatBufferStreamContentType)
		return
	}
	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		results = append(results, rawRecordRow(schemaName, rec, includeData))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema":  schemaName,
		"count":   len(results),
		"total":   total,
		"offset":  offset,
		"results": results,
	})
}
