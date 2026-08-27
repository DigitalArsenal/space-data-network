package sds

import (
	"context"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/STX"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/TXS"
)

const (
	txsTestSiteID   = "txs-admission-test-site"
	stxTestRowID    = "stx-admission-test-row"
	txsTestProvider = "test-rf-register"
	txsTestLicense  = "CC-BY-4.0"
	txsTestRetrieve = "2026-08-28T00:00:00.000Z"
)

// buildTXSSite writes the smallest LEGAL $TXS record. The IDL marks ID,
// SOURCES and CONSENSUS (required) on the root, and PROVIDER_ID, RETRIEVED_AT
// and LICENSE (required) on every TXSProvenance — the standard's own statement
// that a merged facility is never unattributed. A FlatBuffers builder refuses
// to finish a table missing a required field, so this constructor is itself a
// check that the vendored binding and the embedded TXS.fbs agree on what
// "required" means.
func buildTXSSite(t *testing.T) []byte {
	t.Helper()

	b := flatbuffers.NewBuilder(1024)

	providerID := b.CreateString(txsTestProvider)
	retrievedAt := b.CreateString(txsTestRetrieve)
	license := b.CreateString(txsTestLicense)
	TXS.TXSProvenanceStart(b)
	TXS.TXSProvenanceAddPROVIDER_ID(b, providerID)
	TXS.TXSProvenanceAddRETRIEVED_AT(b, retrievedAt)
	TXS.TXSProvenanceAddLICENSE(b, license)
	provenance := TXS.TXSProvenanceEnd(b)

	TXS.TXSStartSOURCESVector(b, 1)
	b.PrependUOffsetT(provenance)
	sources := b.EndVector(1)

	TXS.TXSConsensusStart(b)
	TXS.TXSConsensusAddCONFIDENCE(b, 1.0)
	consensus := TXS.TXSConsensusEnd(b)

	id := b.CreateString(txsTestSiteID)
	TXS.TXSStart(b)
	TXS.TXSAddID(b, id)
	TXS.TXSAddSOURCES(b, sources)
	TXS.TXSAddCONSENSUS(b, consensus)
	site := TXS.TXSEnd(b)

	TXS.FinishSizePrefixedTXSBuffer(b, site)
	return b.FinishedBytes()
}

// buildSTXSchedule writes the smallest LEGAL $STX record that also carries the
// join the RF catalogue is read on: SITE_ID pointing at a $TXS.ID.
//
// The builders come from the STX package, not TXS, and that is the point:
// STX.fbs includes ../TXS/main.fbs, so flatc emits its OWN copy of
// TXSProvenance into the STX package. Building the schedule row's provenance
// with STX.TXSProvenance* is what proves the two embedded IDLs are one include
// closure rather than two files that merely happen to be present.
func buildSTXSchedule(t *testing.T) []byte {
	t.Helper()

	b := flatbuffers.NewBuilder(1024)

	providerID := b.CreateString(txsTestProvider)
	retrievedAt := b.CreateString(txsTestRetrieve)
	license := b.CreateString(txsTestLicense)
	STX.TXSProvenanceStart(b)
	STX.TXSProvenanceAddPROVIDER_ID(b, providerID)
	STX.TXSProvenanceAddRETRIEVED_AT(b, retrievedAt)
	STX.TXSProvenanceAddLICENSE(b, license)
	provenance := STX.TXSProvenanceEnd(b)

	STX.STXStartSOURCESVector(b, 1)
	b.PrependUOffsetT(provenance)
	sources := b.EndVector(1)

	id := b.CreateString(stxTestRowID)
	siteID := b.CreateString(txsTestSiteID)
	STX.STXStart(b)
	STX.STXAddID(b, id)
	STX.STXAddSITE_ID(b, siteID)
	STX.STXAddSOURCES(b, sources)
	row := STX.STXEnd(b)

	STX.FinishSizePrefixedSTXBuffer(b, row)
	return b.FinishedBytes()
}

