// Command manual-run-backoff-e2e drives operator manual runs and scheduled
// ticks through the real plugin manager, flow runtime and persistent retrieval
// ledger, against a loopback publisher:
//
//   - a manual run skips a lane's failure backoff but never the provider's
//     minimum interval: inside it the run is deferred, with the time it becomes
//     allowed; outside it the run goes at once and, on success, clears the
//     backoff. A scheduled tick inside the backoff still skips.
//   - a clean scheduled run of a flow that stores nothing (record-retention)
//     is a success, not "landed no batch"; a refused one is still a failure.
//
// Guests are published flows, not Go reimplementations: the CelesTrak GP flow
// beside the cron-robustness E2E and the record-retention flow in testdata
// (provenance in each testdata/README.md). Run from sdn-server:
// go -C sdn-server run ./cmd/manual-run-backoff-e2e
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/plugins"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	gpTrigger        = "timer-gp"
	retentionTrigger = "timer-retention"
	// The lanes' own minimum intervals (flows.services[].retrieval_interval),
	// seconds instead of CelesTrak's 3 h so the E2E can step past them.
	gpWindow        = 3 * time.Second
	retentionWindow = time.Second
)

func require(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}

type logCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// info reports whether msg was logged at Info.
func (l *logCapture) info(msg string) bool {
	quoted, _ := json.Marshal(msg)
	msg = string(quoted[1 : len(quoted)-1])
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(l.b.String(), "\n") {
		if strings.Contains(line, msg) && strings.Contains(line, `"level":"info"`) {
			return true
		}
	}
	return false
}

// load stages a published guest and loads it as a flow service with the given
// capabilities granted.
func load(root, name, wasmPath, flowPath string, config map[string]interface{}, grant func(*modulert.CapabilityRegistry), capabilities ...string) *flowrt.ServiceFlow {
	artifact := filepath.Join(root, name)
	require(os.Mkdir(artifact, 0700) == nil, "create %s", artifact)
	guest, err := os.ReadFile(wasmPath)
	require(err == nil, "fixture %s (run from sdn-server): %v", wasmPath, err)
	topology, err := os.ReadFile(flowPath)
	require(err == nil, "fixture %s (run from sdn-server): %v", flowPath, err)
	require(os.WriteFile(filepath.Join(artifact, "runtime.wasm"), guest, 0600) == nil, "write guest")
	require(os.WriteFile(filepath.Join(artifact, "flow.json"), topology, 0600) == nil, "write topology")
	policy, err := modulert.NewCapabilityPolicyStore("")
	require(err == nil, "policy: %v", err)
	reg := modulert.NewCapabilityRegistry()
	grant(reg)
	for _, capability := range capabilities {
		_, err = policy.Approve(modulert.CapabilityApproval{
			ModuleHash: modulert.ContentHashHex(modulert.StripPublicationTrailer(guest)), Capability: capability,
		})
		require(err == nil, "approve %s: %v", capability, err)
	}
	sf, err := flowrt.LoadFlowService(artifact, nil, flowrt.FlowMountDeps{
		CapRegistry: reg, MaxMemoryPages: 4096,
		NodeCtx: &modulert.NodeContext{CapabilityPolicy: policy, Config: config},
	})
	require(err == nil, "load %s: %v", name, err)
	sf.SetReleaseAfterRun(true)
	return sf
}

// bind wires a flow to the ledger exactly as internal/node does for a flow
// service with a configured retrieval_interval of window.
func bind(sf *flowrt.ServiceFlow, ledger *sourcemetrics.Store, window time.Duration) {
	id, baseHours := sf.ID(), window.Hours()
	sf.SetRetrievalGate(func() (bool, string) {
		due, reason, _ := ledger.RetrievalDue(id, baseHours, true)
		if due {
			ledger.RecordAttempt(id)
		}
		return due, reason
	})
	sf.SetRetrievalOutcome(func(err error) {
		switch {
		case errors.Is(err, flowrt.ErrRetrievalUnchanged):
			ledger.RecordAttemptUnchanged(id)
		case errors.Is(err, flowrt.ErrNothingToStore):
			ledger.RecordAttemptSucceeded(id)
		default:
			ledger.RecordAttemptOutcome(id, err)
		}
	})
	sf.SetManualRunGate(func() (func(error), time.Time) { return ledger.AdmitManualRun(id, baseHours) })
}

