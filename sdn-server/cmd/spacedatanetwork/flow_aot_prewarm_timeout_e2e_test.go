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
      execution_timeout: 15s
      release_after_run: true
      config:
        celestrak_gp_url: %q
        celestrak_http_timeout_ms: 15000
    - flow: %q
      exclusive_group: space-weather
`, flowsDir, slowFlowID, upstream.URL+"/slow", fastFlowID, upstream.URL+"/fast", missingFlowID)
	if err := os.WriteFile(d.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	d.start(t)

	// Wait for StartConfiguredFlowServices to mount all installed services.
	publicationAwaitLog(t, d, fmt.Sprintf(`Flow service %q started and scheduled`, fastFlowID), 30*time.Second)

	logText := readFileString(d.logPath)
	for _, want := range []string{
		fmt.Sprintf("compiled flow %s AOT artifact for this host", slowFlowID),
		fmt.Sprintf("compiled flow %s AOT artifact for this host", fastFlowID),
		fmt.Sprintf(`Flow service %q loaded (1 timers, aot true, exclusive group "space-weather"`, slowFlowID),
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

	// Verify both flow AOT artifacts exist on disk and a repeat prewarm is an
	// idempotent no-op preserving mtime and size.
	aotCacheDir := filepath.Join(os.Getenv("SDN_E2E_PUBLICATION_AOT_CACHE"), "flatsql-aot")
	slowArtifact, err := flowrt.FlowAOTArtifactPath(slowFlowID, store, aotCacheDir)
	if err != nil {
		t.Fatalf("FlowAOTArtifactPath(%s): %v", slowFlowID, err)
	}
	fastArtifact, err := flowrt.FlowAOTArtifactPath(fastFlowID, store, aotCacheDir)
	if err != nil {
		t.Fatalf("FlowAOTArtifactPath(%s): %v", fastFlowID, err)
	}
	slowStatBefore, err := os.Stat(slowArtifact)
	if err != nil || slowStatBefore.Size() == 0 {
		t.Fatalf("missing slow flow AOT artifact %s: %v", slowArtifact, err)
	}
	fastStatBefore, err := os.Stat(fastArtifact)
	if err != nil || fastStatBefore.Size() == 0 {
		t.Fatalf("missing fast flow AOT artifact %s: %v", fastArtifact, err)
	}

	// Restarting the daemon must be an idempotent cache hit: neither flow is
	// recompiled, artifact mtimes/sizes stay identical, and both still boot
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
	for _, flowID := range []string{slowFlowID, fastFlowID} {
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
	fastStatAfter, err := os.Stat(fastArtifact)
	if err != nil || !fastStatBefore.ModTime().Equal(fastStatAfter.ModTime()) || fastStatBefore.Size() != fastStatAfter.Size() {
		t.Fatalf("daemon restart modified %s", fastArtifact)
	}

	// Trigger the slow flow (which has no per-service execution_timeout and must
	// inherit modules.scheduled_invoke_timeout = 1200ms). Once it is actively
	// holding exclusive_group "space-weather", trigger the fast flow in the
	// same group and verify it waits, then runs to ok after the slow flow times out.
	trigger := func(flowID string) chan string {
		ch := make(chan string, 1)
		go func() {
			resp, reqErr := http.Post(fmt.Sprintf("%s/api/v1/modules/runtime/%s/schedules/timer-gp/run", d.base, flowID), "application/json", nil)
			if reqErr != nil {
				ch <- reqErr.Error()
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			ch <- fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}()
		return ch
	}

	slowStartedAt := time.Now()
	slowHTTP := trigger(slowFlowID)
	select {
	case <-slowStarted:
	case msg := <-slowHTTP:
		t.Fatalf("slow flow finished before hitting /slow: %s\n%s", msg, readFileString(d.logPath))
	case <-time.After(15 * time.Second):
		t.Fatalf("slow flow never reached /slow:\n%s", readFileString(d.logPath))
	}

	fastHTTP := trigger(fastFlowID)

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
