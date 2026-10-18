// control-write-hold-e2e measures control SQL during real format-4 weather
// ingestion and retention. Fixtures are synthetic, bounded, and removed on exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

const (
	fixtureLimit   = 1536 << 20
	batchCount     = 6
	callsPerBatch  = 12
	directoryRows  = 8
	directoryCalls = 24
	holdBudget     = 250 * time.Millisecond
	syncDelay      = 200 * time.Millisecond
)

var families = []string{"source_batch_license_upsert", "directory_upsert", "last_insert_rowid_changes"}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func require(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}

func diskGuard() {
	out, err := exec.Command("df", "-g", "/").CombinedOutput()
	check(err)
	fmt.Printf("disk_guard %s", out)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	require(len(fields) >= 4, "cannot parse df -g output")
	free, err := strconv.ParseInt(fields[3], 10, 64)
	check(err)
	require(free >= 18, "fixture refused: free=%d GiB, minimum=18 GiB", free)
}

func fixtureSize(root string) int64 {
	var total int64
	check(filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	}))
	require(total <= fixtureLimit, "fixture exceeds 1.5 GiB: %d bytes", total)
	return total
}

func weather(id int, at time.Time) []byte {
	b := flatbuffers.NewBuilder(9000)
	field := b.CreateString(fmt.Sprintf("gfs:TMP:2m:%08d", id))
	WXF.WXFStartVALUESVector(b, 46*46)
	for i := 46*46 - 1; i >= 0; i-- {
		b.PrependFloat32(273 + float32((id+i)%91))
	}
	values := b.EndVector(46 * 46)
	WXF.WXFGridStart(b)
	WXF.WXFGridAddNLAT(b, 46)
	WXF.WXFGridAddNLON(b, 46)
	grid := WXF.WXFGridEnd(b)
	WXF.WXFStart(b)
	WXF.WXFAddFIELD_ID(b, field)
	WXF.WXFAddGRID(b, grid)
	WXF.WXFAddVALUES(b, values)
	WXF.WXFAddVALID_TIME_MS(b, uint64(at.UnixMilli()))
	WXF.FinishSizePrefixedWXFBuffer(b, WXF.WXFEnd(b))
	// The ingest splitter removes the stream prefix; format 4 frames the raw
	// FlatBuffer itself when appending it to the record stream.
	return append([]byte(nil), b.FinishedBytes()[4:]...)
}

func seed(root string, count int, now time.Time) {
	ctx := context.Background()
	e, err := format4.Open(ctx, format4.Options{DataRoot: root, Create: format4.CreateFresh,
		GseqFloor: 1, AOTCacheDir: storage.EngineAOTCacheDir()})
	check(err)
	defer func() { check(e.Close(ctx)) }()
	spec, err := format4.TypeSpecFor("WXF.fbs")
	check(err)
	check(e.RegisterType(spec))
	for start := 0; start < count; start += 256 {
		b := format4.Batch{Type: "WXF", Peer: "control-write-e2e", Tags: []format4.Tag{{
			Provider: "weather", Source: "gfs-control", Batch: fmt.Sprintf("history-%d", start/256),
		}}}
		for id := start; id < min(start+256, count); id++ {
			data := weather(id, now.Add(-96*time.Hour))
			c, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: 32}).Sum(data)
			check(err)
			b.Records = append(b.Records, format4.In{CID: c.String(), Plain: data, TS: now.Add(-96 * time.Hour).Unix()})
		}
		out, err := e.Put(ctx, b)
		check(err)
		require(len(out) == len(b.Records), "seed lost records")
		for _, record := range out {
			require(record.Action == format4.ActNew, "seed action=%+v", record)
		}
		fixtureSize(root)
	}
}

type stat struct {
	count         int
	held, maxHeld time.Duration
	syncs         int64
}

type account struct {
	mu    sync.Mutex
	stats map[string]stat
	sql   map[string]string
}

func newAccount() *account { return &account{stats: map[string]stat{}, sql: map[string]string{}} }

func (a *account) snapshot() *account {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := newAccount()
	for key, value := range a.stats {
		out.stats[key] = value
	}
	for key, value := range a.sql {
		out.sql[key] = value
	}
	return out
}

