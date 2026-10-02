package format4_test

// The lifecycle on the REAL engine: the embedded release, or
// SDN_P4_WASM (a build of the engine's task branch) during development. They
// need the patched runtime (the release's static WasmEdge; CI's substrate
// lane) and skip without an engine or on an upstream library, unless
// SDN_WASM_REQUIRE_PATCHED=1.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/internal/cidv1"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

var (
	realAOTOnce sync.Once
	realAOTDir  string
)

// realWasm is the engine under test, or a skip.
func realWasm(t testing.TB) []byte {
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
	if p := os.Getenv("SDN_P4_WASM"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if len(flatsqlrt.P4ThreadsWasm()) == 0 {
		t.Skip("no format-4 engine: none is embedded yet and SDN_P4_WASM is unset")
	}
	return flatsqlrt.P4ThreadsWasm()
}

// ommRecord builds a deterministic OMM record (size-prefixed, as the SDN
// builders emit it).
func ommRecord(norad uint32, objectID, epoch string) []byte {
	return sds.NewOMMBuilder().WithNoradCatID(norad).WithObjectID(objectID).WithObjectName(fmt.Sprintf("SAT-%d", norad)).
		WithEpoch(epoch).WithCreationDate("2026-09-30T00:00:00Z").Build()
}

// putIn is a PUT record of plain at source time ts.
func putIn(plain []byte, ts int64) format4.In {
	return format4.In{CID: cidv1.Of(plain), Plain: plain, TS: ts}
}

// realAOT is a per-user AOT cache: the key is the artifact's sha256 and the
// runtime tag, so a stale entry never loads and a warm run skips the compile.
func realAOT(t testing.TB) string {
	realAOTOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		realAOTDir = filepath.Join(base, "sdn-format4-test-aot")
	})
	return realAOTDir
}

