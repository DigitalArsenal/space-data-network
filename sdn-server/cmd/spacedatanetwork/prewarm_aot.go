package main

// prewarm_aot.go: `spacedatanetwork prewarm-aot` AOT-compiles the FlatSQL
// engine (and the engine-link shim) into the machine-wide AOT cache the
// daemon reads at startup. Production daemons open the engine with
// WithPrecompiledAOTCache and NEVER invoke the WasmEdge AOT compiler on the
// service path; on a cache miss they fall back to the interpreter, which is
// ~100x slower for query workloads. An operator runs this once per host — as
// the SAME user/HOME the daemon runs under, so the cache dir resolves
// identically — to populate the cache. Idempotent: a second run is a fast
// no-op.

import (
	"fmt"
	"io"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

var prewarmAOTCacheDir string

var prewarmAOTCmd = &cobra.Command{
	Use:   "prewarm-aot",
	Short: "Precompile the FlatSQL engine AOT artifact into the daemon's cache",
	Long: `Precompile the FlatSQL-WASM engine (and the engine-link shim) into the
machine-wide AOT artifact cache the daemon reads at startup.

Production daemons open the engine with a precompiled-only cache and never
invoke the WasmEdge AOT compiler on the service path; on a cache miss they
fall back to the interpreter, which is ~100x slower for query workloads. Run
this once per host to populate the cache so daemon startup finds a native
artifact instead of interpreting.

--cache-dir defaults to the exact directory the daemon resolves
(os.UserCacheDir()/flatsql-aot, i.e. $XDG_CACHE_HOME/flatsql-aot), so run this
as the SAME user/HOME the daemon runs under or the daemon won't find the
artifact. The command is idempotent: a second run finds the cached artifacts
and exits fast without recompiling.`,
	RunE: runPrewarmAOT,
}

func init() {
	prewarmAOTCmd.Flags().StringVar(&prewarmAOTCacheDir, "cache-dir", "", "AOT artifact cache directory (default: the daemon's os.UserCacheDir()/flatsql-aot)")
	rootCmd.AddCommand(prewarmAOTCmd)
}

func runPrewarmAOT(cmd *cobra.Command, args []string) error {
	cacheDir := strings.TrimSpace(prewarmAOTCacheDir)
	if cacheDir == "" {
		cacheDir = storage.EngineAOTCacheDir()
	}
	return prewarmAOTArtifacts(cmd.OutOrStdout(), cacheDir)
}

// prewarmAOTArtifacts compiles the engine (mandatory) and the engine-link shim
// (best-effort) into cacheDir, printing each artifact path with its status. It
// is factored out of the cobra RunE so tests can drive it against a temp cache
// dir with a captured writer.
func prewarmAOTArtifacts(out io.Writer, cacheDir string) error {
	fmt.Fprintf(out, "prewarming FlatSQL AOT artifacts into %s\n", cacheDir)

	// The engine artifact is the one that matters: without it every daemon
	// query runs INTERPRETED. Fail loud (non-zero exit) if it can't compile —
	// the usual cause is a libwasmedge built without the AOT/LLVM compiler.
	enginePath, enginePresent, err := flatsqlrt.PrewarmEngineAOT(cacheDir)
	if err != nil {
		return fmt.Errorf("prewarm FlatSQL engine AOT artifact: %w\n"+
			"the linked libwasmedge has no AOT compiler — install/point at a libwasmedge built with the AOT (LLVM) compiler and re-run", err)
	}
	reportPrewarm(out, "flatsql engine", enginePath, enginePresent)

	// The engine-link shim only matters for engine-linked flow mounts. Compile
	// it best-effort: the engine compiled above, so the same compiler is
	// available, but a shim failure must never mask the successful engine
	// prewarm. Both share cacheDir under distinct prefixes.
	shimPath, shimPresent, shimErr := flowrt.PrewarmLinkShimAOT(cacheDir)
	if shimErr != nil {
		fmt.Fprintf(out, "  flatsql-link shim: SKIPPED (%v)\n", shimErr)
	} else {
		reportPrewarm(out, "flatsql-link shim", shimPath, shimPresent)
	}
	return nil
}

func reportPrewarm(out io.Writer, label, path string, alreadyPresent bool) {
	status := "compiled"
	if alreadyPresent {
		status = "already present"
	}
	fmt.Fprintf(out, "  %s: %s (%s)\n", label, path, status)
}
