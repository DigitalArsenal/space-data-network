package format4proof

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Feed names (contract C-37 (1): "file names are a safe encoding of the
// feed"). SDN puts no character limit on a provider or source
// (ValidateSourceTags requires them non-empty; connectors and dataset-shard
// index tags supply them), so whatever the strings hold, a feed's records
// must live in the one file the engine names for it (store.cpp
// feedFileName, mirrored by feedFileName here), which the engine creates,
// measures for the quota, opens, recovers and migrates. GATES-feed-r1 B1
// found a feed whose name needs escaping stored in another file than the
// one the engine created and measured (SQLite decoded the name's %XX
// escapes in a URI open): a '/' made a directory, "/../" was refused, and
// the quota did not count the feed.
//
// DriveFeedNames proves the names end to end through SDN on fresh stores
// (no fixture): the file set, the reads against format 1 given the same
// writes, the reads after a reopen, the quota, store-migrate --to 4 of the
// format-1 store, and kill -9 rounds on a feed whose source holds every
// such character. The coverage class X15 (and XM, which migrates it) reads
// the same feeds on the fixture field by field.

// feedNameCase is one feed whose provider or source needs escaping in a file
// name.
type feedNameCase struct{ Provider, Source, Why string }

// feedNameCases are the names: each a character a path, a URI or a file
// system treats specially.
var feedNameCases = []feedNameCase{
	{FixtureProvider, "feed name with spaces", "spaces"},
	{"other provider", "other source/x", "a space in the provider, a '/' in the source"},
	{FixtureProvider, "x/../local", "'/../', a path to another feed's file"},
	{FixtureProvider, "../local", "a leading '../'"},
	{FixtureProvider, "50%25 off %zz", "'%': an escape's own text and a malformed escape"},
	{FixtureProvider, "gp?share=0&mode=ro", "'?' and '&': a URI query"},
	{FixtureProvider, "gp#frag", "'#': a URI fragment"},
	{"prövider-ü", "源-é/ñ", "non-ASCII bytes and a '/'"},
	{".hidden", "dot-led", "a name that starts with '.'"},
	{FixtureProvider, caseTwinSource, "a name equal to another feed's but for case (case-insensitive file systems)"},
	{FixtureProvider, strings.Repeat("long-source-name/", 12), "a name longer than a file name may be, escaped"},
}

// caseTwinSource is the feedNameCases source equal to the fixture's
// celestrak-gp but for case.
const caseTwinSource = "Celestrak-GP"

// feedNameCrashSource is the kill -9 rounds' source: every character of
// feedNameCases in one name.
const feedNameCrashSource = "proof crash/../ü %25?x=1#y"

// feedNamesBatch is the batch each feed's records arrive in; the first feed
// also gets feedNamesBatch2, then a supersede keeps it (a supersede in a
// file of an escaped name).
const (
	feedNamesBatch  = "names-b1"
	feedNamesBatch2 = "names-b2"
	feedNamesPeer   = "source:feed-names"
	feedNamesPer    = 5 // records of the first feed and of the untagged write; feed i holds feedNamesPer+i
)

// feedNameFeeds are the feeds DriveFeedNames writes: every case, the feed
// whose name caseTwinSource equals but for case, and a feed whose source is
// literally "local" (format 1's "local" partition holds it beside the
// untagged records: "<TYPE>@local" reads both, GATES-feed-r2 N-B).
func feedNameFeeds() []feedNameCase {
	return append(append([]feedNameCase(nil), feedNameCases...),
		feedNameCase{FixtureProvider, "celestrak-gp", "the feed the case-collision case collides with"},
		feedNameCase{FixtureProvider, "local", "a source spelled as the untagged records' partition"})
}

// feedNameWrite is one write of DriveFeedNames: its records and tags (nil:
// untagged, the local file).
type feedNameWrite struct {
	recs [][]byte
	tags *storage.SourceTags
}

// feedNameWrites are DriveFeedNames' writes, in order: each feed's records
// (a distinct count per feed, so a read of the wrong feed shows: GATES-feed-r2
// N-C), untagged records, then the first feed's second batch.
func feedNameWrites() []feedNameWrite {
	var out []feedNameWrite
	for i, f := range feedNameFeeds() {
		t := storage.SourceTags{ProviderID: f.Provider, SourceName: f.Source, BatchID: feedNamesBatch}
		out = append(out, feedNameWrite{CrashRecords(9100+i, 0, feedNamesPer+i), &t})
	}
	out = append(out, feedNameWrite{CrashRecords(9099, 0, feedNamesPer), nil})
	f := feedNameFeeds()[0]
	t := storage.SourceTags{ProviderID: f.Provider, SourceName: f.Source, BatchID: feedNamesBatch2}
	return append(out, feedNameWrite{CrashRecords(9098, 0, feedNamesPer), &t})
}

// writeFeedNames applies feedNameWrites to s, then supersedes the first
// feed keeping its second batch. A write that fails is a problem, and the
// others still run (the reads then show what it left out).
func writeFeedNames(s *storage.FlatSQLStore) []string {
	var probs []string
	for _, w := range feedNameWrites() {
		var n int
		var err error
		feed := "untagged"
		if w.tags == nil {
			n, err = s.StoreBatch(crashSchema, w.recs, feedNamesPeer, nil)
		} else {
			feed = fmt.Sprintf("%q/%q %s", w.tags.ProviderID, w.tags.SourceName, w.tags.BatchID)
			n, err = s.StoreBatchWithSourceTags(crashSchema, w.recs, feedNamesPeer, nil, *w.tags)
		}
		switch {
		case err != nil:
			probs = append(probs, fmt.Sprintf("write %s: %v", feed, err))
		case n != len(w.recs):
			probs = append(probs, fmt.Sprintf("write %s: stored %d of %d", feed, n, len(w.recs)))
		}
	}
	f := feedNameFeeds()[0]
	if _, err := s.SupersedeSourceBatches(crashSchema, f.Provider, f.Source, feedNamesBatch2); err != nil {
		probs = append(probs, fmt.Sprintf("supersede %q: %v", f.Source, err))
	}
	return probs
}

// feedNameWant is the feed files a store holds after writeFeedNames (type
// -> feedFileName names): every feed's and local.
func feedNameWant() map[string]map[string]bool {
	files := map[string]bool{"local": true}
	for _, f := range feedNameFeeds() {
		files[feedFileName(f.Provider, f.Source)] = true
	}
	return map[string]map[string]bool{strings.TrimSuffix(crashSchema, ".fbs"): files}
}

// relationCount is `SELECT COUNT(*) AS n FROM "OMM@<source>"` as rows that
// start with key: its count, or its error kind.
func relationCount(s *storage.FlatSQLStore, key Row, source string) ([]Row, error) {
	sql := fmt.Sprintf(`SELECT COUNT(*) AS n FROM "OMM@%s"`, source)
	payload, _, _, err := s.QuerySandboxedJSON(sql, sandboxCaps)
	if err != nil {
		return []Row{append(append(Row(nil), key...), Field{"sql", errKind(err)})}, nil
	}
	rs, err := JSONRows(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", sql, err)
	}
	out := make([]Row, 0, len(rs))
	for _, r := range rs {
		out = append(out, append(append(append(Row(nil), key...), Field{"sql", "count"}), r...))
	}
	return out, nil
}

// Relations feedNameAnswers reads beside each feed's own (C-43): the
// untagged records' (B2: format 1's "local" partition, which also holds the
// feed whose source is "local"), the first feed's in capitals (B3: a unique
// case-insensitive match) and the case twins' in capitals (B3: a spelling of
// neither twin answers none, never another feed).
func feedNameRelations() []string {
	return []string{"local", strings.ToUpper(feedNameCases[0].Source), strings.ToUpper(caseTwinSource)}
}

// feedNamesEpochAt is the time feedNameAnswers' epoch streams ask for: half
// a second after the third record's epoch of every write (CrashRecords).
const feedNamesEpochAt = float64(crashEpoch0+2*37) + 0.5

// feedNameAnswers reads what writeFeedNames left, as clock-free rows that
// compare across formats: per feed its count, its tagged records (CID,
// provider, source, batch, bytes) and its `<TYPE>@<source>` SQL count; the
// counts of feedNameRelations and the epoch streams of source "local"; per
// record written its tags and bytes; the type's count and the store's
// source record counts.
func feedNameAnswers(s *storage.FlatSQLStore) ([]Row, error) {
	var out []Row
	for _, f := range feedNameFeeds() {
		q := storage.RawRecordQuery{SchemaName: crashSchema, ProviderID: f.Provider, SourceName: f.Source}
		n, err := s.CountRawRecords(q)
		if err != nil {
			return nil, fmt.Errorf("count %q: %w", f.Source, err)
		}
		out = append(out, ValueRow("feed", f.Provider+"/"+f.Source, "count", i64(n)))
		recs, err := s.QuerySourceTaggedRecords(storage.SourceTagQuery{SchemaName: crashSchema, ProviderID: f.Provider, SourceName: f.Source, Limit: 1000})
		if err != nil {
			return nil, fmt.Errorf("tagged %q: %w", f.Source, err)
		}
		var rows []Row
		for _, r := range recs {
			rows = append(rows, ValueRow("feed", f.Provider+"/"+f.Source, "cid", r.CID, "provider", r.SourceTags.ProviderID,
				"source", r.SourceTags.SourceName, "batch", r.SourceTags.BatchID, "data", digest(r.Data)))
		}
		out = append(out, sortRows(rows)...)
		rs, err := relationCount(s, ValueRow("feed", f.Provider+"/"+f.Source), f.Source)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}
	for _, rel := range feedNameRelations() {
		rs, err := relationCount(s, ValueRow("relation", "OMM@"+rel), rel)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}
	for _, profile := range []string{"nearest", "as_of", "forward"} {
		key := ValueRow("epoch stream", "local "+profile)
		st, err := s.QueryEpochRawStream(crashSchema, "local", profile, feedNamesEpochAt, 0)
		if err != nil {
			out = append(out, append(key, Field{"err", errKind(err)}))
			continue
		}
		frames, err := FrameRows(st.Bytes)
		if err != nil {
			return nil, fmt.Errorf("epoch stream local %s: %w", profile, err)
		}
		out = append(out, append(key, Field{"frames", strconv.Itoa(len(frames))}))
		for _, r := range unorderedDigest(frames) {
			out = append(out, append(append(Row(nil), key...), r...))
		}
	}
	for _, w := range feedNameWrites() {
		for _, rec := range w.recs {
			cid := storage.ComputeCID(rec)
			t, err := s.GetSourceTags(crashSchema, cid)
			if err != nil {
				out = append(out, ValueRow("cid", cid, "tags", errKind(err)))
			} else {
				out = append(out, ValueRow("cid", cid, "provider", t.ProviderID, "source", t.SourceName, "batch", t.BatchID))
			}
			r, err := s.GetRecord(crashSchema, cid)
			if err != nil {
				out = append(out, ValueRow("cid", cid, "get", errKind(err)))
			} else {
				out = append(out, ValueRow("cid", cid, "data", digest(r.Data)))
			}
		}
	}
	n, err := s.Count(crashSchema)
	if err != nil {
		return nil, err
	}
	out = append(out, ValueRow("count", i64(n)))
	counts, err := s.SourceRecordCounts()
	if err != nil {
		return nil, err
	}
	var rows []Row
	for k, n := range counts {
		rows = append(rows, ValueRow("feed", k, "records", i64(n)))
	}
	return append(out, sortRows(rows)...), nil
}

// caseTwinResidual is the C-43 B3 residual on the feed-name answers: format
// 1 answers a case twin's relation (caseTwinSource or celestrak-gp, and
// the twins' spelling in capitals, which is neither's) with its own
// order-dependent choice between the twins. Format 4's answer must be the
// named feed's own count (CountRawRecords' row), and none (a count of 0 or
// an error) for the spelling of neither. It returns format 1's rows with
// each such row format 4 answers so replaced by format 4's (so diffRows
// compares the rest), and what format 4 answered wrong.
func caseTwinResidual(a1, a4 []Row) ([]Row, []string) {
	if len(a1) != len(a4) {
		return a1, nil // diffRows reports it
	}
	twin := map[string]bool{FixtureProvider + "/" + caseTwinSource: true, FixtureProvider + "/celestrak-gp": true}
	own := map[string]string{}
	for _, r := range a4 {
		if f, n := r.Get("feed"), r.Get("count"); f != "" && n != "" && r.Get("cid") == "" {
			own[f] = n
		}
	}
	neither := "OMM@" + strings.ToUpper(caseTwinSource)
	out := append([]Row(nil), a1...)
	var probs []string
	for i, r := range a4 {
		switch {
		case twin[r.Get("feed")] && r.Get("sql") != "":
			if r.Get("sql") != "count" || r.Get("n") != own[r.Get("feed")] {
				probs = append(probs, fmt.Sprintf("format 4 counts OMM@%s as %s; the feed holds %s", strings.TrimPrefix(r.Get("feed"), FixtureProvider+"/"), r.text(), own[r.Get("feed")]))
				continue
			}
		case r.Get("relation") == neither:
			if r.Get("sql") == "count" && r.Get("n") != "0" {
				probs = append(probs, fmt.Sprintf("format 4 reads %s, which spells neither twin: %s", neither, r.text()))
				continue
			}
		default:
			continue
		}
		out[i] = r
	}
	return out, probs
}

// diffRows lists where two answers differ (at most a few rows).
func diffRows(what string, a, b []Row) []string {
	var out []string
	if len(a) != len(b) {
		out = append(out, fmt.Sprintf("%s: %d rows, then %d", what, len(a), len(b)))
	}
	for i := 0; i < len(a) && i < len(b) && len(out) < 8; i++ {
		if a[i].text() != b[i].text() {
			out = append(out, fmt.Sprintf("%s row %d: %s | %s", what, i, a[i].text(), b[i].text()))
		}
	}
	return out
}

// FeedNamesResult sums DriveFeedNames.
type FeedNamesResult struct {
	Problems []string `json:"problems"`
}

// DriveFeedNames runs the feed-name proof (the doc above) and writes a run
// "feednames". crashRounds are the kill -9 rounds per scenario (0 skips
// them).
func DriveFeedNames(ctx context.Context, c Config, crashRounds int, logf Logf) (*Run, error) {
	r := &Run{Kind: "feednames", Arm: ArmS, Format: "4", Label: "fresh", Started: time.Now().UTC().Format(time.RFC3339),
		Machine: ThisMachine(), LoadStart: Load(), Extra: map[string]any{}}
	var probs []string
	fail := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }
	if err := PrewarmAOT(); err != nil {
		return r, fmt.Errorf("harness: %w", err)
	}
	root := c.workPath("feednames")
	_ = os.RemoveAll(root)
	defer os.RemoveAll(root)
	f1, p4 := filepath.Join(root, "f1"), filepath.Join(root, "s")
	for _, d := range []string{f1, p4} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return r, err
		}
	}
	cases := make([]map[string]string, 0, len(feedNameFeeds()))
	for _, f := range feedNameFeeds() {
		cases = append(cases, map[string]string{"provider": f.Provider, "source": f.Source, "why": f.Why, "file": feedFileName(f.Provider, f.Source)})
	}
	r.Extra["feeds"] = cases

	// 1. The same writes on format 1 and format 4; format 4's files.
	written := func(arm, dir string) ([]Row, error) {
		s, _, err := OpenArm(arm, dir)
		if err != nil {
			return nil, err
		}
		for _, p := range writeFeedNames(s) {
			fail("%s: %s", arm, p)
		}
		if arm == ArmF1 {
			if _, err := s.HydrateEngineHotWindow(); err != nil {
				_ = s.Close()
				return nil, fmt.Errorf("format 1 hot window: %w", err)
			}
		}
		a, err := feedNameAnswers(s)
		if cerr := s.Close(); err == nil {
			err = cerr
		}
		return a, err
	}
	a1, err := written(ArmF1, f1)
	if err != nil {
		return r, err
	}
	a4, err := written(ArmS, p4)
	if err != nil {
		return r, err
	}
	r.Extra["rows"] = len(a1)
	a1r, twinProbs := caseTwinResidual(a1, a4)
	for _, p := range twinProbs {
		fail("%s", p)
	}
	for _, d := range diffRows("format 1 vs format 4", a1r, a4) {
		fail("%s", d)
	}
	layout := func(step string) {
		lay, err := checkFeedFiles(feedNameWant(), p4, root)
		if err != nil {
			fail("%s: layout: %v", step, err)
			return
		}
		r.Extra["files_"+step] = lay.Files
		for _, p := range lay.Problems {
			fail("%s: %s", step, p)
		}
	}
	layout("written")

	// 2. Reopen: the same answers, the same files.
	s, _, err := OpenArm(ArmS, p4)
	if err != nil {
		return r, fmt.Errorf("reopen: %w", err)
	}
	again, err := feedNameAnswers(s)
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fail("reopen: %v", err)
	} else {
		for _, d := range diffRows("format 4 before vs after a reopen", a4, again) {
			fail("%s", d)
		}
	}
	layout("reopened")
	for _, v := range VerifyFormat4Store(p4) {
		fail("verify: %s", v)
	}

	// 3. store-migrate --to 4 of the format-1 store: the layout (format 1's
	// feeds) and the same answers.
	if c.SDNBin == "" {
		fail("store-migrate: %s not set", EnvSDNBin)
	} else {
		mig := filepath.Join(root, "migrated")
		if err := CloneStore(f1, mig); err != nil {
			return r, err
		}
		if _, _, err := migrateRun(ctx, c.SDNBin, mig, c.logPath("feednames-migrate"), 0); err != nil {
			fail("store-migrate: %v", err)
		} else {
			for _, p := range checkMigrated(mig) {
				fail("migrated: %s", p)
			}
			f1c := filepath.Join(root, "f1-layout")
			if err := CloneStore(f1, f1c); err != nil {
				return r, err
			}
			lay, err := CheckFeedLayout(f1c, mig, root)
			if err != nil {
				fail("migrated: layout: %v", err)
			} else {
				r.Extra["files_migrated"] = lay.Files
				for _, p := range lay.Problems {
					fail("migrated: %s", p)
				}
			}
			s, _, err := OpenArm(ArmS, mig)
			if err != nil {
				fail("open the migrated store: %v", err)
			} else {
				am, err := feedNameAnswers(s)
				if cerr := s.Close(); err == nil {
					err = cerr
				}
				if err != nil {
					fail("migrated: %v", err)
				} else {
					a1m, twinProbs := caseTwinResidual(a1, am)
					for _, p := range twinProbs {
						fail("migrated: %s", p)
					}
					for _, d := range diffRows("format 1 vs migrated", a1m, am) {
						fail("%s", d)
					}
				}
			}
		}
	}

	// 4. The quota: the same records under these names and under plain
	// ones, then GarbageCollectToQuota with the same falling bounds (from
	// the plain store's disk use, halved each pass): each pass must evict
	// alike (the engine measures the files it wrote; B1's quota measured an
	// empty file and evicted nothing).
	quota := func(name string, plain bool, bounds []int64) (ev []int64, left int64, du int64, err error) {
		dir := filepath.Join(root, "quota-"+name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, 0, 0, err
		}
		s, _, err := OpenArm(ArmS, dir)
		if err != nil {
			return nil, 0, 0, err
		}
		defer s.Close()
		for i, f := range feedNameCases {
			t := storage.SourceTags{ProviderID: f.Provider, SourceName: f.Source, BatchID: feedNamesBatch}
			if plain {
				t.ProviderID, t.SourceName = FixtureProvider, "names-plain-"+strconv.Itoa(i)
			}
			for call := 0; call < 3; call++ {
				if _, err := s.StoreBatchWithSourceTags(crashSchema, CrashRecords(9200+i, call, 100), feedNamesPeer, nil, t); err != nil {
					fail("quota %s: write %q/%q: %v", name, t.ProviderID, t.SourceName, err)
				}
			}
		}
		if du, err = s.DiskUsageBytes(); err != nil {
			return nil, 0, 0, err
		}
		for _, b := range bounds {
			n, err := s.GarbageCollectToQuota(b)
			if err != nil {
				return ev, 0, du, err
			}
			ev = append(ev, n)
		}
		left, err = s.Count(crashSchema)
		return ev, left, du, err
	}
	// The bounds: the plain store's disk use, halved until the quota no
	// longer fits the files every store holds (a pass that leaves no record).
	_, _, duPlain, err := quota("probe", true, nil)
	var bounds []int64
	if err == nil {
		for b := duPlain; b > 16<<10 && len(bounds) < 14; b /= 2 {
			bounds = append(bounds, b)
		}
	}
	evPlain, leftPlain, _, err2 := quota("plain", true, bounds)
	evNames, leftNames, duNames, err1 := quota("names", false, bounds)
	r.Extra["quota"] = map[string]any{"records": int64(len(feedNameCases) * 300), "bounds": bounds, "disk_plain": duPlain, "disk_names": duNames,
		"evicted_plain": evPlain, "left_plain": leftPlain, "evicted_names": evNames, "left_names": leftNames}
	var cumPlain, cumNames, informative int64
	switch {
	case err != nil || err1 != nil || err2 != nil:
		fail("quota: %v / %v / %v", err, err2, err1)
	default:
		for i := range evPlain {
			cumPlain += evPlain[i]
			if i < len(evNames) {
				cumNames += evNames[i]
			}
			if cumPlain > 0 && cumPlain < int64(len(feedNameCases)*300) {
				informative++
			}
			if absDiff(cumNames, cumPlain) > max(32, cumPlain/20) {
				fail("quota: after GarbageCollectToQuota(%d) %d records are evicted under these names, %d under plain names (passes %v / %v)",
					bounds[i], cumNames, cumPlain, evNames, evPlain)
				break
			}
		}
		if informative == 0 {
			fail("quota: no bound evicted part of the plain store (passes %v over bounds %v)", evPlain, bounds)
		}
	}

	// 5. kill -9 rounds on a feed whose source holds every such character.
	if crashRounds > 0 {
		for _, sc := range []string{ScenarioIngest, ScenarioSupersede} {
			store := filepath.Join(root, "crash-"+sc)
			if err := os.MkdirAll(store, 0o755); err != nil {
				return r, err
			}
			// Out stays empty: the rounds' runs would take the crash phase's
			// file names; a violation is the loop's result.
			res, err := CrashLoop(ctx, CrashLoopSpec{Arm: ArmS, Scenario: sc, Store: store, Work: root, Rounds: crashRounds,
				Batch: 1024, Source: feedNameCrashSource, Logs: filepath.Join(root, "crash-logs-"+sc)}, logf)
			r.Extra["crash_"+sc] = res
			if err != nil {
				fail("crash %s: %v", sc, err)
			} else if res.Violating > 0 {
				fail("crash %s: %d of %d rounds violate: %s", sc, res.Violating, res.Rounds, res.FirstViolation)
			}
			logf("feednames crash %s: %d rounds, %d killed, %d violating", sc, res.Rounds, res.Killed, res.Violating)
			_ = os.RemoveAll(store)
		}
	}
	r.Extra["problems"] = append([]string{}, probs...)
	r.LoadEnd = Load()
	if c.Out != "" {
		if _, err := WriteRun(c.Out, r); err != nil {
			return r, err
		}
	}
	if len(probs) > 0 {
		return r, errors.New(strings.Join(probs, "\n"))
	}
	return r, nil
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}
