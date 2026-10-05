package format4proof

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// The owner's layout (contract C-37, C-38; BRIEF4): per source feed x
// standard, the records in a pure FlatBuffer stream P/<TYPE>/<provider@source>.fsdata
// (a compaction's generation is <name>.<gen>.fsdata) and their index in the
// SQLite file P/<TYPE>/<provider@source>.db; untagged records in
// P/<TYPE>/local.fsdata and local.db; no row holds a provider or source
// string (the file is the feed) nor the record's bytes (an index row names
// its frame by off and len); each feed file has exactly one CID index, and
// the type index holds no per-record entry. FeedLayout checks a migrated
// store against the format-1 store it came from: the feed files of each type
// are exactly the (provider, source) pairs format 1's source summary holds
// live records of (plus local), each with a non-empty stream; no table of
// any feed file has a provider or source column; the record table has off
// and len and no record-bytes column d; one index of each feed file covers
// the CID, on the CID alone; no table of a type index T/<TYPE>.idx has a CID
// column.

// FeedLayout is a store's feed files and the problems found.
type FeedLayout struct {
	Files      map[string][]string `json:"files"`       // type -> feed file names
	Streams    map[string][]string `json:"streams"`     // type -> stream file names (<feed>.fsdata, <feed>.<gen>.fsdata)
	Columns    map[string][]string `json:"columns"`     // table -> columns, as one feed file holds them
	CIDIndexes map[string][]string `json:"cid_indexes"` // feed file -> its indexes on a cid column (name: columns)
	TypeIndex  map[string][]string `json:"type_index"`  // type -> the tables of T/<TYPE>.idx
	Problems   []string            `json:"problems,omitempty"`
}

// feedFileName is the engine's file name of a feed (store.cpp
// feedFileName): provider and source, URL-escaped outside [A-Za-z0-9._-],
// joined by '@' (local for neither). A name longer than 160 bytes keeps its
// first 140, and a long, case-colliding or dot-led name carries "~<feed id>"
// (feedIDSuffix), which a file name is compared without.
func feedFileName(provider, source string) string {
	if provider == "" && source == "" {
		return "local"
	}
	enc := func(s string) string {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
				b.WriteByte(c)
			default:
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
		return b.String()
	}
	name := enc(provider) + "@" + enc(source)
	if len(name) > 160 {
		name = name[:140]
	}
	return name
}

// feedIDSuffix is the "~<feed id>" a long or case-colliding name carries.
var feedIDSuffix = regexp.MustCompile(`~[0-9]+$`)

// format1Feeds are the (provider, source) feeds of each type with live
// records in format 1's source summary, as feed file names.
func format1Feeds(f1Store string) (map[string]map[string]bool, error) {
	src, err := storage.OpenMigrationSource(f1Store)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	rows, err := src.SourceSummaries()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, r := range rows {
		if r.Count <= 0 {
			continue
		}
		typ := strings.TrimSuffix(r.Schema, ".fbs")
		if out[typ] == nil {
			out[typ] = map[string]bool{}
		}
		out[typ][feedFileName(r.ProviderID, r.SourceName)] = true
	}
	return out, nil
}

// CheckFeedLayout checks the feed files of the format-4 store f4Store
// against the format-1 store f1Store (a clone; it is opened).
func CheckFeedLayout(f1Store, f4Store, work string) (*FeedLayout, error) {
	want, err := format1Feeds(f1Store)
	if err != nil {
		return nil, fmt.Errorf("format 1 feeds: %w", err)
	}
	return checkFeedFiles(want, f4Store, work)
}

