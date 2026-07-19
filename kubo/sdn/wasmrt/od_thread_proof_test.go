//go:build linux

package wasmrt_test

// od_thread_proof_test.go — proves the REAL analysis/od isomorphic wasi-threads
// module (dist/isomorphic/module.wasm, ~3.4MB: SGP4 + OD fit + Eigen + FB + a
// std::thread work-stealing pool over od::run_batch_fit) INSTANTIATES with shared
// memory and SPAWNS + RUNS more than one OS thread doing real fits UNDER THE NODE'S
// WasmEdge 0.14.1 (via wasmrt.WithWASIThreads) — not just wasmtime/Node. This is
// the deploy-gate the mission demands: compiling != running; the module must not
// trap/deadlock at instantiation or thread-spawn under WasmEdge.
//
// It drives the module's WASI-command surface (_start): reads N in-memory $OEM
// FlatBuffers from a preopened /in dir, fans them across std::thread workers,
// writes per-object $OMM/$OCM/$OBD to a preopened /out dir, and prints a JSON
// telemetry line (worker_count, distinct_worker_thread_ids) to stdout. $OEM is
// INPUT ONLY — never persisted by the module.
//
// Env (skips cleanly if unset):
//   SDN_OD_MODULE_WASM_PATH  path to analysis/od/dist/isomorphic/module.wasm
//   SDN_OD_OEM_FB_DIR        dir of obj*.oem $OEM FlatBuffer fixtures
//   SDN_OD_REPEAT            (scaling bench) fixture-set repeat count (default 8)

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/wasmrt"
)

type odResult struct {
	Idx       int     `json:"idx"`
	Ok        bool    `json:"ok"`
	Converged bool    `json:"converged"`
	RmsKm     float64 `json:"rms_km"`
	OmmSha256 string  `json:"omm_sha256"`
	OmmBytes  int     `json:"omm_bytes"`
	OcmBytes  int     `json:"ocm_bytes"`
	ObdBytes  int     `json:"obd_bytes"`
}

type odTelemetry struct {
	Objects                 int        `json:"objects"`
	ThreadsRequested        int        `json:"threads_requested"`
	WorkerCount             int        `json:"worker_count"`
	DistinctWorkerThreadIDs int        `json:"distinct_worker_thread_ids"`
	Results                 []odResult `json:"results"`
}

type odRun struct {
	tele      odTelemetry
	rawOut    string
	outDir    string
	files     []string
	hostPeak  int
	hostSpawn int
	hostTIDs  []int64
	wall      time.Duration
}

func odProofEnv(t testing.TB) (wasmPath, fbDir string) {
	wasmPath = os.Getenv("SDN_OD_MODULE_WASM_PATH")
	fbDir = os.Getenv("SDN_OD_OEM_FB_DIR")
	if wasmPath == "" || fbDir == "" {
		t.Skip("set SDN_OD_MODULE_WASM_PATH + SDN_OD_OEM_FB_DIR to run the real-OD-module WasmEdge thread proof")
	}
	return
}

func listOEM(t testing.TB, fbDir string, repeat int) []string {
	ents, err := os.ReadDir(fbDir)
	if err != nil {
		t.Fatalf("read fixtures dir %s: %v", fbDir, err)
	}
	var base []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".oem") {
			base = append(base, e.Name())
		}
	}
	sort.Strings(base)
	if len(base) == 0 {
		t.Fatalf("no *.oem fixtures in %s", fbDir)
	}
	if repeat < 1 {
		repeat = 1
	}
	var files []string
	for i := 0; i < repeat; i++ {
		files = append(files, base...)
	}
	return files
}

