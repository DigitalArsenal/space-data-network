// Command local-flow-backoff-e2e drives scheduled ticks of the published
// catalog-builder flow through the real flow runtime, a real FlatSQL store
// with the production storage capability handlers, and the persistent
// retrieval ledger, wired to the ledger the way internal/node wires a flow
// service.
//
// The catalog builder fetches from no publisher: its manifest declares only
// clock, storage_query and storage_ingest. It ticks every 10 minutes by design,
// and a tick with nothing new to write is normal. The E2E proves:
//
//   - a tick that completes without an error is a success, whatever it wrote:
//     over an empty store the flow lands no batch and its chain never runs,
//     and the failure count stays 0;
//   - both of its timers, firing in the same slot as the cron manager fires
//     them, run, and run again at the next slot (no minimum interval and no
//     backoff hold them, where the retrieval rule backed the builder off);
//   - a tick in which a node refuses (storage answering an error) is still a
//     failure: the next tick inside the backoff window is skipped, the first
//     tick after the window runs, and a clean run clears the streak.
//
// The guest is the published flow, not a Go reimplementation (provenance in
// testdata/README.md). Run from sdn-server:
// go -C sdn-server run ./cmd/local-flow-backoff-e2e
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/plugins"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	guestPath    = "cmd/local-flow-backoff-e2e/testdata/catalog-builder-flow.wasm"
	topologyPath = "cmd/local-flow-backoff-e2e/testdata/catalog-builder-flow.json"
	// The builder's own retrieval_interval on this Mac's builder node is 1m;
	// seconds here so the backoff after a refusal (6 s, then 12 s) can be
	// stepped past.
	window = 3 * time.Second
	// A run is given this long; a tick expected to be skipped gets the short
	// deadline, which ends its wait-out-the-window grace early.
	runDeadline  = 5 * time.Minute
	skipDeadline = 2 * time.Second
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

// count reports how many Info lines contain msg.
func (l *logCapture) count(msg string) int {
	quoted, _ := json.Marshal(msg)
	msg = string(quoted[1 : len(quoted)-1])
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range strings.Split(l.b.String(), "\n") {
		if strings.Contains(line, msg) && strings.Contains(line, `"level":"info"`) {
			n++
		}
	}
	return n
}

// The builder's partition-0 configuration on this Mac's builder node.
func builderConfig() map[string]interface{} {
	return map[string]interface{}{
		"catalog.provider":                      "catalog-producer",
		"catalog.identity_provider":             "catalog-inputs",
		"catalog.identity_source":               "satcat",
		"catalog.weather_provider":              "catalog-inputs",
		"catalog.weather_source":                "weather",
		"catalog.seed_providers":                []interface{}{"catalog-inputs", "catalog-inputs", "catalog-inputs", "catalog-inputs"},
		"catalog.seed_sources":                  []interface{}{"planet-operator", "eumetsat-operator", "keeptrack", "space-track-public-gp"},
		"catalog.ephemeris_providers":           []interface{}{"catalog-inputs", "catalog-inputs", "catalog-inputs"},
		"catalog.ephemeris_sources":             []interface{}{"spacex-starlink", "iss", "css-tiangong"},
		"catalog.ephemeris_max_age_hours":       24,
		"catalog.batch_size":                    256,
		"catalog.partition_count":               8,
		"catalog.partition_index":               0,
		"catalog.page_size":                     512,
		"catalog.max_records":                   400000,
		"catalog.max_bytes":                     234881024,
		"catalog.invocation_budget_ms":          540000,
		"catalog.object_allowance_ms":           2000,
		"catalog.eccentric_object_allowance_ms": 240000,
		"catalog.max_removal_fraction":          0.25,
		"catalog.recipe_revision":               "W/1",
		"catalog.validity_margin_seconds":       0,
		"catalog.max_fit_rms_km":                2,
		"catalog.max_fit_error_km":              10,
	}
}