// checkFeedFiles checks the feed files of the format-4 store f4Store against
// want (type -> feed file names, feedFileName): every wanted feed has its
// file, every file names a wanted feed (or local), and the schema rules of
// C-37 and C-38 hold in every file.
func checkFeedFiles(want map[string]map[string]bool, f4Store, work string) (*FeedLayout, error) {
	lay := &FeedLayout{Files: map[string][]string{}, Streams: map[string][]string{}, Columns: map[string][]string{},
		CIDIndexes: map[string][]string{}, TypeIndex: map[string][]string{}}
	fail := func(f string, a ...any) { lay.Problems = append(lay.Problems, fmt.Sprintf(f, a...)) }
	root := filepath.Join(f4Store, marker.Dir, "P")
	types, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	have := map[string]map[string]bool{}
	var files []string
	for _, t := range types {
		if !t.IsDir() {
			fail("P/%s is not a type directory", t.Name())
			continue
		}
		ents, err := os.ReadDir(filepath.Join(root, t.Name()))
		if err != nil {
			return nil, err
		}
		have[t.Name()] = map[string]bool{}
		dbs := map[string]bool{}
		for _, e := range ents {
			if feed, ok := strings.CutSuffix(e.Name(), ".db"); ok && !e.IsDir() {
				dbs[feed] = true
			}
		}
		streamBytes := map[string]int64{} // feed file name -> its streams' bytes
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() {
				// A '/' of a provider or source reached the file system.
				fail("P/%s/%s is a directory: a feed file name is one path element", t.Name(), name)
				continue
			}
			if feed, ok := streamFeed(name, dbs); ok {
				lay.Streams[t.Name()] = append(lay.Streams[t.Name()], name)
				if fi, err := e.Info(); err == nil {
					streamBytes[feed] += fi.Size()
				}
				continue
			}
			feed, ok := strings.CutSuffix(name, ".db")
			if !ok {
				if strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm") || strings.HasSuffix(name, ".db-journal") {
					continue
				}
				fail("P/%s/%s is not a feed file", t.Name(), name)
				continue
			}
			if fi, err := e.Info(); err == nil && fi.Size() == 0 {
				// The engine created and measures this name, and SQLite wrote
				// another (a URI decoded the name's escapes).
				fail("P/%s/%s is empty: no SQLite database was written at the feed's file name", t.Name(), name)
			}
			lay.Files[t.Name()] = append(lay.Files[t.Name()], feed)
			have[t.Name()][feedIDSuffix.ReplaceAllString(feed, "")] = true
			files = append(files, filepath.Join(root, t.Name(), name))
		}
		sort.Strings(lay.Files[t.Name()])
		sort.Strings(lay.Streams[t.Name()])
		for _, feed := range lay.Files[t.Name()] {
			if want[t.Name()][feedIDSuffix.ReplaceAllString(feed, "")] && streamBytes[feed] == 0 {
				fail("P/%s/%s.fsdata is missing or empty, and the feed holds records", t.Name(), feed)
			}
		}
	}
	for typ, feeds := range want {
		for feed := range feeds {
			if !have[typ][feed] {
				fail("%s: feed %s holds records; format 4 has no file P/%s/%s.db", typ, feed, typ, feed)
			}
		}
	}
	for typ, feeds := range have {
		for feed := range feeds {
			if feed != "local" && !want[typ][feed] {
				fail("%s: feed file %s.db names no feed that holds records", typ, feed)
			}
		}
	}
	// Every table of every feed file: no provider or source column.
	tmp := filepath.Join(work, "layout-check")
	defer os.RemoveAll(tmp)
	for _, f := range files {
		cols, err := tableColumns(f, tmp)
		if err != nil {
			fail("%s: %v", strings.TrimPrefix(f, root+"/"), err)
			continue
		}
		for table, cs := range cols {
			if _, seen := lay.Columns[table]; !seen {
				lay.Columns[table] = cs
			}
			for _, c := range cs {
				switch strings.ToLower(c) {
				case "provider", "source", "provider_id", "source_name":
					fail("%s: table %s has a %s column", strings.TrimPrefix(f, root+"/"), table, c)
				}
			}
		}
		// BRIEF4: the record's bytes are in the stream; its row names the
		// frame (off, len) and holds no record bytes.
		if r := cols["r"]; !contains(r, "off") || !contains(r, "len") || contains(r, "d") {
			fail("%s: table r is %v; an index row names its frame by off and len and holds no record bytes (d)", strings.TrimPrefix(f, root+"/"), r)
		}
		// C-38 (1): the feed file's one CID index, on the CID alone, or
		// (C-45 (3)) on cp, the generated first 8 bytes of the CID.
		rel := strings.TrimPrefix(f, root+"/")
		idx, err := cidIndexes(f, tmp)
		if err != nil {
			fail("%s: %v", rel, err)
			continue
		}
		lay.CIDIndexes[rel] = idx
		if len(idx) != 1 || !(strings.HasSuffix(idx[0], ": cid") || strings.HasSuffix(idx[0], ": cp")) {
			fail("%s: the CID indexes are %v; C-38 keeps exactly one, on the CID alone (C-45 (3): or on its prefix cp)", rel, idx)
		}
	}
	// C-38 (2): the type index holds no per-record entry (no CID column).
	for typ := range have {
		idxFile := filepath.Join(f4Store, marker.Dir, "T", typ+".idx")
		if _, err := os.Stat(idxFile); err != nil {
			fail("%s: no type index (%v)", typ, err)
			continue
		}
		cols, err := tableColumns(idxFile, tmp)
		if err != nil {
			fail("T/%s.idx: %v", typ, err)
			continue
		}
		for table, cs := range cols {
			lay.TypeIndex[typ] = append(lay.TypeIndex[typ], table)
			for _, c := range cs {
				if strings.EqualFold(c, "cid") {
					fail("T/%s.idx: table %s has a cid column (C-38: no per-record entry in the type index)", typ, table)
				}
			}
		}
		sort.Strings(lay.TypeIndex[typ])
	}
	return lay, nil
}

