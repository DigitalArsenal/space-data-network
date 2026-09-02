package sds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/VCF"
)

// canonicalTestCard is a CRLF-terminated vCard 3.0 text. It is the only field
// the $VCF IDL marks (required), and per schema/VCF/CANONICAL_SERIALIZATION.md
// it is AUTHORITATIVE over the parsed PROPERTIES/ALIASES in every
// disagreement — so a test that proves admission proves it on the card text.
const canonicalTestCard = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"FN:SDN Node\r\n" +
	"EMAIL;TYPE=x-sdn-sign:c2lnbi1rZXk@sign.spacedatanetwork.org\r\n" +
	"END:VCARD\r\n"

// buildVCFProjectionCard writes the smallest LEGAL $VCF record: the one
// (required) field on the root table, plus the conformance facts a serving
// surface budgets against. A FlatBuffers builder refuses to finish a table
// missing a required field, so this constructor is itself a check that the
// vendored VCF binding and the embedded VCF.fbs agree on what "required"
// means.
func buildVCFProjectionCard(t *testing.T) []byte {
	t.Helper()

	b := flatbuffers.NewBuilder(1024)

	digest := sha256.Sum256([]byte(canonicalTestCard))

	version := b.CreateString("3.0")
	cardSHA := b.CreateString(hex.EncodeToString(digest[:]))
	VCF.VCFConformanceStart(b)
	VCF.VCFConformanceAddVERSION(b, version)
	VCF.VCFConformanceAddBYTE_LENGTH(b, uint32(len(canonicalTestCard)))
	VCF.VCFConformanceAddUNFOLDED_BYTE_LENGTH(b, uint32(len(canonicalTestCard)))
	VCF.VCFConformanceAddCARD_SHA256(b, cardSHA)
	VCF.VCFConformanceAddPROPERTY_COUNT(b, 5)
	conformance := VCF.VCFConformanceEnd(b)

	cardID := b.CreateString("vcf-admission-test-card")
	card := b.CreateString(canonicalTestCard)
	mediaType := b.CreateString("text/vcard; charset=utf-8")

	VCF.VCFStart(b)
	VCF.VCFAddCARD_ID(b, cardID)
	VCF.VCFAddCARD(b, card)
	VCF.VCFAddMEDIA_TYPE(b, mediaType)
	VCF.VCFAddCONFORMANCE(b, conformance)
	record := VCF.VCFEnd(b)

	VCF.FinishSizePrefixedVCFBuffer(b, record)
	return b.FinishedBytes()
}

// TestVCFIsAdmittedByTheEmbeddedValidator is the reason the v1.197.0 pin
// exists.
//
// The vCard projection module projects a published $EPM into a $VCF card and
// writes it through the SCHEMA-TYPED storage.write capability. That lane is
// only as good as this node's embedded validator: a standard the validator has
// never loaded is not a standard it can admit, so before this embed the
// projection write failed CLOSED — exactly as $IRM's checkpoint write did
// before v1.196.0.
//
// The host does not field-decode $VCF in Go. It emits vCard TEXT from an EPM
// (internal/epm, internal/auth), but nothing here reads a $VCF FlatBuffer, so
// VCF.fbs sits in unguardedEmbeddedSchemas rather than driftGuardedSchemas.
// This test is the compensating control for that waiver: it exercises the
// embed against the VENDORED binding, so the two authorities cannot silently
// disagree about the record the projection lane depends on.
func TestVCFIsAdmittedByTheEmbeddedValidator(t *testing.T) {
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	ctx := context.Background()

	if !validator.HasSchema("VCF.fbs") {
		t.Fatal("VCF.fbs is not loaded by the validator: a projected card cannot be written through storage.write")
	}

	// storage.write -> FlatSQLStore.storeOne gates the table name through this
	// helper before it touches the stream, so $VCF must resolve to a table.
	table, err := SchemaNameToTable("VCF.fbs")
	if err != nil {
		t.Fatalf("SchemaNameToTable(VCF.fbs): %v", err)
	}
	if table != "VCF" {
		t.Errorf("SchemaNameToTable(VCF.fbs) = %q, want VCF", table)
	}

	record := buildVCFProjectionCard(t)

	if !VCF.SizePrefixedVCFBufferHasIdentifier(record) {
		t.Fatal("built VCF buffer is missing the $VCF file identifier")
	}
	if got, ok := validator.FileIdentifier("VCF.fbs"); !ok || got != "$VCF" {
		t.Errorf("embedded VCF.fbs declares file identifier %q (found=%v), want $VCF", got, ok)
	}

	if err := validator.Validate(ctx, "VCF.fbs", record); err != nil {
		t.Errorf("a real $VCF projection card was refused by the embedded validator: %v", err)
	}

	// A registered schema is not a licence to store arbitrary bytes.
	if err := validator.Validate(ctx, "VCF.fbs", []byte(`{"CARD":"BEGIN:VCARD"}`)); err == nil {
		t.Error("expected a JSON payload published as a $VCF FlatBuffer to be refused")
	}
	if err := validator.Validate(ctx, "VCF.fbs", []byte{}); err == nil {
		t.Error("expected an empty $VCF payload to be refused")
	}

	// Round-trip through the vendored binding. CARD is authoritative over
	// every parsed projection of it, so it must read back OCTET FOR OCTET —
	// terminators included. A card that survives storage with its CRLFs
	// rewritten is a different card with the same digest claim.
	decoded := VCF.GetSizePrefixedRootAsVCF(record, 0)
	if got := string(decoded.CARD()); got != canonicalTestCard {
		t.Errorf("CARD did not round-trip verbatim:\n got %q\nwant %q", got, canonicalTestCard)
	}
	if got := string(decoded.CARD_ID()); got != "vcf-admission-test-card" {
		t.Errorf("CARD_ID mismatch: got %q", got)
	}
	conf := decoded.CONFORMANCE(nil)
	if conf == nil {
		t.Fatal("CONFORMANCE is nil on a record that was built with one")
	}
	if got := string(conf.VERSION()); got != "3.0" {
		t.Errorf("CONFORMANCE.VERSION mismatch: got %q", got)
	}
	if got := conf.BYTE_LENGTH(); got != uint32(len(canonicalTestCard)) {
		t.Errorf("CONFORMANCE.BYTE_LENGTH = %d, want %d", got, len(canonicalTestCard))
	}
	// The digest is what two implementations compare to prove byte-identical
	// projection; if the stored card and the stored digest disagree, the
	// record cannot serve its own purpose.
	wantDigest := sha256.Sum256(decoded.CARD())
	if got := string(conf.CARD_SHA256()); got != hex.EncodeToString(wantDigest[:]) {
		t.Errorf("CONFORMANCE.CARD_SHA256 does not describe the CARD that round-tripped: got %q", got)
	}
}

