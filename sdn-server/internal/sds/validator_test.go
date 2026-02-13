// Package sds provides Space Data Standards validation and schema handling.
package sds

import (
	"context"
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
		{"OMM.fbs", "sds_omm"},
		{"CDM.fbs", "sds_cdm"},
		{"EPM.fbs", "sds_epm"},
		{"CUSTOM", "sds_custom"},
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

func TestSupportedSchemas(t *testing.T) {
	// Verify SupportedSchemas contains expected schemas
	expectedSchemas := []string{
		"ACL.fbs", "ATM.fbs", "BOV.fbs", "CAT.fbs", "CDM.fbs",
		"CRM.fbs", "CSM.fbs", "CTR.fbs", "EME.fbs", "EOO.fbs",
		"EOP.fbs", "EPM.fbs", "HYP.fbs", "IDM.fbs", "LCC.fbs",
		"LDM.fbs", "MET.fbs", "MPE.fbs", "OCM.fbs", "OEM.fbs",
		"OMM.fbs", "OSM.fbs", "PLD.fbs", "PNM.fbs", "PRG.fbs",
		"PUR.fbs", "REC.fbs", "REV.fbs", "RFM.fbs", "RHD.fbs",
		"ROC.fbs", "SCM.fbs", "SIT.fbs", "STF.fbs", "TDM.fbs",
		"TIM.fbs", "VCM.fbs",
	}

	if len(SupportedSchemas) != len(expectedSchemas) {
		t.Errorf("Expected %d schemas, got %d", len(expectedSchemas), len(SupportedSchemas))
	}

	for _, expected := range expectedSchemas {
		found := false
		for _, s := range SupportedSchemas {
			if s == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected schema %s not found in SupportedSchemas", expected)
		}
	}
}