// cidIndexes lists a feed file's indexes that cover a cid column, or cp
// when r defines cp as the CID's first 8 bytes (C-45 (3)), each as
// "name: col, col", read by the system sqlite3 from a clone of the file.
func cidIndexes(file, tmp string) ([]string, error) {
	if err := cloneForRead(file, tmp); err != nil {
		return nil, err
	}
	out, err := exec.Command("sqlite3", filepath.Join(tmp, "feed.db"),
		"SELECT i.name, group_concat(ii.name, ', ') FROM sqlite_master m, pragma_index_list(m.name) i, pragma_index_info(i.name) ii "+
			"WHERE m.type = 'table' GROUP BY i.name HAVING sum(ii.name = 'cid' OR (ii.name = 'cp' AND "+
			"replace(lower(m.sql), ' ', '') LIKE '%cpblobgeneratedalwaysas(substr(cid,1,8))%')) > 0 ORDER BY i.name").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("sqlite3: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var idx []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name, cols, ok := strings.Cut(line, "|"); ok {
			idx = append(idx, name+": "+cols)
		}
	}
	return idx, nil
}

// tableColumns lists each table's columns of a feed file, read by the
// system sqlite3 from a clone of the file (and its WAL): the store is not touched.
func tableColumns(file, tmp string) (map[string][]string, error) {
	if err := cloneForRead(file, tmp); err != nil {
		return nil, err
	}
	dst := filepath.Join(tmp, "feed.db")
	out, err := exec.Command("sqlite3", dst,
		"SELECT m.name || '|' || p.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' ORDER BY m.name, p.cid").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("sqlite3: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cols := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if table, col, ok := strings.Cut(line, "|"); ok {
			cols[table] = append(cols[table], col)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no tables")
	}
	return cols, nil
}

// cloneForRead clones a store file (and its WAL) to tmp/feed.db for the
// system sqlite3: the store is not touched.
func cloneForRead(file, tmp string) error {
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(tmp, "feed.db")
	for _, sfx := range []string{"", "-wal"} {
		if _, err := os.Stat(file + sfx); err == nil {
			if out, err := exec.Command("cp", "-c", file+sfx, dst+sfx).CombinedOutput(); err != nil {
				if out2, err2 := exec.Command("cp", file+sfx, dst+sfx).CombinedOutput(); err2 != nil {
					return fmt.Errorf("copy: %v %s %s", err2, out, out2)
				}
			}
		}
	}
	return nil
}

// feedDirProblems checks one type directory P/<typ> of a format-4 store: it
// holds no directory and no file but the feed files of feeds (feedFileName
// names; "~<feed id>" aside), their SQLite sidecars and their streams, and
// the index and stream of each feed in nonEmpty exist and are not empty (a
// feed's records are in the files the engine named, not in ones a URI
// decoded the name to).
func feedDirProblems(store, typ string, feeds, nonEmpty []string) []string {
	var out []string
	dir := filepath.Join(store, marker.Dir, "P", typ)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return []string{fmt.Sprintf("P/%s: %v", typ, err)}
	}
	dbs := map[string]bool{}
	for _, e := range ents {
		if feed, ok := strings.CutSuffix(e.Name(), ".db"); ok && !e.IsDir() {
			dbs[feed] = true
		}
	}
	size, stream := map[string]int64{}, map[string]int64{}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			out = append(out, fmt.Sprintf("P/%s/%s is a directory: a feed file name is one path element", typ, name))
			continue
		}
		if file, ok := streamFeed(name, dbs); ok {
			if feed := feedIDSuffix.ReplaceAllString(file, ""); contains(feeds, feed) {
				if fi, err := e.Info(); err == nil {
					stream[feed] += fi.Size()
				}
				continue
			}
		}
		base := name
		for _, sfx := range []string{"-wal", "-shm", "-journal"} {
			base = strings.TrimSuffix(base, sfx)
		}
		feed, ok := strings.CutSuffix(base, ".db")
		if feed = feedIDSuffix.ReplaceAllString(feed, ""); !ok || !contains(feeds, feed) {
			out = append(out, fmt.Sprintf("P/%s/%s names no feed of this store", typ, name))
			continue
		}
		if fi, err := e.Info(); err == nil && !strings.HasSuffix(name, "-shm") {
			size[feed] += fi.Size()
		}
	}
	for _, f := range nonEmpty {
		if n, ok := size[f]; !ok || n == 0 {
			out = append(out, fmt.Sprintf("P/%s/%s.db is missing or empty, and the feed holds records", typ, f))
		}
		if stream[f] == 0 {
			out = append(out, fmt.Sprintf("P/%s/%s.fsdata is missing or empty, and the feed holds records", typ, f))
		}
	}
	return out
}

// streamFeed is the feed file name (dbs: the type directory's <feed>.db
// names, without ".db") whose stream name is: <feed>.fsdata, or a
// compaction's generation <feed>.<gen>.fsdata.
func streamFeed(name string, dbs map[string]bool) (string, bool) {
	base, ok := strings.CutSuffix(name, ".fsdata")
	if !ok {
		return "", false
	}
	if dbs[base] {
		return base, true
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 && dbs[base[:i]] && strings.Trim(base[i+1:], "0123456789") == "" && i+1 < len(base) {
		return base[:i], true
	}
	return "", false
}
