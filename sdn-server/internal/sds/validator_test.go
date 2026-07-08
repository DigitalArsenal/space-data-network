// Package sds provides Space Data Standards validation and schema handling.
package sds

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
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

	// Test validation with known schema (basic validation without WASM)
	err = validator.Validate(ctx, "OMM.fbs", []byte(`{"satellite": "ISS"}`))
	if err != nil {
		t.Errorf("Unexpected validation error: %v", err)
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
	expectedStandardSchemaCount = 171
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

		// All include targets must also be registered schemas.
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
