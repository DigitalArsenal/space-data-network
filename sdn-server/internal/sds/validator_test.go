// Package sds provides Space Data Standards validation and schema handling.
package sds

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func TestNewValidator(t *testing.T) {
	// Create validator without WASM
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	if validator == nil {
		t.Fatal("Expected non-nil validator")
	}
}

func TestValidatorSchemas(t *testing.T) {
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	schemas := validator.Schemas()

	// Should have schemas loaded
	if len(schemas) == 0 {
		t.Error("Expected schemas to be loaded")
	}

	// Check for some expected schemas
	expectedSchemas := []string{"OMM.fbs", "CDM.fbs", "EPM.fbs", "CAT.fbs"}
	for _, expected := range expectedSchemas {
		found := false
		for _, s := range schemas {
			if s == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected schema %s not found", expected)
		}
	}
}

func TestValidatorHasSchema(t *testing.T) {
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	// Test schema that should exist
	if !validator.HasSchema("OMM.fbs") {
		t.Error("Expected OMM.fbs schema to exist")
	}

	// Test schema that shouldn't exist
	if validator.HasSchema("NONEXISTENT.fbs") {
		t.Error("Expected NONEXISTENT.fbs schema to not exist")
	}
}

func TestValidatorAddSchema(t *testing.T) {
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	ctx := context.Background()

	// Add a custom schema
	err = validator.AddSchema(ctx, "CUSTOM.fbs", []byte("// Custom schema content"))
	if err != nil {
		t.Fatalf("Failed to add schema: %v", err)
	}

	// Verify it was added
	if !validator.HasSchema("CUSTOM.fbs") {
		t.Error("Expected CUSTOM.fbs schema to exist after adding")
	}
}

func TestValidatorValidateBasic(t *testing.T) {
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	ctx := context.Background()

	// Test validation with unknown schema
	err = validator.Validate(ctx, "UNKNOWN.fbs", []byte(`{"test": true}`))
	if err == nil {
		t.Error("Expected error for unknown schema")
	}

	// A known schema is NOT a licence to store arbitrary bytes. Without the flatc
	// WASM module (the state of every packaged deployment) validation used to
	// degrade to a non-empty check, so this JSON blob was accepted and stored as
	// if it were an OMM record. It must be rejected on structure + file identifier.
	err = validator.Validate(ctx, "OMM.fbs", []byte(`{"satellite": "ISS"}`))
	if err == nil {
		t.Error("Expected error for a JSON payload published as an OMM FlatBuffer")
	}

	// A real OMM record still validates without WASM.
	err = validator.Validate(ctx, "OMM.fbs", NewOMMBuilder().WithNoradCatID(25544).Build())
	if err != nil {
		t.Errorf("Unexpected validation error for a real OMM record: %v", err)
	}

	// Test validation with empty data
	err = validator.Validate(ctx, "OMM.fbs", []byte{})
	if err == nil {
		t.Error("Expected error for empty data")
	}
}

func TestSchemaNameFromExtension(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"omm", "OMM.fbs"},
		{".omm", "OMM.fbs"},
		{"OMM", "OMM.fbs"},
		{"OMM.fbs", "OMM.FBS.fbs"}, // Already has .fbs
		{"cdm", "CDM.fbs"},
	}

	for _, test := range tests {
		result := SchemaNameFromExtension(test.input)
		if result != test.expected {
			t.Errorf("SchemaNameFromExtension(%q) = %q, want %q", test.input, result, test.expected)
		}
	}
}

func TestSchemaNameToTable(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"OMM.fbs", "OMM"},
		{"CDM.fbs", "CDM"},
		{"EPM.fbs", "EPM"},
		{"CUSTOM", "CUSTOM"},
		{"My_Schema_v2.fbs", "My_Schema_v2"},
	}

	for _, test := range tests {
		result, err := SchemaNameToTable(test.input)
		if err != nil {
			t.Errorf("SchemaNameToTable(%q) returned error: %v", test.input, err)
			continue
		}
		if result != test.expected {
			t.Errorf("SchemaNameToTable(%q) = %q, want %q", test.input, result, test.expected)
		}
	}
}

