package sdnflows

// celestrak_set.go describes the host-02 (celestrak.eth) CelesTrak REFERENCE SET
// — the fixed set of first-party CelesTrak data-source units a node in the
// "celestrak" ROLE pulls every 3 hours, per the owner rule:
//
//	host-02 pulls CelesTrak references EVERY 3 HOURS =
//	    GP + SupGP + Space Weather (SPW) + GPS almanac + SATCAT catalog file.
//
// The set is data, not behavior: it enumerates each reference unit, its kind
// (a compiled FLOW bundle vs. a standalone WASM MODULE), where its artifact
// resolves under the space-data-network-modules dist tree, and the 3h default
// interval. Two install seams consume it:
//
//   - InstallCelestrakFlows installs the FLOW members through this package's
//     Installer (the same path the SPW flow proves) and reports every member's
//     status. It is what the acceptance test drives.
//   - The sdnruntime plugin's role-gated orchestration installs the MODULE
//     members (SupGP, GPS almanac) through the sdnmodules installer — those ship
//     as executable data-source modules with their own cron timers, not as flow
//     bundles — and sets each to the 3h reference interval.
//
// AVAILABILITY (this checkout):
//
//	GP           FLOW    flows/celestrak-ingest/dist/gp       present
//	SATCAT       FLOW    flows/celestrak-ingest/dist/satcat   present
//	SPW          FLOW    flows/celestrak-ingest/dist/spw      present  (proven)
//	SupGP        MODULE  data-source/celestrak-supgp          present  (no flow bundle exists)
//	GPS almanac  MODULE  data-source/gps-source               present  (no flow bundle exists)
//
// There is NO GP-almanac / SupGP FLOW bundle in the modules repo — the
// celestrak-request/celestrak-parser data-source modules expose only gp/satcat/
// spw methods, so no supgp or gps-almanac flow can be compiled from them. SupGP
// and the GPS almanac instead exist as standalone data-source MODULES
// (com.orbpro.celestrak-supgp, com.orbpro.gps-source), each with its own manifest
// cron timer; the reference set carries them as Kind == KindModule so the module
// installer, not this flow installer, brings them up.
//
// The CelesTrak fetch policy (>= 2.5s serial spacing, 3h no-refetch ledger) is
// enforced independently by the http capability for every unit here — this file
// only schedules them; it does not fetch.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CelestrakReferenceIntervalMs is the owner-mandated 3-hour reference cadence in
// milliseconds. Every reference-set member defaults to this interval; it is
// overridable per-unit via the node's home-dir cron config and the Modules UI.
const CelestrakReferenceIntervalMs int64 = 10800000

// CelestrakReferenceInterval is CelestrakReferenceIntervalMs as a Go duration
// string, the form FlowSpec.Intervals takes.
const CelestrakReferenceInterval = "3h"

// CelestrakRoleName is the node role that turns the reference set on. A node
// whose SDN_ROLE env (or configured role) names this installs the set; every
// other node leaves it dormant (the set is NOT installed on every node).
const CelestrakRoleName = "celestrak"

// ReferenceKind distinguishes a compiled flow bundle from a standalone WASM
// module in the reference set: they install through different pipelines.
type ReferenceKind string

const (
	// KindFlow is a compiled flow bundle (runtime.wasm + flow.plg)
	// installed through sdnflows.Installer; its timer id is TimerID.
	KindFlow ReferenceKind = "flow"
	// KindModule is a standalone data-source module.wasm installed through the
	// sdnmodules installer; its cron method is TimerID (the manifest timer's
	// methodId).
	KindModule ReferenceKind = "module"
)

// ReferenceMember is one unit of the CelesTrak reference set.
type ReferenceMember struct {
	// Name is a short human label (e.g. "GP", "SupGP", "GPS almanac").
	Name string
	// ID is the unit's program/plugin id (the scheduler id it registers under).
	ID string
	// Kind is KindFlow or KindModule.
	Kind ReferenceKind
	// DistSuffix is the artifact location relative to the modules-repo root:
	// a bundle directory for a flow, or a module.wasm file for a module.
	DistSuffix []string
	// TimerID is the flow trigger id (KindFlow) or the module cron method id
	// (KindModule) the 3h interval attaches to.
	TimerID string
	// Caps are the SENSITIVE capabilities the unit declares; the role-gated
	// installer approves these for the unit's content hash so the fail-closed
	// gate admits the first-party reference unit.
	Caps []string
	// Note records availability provenance (e.g. why a unit is a module).
	Note string
}

