// scan-rowid-seek-e2e measures incremental scans against real format-4 WXF
// partitions. Fixtures are synthetic, temporary, and removed before the next size.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

const (
	fixtureCap = int64(1536 * 1024 * 1024)
	batchSize  = 4096
	pollLimit  = 50 * time.Millisecond
	tailLimit  = 100 * time.Millisecond
	syncPage   = 1000
)

type measurement struct {
	rows        int
	poll        time.Duration
	returned    int
	total       int64
	correctness string
	diagnostics string
	tail        time.Duration
	tailRows    int
	full        *syncMeasurement
	failures    []string
}

type syncMeasurement struct {
	wall, first10, last10 time.Duration
	pages, returned       int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "scan-rowid-seek-e2e FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("scan-rowid-seek-e2e PASS")
}

func run(ctx context.Context) error {
	if err := os.Setenv(format4.FormatEnv, "4"); err != nil {
		return err
	}
	var results []measurement
	for _, rows := range []int{400000, 1600000} {
		m, err := runFixture(ctx, rows)
		if err != nil {
			return err
		}
		results = append(results, m)
	}
	searchErr := checkSearchFixture(ctx)
	if searchErr != nil {
		fmt.Printf("search_pagination FAIL: %v\n", searchErr)
	} else {
		fmt.Println("search_pagination PASS pages=3,3,1 snapshot_totals=7,7,7 cursor_only_total=1 snapshot_identity=stable query_mismatch=rejected")
	}
	ratio := float64(results[1].poll) / float64(results[0].poll)
	tailRatio := float64(results[1].tail) / float64(results[0].tail)
	for _, m := range results {
		fmt.Printf("result rows=%d empty_ms=%.3f empty_rows=%d empty_total=%d tail_ms=%.3f tail_rows=%d correctness=%s explain=%s\n",
			m.rows, milliseconds(m.poll), m.returned, m.total, milliseconds(m.tail), m.tailRows, m.correctness, m.diagnostics)
		if m.full != nil {
			fmt.Printf("full_sync rows=%d pages=%d returned=%d wall_ms=%.3f first10_mean_ms=%.3f last10_mean_ms=%.3f last_first_ratio=%.3f required_ratio<2\n",
				m.rows, m.full.pages, m.full.returned, milliseconds(m.full.wall), milliseconds(m.full.first10), milliseconds(m.full.last10), float64(m.full.last10)/float64(m.full.first10))
		}
	}
	fmt.Printf("scaling rows_ratio=4 empty_ratio=%.3f tail_ratio=%.3f required_empty_ms<50 required_tail_ms<100 required_ratios<2\n", ratio, tailRatio)
	var failures []string
	if searchErr != nil {
		failures = append(failures, fmt.Sprintf("search pagination: %v", searchErr))
	}
	for _, m := range results {
		failures = append(failures, m.failures...)
	}
	if ratio >= 2 {
		failures = append(failures, fmt.Sprintf("4x partition growth increased empty-poll time %.3fx", ratio))
	}
	if tailRatio >= 2 {
		failures = append(failures, fmt.Sprintf("4x partition growth increased 1000-row tail-poll time %.3fx", tailRatio))
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

// CAT has canonical full-text indexing enabled. WXF deliberately does not,
// so search continuation coverage uses a separate seven-record CAT fixture.
func checkSearchFixture(ctx context.Context) (err error) {
	if err := checkFreeDisk(ctx); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "scan-rowid-seek-e2e-search-")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(root))
		fmt.Printf("cleanup search_rows=7 removed=%t\n", !exists(root))
	}()
	records := make([]format4.In, 7)
	for i := range records {
		b := flatbuffers.NewBuilder(128)
		name := b.CreateString(fmt.Sprintf("scansearch %d", i+1))
		CAT.CATStart(b)
		CAT.CATAddOBJECT_NAME(b, name)
		CAT.CATAddNORAD_CAT_ID(b, uint32(i+1))
		CAT.FinishCATBuffer(b, CAT.CATEnd(b))
		data := b.FinishedBytes()
		c, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: 32}).Sum(data)
		if err != nil {
			return err
		}
		records[i] = format4.In{CID: c.String(), Plain: data, TS: 1792238400}
	}
	if err := seedSearch(ctx, root, records); err != nil {
		return err
	}
	v, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	err = withFreshStore(ctx, root, v, func(store *storage.FlatSQLStore) error {
		base := datasync.QueryRequest{Schema: "CAT", ProviderID: "scan-e2e", SourceName: "search", Search: "scansearch", Limit: 3}
		req := base
		snapshotID, lastCursor := "", ""
		after := 0
		for _, count := range []int{3, 3, 1} {
			response, found, err := datasync.Scan(store, req, 100)
			if err != nil {
				return fmt.Errorf("search after=%d: %w", after, err)
			}
			if len(found) != count || response.Count != count || response.TotalCount != 7 {
				return fmt.Errorf("search after=%d records=%d count=%d total=%d want=%d total=7", after, len(found), response.Count, response.TotalCount, count)
			}
			if snapshotID != "" && response.SnapshotID != snapshotID {
				return errors.New("provided search continuation changed snapshot identity")
			}
			snapshotID = response.SnapshotID
			for i, record := range found {
				if record.RowID != int64(after+i+1) || record.CID != records[after+i].CID {
					return fmt.Errorf("search after=%d record[%d] has incorrect row ID or CID", after, i)
				}
			}
			after += count
			if after == 7 {
				if response.NextCursor != "" {
					return errors.New("final search page has a next cursor")
				}
				break
			}
			next, ok, err := datasync.ParseRawRecordCursor(response.NextCursor)
			if err != nil || !ok || next.AfterRowID != int64(after) || next.MaxRowID != 7 || next.SnapshotID != snapshotID {
				return fmt.Errorf("invalid search next cursor: %+v valid=%t err=%v", next, ok, err)
			}
			lastCursor = response.NextCursor
			req.Cursor, req.TotalCount = response.NextCursor, response.TotalCount
			req.SnapshotID, req.Head, req.HighWaterMark = response.SnapshotID, response.Head, response.HighWaterMark
		}
		cursorOnly := base
		cursorOnly.Cursor = lastCursor
		response, found, err := datasync.Scan(store, cursorOnly, 100)
		if err != nil {
			return fmt.Errorf("cursor-only search continuation: %w", err)
		}
		if len(found) != 1 || response.Count != 1 || response.TotalCount != 1 || response.SnapshotID != snapshotID || response.NextCursor != "" || found[0].RowID != 7 || found[0].CID != records[6].CID {
			return fmt.Errorf("cursor-only search continuation count=%d total=%d snapshot=%q want one row and snapshot=%q", response.Count, response.TotalCount, response.SnapshotID, snapshotID)
		}
		cursorOnly.Search = "different"
		if _, _, err := datasync.Scan(store, cursorOnly, 100); err == nil || !strings.Contains(err.Error(), "cursor belongs to a different search or data source") {
			return fmt.Errorf("changed search with original cursor was not rejected: %v", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, err = fixtureBytes(root)
	return err
}

func seedSearch(ctx context.Context, root string, records []format4.In) (err error) {
	e, err := format4.Open(ctx, format4.Options{DataRoot: root, Create: format4.CreateFresh,
		GseqFloor: 1, AOTCacheDir: storage.EngineAOTCacheDir()})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, e.Close(context.Background())) }()
	spec, err := format4.TypeSpecFor("CAT.fbs")
	if err != nil {
		return err
	}
	if !spec.FullText {
		return errors.New("CAT fixture requires its canonical full-text index")
	}
	if err := e.RegisterType(spec); err != nil {
		return err
	}
	out, err := e.Put(ctx, format4.Batch{Type: "CAT", Peer: "scan-rowid-seek-e2e", Records: records,
		Tags: []format4.Tag{{Provider: "scan-e2e", Source: "search", Batch: "seed"}}})
	if err != nil {
		return err
	}
	if len(out) != len(records) {
		return fmt.Errorf("search fixture inserted %d of %d records", len(out), len(records))
	}
	for i, result := range out {
		if result.Action != format4.ActNew || result.Seq != int64(i+1) {
			return fmt.Errorf("search seed row %d: %+v", i+1, result)
		}
	}
	if _, err := e.Rebuild(ctx, "CAT", format4.RebuildFTS); err != nil {
		return err
	}
	_, err = fixtureBytes(root)
	return err
}

// Check the actual volume before creating every fixture; other lanes may write
// between runs. df -g reports Available in GiB on the target macOS machine.
func checkFreeDisk(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "df", "-g", "/").CombinedOutput()
	if err != nil {
		return fmt.Errorf("df -g /: %w: %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return fmt.Errorf("cannot parse df -g /: %s", out)
	}
	free, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return fmt.Errorf("cannot parse available GiB: %w", err)
	}
	fmt.Printf("disk available_gib=%d required_gib=18\n", free)
	if free < 18 {
		return fmt.Errorf("fixture aborted: only %d GiB free; require at least 18", free)
	}
	return nil
}

