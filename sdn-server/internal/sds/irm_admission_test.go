package sds

import (
	"context"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IRM"
)

// buildIRMResumeMark writes the smallest LEGAL $IRM record: the three fields the
// IDL marks (required) — JOB_ID, PROVIDER_ID and a SOURCE table whose own
// SOURCE_URL is required — plus the position a resuming reader actually needs.
// A FlatBuffers builder refuses to finish a table missing a required field, so
// this constructor is itself a check that the vendored IRM binding and the
// embedded IRM.fbs agree on what "required" means.
func buildIRMResumeMark(t *testing.T) []byte {
	t.Helper()

	b := flatbuffers.NewBuilder(512)

	sourceURL := b.CreateString("https://example.invalid/bulk/catalog.csv")
	IRM.IRMSourceStart(b)
	IRM.IRMSourceAddSOURCE_URL(b, sourceURL)
	source := IRM.IRMSourceEnd(b)

	jobID := b.CreateString("irm-admission-test-job")
	providerID := b.CreateString("12D3KooWTestProviderIdentityForIRMAdmission")

	IRM.IRMStart(b)
	IRM.IRMAddJOB_ID(b, jobID)
	IRM.IRMAddPROVIDER_ID(b, providerID)
	IRM.IRMAddSOURCE(b, source)
	IRM.IRMAddSEQUENCE(b, 1)
	// A mark advances from the DURABLE result: this one says one chunk was
	// committed and the next read starts at a RECORD boundary, not a byte budget.
	IRM.IRMAddNEXT_OFFSET(b, 4096)
	IRM.IRMAddCHUNKS_COMMITTED(b, 1)
	mark := IRM.IRMEnd(b)

	IRM.FinishSizePrefixedIRMBuffer(b, mark)
	return b.FinishedBytes()
}

// TestIRMIsAdmittedByTheEmbeddedValidator is the reason the v1.196.0 pin exists.
//
// The cellular ingest flow checkpoints itself by writing an $IRM record through
// the SCHEMA-TYPED storage.write capability. That lane is only as good as this
// node's embedded validator: a standard the validator has never loaded is not a
// standard it can admit, so before this embed the checkpoint write failed closed
// and every crash re-read the whole source from offset zero.
//
// The host does not field-decode $IRM in Go — that is why IRM.fbs sits in
// unguardedEmbeddedSchemas rather than driftGuardedSchemas. This test is the
// compensating control for that waiver: it exercises the embed against the
// VENDORED binding, so the two authorities cannot silently disagree about the
// record the ingest lane depends on.
func TestIRMIsAdmittedByTheEmbeddedValidator(t *testing.T) {
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	ctx := context.Background()

	if !validator.HasSchema("IRM.fbs") {
		t.Fatal("IRM.fbs is not loaded by the validator: the ingest resume mark cannot be written through storage.write")
	}

	// storage.write -> FlatSQLStore.storeOne gates the table name through this
	// helper before it touches the stream, so $IRM must resolve to a table.
	table, err := SchemaNameToTable("IRM.fbs")
	if err != nil {
		t.Fatalf("SchemaNameToTable(IRM.fbs): %v", err)
	}
	if table != "IRM" {
		t.Errorf("SchemaNameToTable(IRM.fbs) = %q, want IRM", table)
	}

	record := buildIRMResumeMark(t)

	if !IRM.SizePrefixedIRMBufferHasIdentifier(record) {
		t.Fatal("built IRM buffer is missing the $IRM file identifier")
	}
	if got, ok := validator.FileIdentifier("IRM.fbs"); !ok || got != "$IRM" {
		t.Errorf("embedded IRM.fbs declares file identifier %q (found=%v), want $IRM", got, ok)
	}

	if err := validator.Validate(ctx, "IRM.fbs", record); err != nil {
		t.Errorf("a real $IRM resume mark was refused by the embedded validator: %v", err)
	}

	// A registered schema is not a licence to store arbitrary bytes.
	if err := validator.Validate(ctx, "IRM.fbs", []byte(`{"JOB_ID":"not-a-flatbuffer"}`)); err == nil {
		t.Error("expected a JSON payload published as an $IRM FlatBuffer to be refused")
	}
	if err := validator.Validate(ctx, "IRM.fbs", []byte{}); err == nil {
		t.Error("expected an empty $IRM payload to be refused")
	}

	// Round-trip through the vendored binding: the mark must read back the
	// position it was written with, or "resume" silently means "restart".
	decoded := IRM.GetSizePrefixedRootAsIRM(record, 0)
	if string(decoded.JOB_ID()) != "irm-admission-test-job" {
		t.Errorf("JOB_ID mismatch: got %q", decoded.JOB_ID())
	}
	if decoded.NEXT_OFFSET() != 4096 {
		t.Errorf("NEXT_OFFSET mismatch: got %d, want 4096", decoded.NEXT_OFFSET())
	}
	if decoded.CHUNKS_COMMITTED() != 1 {
		t.Errorf("CHUNKS_COMMITTED mismatch: got %d, want 1", decoded.CHUNKS_COMMITTED())
	}
	src := decoded.SOURCE(nil)
	if src == nil {
		t.Fatal("SOURCE is nil on a record whose IDL marks it required")
	}
	if string(src.SOURCE_URL()) != "https://example.invalid/bulk/catalog.csv" {
		t.Errorf("SOURCE_URL mismatch: got %q", src.SOURCE_URL())
	}
}

// TestIRMIsNotOnTheAnonymousDataPlane states the other half of the ruling.
//
// $IRM is BOOKKEEPING ABOUT an ingest, never the ingested data — it carries no
// rows, and a consumer that loses every mark loses only work. It is therefore
// writable through storage.write and readable by its own publisher, but it is
// not a served record surface: it is not public-read, and it is deliberately
// absent from the engine's routed record slice (OMM/TBS today). Publishing a
// node's job cursors anonymously would leak its ingest schedule for nothing.
func TestIRMIsNotOnTheAnonymousDataPlane(t *testing.T) {
	if IsPublicReadSchema("IRM.fbs") {
		t.Error("IRM.fbs is on the anonymous public-read allow-list: a resume mark is bookkeeping, not a published record")
	}
}