func openReal(t testing.TB, root string, mode format4.CreateMode, floor uint64, onFail func(error)) *format4.Engine {
	t.Helper()
	wasm := realWasm(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	e, err := format4.Open(ctx, format4.Options{DataRoot: root, Create: mode, GseqFloor: floor, Cores: 4, Wasm: wasm,
		AOTCacheDir: realAOT(t), CompileOnMiss: true, OnFailure: onFail})
	if err != nil {
		t.Fatalf("Open(%s, mode %d): %v", root, mode, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = e.Close(ctx)
	})
	return e
}

// Create modes, markers and activation through the real engine (§2.2, §2.3).
func TestRealEngineCreateModesAndActivation(t *testing.T) {
	realWasm(t)
	ctx := ctxT(t)

	fresh := t.TempDir()
	e := openReal(t, fresh, format4.CreateFresh, 0, nil)
	if m, err := marker.Read(fresh); err != nil || !m.Activated() || m.MigratedFrom != 0 {
		t.Fatalf("fresh store markers: %+v %v", m, err)
	}
	if err := e.Activate(ctx); !errors.Is(err, format4.ErrFormat) {
		t.Fatalf("Activate on a fresh store: %v, want ErrFormat", err)
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	openReal(t, fresh, format4.OpenExisting, 0, nil)

	// A migration target writes nothing until Activate; then MIGRATED and
	// STORE carry the floor and migrated_from 1, and new seqs start above it.
	const floor = 1060922
	mig := t.TempDir()
	m := openReal(t, mig, format4.CreateForMigration, floor, nil)
	if mk, _ := marker.Read(mig); mk.Format4() {
		t.Fatalf("a migration target wrote markers before activation: %+v", mk)
	}
	spec, err := format4.TypeSpecFor("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	plain := ommRecord(25544, "1998-067A", "2026-09-01T00:00:00Z")
	in := putIn(plain, 1790000000)
	in.Seq = 777
	out, err := m.Put(ctx, format4.Batch{Type: "OMM", Peer: "12D3KooWMigrated", Mode: format4.ModeMigrate,
		Tags: []format4.Tag{{Provider: "celestrak", Source: "celestrak-gp", Batch: "b1"}}, Records: []format4.In{in}})
	if err != nil || out[0].Action != format4.ActMigrated || out[0].Seq != 777 {
		t.Fatalf("migrate put: %+v %v", out, err)
	}
	if _, err := m.Rebuild(ctx, "", format4.RebuildPartitionIndexes|format4.RebuildTypeIndex); err != nil {
		t.Fatal(err)
	}
	if err := m.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	mk, err := marker.Read(mig)
	if err != nil || !mk.Activated() || mk.GseqFloor != floor || mk.MigratedFrom != 1 || !mk.NeedsFinish() {
		t.Fatalf("after Activate: %+v %v", mk, err)
	}
	if err := marker.FinishActivation(mig); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r := openReal(t, mig, format4.OpenExisting, 0, nil)
	if err := r.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	recs, err := r.Get(ctx, "OMM", []string{in.CID}, false, true)
	if err != nil || len(recs) != 1 || recs[0].Seq != 777 || string(recs[0].Data) != string(plain) {
		t.Fatalf("migrated record after reopen: %+v %v", recs, err)
	}
	next := putIn(ommRecord(43013, "2017-073A", "2026-09-02T00:00:00Z"), 1790000001)
	out, err = r.Put(ctx, format4.Batch{Type: "OMM", Peer: "12D3KooWMigrated", Records: []format4.In{next}})
	if err != nil || out[0].Action != format4.ActNew || out[0].Seq < floor {
		t.Fatalf("first new seq after migration: %+v %v (floor %d)", out, err, floor)
	}
}

// Every remaining op round-trips: rebuild verify, quota, the summaries,
// stats; SQL and SURFACE answer (rows, or a status) and never trap.
func TestRealEngineEveryOp(t *testing.T) {
	realWasm(t)
	ctx := ctxT(t)
	e := openReal(t, t.TempDir(), format4.CreateFresh, 0, nil)
	spec, err := format4.TypeSpecFor("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	var ins []format4.In
	for i := 0; i < 50; i++ {
		ins = append(ins, putIn(ommRecord(uint32(40000+i), "2020-001A", "2026-08-01T00:00:00Z"), 1790000000))
	}
	if _, err := e.Put(ctx, format4.Batch{Type: "OMM", Peer: "p", Tags: []format4.Tag{{Provider: "x", Source: "y", Batch: "b"}},
		Records: ins}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.Rebuild(ctx, "OMM", format4.RebuildVerify)
	if err != nil || len(rows) != 1 || rows[0].Mismatches != 0 {
		t.Fatalf("rebuild verify: %+v %v", rows, err)
	}
	for name, call := range map[string]func() error{
		"disk":  func() error { _, err := e.Disk(ctx); return err },
		"fts":   func() error { _, err := e.FTS(ctx); return err },
		"parts": func() error { _, err := e.Partitions(ctx); return err },
		"lanes": func() error { _, err := e.Lanes(ctx, ""); return err },
		"types": func() error { _, err := e.Types(ctx); return err },
		"quota": func() error { _, err := e.QuotaGC(ctx, 1<<40); return err },
		"stats": func() error { _, err := e.Stats(); return err },
	} {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var se *format4.StatusError
	if _, err := e.Surface(ctx); err != nil && !errors.As(err, &se) {
		t.Fatalf("surface: %v", err)
	}
	if _, err := e.SQL(ctx, format4.SQLRequest{SQL: "SELECT 1"}, nil); err != nil && !errors.As(err, &se) {
		t.Fatalf("sql: %v", err)
	}
}

// Close drains within its deadline; afterwards every call is ErrStopped.
func TestRealEngineStopDrainsWithinTheDeadline(t *testing.T) {
	realWasm(t)
	ctx := ctxT(t)
	e := openReal(t, t.TempDir(), format4.CreateFresh, 0, nil)
	spec, _ := format4.TypeSpecFor("OMM.fbs")
	if err := e.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		in := putIn(ommRecord(uint32(1000+i), "x", "2026-08-01T00:00:00Z"), 1790000000)
		if _, err := e.Put(ctx, format4.Batch{Type: "OMM", Peer: "p", Records: []format4.In{in}}); err != nil {
			t.Fatal(err)
		}
	}
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := e.Close(cctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if took := time.Since(start); took > 11*time.Second {
		t.Fatalf("close took %s, deadline 10s", took)
	} else {
		t.Logf("close drained in %s", took.Round(time.Millisecond))
	}
	if _, err := e.Head(ctx, format4.Query{Type: "OMM"}); !errors.Is(err, format4.ErrStopped) {
		t.Fatalf("Head after Close: %v", err)
	}
}

// A trap fences the engine (every call ErrStopped, OnFailure runs); a
// reopen replays the store and serves every acked record.
func TestRealEngineTrapFencesAndReopenReplays(t *testing.T) {
	realWasm(t)
	ctx := ctxT(t)
	root := t.TempDir()
	failed := make(chan error, 1)
	e := openReal(t, root, format4.CreateFresh, 0, func(err error) { failed <- err })
	spec, _ := format4.TypeSpecFor("OMM.fbs")
	if err := e.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	var cids []string
	for i := 0; i < 10; i++ {
		in := putIn(ommRecord(uint32(2000+i), "x", "2026-08-01T00:00:00Z"), 1790000000)
		if _, err := e.Put(ctx, format4.Batch{Type: "OMM", Peer: "p", Records: []format4.In{in}}); err != nil {
			t.Fatal(err)
		}
		cids = append(cids, in.CID)
	}
	if !format4.FenceForTest(e, errors.New("injected trap")) {
		t.Fatal("the engine has no instance to fence")
	}
	select {
	case <-failed:
	case <-time.After(10 * time.Second):
		t.Fatal("OnFailure did not run")
	}
	if _, err := e.Get(ctx, "OMM", cids, false, false); !errors.Is(err, format4.ErrStopped) {
		t.Fatalf("Get on a fenced engine: %v", err)
	}
	r := openReal(t, root, format4.OpenExisting, 0, nil)
	if err := r.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	recs, err := r.Get(ctx, "OMM", cids, false, false)
	if err != nil || len(recs) != len(cids) {
		t.Fatalf("after reopen: %d of %d acked records, %v", len(recs), len(cids), err)
	}
}
