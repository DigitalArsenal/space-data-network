package wasmrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWasmEdgeLoggerIsRaisedToError pins the suppression of WasmEdge's
// per-invocation statistics dump.
//
// The dump is emitted at INFO after EVERY guest invocation whenever cost
// measuring is on — and cost measuring is REQUIRED for the fuel enforcement
// WithCostLimit provides, so it cannot simply be turned off. Measured on
// host-01 (2 vCPU) on 2026-08-08: 5176 of 8311 journal lines in five minutes,
// ~17/s indefinitely, reporting a CUMULATIVE gas counter that had reached
// 3.68e10 and therefore said nothing about the call that triggered it.
//
// Asserted at the source level because the WasmEdge Go binding exposes no
// getter for the current log threshold.
func TestWasmEdgeLoggerIsRaisedToError(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile(filepath.Join(".", "runtime.go"))
	if err != nil {
		t.Fatalf("read runtime.go: %v", err)
	}
	text := string(source)

	if !strings.Contains(text, "wasmedge.SetLogErrorLevel()") {
		t.Fatalf("wasmrt must raise WasmEdge's log threshold to ERROR at init: the statistics dump " +
			"was 62%% of this daemon's journal output")
	}
	if strings.Contains(text, "wasmedge.SetLogOff()") {
		t.Fatalf("SetLogOff would also silence genuine instantiation and trap errors, which is how a " +
			"defect becomes invisible; ERROR is the floor")
	}
}
