package sds

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
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

// driftGuardedSchemas (schema_embed_bindings_test.go) is GENERATED from the
// embeds by internal/sds/gen: every embedded standard with a linked binding is
// guarded, so the denominator can no longer be a list somebody remembered.

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
			accessors := bindingFieldAccessors(guarded.binding, fields)
			if len(accessors) == 0 {
				t.Fatalf("%s: no field accessors found on %T", guarded.file, guarded.binding)
			}

			// flatc's Go generator names each accessor after the field and
			// also emits its UpperCamel alias (R1_X -> R1X, version ->
			// Version); a field counts as exposed under either name.
			exposed := make([]string, 0, len(fields))
			for _, field := range fields {
				exposed = append(exposed, goAccessorName(field, accessors))
			}
			missingInEmbed := difference(accessors, append(exposed, fields...))
			missingInBindings := difference(fields, fieldsWithAccessor(fields, accessors))

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
// bindingFieldAccessors returns the binding's field accessors: every exported
// method named like a verbatim UPPER_SNAKE field, plus every UpperCamel alias
// of an IDL field (the only exported name a lower_snake field gets). An alias
// is dropped when its verbatim twin is also present.
func bindingFieldAccessors(binding interface{}, fields []string) []string {
	t := reflect.TypeOf(binding)
	methods := map[string]bool{}
	for i := 0; i < t.NumMethod(); i++ {
		methods[t.Method(i).Name] = true
	}
	aliases := map[string]bool{}
	for _, field := range fields {
		if camel := upperCamel(field); camel != field && methods[camel] && !methods[field] {
			aliases[camel] = true
		}
	}
	var names []string
	for name := range methods {
		switch {
		case aliases[name]:
			names = append(names, name)
		case accessorRe.MatchString(name) && !isCamelAliasOfVerbatim(name, methods, fields):
			names = append(names, name)
		}
	}
	return names
}

// isCamelAliasOfVerbatim reports whether name is the UpperCamel alias of an
// IDL field whose verbatim accessor also exists (R1X beside R1_X).
func isCamelAliasOfVerbatim(name string, methods map[string]bool, fields []string) bool {
	for _, field := range fields {
		if field != name && upperCamel(field) == name && methods[field] {
			return true
		}
	}
	return false
}

// goAccessorName is the accessor name the binding uses for field: the field
// itself when exported verbatim, else its UpperCamel alias.
func goAccessorName(field string, accessors []string) string {
	if contains(accessors, field) {
		return field
	}
	return upperCamel(field)
}

func fieldsWithAccessor(fields, accessors []string) []string {
	var out []string
	for _, field := range fields {
		if contains(accessors, field) || contains(accessors, upperCamel(field)) {
			out = append(out, field)
		}
	}
	return out
}

// upperCamel is flatc's Go case conversion of a snake_case field name.
func upperCamel(field string) string {
	var b strings.Builder
	for _, part := range strings.Split(field, "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + strings.ToLower(part[1:]))
	}
	return b.String()
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
