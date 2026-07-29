package flowrt

import (
	"strings"
	"testing"
)

// neutralLinkedStoreDescriptor was orphaned when flatsql_query_test.go was
// deleted (its last definition is in 459eedd7), leaving flatsql_descriptor_test.go
// referencing an undefined symbol. That did not break the BUILD — the package
// compiles fine — it broke the TEST BINARY, so `go vet`/`go test` failed to
// compile and NO test in this package could run, including the ones pinning the
// 64->68 byte FlowEdge stride. A pin that cannot execute is not a pin.
func neutralLinkedStoreDescriptor() *LinkedStoreDescriptor {
	return &LinkedStoreDescriptor{
		Version:  1,
		Engine:   "flatsql",
		Database: "fixture_records",
		Schema:   "table fixture_records { key:string (key); label:string; data:[ubyte]; }",
		FileIdentifiers: []LinkedStoreFileIdentifier{{
			ID: "TREC", Table: "fixture_records",
		}},
	}
}

// The generation this host decodes must track the stride it decodes. If someone
// changes one without the other the gate silently starts admitting the wrong
// artifacts, which is the whole failure mode it exists to prevent.
func TestDescriptorABIGenerationMatchesTheStride(t *testing.T) {
	if flowEdgeDescriptorSize != 68 {
		t.Fatalf("FlowEdge stride is %d; generation %d is defined as the 68-byte layout — bump the generation with the stride",
			flowEdgeDescriptorSize, flowEdgeDescriptorABIGeneration)
	}
	if flowEdgeDescriptorABIGeneration != 2 {
		t.Fatalf("descriptor ABI generation is %d, want 2 for the 68-byte FlowEdge", flowEdgeDescriptorABIGeneration)
	}
}

// A generation-1 artifact must be REFUSED, not read at the generation-2 stride.
// Generation 1 is also what a MISSING export means: every generation-1 bundle
// predates the export, so absence can never be treated as permission.
func TestGenerationOneIsRefused(t *testing.T) {
	err := checkDescriptorABIGeneration(1)
	if err == nil {
		t.Fatal("a generation-1 artifact was accepted: its 64-byte edges would be read at stride 68 and believed")
	}
	if !strings.Contains(err.Error(), "generation 1") {
		t.Fatalf("refusal does not name the offending generation, which is what an operator needs: %v", err)
	}
}

func TestCurrentGenerationIsAccepted(t *testing.T) {
	if err := checkDescriptorABIGeneration(flowEdgeDescriptorABIGeneration); err != nil {
		t.Fatalf("the generation this host decodes was refused: %v", err)
	}
}

// A FUTURE generation must fail closed too. Reading a newer table at today's
// stride is the same defect in the other direction.
func TestFutureGenerationIsRefused(t *testing.T) {
	if err := checkDescriptorABIGeneration(flowEdgeDescriptorABIGeneration + 1); err == nil {
		t.Fatal("a future descriptor generation was accepted; newer is not compatible, only different")
	}
}
