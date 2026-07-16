package sdnflows

// operator_ephemeris_set.go describes the OMM-host OPERATOR EPHEMERIS SET — the
// set of first-party operator data-source modules an "omm" ROLE node registers as
// self-scheduling cron modules so it INGESTS per-object operator ephemeris into
// the record store. That ingested ephemeris is the INPUT the supplemental-OMM OD
// run engine's store-backed source then fits (one OD fit per object). Without
// these modules registered the store's OEM lanes stay empty and the OD run has
// nothing to read — the "one ISS object" fallback is exactly that empty-store
// symptom.
//
// Like the CelesTrak reference set this is DATA, not behaviour: it enumerates
// each provider's module.wasm location under the space-data-network-modules dist
// tree, the manifest cron method the pull runs on ("pull"), and the sensitive
// capabilities the role approves. The sdnruntime plugin's role-gated
// orchestration (maybeInstallOperatorEphemerisSet) installs them through the
// sdnmodules installer and seeds each a HIGH per-pull object cap in its config so
// a provider ingests its FULL constellation (Starlink alone is 10k+), not the
// module's built-in per-pull default.
//
// Providers whose upstream needs credentials register the same way and simply
// no-op (empty fetch) until the operator supplies creds — registration never
// blocks on credentials. Starlink's manifest is PUBLIC (no creds) and is the
// 10k+ object driver, so it is the primary acceptance target.
//
// The operator-source fetch policy (polite serial spacing, no-refetch ledger) is
// enforced independently by the http capability; this file only registers +
// schedules, and it does NOT weaken any fetch cadence — raising the OBJECT cap
// changes how many objects a pull covers, not how fast it fetches them.

import "os"

// OMMRoleName is the node role that turns the operator ephemeris set on. A node
// whose SDN_ROLE env (or configured role) names this registers the operator
// data-source modules; every other node leaves the set dormant.
const OMMRoleName = "omm"

// OperatorEphemerisObjectCap is the per-pull object cap the role seeds into each
// operator data-source module's config (the config-driven `objectCap` the
// module's parse_config reads). It is set high enough to cover the full Starlink
// constellation (10k+) with headroom, replacing the module's built-in per-pull
// default (e.g. Starlink's 25). Operators can lower it per module from the
// Modules settings API (the timer_input config key).
const OperatorEphemerisObjectCap int64 = 100000

// OMMRoleEnabled reports whether this node is in the OMM role (SDN_ROLE env or
// the given configuredRole names "omm"; comma/space/semicolon-separated lists
// and case/whitespace are tolerated, so a node may hold several roles).
func OMMRoleEnabled(configuredRole string) bool {
	return roleListHas(os.Getenv("SDN_ROLE"), OMMRoleName) ||
		roleListHas(configuredRole, OMMRoleName)
}

// OperatorEphemerisSet returns the fixed OMM-host operator ephemeris set in a
// stable install order. Every member is a standalone data-source MODULE (its own
// manifest "pull" cron timer). Availability is per-checkout: a member whose
// module.wasm is absent under the dist root is reported missing and skipped.
//
// Caps is the union of sensitive capabilities the operator data-source modules
// declare (http fetch, storage_ingest for per-record store, wallet_sign +
// crypto_sign for the PNM signature, pubsub for the PNM pointer). Approving a
// capability a given module does not declare is harmless — the fail-closed gate
// only requires each DECLARED sensitive capability to be approved.
func OperatorEphemerisSet() []ReferenceMember {
	caps := []string{"http", "storage_ingest", "wallet_sign", "crypto_sign", "pubsub"}
	mod := func(name, id, dir, note string) ReferenceMember {
		return ReferenceMember{
			Name:       name,
			ID:         id,
			Kind:       KindModule,
			DistSuffix: []string{"data-source", dir, "dist", "isomorphic", "module.wasm"},
			TimerID:    "pull",
			Caps:       caps,
			Note:       note,
		}
	}
	return []ReferenceMember{
		mod("Starlink", "com.orbpro.spacex-starlink-source", "spacex-starlink-source",
			"SpaceX/Starlink ephemeris (PUBLIC manifest, no creds; 10k+ objects). Stores per-object OEM under source lane \"spacex-starlink\"."),
		mod("ISS", "com.orbpro.iss-source", "iss-source",
			"NASA public ISS OEM. Stores OEM under source lane \"iss\"."),
		mod("OneWeb", "com.orbpro.oneweb-source", "oneweb-source",
			"OneWeb ephemeris. Stores OEM under source lane \"oneweb\"."),
		mod("GLONASS", "com.orbpro.glonass-source", "glonass-source",
			"GLONASS ephemeris. Stores OEM under source lane \"glonass\"."),
		mod("Intelsat", "com.orbpro.intelsat-source", "intelsat-source",
			"Intelsat ephemeris. Stores OEM under source lane \"intelsat\"."),
		mod("CPF", "com.orbpro.cpf-source", "cpf-source",
			"ILRS CPF position-only ephemeris. Stores OEM under source lane \"cpf\"."),
		mod("GPS", "com.orbpro.gps-source", "gps-source",
			"GPS almanac (NAVCEN SEM/YUMA). Stores under source lane \"gps\"."),
	}
}