func fixtureBytes(root string) (int64, error) {
	var bytes int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		bytes += info.Size()
		if bytes > fixtureCap {
			return fmt.Errorf("fixture exceeds 1.5 GiB cap: %d bytes", bytes)
		}
		return nil
	})
	return bytes, err
}

func weather(id int) (format4.In, error) {
	b := flatbuffers.NewBuilder(128)
	field := b.CreateString(fmt.Sprintf("scan-%07d", id))
	WXF.WXFStartVALUESVector(b, 1)
	b.PrependFloat32(280)
	values := b.EndVector(1)
	WXF.WXFGridStart(b)
	WXF.WXFGridAddNLAT(b, 1)
	WXF.WXFGridAddNLON(b, 1)
	grid := WXF.WXFGridEnd(b)
	WXF.WXFStart(b)
	WXF.WXFAddFIELD_ID(b, field)
	WXF.WXFAddGRID(b, grid)
	WXF.WXFAddVALUES(b, values)
	WXF.WXFAddVALID_TIME_MS(b, 1792238400000)
	WXF.FinishSizePrefixedWXFBuffer(b, WXF.WXFEnd(b))
	// The engine frames records; supply the canonical payload without a prefix.
	data := b.FinishedBytes()[4:]
	c, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: 32}).Sum(data)
	if err != nil {
		return format4.In{}, err
	}
	return format4.In{CID: c.String(), Plain: data, TS: 1792238400,
		Seq: int64(id), Tags: []format4.TagAt{{Tag: 0, At: 1792238400}}}, nil
}