// bind wires the flow to the ledger exactly as internal/node does for a flow
// service with a configured retrieval_interval of window
// (flowServiceRetrievalDue and the outcome switch in node.go).
func bind(sf *flowrt.ServiceFlow, ledger *sourcemetrics.Store, diagnoses *[]error, mu *sync.Mutex) {
	id, baseHours := sf.ID(), window.Hours()
	sf.SetRetrievalGate(func() (bool, string, flowrt.RetrievalOutcome) {
		var due bool
		var reason string
		if !sf.Retrieves() {
			due, reason, _ = ledger.LocalRunDue(id, baseHours)
		} else {
			due, reason, _ = ledger.RetrievalDue(id, baseHours, true)
		}
		if !due {
			return false, reason, nil
		}
		stamp := ledger.RecordAttempt(id)
		return true, "", func(err error) {
			mu.Lock()
			*diagnoses = append(*diagnoses, err)
			mu.Unlock()
			switch {
			case errors.Is(err, flowrt.ErrRetrievalUnchanged):
				ledger.RecordAttemptUnchangedAt(id, stamp)
			case errors.Is(err, flowrt.ErrNothingToStore):
				ledger.RecordAttemptSucceededAt(id, stamp)
			case errors.Is(err, flowrt.ErrLocalRunSucceeded):
				ledger.RecordAttemptLocalSucceededAt(id, stamp)
			default:
				ledger.RecordAttemptOutcome(id, err)
			}
		}
	})
	sf.SetManualRunGate(func() (func(error), time.Time) { return ledger.AdmitManualRun(id, baseHours) })
}

type tick struct {
	trigger string
	skipped bool
	reason  string
	took    time.Duration
}

// slot fires the given timers at the same instant, as the cron manager fires
// a flow's timers that share an interval, and waits for all of them. The
// deadline bounds the ticker's wait-out-the-window grace (a twentieth of the
// 10-minute interval), which a refused tick spends first.
func slot(sf *flowrt.ServiceFlow, deadline time.Duration, triggers ...string) []tick {
	out := make([]tick, len(triggers))
	var wg sync.WaitGroup
	for i, trigger := range triggers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(plugins.WithScheduledRun(context.Background()), deadline)
			defer cancel()
			started := time.Now()
			summary, err := sf.InvokeCron(ctx, trigger, nil)
			require(err == nil, "scheduled tick %s: %v", trigger, err)
			var s struct {
				Skipped bool   `json:"skipped"`
				Reason  string `json:"reason"`
			}
			_ = json.Unmarshal(summary, &s)
			out[i] = tick{trigger: trigger, skipped: s.Skipped, reason: s.Reason, took: time.Since(started)}
		}()
	}
	wg.Wait()
	return out
}

