package main

// store-migrate against real legacy stores (design §16, T6 #1). The engine
// half needs the patched WasmEdge runtime (the release's static build, CI's
// substrate lane) and skips on an upstream library unless
// SDN_WASM_REQUIRE_PATCHED=1.
//
// SDN_MIGRATE_FIXTURE=<dir> additionally migrates a clone of a populated
// fixture store (<dir>/store, internal/stress TestPartitionStoreFixture) and
// reports the rate, machine and load.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

func requirePSEngine(t testing.TB) {
	t.Helper()
	if !flatsqlrt.NativeHostIOSupported() {
		t.Skip("the C host I/O module is not available on this platform")
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatalf("runtime is not patched: %+v", rep)
		}
		t.Skipf("linked libwasmedge lacks the SDN runtime patches (%+v)", rep)
	}
}

func migrateTestAOTDir(t testing.TB) string {
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return filepath.Join(base, "sdn-format2-test-aot")
}

func migrateTestOMM(norad uint32, epoch time.Time, name string) []byte {
	b := flatbuffers.NewBuilder(256)
	id := b.CreateString(fmt.Sprintf("2026-%03dA", norad%1000))
	nm := b.CreateString(name)
	ep := b.CreateString(epoch.UTC().Format("2006-01-02T15:04:05.000000"))
	OMM.OMMStart(b)
	OMM.OMMAddOBJECT_NAME(b, nm)
	OMM.OMMAddOBJECT_ID(b, id)
	OMM.OMMAddEPOCH(b, ep)
	OMM.OMMAddMEAN_MOTION(b, 15.5)
	OMM.OMMAddNORAD_CAT_ID(b, norad)
	root := OMM.OMMEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$OMM"))
	return append([]byte(nil), b.FinishedBytes()...)
}

func migrateTestCAT(norad uint32, name string) []byte {
	b := flatbuffers.NewBuilder(256)
	nm := b.CreateString(name)
	id := b.CreateString(fmt.Sprintf("1990-%05dA", norad))
	CAT.CATStart(b)
	CAT.CATAddOBJECT_NAME(b, nm)
	CAT.CATAddOBJECT_ID(b, id)
	CAT.CATAddNORAD_CAT_ID(b, norad)
	root := CAT.CATEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$CAT"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// buildLegacyStore writes a small legacy store through the daemon's own
// write path: two OMM producers sharing CIDs (REPEAT copies), records with
// several source tags (RETAG), CAT records that supersede, and a licence.
func buildLegacyStore(t *testing.T, dir string) {
	t.Helper()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(dir, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var omm [][]byte
	for i := 0; i < 300; i++ {
		omm = append(omm, migrateTestOMM(uint32(20000+i), base.Add(time.Duration(i)*time.Minute), fmt.Sprintf("OBJ-%d", i)))
	}
	gp := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-001",
		License: "CC-BY-4.0", LicenseURL: "https://creativecommons.org/licenses/by/4.0/"}
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", omm[:200], "source:celestrak", nil, gp); err != nil {
		t.Fatal(err)
	}
	// The next batch re-tags the first 50 records (a second tag row each).
	gp2 := gp
	gp2.BatchID = "gp-002"
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", omm[:50], "source:celestrak", nil, gp2); err != nil {
		t.Fatal(err)
	}
	// A second producer holds 100 of the same CIDs (REPEAT copies) and 100 new.
	peer := storage.SourceTags{ProviderID: "space-data-network-01", SourceName: "mirror", BatchID: "m-1"}
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", omm[100:300], "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", nil, peer); err != nil {
		t.Fatal(err)
	}
	var cat [][]byte
	for i := 0; i < 40; i++ {
		cat = append(cat, migrateTestCAT(uint32(100+i), fmt.Sprintf("SAT %d", i)))
	}
	satcat := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-satcat", BatchID: "sc-1"}
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", cat, "source:celestrak", nil, satcat); err != nil {
		t.Fatal(err)
	}
	// 10 objects change: their new editions supersede the old ones.
	var changed [][]byte
	for i := 0; i < 10; i++ {
		changed = append(changed, migrateTestCAT(uint32(100+i), fmt.Sprintf("SAT %d (renamed)", i)))
	}
	satcat.BatchID = "sc-2"
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", changed, "source:celestrak", nil, satcat); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestStoreMigrateCopiesVerifiesAndActivates(t *testing.T) {
	requirePSEngine(t)
	dir := t.TempDir()
	buildLegacyStore(t, dir)
	var log bytes.Buffer
	rep, err := migrateStore(context.Background(), migrateOptions{Store: dir, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, PageRows: 64}, &log)
	if err != nil {
		t.Fatalf("migrate: %v\n%s\nreport %+v\nverification %+v", err, log.String(), rep, rep.Verification)
	}
	v := rep.Verification
	if v == nil || !v.PartitionsEqual || !v.LanesEqual || !v.CIDSequences {
		t.Fatalf("verification %+v", v)
	}
	if v.Partitions != 3 || v.CIDsCompared != 340 {
		t.Fatalf("verified %d partitions and %d cids, want 3 and 340 (300 OMM + 40 CAT)", v.Partitions, v.CIDsCompared)
	}
	if !rep.Activated {
		t.Fatal("not activated")
	}
	if ok, err := format2.Migrated(dir); err != nil || !ok {
		t.Fatalf("fsql2 MIGRATED: %v %v", ok, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "control.flatsqldb")); err != nil || !fi.IsDir() {
		t.Fatalf("control.flatsqldb placeholder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pre-format2", "control.flatsqldb")); err != nil {
		t.Fatalf("legacy control database not retained: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "control2.flatsqldb")); err != nil || fi.Size() == 0 {
		t.Fatalf("control copy: %v", err)
	}
	t.Logf("control copy: %v", rep.Extra["control_copy"])
	t.Logf("migrated %d records (%d entries) in %s; %s", rep.Records, rep.Entries, rep.Took, strings.Join(v.MaxRowID, "; "))

	// A5: a legacy (pre-T6) open of the activated store fails and creates
	// no file.
	before := listDir(t, dir)
	sv, _ := sds.NewValidator(nil)
	if s, err := storage.NewFlatSQLStore(dir, sv, storage.WithDeferredBootRebuilds()); err == nil {
		s.Close()
		t.Fatal("a legacy open of an activated store succeeded")
	}
	if after := listDir(t, dir); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("the refused legacy open created files:\nbefore %v\nafter  %v", before, after)
	}
	// A second run is a no-op on an activated store.
	rep2, err := migrateStore(context.Background(), migrateOptions{Store: dir, AOTCacheDir: migrateTestAOTDir(t)}, &log)
	if err != nil || !rep2.Activated {
		t.Fatalf("rerun on an activated store: %v %+v", err, rep2)
	}
}

