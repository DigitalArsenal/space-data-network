package storage

// Cellular $TBS topology hop proof (sdn-tbs-feed-sync-for-cache-lane,
// 2026-08-21): the aggregate cache on the consumer node (host-01,
// sdn.spaceaware.io) serves from ITS LOCAL store, but cellular ingest is
// owner-placed on host-02 (celestrak.eth). The mechanism that moves $TBS rows
// between them is the SDN's own data plane — the dataset-feed-head-sync lane:
// host-02's publishing.auto_publish TBS.fbs lane (deployment/celestrak/
// config.yaml) turns each landed cell-tower batch into a dataset publication;
// host-01's feed-head subscription materializes the shard through the EXACT
// store function under test here (ImportDatasetShardFromFiles — what
// materializeDatasetFeedHeadAnnouncement calls after fetching the CID-verified
// shard+index to disk). All records are REAL $TBS FlatBuffers (required
// SOURCES + CONSENSUS), attribution mirrors the ingest module defaults
// (provider "opencellid", source "cell-tower-bulk").

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/TBS"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// newTBSRecord builds one size-prefixed "$TBS" FlatBuffer carrying the two
// required fields (SOURCES with one provenance entry, CONSENSUS) plus the
// identity/position fields the aggregate cache surfaces.
func newTBSRecord(id, providerID string, mcc uint32, lat, lon float64) []byte {
	b := flatbuffers.NewBuilder(1024)

	idOff := b.CreateString(id)
	nativeOff := b.CreateString(id + "@" + providerID)
	providerOff := b.CreateString(providerID)

	TBS.TBSProvenanceStart(b)
	TBS.TBSProvenanceAddProviderId(b, providerOff)
	TBS.TBSProvenanceAddNativeId(b, nativeOff)
	TBS.TBSProvenanceAddReportedLatitude(b, lat)
	TBS.TBSProvenanceAddReportedLongitude(b, lon)
	provenance := TBS.TBSProvenanceEnd(b)

	TBS.TBSStartSOURCESVector(b, 1)
	b.PrependUOffsetT(provenance)
	sources := b.EndVector(1)

	TBS.TBSConsensusStart(b)
	TBS.TBSConsensusAddWinningProviderId(b, providerOff)
	TBS.TBSConsensusAddProvidersConsulted(b, 1)
	TBS.TBSConsensusAddProvidersAgreeing(b, 1)
	consensus := TBS.TBSConsensusEnd(b)

	TBS.TBSStart(b)
	TBS.TBSAddID(b, idOff)
	TBS.TBSAddMCC(b, mcc)
	TBS.TBSAddLATITUDE(b, lat)
	TBS.TBSAddLONGITUDE(b, lon)
	TBS.TBSAddSOURCES(b, sources)
	TBS.TBSAddCONSENSUS(b, consensus)
	tbs := TBS.TBSEnd(b)
	TBS.FinishSizePrefixedTBSBuffer(b, tbs)
	return b.FinishedBytes()
}

