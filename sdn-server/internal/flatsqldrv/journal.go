package flatsqldrv

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// StatementJournal is an append-only log of committed mutating SQL
// statements (+ typed params). Replaying it against a fresh engine
// reproduces control-table state deterministically — including SQLite
// rowids, which is what keeps the datasync cursor stable across restarts
// (docs/flatsql-store-v2.md §3).
//
// Frame layout: [u32le totalLen][u32le crc32(payload)][payload] where
// payload = JSON {sql, params:[{t,v}...]}. A torn tail (crash mid-append)
// fails the length/CRC check and is truncated at open.
type StatementJournal struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

type journalFrame struct {
	SQL    string        `json:"sql"`
	Params []interface{} `json:"params,omitempty"`
}

type typedParam struct {
	T string          `json:"t"`
	V json.RawMessage `json:"v,omitempty"`
}

func encodeFrame(fr journalFrame) ([]byte, error) {
	params := make([]typedParam, len(fr.Params))
	for i, p := range fr.Params {
		switch v := p.(type) {
		case nil:
			params[i] = typedParam{T: "null"}
		case bool:
			b, _ := json.Marshal(v)
			params[i] = typedParam{T: "bool", V: b}
		case int64:
			b, _ := json.Marshal(v)
			params[i] = typedParam{T: "i64", V: b}
		case float64:
			b, _ := json.Marshal(v)
			params[i] = typedParam{T: "f64", V: b}
		case string:
			b, _ := json.Marshal(v)
			params[i] = typedParam{T: "str", V: b}
		case []byte:
			b, _ := json.Marshal(v) // base64 via encoding/json
			params[i] = typedParam{T: "bytes", V: b}
		default:
			return nil, fmt.Errorf("flatsqldrv: unjournalable param type %T", p)
		}
	}
	return json.Marshal(struct {
		SQL    string       `json:"sql"`
		Params []typedParam `json:"params,omitempty"`
	}{fr.SQL, params})
}

func decodeFrame(payload []byte) (journalFrame, error) {
	var raw struct {
		SQL    string       `json:"sql"`
		Params []typedParam `json:"params"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return journalFrame{}, err
	}
	fr := journalFrame{SQL: raw.SQL, Params: make([]interface{}, len(raw.Params))}
	for i, p := range raw.Params {
		switch p.T {
		case "null":
			fr.Params[i] = nil
		case "bool":
			var v bool
			if err := json.Unmarshal(p.V, &v); err != nil {
				return journalFrame{}, err
			}
			fr.Params[i] = v
		case "i64":
			var v int64
			if err := json.Unmarshal(p.V, &v); err != nil {
				return journalFrame{}, err
			}
			fr.Params[i] = v
		case "f64":
			var v float64
			if err := json.Unmarshal(p.V, &v); err != nil {
				return journalFrame{}, err
			}
			fr.Params[i] = v
		case "str":
			var v string
			if err := json.Unmarshal(p.V, &v); err != nil {
				return journalFrame{}, err
			}
			fr.Params[i] = v
		case "bytes":
			var v []byte
			if err := json.Unmarshal(p.V, &v); err != nil {
				return journalFrame{}, err
			}
			fr.Params[i] = v
		default:
			return journalFrame{}, fmt.Errorf("flatsqldrv: unknown journal param tag %q", p.T)
		}
	}
	return fr, nil
}

// OpenStatementJournal opens (creating if needed) the journal at path and
// truncates any torn tail frame left by a crash.
func OpenStatementJournal(path string) (*StatementJournal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("flatsqldrv: open journal: %w", err)
	}
	valid, err := scanValidLength(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Truncate(valid); err != nil {
		f.Close()
		return nil, fmt.Errorf("flatsqldrv: truncate torn journal tail: %w", err)
	}
	if _, err := f.Seek(valid, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &StatementJournal{f: f, path: path}, nil
}

func scanValidLength(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	var off int64
	var hdr [8]byte
	for off < size {
		if size-off < 8 {
			break
		}
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			return 0, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		crc := binary.LittleEndian.Uint32(hdr[4:])
		if n == 0 || off+8+n > size {
			break
		}
		payload := make([]byte, n)
		if _, err := f.ReadAt(payload, off+8); err != nil {
			return 0, err
		}
		if crc32.ChecksumIEEE(payload) != crc {
			break
		}
		off += 8 + n
	}
	return off, nil
}

// appendAll writes frames and fsyncs once (one commit point per batch/tx).
func (j *StatementJournal) appendAll(frames []journalFrame) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var buf []byte
	for _, fr := range frames {
		payload, err := encodeFrame(fr)
		if err != nil {
			return err
		}
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)))
		binary.LittleEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(payload))
		buf = append(buf, hdr[:]...)
		buf = append(buf, payload...)
	}
	if _, err := j.f.Write(buf); err != nil {
		return err
	}
	return j.f.Sync()
}

// Close closes the journal file.
func (j *StatementJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}

// Path returns the journal file path.
func (j *StatementJournal) Path() string { return j.path }

// Replay executes every journaled statement, in order, against db —
// the boot-rebuild path for control-table state.
func (j *StatementJournal) Replay(db *flatsqlrt.Database) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	info, err := j.f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	var off int64
	var hdr [8]byte
	count := 0
	for off < size {
		if _, err := j.f.ReadAt(hdr[:], off); err != nil {
			return count, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		payload := make([]byte, n)
		if _, err := j.f.ReadAt(payload, off+8); err != nil {
			return count, err
		}
		fr, err := decodeFrame(payload)
		if err != nil {
			return count, fmt.Errorf("flatsqldrv: journal frame at %d: %w", off, err)
		}
		if _, err := db.Query(fr.SQL, fr.Params...); err != nil {
			return count, fmt.Errorf("flatsqldrv: replay %q: %w", fr.SQL, err)
		}
		count++
		off += 8 + n
	}
	return count, nil
}