// TestVCFIsNotOnTheAnonymousDataPlane states the other half of the ruling.
//
// publicReadSchemas is an ALLOW list and it fails closed: embedding a standard
// never makes it anonymously readable. $VCF stays off that list in this cut,
// and deliberately so. A $VCF card is not the profile — it is a PROJECTION
// this node's projector assembled, and the projector decides what the card
// text contains. The published $EPM is already on the anonymous plane and
// remains the authoritative public identity surface; serving a node-generated
// artifact whose contents the source record does not bound is a separate,
// deliberate decision, and adding the one line is what makes it one.
func TestVCFIsNotOnTheAnonymousDataPlane(t *testing.T) {
	if IsPublicReadSchema("VCF.fbs") {
		t.Error("VCF.fbs is on the anonymous public-read allow-list: a projected card is not the published profile")
	}
}

// TestEmbeddedSetIsTheWholePinnedStandardSet states the count outcome this
// task exists to produce, in the terms the pin is written in: every standard
// spacedatastandards.org publishes at v1.197.0 is embedded, and $VCF is in it.
//
// The count constant alone would pass on a set that embedded 225 of the wrong
// files; this also names the standard the bump was for and pins the internal
// (non-SDS) schemas that make the total, so a future bump cannot quietly
// swap one for the other.
func TestEmbeddedSetIsTheWholePinnedStandardSet(t *testing.T) {
	standards := 0
	internal := 0
	seen := make(map[string]bool, len(SupportedSchemas))
	for _, name := range SupportedSchemas {
		if seen[name] {
			t.Fatalf("%s is listed twice in SupportedSchemas", name)
		}
		seen[name] = true
		if internalSchemas[name] {
			internal++
			continue
		}
		standards++
	}
	if standards != 230 {
		t.Errorf("embedded SDS standards = %d, want 230 (spacedatastandards.org v1.207.0)", standards)
	}
	if internal != expectedInternalSchemaCount {
		t.Errorf("embedded internal schemas = %d, want %d", internal, expectedInternalSchemaCount)
	}
	for _, name := range []string{"VCF.fbs", "TXS.fbs", "STX.fbs"} {
		if !seen[name] {
			t.Errorf("%s is absent: the v1.197.0/v1.198.0 bumps exist to embed it", name)
		}
	}

	// The embed is loadable, not merely listed. REC.fbs at this pin includes
	// ../VCF/main.fbs, so a validator that builds without a dangling include
	// is the proof that the closure is complete.
	validator, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("the embedded set does not load: %v", err)
	}
	for _, name := range SupportedSchemas {
		if !validator.HasSchema(name) {
			t.Errorf("%s is supported but not loaded by the validator", name)
		}
	}
	rec, err := schemasFS.ReadFile("schemas/REC.fbs")
	if err != nil {
		t.Fatalf("REC.fbs is not embedded: %v", err)
	}
	for _, inc := range []string{"../VCF/main.fbs", "../TXS/main.fbs", "../STX/main.fbs"} {
		if !strings.Contains(string(rec), inc) {
			t.Errorf("the embedded REC.fbs does not include %s: the aggregate record schema is behind the pin", inc)
		}
	}

	// $STX does not stand alone: STX.fbs includes ../TXS/main.fbs and reuses
	// TXSProvenance, so embedding one without the other is a dangling include
	// rather than a partial feature.
	stx, err := schemasFS.ReadFile("schemas/STX.fbs")
	if err != nil {
		t.Fatalf("STX.fbs is not embedded: %v", err)
	}
	if !strings.Contains(string(stx), "../TXS/main.fbs") {
		t.Error("the embedded STX.fbs does not include ../TXS/main.fbs: the schedule row has lost its facility closure")
	}
}