func TestValidateSchemaName(t *testing.T) {
	tests := []struct {
		name        string
		schemaName  string
		expectError error
	}{
		// Valid schema names
		{"valid simple", "OMM.fbs", nil},
		{"valid uppercase", "CDM", nil},
		{"valid lowercase", "omm", nil},
		{"valid with underscore", "my_schema", nil},
		{"valid with dot", "schema.fbs", nil},
		{"valid alphanumeric", "schema123", nil},
		{"valid mixed", "My_Schema_v2.fbs", nil},

		// Empty name
		{"empty string", "", ErrSchemaNameEmpty},

		// Too long
		{"too long", "a" + string(make([]byte, MaxSchemaNameLength)), ErrSchemaNameTooLong},
		{"exactly max length", string(make([]byte, MaxSchemaNameLength)), nil}, // 64 'a' characters

		// Path traversal
		{"path traversal double dot", "../etc/passwd", ErrSchemaNamePathTraversal},
		{"path traversal forward slash", "foo/bar", ErrSchemaNamePathTraversal},
		{"path traversal backslash", "foo\\bar", ErrSchemaNamePathTraversal},
		{"path traversal complex", "..\\..\\etc\\passwd", ErrSchemaNamePathTraversal},
		{"double dot in middle", "foo..bar", ErrSchemaNamePathTraversal},

		// Invalid characters (potential SQL injection or other issues)
		{"sql injection semicolon", "schema;DROP TABLE", ErrSchemaNameInvalidChars},
		{"sql injection quote", "schema'--", ErrSchemaNameInvalidChars},
		{"space in name", "my schema", ErrSchemaNameInvalidChars},
		{"hyphen in name", "my-schema", ErrSchemaNameInvalidChars},
		{"special char at", "user@domain", ErrSchemaNameInvalidChars},
		{"special char hash", "schema#1", ErrSchemaNameInvalidChars},
		{"special char dollar", "$schema", ErrSchemaNameInvalidChars},
		{"special char percent", "schema%20", ErrSchemaNameInvalidChars},
		{"null byte", "schema\x00name", ErrSchemaNameInvalidChars},
		{"newline", "schema\nname", ErrSchemaNameInvalidChars},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For "exactly max length" test, create a string of exactly MaxSchemaNameLength 'a' characters
			schemaName := tt.schemaName
			if tt.name == "exactly max length" {
				schemaName = string(make([]byte, MaxSchemaNameLength))
				for i := range schemaName {
					schemaName = schemaName[:i] + "a" + schemaName[i+1:]
				}
				// Actually create it properly
				buf := make([]byte, MaxSchemaNameLength)
				for i := range buf {
					buf[i] = 'a'
				}
				schemaName = string(buf)
			}

			err := ValidateSchemaName(schemaName)
			if tt.expectError != nil {
				if err == nil {
					t.Errorf("Expected error %v, got nil", tt.expectError)
				} else if err != tt.expectError {
					t.Errorf("Expected error %v, got %v", tt.expectError, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
			}
		})
	}
}

func TestValidateSchemaNameMaxLength(t *testing.T) {
	// Test boundary conditions for max length
	exactMax := make([]byte, MaxSchemaNameLength)
	for i := range exactMax {
		exactMax[i] = 'a'
	}

	overMax := make([]byte, MaxSchemaNameLength+1)
	for i := range overMax {
		overMax[i] = 'a'
	}

	if err := ValidateSchemaName(string(exactMax)); err != nil {
		t.Errorf("Expected no error for exactly max length, got %v", err)
	}

	if err := ValidateSchemaName(string(overMax)); err != ErrSchemaNameTooLong {
		t.Errorf("Expected ErrSchemaNameTooLong for over max length, got %v", err)
	}
}

// internalSchemas are the SDN-internal schemas that are not part of the
// upstream spacedatastandards.org standards set.
var internalSchemas = map[string]bool{
	"PGR.fbs":  true, // Peer Graph Record
	"PLHD.fbs": true, // Publication Log Head
	"PLOG.fbs": true, // Publication Log Entry
	"RHD.fbs":  true, // Routing Header
}