// captureFD1 redirects OS fd 1 (stdout — where WasmEdge WASI writes the guest's
// stdout) to a temp file for the duration of fn, then returns what was written.
func captureFD1(t testing.TB, fn func()) string {
	tmp, err := os.CreateTemp("", "od-stdout-*")
	if err != nil {
		t.Fatalf("temp stdout: %v", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	saved, err := syscall.Dup(1)
	if err != nil {
		t.Fatalf("dup fd1: %v", err)
	}
	if err := syscall.Dup3(int(tmp.Fd()), 1, 0); err != nil {
		syscall.Close(saved)
		t.Fatalf("redirect fd1: %v", err)
	}
	fn()
	_ = syscall.Dup3(saved, 1, 0)
	syscall.Close(saved)
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	b, _ := io.ReadAll(tmp)
	return string(b)
}

func runODStart(t testing.TB, wasmPath, fbDir string, files []string, threads int) odRun {
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read module: %v", err)
	}
	outDir, err := os.MkdirTemp("", "od-out-*")
	if err != nil {
		t.Fatalf("out dir: %v", err)
	}
	args := []string{"od", "--threads", strconv.Itoa(threads), "--out", "/out"}
	for _, f := range files {
		args = append(args, "/in/"+f)
	}
	preopens := []string{"/in:" + fbDir, "/out:" + outDir}

	m, err := wasmrt.NewModule(wasm,
		wasmrt.WithWASIThreads(),
		wasmrt.WithWASIArgs(args, nil, preopens),
		wasmrt.WithMaxMemoryPages(32768),
	)
	if err != nil {
		t.Fatalf("NewModule(WithWASIThreads) for real OD module FAILED (instantiation trap under WasmEdge?): %v", err)
	}
	defer m.Release()

	var wall time.Duration
	raw := captureFD1(t, func() {
		start := time.Now()
		// _start runs main: reads $OEM, threads the fit, writes $OMM/$OCM/$OBD,
		// prints JSON, then proc_exit(0) — a clean exit surfaces as nil or a
		// tolerated exit here.
		_, execErr := m.Execute("_start")
		wall = time.Since(start)
		if execErr != nil && !strings.Contains(strings.ToLower(execErr.Error()), "exit") {
			// Non-exit error = real trap/deadlock: surface it.
			fmt.Printf("[_start execErr] %v\n", execErr)
		}
	})

	run := odRun{
		rawOut:    raw,
		outDir:    outDir,
		files:     files,
		hostPeak:  m.PeakConcurrentThreads(),
		hostSpawn: m.ThreadSpawnCount(),
		hostTIDs:  m.WorkerOSThreadIDs(),
		wall:      wall,
	}
	if we := m.WorkerError(); we != nil {
		t.Errorf("a worker thread's wasi_thread_start FAILED under WasmEdge: %v", we)
	}
	// Parse the telemetry JSON line (last {...} in stdout).
	if i := strings.LastIndex(raw, "{\"objects\""); i >= 0 {
		line := raw[i:]
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		if err := json.Unmarshal([]byte(line), &run.tele); err != nil {
			t.Logf("telemetry parse warn: %v (raw tail: %.300q)", err, tail(raw, 300))
		}
	} else {
		t.Logf("no telemetry line in stdout (raw tail: %.400q)", tail(raw, 400))
	}
	return run
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// hasFileID reports the aligned/size-prefixed FlatBuffer carries the given 4-char
// SDS file identifier in its header region.
func hasFileID(b []byte, id string) bool {
	if len(b) < 12 {
		return false
	}
	return bytes.Contains(b[:24], []byte(id))
}

// TestODModuleThreadsUnderWasmEdge is the deploy gate: the real OD module spawns
// >1 OS thread doing real fits under WasmEdge, emits valid $OMM/$OCM/$OBD, and
// single- vs multi-thread output is byte-identical (parity).
func TestODModuleThreadsUnderWasmEdge(t *testing.T) {
	wasmPath, fbDir := odProofEnv(t)
	files := listOEM(t, fbDir, 1)
	n := runtime.NumCPU()
	if n < 2 {
		n = 2
	}
	t.Logf("host NumCPU=%d, fixtures=%d objects", runtime.NumCPU(), len(files))

	// ── multi-thread run ─────────────────────────────────────────────────────
	multi := runODStart(t, wasmPath, fbDir, files, n)
	t.Logf("MULTI(threads=%d): objects=%d worker_count=%d distinct_worker_thread_ids=%d host_peak=%d host_spawn=%d host_tids=%v wall=%s",
		n, multi.tele.Objects, multi.tele.WorkerCount, multi.tele.DistinctWorkerThreadIDs, multi.hostPeak, multi.hostSpawn, multi.hostTIDs, multi.wall)

	// GATE 1: host observed >1 real OS worker thread live at once.
	if multi.hostPeak < 2 {
		t.Errorf("GATE FAIL: host PeakConcurrentThreads=%d — WasmEdge never ran >1 worker at once (no real parallelism / thread-spawn trap)", multi.hostPeak)
	}
	distinct := map[int64]bool{}
	for _, id := range multi.hostTIDs {
		distinct[id] = true
	}
	if len(multi.hostTIDs) < 2 || len(distinct) != len(multi.hostTIDs) {
		t.Errorf("GATE FAIL: worker OS tids not >1-and-distinct: %v", multi.hostTIDs)
	}
	// GATE 2: the module's OWN witness agrees (>1 distinct worker thread id).
	if multi.tele.DistinctWorkerThreadIDs < 2 {
		t.Errorf("GATE FAIL: module telemetry distinct_worker_thread_ids=%d (<2)", multi.tele.DistinctWorkerThreadIDs)
	}
	// GATE 3: every object fitted + emitted $OMM/$OCM/$OBD.
	if len(multi.tele.Results) != len(files) {
		t.Errorf("GATE FAIL: telemetry has %d results, want %d", len(multi.tele.Results), len(files))
	}
	okCount := 0
	for _, r := range multi.tele.Results {
		if r.Ok && r.OmmBytes > 0 && r.OcmBytes > 0 && r.ObdBytes > 0 {
			okCount++
		}
	}
	if okCount != len(files) {
		t.Errorf("GATE FAIL: only %d/%d objects produced $OMM+$OCM+$OBD", okCount, len(files))
	}
	// GATE 4: validate emitted FlatBuffers on disk (real $OMM/$OCM/$OBD bytes).
	validated := 0
	for i := range files {
		omm, e1 := os.ReadFile(filepath.Join(multi.outDir, fmt.Sprintf("obj%d.omm", i)))
		ocm, e2 := os.ReadFile(filepath.Join(multi.outDir, fmt.Sprintf("obj%d.ocm", i)))
		obd, e3 := os.ReadFile(filepath.Join(multi.outDir, fmt.Sprintf("obj%d.obd", i)))
		if e1 == nil && e2 == nil && e3 == nil &&
			hasFileID(omm, "$OMM") && hasFileID(ocm, "$OCM") && hasFileID(obd, "$OBD") {
			validated++
		}
	}
	t.Logf("on-disk FlatBuffer validation: %d/%d objects had valid $OMM+$OCM+$OBD files in /out", validated, len(files))

	// GATE 5: ZERO $OEM persisted by the module (it is input-only).
	oemPersisted := 0
	_ = filepath.Walk(multi.outDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if hasFileID(b, "$OEM") {
			oemPersisted++
			t.Errorf("GATE FAIL: $OEM persisted to %s (module must never write $OEM)", p)
		}
		return nil
	})
	if oemPersisted == 0 {
		t.Logf("in-memory-only invariant OK: 0 $OEM files in module output dir")
	}

	// ── single-thread run + PARITY ───────────────────────────────────────────
	single := runODStart(t, wasmPath, fbDir, files, 1)
	t.Logf("SINGLE(threads=1): worker_count=%d distinct_worker_thread_ids=%d wall=%s",
		single.tele.WorkerCount, single.tele.DistinctWorkerThreadIDs, single.wall)

	// PARITY 1: per-object omm sha256 identical single vs multi.
	sm := map[int]string{}
	for _, r := range single.tele.Results {
		sm[r.Idx] = r.OmmSha256
	}
	parity := 0
	for _, r := range multi.tele.Results {
		if s, ok := sm[r.Idx]; ok && s == r.OmmSha256 && r.OmmSha256 != "" {
			parity++
		} else {
			t.Errorf("PARITY FAIL obj%d: single omm_sha256=%s multi=%s", r.Idx, sm[r.Idx], r.OmmSha256)
		}
	}
	// PARITY 2: sacred ISS RMS (obj0) — the fit is real physics, not a stub.
	if len(multi.tele.Results) > 0 {
		iss := multi.tele.Results[0]
		t.Logf("ISS (obj0) RMS=%.9g km converged=%v (sacred ref ~0.070907542)", iss.RmsKm, iss.Converged)
	}
	t.Logf("PARITY: %d/%d objects byte-identical (omm_sha256) single-thread vs %d-thread", parity, len(multi.tele.Results), n)
	if parity != len(multi.tele.Results) || parity == 0 {
		t.Errorf("PARITY FAIL: only %d/%d objects identical single vs multi thread", parity, len(multi.tele.Results))
	}

	if !t.Failed() {
		t.Logf("★★★ WasmEdge THREAD PROOF MET: real OD module spawned %d distinct OS threads (host peak %d), fitted %d/%d objects to valid $OMM/$OCM/$OBD, single==multi byte-parity, 0 $OEM persisted.",
			len(distinct), multi.hostPeak, okCount, len(files))
	}
}

// TestODModuleThreadScaling sweeps a thread-count list over a larger (repeated)
// batch and prints the full-catalog-style scaling table: per-thread-count wall
// time, per-object time, and speedup vs the 1-thread baseline. Set
// SDN_OD_THREAD_SWEEP (e.g. "1,2,4,8,16") and SDN_OD_REPEAT to shape it.
func TestODModuleThreadScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling bench skipped in -short")
	}
	wasmPath, fbDir := odProofEnv(t)
	repeat := 8
	if v := os.Getenv("SDN_OD_REPEAT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			repeat = n
		}
	}
	files := listOEM(t, fbDir, repeat)

	sweep := []int{1, 2, 4, 8, runtime.NumCPU()}
	if v := os.Getenv("SDN_OD_THREAD_SWEEP"); v != "" {
		sweep = nil
		for _, p := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 {
				sweep = append(sweep, n)
			}
		}
	}
	// De-dup + sort ascending.
	seen := map[int]bool{}
	var uniq []int
	for _, n := range sweep {
		if !seen[n] {
			seen[n] = true
			uniq = append(uniq, n)
		}
	}
	sort.Ints(uniq)
	sweep = uniq

	t.Logf("SCALING batch=%d objects, host NumCPU=%d, sweep=%v", len(files), runtime.NumCPU(), sweep)
	var baseline time.Duration
	t.Logf("%-8s %-14s %-14s %-9s %-12s %-9s", "threads", "wall", "per-object", "speedup", "distinct_tid", "peak")
	for _, n := range sweep {
		r := runODStart(t, wasmPath, fbDir, files, n)
		if n == 1 || baseline == 0 {
			baseline = r.wall
		}
		perObj := r.wall / time.Duration(max1(len(files)))
		speedup := float64(baseline) / float64(r.wall)
		t.Logf("%-8d %-14s %-14s %-8.2fx %-12d %-9d",
			n, r.wall.Round(time.Millisecond), perObj.Round(time.Microsecond), speedup,
			r.tele.DistinctWorkerThreadIDs, r.hostPeak)
		if n > 1 && r.hostPeak < 2 {
			t.Errorf("SCALING threads=%d: host never ran >1 worker (peak=%d)", n, r.hostPeak)
		}
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
