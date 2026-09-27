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
}

func (c probeConfig) bytes() []byte {
	b := make([]byte, 64+len(c.Prefix))
	for i, v := range []uint32{c.Mode, c.Threads, c.ActiveWaitUs, c.IdleWaitUs, c.IOOps, c.IOSize, c.IOFileBytes,
		c.StackBytes, c.Marker, c.Files, uint32(len(c.Prefix))} {
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
	statWakeups = iota
	statTimeoutPending
	statWork
	statIOOK
	statIOErr
	statBytesWritten
	statChecksum
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
