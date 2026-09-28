package flatsqlrt

// Partition-store instance substrate tests (design §18 T5, A21-A24, A29, A30).
//
// They drive testdata ps-probe.wasm (sdn-server/internal/wasmrt/testdata), a
// stand-in engine built exactly like the real one. Tests that need the
// patched runtime skip on an unpatched library unless
// SDN_WASM_REQUIRE_PATCHED=1, which the release workflow sets. Long runs
// (30 min soak, 1 h service thread, 10^6 doorbell transitions) take their
// duration from SDN_PS_SOAK and friends; defaults are short.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

var (
	psAOTDir     string
	psAOTDirOnce sync.Once
)

func TestMain(m *testing.M) {
	if child := os.Getenv(childEnv); child != "" {
		os.Exit(runChild(child))
	}
	code := m.Run()
	if psAOTDir != "" {
		os.RemoveAll(psAOTDir)
	}
	os.Exit(code)
}

func sharedPSAOTDir(t testing.TB) string {
	psAOTDirOnce.Do(func() {
		d, err := os.MkdirTemp("", "sdn-ps-aot-")
		if err != nil {
			t.Fatal(err)
		}
		psAOTDir = d
	})
	return psAOTDir
}

func psProbeWasm(t testing.TB) []byte {
	b, err := os.ReadFile(filepath.Join("..", "wasmrt", "testdata", "ps-probe.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// requirePatched skips (or, under SDN_WASM_REQUIRE_PATCHED=1, fails) when the
// linked runtime lacks the SDN patches.
func requirePatched(t testing.TB) {
	t.Helper()
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatalf("runtime is not patched: %+v", rep)
		}
		t.Skipf("linked libwasmedge lacks the SDN runtime patches (%+v); build it with scripts/build-static-wasmedge.sh", rep)
	}
}

// perfGate reports whether latency and scaling thresholds are asserted. They
// are acceptance numbers for a named machine (design §18: host-tier gates are
// ops evidence with receipts), so a shared CI runner reports them and
// SDN_PS_ACCEPTANCE=1 on the acceptance machine enforces them. Functional
// outcomes (errors, bytes, fencing) are asserted everywhere.
func perfGate() bool { return os.Getenv("SDN_PS_ACCEPTANCE") == "1" }

func envDuration(t testing.TB, name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return d
}

func envInt(t testing.TB, name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return n
}

// Probe modes (ps-probe.c).
const (
	probeDoorbell = 1
	probeCPU      = 2
	probeSpin     = 3
	probeIO       = 4
	probeHammer   = 5
)

type probeConfig struct {
	Mode, Threads, ActiveWaitUs, IdleWaitUs uint32
	IOOps, IOSize, IOFileBytes, StackBytes  uint32
	Marker, Files                           uint32
	Prefix                                  string
	// DropNotify > 0 makes the probe's wake export skip every Nth doorbell
	// notify while recording it as issued: a runtime that loses wakeups (the
	// negative control for the lost-wakeup count).
	DropNotify uint32
}

func (c probeConfig) bytes() []byte {
	b := make([]byte, 64+len(c.Prefix))
	for i, v := range []uint32{c.Mode, c.Threads, c.ActiveWaitUs, c.IdleWaitUs, c.IOOps, c.IOSize, c.IOFileBytes,
		c.StackBytes, c.Marker, c.Files, uint32(len(c.Prefix)), c.DropNotify} {
		binary.LittleEndian.PutUint32(b[4*i:], v)
	}
	copy(b[64:], c.Prefix)
	return b
}

type probeOpts struct {
	role     PSRole
	store    *NativeStore
	root     string
	fdBudget int
	stale    time.Duration
	stop     time.Duration
	control  time.Duration
	onFail   func(*PSInstance, error)
}

func openProbe(t testing.TB, pc probeConfig, o probeOpts) *PSInstance {
	t.Helper()
	if o.role == 0 {
		o.role = PSRoleWriter
	}
	if o.store == nil && o.root == "" {
		o.root = t.TempDir()
	}
	if pc.ActiveWaitUs == 0 {
		pc.ActiveWaitUs = 5000
	}
	if pc.IdleWaitUs == 0 {
		pc.IdleWaitUs = 50000
	}
	if pc.StackBytes == 0 {
		pc.StackBytes = 512 << 10
	}
	if o.stale == 0 {
		o.stale = -1
	}
	p, err := OpenPSInstance(PSConfig{
		Role:           o.role,
		Wasm:           psProbeWasm(t),
		AOTCacheDir:    sharedPSAOTDir(t),
		AOTPrefix:      "ps-probe",
		CompileOnMiss:  true,
		Store:          o.store,
		StoreRoot:      o.root,
		FDBudget:       o.fdBudget,
		MaxMemoryPages: 4096,
		InitConfig:     pc.bytes(),
		HeartbeatStale: o.stale,
		StopDeadline:   o.stop,
		ControlBudget:  o.control,
		OnFailure:      o.onFail,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// threadStat reads one field of the probe's per-thread stats block.
func probeStats(t testing.TB, p *PSInstance, threads int) [][8]uint64 {
	t.Helper()
	n := 64 * 64
	out, err := p.Module().AllocateSize(uint32(n))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Module().Deallocate(out)
	if _, err := p.Control("flatsql_ps_stats", int32(out), int32(n)); err != nil {
		t.Fatal(err)
	}
	mem, _ := p.Module().SharedMemory()
	raw := mem.ReadBytes(out, n)
	st := make([][8]uint64, threads)
	for i := 0; i < threads; i++ {
		for f := 0; f < 8; f++ {
			st[i][f] = binary.LittleEndian.Uint64(raw[64*i+8*f:])
		}
	}
	return st
}

const (
	statWakeups    = iota
	statLostWakeup // a wait timed out although the notify for pending work had returned before its deadline
	statWork
	statIOOK
	statIOErr
	statBytesWritten
	statChecksum
	statLateRing // a wait timed out with a ring whose notify had not landed by the deadline
)

func TestPSInstanceDoorbellRoundTrip(t *testing.T) {
	requirePatched(t)
	p := openProbe(t, probeConfig{Mode: probeDoorbell, Threads: 2}, probeOpts{})
	defer p.Stop()
	if p.Started() != 2 {
		t.Fatalf("started %d service threads, want 2", p.Started())
	}
	for round := uint64(1); round <= 50; round++ {
		for i := 0; i < 2; i++ {
			if err := p.Ring(i); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := p.WaitAck(ctx, i, round)
			cancel()
			if err != nil {
				t.Fatalf("round %d writer %d: %v", round, i, err)
			}
		}
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if st := p.Stats(); st.State != "dead" || st.Threads.Live != 0 {
		t.Fatalf("after stop: %+v", st)
	}
}

// percentile returns the p-th percentile of durations.
func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(p * float64(len(s)-1))
	return s[idx]
}

var errUnused = errors.New("unused")

func init() { _ = errUnused; _ = fmt.Sprint }

// ---- T5 #3: host I/O through guest threads -------------------------------------------

type latencySummary struct {
	N              uint64
	P50, P99, P999 time.Duration
}

// histBucketLow decodes the probe's log-linear bucket to its lower bound.
func histBucketLow(b int) time.Duration {
	if b < 128 {
		return time.Duration(b * 8)
	}
	e := (b-128)/32 + 10
	sub := (b - 128) % 32
	return time.Duration((uint64(32+sub) << uint(e-5)))
}

func summarize(hist []uint64) latencySummary {
	var n uint64
	for _, c := range hist {
		n += c
	}
	s := latencySummary{N: n}
	at := func(q float64) time.Duration {
		want := uint64(q * float64(n))
		var acc uint64
		for b, c := range hist {
			acc += c
			if acc > want {
				return histBucketLow(b)
			}
		}
		return histBucketLow(len(hist) - 1)
	}
	s.P50, s.P99, s.P999 = at(0.50), at(0.99), at(0.999)
	return s
}

// merge sums per-thread histograms for one op (0 = read, 1 = write).
func mergeHist(raw []uint64, threads, op int) []uint64 {
	out := make([]uint64, NativeHistBuckets)
	for t := 0; t < threads; t++ {
		base := (t*2 + op) * NativeHistBuckets
		for b := 0; b < NativeHistBuckets; b++ {
			out[b] += raw[base+b]
		}
	}
	return out
}

// T5 #3: 8 threads x 1M pread/pwrite: 0 Go host-function dispatches for
// flatsql_io, and overhead p99 <= 5 us over the raw syscall (the same
// operations timed the same way from C threads on the same machine).
func TestPSHostIOThroughGuestThreads(t *testing.T) {
	requirePatched(t)
	threads := 8
	ops := envInt(t, "SDN_PS_IO_OPS", 1000000)
	const size, fileBytes = 4096, 16 << 20
	root := t.TempDir()
	p := openProbe(t, probeConfig{Mode: probeIO, Threads: uint32(threads), IOOps: uint32(ops), IOSize: size,
		IOFileBytes: fileBytes, Prefix: "io/"}, probeOpts{root: root, control: time.Minute})
	defer p.Stop()
	env := p.Module().HostModule(HostIOModule)
	if n, err := p.HostIO().InstalledIn(env); err != nil || n != 7 {
		t.Fatalf("flatsql_io imports served by this instance's C functions: %d (%v); want 7", n, err)
	}
	deadline := time.Now().Add(10 * time.Minute)
	for p.Module().ThreadStats().Live > 0 {
		if time.Now().After(deadline) {
			t.Fatal("I/O threads did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := probeStats(t, p, threads)
	var ok, bad uint64
	for i := 0; i < threads; i++ {
		ok += st[i][statIOOK]
		bad += st[i][statIOErr]
	}
	io := p.HostIO().Stats()
	if bad != 0 || ok != uint64(threads*ops) {
		t.Fatalf("guest I/O: %d ok, %d errors; want %d ok", ok, bad, threads*ops)
	}
	// Every guest call reached the C module: its counters hold them all.
	prefill := uint64(threads * fileBytes / size)
	if uint64(io.Reads) != uint64(threads*ops/2) || uint64(io.Writes) != uint64(threads*ops/2)+prefill {
		t.Fatalf("C module counted %d reads, %d writes", io.Reads, io.Writes)
	}
	mem, _ := p.Module().SharedMemory()
	v, err := p.Control("probe_hist_ptr")
	if err != nil {
		t.Fatal(err)
	}
	histBytes := mem.ReadBytes(uint32(wasmrt.ToInt32(v[0])), 64*2*NativeHistBuckets*8)
	guest := make([]uint64, threads*2*NativeHistBuckets)
	for i := range guest {
		guest[i] = binary.LittleEndian.Uint64(histBytes[8*i:])
	}
	raw, err := benchRawIO(t.TempDir(), threads, ops, size, fileBytes)
	if err != nil {
		t.Fatal(err)
	}
	for op, name := range []string{"pread", "pwrite"} {
		g, r := summarize(mergeHist(guest, threads, op)), summarize(mergeHist(raw, threads, op))
		over := g.P99 - r.P99
		t.Logf("%s %dx%d x %d B (%s/%s, %d CPUs): guest p50 %s p99 %s p99.9 %s | raw p50 %s p99 %s p99.9 %s | p99 overhead %s",
			name, threads, ops/2, size, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), g.P50, g.P99, g.P999, r.P50, r.P99, r.P999, over)
		// The acceptance machine is Linux. On darwin the raw baseline's own
		// pthreads write measurably faster than ANY other thread, the guest's
		// or plain Go syscall.Pwrite from locked goroutines alike (measured
		// 2026-09-27: 9.0 us p50 against 12.0-12.8 us for both), so a darwin
		// delta measures the scheduler, not this module.
		if over > 5*time.Microsecond && runtime.GOOS == "linux" && perfGate() {
			t.Errorf("%s p99 overhead %s over the raw syscall (> 5 us)", name, over)
		}
	}
}

// ---- T5 #4 / A23 through guest threads --------------------------------------------------

// Guest threads hammer writes; revoke returns; the instance's files never
// change again, including the write of a thread that was blocked inside a
// host call across the revoke. A replacement instance on the same store,
// started during the revoke, gets only its own bytes.
func TestPSRevokeFencesGuestWriters(t *testing.T) {
	requirePatched(t)
	st, root := newTestStore(t)
	old := openProbe(t, probeConfig{Mode: probeHammer, Threads: 4, Files: 4, Marker: 0xEE, Prefix: "old/"},
		probeOpts{store: st, control: 30 * time.Second})
	defer old.Stop()
	waitBytes := func(p *PSInstance, threads int) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			st := probeStats(t, p, threads)
			all := true
			for i := 0; i < threads; i++ {
				all = all && st[i][statBytesWritten] > 0
			}
			if all {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("hammer threads did not start writing")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitBytes(old, 4)
	// Every write from now on stalls 100 ms inside the host call first.
	old.HostIO().SetFault(0, 100*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	repl := make(chan *PSInstance, 1)
	go func() {
		repl <- openProbe(t, probeConfig{Mode: probeHammer, Threads: 4, Files: 4, Marker: 0x11, Prefix: "new/"},
			probeOpts{store: st, control: 30 * time.Second})
	}()
	drain := old.HostIO().Revoke()
	snapshot := func(dir string) map[string][]byte {
		out := map[string][]byte{}
		filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				b, _ := os.ReadFile(path)
				out[path] = b
			}
			return nil
		})
		return out
	}
	before := snapshot("old")
	time.Sleep(300 * time.Millisecond)
	after := snapshot("old")
	if len(before) != 16 {
		t.Fatalf("old instance has %d files, want 16", len(before))
	}
	for path, b := range before {
		if !bytesEqual(b, after[path]) {
			t.Fatalf("%s changed after revoke returned", path)
		}
	}
	s := old.HostIO().Stats()
	t.Logf("revoke drained in-flight writes in %s (a stalled write was in a host call); writes after revoke: %d; parked callers: %d",
		drain, s.WritesAfterRevoke, s.Parked)
	if s.WritesAfterRevoke != 0 || drain < 50*time.Millisecond || s.Parked == 0 {
		t.Fatalf("fencing: %+v drain %s", s, drain)
	}
	r := <-repl
	defer r.Stop()
	waitBytes(r, 4)
	time.Sleep(100 * time.Millisecond)
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	for path, b := range snapshot("new") {
		for i, c := range b {
			if c != 0x11 && c != 0 {
				t.Fatalf("%s byte %d is %#x: a foreign byte in the replacement's file", path, i, c)
			}
		}
	}
	if err := old.Stop(); err != nil {
		t.Fatal(err)
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

// ---- T5 #6: budgets and blast radius ---------------------------------------------------------

// A service thread runs far past the instance's per-call budget and is
// never budget-poisoned (acceptance: 1 h, SDN_PS_SERVICE_RUN=1h; default 5 s
// against a 200 ms control budget).
func TestPSServiceThreadsAreExemptFromCallBudgets(t *testing.T) {
	requirePatched(t)
	run := envDuration(t, "SDN_PS_SERVICE_RUN", 5*time.Second)
	p := openProbe(t, probeConfig{Mode: probeCPU, Threads: 2}, probeOpts{control: 200 * time.Millisecond, stale: 10 * time.Second})
	defer p.Stop()
	start := time.Now()
	last := uint32(0)
	for time.Since(start) < run {
		time.Sleep(run / 10)
		if _, err := p.Control("probe_mem_pages"); err != nil {
			t.Fatalf("control call after %s: %v", time.Since(start), err)
		}
		hb, ok := p.Load32(p.Layout().HeartbeatBase)
		if !ok || hb == last {
			t.Fatalf("service thread stopped beating after %s", time.Since(start))
		}
		last = hb
	}
	s := p.Stats()
	if s.Poisoned || p.Failure() != nil || s.Threads.Live != 2 {
		t.Fatalf("after %s of service work under a 200 ms call budget: %+v %v", run, s, p.Failure())
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	t.Logf("2 service threads ran %s under a 200 ms control budget: not poisoned, stopped cleanly", run)
}

// A trap in one instance cancels nothing outside it: the other instances
// keep every thread, their doorbells answer, and none is poisoned.
func TestPSTrapStaysInItsInstance(t *testing.T) {
	requirePatched(t)
	st, _ := newTestStore(t)
	failed := make(chan error, 1)
	victim := openProbe(t, probeConfig{Mode: probeDoorbell, Threads: 3}, probeOpts{store: st,
		onFail: func(_ *PSInstance, err error) { failed <- err }})
	defer victim.Stop()
	others := []*PSInstance{
		openProbe(t, probeConfig{Mode: probeDoorbell, Threads: 3}, probeOpts{store: st, role: PSRoleReader}),
		openProbe(t, probeConfig{Mode: probeDoorbell, Threads: 3}, probeOpts{store: st, role: PSRoleBulk}),
	}
	defer func() {
		for _, o := range others {
			o.Stop()
		}
	}()
	spawnedBefore := []int64{others[0].Stats().Threads.Spawned, others[1].Stats().Threads.Spawned}
	// Corrupt thread 1's canary: its next loop iteration traps (A30 canary).
	victim.Store32(victim.Layout().CanaryBase+4, 0xBAD)
	select {
	case err := <-failed:
		t.Logf("victim fenced: %v (revoke %s after detection)", err, victim.FenceLatency())
	case <-time.After(10 * time.Second):
		t.Fatal("the trap was not detected")
	}
	for i, o := range others {
		s := o.Stats()
		if s.Poisoned || o.Failure() != nil || s.Threads.Live != 3 || s.Threads.Spawned != spawnedBefore[i] {
			t.Fatalf("instance %d affected by another instance's trap: %+v", i, s)
		}
		for w := 0; w < 3; w++ {
			o.Ring(w)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := o.WaitAck(ctx, w, 1)
			cancel()
			if err != nil {
				t.Fatalf("instance %d writer %d after the trap: %v", i, w, err)
			}
		}
	}
	if !victim.Stats().Poisoned {
		t.Fatal("the trapped instance is not poisoned")
	}
}

// ---- T5 #7 and A24: idle cost and doorbell latency -------------------------------------------

func ackValue(p *PSInstance, i int) uint64 {
	mem, _ := p.Module().SharedMemory()
	return mem.Load64(p.Layout().AckBase + uint32(8*i))
}

// T5 #7: <= 200 guest wakeups/s per instance after 1 s idle; first-request
// doorbell latency p99 <= 1 ms. A24: over many idle-to-active transitions
// (acceptance 10^6: SDN_PS_TRANSITIONS=1000000; default 20,000) the p99
// holds and no wakeup is lost.
//
// A wait that times out and then finds work is not by itself a lost wakeup.
// A ring stores to seq and asks the doorbell thread for a notify, so a ring
// that lands within one notify latency of the writer's deadline, or just
// after it while the writer's sleeping flag is still set, meets a wait that
// has already timed out; the writer takes the work on its next pass. Counting
// those as failures made this test flake on loaded runners (1 in 20,000 on
// CI, ed8b028c0). The probe counts them as late rings, and counts a lost
// wakeup only when the notify for the pending work had returned before the
// wait's deadline (ps-probe.c doorbell_loop). SDN_PS_ACTIVE_WAIT_US shortens
// the writers' active wait, which makes late rings frequent.
func TestPSIdleCostAndDoorbellLatency(t *testing.T) {
	requirePatched(t)
	const writers = 4
	p := openProbe(t, probeConfig{Mode: probeDoorbell, Threads: writers,
		ActiveWaitUs: uint32(envInt(t, "SDN_PS_ACTIVE_WAIT_US", 5000))}, probeOpts{})
	defer p.Stop()
	time.Sleep(1100 * time.Millisecond)
	w0 := probeStats(t, p, writers)
	time.Sleep(2 * time.Second)
	w1 := probeStats(t, p, writers)
	var wakeups uint64
	for i := 0; i < writers; i++ {
		wakeups += w1[i][statWakeups] - w0[i][statWakeups]
	}
	idleRate := float64(wakeups) / 2
	t.Logf("idle: %.0f guest wakeups/s across %d writer threads (acceptance <= 200 per instance)", idleRate, writers)
	if idleRate > 200 && perfGate() {
		t.Fatalf("%.0f idle wakeups/s", idleRate)
	}

	transitions := envInt(t, "SDN_PS_TRANSITIONS", 20000)
	lat, viaPoller := ringTransitions(t, p, writers, transitions)
	lost, late := doorbellTimeouts(t, p, writers)
	db := p.Stats().Doorbell
	t.Logf("%d idle-to-active transitions (%s/%s): ring->ack p50 %s p99 %s p99.9 %s max %s; through the poller p99 %s; notifies %d of %d rings; lost wakeups %d; late rings %d",
		transitions, runtime.GOOS, runtime.GOARCH, percentile(lat, 0.5), percentile(lat, 0.99), percentile(lat, 0.999),
		percentile(lat, 1), percentile(viaPoller, 0.99), db.Notifies, db.Rings, lost, late)
	if p99 := percentile(lat, 0.99); p99 > time.Millisecond && perfGate() {
		t.Fatalf("doorbell p99 %s > 1 ms", p99)
	}
	if lost != 0 {
		t.Fatalf("%d lost wakeups: a wait timed out after the notify for its pending work had returned", lost)
	}
}

// A24 with the writers' active wait (200 us) close to the interval between
// rings, so rings keep meeting waits at their deadline: the race behind the
// ed8b028c0 flake, hundreds of times per run instead of once in a while.
// Late rings are expected; a lost wakeup is not.
func TestPSDoorbellRingsAtTheDeadlineAreNotLost(t *testing.T) {
	requirePatched(t)
	const writers = 4
	p := openProbe(t, probeConfig{Mode: probeDoorbell, Threads: writers, ActiveWaitUs: 200}, probeOpts{})
	defer p.Stop()
	transitions := envInt(t, "SDN_PS_TRANSITIONS", 20000)
	lat, _ := ringTransitions(t, p, writers, transitions)
	lost, late := doorbellTimeouts(t, p, writers)
	t.Logf("%d transitions against a 200 us active wait: ring->ack p99 %s max %s; lost wakeups %d; late rings %d",
		transitions, percentile(lat, 0.99), percentile(lat, 1), lost, late)
	if lost != 0 {
		t.Fatalf("%d lost wakeups: a wait timed out after the notify for its pending work had returned", lost)
	}
}

// The negative control: a wake export that drops every 10th doorbell notify
// (a runtime that loses wakeups) must show up as lost wakeups, or the count
// the two tests above assert on could never fail.
func TestPSDoorbellCountsALostWakeup(t *testing.T) {
	requirePatched(t)
	const writers = 2
	p := openProbe(t, probeConfig{Mode: probeDoorbell, Threads: writers, DropNotify: 10}, probeOpts{})
	defer p.Stop()
	lat, _ := ringTransitions(t, p, writers, 400)
	lost, late := doorbellTimeouts(t, p, writers)
	t.Logf("400 transitions, every 10th notify dropped: ring->ack max %s; lost wakeups %d; late rings %d",
		percentile(lat, 1), lost, late)
	if lost == 0 {
		t.Fatal("dropped notifies were not counted as lost wakeups")
	}
}

// ringTransitions rings writers round-robin, each once it has gone to sleep,
// and waits for its ack. It returns ring->ack latencies; every 100th
// transition also goes end to end through the completion poller.
func ringTransitions(t *testing.T, p *PSInstance, writers, transitions int) (lat, viaPoller []time.Duration) {
	t.Helper()
	lat = make([]time.Duration, 0, transitions)
	for n := 0; n < transitions; n++ {
		w := n % writers
		want := ackValue(p, w) + 1
		deadline := time.Now().Add(time.Second)
		for !p.doorbell.Sleeping(w) {
			if time.Now().After(deadline) {
				t.Fatalf("writer %d never went to sleep", w)
			}
			runtime.Gosched()
		}
		t0 := time.Now()
		p.Ring(w)
		if n%100 == 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := p.WaitAck(ctx, w, want); err != nil {
				t.Fatal(err)
			}
			cancel()
			viaPoller = append(viaPoller, time.Since(t0))
		} else {
			for ackValue(p, w) < want {
				if time.Since(t0) > 5*time.Second {
					t.Fatalf("transition %d: no ack", n)
				}
			}
		}
		lat = append(lat, time.Since(t0))
	}
	return lat, viaPoller
}

// doorbellTimeouts sums the probe's two kinds of timed-out wait that found
// work: lost wakeups and late rings (ps-probe.c doorbell_loop).
func doorbellTimeouts(t testing.TB, p *PSInstance, writers int) (lost, late uint64) {
	t.Helper()
	st := probeStats(t, p, writers)
	for i := 0; i < writers; i++ {
		lost += st[i][statLostWakeup]
		late += st[i][statLateRing]
	}
	return lost, late
}

// ---- A21: a hung service thread is fenced within 15 s --------------------------------------------

func TestPSHungServiceThreadIsFenced(t *testing.T) {
	requirePatched(t)
	failed := make(chan error, 1)
	root := t.TempDir()
	p := openProbe(t, probeConfig{Mode: probeCPU, Threads: 2}, probeOpts{root: root, stale: 10 * time.Second,
		onFail: func(_ *PSInstance, err error) { failed <- err }})
	defer p.Stop()
	time.Sleep(200 * time.Millisecond)
	hungAt := time.Now()
	if _, err := p.Control("probe_hang", int32(0)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failed:
		fenced := p.FencedAt().Sub(hungAt)
		t.Logf("hung thread fenced (host I/O revoked) %s after it stopped beating; stopped %s after: %v",
			fenced.Round(time.Millisecond), time.Since(hungAt).Round(time.Millisecond), err)
		if fenced > 15*time.Second {
			t.Fatalf("fenced after %s (> 15 s)", fenced)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the hung thread was never fenced")
	}
	// The failure path stops the instance; the Interruptible stop reaches
	// the spinning thread, so nothing is retained.
	if err := p.Stop(); err != nil {
		t.Fatalf("stop after fencing: %v", err)
	}
	if s := p.Stats(); s.Threads.Live != 0 || s.State != "dead" {
		t.Fatalf("after fencing: %+v", s)
	}
}

// ---- A30: fail closed ------------------------------------------------------------------------------

// An artifact whose AOT section is not used is refused, never interpreted.
func TestPSRefusesAnArtifactThatWouldRunInterpreted(t *testing.T) {
	requirePatched(t)
	dir := t.TempDir()
	wasm := psProbeWasm(t)
	// Plant the portable bytes where the compiled artifact belongs.
	if err := os.WriteFile(ThreadedAOTPath(dir, "ps-probe", wasm), wasm, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := OpenPSInstance(PSConfig{Role: PSRoleWriter, Wasm: wasm, AOTCacheDir: dir, AOTPrefix: "ps-probe",
		StoreRoot: t.TempDir(), MaxMemoryPages: 4096, InitConfig: probeConfig{Mode: probeDoorbell}.bytes()})
	if !errors.Is(err, ErrPSSubstrate) || err == nil {
		t.Fatalf("an interpreted artifact was accepted: %v", err)
	}
	t.Logf("refused: %v", err)
	// And a missing artifact is never compiled on the service path.
	_, err = OpenPSInstance(PSConfig{Role: PSRoleWriter, Wasm: wasm, AOTCacheDir: t.TempDir(), AOTPrefix: "ps-probe",
		StoreRoot: t.TempDir(), MaxMemoryPages: 4096, InitConfig: probeConfig{Mode: probeDoorbell}.bytes()})
	if !errors.Is(err, ErrPSSubstrate) {
		t.Fatalf("a missing artifact did not refuse: %v", err)
	}
}

// probeHammerSyncEvery: ps-probe.c's hammer_loop syncs after every 64th
// write, before it checks the stop word again.
const probeHammerSyncEvery = 64

// A21 retention: an instance whose thread is stuck in the kernel cannot be
// stopped; it is retained, and a second such instance demands a restart.
func TestPSRetentionCap(t *testing.T) {
	requirePatched(t)
	psRetained.Store(0)
	defer psRetained.Store(0)
	// stuck returns once the hammer thread is inside a stalled sync, or
	// committed to one, that began after the fault was armed. Syncs count on
	// return, so waiting for Syncs > 0 returned when the first stalled sync
	// ended, and Stop then met the thread in its next 64 writes, where it saw
	// the stop word and left cleanly.
	stuck := func() *PSInstance {
		p := openProbe(t, probeConfig{Mode: probeHammer, Threads: 1, Files: 1, Marker: 0x22, Prefix: "s/"},
			probeOpts{stop: 100 * time.Millisecond})
		hio := p.HostIO()
		hio.SetFault(2, 3*time.Second) // every sync from now on stalls 3 s in "the kernel"
		armed := hio.Stats().Writes
		deadline := time.Now().Add(10 * time.Second)
		for {
			// Stats reads writes before syncs. The write that ends a batch
			// of 64 completed after the fault was armed, and the batch's sync
			// has not returned: the thread is between that write and the end
			// of a sync that sees the fault, with no stop check in between.
			st := hio.Stats()
			if st.Errors != 0 {
				t.Fatalf("hammer I/O errors break the write/sync cadence: %+v", st)
			}
			if st.Writes > armed && st.Writes%probeHammerSyncEvery == 0 && st.Syncs < st.Writes/probeHammerSyncEvery {
				return p
			}
			if time.Now().After(deadline) {
				t.Fatalf("no sync issued after the fault was armed: %+v", st)
			}
			time.Sleep(time.Millisecond)
		}
	}
	first := stuck()
	if err := first.Stop(); !errors.Is(err, ErrPSRetained) {
		t.Fatalf("first stuck instance: %v", err)
	}
	second := stuck()
	if err := second.Stop(); !errors.Is(err, ErrPSRestartRequired) {
		t.Fatalf("second stuck instance: %v", err)
	}
	if PSRetainedInstances() != 2 {
		t.Fatalf("retained %d", PSRetainedInstances())
	}
	time.Sleep(4 * time.Second) // let the stuck syncs finish before the process moves on
}
