package storage

import (
	"bytes"
	"path/filepath"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/STX"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/TXS"
)

// buildEngineTXS returns an UNPREFIXED $TXS record, the shape
// FlatSQLStore.Store takes. ID, SOURCES and CONSENSUS are (required) on the
// root and PROVIDER_ID/RETRIEVED_AT/LICENSE on every provenance entry — the
// standard's own statement that a merged facility is never unattributed.
func buildEngineTXS(t *testing.T, id, siteName string) []byte {
	t.Helper()

	b := flatbuffers.NewBuilder(1024)

	providerID := b.CreateString("test-rf-register")
	retrievedAt := b.CreateString("2026-08-28T00:00:00.000Z")
	license := b.CreateString("CC-BY-4.0")
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

	idOffset := b.CreateString(id)
	nameOffset := b.CreateString(siteName)
	TXS.TXSStart(b)
	TXS.TXSAddID(b, idOffset)
	TXS.TXSAddSITE_NAME(b, nameOffset)
	TXS.TXSAddSOURCES(b, sources)
	TXS.TXSAddCONSENSUS(b, consensus)
	TXS.FinishSizePrefixedTXSBuffer(b, TXS.TXSEnd(b))

	return b.FinishedBytes()[4:] // strip the 4-byte size prefix
}

// buildEngineSTX returns an UNPREFIXED $STX schedule row carrying the SITE_ID
// join into $TXS.
func buildEngineSTX(t *testing.T, id, siteID string) []byte {
	t.Helper()

	b := flatbuffers.NewBuilder(1024)

	providerID := b.CreateString("test-rf-register")
	retrievedAt := b.CreateString("2026-08-28T00:00:00.000Z")
	license := b.CreateString("CC-BY-4.0")
	STX.TXSProvenanceStart(b)
	STX.TXSProvenanceAddPROVIDER_ID(b, providerID)
	STX.TXSProvenanceAddRETRIEVED_AT(b, retrievedAt)
	STX.TXSProvenanceAddLICENSE(b, license)
	provenance := STX.TXSProvenanceEnd(b)

	STX.STXStartSOURCESVector(b, 1)
	b.PrependUOffsetT(provenance)
	sources := b.EndVector(1)

	idOffset := b.CreateString(id)
	siteOffset := b.CreateString(siteID)
	STX.STXStart(b)
	STX.STXAddID(b, idOffset)
	STX.STXAddSITE_ID(b, siteOffset)
	STX.STXAddSOURCES(b, sources)
	STX.FinishSizePrefixedSTXBuffer(b, STX.STXEnd(b))

	return b.FinishedBytes()[4:]
}

// TestTXSAndSTXAreEngineRoutedLikeEveryOtherStandard is the storage half of
// the v1.198.0 pin, and it names the query the RF catalogue is actually read
// with.
//
// ALL-STANDARDS-ENGINE-ROUTED (owner law 2026-08-25): every embedded standard
// reaches the FlatSQL engine the same way $OMM and $TBS do, through the
// GENERATED catalog — never a per-standard hardcode. engine_standard_catalog.go
// is produced by `go generate ./sdn-server/internal/storage/...` from
// internal/sds/schemas, so embedding TXS.fbs and STX.fbs is what routes them.
// This test states the outcome that mechanism is supposed to produce.
func TestTXSAndSTXAreEngineRoutedLikeEveryOtherStandard(t *testing.T) {
	for _, tc := range []struct{ schema, table, fileID string }{
		{"TXS.fbs", "TXS", "$TXS"},
		{"STX.fbs", "STX", "$STX"},
	} {
		binding, routed := engineRoutedSchemaFor(tc.schema)
		if !routed {
			t.Fatalf("%s is not engine-routed: the generated catalog did not follow the embed", tc.schema)
		}
		if binding.Table != tc.table || binding.FileID != tc.fileID {
			t.Errorf("%s routed as {Table:%q FileID:%q}, want {%q %q}", tc.schema, binding.Table, binding.FileID, tc.table, tc.fileID)
		}
	}

	txsBinding, _ := engineRoutedSchemaFor("TXS.fbs")
	stxBinding, _ := engineRoutedSchemaFor("STX.fbs")

	site := buildEngineTXS(t, "site-001", "First Site")
	site2 := buildEngineTXS(t, "site-002", "Second Site")
	row := buildEngineSTX(t, "sched-001", "site-002")

	// Routing is on the record's OWN identifier: the two standards share an
	// include closure but are NOT interchangeable on the wire.
	if _, _, ok := engineIngestablePayload(stxBinding, site); ok {
		t.Fatal("a $TXS buffer was accepted as an $STX record: the identifier is what the engine routes on")
	}
	if _, _, ok := engineIngestablePayload(txsBinding, row); ok {
		t.Fatal("an $STX buffer was accepted as a $TXS record")
	}

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	for _, rec := range [][]byte{site, site2} {
		if _, err := store.Store("TXS.fbs", rec, "peer", nil); err != nil {
			t.Fatalf("store $TXS through the schema-typed write path: %v", err)
		}
	}
	if _, err := store.Store("STX.fbs", row, "peer", nil); err != nil {
		t.Fatalf("store $STX through the schema-typed write path: %v", err)
	}

	// THE QUERY FORM the RF dataset is read with. No $TXS-shaped SQL anywhere,
	// just the table the generated catalog named.
	res, err := store.engineDB.Query(`SELECT _data FROM TXS ORDER BY _rowid DESC LIMIT ?`, 1)
	if err != nil {
		t.Fatalf("SELECT _data FROM TXS: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("the routed $TXS read returned %d rows, want 1", len(res.Rows))
	}
	data, ok := res.Rows[0][0].([]byte)
	if !ok {
		t.Fatalf("_data came back as %T, want []byte", res.Rows[0][0])
	}
	if !bytes.Equal(data, site2) {
		t.Error("the routed read did not return the last stored $TXS record verbatim")
	}
	decodedSite := TXS.GetRootAsTXS(data, 0)
	if got := string(decodedSite.ID()); got != "site-002" {
		t.Errorf("TXS.ID from the engine = %q, want site-002", got)
	}

	stxRes, err := store.engineDB.Query(`SELECT _data FROM STX ORDER BY _rowid DESC LIMIT ?`, 1)
	if err != nil {
		t.Fatalf("SELECT _data FROM STX: %v", err)
	}
	if len(stxRes.Rows) != 1 {
		t.Fatalf("the routed $STX read returned %d rows, want 1", len(stxRes.Rows))
	}
	stxData, ok := stxRes.Rows[0][0].([]byte)
	if !ok {
		t.Fatalf("$STX _data came back as %T, want []byte", stxRes.Rows[0][0])
	}
	decodedRow := STX.GetRootAsSTX(stxData, 0)

	// The join survives the engine: the schedule row still points at the
	// facility that came back from the TXS table.
	if got := string(decodedRow.SITE_ID()); got != string(decodedSite.ID()) {
		t.Errorf("the $TXS <- $STX join did not survive the engine: STX.SITE_ID = %q, TXS.ID = %q", got, decodedSite.ID())
	}

	if resident := store.engineResidentCount("TXS.fbs"); resident != 2 {
		t.Errorf("engine residency for TXS.fbs = %d, want 2", resident)
	}
	if resident := store.engineResidentCount("STX.fbs"); resident != 1 {
		t.Errorf("engine residency for STX.fbs = %d, want 1", resident)
	}
}
