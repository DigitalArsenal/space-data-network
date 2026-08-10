package cct

import (
	"testing"

	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
)

// TestMappingMatchesRatifiedVocabulary is the guard that makes the transcribed
// table trustworthy.
//
// The mapping in this package is a hand-copy of the normative table published
// on PLG.PRIMARY_CATEGORY. A hand-copy can be wrong in exactly two ways, and
// both are checked here against the GENERATED enums rather than against another
// hand-list: a source member nobody mapped (which would silently shelve that
// family as UNSPECIFIED forever), and a target that is not a real
// capabilityClass member (which would resolve to the enum's zero value at
// encode time — and since $CCT's zero IS UNSPECIFIED, that failure would be
// invisible on the wire rather than loud).
func TestMappingMatchesRatifiedVocabulary(t *testing.T) {
	t.Parallel()

	for symbol := range plg.EnumValuespluginCategory {
		if _, mapped := pluginTypeToCapabilityClass[symbol]; !mapped {
			t.Errorf("pluginCategory member %q has no capabilityClass mapping — it would shelve as UNSPECIFIED forever", symbol)
		}
	}

	for symbol, class := range pluginTypeToCapabilityClass {
		if _, real := plg.EnumValuespluginCategory[symbol]; !real {
			t.Errorf("mapping has source %q, which is not a pluginCategory member — stale entry", symbol)
		}
		if _, real := plg.EnumValuescapabilityClass[class]; !real {
			t.Errorf("%s maps to %q, which is not a capabilityClass member", symbol, class)
		}
	}
}

// TestUnspecifiedIsOrdinalZero pins the structural property the whole migration
// rests on. `pluginCategory` put a real family (Sensor) at 0, which is why the
// $PLG lane has to resolve unknowns to Unspecified EXPLICITLY — a defaulted
// field there is a confident wrong answer. $CCT put UNSPECIFIED at 0 so that
// class of defect cannot recur. If this ever fails, every unset PRIMARY_CATEGORY
// in the fleet has silently become a real category.
func TestUnspecifiedIsOrdinalZero(t *testing.T) {
	t.Parallel()

	if got := plg.EnumValuescapabilityClass[Unspecified]; got != 0 {
		t.Errorf("capabilityClass.%s = %d, want ordinal 0", Unspecified, got)
	}
	if got := plg.EnumValuespluginCategory["Sensor"]; got != 0 {
		t.Errorf("pluginCategory.Sensor = %d, want ordinal 0 — the premise this migration corrects", got)
	}
}

func TestFromPluginType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		declared string
		want     string
	}{
		{"a clean family survives", "Propagator", "PROPAGATION"},
		{"the vendor member maps on what it does", "Basilisk", "PROPAGATION"},
		{"plumbing collapses into node infrastructure", "Publisher", "NODE_INFRASTRUCTURE"},
		{"storefront and licensing share a class", "Storefront", "COMMERCE_AND_LICENSING"},
		{"foundation is a class of its own", "Foundation", "FOUNDATION_AND_MATH"},
		{"blank is UNSPECIFIED", "", Unspecified},
		{"unknown is UNSPECIFIED, never a guess", "NotACategory", Unspecified},
		{"the old Unspecified carries across", "Unspecified", Unspecified},
		{"surrounding space is trimmed", "  Comms  ", "RF_AND_COMMUNICATIONS"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := FromPluginType(tc.declared); got != tc.want {
				t.Errorf("FromPluginType(%q) = %q, want %q", tc.declared, got, tc.want)
			}
		})
	}
}

// TestMappingIsOneWay states the contract in code: several old members share a
// new class, so no inverse exists that could round-trip. Anyone tempted to add
// a ToPluginType has to break this test first, and $PLG forbids the operation
// outright.
func TestMappingIsOneWay(t *testing.T) {
	t.Parallel()

	collisions := map[string][]string{}
	for symbol, class := range pluginTypeToCapabilityClass {
		collisions[class] = append(collisions[class], symbol)
	}
	shared := 0
	for _, symbols := range collisions {
		if len(symbols) > 1 {
			shared++
		}
	}
	if shared == 0 {
		t.Error("no capabilityClass has multiple pluginCategory sources; if the mapping really became 1:1, the one-way claim in the package doc needs rewriting")
	}
}