// TestTXSAndSTXAreAdmittedByTheEmbeddedValidator is the reason the v1.198.0
// pin exists.
//
// The RF-catalogue ingest builds $TXS facility records and $STX schedule rows
// in WASM and writes them through the SCHEMA-TYPED storage.write capability.
// That lane is only as good as this node's embedded validator: a standard the
// validator has never loaded is not one it can admit, so before this embed
// both writes failed CLOSED — the same failure $VCF had before v1.197.0 and
// $IRM before v1.196.0.
//
// The host does not field-decode either standard in Go, which is why both sit
// in unguardedEmbeddedSchemas rather than driftGuardedSchemas. This test is
// the compensating control for that waiver, and it is deliberately ONE test
// for BOTH: STX.fbs includes ../TXS/main.fbs, so the pair is a single include
// closure that the validator loads whole or not at all.
func TestTXSAndSTXAreAdmittedByTheEmbeddedValidator(t *testing.T) {
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	ctx := context.Background()

	for _, tc := range []struct {
		schema string
		fileID string
		table  string
	}{
		{"TXS.fbs", "$TXS", "TXS"},
		{"STX.fbs", "$STX", "STX"},
	} {
		if !validator.HasSchema(tc.schema) {
			t.Fatalf("%s is not loaded by the validator: RF-catalogue records cannot be written through storage.write", tc.schema)
		}
		// storage.write -> FlatSQLStore.storeOne gates the table name through
		// this helper before it touches the stream.
		table, err := SchemaNameToTable(tc.schema)
		if err != nil {
			t.Fatalf("SchemaNameToTable(%s): %v", tc.schema, err)
		}
		if table != tc.table {
			t.Errorf("SchemaNameToTable(%s) = %q, want %s", tc.schema, table, tc.table)
		}
		if got, ok := validator.FileIdentifier(tc.schema); !ok || got != tc.fileID {
			t.Errorf("embedded %s declares file identifier %q (found=%v), want %s", tc.schema, got, ok, tc.fileID)
		}
	}

	site := buildTXSSite(t)
	row := buildSTXSchedule(t)

	if !TXS.SizePrefixedTXSBufferHasIdentifier(site) {
		t.Fatal("built TXS buffer is missing the $TXS file identifier")
	}
	if !STX.SizePrefixedSTXBufferHasIdentifier(row) {
		t.Fatal("built STX buffer is missing the $STX file identifier")
	}

	if err := validator.Validate(ctx, "TXS.fbs", site); err != nil {
		t.Errorf("a real $TXS transmitter site was refused by the embedded validator: %v", err)
	}
	if err := validator.Validate(ctx, "STX.fbs", row); err != nil {
		t.Errorf("a real $STX schedule row was refused by the embedded validator: %v", err)
	}

	// A registered schema is not a licence to store arbitrary bytes, and the
	// two identifiers are not interchangeable: routing is on the record's own
	// identifier, so a $TXS record offered as $STX must be refused.
	if err := validator.Validate(ctx, "TXS.fbs", []byte(`{"ID":"not-a-flatbuffer"}`)); err == nil {
		t.Error("expected a JSON payload published as a $TXS FlatBuffer to be refused")
	}
	if err := validator.Validate(ctx, "STX.fbs", []byte{}); err == nil {
		t.Error("expected an empty $STX payload to be refused")
	}
	if err := validator.Validate(ctx, "STX.fbs", site); err == nil {
		t.Error("expected a $TXS record offered as $STX to be refused: the identifier is what identifies the standard")
	}

	// Round-trip through the vendored bindings, and assert the JOIN the RF
	// dataset is actually read on: STX.SITE_ID resolves to TXS.ID. If these
	// two drift apart, a schedule row silently references no facility.
	decodedSite := TXS.GetSizePrefixedRootAsTXS(site, 0)
	if got := string(decodedSite.ID()); got != txsTestSiteID {
		t.Errorf("TXS.ID mismatch: got %q, want %q", got, txsTestSiteID)
	}
	if n := decodedSite.SOURCESLength(); n != 1 {
		t.Errorf("TXS.SOURCES length = %d, want 1 — a merged facility is never unattributed", n)
	}
	if decodedSite.CONSENSUS(nil) == nil {
		t.Error("TXS.CONSENSUS is nil on a record whose IDL marks it required")
	}

	decodedRow := STX.GetSizePrefixedRootAsSTX(row, 0)
	if got := string(decodedRow.ID()); got != stxTestRowID {
		t.Errorf("STX.ID mismatch: got %q", got)
	}
	if got := string(decodedRow.SITE_ID()); got != string(decodedSite.ID()) {
		t.Errorf("the $TXS <- $STX join broke: STX.SITE_ID = %q, TXS.ID = %q", got, decodedSite.ID())
	}
}

// TestTXSAndSTXAreNotOnTheAnonymousDataPlane states the other half of the
// ruling, on the same reasoning the $VCF exclusion rests on.
//
// publicReadSchemas is an ALLOW list and it fails closed: embedding a standard
// never makes it anonymously readable. The RF catalogue's source registers
// carry per-source LICENSE terms in TXSProvenance, and this node has not yet
// ruled that every licence it may ingest permits anonymous republication —
// $TXS.SOURCES is precisely the field that would decide it, record by record.
// Serving the merged product anonymously before that check exists would
// republish third-party register data on terms nobody verified. Adding either
// schema later is one line, and this test makes that a deliberate act.
func TestTXSAndSTXAreNotOnTheAnonymousDataPlane(t *testing.T) {
	for _, schema := range []string{"TXS.fbs", "STX.fbs"} {
		if IsPublicReadSchema(schema) {
			t.Errorf("%s is on the anonymous public-read allow-list before its per-source LICENSE terms have been ruled on", schema)
		}
	}
}
