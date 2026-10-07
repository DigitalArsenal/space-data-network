package addressbook

// The book lives on the node's own disk, beside its homepage document and
// outside the record store. One file keeps the latest signed revision of
// every entry, tombstones included, so a revoked entry stays revoked.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// entriesFile holds size-prefixed $ABA frames, back to back.
const entriesFile = "entries.aba"

type store struct{ dir string }

// load returns the latest revision of each entry that verifies. A frame that
// does not verify is not an entry, and a torn tail ends the file.
func (s store) load() (map[string]Record, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, entriesFile))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]Record{}
	for len(raw) >= 4 {
		n := int(binary.LittleEndian.Uint32(raw))
		if n <= 0 || n > len(raw)-4 {
			break
		}
		r, err := DecodeFrame(raw[:4+n])
		raw = raw[4+n:]
		if err != nil || r.Verify() != nil {
			continue
		}
		if held, ok := out[r.EntryID]; !ok || r.UpdatedAt > held.UpdatedAt {
			out[r.EntryID] = r
		}
	}
	return out, nil
}

// save replaces the file with records, oldest entry first.
func (s store) save(records map[string]Record) error {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := records[ids[i]], records[ids[j]]
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return a.EntryID < b.EntryID
	})
	var buf bytes.Buffer
	for _, id := range ids {
		buf.Write(records[id].Frame())
	}
	return s.write(entriesFile, buf.Bytes())
}

// write replaces one file atomically, readable by the node's user only.
func (s store) write(name string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.dir, name))
}
