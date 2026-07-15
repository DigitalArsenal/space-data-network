package sdnruntime

// celestrak_set.go is the ROLE-gated boot orchestration for the host-02
// (celestrak.eth) CelesTrak reference set. When the node is configured in the
// "celestrak" role (SDN_ROLE=celestrak env, or the plugin's Config.Role key), it
// brings up the owner-mandated 3-hourly CelesTrak reference units:
//
//	GP + SupGP + Space Weather (SPW) + GPS almanac + SATCAT catalog file
//
// The set descriptor, its 3h interval, availability resolution and the flow
// install helper live in sdn/sdnflows (celestrak_set.go). This file wires that
// descriptor to the node's TWO install seams:
//
//   - the FLOW members (GP, SATCAT, SPW) install through the sdnflows flow
//     installer at the 3h reference interval;
//   - the MODULE members (SupGP, GPS almanac) — which ship as standalone
//     data-source modules with their own manifest cron timers, NOT as flow
//     bundles — install through the sdnmodules module installer, then have their
//     cron timer pinned to 3h via the scheduler (persisted to home-dir config,
//     overridable in the Modules UI).
//
// Role implies APPROVAL of the first-party reference set: an operator who sets
// SDN_ROLE=celestrak is declaring this the CelesTrak reference node, so this path
// records the operator approvals each unit's declared sensitive capabilities need
// to clear the fail-closed gate. It is the ONLY auto-approval outside the
// SDN_INSTALL_* developer affordances, and it is scoped to exactly the fixed
// first-party reference bundles. Every other node leaves the set dormant.
//
// The CelesTrak fetch policy (>= 2.5s serial spacing, 3h no-refetch ledger) is
// enforced independently by the http capability; this only schedules.