// CelestrakReferenceSet returns the fixed host-02 reference set, in a stable
// install order (flows first, then modules). See the package/file doc for
// availability.
func CelestrakReferenceSet() []ReferenceMember {
	return []ReferenceMember{
		{
			Name:       "GP",
			ID:         "com.digitalarsenal.flows.celestrak-gp-ingest",
			Kind:       KindFlow,
			DistSuffix: []string{"flows", "celestrak-ingest", "dist", "gp"},
			TimerID:    "timer-gp",
			Caps:       []string{"http", "storage_ingest"},
			Note:       "GP full-catalog CSV ingest flow.",
		},
		{
			Name:       "SATCAT",
			ID:         "com.digitalarsenal.flows.celestrak-satcat-ingest",
			Kind:       KindFlow,
			DistSuffix: []string{"flows", "celestrak-ingest", "dist", "satcat"},
			TimerID:    "timer-satcat",
			Caps:       []string{"http", "storage_ingest"},
			Note:       "SATCAT catalog (txt + csv) ingest flow.",
		},
		{
			Name:       "SPW",
			ID:         "com.digitalarsenal.flows.celestrak-spw-ingest",
			Kind:       KindFlow,
			DistSuffix: []string{"flows", "celestrak-ingest", "dist", "spw"},
			TimerID:    "timer-spw",
			Caps:       []string{"http", "storage_ingest"},
			Note:       "Space Weather (SW-All.csv) ingest flow.",
		},
		{
			Name:       "SupGP",
			ID:         "com.orbpro.celestrak-supgp",
			Kind:       KindModule,
			DistSuffix: []string{"data-source", "celestrak-supgp", "dist", "isomorphic", "module.wasm"},
			TimerID:    "pull",
			Caps:       []string{"http", "storage_ingest", "wallet_sign", "pubsub"},
			Note:       "Multi-provider supplemental OMM: ships as a data-source MODULE, not a flow bundle (no supgp flow can be compiled from celestrak-request/parser). Manifest timer celestrak-supgp-pull default 2h -> pinned to 3h here.",
		},
		{
			Name:       "GPS almanac",
			ID:         "com.orbpro.gps-source",
			Kind:       KindModule,
			DistSuffix: []string{"data-source", "gps-source", "dist", "isomorphic", "module.wasm"},
			TimerID:    "pull",
			Caps:       []string{"http", "storage_ingest", "wallet_sign", "pubsub"},
			Note:       "GPS almanac (NAVCEN SEM/YUMA): ships as a data-source MODULE, not a flow bundle (no gps-almanac flow exists). Manifest timer gps-pull default 12h -> pinned to 3h here.",
		},
	}
}

// CelestrakRoleEnabled reports whether this node is in the CelesTrak role. It is
// true when the SDN_ROLE environment variable, or the given configuredRole
// (a plugin config value; "" if unset), names "celestrak" — comma/space-
// separated lists and surrounding whitespace/case are tolerated so a node can
// hold several roles (e.g. SDN_ROLE="celestrak,gateway"). The set is NOT
// installed on nodes outside the role.
func CelestrakRoleEnabled(configuredRole string) bool {
	return roleListHas(os.Getenv("SDN_ROLE"), CelestrakRoleName) ||
		roleListHas(configuredRole, CelestrakRoleName)
}

func roleListHas(list, want string) bool {
	for _, tok := range strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == ';'
	}) {
		if strings.EqualFold(strings.TrimSpace(tok), want) {
			return true
		}
	}
	return false
}

// ResolveModulesDist locates the space-data-network-modules dist root that holds
// the reference artifacts. Resolution order:
//
//  1. explicit (a caller-supplied path), if it names a modules root;
//  2. env SDN_MODULES_DIST, then SDN_CELESTRAK_MODULES_DIST;
//  3. a source-tree walk from this file up to a sibling
//     space-data-network-modules checkout (dev/test convenience).
//
// A directory is a modules root when it holds a "flows" subdirectory. Returns
// the resolved root and whether one was found.
func ResolveModulesDist(explicit string) (string, bool) {
	for _, cand := range []string{
		strings.TrimSpace(explicit),
		strings.TrimSpace(os.Getenv("SDN_MODULES_DIST")),
		strings.TrimSpace(os.Getenv("SDN_CELESTRAK_MODULES_DIST")),
	} {
		if cand == "" {
			continue
		}
		if root, ok := modulesRootFrom(cand); ok {
			return root, true
		}
	}

	// Source-tree fallback: from this file (.../kubo/sdn/sdnflows) walk up to a
	// sibling space-data-network-modules checkout. Tolerant of a git worktree
	// (an extra directory level), mirroring the flow-bundle test resolver.
	if _, callerFile, _, ok := runtime.Caller(0); ok {
		anchor := filepath.Dir(callerFile)
		for _, ups := range [][]string{
			{"..", "..", "..", ".."},
			{"..", "..", "..", "..", ".."},
			{"..", "..", "..", "..", "..", ".."},
		} {
			cand := filepath.Clean(filepath.Join(append([]string{anchor}, append(ups, "space-data-network-modules")...)...))
			if root, ok := modulesRootFrom(cand); ok {
				return root, true
			}
		}
	}
	return "", false
}

