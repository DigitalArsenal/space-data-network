package node

// The HD-wallet wasm is what turns this node's mnemonic into its PEER IDENTITY.
// Resolving it out of ANOTHER install's directory is how a purge of a retired
// node silently re-identifies a live one, so the daemon's own install must come
// first in the search order.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutableRelativeWalletWasmPathsPreferOwnInstall(t *testing.T) {
	t.Parallel()

	paths := executableRelativeWalletWasmPaths()
	if len(paths) != 3 {
		t.Fatalf("expected three executable-relative candidates, got %v", paths)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)

	if paths[0] != filepath.Join(dir, "wasm", "hd-wallet-wasi.wasm") {
		t.Fatalf("first candidate = %q, want <exeDir>/wasm/hd-wallet-wasi.wasm", paths[0])
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			t.Fatalf("candidate %q is not absolute", p)
		}
		if !strings.HasSuffix(p, "hd-wallet-wasi.wasm") {
			t.Fatalf("candidate %q is not the WASI wallet artifact", p)
		}
	}
}
