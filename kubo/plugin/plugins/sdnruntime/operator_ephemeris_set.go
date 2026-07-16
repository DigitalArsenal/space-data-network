package sdnruntime

// operator_ephemeris_set.go is the ROLE-gated boot orchestration for the OMM host
// operator ephemeris set. When the node is in the "omm" role (SDN_ROLE=omm env,
// or the plugin's Config.Role key), it registers the first-party operator
// data-source modules (Starlink, ISS, OneWeb, GLONASS, Intelsat, CPF, GPS) as
// self-scheduling cron modules so the node INGESTS per-object operator ephemeris
// into the record store — the input the supplemental-OMM OD run engine's
// store-backed source fits.
//
// It mirrors maybeInstallCelestrakReferenceSet (celestrak_set.go): role implies
// APPROVAL of the first-party set, so it records the operator approvals each
// module's declared sensitive capabilities need to clear the fail-closed gate,
// installs each module.wasm through the sdnmodules installer, and then seeds each
// module's home-dir config with a HIGH per-pull object cap (timer_input.objectCap)
// so a provider ingests its full constellation, not the module's built-in default.
//
// The seeded cap is config: it is written to <repo>/sdn/modules/<id>.json and is
// editable from the Modules settings API. The operator-source fetch cadence is
// enforced independently by the http capability; this file only registers +
// schedules and never weakens a fetch spacing or ledger.

import (
	"context"
	"os"

	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnflows"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// maybeInstallOperatorEphemerisSet registers the operator ephemeris source set
// when the node is in the "omm" role; otherwise it is a no-op. It is tolerant: a
// missing artifact or a single failed unit is logged and skipped, never fatal.
func (p *sdnRuntimePlugin) maybeInstallOperatorEphemerisSet(
	ctx context.Context,
	moduleInstaller *sdnmodules.Installer,
	svc *sdnservices.Services,
) {
	if !sdnflows.OMMRoleEnabled(p.role) {
		return
	}
	if moduleInstaller == nil {
		log.Warnf("SDN omm role: module installer unavailable; operator ephemeris set NOT installed")
		return
	}
	distRoot, ok := sdnflows.ResolveModulesDist("")
	if !ok {
		log.Warnf("SDN omm role: modules dist not found (set SDN_MODULES_DIST); operator ephemeris set NOT installed")
		return
	}
	log.Infof("SDN omm role ACTIVE: registering operator ephemeris source set from %q (per-pull objectCap=%d)",
		distRoot, sdnflows.OperatorEphemerisObjectCap)

	installed := 0
	for _, m := range sdnflows.OperatorEphemerisSet() {
		path, avail := m.Resolve(distRoot)
		if !avail {
			log.Warnf("SDN operator ephemeris MODULE %s (%s) MISSING in dist at %q; skipped", m.Name, m.ID, path)
			continue
		}
		// Role implies approval: record the module's declared sensitive caps
		// against its content hash so the fail-closed gate admits it.
		p.approveOperatorEphemerisMember(svc, m, path)

		wasm, rerr := os.ReadFile(path)
		if rerr != nil {
			log.Warnf("SDN operator ephemeris MODULE %s: read %q: %v", m.Name, path, rerr)
			continue
		}
		mod, ierr := moduleInstaller.InstallBytes(ctx, wasm, "role:omm")
		if ierr != nil {
			log.Warnf("SDN operator ephemeris MODULE %s (%s) install failed: %v", m.Name, m.ID, ierr)
			continue
		}

		// Seed the HIGH per-pull object cap into the module's home-dir config so
		// the next pull ingests the full constellation (config-driven; editable in
		// the Modules UI). The scheduler passes timer_input as the pull payload.
		if svc.Scheduler != nil {
			cfg := sdncron.ModuleConfig{
				"timer_input": map[string]interface{}{
					"objectCap": sdnflows.OperatorEphemerisObjectCap,
				},
			}
			if _, cerr := svc.Scheduler.ApplyConfig(mod.ID, cfg); cerr != nil {
				log.Warnf("SDN operator ephemeris MODULE %s: seed objectCap config failed: %v", m.Name, cerr)
			}
		}
		installed++
		log.Infof("SDN operator ephemeris MODULE registered: %s (%s) timer=%s objectCap=%d",
			m.Name, mod.ID, m.TimerID, sdnflows.OperatorEphemerisObjectCap)
	}
	log.Infof("SDN operator ephemeris set: %d module(s) registered on the omm role", installed)
}

// approveOperatorEphemerisMember records operator approvals for a module's
// declared sensitive capabilities keyed by its content hash, so the fail-closed
// capability gate admits the first-party operator source. Reuses the module
// content-hash helper (module bytes hashed as-is).
func (p *sdnRuntimePlugin) approveOperatorEphemerisMember(svc *sdnservices.Services, m sdnflows.ReferenceMember, path string) {
	if svc == nil || svc.NodeCtx == nil || svc.NodeCtx.CapabilityPolicy == nil {
		return
	}
	hash, herr := celestrakContentHash(sdnflows.KindModule, path)
	if herr != nil {
		log.Warnf("SDN omm role: hash %s (%s): %v", m.Name, path, herr)
		return
	}
	for _, cap := range m.Caps {
		if _, err := svc.NodeCtx.CapabilityPolicy.Approve(modulert.CapabilityApproval{
			ModuleHash: hash,
			Capability: cap,
			ApprovedBy: "role:omm",
			Note:       "first-party operator ephemeris set, approved by SDN_ROLE=omm",
		}); err != nil {
			log.Warnf("SDN omm role: approve %s for %s: %v", cap, m.Name, err)
		}
	}
}