import (
	"context"
	"os"
	"path/filepath"

	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnflows"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// maybeInstallCelestrakReferenceSet installs the CelesTrak reference set when the
// node is in the celestrak role; otherwise it is a no-op. It is tolerant: a
// missing artifact or a single failed unit is logged and skipped, never fatal —
// the node boots with whatever reference units are available.
func (p *sdnRuntimePlugin) maybeInstallCelestrakReferenceSet(
	ctx context.Context,
	flowInstaller *sdnflows.Installer,
	moduleInstaller *sdnmodules.Installer,
	svc *sdnservices.Services,
) {
	if !sdnflows.CelestrakRoleEnabled(p.role) {
		return
	}
	distRoot, ok := sdnflows.ResolveModulesDist("")
	if !ok {
		log.Warnf("SDN celestrak role: modules dist not found (set SDN_MODULES_DIST); reference set NOT installed")
		return
	}
	log.Infof("SDN celestrak role ACTIVE: installing 3h CelesTrak reference set from %q", distRoot)

	// Approve every available reference unit's declared sensitive capabilities
	// for its content hash so the fail-closed gate admits the first-party set.
	for _, m := range sdnflows.CelestrakReferenceSet() {
		path, avail := m.Resolve(distRoot)
		if !avail {
			continue
		}
		p.approveCelestrakMember(svc, m, path)
	}

	// FLOW members: install + register at 3h through the flow installer.
	statuses, err := sdnflows.InstallCelestrakFlows(flowInstaller, distRoot, "role:celestrak")
	if err != nil {
		log.Warnf("SDN celestrak role: flow install failed: %v", err)
		return
	}
	installedFlows := 0
	for _, s := range statuses {
		switch {
		case s.Kind == sdnflows.KindFlow && s.Installed:
			installedFlows++
			log.Infof("SDN celestrak reference FLOW installed: %s (%s) @ 3h", s.Name, s.ID)
		case s.Kind == sdnflows.KindFlow && s.Available:
			log.Warnf("SDN celestrak reference FLOW %s (%s) available but not installed: %s", s.Name, s.ID, s.Err)
		case s.Kind == sdnflows.KindFlow:
			log.Warnf("SDN celestrak reference FLOW %s (%s) MISSING in dist: %s", s.Name, s.ID, s.Err)
		}
	}

	// MODULE members (SupGP, GPS almanac): install through the module installer,
	// then pin their cron timer to the 3h reference interval.
	installedMods := 0
	for _, m := range sdnflows.CelestrakReferenceSet() {
		if m.Kind != sdnflows.KindModule {
			continue
		}
		path, avail := m.Resolve(distRoot)
		if !avail {
			log.Warnf("SDN celestrak reference MODULE %s (%s) MISSING in dist at %q", m.Name, m.ID, path)
			continue
		}
		if moduleInstaller == nil {
			log.Warnf("SDN celestrak reference MODULE %s (%s) available but module installer unavailable; skipped", m.Name, m.ID)
			continue
		}
		wasm, rerr := os.ReadFile(path)
		if rerr != nil {
			log.Warnf("SDN celestrak reference MODULE %s: read %q: %v", m.Name, path, rerr)
			continue
		}
		mod, ierr := moduleInstaller.InstallBytes(ctx, wasm, "role:celestrak")
		if ierr != nil {
			log.Warnf("SDN celestrak reference MODULE %s (%s) install failed: %v", m.Name, m.ID, ierr)
			continue
		}
		if svc.Scheduler != nil {
			if serr := svc.Scheduler.SetInterval(mod.ID, m.TimerID, sdnflows.CelestrakReferenceIntervalMs); serr != nil {
				log.Warnf("SDN celestrak reference MODULE %s: pin %s to 3h failed: %v", m.Name, m.TimerID, serr)
			}
		}
		installedMods++
		log.Infof("SDN celestrak reference MODULE installed: %s (%s) timer=%s @ 3h", m.Name, mod.ID, m.TimerID)
	}

	log.Infof("SDN celestrak reference set: %d flow(s) + %d module(s) installed at 3h", installedFlows, installedMods)
}

// approveCelestrakMember records operator approvals for a reference unit's
// declared sensitive capabilities, keyed by the unit's content hash, so the
// fail-closed capability gate admits it. Flow bundles are trailer-stripped
// before hashing (as LoadFlowService does); module bytes are hashed as-is (as
// the module installer does).
func (p *sdnRuntimePlugin) approveCelestrakMember(svc *sdnservices.Services, m sdnflows.ReferenceMember, path string) {
	if svc == nil || svc.NodeCtx == nil || svc.NodeCtx.CapabilityPolicy == nil {
		return
	}
	hash, herr := celestrakContentHash(m.Kind, path)
	if herr != nil {
		log.Warnf("SDN celestrak role: hash %s (%s): %v", m.Name, path, herr)
		return
	}
	for _, cap := range m.Caps {
		if _, err := svc.NodeCtx.CapabilityPolicy.Approve(modulert.CapabilityApproval{
			ModuleHash: hash,
			Capability: cap,
			ApprovedBy: "role:celestrak",
			Note:       "first-party CelesTrak reference set, approved by SDN_ROLE=celestrak",
		}); err != nil {
			log.Warnf("SDN celestrak role: approve %s for %s: %v", cap, m.Name, err)
		}
	}
}

// celestrakContentHash computes a reference unit's capability-policy identity the
// same way its loader does: a flow bundle's runtime.wasm trailer-stripped then
// hashed; a module's bytes hashed as-is.
func celestrakContentHash(kind sdnflows.ReferenceKind, path string) (string, error) {
	raw, err := os.ReadFile(celestrakArtifactWasm(kind, path))
	if err != nil {
		return "", err
	}
	if kind == sdnflows.KindFlow {
		portable, _, err := modulert.EnforceModuleSignaturePolicy(nil, raw)
		if err != nil {
			return "", err
		}
		return modulert.ContentHashHex(portable), nil
	}
	return modulert.ContentHashHex(raw), nil
}

// celestrakArtifactWasm resolves the WASM file to hash: a flow bundle dir's
// runtime.wasm, or a module path (already the module.wasm file).
func celestrakArtifactWasm(kind sdnflows.ReferenceKind, path string) string {
	if kind == sdnflows.KindFlow {
		return filepath.Join(path, "runtime.wasm")
	}
	return path
}
