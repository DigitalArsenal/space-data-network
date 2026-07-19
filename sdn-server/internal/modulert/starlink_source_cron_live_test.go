package modulert_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// fastCronModule shortens the module's declared timer cadence so the manager's
// cron scheduler fires within test time. Everything else — the scheduling
// loop, InvokeCron, the module, and the real HTTP capability — is the real
// production path. (providerMinimumCadence clamps config overrides up to the
// module's declared DefaultInterval, which is 1h for starlink-pull.)
type fastCronModule struct {
	*modulert.Module
}

func (f fastCronModule) CronMethods() []plugins.CronMethodSpec {
	specs := f.Module.CronMethods()
	for i := range specs {
		specs[i].DefaultInterval = "1s"
	}
	return specs
}

func liveOKJSON(result interface{}) []byte {
	encoded, _ := json.Marshal(map[string]interface{}{"ok": true, "result": result})
	return encoded
}

// TestStarlinkSourceCronLivePull is the WS5.6 live Go-node cron run: the
// plugins.Manager cron scheduler (the same path the node uses for manifest
// TIMERS) drives the module's pull against the REAL Starlink upstream
// (api.starlink.com MANIFEST.txt) through the real HTTP host capability.
//
// Under the CURRENT module contract (post Go-orchestration purge) pull
// returns its $OEM record directly for in-wasm consumption and never drives
// storage_ingest/wallet_sign/crypto_sign/pubsub — this test used to wait for
// that store→sign→publish chain, which was exactly the purged Go
// orchestration (see starlink_source_integration_test.go's doc comment for
// the full rationale). InvokeCron (what the cron scheduler calls) still
// routes through the same $PIV-envelope decode as InvokeMethod, which errors
// on pull's raw $OEM response by design; that error is expected here and is
// not fatal to the manager. The meaningful, still-attainable assertion at
// this call boundary (plugins.Manager, not the low-level module runtime) is
// that a live pull actually round-trips through the real HTTP capability
// (proving the cron wiring works end-to-end against the network) and that it
// NEVER reaches the legacy store/sign/publish capabilities.
//
// Requires network; gated behind SDN_LIVE_STARLINK_CRON=1.
func TestStarlinkSourceCronLivePull(t *testing.T) {
	if os.Getenv("SDN_LIVE_STARLINK_CRON") == "" {
		t.Skip("set SDN_LIVE_STARLINK_CRON=1 to run the live cron pull (real network)")
	}

	wasmPath := testsupport.SkipIfNoStarlinkSourceWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	var mu sync.Mutex
	var ops []string
	record := func(op string) {
		mu.Lock()
		ops = append(ops, op)
		mu.Unlock()
	}

	reg := modulert.NewCapabilityRegistry()
	// REAL outbound HTTP — the pull GETs the live Starlink MANIFEST.txt.
	reg.Register("http", caps.NewHTTPCapFactory())
	// storage_ingest/wallet_sign/crypto_sign/pubsub are registered only because
	// the manifest still declares them as sensitive capabilities (NewModule's
	// fail-closed gate needs a handler present); pull must never actually call
	// them under the current contract — see the assertion below.
	reg.Register("storage_ingest", func(_ *modulert.Module) modulert.CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return liveOKJSON(map[string]interface{}{"inserted": 1}), nil
		}
	})
	reg.Register("wallet_sign", func(_ *modulert.Module) modulert.CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return liveOKJSON(map[string]interface{}{"signature": "", "algorithm": "ed25519"}), nil
		}
	})
	reg.Register("crypto_sign", func(_ *modulert.Module) modulert.CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return liveOKJSON(map[string]interface{}{"signature": ""}), nil
		}
	})
	reg.Register("pubsub", func(_ *modulert.Module) modulert.CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return liveOKJSON(map[string]interface{}{"published": true}), nil
		}
	})

	mod, err := modulert.NewModule(wasmBytes, reg, &modulert.NodeContext{})
	if err != nil {
		t.Fatalf("NewModule failed: %v", err)
	}
	defer mod.Close()

	mgr := plugins.New()
	if err := mgr.Register(fastCronModule{mod}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.StartAll(ctx, plugins.RuntimeContext{}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}
	defer mgr.Close()

	// Wait for the scheduler to tick and the live pull to round-trip through
	// the real HTTP capability (MANIFEST fetch + at least one object fetch).
	deadline := time.After(60 * time.Second)
	for {
		mu.Lock()
		got := append([]string(nil), ops...)
		mu.Unlock()
		if countLiveOp(got, "http.request") >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("cron pull did not complete a live http.request round-trip in time; recorded ops: %v", got)
		case <-time.After(500 * time.Millisecond):
		}
	}

	// Regression guard: the purged Go-orchestration host-cap sequence
	// (storage.ingest_with_source / keyslot.sign / pubsub.publish) must never
	// fire — pull returns $OEM directly to its caller now.
	mu.Lock()
	got := append([]string(nil), ops...)
	mu.Unlock()
	for _, forbidden := range []string{"storage.ingest_with_source", "keyslot.sign", "pubsub.publish"} {
		if n := countLiveOp(got, forbidden); n != 0 {
			t.Fatalf("expected the live cron pull to NEVER drive %q (purged Go-orchestration "+
				"host-cap sequence), got %d invocation(s) in %v", forbidden, n, got)
		}
	}
	t.Logf("live cron pull completed against the real Starlink upstream; recorded ops: %v", got)
}

func countLiveOp(ops []string, op string) int {
	n := 0
	for _, o := range ops {
		if o == op {
			n++
		}
	}
	return n
}