// modulesRootFrom accepts either a modules root (holds flows/) or a path that
// points a level too deep/shallow at the flows dir, and returns the root.
func modulesRootFrom(cand string) (string, bool) {
	if cand == "" {
		return "", false
	}
	if fi, err := os.Stat(filepath.Join(cand, "flows")); err == nil && fi.IsDir() {
		return cand, true
	}
	// Allow passing the flows dir itself, or the celestrak-ingest dist dir.
	if base := filepath.Base(cand); base == "flows" {
		parent := filepath.Dir(cand)
		if fi, err := os.Stat(filepath.Join(parent, "flows")); err == nil && fi.IsDir() {
			return parent, true
		}
	}
	return "", false
}

// ArtifactPath is the absolute artifact location of m under distRoot (a flow
// bundle directory or a module.wasm file).
func (m ReferenceMember) ArtifactPath(distRoot string) string {
	return filepath.Join(append([]string{distRoot}, m.DistSuffix...)...)
}

// Resolve returns m's artifact path and whether it exists in distRoot: a flow
// bundle's runtime.wasm must be present, a module's module.wasm file must exist.
func (m ReferenceMember) Resolve(distRoot string) (string, bool) {
	p := m.ArtifactPath(distRoot)
	switch m.Kind {
	case KindFlow:
		if _, err := os.Stat(filepath.Join(p, "runtime.wasm")); err == nil {
			return p, true
		}
	case KindModule:
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return p, false
}

// FlowSpec builds the install spec for a KindFlow member: the resolved bundle
// dir as Ref, with its timer pinned to the 3h reference interval. extraConfig
// (may be nil) supplies node CONFIG (e.g. a stub URL in tests).
func (m ReferenceMember) FlowSpec(distRoot string, extraConfig map[string]interface{}) FlowSpec {
	spec := FlowSpec{
		Ref:       m.ArtifactPath(distRoot),
		Intervals: map[string]string{m.TimerID: CelestrakReferenceInterval},
	}
	if len(extraConfig) > 0 {
		spec.Config = extraConfig
	}
	return spec
}

// MemberStatus is the per-member outcome of a reference-set install attempt.
type MemberStatus struct {
	Name       string        `json:"name"`
	ID         string        `json:"id"`
	Kind       ReferenceKind `json:"kind"`
	Available  bool          `json:"available"`
	Installed  bool          `json:"installed"`
	Path       string        `json:"path,omitempty"`
	IntervalMs int64         `json:"interval_ms"`
	Note       string        `json:"note,omitempty"`
	Err        string        `json:"error,omitempty"`
}

// InstallCelestrakFlows installs every KindFlow member of the reference set
// through in at the 3h reference interval and returns a status for EVERY member.
// KindModule members (SupGP, GPS almanac) are reported (Available reflects
// whether their module.wasm resolves) but NOT installed here — they ride the
// sdnmodules installer, which this package does not depend on. Sensitive-
// capability approval for each flow's content hash must already be recorded
// (fail closed); this method only loads + registers.
//
// A member whose artifact is missing, or whose install fails, is recorded with
// Installed=false and (on failure) Err set, and does not stop the rest — the set
// is honest about partial availability. The returned error is non-nil only on a
// systemic problem (a nil installer).
func InstallCelestrakFlows(in *Installer, distRoot, source string) ([]MemberStatus, error) {
	if in == nil {
		return nil, fmt.Errorf("sdnflows: InstallCelestrakFlows: nil installer")
	}
	out := make([]MemberStatus, 0, len(CelestrakReferenceSet()))
	for _, m := range CelestrakReferenceSet() {
		st := MemberStatus{
			Name:       m.Name,
			ID:         m.ID,
			Kind:       m.Kind,
			IntervalMs: CelestrakReferenceIntervalMs,
			Note:       m.Note,
		}
		path, ok := m.Resolve(distRoot)
		st.Path, st.Available = path, ok

		if m.Kind != KindFlow {
			// Modules are installed by the runtime's module-installer seam.
			out = append(out, st)
			continue
		}
		if !ok {
			st.Err = "flow bundle not found in modules dist"
			out = append(out, st)
			continue
		}
		if _, err := in.Install(m.FlowSpec(distRoot, nil), source); err != nil {
			st.Err = err.Error()
			out = append(out, st)
			continue
		}
		st.Installed = true
		out = append(out, st)
	}
	return out, nil
}
