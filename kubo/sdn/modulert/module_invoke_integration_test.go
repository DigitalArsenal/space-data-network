package modulert

// Checkpoint acceptance test for the kubo rebase Phase 2b module-runtime port:
// prove that a single real WASM module loads on the kubo-hosted runtime AND
// that a method can be INVOKED through it end-to-end — the "degenerate flow".
//
// The invoke path exercised here is the full one:
//
//	InvokeMethod → encodePluginInvokeRequestFrames (SDS $PIV request, built with
//	the ported spacedatastandards PIV FlatBuffer bindings) → WasmEdge executes
//	the guest export plugin_invoke_stream → the guest's own method dispatcher
//	runs → decodePluginInvokeResponseBytes (SDS $PIV response) →
//	extractPluginInvokePayload.
//
// We assert on the module-PRODUCED, per-method structured responses (not a Go
// host error), which is what proves the round trip actually reached the guest
// and was dispatched by method id:
//
//   - A DECLARED method ("server_configure_runtime") is dispatched: the guest
//     runs that method's input-port validation and returns its specific
//     "Missing required input port: config" (status 400). A module that never
//     received/dispatched the call could not name that port.
//   - An UNDECLARED method returns a DISTINCT "Unknown method" (status 404),
//     proving the guest's dispatcher discriminates by method id.
//
// Both cases are deterministic and require no host capabilities, so the
// checkpoint proves invoke without first porting the caps services (p2p,
// secrets, storage) that a data-producing invoke would need.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/testsupport"
)

func TestModuleInvokesMethodThroughRuntime(t *testing.T) {
	t.Parallel()

	wasmPath := testsupport.SkipIfNoLicensingModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", wasmPath, err)
	}

	// The licensing manifest declares sensitive capabilities that default-deny
	// at load. Pre-approve them so NewModule admits the module; the invoke
	// itself needs no host capability.
	policy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore: %v", err)
	}
	moduleHash := ContentHashHex(wasmBytes)
	for _, capability := range []string{"ipfs", "protocol_dial", "wallet_sign", "protocol_handle", "crypto_sign", "crypto_verify"} {
		if _, err := policy.Approve(CapabilityApproval{
			ModuleHash: moduleHash,
			Capability: capability,
			PluginID:   "licensing",
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s): %v", capability, err)
		}
	}

	mod, err := NewModule(wasmBytes, nil, &NodeContext{CapabilityPolicy: policy})
	if err != nil {
		t.Fatalf("NewModule(real licensing WASM): %v", err)
	}
	defer func() {
		if closeErr := mod.Close(); closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
	}()

	// Sanity: the method we invoke below must actually be declared by the loaded
	// module, so the assertion below tests real per-method dispatch.
	manifest := mod.Manifest()
	if manifest == nil {
		t.Fatal("expected a parsed manifest")
	}
	const declaredMethod = "server_configure_runtime"
	if !hasInvokeMethod(manifest, declaredMethod) {
		t.Fatalf("expected declared method %q in manifest methods %+v", declaredMethod, manifest.Methods)
	}

	ctx := context.Background()

	// ── Invoke a DECLARED method through plugin_invoke_stream ────────────────
	// With no input frames the guest dispatches server_configure_runtime and
	// runs its required-input-port validation, returning a structured $PIV
	// failure that NAMES the missing port. Reaching that message proves the
	// call round-tripped into the guest and was dispatched by method id.
	_, err = mod.InvokeMethod(ctx, declaredMethod, nil)
	if err == nil {
		t.Fatalf("expected %q with no input to report its missing required port", declaredMethod)
	}
	if !strings.Contains(err.Error(), "Missing required input port") ||
		!strings.Contains(err.Error(), "config") {
		t.Fatalf("expected module-dispatched port-validation error naming \"config\", got: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected the guest's structured PIV status 400 for a bad-input invoke, got: %v", err)
	}

	// ── Invoke an UNDECLARED method: the guest's dispatcher rejects it with a
	// DISTINCT unknown-method response (status 404), proving dispatch keys off
	// the method id rather than always returning the same error. ─────────────
	_, err = mod.InvokeMethod(ctx, "no-such-method", nil)
	if err == nil {
		t.Fatal("expected an unknown method id to be rejected by the guest dispatcher")
	}
	if !strings.Contains(err.Error(), "Unknown method") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected the guest's \"Unknown method\" (404) response, got: %v", err)
	}
}

// hasInvokeMethod reports whether the parsed manifest declares an invokable
// method with the given id.
func hasInvokeMethod(m *Manifest, methodID string) bool {
	if m == nil {
		return false
	}
	for _, method := range m.Methods {
		if method.MethodID == methodID {
			return true
		}
	}
	return false
}
