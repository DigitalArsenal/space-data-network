package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// entryCount reports how many keys the reader holds, for the load/eviction
// assertions below.
func (b *boundedReader) entryCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

func int64Ptr(v int64) *int64 { return &v }

// TestBoundedCachePersistRoundTrip is the whole point of the feature: what the
// node served before a restart it serves again on the FIRST request of the next
// boot, marked stale, while the real (slow) load is still running.
func TestBoundedCachePersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")

	want := &recordIndexPage{
		Total: 42,
		Rows: []storage.RecordIndexRow{
			{NoradCatID: int64Ptr(25544), EpochUnix: int64Ptr(1700000000), CID: "cid-a"},
			{CID: "cid-b"},
		},
	}

	first := newBoundedReaderPersisted(8, path, decodeRecordIndexPageValue)
	res := first.read("k", time.Second, 0, func() (interface{}, error) { return want, nil })
	if !res.OK || !res.Fresh {
		t.Fatalf("first read: OK=%v Fresh=%v, want both true", res.OK, res.Fresh)
	}
	if err := first.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cache file mode = %v, want 0600", perm)
	}

	// A fresh instance: the store is busy (the builder never returns within the
	// budget), exactly the post-restart hydration window.
	release := make(chan struct{})
	defer close(release)

	// minRefresh 0 forces the real load to start, so this asserts the restored
	// value is served WHILE a slow builder is still running.
	second := newBoundedReaderPersisted(8, path, decodeRecordIndexPageValue)
	got := second.read("k", 50*time.Millisecond, 0, func() (interface{}, error) {
		<-release
		return &recordIndexPage{Total: 99}, nil
	})

	if !got.OK {
		t.Fatal("restored read: OK=false — a restart blanked the surface (this is the bug)")
	}
	if got.Fresh {
		t.Fatal("restored read: Fresh=true — a pre-restart value must render stale")
	}
	if got.AsOf.IsZero() {
		t.Fatal("restored read: AsOf is zero — a stale answer must say when it was true")
	}
	page, ok := got.Value.(*recordIndexPage)
	if !ok {
		t.Fatalf("restored value is %T, want *recordIndexPage (a generic map would render an empty page)", got.Value)
	}
	if page.Total != 42 || len(page.Rows) != 2 {
		t.Fatalf("restored page = {Total:%d Rows:%d}, want {42 2}", page.Total, len(page.Rows))
	}
	if page.Rows[0].NoradCatID == nil || *page.Rows[0].NoradCatID != 25544 || page.Rows[0].CID != "cid-a" {
		t.Fatalf("restored row 0 = %+v, want norad 25544 / cid-a", page.Rows[0])
	}
	if page.Rows[1].NoradCatID != nil || page.Rows[1].EpochUnix != nil {
		t.Fatalf("restored row 1 = %+v, want nil norad/epoch preserved", page.Rows[1])
	}
}