func TestStoreMigrateResumesAfterAnAbort(t *testing.T) {
	requirePSEngine(t)
	dir := t.TempDir()
	buildLegacyStore(t, dir)
	var log bytes.Buffer
	opt := migrateOptions{Store: dir, AOTCacheDir: migrateTestAOTDir(t), CompileOnMiss: true, PageRows: 32, NoActivate: true}
	for _, at := range []int64{40, 130, 260} {
		o := opt
		o.testAbortAfter = at
		if _, err := migrateStore(context.Background(), o, &log); err != errMigrateTestAbort {
			t.Fatalf("abort at %d: %v\n%s", at, err, log.String())
		}
	}
	rep, err := migrateStore(context.Background(), opt, &log)
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, log.String())
	}
	if !rep.Resumed || rep.Verification == nil || rep.Verification.CIDsCompared != 340 {
		t.Fatalf("resumed run %+v", rep)
	}
}

const migrateKillChildEnv = "SDN_MIGRATE_KILL_CHILD_STORE"

// TestStoreMigrateKillChild is the child the kill -9 test runs and kills.
func TestStoreMigrateKillChild(t *testing.T) {
	dir := os.Getenv(migrateKillChildEnv)
	if dir == "" {
		t.Skip("runs only as the kill -9 test's child")
	}
	_, _ = migrateStore(context.Background(), migrateOptions{Store: dir, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, PageRows: 64, NoActivate: true}, nil)
}