const (
	// Every standard published by spacedatastandards.org at the pinned
	// version. The embed is a FULL mirror, not a subset: REC.fbs (the
	// aggregate Records schema) includes every other standard, so a partial
	// embed leaves dangling includes — which is exactly how the set went
	// stale before v1.177.0.
	//
	// 225 from spacedatastandards.org v1.197.0 — every standard the pin
	// publishes, counted as `ls schema/*/main.fbs` counts them. The v1.196.0
	// bump folded the v1.193.0 $EGP partial addition back in and brought the
	// count to 224 with $IRM (Ingest Resume Mark, REC ordinal 223) and the 17
	// other standards REC.fbs began including between v1.186.0 and v1.196.0.
	//
	// v1.197.0 added exactly ONE: $VCF (vCard Projection Card, REC ordinal 224),
	// the canonical contact-card projection of one published EPM. The vCard
	// projection module writes it through the schema-typed storage.write
	// capability, and a standard the validator has never loaded is not one it
	// can admit — before that embed the write failed closed exactly as $IRM's
	// did before v1.196.0.
	//
	// v1.198.0 adds TWO more for the RF-catalog program: $STX (Scheduled
	// Transmission, REC ordinal 225) and $TXS (Terrestrial Transmitter Site,
	// REC ordinal 226). $TXS is the merged, source-attributed facility record
	// and $STX is one broadcast schedule row that references it by
	// TXS.ID — STX.fbs literally includes ../TXS/main.fbs and reuses
	// TXSProvenance, so the two are ONE include closure and neither embeds
	// alone. Only REC.fbs changed among the 225 already embedded; the rest of
	// this pin is the two new standards and the re-vendored bindings.
	// The admitted embedded set also includes WXF and NCD from SDS v1.215.0.
	expectedStandardSchemaCount = 232
	expectedInternalSchemaCount = 4
	expectedTotalSchemaCount    = expectedStandardSchemaCount + expectedInternalSchemaCount
)

func TestSupportedSchemas(t *testing.T) {
	if len(SupportedSchemas) != expectedTotalSchemaCount {
		t.Errorf("Expected %d schemas, got %d", expectedTotalSchemaCount, len(SupportedSchemas))
	}

	// Verify uniqueness and count standard vs internal schemas
	seen := make(map[string]bool, len(SupportedSchemas))
	standard, internal := 0, 0
	for _, s := range SupportedSchemas {
		if seen[s] {
			t.Errorf("Duplicate schema in SupportedSchemas: %s", s)
		}
		seen[s] = true

		if internalSchemas[s] {
			internal++
		} else {
			standard++
		}
	}

	if standard != expectedStandardSchemaCount {
		t.Errorf("Expected %d standard schemas, got %d", expectedStandardSchemaCount, standard)
	}
	if internal != expectedInternalSchemaCount {
		t.Errorf("Expected %d SDN-internal schemas, got %d", expectedInternalSchemaCount, internal)
	}

	// Spot-check a few well-known schemas
	for _, expected := range []string{"OMM.fbs", "CDM.fbs", "EPM.fbs", "CAT.fbs", "PNM.fbs", "PGM.fbs", "PRR.fbs", "PGR.fbs"} {
		if !seen[expected] {
			t.Errorf("Expected schema %s not found in SupportedSchemas", expected)
		}
	}
}

func TestSupportedSchemasMatchEmbedded(t *testing.T) {
	entries, err := schemasFS.ReadDir("schemas")
	if err != nil {
		t.Fatalf("Failed to read embedded schemas: %v", err)
	}

	embedded := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".fbs") {
			continue
		}
		embedded[entry.Name()] = true
	}

	supported := make(map[string]bool, len(SupportedSchemas))
	for _, s := range SupportedSchemas {
		supported[s] = true
		if !embedded[s] {
			t.Errorf("Schema %s listed in SupportedSchemas but not embedded", s)
		}
	}

	for name := range embedded {
		if !supported[name] {
			t.Errorf("Embedded schema %s missing from SupportedSchemas", name)
		}
	}

	if len(embedded) != expectedTotalSchemaCount {
		t.Errorf("Expected %d embedded schemas, got %d", expectedTotalSchemaCount, len(embedded))
	}
}

