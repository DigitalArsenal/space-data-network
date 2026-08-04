package storage

import (
	"path/filepath"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/RFB"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// THE DEFECT (sdn-data-index-rfb-norad): the record index projection was
// OMM/CAT-shaped, so every one of the 5,289 SatNOGS $RFB rows indexed with
// norad_cat_id NULL even though the stored bytes carried NORAD_CAT_ID.
// /api/v1/data/index answered "norad": null for all of them, and ?norad=25544 —
// the "which satellites transmit on S-band" query path — matched nothing.

// buildRFB writes a size-prefixed $RFB record the way the SatNOGS parser does:
// one record per LINK_DIRECTION, the transmitter uuid in ID_TRANSMITTER, the
// spacecraft in NORAD_CAT_ID, frequencies already converted to MHz.
func buildRFB(t *testing.T, id, entity, transmitter string, norad uint32, centerFreqMHz float64) []byte {
	t.Helper()
	builder := flatbuffers.NewBuilder(512)
	idOff := builder.CreateString(id)
	entityOff := builder.CreateString(entity)
	transmitterOff := builder.CreateString(transmitter)

	RFB.RFBStart(builder)
	RFB.RFBAddID(builder, idOff)
	RFB.RFBAddID_ENTITY(builder, entityOff)
	RFB.RFBAddCENTER_FREQ(builder, centerFreqMHz)
	if norad > 0 {
		RFB.RFBAddNORAD_CAT_ID(builder, norad)
	}
	RFB.RFBAddID_TRANSMITTER(builder, transmitterOff)
	root := RFB.RFBEnd(builder)
	RFB.FinishSizePrefixedRFBBuffer(builder, root)
	return builder.FinishedBytes()
}

func newRFBTestStore(t *testing.T) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRFBRecordsIndexTheirNORADAndTransmitter(t *testing.T) {
	store := newRFBTestStore(t)
	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "satnogs-db", BatchID: "batch-rfb-1", ContentKeyID: "public"}

	if _, err := store.StoreWithSourceTags("RFB.fbs",
		buildRFB(t, "satnogs:uuid-1:downlink", "ISS", "uuid-1", 25544, 437.550),
		"source:satnogs", nil, tags); err != nil {
		t.Fatalf("store RFB failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("RFB.fbs",
		buildRFB(t, "satnogs:uuid-2:downlink", "NOAA-19", "uuid-2", 33591, 137.100),
		"source:satnogs", nil, tags); err != nil {
		t.Fatalf("store RFB failed: %v", err)
	}
	// An emitter with no NORAD binding (RFB.fbs: "0 when unbound") must index
	// as NULL rather than as object 0.
	if _, err := store.StoreWithSourceTags("RFB.fbs",
		buildRFB(t, "satnogs:uuid-3:downlink", "UNKNOWN", "uuid-3", 0, 2400.000),
		"source:satnogs", nil, tags); err != nil {
		t.Fatalf("store unbound RFB failed: %v", err)
	}

	rows, total, err := store.RecordIndexPage(RecordIndexPageQuery{SchemaName: "RFB.fbs", Limit: 50})
	if err != nil {
		t.Fatalf("RecordIndexPage failed: %v", err)
	}
	if total != 3 {
		t.Fatalf("indexed RFB rows = %d, want 3", total)
	}

	byNorad := map[int64]RecordIndexRow{}
	unbound := 0
	for _, row := range rows {
		if row.NoradCatID == nil {
			unbound++
			continue
		}
		byNorad[*row.NoradCatID] = row
	}
	if unbound != 1 {
		t.Fatalf("rows with a NULL norad = %d, want 1 (the unbound emitter)", unbound)
	}
	iss, ok := byNorad[25544]
	if !ok {
		t.Fatalf("no indexed row for NORAD 25544; got %v", byNorad)
	}
	// $RFB is a band specification, not an observation: a synthesized epoch
	// would make every emitter look like it changed at ingest time.
	if iss.EpochUnix != nil {
		t.Fatalf("epoch = %v, want NULL for a band specification", *iss.EpochUnix)
	}
}

// The S-band query path: ?norad= on the index must actually filter $RFB.
func TestRFBRecordIndexFiltersByNORAD(t *testing.T) {
	store := newRFBTestStore(t)
	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "satnogs-db", BatchID: "batch-rfb-2", ContentKeyID: "public"}

	for _, emitter := range []struct {
		id    string
		uuid  string
		norad uint32
	}{
		{"satnogs:a:downlink", "uuid-a", 25544},
		{"satnogs:a:uplink", "uuid-a", 25544},
		{"satnogs:b:downlink", "uuid-b", 33591},
	} {
		if _, err := store.StoreWithSourceTags("RFB.fbs",
			buildRFB(t, emitter.id, "ENTITY", emitter.uuid, emitter.norad, 2200.0),
			"source:satnogs", nil, tags); err != nil {
			t.Fatalf("store RFB %s failed: %v", emitter.id, err)
		}
	}

	rows, total, err := store.RecordIndexPage(RecordIndexPageQuery{SchemaName: "RFB.fbs", NoradLike: "25544", Limit: 50})
	if err != nil {
		t.Fatalf("RecordIndexPage(norad) failed: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("?norad=25544 matched %d rows (total %d), want 2 — the UPLINK/DOWNLINK pair", len(rows), total)
	}
	for _, row := range rows {
		if row.NoradCatID == nil || *row.NoradCatID != 25544 {
			t.Fatalf("filtered row has norad %v, want 25544", row.NoradCatID)
		}
	}

	// The same two columns drive the indexed RECORD query the storage cap
	// serves to modules: norad_cat_id selects the spacecraft, entity_id selects
	// the transmitter (both halves of one transponder share it).
	norad := uint32(25544)
	byObject, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "RFB.fbs", NoradCatID: &norad, Limit: 50})
	if err != nil {
		t.Fatalf("QueryIndexedRecords(norad) failed: %v", err)
	}
	if len(byObject) != 2 {
		t.Fatalf("norad_cat_id=25544 matched %d records, want 2", len(byObject))
	}
	byTransmitter, err := store.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "RFB.fbs", EntityID: "uuid-a", Limit: 50})
	if err != nil {
		t.Fatalf("QueryIndexedRecords(entity) failed: %v", err)
	}
	if len(byTransmitter) != 2 {
		t.Fatalf("entity_id=uuid-a matched %d records, want the UPLINK/DOWNLINK pair", len(byTransmitter))
	}
}
