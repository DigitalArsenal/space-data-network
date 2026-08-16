package sds

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/APP"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CCT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EGP"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CNP"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/LKS"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	sdsplg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	sdspmm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/RFB"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/SPW"
	sdsstf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STF"
)

// WHY THIS GUARD EXISTS (2026-08-04, sdn-server-rfb-schema-embed-stale).
//
// internal/sds/schemas/*.fbs are COPIES of spacedatastandards.org/schema/<X>/
// main.fbs (scripts/subtree-update.sh), while the decode bindings come from the
// pinned Go library in third_party/. Nothing tied the two together, so they
// drifted silently: RFB.fbs sat at v0.0.3 — stopping at EIRP — while the
// bindings were v0.0.4 and already carried NORAD_CAT_ID, ID_TRANSMITTER,
// LINK_DIRECTION, BAUD, SERVICE, XMT_STATUS, INVERT, IARU_COORDINATION and
// CITATION. The node happily decoded fields its own strict validator's schema
// said did not exist.
//
// The guard compares the EMBEDDED IDL against the LINKED bindings field by
// field. A binding accessor with no field in the embed means the embed lags
// (the RFB defect). A field with no accessor means the embed is ahead of the
// pin. Either way the two authorities disagree, and this test says so with the
// exact field names — it can no longer happen quietly.

// driftGuardedSchemas are the standards this node DECODES or serves on the
// anonymous data plane. Add a schema here whenever the host starts reading its
// fields; the cost is one line and it can never then drift unnoticed.
var driftGuardedSchemas = []struct {
	file    string
	binding interface{}
}{
	{"OMM.fbs", &OMM.OMM{}},
	{"CAT.fbs", &CAT.CAT{}},
	{"MPE.fbs", &MPE.MPE{}},
	{"PNM.fbs", &PNM.PNM{}},
	{"RFB.fbs", &RFB.RFB{}},
	{"LKS.fbs", &LKS.LKS{}},
	{"SPW.fbs", &SPW.SPW{}},
	{"APP.fbs", &APP.APP{}},
	// RF data suite (SDS v1.177.0). Guarded from the day they are embedded:
	// both arrive with the pin bump, before any record exists, so the embed
	// and the bindings are provably the same authority from record zero.
	{"IQC.fbs", &IQC.IQC{}},
	{"CNP.fbs", &CNP.CNP{}},
	// The category lane (SDS v1.186.0, $CCT). These three carry
	// PRIMARY_CATEGORY/CATEGORIES and this node WRITES all three, so they move
	// out of the unguarded waiver on the same pin bump that gives them a
	// category to write. $CCT itself is guarded because it is the vocabulary
	// those fields join against: an embed that lagged it would let the strict
	// validator disagree with the enum the encoders resolve through.
	{"PLG.fbs", &sdsplg.PLG{}},
	{"PMM.fbs", &sdspmm.PMM{}},
	{"STF.fbs", &sdsstf.STF{}},
	{"CCT.fbs", &CCT.CCT{}},
	// Entity groups (SDS v1.193.0, $EGP). Guarded from the day it is embedded:
	// the node serves $EGP on the anonymous data plane and decodes its fields,
	// so the embed and the binding must be provably the same authority from
	// record zero.
	{"EGP.fbs", &EGP.EGP{}},
}