func TestEmbeddedSchemaPathUsesSlashSeparator(t *testing.T) {
	if got, want := embeddedSchemaPath("PNM.fbs"), "schemas/PNM.fbs"; got != want {
		t.Fatalf("embeddedSchemaPath() = %q, want %q", got, want)
	}
}

func TestEmbeddedSchemaPathDoesNotUseOSPathPackage(t *testing.T) {
	source, err := os.ReadFile("embed_path.go")
	if err != nil {
		t.Fatalf("failed to read embed path helper source: %v", err)
	}
	if strings.Contains(string(source), `"path/filepath"`) {
		t.Fatal("embedded schema paths must use forward slash paths for embed.FS, not OS-specific filepath paths")
	}
}

// includeRegex matches FlatBuffers include directives of the form:
// include "../XXX/main.fbs";
var includeRegex = regexp.MustCompile(`(?m)^include\s+"\.\./(\w+)/main\.fbs"`)

func TestEmbeddedSchemasParse(t *testing.T) {
	supported := make(map[string]bool, len(SupportedSchemas))
	for _, s := range SupportedSchemas {
		supported[s] = true
	}

	for _, name := range SupportedSchemas {
		content, err := schemasFS.ReadFile(embeddedSchemaPath(name))
		if err != nil {
			t.Errorf("Failed to read embedded schema %s: %v", name, err)
			continue
		}

		text := string(content)
		if len(strings.TrimSpace(text)) == 0 {
			t.Errorf("Schema %s is empty", name)
			continue
		}

		// Every schema must declare a root type to be usable for validation.
		if !strings.Contains(text, "root_type") {
			t.Errorf("Schema %s has no root_type declaration", name)
		}

		// Braces must balance for the schema to parse.
		if strings.Count(text, "{") != strings.Count(text, "}") {
			t.Errorf("Schema %s has unbalanced braces", name)
		}

		// All include targets must also be registered schemas. REC.fbs is the
		// exception by construction: it is the union wrapper over EVERY ratified
		// record type, and the node embeds a curated subset of the standard. The
		// host reads REC by Record.standard (internal/modulert/publication_signature.go)
		// and the validator loads each embed standalone, so an include of a
		// standard the node does not carry is expected there and resolved nowhere.
		if name == "REC.fbs" {
			continue
		}
		for _, match := range includeRegex.FindAllStringSubmatch(text, -1) {
			target := match[1] + ".fbs"
			if !supported[target] {
				t.Errorf("Schema %s includes %s which is not a registered schema", name, target)
			}
		}
	}
}

func TestValidatorLoadsAllSupportedSchemas(t *testing.T) {
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	if got := len(validator.Schemas()); got != expectedTotalSchemaCount {
		t.Errorf("Expected validator to load %d schemas, got %d", expectedTotalSchemaCount, got)
	}

	for _, name := range SupportedSchemas {
		if !validator.HasSchema(name) {
			t.Errorf("Validator missing supported schema %s", name)
		}
	}
}

