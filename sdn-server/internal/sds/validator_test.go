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
		result := SchemaNameToTable(test.input)
		if result != test.expected {
			t.Errorf("SchemaNameToTable(%q) = %q, want %q", test.input, result, test.expected)
		}
	}
}

func TestSupportedSchemas(t *testing.T) {
	// Verify SupportedSchemas contains expected schemas
	expectedSchemas := []string{
		"ATM.fbs", "BOV.fbs", "CAT.fbs", "CDM.fbs", "CRM.fbs",
		"CSM.fbs", "CTR.fbs", "EME.fbs", "EOO.fbs", "EOP.fbs",
		"EPM.fbs", "HYP.fbs", "IDM.fbs", "LCC.fbs", "LDM.fbs",
		"MET.fbs", "MPE.fbs", "OCM.fbs", "OEM.fbs", "OMM.fbs",
		"OSM.fbs", "PLD.fbs", "PNM.fbs", "PRG.fbs", "REC.fbs",
		"RFM.fbs", "ROC.fbs", "SCM.fbs", "SIT.fbs", "TDM.fbs",
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
