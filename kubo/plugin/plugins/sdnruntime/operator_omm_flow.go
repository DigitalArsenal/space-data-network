package sdnruntime

// operator_omm_flow.go is the ROLE-gated boot orchestration for the OMM host's ONE
// composed supplemental-OMM OD flow. When the node is in the "omm" role
// (SDN_ROLE=omm env, or the plugin's Config.Role key), it BAKES the composed
// wasi-threads flow (five in-wasm provider fetch/parse nodes -> threaded SGP4 OD
// fit -> in-wasm FlatSQL store) on the node and mounts it as a timer-served
// ServiceFlow.
//
// It REPLACES the old per-provider operator-ephemeris MODULE set (formerly
// maybeInstallOperatorEphemerisSet, deleted by the Go-orchestration purge —
// sdn/sdnflows/operator_ephemeris_set.go and this package's own
// operator_ephemeris_set.go), which registered seven data-source modules that
// each pulled + stored per-object $OEM through the Go storage sink. Here the
// ONE wasm flow does the fetch, the threaded fit, and the $OMM/$OCM/$OBD arena
// store — all in-wasm. Ephemeris is in-memory only; no $OEM is persisted.
//
// The Go host contributes ONLY capability plumbing: it bakes the artifact, records
// the operator approval for the flow's DECLARED http cap against its content hash
// (fail-closed gate), and registers a DUMB timer. It never batches, schedules
// beyond the timer, knows a provider, or handles a record.

import (
	"context"
	"os"
	"path/filepath"

	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnflows"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// odSupplementalCaps is the composed OD flow's DECLARED bridge capability set: http
// only (the five provider nodes fetch their constellations via the http cap, which
// keeps its own CelesTrak 2.5s/3h policy). The in-wasm FlatSQL store persists via
// the fs connector (a host primitive, not a bridge cap), and the engine link is
// wired by NewFlowRuntime, never a capability.
var odSupplementalCaps = []string{"http"}

const (
	// odSupplementalTimerID is the flow's single host-cron trigger (ODSupplementalOMMSpec t0).
	odSupplementalTimerID = "t0"
	// odSupplementalInterval pins the trigger cadence; 3h matches the reference
	// cadence and avoids overlapping full-catalog fits (ServiceFlow serializes
	// firings anyway). Overridable via home-dir cron config once installed.
	odSupplementalInterval = "3h"
	// odSupplementalPages caps the composed flow's linear memory (64KB pages).
	odSupplementalPages = 8192
)

// maybeInstallOperatorOMMFlow bakes + mounts the composed supplemental-OMM OD flow
// when the node is in the "omm" role; otherwise it is a no-op. Tolerant: a missing
// toolchain, a bake failure, or an install failure is logged and skipped, never
// fatal — the node still boots.
func (p *sdnRuntimePlugin) maybeInstallOperatorOMMFlow(
	ctx context.Context,
	flowInstaller *sdnflows.Installer,
	svc *sdnservices.Services,
) {
	if !sdnflows.OMMRoleEnabled(p.role) {
		return
	}
	if flowInstaller == nil {
		log.Warnf("SDN omm role: flow installer unavailable; OD flow NOT installed")
		return
	}
	home := flowcc.ResolveHome()
	if !home.ThreadsStaged() {
		log.Warnf("SDN omm role: flowcc wasi-threads toolchain not staged (run stage-toolchain.sh v2); OD flow NOT baked")
		return
	}
	// Stage the module guest-links (idempotent) so the bake can link them.
	if distRoot, ok := sdnflows.ResolveModulesDist(""); ok {
		if _, err := flowcc.StageModulesFromDist(home, distRoot); err != nil {
			log.Warnf("SDN omm role: stage module guest-links from %q: %v", distRoot, err)
		}
	} else {
		log.Warnf("SDN omm role: modules dist not found (set SDN_MODULES_DIST); OD flow bake may miss guest-links")
	}

	baker, err := flowrt.NewBaker(home, odSupplementalPages)
	if err != nil {
		log.Warnf("SDN omm role: build baker: %v; OD flow NOT baked", err)
		return
	}
	spec := flowrt.ODSupplementalOMMSpec()
	plg := flowrt.BuildFlowPLG(spec)
	res, err := baker.Bake(ctx, flowrt.BakeRequest{FlowPLG: plg})
	if err != nil {
		log.Warnf("SDN omm role: bake OD flow: %v", err)
		return
	}

	// Write the flow bundle (runtime.wasm + flow.plg) under the sdn home so the
	// mount + boot re-install can load it.
	bundleDir := filepath.Join(p.repoPath, "sdn", "flows", "od-supplemental-omm")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		log.Warnf("SDN omm role: create OD bundle dir: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "runtime.wasm"), res.Wasm, 0o644); err != nil {
		log.Warnf("SDN omm role: write runtime.wasm: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "flow.plg"), plg, 0o644); err != nil {
		log.Warnf("SDN omm role: write flow.plg: %v", err)
		return
	}

	// Role implies approval: record the flow's declared http cap against its
	// content hash so the fail-closed gate admits it at mount.
	p.approveODFlow(svc, bundleDir)

	if _, err := flowInstaller.Install(sdnflows.FlowSpec{
		Ref:          bundleDir,
		Capabilities: odSupplementalCaps,
		Intervals:    map[string]string{odSupplementalTimerID: odSupplementalInterval},
	}, "role:omm"); err != nil {
		log.Warnf("SDN omm role: install OD flow: %v", err)
		return
	}
	log.Infof("SDN omm role ACTIVE: baked + installed the composed supplemental-OMM OD flow (%d bytes, timer=%s @ %s) — ONE wasm flow does fetch + threaded fit + $OMM/$OCM/$OBD store, replacing the per-provider operator-ephemeris module set",
		len(res.Wasm), odSupplementalTimerID, odSupplementalInterval)
}

// approveODFlow records the operator approval for the OD flow's declared caps keyed
// by its content hash (bundle runtime.wasm trailer-stripped, exactly as
// LoadFlowService hashes it) so ProvisionBridge admits them fail-closed.
func (p *sdnRuntimePlugin) approveODFlow(svc *sdnservices.Services, bundleDir string) {
	if svc == nil || svc.NodeCtx == nil || svc.NodeCtx.CapabilityPolicy == nil {
		return
	}
	hash, herr := celestrakContentHash(sdnflows.KindFlow, bundleDir)
	if herr != nil {
		log.Warnf("SDN omm role: hash OD flow bundle: %v", herr)
		return
	}
	for _, capName := range odSupplementalCaps {
		if _, err := svc.NodeCtx.CapabilityPolicy.Approve(modulert.CapabilityApproval{
			ModuleHash: hash,
			Capability: capName,
			ApprovedBy: "role:omm",
			Note:       "composed supplemental-OMM OD flow, approved by SDN_ROLE=omm",
		}); err != nil {
			log.Warnf("SDN omm role: approve %s for OD flow: %v", capName, err)
		}
	}
}