// TestValidatorFieldLevelSampleMeasurement measures the converter's
// field-level parse over real stored records before it is enabled on the
// ingest path. SDN_VALIDATOR_SAMPLES names a directory of
// "<STD>--<label>.hex" files, one hex-encoded record per line (for example
// `select hex(data) from sds_p_<producer>__<STD>` against a copy of a node's
// control.flatsqldb). It reports, per standard, how many records pass the
// envelope, how many of those the field-level parse would reject, and what the
// parse costs per record. A rejection of an envelope-valid stored record is a
// false rejection: the record is already accepted by the network.
func TestValidatorFieldLevelSampleMeasurement(t *testing.T) {
	dir := os.Getenv("SDN_VALIDATOR_SAMPLES")
	if dir == "" {
		t.Skip("SDN_VALIDATOR_SAMPLES not set (measurement harness)")
	}
	ctx := context.Background()
	fm, err := wasm.NewEmbeddedFlatcModule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fm.Close(ctx)
	v, err := NewValidator(fm)
	if err != nil {
		t.Fatal(err)
	}
	v.SetFieldLevelValidation(true)

	type row struct {
		records, envelopeFail, rejected, realigned, skipped int
		verified                                            int
		parse, verifiedParse                                time.Duration
		samples                                             []string
	}
	stats := map[string]*row{}
	files, err := filepath.Glob(filepath.Join(dir, "*.hex"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no *.hex samples in %s (%v)", dir, err)
	}
	for _, file := range files {
		std := strings.SplitN(filepath.Base(file), "--", 2)[0]
		schema := std + ".fbs"
		r := stats[std]
		if r == nil {
			r = &row{}
			stats[std] = r
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		id, idErr := v.converterID(ctx, schema)
		for _, line := range strings.Split(string(content), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			data, err := hex.DecodeString(line)
			if err != nil {
				t.Fatalf("%s: bad hex: %v", file, err)
			}
			r.records++
			if err := v.VerifyEnvelope(schema, data); err != nil {
				r.envelopeFail++
				continue
			}
			if idErr != nil {
				r.skipped++
				continue
			}
			opts := wasm.FlatcOption(0)
			if form, _ := v.DetectEnvelopeForm(schema, data); form == EnvelopeSizePrefixed {
				opts = wasm.FlatcSizePrefixed
			}
			start := time.Now()
			err = fm.VerifyBinary(ctx, id, data, opts)
			took := time.Since(start)
			r.parse += took
			if err == nil {
				r.verified++
				r.verifiedParse += took
			}
			if err != nil {
				r.rejected++
				if len(r.samples) < 3 {
					r.samples = append(r.samples, err.Error())
				}
				// The verifier checks alignment against the buffer start. A
				// record whose size prefix was stripped has its 8-byte fields
				// at 4 mod 8; behind a fresh 4-byte prefix it is aligned again.
				if opts == 0 {
					framed := binary.LittleEndian.AppendUint32(make([]byte, 0, len(data)+4), uint32(len(data)))
					start := time.Now()
					if fm.VerifyBinary(ctx, id, append(framed, data...), wasm.FlatcSizePrefixed) == nil {
						r.realigned++
						r.verified++
						r.verifiedParse += time.Since(start)
					}
				}
			}
		}
		if idErr != nil {
			t.Logf("%s: converter cannot load %s: %v", filepath.Base(file), schema, idErr)
		}
	}

	names := make([]string, 0, len(stats))
	for std := range stats {
		names = append(names, std)
	}
	sort.Strings(names)
	var total row
	// us/record: the parse as Validate would run it, per envelope-valid record.
	// us/verified: the parse of a record that verifies (directly or realigned),
	// the steady-state cost once the alignment rejections are resolved.
	t.Logf("%-5s %9s %9s %9s %9s %9s %12s %12s", "STD", "records", "env-fail", "rejected", "realign", "skipped", "us/record", "us/verified")
	for _, std := range names {
		r := stats[std]
		parsed := r.records - r.envelopeFail - r.skipped
		per := float64(r.parse.Microseconds()) / float64(max(parsed, 1))
		perOK := float64(r.verifiedParse.Microseconds()) / float64(max(r.verified, 1))
		t.Logf("%-5s %9d %9d %9d %9d %9d %12.1f %12.1f", std, r.records, r.envelopeFail, r.rejected, r.realigned, r.skipped, per, perOK)
		for _, s := range r.samples {
			t.Logf("      %s", s)
		}
		total.records += r.records
		total.envelopeFail += r.envelopeFail
		total.rejected += r.rejected
		total.realigned += r.realigned
		total.skipped += r.skipped
		total.parse += r.parse
		total.verified += r.verified
		total.verifiedParse += r.verifiedParse
	}
	parsed := total.records - total.envelopeFail - total.skipped
	t.Logf("%-5s %9d %9d %9d %9d %9d %12.1f %12.1f", "ALL", total.records, total.envelopeFail, total.rejected, total.realigned, total.skipped,
		float64(total.parse.Microseconds())/float64(max(parsed, 1)),
		float64(total.verifiedParse.Microseconds())/float64(max(total.verified, 1)))
}

func newConverterValidator(t *testing.T) *Validator {
	t.Helper()
	fm, err := wasm.NewEmbeddedFlatcModule(context.Background())
	if err != nil {
		t.Fatalf("NewEmbeddedFlatcModule: %v", err)
	}
	t.Cleanup(func() { fm.Close(context.Background()) })
	v, err := NewValidator(fm)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

func jsonValues(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return out
}

func TestValidatorConverterRoundTrip(t *testing.T) {
	v := newConverterValidator(t)
	ctx := context.Background()

	cases := map[string]string{
		"OMM.fbs": `{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","EPOCH":"2026-09-27T12:00:00.000000","MEAN_MOTION":15.50103472,"ECCENTRICITY":0.0006703,"NORAD_CAT_ID":25544,"BSTAR":0.0001027,"MEAN_MOTION_DDOT":0}`,
		"MPE.fbs": `{"ENTITY_ID":"25544","EPOCH":1790000000.25,"MEAN_MOTION":15.50103472,"BSTAR":0.0001027,"MEAN_ELEMENT_THEORY":"SGP4"}`,
		"CAT.fbs": `{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","NORAD_CAT_ID":25544,"PERIOD":92.9}`,
	}
	for schema, record := range cases {
		stored, err := v.JSONToFlatBuffer(ctx, schema, []byte(record), StoredRecordOptions)
		if err != nil {
			t.Fatalf("%s JSONToFlatBuffer: %v", schema, err)
		}
		if form, err := v.DetectEnvelopeForm(schema, stored); err != nil || form != EnvelopeBare {
			t.Fatalf("%s stored form = %q, %v; want bare", schema, form, err)
		}
		if err := v.Validate(ctx, schema, stored); err != nil {
			t.Fatalf("%s Validate: %v", schema, err)
		}
		framed, err := v.JSONToFlatBuffer(ctx, schema, []byte(record), StoredRecordOptions|wasm.FlatcSizePrefixed)
		if err != nil {
			t.Fatalf("%s size-prefixed JSONToFlatBuffer: %v", schema, err)
		}
		// Either form reads back; the prefix option follows the envelope.
		for _, bin := range [][]byte{stored, framed} {
			out, err := v.FlatBufferToJSON(ctx, schema, bin, wasm.FlatcCompactJSON)
			if err != nil {
				t.Fatalf("%s FlatBufferToJSON: %v", schema, err)
			}
			if got, want := jsonValues(t, out), jsonValues(t, []byte(record)); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s round trip\n got %v\nwant %v", schema, got, want)
			}
		}
	}
}

func TestValidatorConverterSchemaAvailability(t *testing.T) {
	v := newConverterValidator(t)
	ctx := context.Background()

	// REC.fbs includes standards the node does not embed: registered for
	// envelope validation, unavailable for conversion, and says why.
	if !v.HasSchema("REC.fbs") {
		t.Fatal("REC.fbs not registered")
	}
	if err := v.ConverterError(ctx, "REC.fbs"); err == nil || !strings.Contains(err.Error(), "include") {
		t.Fatalf("REC.fbs converter error = %v, want an unresolved include", err)
	}
	if _, err := v.JSONToFlatBuffer(ctx, "REC.fbs", []byte(`{}`), 0); err == nil || !strings.Contains(err.Error(), "not loaded in the flatc converter") {
		t.Fatalf("REC.fbs conversion: err = %v", err)
	}
	if _, err := v.JSONToFlatBuffer(ctx, "NOPE.fbs", []byte(`{}`), 0); err == nil || !strings.Contains(err.Error(), "unknown schema") {
		t.Fatalf("unknown schema conversion: err = %v", err)
	}

	envelopeOnly, err := NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := envelopeOnly.JSONToFlatBuffer(ctx, "OMM.fbs", []byte(`{}`), 0); !errors.Is(err, wasm.ErrNoModule) {
		t.Fatalf("no converter: err = %v, want ErrNoModule", err)
	}
	if envelopeOnly.FieldLevelValidation() {
		t.Fatal("field-level validation reported on without a converter")
	}
}

// Every embedded schema but REC.fbs parses in the converter with its
// includes resolved through the <FAMILY>/main.fbs file map.
func TestValidatorConverterLoadsEmbeddedSchemas(t *testing.T) {
	v := newConverterValidator(t)
	ctx := context.Background()
	loaded, failed := v.PreloadConverterSchemas(ctx)
	if failed != 1 || v.ConverterError(ctx, "REC.fbs") == nil {
		for _, name := range v.Schemas() {
			if err := v.ConverterError(ctx, name); err != nil {
				t.Logf("%s: %v", name, err)
			}
		}
		t.Fatalf("converter failed %d schema(s), want only REC.fbs", failed)
	}
	if loaded != expectedTotalSchemaCount-1 {
		t.Fatalf("converter loaded %d schemas, want %d", loaded, expectedTotalSchemaCount-1)
	}
}

// corruptStringOffset points OMM.OBJECT_NAME (field 3) past the end of a bare
// buffer: the envelope still holds, the field does not.
func corruptStringOffset(t *testing.T, buf []byte) []byte {
	t.Helper()
	out := append([]byte(nil), buf...)
	root := int(binary.LittleEndian.Uint32(out))
	vtable := root - int(int32(binary.LittleEndian.Uint32(out[root:])))
	slot := vtable + 4 + 2*3
	fieldOff := int(binary.LittleEndian.Uint16(out[slot:]))
	if fieldOff == 0 {
		t.Fatal("fixture has no OBJECT_NAME")
	}
	binary.LittleEndian.PutUint32(out[root+fieldOff:], 0x7ffffff0)
	return out
}

func TestValidatorFieldLevelSwitch(t *testing.T) {
	v := newConverterValidator(t)
	ctx := context.Background()
	if v.FieldLevelValidation() != DefaultFieldLevelValidation {
		t.Fatalf("field-level validation = %v, want the default %v", v.FieldLevelValidation(), DefaultFieldLevelValidation)
	}

	good, err := v.JSONToFlatBuffer(ctx, "OMM.fbs", []byte(`{"OBJECT_NAME":"X","NORAD_CAT_ID":7}`), StoredRecordOptions)
	if err != nil {
		t.Fatal(err)
	}
	bad := corruptStringOffset(t, good)
	if err := v.VerifyEnvelope("OMM.fbs", bad); err != nil {
		t.Fatalf("fixture must pass the envelope: %v", err)
	}

	v.SetFieldLevelValidation(false)
	if err := v.Validate(ctx, "OMM.fbs", bad); err != nil {
		t.Fatalf("switch off: the envelope verdict stands, got %v", err)
	}
	v.SetFieldLevelValidation(true)
	if err := v.Validate(ctx, "OMM.fbs", bad); err == nil || !strings.Contains(err.Error(), "INVALID_BINARY") {
		t.Fatalf("switch on: err = %v, want INVALID_BINARY", err)
	}
	if err := v.Validate(ctx, "OMM.fbs", good); err != nil {
		t.Fatalf("switch on, good record: %v", err)
	}

	// The in-repo record builders' output passes the field-level parse.
	fixtures := map[string][]byte{
		"OMM.fbs": NewOMMBuilder().WithNoradCatID(25544).Build(),
		"CAT.fbs": NewCATBuilder().WithNoradCatID(25544).Build(),
		"EPM.fbs": NewEPMBuilder().WithLegalName("Test Org").Build(),
		"PNM.fbs": NewPNMBuilder().Build(),
	}
	for schema, record := range fixtures {
		if err := v.Validate(ctx, schema, record); err != nil {
			t.Errorf("builder %s record rejected by the field-level parse: %v", schema, err)
		}
	}
}

func TestFieldLevelValidationEnv(t *testing.T) {
	t.Setenv(FieldLevelValidationEnv, "maybe")
	if _, err := NewValidator(nil); err == nil || !strings.Contains(err.Error(), FieldLevelValidationEnv) {
		t.Fatalf("invalid switch value: err = %v", err)
	}
	t.Setenv(FieldLevelValidationEnv, "1")
	v := newConverterValidator(t)
	if !v.FieldLevelValidation() {
		t.Fatalf("%s=1 left field-level validation off", FieldLevelValidationEnv)
	}
}
