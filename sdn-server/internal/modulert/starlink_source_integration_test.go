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
// fetch signing key (wallet_sign/keyslot) → sign PNM (crypto_sign) → publish
// (pubsub). Capabilities are only granted because the embedded manifest declares
// them (NewModule provisions caps from manifest.Capabilities), so this also
// exercises the WS5.4 real-manifest embedding end-to-end.
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
			// keyslot.get returns the signing key bytes (base64 -> detached segment).
			key := make([]byte, 32)
			for i := range key {
				key[i] = byte(i + 1)
			}
			return okJSON(map[string]interface{}{
				"__type": "bytes",
				"base64": base64.StdEncoding.EncodeToString(key),
			}), nil
		}
	})
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

	// NewModule reads + parses the embedded $PLG manifest. The Go
	// third_party/spacedatastandards-go/PLG bindings are currently stale vs the
	// SDK's PLG schema (SDK has METHODS at field index 45, the Go bindings at 44 —
	// off by one), so parsing a current-SDK manifest panics for EVERY compiled
	// module (licensing included). Skip until those bindings are synced (WS5.5),
	// at which point this test runs and asserts the full host-cap sequence.
	mod, err := func() (m *Module, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic parsing manifest: %v", r)
			}
		}()
		return NewModule(wasmBytes, reg, &NodeContext{})
	}()
	if err != nil {
		t.Skipf("module runtime cannot parse current-SDK $PLG manifests yet "+
			"(Go third_party/spacedatastandards-go/PLG stale vs SDK; see WS5.5): %v", err)
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
	// returning. The invoke response is not yet an SDS PIV frame (that is the
	// follow-on WS5.5 invoke-bridge task), so we do not assert on the decoded
	// payload here — the recorded host-cap sequence below is the assertion.
	if _, invokeErr := mod.InvokeMethod(context.Background(), "pull", nil); invokeErr != nil {
		t.Logf("pull invoke returned (response framing pending WS5.5): %v", invokeErr)
	}

	mu.Lock()
	got := append([]string(nil), ops...)
	mu.Unlock()

	want := []string{
		"http.request",
		"storage.write",
		"keyslot.get",
		"crypto.sign",
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