func seed(ctx context.Context, root string, rows int) (err error) {
	e, err := format4.Open(ctx, format4.Options{DataRoot: root,
		Create: format4.CreateForMigration, GseqFloor: 1, AOTCacheDir: storage.EngineAOTCacheDir()})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, e.Close(context.Background())) }()
	spec, err := format4.TypeSpecFor("WXF.fbs")
	if err != nil {
		return err
	}
	spec.FullText = false
	if err := e.RegisterType(spec); err != nil {
		return err
	}
	start, last := time.Now(), time.Now()
	for first := 1; first <= rows; first += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(batchSize, rows-first+1)
		records := make([]format4.In, n)
		for i := range records {
			records[i], err = weather(first + i)
			if err != nil {
				return err
			}
		}
		out, err := e.Put(ctx, format4.Batch{Type: "WXF", Peer: "scan-rowid-seek-e2e", Mode: format4.ModeMigrate,
			Records: records, Tags: []format4.Tag{{Provider: "noaa-ncep", Source: "gfs-0p25-sea", Batch: "seed"}}})
		if err != nil {
			return err
		}
		if len(out) != n {
			return fmt.Errorf("seed returned %d outcomes for %d records", len(out), n)
		}
		for i, result := range out {
			if result.Action != format4.ActMigrated || result.Seq != int64(first+i) {
				return fmt.Errorf("seed row %d: unexpected outcome %+v", first+i, result)
			}
		}
		bytes, err := fixtureBytes(root)
		if err != nil {
			return err
		}
		if time.Since(last) >= 10*time.Second {
			fmt.Printf("seed rows=%d/%d fixture_bytes=%d elapsed=%s\n", first+n-1, rows, bytes, time.Since(start).Round(time.Millisecond))
			last = time.Now()
		}
	}
	if _, err := e.Rebuild(ctx, "WXF", format4.RebuildPartitionIndexes); err != nil {
		return err
	}
	if _, err := fixtureBytes(root); err != nil {
		return err
	}
	if err := e.Activate(ctx); err != nil {
		return err
	}
	bytes, err := fixtureBytes(root)
	if err != nil {
		return err
	}
	fmt.Printf("fixture rows=%d bytes=%d cap_bytes=%d seed_time=%s\n", rows, bytes, fixtureCap, time.Since(start).Round(time.Millisecond))
	return nil
}

