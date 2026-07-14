package modulert_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
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
// (api.starlink.com MANIFEST.txt) through the real HTTP host capability, and
// the pull's store→sign→publish chain runs against recording fakes.
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
	var pnmSigned []byte
	record := func(op string) {
		mu.Lock()
		ops = append(ops, op)
		mu.Unlock()
	}

	reg := modulert.NewCapabilityRegistry()
	// REAL outbound HTTP — the pull GETs the live Starlink MANIFEST.txt.
	reg.Register("http", caps.NewHTTPCapFactory())
	reg.Register("storage_ingest", func(_ *modulert.Module) modulert.CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return liveOKJSON(map[string]interface{}{"inserted": 1}), nil
		}
	})
	reg.Register("wallet_sign", func(_ *modulert.Module) modulert.CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			// keyslot.sign host-side oracle: detached signature, never a raw key.
			sig := make([]byte, 64)
			return liveOKJSON(map[string]interface{}{
				"signature": base64.StdEncoding.EncodeToString(sig),
				"algorithm": "ed25519",
			}), nil
		}
	})
	reg.Register("crypto_sign", func(_ *modulert.Module) modulert.CapHandler {
		return func(op string, payload []byte) ([]byte, error) {
			record(op)
			mu.Lock()
			pnmSigned = append([]byte(nil), payload...)
			mu.Unlock()
			sig := make([]byte, 64)
			return liveOKJSON(map[string]interface{}{
				"signature": base64.StdEncoding.EncodeToString(sig),
			}), nil
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

	// Wait for the scheduler to tick and the live pull to complete the chain.
	deadline := time.After(60 * time.Second)
	for {
		mu.Lock()
		got := append([]string(nil), ops...)
		mu.Unlock()
		if containsAll(got, []string{"storage.ingest_with_source", "keyslot.sign", "pubsub.publish"}) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("cron pull did not complete the host-cap chain in time; recorded ops: %v", got)
		case <-time.After(500 * time.Millisecond):
		}
	}

	// The signed payload derives from the LIVE upstream fetch: the module signs
	// its PNM pointer JSON ({"data": base64(pnm)}), which embeds the discover
	// URL and the discovered-resource count from the real MANIFEST.txt listing.
	mu.Lock()
	signReq := append([]byte(nil), pnmSigned...)
	mu.Unlock()
	var signPayload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(signReq, &signPayload); err != nil {
		t.Fatalf("crypto.sign payload is not JSON: %v (%.200q)", err, signReq)
	}
	pnm, err := base64.StdEncoding.DecodeString(signPayload.Data)
	if err != nil {
		t.Fatalf("crypto.sign data is not base64: %v", err)
	}
	if !strings.Contains(string(pnm), "api.starlink.com") {
		t.Fatalf("signed PNM does not reference the live upstream: %.300q", pnm)
	}
	t.Logf("live cron pull completed; signed PNM: %.400s", pnm)
}

func containsAll(got, want []string) bool {
	seen := make(map[string]bool, len(got))
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}
