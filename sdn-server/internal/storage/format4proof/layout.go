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

// The owner's layout (contract C-37): one SQLite table file per source feed
// x standard, P/<TYPE>/<provider@source>.db, untagged records in
// P/<TYPE>/local.db; no row holds a provider or source string (the file is
// the feed). FeedLayout checks a migrated store against the format-1 store it
// came from: the feed files of each type are exactly the (provider, source)
// pairs format 1's source summary holds live records of (plus local), and no
// table of any feed file has a provider or source column.

// FeedLayout is a store's feed files and the problems found.
type FeedLayout struct {
	Files    map[string][]string `json:"files"`   // type -> feed file names
	Columns  map[string][]string `json:"columns"` // table -> columns, as one feed file holds them
	Problems []string            `json:"problems,omitempty"`
}

// feedFileName is the engine's file name of a feed: provider and source,
// URL-escaped outside [A-Za-z0-9._-], joined by '@' (local for neither).
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
	return enc(provider) + "@" + enc(source)
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
	lay := &FeedLayout{Files: map[string][]string{}, Columns: map[string][]string{}}
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
		for _, e := range ents {
			name := e.Name()
			feed, ok := strings.CutSuffix(name, ".db")
			if !ok {
				if strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm") || strings.HasSuffix(name, ".db-journal") {
					continue
				}
				fail("P/%s/%s is not a feed file", t.Name(), name)
				continue
			}
			lay.Files[t.Name()] = append(lay.Files[t.Name()], feed)
			have[t.Name()][feedIDSuffix.ReplaceAllString(feed, "")] = true
			files = append(files, filepath.Join(root, t.Name(), name))
		}
		sort.Strings(lay.Files[t.Name()])
	}
	for typ, feeds := range want {
		for feed := range feeds {
			if !have[typ][feed] {
				fail("%s: format 1 holds records of feed %s; format 4 has no file P/%s/%s.db", typ, feed, typ, feed)
			}
		}
	}
	for typ, feeds := range have {
		for feed := range feeds {
			if feed != "local" && !want[typ][feed] {
				fail("%s: feed file %s.db names no feed format 1 holds records of", typ, feed)
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
	}
	return lay, nil
}

// tableColumns lists each table's columns of a feed file, read by the
// system sqlite3 from a clone of the file (and its WAL): the store is not touched.
func tableColumns(file, tmp string) (map[string][]string, error) {
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	dst := filepath.Join(tmp, "feed.db")
	for _, sfx := range []string{"", "-wal"} {
		if _, err := os.Stat(file + sfx); err == nil {
			if out, err := exec.Command("cp", "-c", file+sfx, dst+sfx).CombinedOutput(); err != nil {
				if out2, err2 := exec.Command("cp", file+sfx, dst+sfx).CombinedOutput(); err2 != nil {
					return nil, fmt.Errorf("copy: %v %s %s", err2, out, out2)
				}
			}
		}
	}
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