// TestBoundedCachePersistFreshAfterFirstLoad checks the stale flag CLEARS once
// this boot has actually read the store: a restored value must not be reported
// stale forever.
func TestBoundedCachePersistFreshAfterFirstLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")

	first := newBoundedReaderPersisted(4, path, decodeRecordIndexPageValue)
	first.read("k", time.Second, 0, func() (interface{}, error) { return &recordIndexPage{Total: 1}, nil })
	if err := first.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}

	second := newBoundedReaderPersisted(4, path, decodeRecordIndexPageValue)
	if res := second.read("k", 0, storeReadMinRefresh, func() (interface{}, error) {
		return &recordIndexPage{Total: 7}, nil
	}); res.Fresh {
		t.Fatal("zero-budget read of a restored value reported Fresh")
	}
	// Give the abandoned load time to publish, then read again.
	deadline := time.Now().Add(2 * time.Second)
	for {
		res := second.read("k", time.Second, 0, func() (interface{}, error) {
			return &recordIndexPage{Total: 7}, nil
		})
		if res.Fresh {
			if page, _ := res.Value.(*recordIndexPage); page == nil || page.Total != 7 {
				t.Fatalf("fresh value = %+v, want Total 7", res.Value)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("value stayed marked stale after a successful load of this boot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestBoundedCachePersistCorruptFile: an unreadable file is an empty cache and
// no error. Persistence is an optimization, never a dependency.
func TestBoundedCachePersistCorruptFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"garbage", "\x00\x01\x02not json at all"},
		{"truncated json", `{"version":1,"entries":[{"key":"k","value":`},
		{"wrong version", `{"version":99,"entries":[{"key":"k","saved_at":"2026-08-27T00:00:00Z","value":{"Total":5}}]}`},
		{"undecodable value", `{"version":1,"entries":[{"key":"k","saved_at":"2026-08-27T00:00:00Z","value":"not-a-page"}]}`},
		{"missing saved_at", `{"version":1,"entries":[{"key":"k","value":{"Total":5}}]}`},
		{"empty entries", `{"version":1,"entries":[]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			b := newBoundedReaderPersisted(8, path, decodeRecordIndexPageValue)
			if got := b.entryCount(); got != 0 {
				t.Fatalf("entries = %d, want 0 for a corrupt file", got)
			}
			// Cold: nothing to serve, and the reader still works.
			if res := b.read("k", 0, 0, func() (interface{}, error) { return nil, nil }); res.OK {
				t.Fatal("corrupt file produced a cached answer")
			}
			res := b.read("k", time.Second, 0, func() (interface{}, error) {
				return &recordIndexPage{Total: 3}, nil
			})
			if !res.OK || !res.Fresh {
				t.Fatalf("post-corruption read: OK=%v Fresh=%v, want both true", res.OK, res.Fresh)
			}
		})
	}
}

// TestBoundedCachePersistMissingFile: no file at all is the same clean start.
func TestBoundedCachePersistMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "index.json")
	b := newBoundedReaderPersisted(8, path, decodeRecordIndexPageValue)
	if got := b.entryCount(); got != 0 {
		t.Fatalf("entries = %d, want 0", got)
	}
	b.read("k", time.Second, 0, func() (interface{}, error) { return &recordIndexPage{Total: 1}, nil })
	if err := b.flushPersist(); err != nil {
		t.Fatalf("flushPersist into a missing directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}
}

// TestBoundedCachePersistEvictionCap: neither the file nor the restored set may
// grow without bound, and the NEWEST entries are the ones that survive.
func TestBoundedCachePersistEvictionCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")

	base := time.Now().UTC().Truncate(time.Second)
	total := boundedPersistMaxKeys + 200
	file := boundedPersistFile{Version: boundedPersistVersion}
	for i := 0; i < total; i++ {
		raw, err := json.Marshal(&recordIndexPage{Total: int64(i)})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		file.Entries = append(file.Entries, boundedPersistEntry{
			Key:     "k" + strconv.Itoa(i),
			SavedAt: base.Add(time.Duration(i) * time.Second),
			Value:   raw,
		})
	}
	blob, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("marshal file: %v", err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// A reader whose own key ceiling is above the file cap: the file cap binds.
	wide := newBoundedReaderPersisted(4096, path, decodeRecordIndexPageValue)
	if got := wide.entryCount(); got != boundedPersistMaxKeys {
		t.Fatalf("restored %d entries, want the %d cap", got, boundedPersistMaxKeys)
	}
	// Newest kept, oldest dropped.
	newest := "k" + strconv.Itoa(total-1)
	oldest := "k0"
	if res := wide.read(newest, 0, storeReadMinRefresh, blockedLoad(t)); !res.OK {
		t.Fatalf("newest key %s was dropped", newest)
	}
	if res := wide.read(oldest, 0, storeReadMinRefresh, blockedLoad(t)); res.OK {
		t.Fatalf("oldest key %s survived the cap", oldest)
	}

	// A reader whose own key ceiling is lower: the reader's ceiling binds.
	narrow := newBoundedReaderPersisted(10, path, decodeRecordIndexPageValue)
	if got := narrow.entryCount(); got != 10 {
		t.Fatalf("restored %d entries, want the reader ceiling 10", got)
	}

	// And the file it writes back is capped too.
	if err := wide.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}
	reread, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	var out boundedPersistFile
	if err := json.Unmarshal(reread, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Entries) > boundedPersistMaxKeys {
		t.Fatalf("wrote %d entries, want at most %d", len(out.Entries), boundedPersistMaxKeys)
	}
}

// TestBoundedCachePersistStatsDecode covers the /api/v1/stats lane's two keys:
// both must come back as the CONCRETE types dashboard_stats.go asserts, or the
// dashboard renders zeros over real data.
func TestBoundedCachePersistStatsDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")

	summary := &storage.DataSummary{
		TotalRecords: 10847,
		TotalBytes:   987654,
		Schemas:      []storage.DataSchemaSummary{{SchemaName: "OMM.fbs", Count: 10847, TotalBytes: 987654}},
	}
	progress := []storage.SourceBatchProgress{{
		SchemaName: "OMM.fbs", ProviderID: "celestrak", SourceName: "gp", BatchID: "b1",
		Count: 10847, TotalBytes: 987654, FirstSeenUnix: 1, LastSeenUnix: 2, UpdatedAtUnix: 3,
	}}

	first := newBoundedReaderPersisted(8, path, decodeStatsValue)
	first.readAll(time.Second, 0,
		boundedRequest{Key: statsCacheKeySummary, Load: func() (interface{}, error) { return summary, nil }},
		boundedRequest{Key: statsCacheKeySourceProgress, Load: func() (interface{}, error) { return progress, nil }},
		boundedRequest{Key: "retired_key", Load: func() (interface{}, error) { return map[string]int{"x": 1}, nil }},
	)
	if err := first.flushPersist(); err != nil {
		t.Fatalf("flushPersist: %v", err)
	}

	second := newBoundedReaderPersisted(8, path, decodeStatsValue)
	// The retired key has no decoder and must be dropped, not fail the load.
	if got := second.entryCount(); got != 2 {
		t.Fatalf("restored %d entries, want 2 (the retired key must be dropped)", got)
	}

	res := second.readAll(0, storeReadMinRefresh,
		boundedRequest{Key: statsCacheKeySummary, Load: blockedLoad(t)},
		boundedRequest{Key: statsCacheKeySourceProgress, Load: blockedLoad(t)},
	)

	sres := res[statsCacheKeySummary]
	if !sres.OK || sres.Fresh {
		t.Fatalf("summary: OK=%v Fresh=%v, want restored-and-stale", sres.OK, sres.Fresh)
	}
	gotSummary, ok := sres.Value.(*storage.DataSummary)
	if !ok || gotSummary.TotalRecords != 10847 || len(gotSummary.Schemas) != 1 {
		t.Fatalf("summary restored as %T (%+v), want *storage.DataSummary with 10847 records", sres.Value, sres.Value)
	}

	pres := res[statsCacheKeySourceProgress]
	if !pres.OK || pres.Fresh {
		t.Fatalf("progress: OK=%v Fresh=%v, want restored-and-stale", pres.OK, pres.Fresh)
	}
	gotProgress, ok := pres.Value.([]storage.SourceBatchProgress)
	if !ok || len(gotProgress) != 1 || gotProgress[0].Count != 10847 {
		t.Fatalf("progress restored as %T (%+v), want []storage.SourceBatchProgress", pres.Value, pres.Value)
	}
}

// TestBoundedCacheNoPersistPathStaysRAMOnly: the empty-path construction must
// be byte-for-byte today's behavior — no file, no disk touch.
func TestBoundedCacheNoPersistPathStaysRAMOnly(t *testing.T) {
	dir := t.TempDir()
	b := newBoundedReaderPersisted(8, "", decodeRecordIndexPageValue)
	if b.persist != nil {
		t.Fatal("empty path produced a persistence backing")
	}
	b.read("k", time.Second, 0, func() (interface{}, error) { return &recordIndexPage{Total: 1}, nil })
	if err := b.flushPersist(); err != nil {
		t.Fatalf("flushPersist on a RAM-only reader: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("RAM-only reader wrote %d files", len(entries))
	}
}

// blockedLoad is a loader that must never be waited on: these reads use a zero
// budget, so reaching it would mean the cached answer was not used.
func blockedLoad(t *testing.T) func() (interface{}, error) {
	t.Helper()
	return func() (interface{}, error) {
		time.Sleep(500 * time.Millisecond)
		return nil, nil
	}
}
