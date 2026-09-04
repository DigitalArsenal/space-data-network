package api

// boundedpersist.go — a boundedReader that survives a restart.
//
// A RESTART MUST NOT BLANK WHAT THE NODE KNEW A MINUTE AGO.
//
// boundedread.go bounds how long an anonymous read waits on the single-writer
// record store, and answers from the last-known-good value when the budget
// expires. That value lives in RAM only, so it is EMPTY on every boot — and
// host-01 spends 60-100 minutes hydrating under the store lock after each
// daemon restart. For that whole window /api/v1/data/index answered
// STORE_BUSY and /api/v1/stats had no numbers: the node "forgot" everything it
// had served a minute earlier, which is exactly the failure the kubo webui bar
// forbids.
//
// This file gives each boundedReader an optional backing file. Successful
// refreshes are written behind the request (debounced, temp-file + rename,
// 0600); construction loads the file and serves those values IMMEDIATELY.
//
// A loaded value is REAL DATA, never a lie — but it is not this boot's data, so
// it is marked fromDisk and every surface renders it exactly the way a
// budget-miss is rendered today: `stale: true` with an `as_of` naming the
// moment it was true. The first successful load of this boot clears the flag.
// A missing or corrupt file is an empty cache and no error: persistence is an
// optimization, never a dependency.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// errUnknownPersistKey drops one entry whose key no longer maps to a known
// value type (a renamed cache key across versions).
var errUnknownPersistKey = errors.New("bounded cache: unknown persisted key")

// boundedPersistVersion is the on-disk envelope version. A file written by a
// different version is ignored (treated as corrupt) rather than migrated: the
// worst case of ignoring it is one cold boot.
const boundedPersistVersion = 1

// boundedPersistMaxKeys caps the file so a parameterized surface cannot grow an
// unbounded artifact on disk across restarts. The newest entries win.
const boundedPersistMaxKeys = 512

// boundedPersistDebounce is how long a successful refresh waits before the file
// is rewritten. The stats lane refreshes every 5 s and the index refreshes per
// distinct query; collapsing a burst into one rewrite keeps this off both the
// hot path and the disk.
const boundedPersistDebounce = 2 * time.Second

// boundedDecodeFunc turns one persisted JSON value back into the CONCRETE type
// the consumer type-asserts. This is the whole reason a decode hook exists:
// encoding/json would hand back map[string]interface{} and every
// `res.Value.(*recordIndexPage)` in data.go would silently yield nil, i.e. an
// empty page — the "confident zero" this feature exists to prevent.
//
// Returning an error drops that one entry; it never fails the load.
type boundedDecodeFunc func(key string, raw json.RawMessage) (interface{}, error)

// boundedPersistEntry is one key's last-known-good value as stored.
type boundedPersistEntry struct {
	Key     string          `json:"key"`
	SavedAt time.Time       `json:"saved_at"`
	Value   json.RawMessage `json:"value"`
}

// boundedPersistFile is the whole file.
type boundedPersistFile struct {
	Version int                   `json:"version"`
	Entries []boundedPersistEntry `json:"entries"`
}

// boundedPersist is one boundedReader's disk backing.
type boundedPersist struct {
	path     string
	decode   boundedDecodeFunc
	debounce time.Duration

	mu    sync.Mutex
	timer *time.Timer
}

// newBoundedReaderPersisted builds a boundedReader backed by file at path.
//
// An empty path yields exactly today's RAM-only reader — that is what keeps
// every existing construction site and test unchanged.
func newBoundedReaderPersisted(maxKeys int, path string, decode boundedDecodeFunc) *boundedReader {
	b := newBoundedReader(maxKeys)
	if path == "" {
		return b
	}
	b.persist = &boundedPersist{path: path, decode: decode, debounce: boundedPersistDebounce}
	b.loadPersisted()
	return b
}

