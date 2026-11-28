package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestDaemonFlowAOTPrewarmAndTimeoutE2E exercises the end-to-end daemon flow
// lifecycle across three invariants from stack#1230:
//  1. Daemon startup prewarmEngineAOTForDaemon compiles installed
//     flows.services[] artifacts into the active CPU-profile AOT cache on a
//     cold flow cache, skips uninstalled services cleanly, and is idempotent.
//  2. LoadFlowServices mounts the prewarmed flows with `aot true` (zero
//     "AOT artifact unavailable" fallback).
//  3. node.startFlowServices wires modules.scheduled_invoke_timeout into
//     FlowMountDeps.ExecutionTimeout so a flow without per-service
//     execution_timeout is bounded by the node timeout and releases its
//     exclusive_group lock for waiting flows.
func TestDaemonFlowAOTPrewarmAndTimeoutE2E(t *testing.T) {
	exe := publicationBinary(t)
	seedEngineAOTOnlyForTest(t, filepath.Join(os.Getenv("SDN_E2E_PUBLICATION_AOT_CACHE"), "flatsql-aot"))

	wasmPath := filepath.Join("..", "cron-robustness-e2e", "testdata", "retrieval-flow.wasm")
	flowJSONPath := filepath.Join("..", "cron-robustness-e2e", "testdata", "flow.json")
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read retrieval flow wasm: %v", err)
	}
	flowJSONBytes, err := os.ReadFile(flowJSONPath)
	if err != nil {
		t.Fatalf("read retrieval flow topology: %v", err)
	}

	const (
		slowFlowID    = "com.digitalarsenal.flows.e2e-slow-blocker"
		cpuLoopFlowID = "com.digitalarsenal.flows.e2e-cpu-loop"
		fastFlowID    = "com.digitalarsenal.flows.e2e-fast-waiter"
		missingFlowID = "com.digitalarsenal.flows.e2e-not-installed"
	)

	d := newPublicationDaemon(t, exe, "flow-aot-timeout", "", "", "keep-all")
	flowsDir := filepath.Join(d.root, "flows")
	store, err := flowrt.NewFlowStore(flowsDir)
	if err != nil {
		t.Fatalf("open test flow store: %v", err)
	}
	for _, id := range []string{slowFlowID, fastFlowID} {
		patched := bytes.Replace(flowJSONBytes,
			[]byte(`"programId": "com.digitalarsenal.flows.celestrak-gp-ingest"`),
			[]byte(fmt.Sprintf(`"programId": %q`, id)), 1)
		if err := store.Install(id, wasmBytes, patched, nil); err != nil {
			t.Fatalf("install %s: %v", id, err)
		}
	}
	cpuLoopFlowJSON := fmt.Sprintf(`{
  "programId": %q,
  "triggers": [
    {
      "triggerId": "timer-cpu-loop",
      "kind": "timer",
      "defaultIntervalMs": 3600000
    }
  ],
  "triggerBindings": [
    {
      "triggerId": "timer-cpu-loop",
      "targetPortId": "tick"
    }
  ]
}
`, cpuLoopFlowID)
	if err := store.Install(cpuLoopFlowID, buildCPULoopFlowWasm(cpuLoopFlowID), []byte(cpuLoopFlowJSON), nil); err != nil {
		t.Fatalf("install %s: %v", cpuLoopFlowID, err)
	}

	hash := modulert.ContentHashHex(modulert.StripPublicationTrailer(wasmBytes))
	policy, _ := json.Marshal(map[string]any{
		"version": 1,
		"approvals": []map[string]string{
			{"module_hash": hash, "capability": "http"},
			{"module_hash": hash, "capability": "storage_ingest"},
		},
	})
	policyPath := filepath.Join(d.root, "capability_policy.json")
	if err := os.WriteFile(policyPath, policy, 0o600); err != nil {
		t.Fatal(err)
	}
	d.env = append(d.env, "SDN_MODULE_CAPABILITY_POLICY_PATH="+policyPath)

	slowStarted := make(chan struct{}, 4)
	var fastCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fast" {
			fastCalls.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("x"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case slowStarted <- struct{}{}:
		default:
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_, _ = w.Write([]byte("x"))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	}))
	defer upstream.Close()

	cfg := readFileString(d.cfg) + fmt.Sprintf(`modules:
  scheduled_invoke_timeout: 1200ms
flows:
  enabled: true
  storage_path: %q
  first_fire_when_due: false
  services:
    - flow: %q
      exclusive_group: space-weather
      release_after_run: true
      config:
        celestrak_gp_url: %q
        celestrak_http_timeout_ms: 120000
    - flow: %q
      exclusive_group: space-weather
      release_after_run: true
    - flow: %q
      exclusive_group: space-weather
      execution_timeout: 15s
      release_after_run: true
      config:
        celestrak_gp_url: %q
        celestrak_http_timeout_ms: 15000
    - flow: %q
      exclusive_group: space-weather
`, flowsDir, slowFlowID, upstream.URL+"/slow", cpuLoopFlowID, fastFlowID, upstream.URL+"/fast", missingFlowID)
	if err := os.WriteFile(d.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	d.start(t)

	// Wait for StartConfiguredFlowServices to mount all installed services.
	publicationAwaitLogFrom(t, d, 0, fmt.Sprintf(`Flow service %q started and scheduled`, fastFlowID), 30*time.Second)

	logText := readFileString(d.logPath)
	for _, want := range []string{
		fmt.Sprintf("compiled flow %s AOT artifact for this host", slowFlowID),
		fmt.Sprintf("compiled flow %s AOT artifact for this host", cpuLoopFlowID),
		fmt.Sprintf("compiled flow %s AOT artifact for this host", fastFlowID),
		fmt.Sprintf(`Flow service %q loaded (1 timers, aot true, exclusive group "space-weather"`, slowFlowID),
		fmt.Sprintf(`Flow service %q loaded (1 timers, aot true, exclusive group "space-weather"`, cpuLoopFlowID),
		fmt.Sprintf(`Flow service %q loaded (1 timers, aot true, exclusive group "space-weather"`, fastFlowID),
		fmt.Sprintf(`Flow service %q not started: its bundle is not installed`, missingFlowID),
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("daemon log missing %q:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "AOT artifact unavailable") {
		t.Fatalf("daemon fell back to interpreter:\n%s", logText)
	}

	// Verify all three flow AOT artifacts exist on disk with the interruptible
	// cache-key discriminator (-intr.aot.wasm) and a repeat prewarm is an
	// idempotent no-op preserving mtime and size.
	aotCacheDir := filepath.Join(os.Getenv("SDN_E2E_PUBLICATION_AOT_CACHE"), "flatsql-aot")
	slowArtifact, err := flowrt.FlowAOTArtifactPath(slowFlowID, store, aotCacheDir)
	if err != nil {
		t.Fatalf("FlowAOTArtifactPath(%s): %v", slowFlowID, err)
	}
	cpuArtifact, err := flowrt.FlowAOTArtifactPath(cpuLoopFlowID, store, aotCacheDir)
	if err != nil {
		t.Fatalf("FlowAOTArtifactPath(%s): %v", cpuLoopFlowID, err)
	}
	fastArtifact, err := flowrt.FlowAOTArtifactPath(fastFlowID, store, aotCacheDir)
	if err != nil {
		t.Fatalf("FlowAOTArtifactPath(%s): %v", fastFlowID, err)
	}
	for _, artifact := range []string{slowArtifact, cpuArtifact, fastArtifact} {
		if !strings.HasSuffix(artifact, "-intr.aot.wasm") {
			t.Fatalf("flow AOT artifact %q missing interruptible -intr.aot.wasm suffix", artifact)
		}
	}
	slowStatBefore, err := os.Stat(slowArtifact)
	if err != nil || slowStatBefore.Size() == 0 {
		t.Fatalf("missing slow flow AOT artifact %s: %v", slowArtifact, err)
	}
	cpuStatBefore, err := os.Stat(cpuArtifact)
	if err != nil || cpuStatBefore.Size() == 0 {
		t.Fatalf("missing cpu-loop flow AOT artifact %s: %v", cpuArtifact, err)
	}
	fastStatBefore, err := os.Stat(fastArtifact)
	if err != nil || fastStatBefore.Size() == 0 {
		t.Fatalf("missing fast flow AOT artifact %s: %v", fastArtifact, err)
	}

	// Restarting the daemon must be an idempotent cache hit: no flow is
	// recompiled, artifact mtimes/sizes stay identical, and all still boot
	// with aot true.
	restartLogOffset := len(readFileString(d.logPath))
	d.stop(t)
	d.start(t)
	restartDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(restartDeadline) {
		if strings.Contains(readFileString(d.logPath)[restartLogOffset:], fmt.Sprintf(`Flow service %q started and scheduled`, fastFlowID)) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	restartLog := readFileString(d.logPath)[restartLogOffset:]
	for _, flowID := range []string{slowFlowID, cpuLoopFlowID, fastFlowID} {
		if strings.Contains(restartLog, fmt.Sprintf("compiled flow %s AOT artifact", flowID)) {
			t.Fatalf("daemon restart recompiled %s instead of hitting cache:\n%s", flowID, restartLog)
		}
		if !strings.Contains(restartLog, fmt.Sprintf(`Flow service %q loaded (1 timers, aot true, exclusive group "space-weather"`, flowID)) {
			t.Fatalf("daemon restart did not load %s with aot true:\n%s", flowID, restartLog)
		}
	}
	if strings.Contains(restartLog, "AOT artifact unavailable") {
		t.Fatalf("daemon restart fell back to interpreter:\n%s", restartLog)
	}
	slowStatAfter, err := os.Stat(slowArtifact)
	if err != nil || !slowStatBefore.ModTime().Equal(slowStatAfter.ModTime()) || slowStatBefore.Size() != slowStatAfter.Size() {
		t.Fatalf("daemon restart modified %s", slowArtifact)
	}
	cpuStatAfter, err := os.Stat(cpuArtifact)
	if err != nil || !cpuStatBefore.ModTime().Equal(cpuStatAfter.ModTime()) || cpuStatBefore.Size() != cpuStatAfter.Size() {
		t.Fatalf("daemon restart modified %s", cpuArtifact)
	}
	fastStatAfter, err := os.Stat(fastArtifact)
	if err != nil || !fastStatBefore.ModTime().Equal(fastStatAfter.ModTime()) || fastStatBefore.Size() != fastStatAfter.Size() {
		t.Fatalf("daemon restart modified %s", fastArtifact)
	}

	triggerMethod := func(flowID, methodID, body string) chan string {
		ch := make(chan string, 1)
		go func() {
			var reqBody io.Reader
			if body != "" {
				reqBody = strings.NewReader(body)
			}
			resp, reqErr := http.Post(fmt.Sprintf("%s/api/v1/modules/runtime/%s/schedules/%s/run", d.base, flowID, methodID), "application/json", reqBody)
			if reqErr != nil {
				ch <- reqErr.Error()
				return
			}
			respBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			ch <- fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
		}()
		return ch
	}

	// Case A: HTTP-hostcall slow flow (inherits modules.scheduled_invoke_timeout = 1200ms).
	slowStartedAt := time.Now()
	slowHTTP := triggerMethod(slowFlowID, "timer-gp", "")
	select {
	case <-slowStarted:
	case msg := <-slowHTTP:
		t.Fatalf("slow flow finished before hitting /slow: %s\n%s", msg, readFileString(d.logPath))
	case <-time.After(15 * time.Second):
		t.Fatalf("slow flow never reached /slow:\n%s", readFileString(d.logPath))
	}

	fastHTTP := triggerMethod(fastFlowID, "timer-gp", "")

	slowEnd := publicationAwaitLog(t, d, fmt.Sprintf(`Plugin %q manual run "timer-gp" end status=`, slowFlowID), 15*time.Second)
	if !strings.HasPrefix(slowEnd, "error ") || !strings.Contains(slowEnd, "deadline exceeded") {
		t.Fatalf("expected slow flow to fail with deadline exceeded, got: %s", slowEnd)
	}
	if elapsed := time.Since(slowStartedAt); elapsed >= 10*time.Second {
		t.Fatalf("slow flow took %s; ScheduledInvokeTimeout did not bound it", elapsed)
	}

	fastEnd := publicationAwaitLog(t, d, fmt.Sprintf(`Plugin %q manual run "timer-gp" end status=`, fastFlowID), 15*time.Second)
	if !strings.HasPrefix(fastEnd, "ok ") {
		t.Fatalf("expected waiting fast flow to succeed after slow timeout, got: %s", fastEnd)
	}
	if fastCalls.Load() < 1 {
		t.Fatalf("fast flow never hit /fast")
	}

	finalLog := readFileString(d.logPath)
	waitMarker := fmt.Sprintf(`Flow service %q: trigger "timer-gp" waits for another run of exclusive group "space-weather"`, fastFlowID)
	if !strings.Contains(finalLog, waitMarker) {
		t.Fatalf("expected fast flow to wait on exclusive_group %q:\n%s", "space-weather", finalLog)
	}

	select {
	case msg := <-slowHTTP:
		t.Logf("slow trigger response: %s", msg)
	case <-time.After(5 * time.Second):
		t.Fatal("slow trigger HTTP call did not return")
	}
	select {
	case msg := <-fastHTTP:
		t.Logf("fast trigger response: %s", msg)
	case <-time.After(5 * time.Second):
		t.Fatal("fast trigger HTTP call did not return")
	}

	// Case B: Pure-CPU infinite WASM loop compiled AOT (`aot true`).
	// ExecutionTimeout (1200ms) must genuinely interrupt the AOT compute loop via
	// AsyncCancel (NOT hit the 2000ms execThreadLost timeout path), release
	// exclusive_group "space-weather", let fastFlowID run to ok, and leave zero
	// leaked spinning threads on shutdown.
	cpuLogOffset := len(readFileString(d.logPath))
	fastCallsBeforeCPU := fastCalls.Load()
	cpuStartedAt := time.Now()
	cpuHTTP := triggerMethod(cpuLoopFlowID, "timer-cpu-loop", "")
	publicationAwaitLogFrom(t, d, cpuLogOffset, fmt.Sprintf(`Plugin %q manual run "timer-cpu-loop" start`, cpuLoopFlowID), 15*time.Second)
	time.Sleep(150 * time.Millisecond)

	fastHTTP2 := triggerMethod(fastFlowID, "timer-gp", `{"force":true}`)

	cpuEnd := publicationAwaitLogFrom(t, d, cpuLogOffset, fmt.Sprintf(`Plugin %q manual run "timer-cpu-loop" end status=`, cpuLoopFlowID), 15*time.Second)
	cpuElapsed := time.Since(cpuStartedAt)
	if !strings.HasPrefix(cpuEnd, "error ") || !strings.Contains(cpuEnd, "deadline exceeded") {
		t.Fatalf("expected pure-CPU AOT loop to fail with deadline exceeded, got: %s", cpuEnd)
	}
	if cpuElapsed >= 2500*time.Millisecond {
		t.Fatalf("pure-CPU AOT loop took %s (>= 2.5s); AsyncCancel hit execThreadLost leak path instead of interrupting AOT loop", cpuElapsed)
	}

	fastEnd2 := publicationAwaitLogFrom(t, d, cpuLogOffset, fmt.Sprintf(`Plugin %q manual run "timer-gp" end status=`, fastFlowID), 15*time.Second)
	if !strings.HasPrefix(fastEnd2, "ok ") {
		t.Fatalf("expected waiting fast flow to succeed after pure-CPU AOT timeout, got: %s", fastEnd2)
	}
	if fastCalls.Load() <= fastCallsBeforeCPU {
		t.Fatalf("fast flow never hit /fast after pure-CPU AOT timeout")
	}
	cpuSectionLog := readFileString(d.logPath)[cpuLogOffset:]
	if !strings.Contains(cpuSectionLog, waitMarker) {
		t.Fatalf("expected fast flow to wait on exclusive_group %q during pure-CPU AOT loop:\n%s", "space-weather", cpuSectionLog)
	}

	select {
	case msg := <-cpuHTTP:
		t.Logf("cpu-loop trigger response: %s (elapsed=%s)", msg, cpuElapsed)
	case <-time.After(5 * time.Second):
		t.Fatal("cpu-loop trigger HTTP call did not return")
	}
	select {
	case msg := <-fastHTTP2:
		t.Logf("fast2 trigger response: %s", msg)
	case <-time.After(5 * time.Second):
		t.Fatal("fast2 trigger HTTP call did not return")
	}

	// Verify clean daemon shutdown within the normal shutdown window and zero
	// leaked spinning threads / undrained background tasks.
	stopStartedAt := time.Now()
	d.stop(t)
	if stopElapsed := time.Since(stopStartedAt); stopElapsed >= 15*time.Second {
		t.Fatalf("daemon shutdown took %s (>= 15s); leaked background task/thread suspected", stopElapsed)
	}
	shutdownLog := readFileString(d.logPath)
	for _, forbidden := range []string{
		"ERROR leaking VM and one OS thread",
		"native execution did not stop after cancellation",
		"background tasks did not drain",
		"AOT artifact unavailable",
	} {
		if strings.Contains(shutdownLog, forbidden) {
			t.Fatalf("daemon log contains %q:\n%s", forbidden, shutdownLog)
		}
	}
}

func publicationAwaitLogFrom(t *testing.T, d *publicationDaemon, offset int, marker string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		text := readFileString(d.logPath)
		if offset < len(text) {
			slice := text[offset:]
			if idx := strings.Index(slice, marker); idx >= 0 {
				rest := slice[idx+len(marker):]
				if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
					return rest[:nl]
				}
				return rest
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no %q after offset %d in %s within %s\n%s", marker, offset, d.logPath, budget, readFileString(d.logPath))
	return ""
}

// buildCPULoopFlowWasm constructs a minimal valid flow WASM module with a $PLG
// manifest and flow runtime exports whose space_data_module_runtime_drain_linked
// enters a pure-CPU infinite loop `(loop (br 0))` when maxIterations != 0
// (returning 0 during the load-time probe drain_linked(0)).
func buildCPULoopFlowWasm(programID string) []byte {
	b := flatbuffers.NewBuilder(256)
	pidOff := b.CreateString(programID)
	nameOff := b.CreateString(programID)
	verOff := b.CreateString("1.0.0")
	plg.PLGStart(b)
	plg.PLGAddPLUGIN_ID(b, pidOff)
	plg.PLGAddNAME(b, nameOff)
	plg.PLGAddVERSION(b, verOff)
	root := plg.PLGEnd(b)
	plg.FinishPLGBuffer(b, root)
	manifest := b.FinishedBytes()

	appendULEB := func(dst []byte, v uint32) []byte {
		for {
			by := byte(v & 0x7f)
			v >>= 7
			if v != 0 {
				by |= 0x80
			}
			dst = append(dst, by)
			if v == 0 {
				return dst
			}
		}
	}
	appendSLEB := func(dst []byte, v int32) []byte {
		for {
			by := byte(v & 0x7f)
			v >>= 7
			done := (v == 0 && (by&0x40) == 0) || (v == -1 && (by&0x40) != 0)
			if !done {
				by |= 0x80
			}
			dst = append(dst, by)
			if done {
				return dst
			}
		}
	}
	sec := func(id byte, payload []byte) []byte {
		out := []byte{id}
		out = appendULEB(out, uint32(len(payload)))
		return append(out, payload...)
	}
	str := func(s string) []byte {
		out := appendULEB(nil, uint32(len(s)))
		return append(out, s...)
	}
	i32Const := func(v int32) []byte {
		out := []byte{0, 0x41}
		out = appendSLEB(out, v)
		return append(out, 0x0b)
	}

	// Types:
	// 0: () -> (i32)
	// 1: (i32) -> (i32)
	// 2: (i32, i32) -> (i32)
	types := []byte{
		3,
		0x60, 0, 1, 0x7f,
		0x60, 1, 0x7f, 1, 0x7f,
		0x60, 2, 0x7f, 0x7f, 1, 0x7f,
	}
	// Functions: 8 functions
	// 0: type 0 -> 0 (node/edge/dep count)
	// 1: type 0 -> 1 (trigger count)
	// 2: type 0 -> -1 (get_ready_node_index)
	// 3: type 0 -> len(manifest)
	// 4: type 0 -> 64 (manifest pointer)
	// 5: type 1 -> 2048 (malloc)
	// 6: type 2 -> 0 (enqueue_trigger_frame)
	// 7: type 1 -> if param0 == 0 return 0 else loop(br 0) (drain_linked)
	funcs := []byte{8, 0, 0, 0, 0, 0, 1, 2, 1}
	mem := []byte{1, 0x00, 1}

	var exports []byte
	exports = append(exports, 11)
	addExp := func(name string, kind, idx byte) {
		exports = append(exports, str(name)...)
		exports = append(exports, kind, idx)
	}
	addExp("memory", 0x02, 0)
	addExp("plugin_get_manifest_flatbuffer_size", 0x00, 3)
	addExp("plugin_get_manifest_flatbuffer", 0x00, 4)
	addExp("malloc", 0x00, 5)
	addExp("space_data_module_runtime_get_node_descriptor_count", 0x00, 0)
	addExp("space_data_module_runtime_get_edge_descriptor_count", 0x00, 0)
	addExp("space_data_module_runtime_get_trigger_descriptor_count", 0x00, 1)
	addExp("space_data_module_runtime_get_dependency_descriptor_count", 0x00, 0)
	addExp("space_data_module_runtime_enqueue_trigger_frame", 0x00, 6)
	addExp("space_data_module_runtime_drain_linked", 0x00, 7)
	addExp("space_data_module_runtime_get_ready_node_index", 0x00, 2)

	bodyDrain := []byte{
		0,          // local decls
		0x20, 0x00, // local.get 0
		0x45,       // i32.eqz
		0x04, 0x7f, // if (result i32)
		0x41, 0x00, // i32.const 0
		0x05,                   // else
		0x03, 0x40, 0x0c, 0x00, // loop (br 0)
		0x0b,       // end loop
		0x41, 0x00, // i32.const 0
		0x0b, // end if
		0x0b, // end func
	}
	bodies := [][]byte{
		i32Const(0),
		i32Const(1),
		i32Const(-1),
		i32Const(int32(len(manifest))),
		i32Const(64),
		i32Const(2048),
		i32Const(0),
		bodyDrain,
	}
	var code []byte
	code = append(code, byte(len(bodies)))
	for _, body := range bodies {
		code = appendULEB(code, uint32(len(body)))
		code = append(code, body...)
	}

	var data []byte
	data = append(data, 1, 0x00, 0x41)
	data = appendSLEB(data, 64)
	data = append(data, 0x0b)
	data = appendULEB(data, uint32(len(manifest)))
	data = append(data, manifest...)

	var wasm []byte
	wasm = append(wasm, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	wasm = append(wasm, sec(1, types)...)
	wasm = append(wasm, sec(3, funcs)...)
	wasm = append(wasm, sec(5, mem)...)
	wasm = append(wasm, sec(7, exports)...)
	wasm = append(wasm, sec(10, code)...)
	wasm = append(wasm, sec(11, data)...)
	return wasm
}

// seedEngineAOTOnlyForTest copies prewarmed FlatSQL engine AOT artifacts (never
// flowmount-* artifacts) from the host's cache dir into dstDir when present, so
// the isolated daemon test avoids recompiling the multi-MB SQL engines while
// still starting with a completely cold flow AOT cache.
func seedEngineAOTOnlyForTest(t *testing.T, dstDir string) {
	t.Helper()
	srcDir := storage.EngineAOTCacheDir()
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".aot.wasm") {
			continue
		}
		if !strings.HasPrefix(name, "flatsql-") && !strings.HasPrefix(name, "fsqlp4-") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(srcDir, name))
		if readErr != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(dstDir, name), data, 0o600)
	}
}
