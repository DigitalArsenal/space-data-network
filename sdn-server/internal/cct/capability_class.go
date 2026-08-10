// Package cct holds the ONE translation from the retired `pluginCategory`
// vocabulary to the ratified `$CCT` `capabilityClass` taxonomy
// (spacedatastandards.org v1.186.0).
//
// It exists as its own leaf package because three encoders need the same
// answer — the $PLG listing frame (internal/node), the $PMM module manifest
// (internal/pmm) and the $STF storefront listing (internal/storefront) — and a
// twenty-two entry table copied into three files is a table that will
// eventually disagree with itself. A module that shelves as PROPAGATION in the
// library and SPACE_ENVIRONMENT in the store is worse than one that shelves
// nowhere.
//
// The package deliberately carries NO display strings. $CCT states each
// member's canonical label and the node does not render labels; a Go-side copy
// of the display names would be a second authority for text the node never
// shows, and would rot the moment the taxonomy adds a member.
//
// Values are exchanged as capabilityClass member NAMES, not typed constants.
// flatc generates `capabilityClass` as an UNEXPORTED Go type once per bundle
// package, so PLG.capabilityClass, PMM.capabilityClass and STF.capabilityClass
// are three distinct types no shared package can name. Each encoder therefore
// resolves the name through its own generated EnumValuescapabilityClass map —
// the same indirection internal/pmm/encode.go already documents for every
// other SDS enum it writes.
package cct

import "strings"

// Unspecified is the capabilityClass member every unmapped, unknown or absent
// classification resolves to.
//
// $CCT holds UNSPECIFIED at ordinal 0 on purpose, so unlike `pluginCategory` —
// whose ordinal 0 is `Sensor`, a real family — a zero-filled or
// default-constructed record here cannot decode as a category the publisher
// never stated. Callers may rely on the flatbuffers default being correct;
// they should still write the field explicitly, because a reader cannot tell
// "defaulted" from "stated" and the encoders' intent should be legible.
const Unspecified = "UNSPECIFIED"

// pluginTypeToCapabilityClass is the NORMATIVE forward mapping published on
// PLG.PRIMARY_CATEGORY at v1.186.0. It is transcribed from the IDL, not
// invented here, and TestMappingMatchesRatifiedVocabulary holds it to the
// generated enums.
//
// Two members deserve a note because they look like judgement calls and are
// not:
//
//   - Basilisk is a vendor-derived name that owner law 2026-08-06 forbids in a
//     standard's vocabulary, which is why it is absent from the new taxonomy.
//     It maps on what such a module DOES — astrodynamics propagation — not on
//     what it was called. $PLG additionally requires consumers to RENDER an
//     existing record carrying it as Propagation and never to surface the
//     member identifier in a user-facing label.
//   - Storefront and Licensing both land on COMMERCE_AND_LICENSING, and
//     Infrastructure and Publisher both land on NODE_INFRASTRUCTURE. The
//     mapping is many-to-one by design: `pluginCategory` mixed node-internal
//     plumbing into a capability vocabulary, and the taxonomy collapses that
//     plumbing back into the two classes it actually belongs to.
//
// The mapping is ONE-WAY. There is deliberately no inverse: PRIMARY_CATEGORY is
// never back-derived into PLUGIN_TYPE, because several old members share one
// new class and any inverse would have to guess which.
var pluginTypeToCapabilityClass = map[string]string{
	"Sensor":         "SENSORS_AND_COVERAGE",
	"Propagator":     "PROPAGATION",
	"Renderer":       "VISUALIZATION_AND_RENDERING",
	"Analysis":       "MISSION_DESIGN_AND_ANALYSIS",
	"DataSource":     "DATA_SOURCES_AND_INGEST",
	"EW":             "ELECTRONIC_WARFARE",
	"Comms":          "RF_AND_COMMUNICATIONS",
	"Physics":        "SPACE_ENVIRONMENT",
	"Shader":         "VISUALIZATION_AND_RENDERING",
	"Parser":         "DATA_SOURCES_AND_INGEST",
	"Validator":      "DATA_VALIDATION_AND_QUALITY",
	"Interpolator":   "PROPAGATION",
	"Exporter":       "DATA_SOURCES_AND_INGEST",
	"Foundation":     "FOUNDATION_AND_MATH",
	"Infrastructure": "NODE_INFRASTRUCTURE",
	"Licensing":      "COMMERCE_AND_LICENSING",
	"Storefront":     "COMMERCE_AND_LICENSING",
	"Publisher":      "NODE_INFRASTRUCTURE",
	"Basilisk":       "PROPAGATION",
	"Maneuver":       "MANEUVER_PLANNING",
	"Flow":           "FLOW_AND_COMPOSITION",
	"Unspecified":    Unspecified,
}

// FromPluginType translates a declared `pluginCategory` symbol into the
// capabilityClass member name a consumer shelves by.
//
// An absent, blank or unrecognized symbol returns Unspecified. That is the
// whole error model: this function never guesses, and never falls back to a
// plausible-looking class, because a wrong shelf is worse than an honest
// absence once a store groups by the field. $CCT defines UNSPECIFIED to render
// as ungrouped precisely so the honest answer is representable.
func FromPluginType(symbol string) string {
	if class, ok := pluginTypeToCapabilityClass[strings.TrimSpace(symbol)]; ok {
		return class
	}
	return Unspecified
}
