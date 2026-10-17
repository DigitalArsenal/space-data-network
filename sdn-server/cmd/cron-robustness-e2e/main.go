// Command cron-robustness-e2e runs the real WASM retrieval flow, host bridge,
// HTTP connector and persistent source ledger against a loopback publisher.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
)

// This is the published flow guest, not a Go reimplementation. Provenance and
// reproduction are in testdata/README.md.
//
//go:embed testdata/retrieval-flow.wasm
var guest []byte

//go:embed testdata/flow.json
var topology []byte

func require(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}

func service(artifact, url string, ledger *sourcemetrics.Store) *flowrt.ServiceFlow {
	policy, err := modulert.NewCapabilityPolicyStore("")
	require(err == nil, "policy: %v", err)
	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("http", caps.NewHTTPBridgeCapFactory())
	reg.Register("storage_ingest", func(*modulert.Module) modulert.CapHandler {
		return func(string, []byte) ([]byte, error) {
			panic("fixture unexpectedly tried to ingest: only failures and 304 are served")
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
	sf.SetRetrievalGate(func() (bool, string) { ledger.RecordAttempt(sf.ID()); return true, "" })
	sf.SetRetrievalOutcome(func(err error) {
		if errors.Is(err, flowrt.ErrRetrievalUnchanged) {
			ledger.RecordAttemptUnchanged(sf.ID())
		} else {
			ledger.RecordAttemptOutcome(sf.ID(), err)
		}
	})
	return sf
}

func invoke(sf *flowrt.ServiceFlow, ctx context.Context) error {
	_, err := sf.InvokeCron(ctx, "timer-gp", nil)
	return err
}

func waitRequest(ch <-chan struct{}) {
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		panic("publisher received no request")
	}
}

func main() {
	if len(os.Args) == 5 && os.Args[1] == "--shutdown-child" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ledger, err := sourcemetrics.Open(os.Args[2])
		require(err == nil, "child ledger: %v", err)
		defer ledger.Close()
		sf := service(os.Args[3], os.Args[4], ledger)
		defer sf.Close()
		require(errors.Is(invoke(sf, ctx), context.Canceled), "shutdown must cancel the flow")
		stats := sf.RuntimeDescriptor().Stats
		require(stats.LastTimerStatus == "cancelled" && stats.ErrorCount == 0, "shutdown stats: %+v", stats)
		return
	}
	run()
}

func run() {
	root, err := os.MkdirTemp("", "cron-robustness-e2e-")
	require(err == nil, "temp: %v", err)
	defer os.RemoveAll(root)
	artifact := filepath.Join(root, "guest")
	require(os.Mkdir(artifact, 0700) == nil, "create fixture directory")
	require(os.WriteFile(filepath.Join(artifact, "runtime.wasm"), guest, 0600) == nil, "write fixture")
	require(os.WriteFile(filepath.Join(artifact, "flow.json"), topology, 0600) == nil, "write topology")
	var mode atomic.Int32
	var requests atomic.Int32
	started := make(chan struct{}, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if mode.Load() == 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if mode.Load() != 2 {
			w.Header().Set("Content-Length", "1900000")
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		started <- struct{}{}
		if mode.Load() == 2 {
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					_, _ = w.Write([]byte("x"))
					w.(http.Flusher).Flush()
				}
			}
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	ledger, err := sourcemetrics.Open(root)
	require(err == nil, "ledger: %v", err)
	defer func() { ledger.Close() }()
	sf := service(artifact, upstream.URL, ledger)
	defer sf.Close()
	type fetch struct {
		bytes, ms int64
		err       string
	}
	fetches := make(chan fetch, 16)
	caps.SetFetchObserver(func(_ string, _ int, bytes, ms int64, msg string) { fetches <- fetch{bytes, ms, msg} })
	defer caps.SetFetchObserver(nil)

	// The fixed-length path is the exact io.ReadFull path in the weather dump.
	t0 := time.Now()
	require(invoke(sf, context.Background()) == nil, "guest drain should return its refusal frame")
	f := <-fetches
	require(strings.Contains(f.err, "stalled for 30s") && f.bytes == 1 && f.ms >= 29000 && f.ms < 40000,
		"stall observation: %+v", f)
	_, failures := ledger.AttemptState(sf.ID())
	require(failures == 1 && strings.Contains(ledger.AttemptFailureReason(sf.ID()), "status 400"), "stall diagnosis: failures=%d reason=%s", failures, ledger.AttemptFailureReason(sf.ID()))
	fmt.Printf("PASS real-flow body stall: %d bytes, %d ms, failure count %d\n", f.bytes, f.ms, failures)
	waitRequest(started)
	mode.Store(1)
	require(invoke(sf, context.Background()) == nil, "next flow failed")
	<-fetches
	_, failures = ledger.AttemptState(sf.ID())
	require(failures == 0, "304 retry did not clear failures")
	fmt.Println("PASS next real flow completes after timed-out transfer (304)")

	// Seed a real prior streak, then SIGTERM a separate runtime process while
	// it is blocked in the body read. Reopen its ledger to check persistence.
	for range 2 {
		ledger.RecordAttempt(sf.ID())
		ledger.RecordAttemptOutcome(sf.ID(), errors.New("prior failure"))
	}
	require(ledger.Close() == nil, "close parent ledger")
	mode.Store(0)
	exe, err := os.Executable()
	require(err == nil, "executable: %v", err)
	child := exec.Command(exe, "--shutdown-child", root, artifact, upstream.URL)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	require(child.Start() == nil, "start shutdown child")
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			<-childDone
		}
	}()
	waitRequest(started)
	shutdownAt := time.Now()
	require(child.Process.Signal(syscall.SIGTERM) == nil, "signal child")
	select {
	case err = <-childDone:
		require(err == nil, "shutdown child: %v", err)
	case <-time.After(5 * time.Second):
		panic("shutdown failed to interrupt HTTP read")
	}
	ledger, err = sourcemetrics.Open(root)
	require(err == nil, "reopen ledger: %v", err)
	require(sf.Close() == nil, "close old flow")
	sf = service(artifact, upstream.URL, ledger)
	defer sf.Close()
	last, failures := ledger.AttemptState(sf.ID())
	require(last == nil && failures == 2 && ledger.AttemptFailureReason(sf.ID()) == "prior failure", "shutdown changed prior streak or blocked retry: %v %d", last, failures)
	fmt.Printf("PASS SIGTERM: %d ms; persisted failures 2 -> 2; next tick eligible\n", time.Since(shutdownAt).Milliseconds())
	mode.Store(1)
	require(invoke(sf, context.Background()) == nil, "post-shutdown next tick")
	<-fetches
	_, failures = ledger.AttemptState(sf.ID())
	require(failures == 0, "post-shutdown retry failed")

	// Cancellation while waiting for a group slot also finalizes the attempt.
	slot := make(chan struct{}, 1)
	slot <- struct{}{}
	sf.SetExclusiveGroup("e2e", slot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require(errors.Is(invoke(sf, ctx), context.Canceled), "exclusive waiter cancellation")
	last, failures = ledger.AttemptState(sf.ID())
	require(last == nil && failures == 0, "exclusive waiter penalized")
	<-slot
	sf.SetExclusiveGroup("", nil)
	fmt.Println("PASS canceled exclusive-group waiter leaves no failure or retry gate")

	// Same production bridge/HTTP connector: an endless chunked response keeps
	// making progress but must still hit the total request deadline.
	bridge := modulert.NewHostBridge(nil, []string{"http"})
	bridge.RegisterCapHandler("http", caps.NewHTTPBridgeCapFactory()(nil, bridge))
	mode.Store(2)
	payload, _ := json.Marshal(map[string]any{"url": upstream.URL, "timeout_ms": 250})
	totalAt := time.Now()
	reply := bridge.Dispatch("http.request", payload)
	require(strings.Contains(string(reply), "deadline exceeded") && time.Since(totalAt) < time.Second*2, "chunked total deadline: %s", reply)
	f = <-fetches
	require(f.bytes > 1 && f.ms >= 200 && f.ms < 2000, "chunk progress: %+v", f)
	waitRequest(started)
	fmt.Printf("PASS chunked total deadline despite progress: %d bytes, %d ms\n", f.bytes, f.ms)

	// Hold a destination slot and prove a queued request spends its own budget
	// waiting, without reaching the publisher; cancellation releases the slot.
	mode.Store(0)
	caps.SetEgressMinIntervals(map[string]time.Duration{"127.0.0.1": time.Millisecond})
	defer caps.SetEgressMinIntervals(nil)
	holdCtx, release := context.WithCancel(context.Background())
	defer release()
	bridge.SetInvocationContext(holdCtx)
	holdDone := make(chan []byte, 1)
	go func() { holdDone <- bridge.Dispatch("http.request", []byte(fmt.Sprintf(`{"url":%q}`, upstream.URL))) }()
	waitRequest(started)
	before := requests.Load()
	queued := modulert.NewHostBridge(nil, []string{"http"})
	queued.RegisterCapHandler("http", caps.NewHTTPBridgeCapFactory()(nil, queued))
	reply = queued.Dispatch("http.request", payload)
	require(strings.Contains(string(reply), "egress pacing") && strings.Contains(string(reply), "deadline exceeded") && requests.Load() == before, "queued timeout: %s", reply)
	<-fetches
	release()
	select {
	case reply = <-holdDone:
		require(strings.Contains(string(reply), "context canceled"), "body cancellation: %s", reply)
	case <-time.After(2 * time.Second):
		panic("body cancellation hung")
	}
	<-fetches
	fmt.Println("PASS total budget includes pacing; body cancellation releases the destination slot")
	fmt.Printf("CRON-ROBUSTNESS-E2E PASS: 6 scenarios, %d upstream requests, %s\n", requests.Load(), time.Since(t0).Round(time.Millisecond))
}
