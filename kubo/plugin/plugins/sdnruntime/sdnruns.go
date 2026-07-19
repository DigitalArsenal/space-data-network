package sdnruntime

// Supplemental-OMM OD run engine wiring — INERT pending the WASM-only replacement.
//
// PER SDN_OD_FLOW_LOOP.md STOP BLOCK (non-negotiable, 2026-07-18): the OD run must
// be ONE composed WASM module, pthreads-multithreaded internally; the Go host may
// ONLY expose network/filesystem host connectors and run the baked module. It must
// NEVER drive a pool, invoke provider modules per-batch, batch/cap/probe from Go,
// or store results from Go — and that includes a "resident-reactor Go fitter" pool
// (explicitly named as forbidden), not just the newer Go FlowRunEngine/FlowPool
// rework. Both the repudiated flow cut-over AND the older ReactorFitter +
// StoreEphemerisSource + sdnruns.Runner wiring are therefore out of bounds here.
//
// This file intentionally wires NOTHING: no fitter, no ephemeris source, no
// runner, no cron registration, no live-smoke trigger. It only opens the
// (read-only, historical) run-record store so GET /sdn/v1/runs keeps serving past
// runs, and logs that the OD run is disabled pending the WASM-only pthreads
// module. The Go-orchestrated fit code this file used to wire
// (sdn/sdnruns/{fit.go,ephemeris_source.go,runner.go}) is left in the tree
// UNREFERENCED — do not re-wire it; the replacement is an isomorphic-pthreads
// WASM module owned by the od-flow-module / deploy nodes, not this plugin.

import (
	"path/filepath"
	"strings"
	"sync"

	core "github.com/ipfs/kubo/core"

	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnruns"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// ---------------------------------------------------------------------------
// Runs() accessor (stash-on-Start, mirrors Services()/Installer()).
// ---------------------------------------------------------------------------

var (
	runsMu   sync.RWMutex
	liveRuns *sdnruns.Store
)

// Runs returns the live supplemental-OMM run store, or nil when the runtime is
// disabled or has not started yet. The sdnapi plugin reaches it through this.
func Runs() *sdnruns.Store {
	runsMu.RLock()
	defer runsMu.RUnlock()
	return liveRuns
}

func setRuns(s *sdnruns.Store) {
	runsMu.Lock()
	liveRuns = s
	runsMu.Unlock()
}

// startSupplementalOMMRuns opens the (read-only) run-record store so historical
// runs remain visible at GET /sdn/v1/runs, then returns WITHOUT constructing or
// registering any OD fitter/runner — the Go host does not drive OD fits. sdnDir is
// <repo>/sdn ("" => no-persistence). Called once from sdnruntime.Start's marked
// block. Never fatal.
func startSupplementalOMMRuns(node *core.IpfsNode, svc *sdnservices.Services, installer *sdnmodules.Installer, sdnDir string) {
	runsDir := ""
	if strings.TrimSpace(sdnDir) != "" {
		runsDir = filepath.Join(sdnDir, "runs")
	}
	store, err := sdnruns.NewStore(runsDir)
	if err != nil {
		log.Warnf("SDN supplemental-OMM run store unavailable: %v", err)
		return
	}
	setRuns(store)

	log.Infof("SDN supplemental-OMM OD run: DISABLED (Go-orchestration purge, SDN_OD_FLOW_LOOP.md STOP block) — pending the isomorphic-pthreads WASM-only module; GET /sdn/v1/runs still serves historical runs")
}
