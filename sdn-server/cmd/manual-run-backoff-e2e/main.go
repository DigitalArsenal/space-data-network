// Command manual-run-backoff-e2e drives an operator's manual run through the
// real plugin manager, flow runtime and persistent retrieval ledger, against a
// loopback publisher: a scheduled tick inside a failure backoff skips, while a
// manual run of the same flow runs at once, logs its start and end at Info,
// and on success clears the backoff.
//
// The guest is the published CelesTrak GP flow committed beside the
// cron-robustness E2E (provenance in its testdata/README.md). Run from
// sdn-server: go -C sdn-server run ./cmd/manual-run-backoff-e2e
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
	fixture = "cmd/cron-robustness-e2e/testdata"
	trigger = "timer-gp"
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

// load wires the flow the way the node does (internal/node: gate, outcome,
// manual-run ledger); the gate is the node's attempt-window check.
func load(artifact, url string, ledger *sourcemetrics.Store) *flowrt.ServiceFlow {
	guest, err := os.ReadFile(filepath.Join(artifact, "runtime.wasm"))
	require(err == nil, "guest: %v", err)
	policy, err := modulert.NewCapabilityPolicyStore("")
	require(err == nil, "policy: %v", err)
	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("http", caps.NewHTTPBridgeCapFactory())
	reg.Register("storage_ingest", func(*modulert.Module) modulert.CapHandler {
		return func(string, []byte) ([]byte, error) {
			panic("fixture unexpectedly tried to ingest: only 304 and failures are served")
		}
	})
	for _, name := range []string{"http", "storage_ingest"} {
		_, err = policy.Approve(modulert.CapabilityApproval{
			ModuleHash: modulert.ContentHashHex(modulert.StripPublicationTrailer(guest)), Capability: name,
		})
		require(err == nil, "approve: %v", err)
	}
	sf, err := flowrt.LoadFlowService(artifact, nil, flowrt.FlowMountDeps{
		CapRegistry: reg, MaxMemoryPages: 4096,
		NodeCtx: &modulert.NodeContext{CapabilityPolicy: policy, Config: map[string]interface{}{
			"celestrak_gp_url": url, "celestrak_http_timeout_ms": 120000,
		}},
	})
	require(err == nil, "load flow: %v", err)
	sf.SetReleaseAfterRun(true)
	id := sf.ID()
	sf.SetRetrievalGate(func() (bool, string) {
		if last, failures := ledger.AttemptState(id); last != nil {
			window := time.Duration(sourcemetrics.EffectiveDebounceHours(failures) * float64(time.Hour))
			if age := time.Since(*last); age < window {
				return false, fmt.Sprintf("last attempt %s ago is inside the %s window (%d consecutive failures)",
					age.Round(time.Second), window, failures)
			}
		}
		ledger.RecordAttempt(id)
		return true, ""
	})
	sf.SetRetrievalOutcome(func(err error) {
		if errors.Is(err, flowrt.ErrRetrievalUnchanged) {
			ledger.RecordAttemptUnchanged(id)
			return
		}
		ledger.RecordAttemptOutcome(id, err)
	})
	sf.SetManualRunLedger(func() func(error) { return ledger.BeginManualRun(id) })
	return sf
}

// scheduledTick fires the trigger as the cron ticker does. The deadline bounds
// the ticker's wait-out-the-window grace, which a refused tick spends first.
func scheduledTick(sf *flowrt.ServiceFlow) (skipped bool, reason string) {
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
	artifact := filepath.Join(root, "guest")
	require(os.Mkdir(artifact, 0700) == nil, "create fixture directory")
	for _, name := range []string{"retrieval-flow.wasm", "flow.json"} {
		raw, err := os.ReadFile(filepath.Join(fixture, name))
		require(err == nil, "fixture %s (run from sdn-server): %v", name, err)
		if name == "retrieval-flow.wasm" {
			name = "runtime.wasm"
		}
		require(os.WriteFile(filepath.Join(artifact, name), raw, 0600) == nil, "write fixture %s", name)
	}

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
	sf := load(artifact, upstream.URL, ledger)
	defer sf.Close()
	id := sf.ID()
	manager := plugins.New()
	require(manager.Register(sf) == nil, "register flow")

	// A lane in its widest backoff: four failed attempts, the last just now.
	for range 4 {
		ledger.RecordAttempt(id)
		ledger.RecordAttemptOutcome(id, errors.New("prior failure"))
	}
	_, failures := ledger.AttemptState(id)
	require(failures == 4, "seeded failures: %d", failures)

	skipped, reason := scheduledTick(sf)
	require(skipped && strings.Contains(reason, "24h0m0s window (4 consecutive failures)") && requests.Load() == 0,
		"scheduled tick inside the backoff ran: skipped=%v reason=%q requests=%d", skipped, reason, requests.Load())
	fmt.Printf("PASS scheduled tick inside the backoff skips: %s\n", reason)

	started := time.Now()
	run, err := manager.RunRuntimeModuleScheduleNow(context.Background(), id, trigger)
	require(err == nil && run.Status == "ok" && run.Trigger == "manual", "manual run: %+v %v", run, err)
	require(requests.Load() == 1, "manual run did not reach the publisher: %d requests", requests.Load())
	last, failures := ledger.AttemptState(id)
	require(failures == 0 && ledger.AttemptFailureReason(id) == "" && last != nil && !last.Before(started.Truncate(time.Second)),
		"manual success left the backoff: failures=%d reason=%q last=%v", failures, ledger.AttemptFailureReason(id), last)
	for _, msg := range []string{
		fmt.Sprintf("Plugin %q manual run %q start", id, trigger),
		fmt.Sprintf("Plugin %q manual run %q end status=ok", id, trigger),
		"retrieval gate bypassed (operator intent)",
		fmt.Sprintf("Manual run of %s succeeded: failure streak and backoff cleared", id),
	} {
		require(logs.info(msg), "missing Info log: %s", msg)
	}
	fmt.Printf("PASS manual run inside the backoff runs at once (%s), logs start and end at Info, and clears the backoff (failures 4 -> 0)\n",
		time.Since(started).Round(time.Millisecond))

	skipped, reason = scheduledTick(sf)
	require(skipped && strings.Contains(reason, "3h0m0s window (0 consecutive failures)") && requests.Load() == 1,
		"scheduled tick after the manual run: skipped=%v reason=%q requests=%d", skipped, reason, requests.Load())
	fmt.Printf("PASS scheduled tick after it is held to the base window only: %s\n", reason)

	failing.Store(true)
	run, err = manager.RunRuntimeModuleScheduleNow(context.Background(), id, trigger)
	require(err == nil, "failed manual run: %+v %v", run, err)
	_, failures = ledger.AttemptState(id)
	cause := ledger.AttemptFailureReason(id)
	require(requests.Load() == 2 && failures == 1 && cause != "",
		"failed manual run not booked: requests=%d failures=%d reason=%q", requests.Load(), failures, cause)
	fmt.Printf("PASS failed manual run books its own outcome: 1 consecutive failure (%s)\n", cause)

	fmt.Printf("MANUAL-RUN-BACKOFF-E2E PASS: 4 scenarios, %d upstream requests, %s\n",
		requests.Load(), time.Since(t0).Round(time.Millisecond))
}
