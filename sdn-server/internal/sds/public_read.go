package sds

import "strings"

// THE ANONYMOUS DATA PLANE (2026-08-04, sdn-rfb-public-read-allowlist).
//
// The node's public record surface was a hand-written list of literal paths.
// It contained "/api/v1/data/omm/bulk" and nothing else per-schema, so the
// moment a second standard went live ($RFB: 5,289 SatNOGS emitters) a browser
// asking for it got 401 — and got it BEFORE the CORS preflight answered, so
// the page saw an opaque network error rather than a refusal. Adding
// "/api/v1/data/rfb/bulk" to the list would have bought exactly one schema and
// left the next one broken the same way.
//
// The principle instead: the anonymous data plane is a property of the SCHEMA,
// not of a string. A standard is either public data this node republishes to
// anyone, or it is not; the read surface follows that classification for every
// route the data mount serves.
//
// FAIL-CLOSED BY CONSTRUCTION. This is an ALLOW list, not a deny list: a
// schema is anonymous only if it is named here. A newly embedded standard —
// including one Themis auto-ratifies — is NOT anonymously readable until
// somebody adds it deliberately. That is the correct default for a store that
// also holds key material, access grants and node-internal ledgers.

// publicReadSchemas is the set of standards served on the anonymous data
// plane, keyed by schema file name.
//
// Membership means: "records of this standard, as this node holds them, are
// public data". Each entry names why.
var publicReadSchemas = map[string]string{
	// The catalogue itself. Already anonymous through the flow-served
	// omm/bulk route since loop C.4, and the reason the surface exists.
	"OMM.fbs": "orbital element sets — the node's primary published catalogue",
	"CAT.fbs": "catalogue entries (SATCAT lane) — public object metadata",
	"MPE.fbs": "mean parameter ephemerides derived from the public catalogue",
	"SPW.fbs": "space weather indices — a public feed by definition",

	// RF spectrum (rf-data-suite-program). SatNOGS DB is CC-BY-SA-4.0: the
	// licence obliges attribution and share-alike, both of which travel in
	// the records (CITATION) and in the batch provenance. Neither is a
	// reason to withhold the data — the opposite.
	"RFB.fbs": "RF band specifications — published emitter catalogue (CC-BY-SA-4.0 carried in-record)",
	"LKS.fbs": "link status — the RF companion standard, same public source class",

	// Announcement / provenance records. These are ALREADY public: PNMs are
	// broadcast on open gossipsub topics and DPM manifests are pinned to
	// IPFS, so refusing them over HTTP protects nothing and only makes the
	// node's own publications unverifiable to a browser.
	"PNM.fbs": "publication notifications — already broadcast on public pubsub topics",
	"DPM.fbs": "dataset publication manifests — already pinned publicly on IPFS",

	// Identity + apps. Both surfaces are already anonymous by explicit route
	// (/api/node/epm, /api/v1/apps/records/): the record form is the same
	// bytes and must not be a privilege.
	"EPM.fbs": "entity profile messages — the identity this node publishes about itself and its peers",
	"APP.fbs": "application package manifests — the apps this node serves are already public",

	// Entity groups. A group asserts MEMBERSHIP ONLY — it never republishes a
	// member's data, and every member it names is a reference into a standard
	// that is already on this list (CAT/OMM today). It is broadcast on an open
	// gossipsub topic (/spacedatanetwork/channels/EGP/<providerID>), so
	// refusing it over HTTP protects nothing and only makes this node's own
	// published groups unverifiable to a browser — the PNM/DPM rationale
	// exactly. Ownership is the signing key on the envelope, never the read
	// gate: anonymous READ does not imply anonymous PUBLISH.
	"EGP.fbs": "entity groups — membership assertions over already-public catalogue records, already broadcast on public channel topics",
}

// IsPublicReadSchema reports whether records of this standard are served on the
// anonymous data plane.
//
// It accepts either the standard code ("RFB", "rfb") or the schema file name
// ("RFB.fbs"). An unknown or unlisted standard is NOT public.
func IsPublicReadSchema(schema string) bool {
	name := NormalizeSchemaFileName(schema)
	if name == "" {
		return false
	}
	_, ok := publicReadSchemas[name]
	return ok
}

// PublicReadSchemas returns the anonymous data plane's schema file names.
// The result is a copy; callers may sort or filter it freely.
func PublicReadSchemas() []string {
	out := make([]string, 0, len(publicReadSchemas))
	for name := range publicReadSchemas {
		out = append(out, name)
	}
	return out
}

// PublicReadSchemaReason returns why a standard is on the anonymous data
// plane, or "" when it is not.
func PublicReadSchemaReason(schema string) string {
	return publicReadSchemas[NormalizeSchemaFileName(schema)]
}

// NormalizeSchemaFileName turns "rfb", "RFB" or "rfb.fbs" into "RFB.fbs".
// It returns "" for input that cannot be a schema name (empty, or carrying a
// path separator or traversal sequence — the same refusals ValidateSchemaName
// makes, applied before any lookup).
func NormalizeSchemaFileName(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return ""
	}
	if strings.ContainsAny(schema, "/\\") || strings.Contains(schema, "..") {
		return ""
	}
	upper := strings.ToUpper(schema)
	if strings.HasSuffix(upper, ".FBS") {
		upper = strings.TrimSuffix(upper, ".FBS")
	}
	if upper == "" {
		return ""
	}
	return upper + ".fbs"
}
