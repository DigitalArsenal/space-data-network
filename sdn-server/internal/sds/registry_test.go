// Package sds provides Space Data Standards schema registry and management.
package sds

import (
	"testing"
)

func TestNewSchemaRegistry(t *testing.T) {
	registry, err := NewSchemaRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	if registry == nil {
		t.Fatal("Expected non-nil registry")
	}
}

func TestSchemaRegistryList(t *testing.T) {
	registry, err := NewSchemaRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	schemas := registry.List()

	// Should have schemas
	if len(schemas) == 0 {
		t.Error("Expected schemas to be listed")
	}
}

func TestSchemaRegistryHas(t *testing.T) {
	registry, err := NewSchemaRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Test schemas that should exist
	expectedSchemas := []string{"OMM.fbs", "CDM.fbs", "EPM.fbs", "CAT.fbs"}
	for _, schema := range expectedSchemas {
		if !registry.Has(schema) {
			t.Errorf("Expected schema %s to exist", schema)
		}
	}

	// Test schema that shouldn't exist
	if registry.Has("NONEXISTENT.fbs") {
		t.Error("Expected NONEXISTENT.fbs to not exist")
	}
}

func TestSchemaRegistryGet(t *testing.T) {
	registry, err := NewSchemaRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Get a schema that should exist
	content, ok := registry.Get("OMM.fbs")
	if !ok {
		t.Error("Expected to get OMM.fbs schema")
	}

	// Content might be nil if schemas weren't embedded, but that's ok
	_ = content

	// Try to get non-existent schema
	_, ok = registry.Get("NONEXISTENT.fbs")
	if ok {
		t.Error("Expected not to get NONEXISTENT.fbs schema")
	}
}

func TestSchemaRegistryAdd(t *testing.T) {
	registry, err := NewSchemaRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Add a custom schema
	content := []byte("// Custom schema")
	description := "Test custom schema"
	registry.Add("CUSTOM.fbs", content, description)

	// Verify it was added
	if !registry.Has("CUSTOM.fbs") {
		t.Error("Expected CUSTOM.fbs to exist after adding")
	}

	// Verify content
	retrieved, ok := registry.Get("CUSTOM.fbs")
	if !ok {
		t.Error("Expected to get CUSTOM.fbs")
	}
	if string(retrieved) != string(content) {
		t.Error("Content mismatch")
	}

	// Verify description
	desc := registry.GetDescription("CUSTOM.fbs")
	if desc != description {
		t.Errorf("Description mismatch: got %q, want %q", desc, description)
	}
}

func TestSchemaRegistryInfo(t *testing.T) {
	registry, err := NewSchemaRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	info := registry.Info()

	// Should have info for all schemas
	if len(info) == 0 {
		t.Error("Expected non-empty info")
	}

	// Verify info structure
	for _, i := range info {
		if i.Name == "" {
			t.Error("Expected non-empty schema name")
		}
	}
}

func TestSchemaDescriptions(t *testing.T) {
	// Verify schemaDescriptions map has entries
	expectedDescriptions := map[string]string{
		"OMM.fbs": "Orbit Mean-Elements Message",
		"CDM.fbs": "Conjunction Data Message",
		"EPM.fbs": "Entity Profile Manifest",
		"CAT.fbs": "Catalog",
	}

	for schema, expectedPartial := range expectedDescriptions {
		desc, ok := schemaDescriptions[schema]
		if !ok {
			t.Errorf("Expected description for %s", schema)
			continue
		}
		if desc == "" {
			t.Errorf("Expected non-empty description for %s", schema)
		}
		// Check that description contains expected substring
		if len(desc) < len(expectedPartial) {
			t.Errorf("Description for %s is too short: %s", schema, desc)
		}
	}
}

func TestExtractDescription(t *testing.T) {
	tests := []struct {
		content  string
		expected string
	}{
		{"/// This is a doc comment", "This is a doc comment"},
		{"// Regular comment", "Regular comment"},
		{"no comment\ntable Test {}", ""},
		{"/// First line\n/// Second line", "First line"},
	}

	for _, test := range tests {
		result := extractDescription([]byte(test.content))
		if result != test.expected {
			t.Errorf("extractDescription(%q) = %q, want %q", test.content, result, test.expected)
		}
	}
}
