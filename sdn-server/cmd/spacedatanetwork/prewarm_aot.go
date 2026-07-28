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
	"slices"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

var (
	prewarmAOTCacheDir string
	prewarmAOTFlows    []string
)

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

FLOWS. The same is true, and worse, for flow modules: a timer-served ingest
flow that misses its artifact runs interpreted for its whole run — tens of
minutes on a small host instead of seconds. Pass --config and every flow that
config declares under flows.services is prewarmed too, resolved exactly the way
the daemon resolves it; --flow prewarms an individual flow (program ID, bundle
directory, or .wasm path) without a config.

--cache-dir defaults to the exact directory the daemon resolves
(os.UserCacheDir()/flatsql-aot, i.e. $XDG_CACHE_HOME/flatsql-aot), so run this
as the SAME user/HOME the daemon runs under or the daemon won't find the
artifact. A daemon started with PrivateTmp/ProtectHome resolves a DIFFERENT
directory than a shell on the same box — check the daemon's "AOT artifact
unavailable" log line for the path it actually wants. The command is
idempotent: a second run finds the cached artifacts and exits fast without
recompiling.`,
	RunE: runPrewarmAOT,
}

func init() {
	prewarmAOTCmd.Flags().StringVar(&prewarmAOTCacheDir, "cache-dir", "", "AOT artifact cache directory (default: the daemon's os.UserCacheDir()/flatsql-aot)")
	prewarmAOTCmd.Flags().StringArrayVar(&prewarmAOTFlows, "flow", nil, "flow to prewarm (program ID, bundle directory, or .wasm path); repeatable. Added to any flows found via --config")
	rootCmd.AddCommand(prewarmAOTCmd)
}

func runPrewarmAOT(cmd *cobra.Command, args []string) error {
	cacheDir := strings.TrimSpace(prewarmAOTCacheDir)
	if cacheDir == "" {
		cacheDir = storage.EngineAOTCacheDir()
	}
	flows, store, err := prewarmFlowTargets(cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if err := prewarmAOTArtifacts(cmd.OutOrStdout(), cacheDir); err != nil {
		return err
	}
	return prewarmFlowAOTArtifacts(cmd.OutOrStdout(), cacheDir, flows, store)
}

// prewarmFlowTargets collects the flows to prewarm: the ones this daemon's
// config declares as services, plus any named with --flow. The config is
// optional — an operator priming one bundle by path should not need one — but
// when it is given, the flow STORE is opened from it so installed-by-program-ID
// references resolve the same way the daemon resolves them.
func prewarmFlowTargets(out io.Writer) ([]string, *flowrt.FlowStore, error) {
	flows := append([]string(nil), prewarmAOTFlows...)
	if strings.TrimSpace(configPath) == "" {
		return flows, nil, nil
	}
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load config for flow prewarm: %w", err)
	}
	storagePath := strings.TrimSpace(cfg.Flows.StoragePath)
	var store *flowrt.FlowStore
	if storagePath != "" {
		store, err = flowrt.NewFlowStore(storagePath)
		if err != nil {
			return nil, nil, fmt.Errorf("open flow store %s: %w", storagePath, err)
		}
	}
	for _, service := range cfg.Flows.Services {
		if ref := strings.TrimSpace(service.Flow); ref != "" && !slices.Contains(flows, ref) {
			flows = append(flows, ref)
		}
	}
	fmt.Fprintf(out, "config %s declares %d flow service(s)\n", configPath, len(cfg.Flows.Services))
	return flows, store, nil
}

// prewarmFlowAOTArtifacts compiles each flow into cacheDir.
//
// One flow failing does not stop the others: these are independent modules, and
// an operator priming three ingest flows is better served by two hits and a
// named failure than by a run that stops at the first problem. The failure is
// still reported and still fails the command, so a deploy step notices.
func prewarmFlowAOTArtifacts(out io.Writer, cacheDir string, flows []string, store *flowrt.FlowStore) error {
	var failed []string
	for _, flowRef := range flows {
		path, present, err := flowrt.PrewarmFlowAOT(flowRef, store, cacheDir)
		if err != nil {
			fmt.Fprintf(out, "  flow %s: FAILED (%v)\n", flowRef, err)
			failed = append(failed, flowRef)
			continue
		}
		reportPrewarm(out, "flow "+flowRef, path, present)
	}
	if len(failed) > 0 {
		return fmt.Errorf("prewarm failed for %d flow(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
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
