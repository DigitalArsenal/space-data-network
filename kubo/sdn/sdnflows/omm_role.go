package sdnflows

// omm_role.go carries the "omm" node-role gate. It used to live alongside the
// per-provider operator-ephemeris MODULE set (operator_ephemeris_set.go), which
// was deleted by the Go-orchestration purge (SDN_OD_FLOW_LOOP.md STOP block):
// that set installed seven data-source modules as self-scheduling cron jobs
// that pulled + persisted per-object $OEM through the Go storage sink — exactly
// the Go-host orchestration the OD run must never do.
//
// The role gate itself is still live: plugin/plugins/sdnruntime/operator_omm_flow.go
// (the ONE composed wasi-threads OD flow — the WASM-only replacement) uses it to
// decide whether this node bakes + mounts that flow. It is kept here, on its
// own, because it is genuinely reused by the live path, not because any part of
// the old module set survives.

import "os"

// OMMRoleName is the node role that turns on the supplemental-OMM OD flow. A
// node whose SDN_ROLE env (or configured role) names this bakes + mounts the
// composed wasi-threads OD flow (see operator_omm_flow.go); every other node
// leaves it dormant.
const OMMRoleName = "omm"

// OMMRoleEnabled reports whether this node is in the OMM role (SDN_ROLE env or
// the given configuredRole names "omm"; comma/space/semicolon-separated lists
// and case/whitespace are tolerated, so a node may hold several roles).
func OMMRoleEnabled(configuredRole string) bool {
	return roleListHas(os.Getenv("SDN_ROLE"), OMMRoleName) ||
		roleListHas(configuredRole, OMMRoleName)
}