func request(after int64, limit int) datasync.QueryRequest {
	return datasync.QueryRequest{Schema: "WXF", ProviderID: "noaa-ncep", SourceName: "gfs-0p25-sea",
		Cursor: datasync.EncodeRawRecordCursor(after, 0, ""), Limit: limit}
}

func runFixture(ctx context.Context, rows int) (m measurement, err error) {
	if err := checkFreeDisk(ctx); err != nil {
		return m, err
	}
	root, err := os.MkdirTemp("", "scan-rowid-seek-e2e-")
	if err != nil {
		return m, err
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(root))
		fmt.Printf("cleanup rows=%d removed=%t\n", rows, !exists(root))
	}()
	if err := seed(ctx, root, rows); err != nil {
		return m, err
	}
	// Each independent measurement closes its store before the next opens.
	// Diagnostics run last, read-only, after all stores close.
	defer func() {
		if diagnosticErr := explain(ctx, root, rows); diagnosticErr != nil {
			m.diagnostics = "FAIL"
			m.failures = append(m.failures, fmt.Sprintf("rows=%d diagnostics: %v", rows, diagnosticErr))
			fmt.Printf("explain rows=%d FAIL: %v\n", rows, diagnosticErr)
		} else {
			m.diagnostics = "PASS"
		}
	}()
	v, err := sds.NewValidator(nil)
	if err != nil {
		return m, err
	}
	m.rows = rows
	if err := withFreshStore(ctx, root, v, func(store *storage.FlatSQLStore) error {
		start := time.Now()
		response, records, err := datasync.Scan(store, request(int64(rows), 100), 100)
		m.poll, m.returned = time.Since(start), len(records)
		if err != nil {
			return err
		}
		m.total = response.TotalCount
		fmt.Printf("poll kind=empty rows=%d after_row_id=%d returned=%d total_count=%d elapsed_ms=%.3f\n",
			rows, rows, m.returned, m.total, milliseconds(m.poll))
		if m.poll >= pollLimit {
			m.failures = append(m.failures, fmt.Sprintf("rows=%d empty poll took %s, require <50ms", rows, m.poll))
		}
		if len(records) != 0 || response.Count != 0 || response.TotalCount != 0 || response.NextCursor != "" {
			m.failures = append(m.failures, fmt.Sprintf("rows=%d empty poll returned count=%d total=%d next=%q", rows, response.Count, response.TotalCount, response.NextCursor))
		}
		if err := checkPages(store, rows); err != nil {
			m.correctness = "FAIL"
			m.failures = append(m.failures, fmt.Sprintf("rows=%d correctness: %v", rows, err))
			fmt.Printf("correctness rows=%d FAIL: %v\n", rows, err)
		} else {
			m.correctness = "PASS"
			fmt.Printf("correctness rows=%d PASS returned=7 pages=3,3,1 snapshot_totals=7,7,7 cursor_order=exact upper_bound_rows=4\n", rows)
		}
		return nil
	}); err != nil {
		return m, err
	}
	// Reopen outside the timer: the baseline discards AfterRowID in its head
	// query, so reusing the empty poll's store could hide a whole-feed scan.
	if err := withFreshStore(ctx, root, v, func(store *storage.FlatSQLStore) error {
		start := time.Now()
		response, records, err := datasync.Scan(store, request(int64(rows-syncPage), syncPage), syncPage)
		m.tail, m.tailRows = time.Since(start), len(records)
		if err != nil {
			return err
		}
		fmt.Printf("poll kind=tail rows=%d after_row_id=%d returned=%d total_count=%d elapsed_ms=%.3f\n",
			rows, rows-syncPage, len(records), response.TotalCount, milliseconds(m.tail))
		if m.tail >= tailLimit {
			m.failures = append(m.failures, fmt.Sprintf("rows=%d 1000-row tail poll took %s, require <100ms", rows, m.tail))
		}
		if len(records) != syncPage || response.Count != syncPage || response.TotalCount != syncPage || response.NextCursor != "" {
			m.failures = append(m.failures, fmt.Sprintf("rows=%d tail count=%d records=%d total=%d next=%q", rows, response.Count, len(records), response.TotalCount, response.NextCursor))
		}
		for i, record := range records {
			id := rows - syncPage + i + 1
			expected, err := weather(id)
			if err != nil {
				return err
			}
			if record.RowID != int64(id) || record.CID != expected.CID {
				m.failures = append(m.failures, fmt.Sprintf("rows=%d tail record[%d] row_id=%d want=%d CID match=%t", rows, i, record.RowID, id, record.CID == expected.CID))
				break
			}
		}
		return nil
	}); err != nil {
		return m, err
	}
	if rows == 400000 {
		if err := withFreshStore(ctx, root, v, func(store *storage.FlatSQLStore) error {
			full, err := fullSync(ctx, store, rows)
			m.full = &full
			if err != nil {
				m.failures = append(m.failures, fmt.Sprintf("full sync: %v", err))
			}
			return nil
		}); err != nil {
			return m, err
		}
	}
	_, err = fixtureBytes(root)
	return m, err
}