func TestEmbeddedSchemasMatchLinkedBindings(t *testing.T) {
	for _, guarded := range driftGuardedSchemas {
		guarded := guarded
		t.Run(guarded.file, func(t *testing.T) {
			source, err := schemasFS.ReadFile(embeddedSchemaPath(guarded.file))
			if err != nil {
				t.Fatalf("embedded schema %s is missing: %v", guarded.file, err)
			}
			idl := string(source)

			root := rootTypeName(idl)
			if root == "" {
				t.Fatalf("%s declares no root_type", guarded.file)
			}
			fields := tableFieldNames(idl, root)
			if len(fields) == 0 {
				t.Fatalf("%s: no fields parsed from table %s", guarded.file, root)
			}
			accessors := bindingFieldAccessors(guarded.binding)
			if len(accessors) == 0 {
				t.Fatalf("%s: no field accessors found on %T", guarded.file, guarded.binding)
			}

			missingInEmbed := difference(accessors, fields)
			missingInBindings := difference(fields, accessors)

			if len(missingInEmbed) > 0 {
				t.Errorf("%s EMBED LAGS THE BINDINGS: the linked %T exposes %v, which the embedded IDL does not declare. "+
					"Refresh the embed from the pinned standard: cp <spacedatastandards.org>/schema/%s/main.fbs internal/sds/schemas/%s",
					guarded.file, guarded.binding, missingInEmbed,
					strings.TrimSuffix(guarded.file, ".fbs"), guarded.file)
			}
			if len(missingInBindings) > 0 {
				t.Errorf("%s EMBED IS AHEAD OF THE BINDINGS: the IDL declares %v, which the linked %T does not expose. "+
					"Bump the spacedatastandards.org/lib/go pin (go.mod + third_party/spacedatastandards-go) rather than reverting the embed",
					guarded.file, missingInBindings, guarded.binding)
			}
		})
	}
}

// TestRFBEmbedCarriesTheEmitterFields is the specific regression: the fields
// the RF program depends on must exist in the embedded IDL, by name.
func TestRFBEmbedCarriesTheEmitterFields(t *testing.T) {
	source, err := schemasFS.ReadFile(embeddedSchemaPath("RFB.fbs"))
	if err != nil {
		t.Fatalf("RFB.fbs embed is missing: %v", err)
	}
	idl := string(source)
	fields := tableFieldNames(idl, "RFB")

	for _, required := range []string{
		"NORAD_CAT_ID",      // the index/query join key (sdn-data-index-rfb-norad)
		"ID_TRANSMITTER",    // pairs UPLINK/DOWNLINK records of one device
		"LINK_DIRECTION",    // one record = one direction
		"BAUD",              //
		"SERVICE",           //
		"XMT_STATUS",        //
		"INVERT",            //
		"IARU_COORDINATION", //
		"CITATION",          // CC-BY-SA-4.0 attribution carried per record
	} {
		if !contains(fields, required) {
			t.Errorf("embedded RFB.fbs does not declare %s (embed is stale — refresh from the pinned standard)", required)
		}
	}
}

var (
	rootTypeRe  = regexp.MustCompile(`(?m)^\s*root_type\s+([A-Za-z_][A-Za-z0-9_]*)\s*;`)
	fieldRe     = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:`)
	accessorRe  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	tableStartF = "table %s {"
)

func rootTypeName(idl string) string {
	match := rootTypeRe.FindStringSubmatch(idl)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

// tableFieldNames returns the field names declared by `table <name> { ... }`,
// ignoring comments and nested braces.
func tableFieldNames(idl, name string) []string {
	start := strings.Index(idl, fmt.Sprintf(tableStartF, name))
	if start < 0 {
		return nil
	}
	body := idl[start:]
	depth := 0
	end := len(body)
	for i, c := range body {
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	var fields []string
	for _, line := range strings.Split(body[:end], "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if match := fieldRe.FindStringSubmatch(line); len(match) == 2 {
			fields = append(fields, match[1])
		}
	}
	return fields
}

// bindingFieldAccessors returns the generated field accessors of a FlatBuffers
// Go binding: the methods whose names are the IDL field names verbatim
// (UPPER_SNAKE). The generator's camelCase aliases, Mutate* setters, vector
// Length/Bytes helpers and Init/Table plumbing all carry lower-case letters and
// are excluded by the pattern.
func bindingFieldAccessors(binding interface{}) []string {
	t := reflect.TypeOf(binding)
	var names []string
	for i := 0; i < t.NumMethod(); i++ {
		name := t.Method(i).Name
		if accessorRe.MatchString(name) {
			names = append(names, name)
		}
	}
	return names
}

func difference(from, minus []string) []string {
	present := make(map[string]struct{}, len(minus))
	for _, value := range minus {
		present[value] = struct{}{}
	}
	var out []string
	for _, value := range from {
		if _, ok := present[value]; !ok {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
