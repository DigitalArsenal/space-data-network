package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/VCF"
)

// buildEngineVCF returns an UNPREFIXED $VCF projection card, the shape
// FlatSQLStore.Store takes. CARD is the only (required) field on the root
// table and is authoritative over every parsed projection of it, so the card
// text carries the assertion.
func buildEngineVCF(t *testing.T, cardID, fullName string) []byte {
	t.Helper()

	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:" + fullName + "\r\nEND:VCARD\r\n"
	digest := sha256.Sum256([]byte(card))

	b := flatbuffers.NewBuilder(512)

	version := b.CreateString("3.0")
	cardSHA := b.CreateString(hex.EncodeToString(digest[:]))
	VCF.VCFConformanceStart(b)
	VCF.VCFConformanceAddVERSION(b, version)
	VCF.VCFConformanceAddBYTE_LENGTH(b, uint32(len(card)))
	VCF.VCFConformanceAddCARD_SHA256(b, cardSHA)
	conformance := VCF.VCFConformanceEnd(b)

	idOffset := b.CreateString(cardID)
	cardOffset := b.CreateString(card)

	VCF.VCFStart(b)
	VCF.VCFAddCARD_ID(b, idOffset)
	VCF.VCFAddCARD(b, cardOffset)
	VCF.VCFAddCONFORMANCE(b, conformance)
	VCF.FinishSizePrefixedVCFBuffer(b, VCF.VCFEnd(b))

	return b.FinishedBytes()[4:] // strip the 4-byte size prefix
}

// TestVCFIsEngineRoutedLikeEveryOtherStandard is the storage half of the
// v1.197.0 pin, and it is written to fail if $VCF ever becomes a special case.
//
// ALL-STANDARDS-ENGINE-ROUTED (owner law 2026-08-25): every embedded standard
// reaches the FlatSQL engine the same way $OMM and $TBS do, through the
// GENERATED catalog — never a per-standard hardcode. engineGeneratedStandardBindings
// is produced by `go generate ./sdn-server/internal/storage/...` from
// internal/sds/schemas, so embedding VCF.fbs is what routes it; this test
// states the outcome that mechanism is supposed to produce, in the query form
// a caller actually issues.
func TestVCFIsEngineRoutedLikeEveryOtherStandard(t *testing.T) {
	binding, routed := engineRoutedSchemaFor("VCF.fbs")
	if !routed {
		t.Fatal("VCF.fbs is not engine-routed: the generated catalog did not follow the embed")
	}
	if binding.Table != "VCF" {
		t.Errorf("routed table = %q, want VCF", binding.Table)
	}
	if binding.FileID != "$VCF" {
		t.Errorf("routed file identifier = %q, want $VCF", binding.FileID)
	}

	// Routing is on the record's OWN identifier, so a foreign buffer offered
	// for the $VCF table is refused rather than mis-shelved.
	if _, _, ok := engineIngestablePayload(binding, buildEngineOMM(t, 25544, "ISS", 1700000000)); ok {
		t.Fatal("an $OMM buffer was accepted as a $VCF record: the identifier is what the engine routes on")
	}

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	first := buildEngineVCF(t, "card-001", "First Card")
	second := buildEngineVCF(t, "card-002", "Second Card")
	for _, record := range [][]byte{first, second} {
		if _, err := store.Store("VCF.fbs", record, "peer", nil); err != nil {
			t.Fatalf("store $VCF through the schema-typed write path: %v", err)
		}
	}

	// THE QUERY FORM. This is the generic record read every routed standard
	// answers — no $VCF-shaped SQL anywhere, just the table the catalog named.
	res, err := store.engineDB.Query(`SELECT _data FROM VCF ORDER BY _rowid DESC LIMIT ?`, 1)
	if err != nil {
		t.Fatalf("SELECT _data FROM VCF: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("the routed read returned %d rows, want 1", len(res.Rows))
	}
	data, ok := res.Rows[0][0].([]byte)
	if !ok {
		t.Fatalf("_data came back as %T, want []byte", res.Rows[0][0])
	}
	// DESC by rowid means the most recently stored card, byte for byte.
	if !bytes.Equal(data, second) {
		t.Error("the routed read did not return the last stored $VCF record verbatim")
	}
	decoded := VCF.GetRootAsVCF(data, 0)
	if got := string(decoded.CARD_ID()); got != "card-002" {
		t.Errorf("CARD_ID from the engine = %q, want card-002", got)
	}
	if got := string(decoded.CARD()); got != "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Second Card\r\nEND:VCARD\r\n" {
		t.Errorf("CARD did not survive the engine verbatim: got %q", got)
	}

	if resident := store.engineResidentCount("VCF.fbs"); resident != 2 {
		t.Errorf("engine residency for VCF.fbs = %d, want 2", resident)
	}
}
