package modulert

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
)

// TestStarlinkSourcePullDrivesHostCapSequence loads the built spacex-starlink-source
// data-source module under the real module runtime and asserts that invoking its
// TIMERS-driven `pull` method (what the node cron re-invokes) drives the full
// data-source host-capability sequence: fetch (http) → store (storage_write) →
// host-side sign the PNM (keyslot.sign, via the wallet_sign capability) → publish
// (pubsub). Capabilities are only granted because the embedded manifest declares
// them (NewModule provisions caps from manifest.Capabilities), so this also
// exercises the WS5.4 real-manifest embedding end-to-end.
//
// Post-B2 signing shape: B2 removed the raw `keyslot.get` key export and the
// in-guest `crypto.sign` step, replacing them with a single host-side signing
// oracle `keyslot.sign` (routed through the wallet_sign capability). The rebuilt
// starlink module (modules repo d482e02) calls that oracle, so the sequence is
// four ops, not the old five, and the wallet_sign fake must return a
// {signature} result (not raw key bytes) for run_pull to record "signed":true.
func TestStarlinkSourcePullDrivesHostCapSequence(t *testing.T) {
	t.Parallel()

	wasmPath := testsupport.SkipIfNoStarlinkSourceWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	// Recording fake capabilities. Each returns the minimal valid cap result the
	// C++ module's run_pull parses so it proceeds to the next step, and records
	// the dispatched operation so we can assert ordering.
	var mu sync.Mutex
	var ops []string
	record := func(op string) {
		mu.Lock()
		ops = append(ops, op)
		mu.Unlock()
	}

	reg := NewCapabilityRegistry()
	reg.Register("http", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			// Non-empty body so run_pull treats the listing as fetched + stores it.
			return okJSON(map[string]interface{}{
				"status":        200,
				"headers":       map[string]string{},
				"body":          "ephemeris-listing-1.txt\nephemeris-listing-2.txt\n",
				"body_encoding": "utf8",
			}), nil
		}
	})
	reg.Register("storage_write", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return okJSON(map[string]interface{}{"written": true}), nil
		}
	})
	reg.Register("wallet_sign", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			// Post-B2: keyslot.sign is a host-side signing oracle that returns a
			// detached signature (never the raw key). run_pull records
			// "signed":true only when it can parse a {signature} result here.
			sig := make([]byte, 64)
			return okJSON(map[string]interface{}{
				"signature": base64.StdEncoding.EncodeToString(sig),
			}), nil
		}
	})
	// crypto_sign is registered defensively: post-B2 the starlink module signs
	// host-side via keyslot.sign and does not drive a separate crypto.sign step,
	// but keeping the handler avoids an unknown-capability error if a module
	// variant ever signs in-guest.
	reg.Register("crypto_sign", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			sig := make([]byte, 64)
			return okJSON(map[string]interface{}{
				"signature": base64.StdEncoding.EncodeToString(sig),
			}), nil
		}
	})
	reg.Register("pubsub", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return okJSON(map[string]interface{}{"published": true}), nil
		}
	})

	// loop B1: the real starlink-source manifest declares sensitive
	// capabilities (http, pubsub, storage_write, wallet_sign) that now
	// require an explicit operator approval before NewModule will load it
	// (default-deny). Pre-approve them so this test keeps exercising the
	// host-capability sequence; approval enforcement itself is covered by
	// capability_policy_test.go.
	policy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	moduleHash := ContentHashHex(wasmBytes)
	for _, capability := range []string{"http", "pubsub", "storage_write", "wallet_sign"} {
		if _, err := policy.Approve(CapabilityApproval{
			ModuleHash: moduleHash,
			Capability: capability,
			PluginID:   "com.orbpro.spacex-starlink-source",
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s) failed: %v", capability, err)
		}
	}

	// NewModule reads + parses the embedded $PLG manifest. Since WS5.5 the SDK
	// encodes with the same spacedatastandards 1.136 PLG layout the Go bindings
	// read, so a parse failure here is a real wire-layout regression.
	mod, err := func() (m *Module, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic parsing manifest: %v", r)
			}
		}()
		return NewModule(wasmBytes, reg, &NodeContext{CapabilityPolicy: policy})
	}()
	if err != nil {
		t.Fatalf("NewModule failed to parse the module's $PLG manifest "+
			"(SDK encoder vs Go PLG bindings wire-layout mismatch?): %v", err)
	}
	defer func() {
		if closeErr := mod.Close(); closeErr != nil {
			t.Fatalf("Close() failed: %v", closeErr)
		}
	}()

	// The embedded manifest must expose the data-source contract that grants caps
	// and schedules the pull timer.
	manifest := mod.Manifest()
	if manifest == nil {
		t.Fatal("expected manifest to be available")
	}
	if manifest.PluginID != "com.orbpro.spacex-starlink-source" {
		t.Fatalf("expected plugin id com.orbpro.spacex-starlink-source, got %q", manifest.PluginID)
	}
	if !hasMethod(manifest, "pull") {
		t.Fatalf("expected pull method, got %+v", manifest.Methods)
	}
	for _, cap := range []string{"http", "storage_write", "wallet_sign", "crypto_sign", "pubsub"} {
		if !hasCapability(manifest, cap) {
			t.Fatalf("expected capability %q, got %v", cap, manifest.Capabilities)
		}
	}
	if !hasTimerForMethod(manifest, "starlink-pull", "pull") {
		t.Fatalf("expected starlink-pull timer invoking pull, got %+v", manifest.Timers)
	}

	// Invoke pull — exactly what the cron TIMERS scheduler re-invokes. run_pull
	// runs to completion (firing all host-caps) inside plugin_invoke_stream before
	// returning. The module still returns a raw JSON summary rather than an SDS
	// PIV frame (follow-on framing work), so we do not assert on the decoded
	// payload here — the recorded host-cap sequence below is the assertion.
	if _, invokeErr := mod.InvokeMethod(context.Background(), "pull", nil); invokeErr != nil {
		t.Logf("pull invoke returned (module response is a raw JSON summary, not PIV yet): %v", invokeErr)
	}

	mu.Lock()
	got := append([]string(nil), ops...)
	mu.Unlock()

	// Post-B2 host-side-signing sequence: fetch → store → keyslot.sign → publish.
	// (Was http→store→keyslot.get→crypto.sign→publish before B2 collapsed key
	// export + in-guest signing into the single keyslot.sign oracle.)
	want := []string{
		"http.request",
		"storage.write",
		"keyslot.sign",
		"pubsub.publish",
	}
	assertSubsequence(t, got, want)
}

func hasMethod(m *Manifest, methodID string) bool {
	for _, method := range m.Methods {
		if method.MethodID == methodID {
			return true
		}
	}
	return false
}

func hasCapability(m *Manifest, cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

func hasTimerForMethod(m *Manifest, timerID, methodID string) bool {
	for _, timer := range m.Timers {
		if timer.TimerID == timerID && timer.MethodID == methodID {
			return true
		}
	}
	return false
}

// assertSubsequence asserts that want appears as an in-order subsequence of got.
func assertSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("host-cap sequence mismatch:\n  want (in order): %v\n  got:             %v\n  matched %d/%d", want, got, i, len(want))
	}
}