// loadPersisted seeds the cache from disk. Every failure mode — no file, bad
// JSON, wrong version, an entry whose value no longer decodes — degrades to
// "that entry is not cached", never to an error.
func (b *boundedReader) loadPersisted() {
	if b == nil || b.persist == nil {
		return
	}
	raw, err := os.ReadFile(b.persist.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Debugf("bounded cache: cannot read %s, starting cold: %v", b.persist.path, err)
		}
		return
	}
	var file boundedPersistFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != boundedPersistVersion {
		log.Debugf("bounded cache: %s unreadable or wrong version, starting cold", b.persist.path)
		return
	}

	// Newest first, so the reader's own key ceiling drops the oldest.
	entries := append([]boundedPersistEntry(nil), file.Entries...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].SavedAt.After(entries[j].SavedAt) })

	now := time.Now()
	loaded := 0
	b.mu.Lock()
	for _, ent := range entries {
		if ent.Key == "" || len(ent.Value) == 0 || ent.SavedAt.IsZero() {
			continue
		}
		if len(b.entries) >= b.maxKeys || loaded >= boundedPersistMaxKeys {
			break
		}
		var val interface{}
		if b.persist.decode != nil {
			decoded, err := b.persist.decode(ent.Key, ent.Value)
			if err != nil {
				continue
			}
			val = decoded
		} else if err := json.Unmarshal(ent.Value, &val); err != nil {
			continue
		}
		b.entries[ent.Key] = &boundedEntry{
			val:      val,
			at:       ent.SavedAt,
			have:     true,
			fromDisk: true,
			used:     now,
		}
		loaded++
	}
	b.mu.Unlock()
	if loaded > 0 {
		log.Debugf("bounded cache: restored %d cached answers from %s", loaded, b.persist.path)
	}
}

// schedulePersist queues a debounced write-behind. Called after a successful
// load, never on a read path that has to answer a request.
func (b *boundedReader) schedulePersist() {
	if b == nil || b.persist == nil {
		return
	}
	p := b.persist
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		return
	}
	p.timer = time.AfterFunc(p.debounce, func() {
		p.mu.Lock()
		p.timer = nil
		p.mu.Unlock()
		if err := b.flushPersist(); err != nil {
			log.Debugf("bounded cache: write-behind to %s failed: %v", p.path, err)
		}
	})
}

// flushPersist writes the current last-known-good set to disk atomically.
func (b *boundedReader) flushPersist() error {
	if b == nil || b.persist == nil {
		return nil
	}
	file := boundedPersistFile{Version: boundedPersistVersion, Entries: b.persistSnapshot()}
	blob, err := json.Marshal(file)
	if err != nil {
		return err
	}

	dir := filepath.Dir(b.persist.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(b.persist.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(blob); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, b.persist.path)
}

// persistSnapshot copies out every key that holds a value, newest first and
// capped. Values that will not marshal are dropped, not fatal.
func (b *boundedReader) persistSnapshot() []boundedPersistEntry {
	type held struct {
		key string
		val interface{}
		at  time.Time
	}
	b.mu.Lock()
	kept := make([]held, 0, len(b.entries))
	for key, e := range b.entries {
		e.mu.Lock()
		if e.have {
			kept = append(kept, held{key: key, val: e.val, at: e.at})
		}
		e.mu.Unlock()
	}
	b.mu.Unlock()

	sort.SliceStable(kept, func(i, j int) bool { return kept[i].at.After(kept[j].at) })
	if len(kept) > boundedPersistMaxKeys {
		kept = kept[:boundedPersistMaxKeys]
	}

	out := make([]boundedPersistEntry, 0, len(kept))
	for _, h := range kept {
		raw, err := json.Marshal(h.val)
		if err != nil {
			continue
		}
		out = append(out, boundedPersistEntry{Key: h.key, SavedAt: h.at, Value: raw})
	}
	return out
}

// decodeRecordIndexPageValue restores the index cache's concrete page type.
// data.go asserts `res.Value.(*recordIndexPage)`; a generic map would assert to
// nil and render an empty page under a real total.
func decodeRecordIndexPageValue(_ string, raw json.RawMessage) (interface{}, error) {
	var page recordIndexPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// decodeStatsValue restores the /api/v1/stats lane's concrete types, keyed
// by the cache key dashboard_stats.go uses. An unknown key is dropped.
func decodeStatsValue(key string, raw json.RawMessage) (interface{}, error) {
	switch key {
	case statsCacheKeySummary:
		var summary storage.DataSummary
		if err := json.Unmarshal(raw, &summary); err != nil {
			return nil, err
		}
		return &summary, nil
	case statsCacheKeySourceProgress:
		var rows []storage.SourceBatchProgress
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
		return rows, nil
	case statsCacheKeyStorageUsage:
		var used int64
		if err := json.Unmarshal(raw, &used); err != nil {
			return nil, err
		}
		return used, nil
	default:
		return nil, errUnknownPersistKey
	}
}
