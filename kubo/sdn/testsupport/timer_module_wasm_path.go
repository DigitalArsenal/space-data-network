package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// timerModuleWasmPathSuffixes locate a REAL module-sdk WASM artifact that
// declares a cron Timer in its manifest and loads cleanly under modulert — the
// celestrak-supgp data-source module (manifest timer "celestrak-supgp-pull",
// method "pull", 2h default). Tests that exercise the install + cron-schedule
// pipeline reuse it as the real-module fixture (verified: it loads and its
// InvokeCron("pull") runs the guest and returns in ~milliseconds).
var timerModuleWasmPathSuffixes = [][]string{
	{
		"space-data-network-modules",
		"data-source",
		"celestrak-supgp",
		"dist",
		"isomorphic",
		"module.wasm",
	},
}

// TimerModuleSensitiveCaps is the celestrak-supgp module's declared SENSITIVE
// capabilities (verified against the loaded manifest). A test approves these for
// the module's content hash to admit it through the fail-closed capability gate,
// and OMITS one to exercise a denial. crypto_sign is also declared but is not
// sensitive, so it needs no approval.
var TimerModuleSensitiveCaps = []string{"http", "storage_ingest", "wallet_sign", "pubsub"}

// TimerModuleTimerMethod is the method id of the timer the fixture declares (the
// scheduler fires it; the settings API reschedules it).
const TimerModuleTimerMethod = "pull"

// findTimerModuleWasmPath resolves the timer-declaring module artifact for test
// packages running from either a normal checkout or a git worktree.
func findTimerModuleWasmPath(t testing.TB, callerDepth int) (string, bool) {
	t.Helper()
	if envPath := os.Getenv("SDN_TIMER_MODULE_WASM_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath, true
		}
	}

	_, callerFile, _, ok := runtime.Caller(callerDepth)
	if !ok {
		t.Fatalf("runtime.Caller(%d) failed", callerDepth)
	}

	anchorDir := filepath.Dir(callerFile)
	var candidates []string
	candidates = appendStackPackageArtifactCandidates(candidates, anchorDir, timerModuleWasmPathSuffixes)
	for _, suffix := range timerModuleWasmPathSuffixes {
		candidates = append(candidates,
			filepath.Join(append([]string{anchorDir, "..", "..", "..", ".."}, suffix...)...),
			filepath.Join(append([]string{anchorDir, "..", "..", "..", "..", "..", ".."}, suffix...)...),
		)
	}

	for _, candidate := range candidates {
		cleaned := filepath.Clean(candidate)
		if _, err := os.Stat(cleaned); err == nil {
			return cleaned, true
		}
	}
	return "", false
}

// SkipIfNoTimerModuleWasm skips tests whose purpose is to exercise the real
// timer-declaring module artifact when it is not present in this checkout.
func SkipIfNoTimerModuleWasm(t testing.TB) string {
	t.Helper()
	if path, ok := findTimerModuleWasmPath(t, 2); ok {
		return path
	}
	t.Skip("real timer-declaring module WASM artifact not available in this checkout")
	return ""
}
