package format4proof

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Answers: what a read returned, canonicalized so two arms can be compared
// field by field. A field whose name starts with "~" is a copy variant
// (contract C-12: peer, signature and source timestamp may come from any one
// of format 1's copies of a CID); every other field must be identical.

// Field is one named value of a row.
type Field struct {
	N string `json:"n"`
	V string `json:"v"`
}

// Row is one canonical row: an ordered list of fields.
type Row []Field

// Get returns the value of field n ("" when absent).
func (r Row) Get(n string) string {
	for _, f := range r {
		if f.N == n {
			return f.V
		}
	}
	return ""
}

func (r Row) text() string {
	var b strings.Builder
	for _, f := range r {
		b.WriteString(f.N)
		b.WriteByte('=')
		b.WriteString(f.V)
		b.WriteByte('\x1f')
	}
	return b.String()
}

// Answer is the canonical result of one call.
type Answer struct {
	Call string `json:"call"`
	Rows []Row  `json:"rows,omitempty"`
	Err  string `json:"err,omitempty"`
}

// Hash is a digest of the rows (and error), for the warm-pass determinism check.
func (a Answer) Hash() string {
	h := sha256.New()
	for _, r := range a.Rows {
		h.Write([]byte(r.text()))
		h.Write([]byte{'\n'})
	}
	h.Write([]byte(a.Err))
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// ShapeAnswers is every call of one shape on one arm (from the cold pass).
type ShapeAnswers struct {
	Class  string   `json:"class"`
	Shape  string   `json:"shape"`
	Schema string   `json:"schema,omitempty"`
	Policy Policy   `json:"policy"`
	Calls  []Answer `json:"calls"`
	// Unstable names calls whose warm answers differed from the cold one.
	Unstable []string `json:"unstable,omitempty"`
}

// AnswerFile holds an arm's answers for one class.
type AnswerFile struct {
	Arm    string         `json:"arm"`
	Label  string         `json:"label"`
	Class  string         `json:"class"`
	Shapes []ShapeAnswers `json:"shapes"`
}

// AnswerFileName is where an arm's answers for a class are written.
func AnswerFileName(arm, label, class string) string {
	return safeName("answers-"+arm+"-"+label+"-"+class) + ".json.gz"
}

// WriteAnswers writes f into dir, gzipped.
func WriteAnswers(dir string, f *AnswerFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out, err := os.Create(dir + string(os.PathSeparator) + AnswerFileName(f.Arm, f.Label, f.Class))
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	if err := json.NewEncoder(zw).Encode(f); err != nil {
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ReadAnswers loads an answer file (nil, nil when it does not exist).
func ReadAnswers(dir, arm, label, class string) (*AnswerFile, error) {
	in, err := os.Open(dir + string(os.PathSeparator) + AnswerFileName(arm, label, class))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return nil, err
	}
	var f AnswerFile
	if err := json.NewDecoder(zr).Decode(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

// --- canonical encoders --------------------------------------------------

func digest(b []byte) string {
	if b == nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:16])
}

func unixOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.Unix(), 10)
}

func i64(v int64) string { return strconv.FormatInt(v, 10) }

// RecordRow canonicalizes a record: its identity and bytes, its datasync
// cursor (RowID), its provenance (the tag fields and the materialized time,
// format 1's created_at) and, as copy variants, the peer, signature and source
// timestamp of the copy served.
func RecordRow(r *storage.Record) Row {
	if r == nil {
		return Row{{"record", "nil"}}
	}
	t := r.SourceTags
	row := Row{
		{"cid", r.CID},
		{"len", strconv.Itoa(len(r.Data))},
		{"data", digest(r.Data)},
		{"rlen", i64(r.RecordLength)},
		{"rowid", i64(r.RowID)},
		{"provider", t.ProviderID},
		{"source", t.SourceName},
		{"url", t.SourceURL},
		{"batch", t.BatchID},
		{"ckey", t.ContentKeyID},
		{"ppeer", t.ProducerPeerID},
		{"ppk", t.ProducerPublicKey},
		{"license", t.License},
		{"license_url", t.LicenseURL},
		{"citation", t.Citation},
		{"share_alike", strconv.FormatBool(t.ShareAlike)},
		{"materialized", unixOf(r.MaterializedAt)},
		{"~peer", r.PeerID},
		{"~ts", unixOf(r.Timestamp)},
		{"~sig", digest(r.Signature)},
	}
	return row
}

// RecordRows canonicalizes a record list in order.
func RecordRows(recs []*storage.Record) []Row {
	out := make([]Row, 0, len(recs))
	for _, r := range recs {
		out = append(out, RecordRow(r))
	}
	return out
}

// TagsRow canonicalizes one SourceTags value.
func TagsRow(cid string, t storage.SourceTags) Row {
	return Row{{"cid", cid}, {"provider", t.ProviderID}, {"source", t.SourceName}, {"url", t.SourceURL},
		{"batch", t.BatchID}, {"ckey", t.ContentKeyID}, {"ppeer", t.ProducerPeerID}, {"ppk", t.ProducerPublicKey}}
}

// FrameRows splits a size-prefixed frame stream ([u32le size][bytes]...) into
// one row per record frame (frameRow). A zero-length frame is alignment
// padding, not a record (flatsqlrt.RawStream.FrameCount skips it too).
func FrameRows(b []byte) ([]Row, error) {
	var out []Row
	for i := 0; len(b) > 0; i++ {
		if len(b) < 4 {
			return out, fmt.Errorf("frame %d: %d trailing bytes", i, len(b))
		}
		n := int(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
		if len(b) < 4+n {
			return out, fmt.Errorf("frame %d: size %d past the stream end", i, n)
		}
		if n > 0 {
			out = append(out, frameRow(b[4:4+n]))
		}
		b = b[4+n:]
	}
	return out, nil
}

// frameRow is one record's bytes as a row: their length and digest.
func frameRow(b []byte) Row { return Row{{"len", strconv.Itoa(len(b))}, {"frame", digest(b)}} }

// ValueRow is a row of named scalar values.
func ValueRow(kv ...string) Row {
	r := make(Row, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		r = append(r, Field{kv[i], kv[i+1]})
	}
	return r
}

// JSONRows canonicalizes a JSON payload of rows (an array of objects or of
// arrays) into one row per element, keys sorted.
func JSONRows(payload []byte) ([]Row, error) {
	var arr []any
	if err := json.Unmarshal(payload, &arr); err != nil {
		var obj map[string]any
		if err2 := json.Unmarshal(payload, &obj); err2 != nil {
			return nil, err
		}
		for _, k := range []string{"rows", "results", "data"} {
			if v, ok := obj[k].([]any); ok {
				arr = v
				break
			}
		}
		if arr == nil {
			return []Row{flatJSON(obj)}, nil
		}
	}
	out := make([]Row, 0, len(arr))
	for _, e := range arr {
		out = append(out, flatJSON(e))
	}
	return out, nil
}

func flatJSON(v any) Row {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		r := make(Row, 0, len(keys))
		for _, k := range keys {
			b, _ := json.Marshal(x[k])
			r = append(r, Field{k, string(b)})
		}
		return r
	case []any:
		r := make(Row, 0, len(x))
		for i, e := range x {
			b, _ := json.Marshal(e)
			r = append(r, Field{strconv.Itoa(i), string(b)})
		}
		return r
	}
	b, _ := json.Marshal(v)
	return Row{{"value", string(b)}}
}