// scheduledTick fires a trigger as the cron ticker does. The deadline bounds
// the ticker's wait-out-the-window grace, which a refused tick spends first.
func scheduledTick(sf *flowrt.ServiceFlow, trigger string) (skipped bool, reason string) {
	ctx, cancel := context.WithTimeout(plugins.WithScheduledRun(context.Background()), 2*time.Second)
	defer cancel()
	out, err := sf.InvokeCron(ctx, trigger, nil)
	require(err == nil, "scheduled tick: %v", err)
	var summary struct {
		Skipped bool   `json:"skipped"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(out, &summary)
	return summary.Skipped, summary.Reason
}

func main() {
	t0 := time.Now()
	logs := &logCapture{}
	logging.SetPrimaryCore(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(logs), zapcore.InfoLevel))
	logging.SetAllLoggers(logging.LevelInfo)

	root, err := os.MkdirTemp("", "manual-run-backoff-e2e-")
	require(err == nil, "temp: %v", err)
	defer os.RemoveAll(root)

	var failing atomic.Bool
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer upstream.Close()

	ledger, err := sourcemetrics.Open(root)
	require(err == nil, "ledger: %v", err)
	defer ledger.Close()
	manager := plugins.New()

	gp := load(root, "gp", "cmd/cron-robustness-e2e/testdata/retrieval-flow.wasm", "cmd/cron-robustness-e2e/testdata/flow.json",
		map[string]interface{}{"celestrak_gp_url": upstream.URL, "celestrak_http_timeout_ms": 120000},
		func(reg *modulert.CapabilityRegistry) {
			reg.RegisterBridgeAware("http", caps.NewHTTPBridgeCapFactory())
			reg.Register("storage_ingest", func(*modulert.Module) modulert.CapHandler {
				return func(string, []byte) ([]byte, error) {
					panic("fixture unexpectedly tried to ingest: only 304 and failures are served")
				}
			})
		}, "http", "storage_ingest")
	defer gp.Close()
	bind(gp, ledger, gpWindow)
	require(manager.Register(gp) == nil, "register gp flow")
	gpID := gp.ID()

	// --- 1. Manual runs: the failure backoff yields, the minimum interval holds.
	for range 4 {
		ledger.RecordAttempt(gpID)
		ledger.RecordAttemptOutcome(gpID, errors.New("prior failure"))
	}
	time.Sleep(gpWindow + time.Second) // past the 3 s minimum, inside the 48 s backoff

	skipped, reason := scheduledTick(gp, gpTrigger)
	require(skipped && strings.Contains(reason, "backoff window (4 consecutive failures; last: prior failure)") && requests.Load() == 0,
		"scheduled tick inside the backoff ran: skipped=%v reason=%q requests=%d", skipped, reason, requests.Load())
	fmt.Printf("PASS scheduled tick inside the failure backoff skips: %s\n", reason)

	started := time.Now()
	run, err := manager.RunRuntimeModuleScheduleNow(context.Background(), gpID, gpTrigger)
	require(err == nil && run.Status == "ok" && run.Trigger == "manual", "manual run: %+v %v", run, err)
	require(requests.Load() == 1, "manual run did not reach the publisher: %d requests", requests.Load())
	last, failures := ledger.AttemptState(gpID)
	require(failures == 0 && ledger.AttemptFailureReason(gpID) == "" && last != nil && !last.Before(started.Truncate(time.Second)),
		"manual success left the backoff: failures=%d reason=%q last=%v", failures, ledger.AttemptFailureReason(gpID), last)
	for _, msg := range []string{
		fmt.Sprintf("Plugin %q manual run %q start", gpID, gpTrigger),
		fmt.Sprintf("Plugin %q manual run %q end status=ok", gpID, gpTrigger),
		"manual run: failure backoff bypassed (operator intent)",
		fmt.Sprintf("Retrieval attempt for %s: manual run succeeded; failure streak and backoff cleared", gpID),
	} {
		require(logs.info(msg), "missing Info log: %s", msg)
	}
	fmt.Printf("PASS manual run outside the minimum interval runs through the backoff (%s), logs start and end at Info, and clears it (failures 4 -> 0)\n",
		time.Since(started).Round(time.Millisecond))

	run, err = manager.RunRuntimeModuleScheduleNow(context.Background(), gpID, gpTrigger)
	allowed := last.Add(gpWindow).UTC().Format(time.RFC3339)
	want := "deferred: provider minimum interval; allowed at " + allowed
	require(err == nil && run.Status == "deferred" && run.Message == want && requests.Load() == 1,
		"manual run inside the minimum interval: %+v %v requests=%d", run, err, requests.Load())
	require(logs.info(want), "missing Info log: %s", want)
	fmt.Printf("PASS manual run inside the minimum interval is deferred, publisher not asked: %s\n", run.Message)

	skipped, reason = scheduledTick(gp, gpTrigger)
	require(skipped && strings.Contains(reason, "debounce window") && requests.Load() == 1,
		"scheduled tick after the manual run: skipped=%v reason=%q requests=%d", skipped, reason, requests.Load())
	fmt.Printf("PASS scheduled tick after it is held to the minimum interval only: %s\n", reason)

	time.Sleep(gpWindow + time.Second)
	failing.Store(true)
	run, err = manager.RunRuntimeModuleScheduleNow(context.Background(), gpID, gpTrigger)
	require(err == nil && run.Status == "ok", "failed manual run: %+v %v", run, err)
	_, failures = ledger.AttemptState(gpID)
	cause := ledger.AttemptFailureReason(gpID)
	require(requests.Load() == 2 && failures == 1 && cause != "",
		"failed manual run not booked: requests=%d failures=%d reason=%q", requests.Load(), failures, cause)
	fmt.Printf("PASS failed manual run books its own outcome: 1 consecutive failure (%s)\n", cause)

	// --- 2. A flow that stores nothing is judged by its own result.
	var expires atomic.Int32
	var expireFails atomic.Bool
	retention := load(root, "retention", "cmd/manual-run-backoff-e2e/testdata/retention-flow.wasm", "cmd/manual-run-backoff-e2e/testdata/retention-flow.json",
		map[string]interface{}{},
		func(reg *modulert.CapabilityRegistry) {
			reg.Register("storage_write", func(*modulert.Module) modulert.CapHandler {
				return func(op string, _ []byte) ([]byte, error) {
					require(op == "storage.expire", "unexpected storage op %s", op)
					expires.Add(1)
					if expireFails.Load() {
						return []byte(`{"ok":false,"error":{"message":"record index unavailable"}}`), nil
					}
					return []byte(`{"ok":true,"result":{"deleted":3,"missing":0,"capped":false}}`), nil
				}
			})
		}, "storage_write")
	defer retention.Close()
	bind(retention, ledger, retentionWindow)
	retentionID := retention.ID()

	skipped, reason = scheduledTick(retention, retentionTrigger)
	_, failures = ledger.AttemptState(retentionID)
	stats := retention.RuntimeDescriptor().Stats
	require(!skipped && expires.Load() == 1 && stats.LastTimerStatus == "ok" && failures == 0 && ledger.AttemptFailureReason(retentionID) == "",
		"clean retention run: skipped=%v (%s) expires=%d status=%s failures=%d reason=%q",
		skipped, reason, expires.Load(), stats.LastTimerStatus, failures, ledger.AttemptFailureReason(retentionID))
	require(logs.info(fmt.Sprintf("Retrieval attempt for %s: run succeeded; the flow stores nothing, so no batch was owed", retentionID)),
		"missing Info log for the retention success")
	fmt.Println("PASS clean scheduled retention run (stores nothing) leaves failures at 0")

	time.Sleep(retentionWindow + 2*time.Second)
	expireFails.Store(true)
	skipped, reason = scheduledTick(retention, retentionTrigger)
	_, failures = ledger.AttemptState(retentionID)
	cause = ledger.AttemptFailureReason(retentionID)
	require(!skipped && expires.Load() == 2 && failures == 1 && strings.Contains(cause, "refused"),
		"refused retention run: skipped=%v (%s) expires=%d failures=%d reason=%q", skipped, reason, expires.Load(), failures, cause)
	fmt.Printf("PASS refused retention run still counts: 1 consecutive failure (%s)\n", cause)

	fmt.Printf("MANUAL-RUN-BACKOFF-E2E PASS: 7 scenarios, %d upstream requests, %d retention passes, %s\n",
		requests.Load(), expires.Load(), time.Since(t0).Round(time.Millisecond))
}