func family(sql string) string {
	q := strings.ToUpper(strings.Join(strings.Fields(sql), " "))
	switch {
	case strings.HasPrefix(q, "INSERT INTO SDN_SOURCE_BATCH_LICENSE"):
		return families[0]
	case strings.HasPrefix(q, "INSERT INTO SDN_DIRECTORY"):
		return families[1]
	case strings.HasPrefix(q, "SELECT LAST_INSERT_ROWID(), CHANGES()"):
		return families[2]
	case strings.HasPrefix(q, "SELECT LICENSE, LICENSE_URL, CITATION, SHARE_ALIKE, UPDATED_AT FROM SDN_SOURCE_BATCH_LICENSE"):
		return "source_batch_license_lookup"
	case strings.HasPrefix(q, "BEGIN"), q == "COMMIT", q == "ROLLBACK":
		return "transaction_" + q
	}
	return "other_control_sql"
}

func (a *account) observe(sample flatsqlrt.QuerySample) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := family(sample.SQL)
	s := a.stats[name]
	s.count++
	s.held += sample.Held
	s.maxHeld = max(s.maxHeld, sample.Held)
	s.syncs += sample.Syncs
	a.stats[name] = s
	if name != "other_control_sql" {
		a.sql[name] = sample.SQL
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func auxiliaryBytes(root string) int64 {
	info, err := os.Stat(filepath.Join(root, "auxiliary.flatsqlmeta"))
	check(err)
	return info.Size()
}

func explain(db *flatsqlrt.Database, a *account) {
	for _, name := range families {
		sql := a.sql[name]
		if sql == "" {
			fmt.Printf("eqp statement=%s absent_from_workload=true\n", name)
			continue
		}
		args := make([]any, strings.Count(sql, "?"))
		res, err := db.Query("EXPLAIN QUERY PLAN "+sql, args...)
		check(err)
		fmt.Printf("eqp statement=%s sql=%q rows=%v\n", name, strings.Join(strings.Fields(sql), " "), res.Rows)
	}
	for _, q := range []string{
		"SELECT license FROM sdn_source_batch_license WHERE schema_name=? AND provider_id=? AND source_name=? AND batch_id=?",
		"SELECT dn FROM sdn_directory WHERE kind=? AND peer_id=?",
	} {
		res, err := db.Query("EXPLAIN QUERY PLAN "+q, make([]any, strings.Count(q, "?"))...)
		check(err)
		fmt.Printf("eqp lookup=%q rows=%v\n", q, res.Rows)
	}
}

// Exercise the public API after reopening, independently of the ingest path:
// reads do not seed the write memo, timestamps do not change license content,
// and changing each content field must write and refresh the memo.
func checkLicenseWrites(label string, count int, root string, s *storage.FlatSQLStore) bool {
	rt, _ := s.EngineRuntime()
	license, found, err := s.SourceBatchLicenseFor("WXF.fbs", "weather", "gfs-control", "live-0")
	check(err)
	require(found, "reopened source batch license missing")
	metrics := newAccount()
	before := auxiliaryBytes(root)
	rt.SetQueryObserver(metrics.observe)
	defer rt.SetQueryObserver(nil)
	check(s.UpsertSourceBatchLicense(license))
	rt.SetQueryObserver(nil)
	metrics = metrics.snapshot()
	appended := auxiliaryBytes(root) - before
	pass := metrics.stats[families[0]].count == 1 && metrics.stats["source_batch_license_lookup"].count == 0 && appended > 0
	fmt.Printf("first_write_after_lookup label=%s records=%d upserts=%d lookups=%d auxiliary_bytes=%d pass=%t\n",
		label, count, metrics.stats[families[0]].count, metrics.stats["source_batch_license_lookup"].count, appended, pass)
	before = auxiliaryBytes(root)
	metrics = newAccount()
	rt.SetQueryObserver(metrics.observe)
	for i := 0; i < callsPerBatch; i++ {
		license.UpdatedAt++
		check(s.UpsertSourceBatchLicense(license))
	}
	rt.SetQueryObserver(nil)
	metrics = metrics.snapshot()
	queries := 0
	for _, st := range metrics.stats {
		queries += st.count
	}
	appended = auxiliaryBytes(root) - before
	unchanged := queries == 0 && appended == 0
	pass = pass && unchanged
	fmt.Printf("unchanged_after_write label=%s records=%d calls=%d control_statements=%d upserts=%d auxiliary_bytes=%d pass=%t\n",
		label, count, callsPerBatch, queries, metrics.stats[families[0]].count, appended, unchanged)
	for _, field := range []string{"license", "license_url", "citation", "share_alike"} {
		switch field {
		case "license":
			license.License = "CC-BY-SA-4.0"
		case "license_url":
			license.LicenseURL = "https://example.test/license-updated"
		case "citation":
			license.Citation = "updated synthetic weather"
		case "share_alike":
			license.ShareAlike = true
		}
		before = auxiliaryBytes(root)
		metrics = newAccount()
		rt.SetQueryObserver(metrics.observe)
		check(s.UpsertSourceBatchLicense(license))
		rt.SetQueryObserver(nil)
		metrics = metrics.snapshot()
		require(metrics.stats[families[0]].count == 1 && auxiliaryBytes(root) > before,
			"changed %s must persist exactly one license write", field)
		got, found, err := s.SourceBatchLicenseFor(license.SchemaName, license.ProviderID, license.SourceName, license.BatchID)
		check(err)
		require(found && got == license, "changed %s lookup: got=%+v want=%+v", field, got, license)
	}
	before = auxiliaryBytes(root)
	metrics = newAccount()
	rt.SetQueryObserver(metrics.observe)
	check(s.UpsertSourceBatchLicense(license))
	rt.SetQueryObserver(nil)
	metrics = metrics.snapshot()
	updatedUnchanged := len(metrics.stats) == 0 && auxiliaryBytes(root) == before
	fmt.Printf("changed_license_fields label=%s records=%d fields=4 unchanged_after_update=%t\n", label, count, updatedUnchanged)
	return pass && updatedUnchanged
}

func runSize(label string, count int, retentionBudget time.Duration) bool {
	diskGuard()
	root, err := os.MkdirTemp(os.Getenv("SDN_CONTROL_E2E_ROOT"), "control-write-fixture-")
	check(err)
	// Match the path spelling used by engine file paths. TMPDIR can end in
	// a slash; the native confined root compares absolute paths literally.
	root = filepath.Clean(root)
	defer func() {
		check(os.RemoveAll(root))
		fmt.Printf("fixture_cleanup records=%d removed=true\n", count)
	}()
	now := time.Now().Truncate(time.Second)
	started := time.Now()
	seed(root, count, now)
	fmt.Printf("fixture label=%s records=%d bytes=%d seed_ms=%.3f\n", label, count, fixtureSize(root), ms(time.Since(started)))
	v, err := sds.NewValidator(nil)
	check(err)
	s, err := storage.NewFlatSQLStore(root, v)
	check(err)
	defer func() {
		if s != nil {
			check(s.Close())
		}
	}()
	rt, db := s.EngineRuntime()
	require(rt.Mode().AOT, "control runtime must use existing AOT cache: %+v", rt.Mode())
	for _, pragma := range []string{"journal_mode", "synchronous", "wal_autocheckpoint"} {
		res, err := db.Query("PRAGMA " + pragma)
		check(err)
		fmt.Printf("pragma %s=%v\n", pragma, res.Rows)
	}
	metrics := newAccount()
	// Delay only the control instance's real host fsyncs. Record partitions
	// keep their normal I/O, matching control contention during NWP ingest.
	rt.FileIO().SetSyncDelayForTesting(syncDelay)
	defer rt.FileIO().SetSyncDelayForTesting(0)
	rt.SetQueryObserver(metrics.observe)
	defer rt.SetQueryObserver(nil)
	beforeAuxiliary := auxiliaryBytes(root)
	fmt.Printf("workload label=%s records=%d fsync_delay_ms=%.3f distinct_batches=%d ingest_calls=%d directory_calls=%d\n",
		label, count, ms(syncDelay), batchCount, batchCount*callsPerBatch, directoryCalls)
	start := make(chan struct{})
	firstIngest := make(chan struct{}, 1)
	errors := make(chan error, 3)
	var wg sync.WaitGroup
	var retention time.Duration
	var expired storage.RecordExpiryResult
	wg.Add(3)
	go func() {
		defer wg.Done()
		defer close(firstIngest)
		<-start
		for batch := 0; batch < batchCount; batch++ {
			tags := storage.SourceTags{ProviderID: "weather", SourceName: "gfs-control", BatchID: fmt.Sprintf("live-%d", batch),
				License: "CC-BY-4.0", LicenseURL: "https://example.test/license", Citation: "synthetic weather"}
			for call := 0; call < callsPerBatch; call++ {
				id := count + 2*(batch*callsPerBatch+call)
				n, err := s.StoreBatchWithSourceTags("WXF.fbs", [][]byte{weather(id, now), weather(id+1, now)}, "control-write-e2e", nil, tags)
				if err != nil || n != 2 {
					errors <- fmt.Errorf("ingest stored=%d: %v", n, err)
					return
				}
				if batch == 0 && call == 0 {
					// A buffered signal keeps retention inside the ingest window.
					firstIngest <- struct{}{}
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < directoryCalls; i++ {
			if err := s.UpsertDirectoryRecord(storage.DirectoryRecord{Kind: "peer", PeerID: fmt.Sprintf("synthetic-peer-%d", i%directoryRows),
				DN: fmt.Sprintf("weather-%d", i), Source: "control-write-e2e", UpdatedAt: now.Unix()}); err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-firstIngest
		began := time.Now()
		var err error
		expired, err = s.ExpireRecords(storage.RecordExpiryQuery{SchemaName: "WXF.fbs", ProviderID: "weather", SourceName: "gfs-control",
			BeforeUnix: now.Add(-72 * time.Hour).Unix(), Batch: 500, MaxDeletes: 500})
		retention = time.Since(began)
		if err != nil {
			errors <- err
		}
	}()
	started = time.Now()
	close(start)
	wg.Wait()
	workload := time.Since(started)
	rt.SetQueryObserver(nil)
	rt.FileIO().SetSyncDelayForTesting(0)
	metrics = metrics.snapshot()
	close(errors)
	for err := range errors {
		check(err)
	}
	pass := true
	names := append([]string(nil), families...)
	for name := range metrics.stats {
		if name != families[0] && name != families[1] && name != families[2] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		st := metrics.stats[name]
		fmt.Printf("control label=%s records=%d statement=%s count=%d syncs=%d total_held_ms=%.3f max_held_ms=%.3f\n",
			label, count, name, st.count, st.syncs, ms(st.held), ms(st.maxHeld))
		if st.maxHeld >= holdBudget {
			pass = false
		}
	}
	issued := metrics.stats[families[0]].count
	lookups := metrics.stats["source_batch_license_lookup"].count
	fmt.Printf("licenses label=%s records=%d upserts_issued=%d distinct_batches=%d ingest_calls=%d lookups=%d auxiliary_bytes=%d\n",
		label, count, issued, batchCount, batchCount*callsPerBatch, lookups, auxiliaryBytes(root)-beforeAuxiliary)
	pass = pass && issued <= batchCount && lookups == 0
	fmt.Printf("retention label=%s records=%d deleted=%d missing=%d page_ms=%.3f budget_ms=%.3f workload_ms=%.3f\n",
		label, count, expired.Deleted, expired.Missing, ms(retention), ms(retentionBudget), ms(workload))
	require(expired.Deleted == 500 && expired.Missing == 0, "retention result=%+v", expired)
	pass = pass && retention <= retentionBudget
	explain(db, metrics)
	fmt.Printf("fixture_peak_checked records=%d bytes=%d\n", count, fixtureSize(root))
	check(s.Close())
	s = nil
	s, err = storage.NewFlatSQLStore(root, v)
	check(err)
	for i := 0; i < batchCount; i++ {
		license, found, err := s.SourceBatchLicenseFor("WXF.fbs", "weather", "gfs-control", fmt.Sprintf("live-%d", i))
		check(err)
		require(found && license.License == "CC-BY-4.0" && license.Citation == "synthetic weather", "license durability failed: batch=%d %+v", i, license)
	}
	directory, err := s.QueryDirectory(storage.DirectoryQuery{Kind: "peer", Source: "control-write-e2e", Limit: 100})
	check(err)
	require(len(directory) == directoryRows, "directory durability: got=%d want=%d", len(directory), directoryRows)
	fmt.Printf("durability label=%s records=%d licenses=%d directory=%d PASS\n", label, count, batchCount, len(directory))
	pass = checkLicenseWrites(label, count, root, s) && pass
	fmt.Printf("result label=%s records=%d pass=%t held_budget_ms=250\n", label, count, pass)
	return pass
}

func run() (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	label := flag.String("label", "measurement", "report label, for example BEFORE or AFTER")
	budget := flag.Duration("retention-budget", 2*time.Second, "maximum time for one 500-record retention page")
	flag.Parse()
	require(*budget > 0 && *budget <= 2*time.Second, "retention budget must be positive and at most 2s")
	check(os.Setenv("SDN_STORE_FORMAT", "4"))
	fmt.Printf("engine=%s sha256=%s\n", flatsqlrt.P4ThreadsPackage, flatsqlrt.P4ThreadsDigest())
	ok, why := format4.Runnable()
	require(ok, "format 4 unavailable: %s", why)
	pass := true
	for _, n := range []int{2048, 8192} {
		if !runSize(*label, n, *budget) {
			pass = false
		}
	}
	require(pass, "control hold, duplicate license write, or retention budget exceeded")
	fmt.Printf("PASS label=%s control_hold_budget_ms=250 retention_budget_ms=%.3f\n", *label, ms(*budget))
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
}