func TestTBSFeedSyncMaterializesOnConsumerNode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tbs-feed-sync-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	producerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "host02-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore (ingest host) failed: %v", err)
	}
	defer producerStore.Close()
	consumerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "host01-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore (serving host) failed: %v", err)
	}
	defer consumerStore.Close()

	tags := SourceTags{
		ProviderID:   "opencellid",
		SourceName:   "cell-tower-bulk",
		SourceURL:    "https://bulk.open-cell-id.example/fixture",
		BatchID:      "opencellid@42",
		ContentKeyID: "public",
	}
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		ids[i] = fmt.Sprintf("310-410-42-%d", 1000+i)
		record := newTBSRecord(ids[i], "opencellid", 310, 40.7128+0.01*float64(i), -74.0060)
		if _, err := producerStore.StoreWithSourceTags("TBS.fbs", record, "space-data-network-02", nil, tags); err != nil {
			t.Fatalf("store $TBS record %d on ingest host failed: %v", i, err)
		}
	}

	export, err := producerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "TBS.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}

	// The exact consumer-side call of the feed-head materialize path.
	imported, index, err := consumerStore.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, "space-data-network-02")
	if err != nil {
		t.Fatalf("ImportDatasetShardFromFiles failed: %v", err)
	}
	if imported != 3 || index.RecordCount != 3 {
		t.Fatalf("first import imported=%d recordCount=%d, want 3/3", imported, index.RecordCount)
	}
	// Re-announcing the same feed head (supersede/re-broadcast) is CID-idempotent.
	imported, _, err = consumerStore.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, "space-data-network-02")
	if err != nil {
		t.Fatalf("replay ImportDatasetShardFromFiles failed: %v", err)
	}
	if imported != 0 {
		t.Fatalf("replay imported=%d, want 0 for an already-present immutable shard", imported)
	}

	records, err := consumerStore.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "TBS.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query imported records failed: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("imported records = %d, want 3", len(records))
	}
	gotIDs := make(map[string]bool, len(records))
	for i, r := range records {
		if !TBS.SizePrefixedTBSBufferHasIdentifier(r.Data) {
			t.Fatalf("record %d is not a size-prefixed $TBS frame (%d bytes)", i, len(r.Data))
		}
		decoded := TBS.GetSizePrefixedRootAsTBS(r.Data, 0)
		gotIDs[string(decoded.ID())] = true
		if decoded.SOURCESLength() != 1 {
			t.Fatalf("record %d SOURCES length = %d, want 1 (required field preserved)", i, decoded.SOURCESLength())
		}
		if decoded.CONSENSUS(nil) == nil {
			t.Fatalf("record %d CONSENSUS absent (required field preserved)", i)
		}
	}
	for _, id := range ids {
		if !gotIDs[id] {
			t.Fatalf("imported records missing ID %q (have %v), want all %v", id, gotIDs, ids)
		}
	}

	// Cache-lane read contract — NOW A PASSING READ. The aggregate cache lane
	// reads through storage.flatsql_query_stream -> QueryRawStream, whose
	// record surface is the ENGINE's per-schema vtabs
	// (mod-cellular-aggregate-cache cache_plan). Until the engine TBS record
	// slice landed, this block pinned the deploy hold as an asserted
	// "no such table: TBS"; the slice (engine_records.go engineRoutedSchemas
	// + the TBS table graph + $TBS file-id registration) is what flips it.
	//
	// What is proven here is the WHOLE hop end to end: rows stored on the
	// producer with StoreWithSourceTags, carried by the dataset-feed-head
	// materialize path, are served by the flow-accessible SQL surface on the
	// consumer — the exact read shape host-01's cell_cache_sql must use.
	//
	// THE READ SHAPE IS `_rowid`, NOT `rowid`: the flow-visible relation is
	// the engine's UNIFIED VIEW over the per-source shadow vtabs, and it
	// exposes the ingest sequence as `_rowid` (plus `_data` and `_source`).
	// `rowid` resolves on neither the view nor the vtab, so host-01's
	// cell_cache_sql must be deployed with exactly this statement.
	stream, err := consumerStore.QueryRawStream("SELECT _data FROM TBS ORDER BY _rowid DESC LIMIT ?", 5)
	if err != nil {
		t.Fatalf("engine $TBS read failed after the record slice landed: %v", err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode engine $TBS frames: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("engine $TBS read returned %d frames, want 3", len(frames))
	}
	engineIDs := make(map[string]bool, len(frames))
	for i, frame := range frames {
		// Raw-stream frames are UNPREFIXED buffers (the u32 length is the
		// frame header, not a FlatBuffer size prefix).
		if !TBS.TBSBufferHasIdentifier(frame) {
			t.Fatalf("engine frame %d is not a $TBS buffer (%d bytes)", i, len(frame))
		}
		decoded := TBS.GetRootAsTBS(frame, 0)
		engineIDs[string(decoded.ID())] = true
		// The engine serves the WHOLE record, not the projected columns: the
		// required attribution fields survive the vtab round trip.
		if decoded.SOURCESLength() != 1 {
			t.Fatalf("engine frame %d SOURCES length = %d, want 1", i, decoded.SOURCESLength())
		}
		if decoded.CONSENSUS(nil) == nil {
			t.Fatalf("engine frame %d CONSENSUS absent", i)
		}
	}
	for _, id := range ids {
		if !engineIDs[id] {
			t.Fatalf("engine $TBS read missing ID %q (have %v), want all %v", id, engineIDs, ids)
		}
	}

	// The projected columns the cellular tile contract names
	// (docs/cellular-density-tiles/contract.json pointFields) decode from the
	// vtab itself, not only from _data — a positional vtable slip would show
	// up here as a neighbouring field value.
	cols, err := consumerStore.engineDB.Query(
		`SELECT ID, MCC, LATITUDE, LONGITUDE FROM TBS ORDER BY ID ASC`)
	if err != nil {
		t.Fatalf("engine $TBS column read failed: %v", err)
	}
	if len(cols.Rows) != 3 {
		t.Fatalf("engine $TBS column read returned %d rows, want 3", len(cols.Rows))
	}
	for i, row := range cols.Rows {
		if got, want := row[0].(string), ids[i]; got != want {
			t.Fatalf("row %d ID = %q, want %q", i, got, want)
		}
		if mcc := engineCellInt(t, row[1]); mcc != 310 {
			t.Fatalf("row %d MCC = %v (%T), want 310", i, row[1], row[1])
		}
		wantLat := 40.7128 + 0.01*float64(i)
		if lat := engineCellFloat(t, row[2]); math.Abs(lat-wantLat) > 1e-9 {
			t.Fatalf("row %d LATITUDE = %v (%T), want %v", i, row[2], row[2], wantLat)
		}
		if lon := engineCellFloat(t, row[3]); math.Abs(lon-(-74.0060)) > 1e-9 {
			t.Fatalf("row %d LONGITUDE = %v (%T), want -74.006", i, row[3], row[3])
		}
	}

	// The engine record count is the cache lane's freshness signal (the tile
	// contract's dataset.records): it must report the resident $TBS window,
	// not refuse the schema.
	count, err := consumerStore.EngineRecordCount("TBS.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount(TBS.fbs): %v", err)
	}
	if count != 3 {
		t.Fatalf("EngineRecordCount(TBS.fbs) = %d, want 3", count)
	}
}

// engineCellInt / engineCellFloat normalize the driver-free engine result
// cells (the engine returns SQLite-native widths, which differ by column
// affinity) so a column assertion tests the VALUE, not the Go type.
func engineCellInt(t *testing.T, v interface{}) int64 {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	}
	t.Fatalf("unexpected engine integer cell %v (%T)", v, v)
	return 0
}

func engineCellFloat(t *testing.T, v interface{}) float64 {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	}
	t.Fatalf("unexpected engine float cell %v (%T)", v, v)
	return 0
}