func withFreshStore(ctx context.Context, root string, validator *sds.Validator, measure func(*storage.FlatSQLStore) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := storage.NewFlatSQLStore(root, validator, storage.WithDeferredBootRebuilds())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	if !store.Format4() {
		return errors.New("fixture did not open as format 4")
	}
	return measure(store)
}

func fullSync(ctx context.Context, store *storage.FlatSQLStore, rows int) (result syncMeasurement, err error) {
	start := time.Now()
	defer func() { result.wall = time.Since(start) }()
	pages := rows / syncPage
	req := request(0, syncPage)
	snapshotID := ""
	for page := 1; page <= pages; page++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		pageStart := time.Now()
		response, records, err := datasync.Scan(store, req, syncPage)
		elapsed := time.Since(pageStart)
		if err != nil {
			return result, err
		}
		if page <= 10 {
			result.first10 += elapsed / 10
		}
		if page > pages-10 {
			result.last10 += elapsed / 10
		}
		result.pages++
		if len(records) != syncPage || response.Count != syncPage || response.TotalCount != int64(rows) {
			return result, fmt.Errorf("page=%d count=%d records=%d total=%d want count=1000 total=%d", page, response.Count, len(records), response.TotalCount, rows)
		}
		if snapshotID != "" && response.SnapshotID != snapshotID {
			return result, fmt.Errorf("page=%d changed snapshot identity", page)
		}
		snapshotID = response.SnapshotID
		for i, record := range records {
			if want := int64(result.returned + i + 1); record.RowID != want {
				return result, fmt.Errorf("page=%d record[%d] row_id=%d want=%d", page, i, record.RowID, want)
			}
		}
		result.returned += len(records)
		if page <= 10 || page > pages-10 || page%100 == 0 {
			fmt.Printf("sync_page page=%d/%d returned=%d total_count=%d elapsed_ms=%.3f\n", page, pages, len(records), response.TotalCount, milliseconds(elapsed))
		}
		if page == pages {
			if response.NextCursor != "" {
				return result, errors.New("full sync final page has a next cursor")
			}
			break
		}
		next, ok, err := datasync.ParseRawRecordCursor(response.NextCursor)
		if err != nil || !ok || next.AfterRowID != int64(result.returned) || next.MaxRowID != int64(rows) || next.SnapshotID != snapshotID {
			return result, fmt.Errorf("page=%d invalid next cursor: %+v valid=%t err=%v", page, next, ok, err)
		}
		req.Cursor, req.TotalCount = response.NextCursor, response.TotalCount
		req.SnapshotID, req.Head, req.HighWaterMark = response.SnapshotID, response.Head, response.HighWaterMark
	}
	if ratio := float64(result.last10) / float64(result.first10); ratio >= 2 {
		return result, fmt.Errorf("last10/first10 page time ratio %.3f, require <2", ratio)
	}
	return result, nil
}