func main() {
	t0 := time.Now()
	logs := &logCapture{}
	logging.SetPrimaryCore(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(logs), zapcore.InfoLevel))
	logging.SetAllLoggers(logging.LevelInfo)

	root, err := os.MkdirTemp("", "local-flow-backoff-e2e-")
	require(err == nil, "temp: %v", err)
	defer os.RemoveAll(root)

	validator, err := sds.NewValidator(nil)
	require(err == nil, "validator: %v", err)
	store, err := storage.NewFlatSQLStore(filepath.Join(root, "store"), validator)
	require(err == nil, "store: %v", err)
	defer store.Close()
	ledger, err := sourcemetrics.Open(filepath.Join(root, "ledger"))
	require(err == nil, "ledger: %v", err)
	defer ledger.Close()

	// The production storage handlers, with one switch: while refusing is
	// set, every storage query answers an error, as a store that cannot serve
	// its index does. The guest's query node then refuses the run.
	var refusing atomic.Bool
	var queries atomic.Int32
	real := caps.NewStorageCapFactoryWithOptions(store, caps.StorageCapOptions{RawRoot: filepath.Join(root, "raw"), MinFreeDiskBytes: 1})
	storageCap := func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		handler := real(mod, bridge)
		return func(op string, payload []byte) ([]byte, error) {
			if strings.HasPrefix(op, "storage.") && op != "storage.ingest_with_source" {
				queries.Add(1)
				if refusing.Load() {
					return []byte(`{"ok":false,"error":{"message":"record index unavailable"}}`), nil
				}
			}
			return handler(op, payload)
		}
	}

	guest, err := os.ReadFile(guestPath)
	require(err == nil, "fixture %s (run from sdn-server): %v", guestPath, err)
	topology, err := os.ReadFile(topologyPath)
	require(err == nil, "fixture %s (run from sdn-server): %v", topologyPath, err)
	artifact := filepath.Join(root, "catalog-builder")
	require(os.Mkdir(artifact, 0700) == nil, "create %s", artifact)
	require(os.WriteFile(filepath.Join(artifact, "runtime.wasm"), guest, 0600) == nil, "write guest")
	require(os.WriteFile(filepath.Join(artifact, "flow.json"), topology, 0600) == nil, "write topology")

	policy, err := modulert.NewCapabilityPolicyStore("")
	require(err == nil, "policy: %v", err)
	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_query", storageCap)
	reg.RegisterBridgeAware("storage_ingest", storageCap)
	for _, capability := range []string{"clock", "storage_query", "storage_ingest"} {
		_, err = policy.Approve(modulert.CapabilityApproval{
			ModuleHash: modulert.ContentHashHex(modulert.StripPublicationTrailer(guest)), Capability: capability,
		})
		require(err == nil, "approve %s: %v", capability, err)
	}
	loadStarted := time.Now()
	sf, err := flowrt.LoadFlowService(artifact, nil, flowrt.FlowMountDeps{
		CapRegistry: reg, MaxMemoryPages: 4096,
		NodeCtx: &modulert.NodeContext{CapabilityPolicy: policy, Config: builderConfig()},
	})
	require(err == nil, "load catalog builder: %v", err)
	defer sf.Close()
	sf.SetReleaseAfterRun(true)
	var diagMu sync.Mutex
	var diagnoses []error
	bind(sf, ledger, &diagnoses, &diagMu)
	id := sf.ID()
	fmt.Printf("loaded %s (%d bytes, %s)\n", id, len(guest), time.Since(loadStarted).Round(time.Millisecond))

	require(!sf.Retrieves(), "premise: the catalog builder declares only local capabilities, got a retrieval flow")
	triggers := sf.Triggers()
	require(len(triggers) == 2, "premise: the builder has two timers, got %d", len(triggers))
	names := []string{triggers[0].TriggerID, triggers[1].TriggerID}
	for _, trigger := range names {
		require(sf.ScheduledInterval(trigger) == 10*time.Minute, "premise: %s ticks every 10 minutes, got %s", trigger, sf.ScheduledInterval(trigger))
	}
	fmt.Printf("PASS premise: %s fetches from no publisher (clock, storage_query, storage_ingest); timers %v every 10m\n", id, names)

	cleanLog := fmt.Sprintf("Retrieval attempt for %s: run succeeded; the flow fetches from no publisher, so nothing new to store is not a failure", id)

	landed := func() (batches int, records int64) {
		progress, err := store.SourceBatchProgress()
		require(err == nil, "batch progress: %v", err)
		for _, p := range progress {
			records += p.Count
		}
		return len(progress), records
	}

	// --- 1. Clean ticks over an empty store: nothing new to write, never a failure.
	for n := 1; n <= 3; n++ {
		before := len(diagnoses)
		batchesBefore, recordsBefore := landed()
		results := slot(sf, runDeadline, names...)
		for _, r := range results {
			require(!r.skipped, "slot %d: timer %s was skipped (%s)", n, r.trigger, r.reason)
		}
		last, failures := ledger.AttemptState(id)
		require(last != nil && failures == 0 && ledger.AttemptFailureReason(id) == "",
			"slot %d: clean ticks left failures=%d reason=%q", n, failures, ledger.AttemptFailureReason(id))
		if n == 1 {
			// The retrieval rule, asked about the same ledger, holds the
			// second timer of a slot to the minimum interval a publisher is
			// owed. The builder has no publisher; its gate lets it run.
			due, reason, _ := ledger.RetrievalDue(id, window.Hours(), true)
			require(!due && strings.Contains(reason, "debounce window"), "premise: retrieval gate due=%v (%s)", due, reason)
			localDue, localReason, _ := ledger.LocalRunDue(id, window.Hours())
			require(localDue, "local gate refused after a clean slot: %s", localReason)
			fmt.Printf("PASS the retrieval rule would hold the flow (%s); the local gate does not (%s)\n", reason, localReason)
		}
		diagMu.Lock()
		got := diagnoses[before:]
		diagMu.Unlock()
		require(len(got) == 2, "slot %d: %d outcomes, want 2", n, len(got))
		for _, d := range got {
			require(errors.Is(d, flowrt.ErrLocalRunSucceeded), "slot %d: outcome %v, want ErrLocalRunSucceeded", n, d)
		}
		batchesAfter, recordsAfter := landed()
		fmt.Printf("PASS slot %d: both timers ran (%s %s, %s %s), %d new batch(es) / %d new record(s), failures 0\n",
			n, results[0].trigger, results[0].took.Round(time.Millisecond), results[1].trigger, results[1].took.Round(time.Millisecond),
			batchesAfter-batchesBefore, recordsAfter-recordsBefore)
	}
	require(logs.count(cleanLog) == 6, "want 6 Info lines %q, got %d", cleanLog, logs.count(cleanLog))
	require(queries.Load() > 0, "the guest never queried its store")
	fmt.Printf("PASS 3 slots, 6 ticks, 0 failures, %d storage queries, each closed at Info: %q\n", queries.Load(), cleanLog)

	// --- 2. A node that refuses is still a failure, and it backs off.
	refusing.Store(true)
	results := slot(sf, runDeadline, names[0])
	require(!results[0].skipped, "refusing tick skipped: %s", results[0].reason)
	_, failures := ledger.AttemptState(id)
	cause := ledger.AttemptFailureReason(id)
	require(failures == 1 && strings.Contains(cause, "refused"),
		"refused tick not booked: failures=%d reason=%q", failures, cause)
	fmt.Printf("PASS refused tick is a failure: 1 consecutive (%s)\n", cause)

	results = slot(sf, skipDeadline, names[1])
	require(results[0].skipped && strings.Contains(results[0].reason, "inside the 6s backoff window (1 consecutive failures"),
		"tick inside the backoff ran: skipped=%v reason=%q", results[0].skipped, results[0].reason)
	fmt.Printf("PASS next tick inside the backoff is skipped: %s\n", results[0].reason)

	last, _ := ledger.AttemptState(id)
	time.Sleep(time.Until(last.Add(2*window)) + time.Second)
	results = slot(sf, runDeadline, names[0])
	require(!results[0].skipped, "tick after the backoff skipped: %s", results[0].reason)
	_, failures = ledger.AttemptState(id)
	require(failures == 2 && strings.Contains(ledger.AttemptFailureReason(id), "refused"),
		"second refusal: failures=%d reason=%q", failures, ledger.AttemptFailureReason(id))
	results = slot(sf, skipDeadline, names[1])
	require(results[0].skipped && strings.Contains(results[0].reason, "inside the 12s backoff window (2 consecutive failures"),
		"tick inside the wider backoff ran: skipped=%v reason=%q", results[0].skipped, results[0].reason)
	fmt.Printf("PASS the first tick after the backoff runs; refused again, the window doubles: %s\n", results[0].reason)

	refusing.Store(false)
	last, _ = ledger.AttemptState(id)
	time.Sleep(time.Until(last.Add(4*window)) + time.Second)
	results = slot(sf, runDeadline, names...)
	for _, r := range results {
		require(!r.skipped, "recovery slot: timer %s skipped (%s)", r.trigger, r.reason)
	}
	_, failures = ledger.AttemptState(id)
	require(failures == 0 && ledger.AttemptFailureReason(id) == "",
		"recovery left failures=%d reason=%q", failures, ledger.AttemptFailureReason(id))
	results = slot(sf, runDeadline, names...)
	for _, r := range results {
		require(!r.skipped, "slot after recovery: timer %s skipped (%s)", r.trigger, r.reason)
	}
	fmt.Println("PASS after the backoff both timers run cleanly, the streak clears to 0, and the next slot runs both again")

	fmt.Printf("LOCAL-FLOW-BACKOFF-E2E PASS: 9 scenarios, %d storage queries, %s\n",
		queries.Load(), time.Since(t0).Round(time.Millisecond))
}