// buildLegacyStoreN is buildLegacyStore with n more OMM records in extra
// batches, so a migration runs long enough to be killed at many points.
func buildLegacyStoreN(t *testing.T, dir string, n int) {
	t.Helper()
	buildLegacyStore(t, dir)
	v, _ := sds.NewValidator(nil)
	s, err := storage.NewFlatSQLStore(dir, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	for b := 0; b*500 < n; b++ {
		var recs [][]byte
		for i := b * 500; i < (b+1)*500 && i < n; i++ {
			recs = append(recs, migrateTestOMM(uint32(40000+i), base.Add(time.Duration(i)*time.Second), fmt.Sprintf("K-%d", i)))
		}
		tags := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: fmt.Sprintf("k-%03d", b)}
		peer := "source:celestrak"
		if b%3 == 2 {
			peer = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
		}
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", recs, peer, nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// migrationDigest is a migration's logical output: every partition's
// counters and every type's arrivals (cid order), read from the engine.
func migrationDigest(t *testing.T, dir string) string {
	t.Helper()
	ns, err := flatsqlrt.OpenNativeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Release()
	r, err := format2.OpenReader(format2.InstanceOptions{Store: ns, AOTCacheDir: migrateTestAOTDir(t), CompileOnMiss: true},
		flatsqlrt.PSRoleBulk, format2.ReaderConfig{Root: migrateStagingDir, Lanes: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	var b strings.Builder
	ctx := context.Background()
	res, err := r.Query(ctx, format2.Request{SQL: "SELECT sql_name, live_count, live_bytes FROM flatsql_partitions ORDER BY sql_name"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range res.Rows {
		fmt.Fprintf(&b, "P %s %d %d\n", row[0].String(), row[1].I, row[2].I)
	}
	res, err = r.Query(ctx, format2.Request{SQL: `SELECT type, provider, source, batch, SUM(count), SUM(bytes) FROM flatsql_lanes
		GROUP BY type, provider, source, batch ORDER BY type, provider, source, batch`})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range res.Rows {
		fmt.Fprintf(&b, "L %s %s %s %s %d %d\n", row[0].String(), row[1].String(), row[2].String(), row[3].String(), row[4].I, row[5].I)
	}
	for _, typ := range []string{"OMM", "CAT"} {
		res, err := r.Query(ctx, format2.Request{SQL: fmt.Sprintf(`SELECT _cid_bin FROM "%s" ORDER BY _gseq`, typ)})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range res.Rows {
			fmt.Fprintf(&b, "%s %x\n", typ, row[0].B)
		}
	}
	return b.String()
}

// T6 #1: kill -9 at many points of a migration, resume, and the output is
// logically identical to an uninterrupted migration of the same store.
// SDN_MIGRATE_KILLS sets the number of kills (acceptance: 200).
func TestStoreMigrateResumesAfterKill9(t *testing.T) {
	requirePSEngine(t)
	kills := 6
	if v := os.Getenv("SDN_MIGRATE_KILLS"); v != "" {
		fmt.Sscanf(v, "%d", &kills)
	}
	legacy := t.TempDir()
	buildLegacyStoreN(t, legacy, 3000)
	clone := func(name string) string {
		dst := filepath.Join(t.TempDir(), name)
		cp := exec.Command("cp", "-R", legacy, dst)
		if out, err := cp.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v %s", err, out)
		}
		return dst
	}
	clean := clone("clean")
	if _, err := migrateStore(context.Background(), migrateOptions{Store: clean, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, PageRows: 64, NoActivate: true}, nil); err != nil {
		t.Fatalf("clean migration: %v", err)
	}
	want := migrationDigest(t, clean)

	// Rounds of up to 10 kills, each on a fresh clone, so every kill lands
	// inside a migration that still has work (startup, legacy open, engine
	// open and recovery, both copy phases, labeling waits, verification).
	rng := time.Now().UnixNano()
	var delays []time.Duration
	for done := 0; done < kills; {
		killed := clone(fmt.Sprintf("killed-%d", done))
		round := kills - done
		if round > 10 {
			round = 10
		}
		for i := 0; i < round; i++ {
			cmd := exec.Command(os.Args[0], "-test.run", "^TestStoreMigrateKillChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), migrateKillChildEnv+"="+killed)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			rng = rng*6364136223846793005 + 1442695040888963407
			d := 100*time.Millisecond + time.Duration(uint64(rng)>>33)%(900*time.Millisecond)
			delays = append(delays, d)
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case <-exited:
			case <-time.After(d):
				_ = cmd.Process.Signal(syscall.SIGKILL)
				<-exited
			}
		}
		done += round
		rep, err := migrateStore(context.Background(), migrateOptions{Store: killed, AOTCacheDir: migrateTestAOTDir(t),
			CompileOnMiss: true, PageRows: 64, NoActivate: true}, nil)
		if err != nil {
			t.Fatalf("resume after kill %d: %v", done, err)
		}
		if v := rep.Verification; v == nil || !v.PartitionsEqual || !v.LanesEqual || !v.CIDSequences {
			t.Fatalf("verification after kill %d: %+v", done, rep.Verification)
		}
		if got := migrationDigest(t, killed); got != want {
			t.Fatalf("output after kill %d differs from the uninterrupted migration", done)
		}
	}
	t.Logf("MEASURED %d kill -9 points (delays %v): resumed output identical (%d lines)", kills, delays, strings.Count(want, "\n"))
}

// TestStoreMigrateFixture migrates a clone of a populated host-02-shaped
// fixture (SDN_MIGRATE_FIXTURE=<dir holding store/>) and reports the rate.
func TestStoreMigrateFixture(t *testing.T) {
	src := strings.TrimSpace(os.Getenv("SDN_MIGRATE_FIXTURE"))
	if src == "" {
		t.Skip("SDN_MIGRATE_FIXTURE names a populated fixture directory")
	}
	requirePSEngine(t)
	work := os.Getenv("SDN_MIGRATE_WORK")
	if work == "" {
		work = t.TempDir()
	}
	dst := filepath.Join(work, "store")
	cp := exec.Command("cp", "-cR", filepath.Join(src, "store"), dst)
	if runtime.GOOS != "darwin" {
		cp = exec.Command("cp", "-R", "--reflink=auto", filepath.Join(src, "store"), dst)
	}
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatalf("clone fixture: %v: %s", err, out)
	}
	inv, err := migrateInventory(dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, ty := range inv.Types {
		t.Logf("inventory %s: %d partitions, %d records, %d B, p50 %d p99 %d max %d", ty.Schema, ty.Partitions, ty.Rows, ty.Bytes, ty.P50, ty.P99, ty.Max)
	}
	t.Logf("inventory took %s; source %d B; max rowid %d; gseq floor %d", inv.Took, inv.SourceBytes, inv.MaxRowID, inv.GseqFloor)
	load := loadAverage()
	var log bytes.Buffer
	rep, err := migrateStore(context.Background(), migrateOptions{Store: dst, AOTCacheDir: migrateTestAOTDir(t),
		CompileOnMiss: true, NoActivate: os.Getenv("SDN_MIGRATE_ACTIVATE") != "1"}, &log)
	if err != nil {
		t.Fatalf("migrate: %v\n%s\n%+v", err, log.String(), rep)
	}
	t.Logf("MEASURED store-migrate: %d records, %d entries, %d record B, source %d B in %s: %.1f MB/s of records, %.1f MB/s of source (load %.1f -> %.1f, %s/%s, %d CPUs)",
		rep.Records, rep.Entries, rep.RecordBytes, rep.SourceBytes, rep.Took, rep.RecordMBps, rep.SourceMBps, load, loadAverage(),
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	t.Logf("time: %v; engine %+v", rep.Extra, rep.Engine)
	if v := rep.Verification; v != nil {
		t.Logf("verification: partitions %v (%d), lanes %v (%d), cid sequences %v (%d cids), %s; %s", v.PartitionsEqual, v.Partitions,
			v.LanesEqual, v.Lanes, v.CIDSequences, v.CIDsCompared, v.Took, strings.Join(v.MaxRowID, "; "))
	}
}

func loadAverage() float64 {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		raw, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return -1
		}
		out = raw
	}
	var a float64
	fmt.Sscanf(strings.Trim(strings.TrimSpace(string(out)), "{} "), "%f", &a)
	return a
}

// TestStoreMigrateInventoryOfStores prints the --inventory of APFS clones of
// the stores named in SDN_MIGRATE_INVENTORY (colon separated), read-only for
// the originals: the largest record per type decides whether A27's jumbo
// frames are needed (the published engine's ring entry is 1 MiB + 4 KiB).
func TestStoreMigrateInventoryOfStores(t *testing.T) {
	list := strings.TrimSpace(os.Getenv("SDN_MIGRATE_INVENTORY"))
	if list == "" {
		t.Skip("SDN_MIGRATE_INVENTORY names stores to inventory")
	}
	for _, src := range strings.Split(list, ":") {
		dst := filepath.Join(t.TempDir(), "store")
		if err := os.MkdirAll(dst, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"control.flatsqldb", "control.flatsqldb-wal", "control.flatsqldb.fsdata", "auxiliary.flatsqlmeta"} {
			if _, err := os.Stat(filepath.Join(src, name)); err != nil {
				continue
			}
			if out, err := exec.Command("cp", "-c", filepath.Join(src, name), filepath.Join(dst, name)).CombinedOutput(); err != nil {
				t.Fatalf("clone %s: %v %s", name, err, out)
			}
		}
		inv, err := migrateInventory(dst)
		if err != nil {
			t.Logf("inventory %s: %v", src, err)
			continue
		}
		for _, ty := range inv.Types {
			over := ""
			if ty.Max > maxEngineEntryPayload {
				over = "  <-- over the engine's ring entry"
			}
			t.Logf("INVENTORY %s %s: %d partitions, %d records, %d B, p50 %d p99 %d max %d%s", filepath.Base(filepath.Dir(src)), ty.Schema,
				ty.Partitions, ty.Rows, ty.Bytes, ty.P50, ty.P99, ty.Max, over)
		}
	}
}

// §22.4-2: --from-snapshot on a consistent copy while the store keeps
// taking writes, then --delta on the stopped store, equals a full offline
// migration of the same final state (partitions, counters, lanes, and every
// type's cid sequence).
func TestStoreMigrateSnapshotPlusDeltaEqualsAFullMigration(t *testing.T) {
	requirePSEngine(t)
	live := t.TempDir()
	buildLegacyStore(t, live)
	cloneLegacy := func(src string) string {
		dst := filepath.Join(t.TempDir(), "store")
		if err := os.MkdirAll(dst, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"control.flatsqldb", "control.flatsqldb-wal", "control.flatsqldb.fsdata", "auxiliary.flatsqlmeta"} {
			if _, err := os.Stat(filepath.Join(src, name)); err != nil {
				continue
			}
			if out, err := exec.Command("cp", filepath.Join(src, name), filepath.Join(dst, name)).CombinedOutput(); err != nil {
				t.Fatalf("copy %s: %v %s", name, err, out)
			}
		}
		return dst
	}
	snap := cloneLegacy(live)
	ctx := context.Background()
	opt := migrateOptions{Store: live, AOTCacheDir: migrateTestAOTDir(t), CompileOnMiss: true, PageRows: 64}
	so := opt
	so.Snapshot = snap
	if _, err := migrateStore(ctx, so, nil); err != nil {
		t.Fatalf("snapshot pass: %v", err)
	}
	// The daemon kept running: new records, re-tags of old ones, a new CAT
	// edition (supersede), a new producer.
	v, _ := sds.NewValidator(nil)
	s, err := storage.NewFlatSQLStore(live, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	var omm [][]byte
	for i := 0; i < 120; i++ {
		omm = append(omm, migrateTestOMM(uint32(26000+i), base.Add(time.Duration(i)*time.Minute), fmt.Sprintf("NEW-%d", i)))
	}
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", omm, "source:celestrak", nil,
		storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-010"}); err != nil {
		t.Fatal(err)
	}
	old := make([][]byte, 0, 30)
	obase := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 150; i < 180; i++ {
		old = append(old, migrateTestOMM(uint32(20000+i), obase.Add(time.Duration(i)*time.Minute), fmt.Sprintf("OBJ-%d", i)))
	}
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", old, "source:celestrak", nil,
		storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "gp-011"}); err != nil {
		t.Fatal(err)
	}
	var cat [][]byte
	for i := 0; i < 8; i++ {
		cat = append(cat, migrateTestCAT(uint32(120+i), fmt.Sprintf("SAT %d (edition 3)", i)))
	}
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", cat, "source:celestrak", nil,
		storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-satcat", BatchID: "sc-3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreBatchWithSourceTags("OMM.fbs", omm[:40], "16Uiu2HAmNewPeer", nil,
		storage.SourceTags{ProviderID: "space-data-network-03", SourceName: "relay", BatchID: "r-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	full := cloneLegacy(live)
	do := opt
	do.Delta = true
	do.NoActivate = true
	rep, err := migrateStore(ctx, do, nil)
	if err != nil {
		t.Fatalf("delta: %v\nverification %+v", err, rep.Verification)
	}
	fo := opt
	fo.Store = full
	fo.NoActivate = true
	if _, err := migrateStore(ctx, fo, nil); err != nil {
		t.Fatalf("full migration: %v", err)
	}
	if got, want := migrationDigest(t, live), migrationDigest(t, full); got != want {
		t.Fatalf("snapshot + delta differs from a full migration of the final state")
	}
}