func explain(ctx context.Context, root string, rows int) error {
	path, err := partitionDatabase(ctx, root)
	if err != nil {
		return err
	}
	uri, err := immutableSQLiteURI(path)
	if err != nil {
		return err
	}
	// These are the actual reader.cpp/codec.cpp statement shapes from the
	// shipped engine source. Its public mailbox API has operation statistics,
	// not a per-statement trace hook. Do not present this list as traced SQL:
	// an empty bounded scan may return before opening a partition reader.
	statements := []struct{ name, sql string }{
		{"reader_metadata", "SELECT k, v FROM meta WHERE k IN ('rows','bytes')"},
		{"node_dictionary", "SELECT id, producer, peer FROM node WHERE id>?1 ORDER BY id"},
		{"batch_dictionary", "SELECT id, batch, ppeer, pkey FROM batch WHERE id>?1 ORDER BY id"},
		{"begin", "BEGIN"},
		{"sequence_page", "SELECT seq FROM r INDEXED BY r_s WHERE seq>?1 AND seq<=?2 ORDER BY seq LIMIT ?3"},
		{"record_by_primary_key", "SELECT rid, n, b, c, u, at, cid, e, k, ts, f, x, len, off FROM r WHERE rid>=?1 AND rid<=?2"},
		{"stream_generation", "SELECT v FROM meta WHERE k='gen'"},
		{"commit", "COMMIT"},
	}
	fmt.Printf("explain rows=%d database=%s source=shipped-engine-statement-shapes actual_sql_trace=unavailable\n", rows, path)
	for _, statement := range statements {
		out, err := exec.CommandContext(ctx, "sqlite3", "-readonly", "-cmd", ".explain off", uri,
			"EXPLAIN QUERY PLAN "+statement.sql).CombinedOutput()
		if err != nil {
			return fmt.Errorf("EXPLAIN %s: %w: %s", statement.name, err, out)
		}
		plan := strings.TrimSpace(string(out))
		if plan == "" {
			plan = "(no plan rows)"
		}
		fmt.Printf("SQL %s: %s\nPLAN %s\n", statement.name, statement.sql, plan)
	}
	return nil
}

func immutableSQLiteURI(path string) (string, error) {
	// The engine's VFS keeps WAL shared memory in the WASM instance. After
	// Close checkpoints the WAL, immutable mode lets native SQLite inspect
	// the database without attempting to create a host -shm file.
	info, err := os.Stat(path + "-wal")
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err == nil && info.Size() != 0 {
		return "", fmt.Errorf("refusing immutable SQLite read with uncheckpointed WAL: %s (%d bytes)", path, info.Size())
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: "immutable=1"}).String(), nil
}

// Discover the actual engine-created database by its header and record table;
// the E2E must not assume how the engine encodes a feed name on disk.
func partitionDatabase(ctx context.Context, root string) (string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		header := make([]byte, 16)
		_, readErr := io.ReadFull(file, header)
		closeErr := file.Close()
		if closeErr != nil {
			return closeErr
		}
		if readErr != nil || string(header) != "SQLite format 3\x00" {
			return nil
		}
		uri, err := immutableSQLiteURI(path)
		if err != nil {
			return err
		}
		out, err := exec.CommandContext(ctx, "sqlite3", "-readonly", uri,
			"SELECT name FROM sqlite_master WHERE type='table' AND name='r'").CombinedOutput()
		if err != nil {
			return fmt.Errorf("identify partition %s: %w: %s", path, err, out)
		}
		if strings.TrimSpace(string(out)) == "r" {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one partition database, found %d: %v", len(matches), matches)
	}
	return matches[0], nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

func checkPages(store *storage.FlatSQLStore, rows int) error {
	after := int64(rows - 7)
	req := request(after, 3)
	snapshotID := ""
	var failures []string
	for remaining := int64(7); remaining > 0; remaining -= 3 {
		response, records, err := datasync.Scan(store, req, 100)
		if err != nil {
			return err
		}
		want := int(min(remaining, 3))
		if response.Count != want || len(records) != want {
			return fmt.Errorf("after=%d count=%d records=%d want=%d", after, response.Count, len(records), want)
		}
		if response.TotalCount != 7 {
			failures = append(failures, fmt.Sprintf("after=%d total=%d want snapshot total=7", after, response.TotalCount))
		}
		if snapshotID != "" && response.SnapshotID != snapshotID {
			failures = append(failures, fmt.Sprintf("after=%d snapshot changed from %q to %q", after, snapshotID, response.SnapshotID))
		}
		snapshotID = response.SnapshotID
		for i, record := range records {
			id := after + int64(i) + 1
			expected, err := weather(int(id))
			if err != nil {
				return err
			}
			if record.RowID != id || record.CID != expected.CID {
				return fmt.Errorf("after=%d record[%d] row_id=%d CID=%q want row_id=%d CID=%q", after, i, record.RowID, record.CID, id, expected.CID)
			}
		}
		current, ok, err := datasync.ParseRawRecordCursor(response.Cursor)
		if err != nil || !ok || current.AfterRowID != after || current.MaxRowID != int64(rows) {
			return fmt.Errorf("unexpected response cursor: %+v, valid=%t, err=%v", current, ok, err)
		}
		after += int64(want)
		if remaining <= 3 {
			if response.NextCursor != "" {
				return fmt.Errorf("last page has next cursor %q", response.NextCursor)
			}
			break
		}
		next, ok, err := datasync.ParseRawRecordCursor(response.NextCursor)
		if err != nil || !ok || next.AfterRowID != after || next.MaxRowID != int64(rows) || next.SnapshotID != response.SnapshotID {
			return fmt.Errorf("unexpected next cursor: %+v, valid=%t, err=%v", next, ok, err)
		}
		// A supplied snapshot keeps the initial tail's total while its cursor
		// advances. Continuations must not recount the remaining suffix.
		req.Cursor = response.NextCursor
		req.TotalCount = response.TotalCount
		req.SnapshotID = response.SnapshotID
		req.Head = response.Head
		req.HighWaterMark = response.HighWaterMark
	}
	// Leave three live rows beyond the requested snapshot boundary so an
	// ignored upper bound cannot accidentally pass on a static fixture.
	bounded := request(int64(rows-7), 10)
	bounded.Cursor = datasync.EncodeRawRecordCursor(int64(rows-7), int64(rows-3), "")
	response, records, err := datasync.Scan(store, bounded, 100)
	if err != nil {
		return err
	}
	if response.Count != 4 || len(records) != 4 || response.TotalCount != 4 || response.NextCursor != "" {
		failures = append(failures, fmt.Sprintf("bounded scan count=%d records=%d total=%d next=%q, want four rows and no next cursor",
			response.Count, len(records), response.TotalCount, response.NextCursor))
	}
	for i, record := range records {
		id := int64(rows-6) + int64(i)
		expected, err := weather(int(id))
		if err != nil {
			return err
		}
		if i >= 4 || record.RowID != id || record.CID != expected.CID {
			return fmt.Errorf("bounded scan record[%d] row_id=%d CID=%q, want row_id=%d CID=%q within upper=%d",
				i, record.RowID, record.CID, id, expected.CID, rows-3)
		}
	}
	current, ok, err := datasync.ParseRawRecordCursor(response.Cursor)
	if err != nil || !ok || current.AfterRowID != int64(rows-7) || current.MaxRowID != int64(rows-3) {
		return fmt.Errorf("unexpected bounded response cursor: %+v, valid=%t, err=%v", current, ok, err)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, ", "))
	}
	return nil
}
