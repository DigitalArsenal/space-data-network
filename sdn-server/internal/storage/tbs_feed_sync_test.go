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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/TBS"
	flatbuffers "github.com/google/flatbuffers/go"
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

	// Cache-lane read contract — DOCUMENTED STATE, not a passing read. The
	// aggregate cache lane reads through storage.flatsql_query_stream ->
	// QueryRawStream, whose record surface is the ENGINE's per-schema vtabs
	// (mod-cellular-aggregate-cache cache_plan). Two facts pin the deploy
	// hold this task exists to make precise:
	//
	//   1. The engine record slice routes OMM.fbs ONLY (engine_records.go
	//      engineOMMSchemaName — "the ONLY SDS schema routed into the engine
	//      so far (loop B.3 slice); further standards land with their own
	//      tables"). A $TBS read — whether the legacy `SELECT data FROM
	//      sds_tbs` the cache plan names (a pre-migration BLOB layout modern
	//      stores migrate away) or the modern `SELECT _data FROM TBS` shape
	//      the caching layer will need — currently fails "no such table".
	//   2. The durable hop above (feed-head materialize) and this reader are
	//      different layers: rows now CROSS and LAND (proven above), but no
	//      flow-accessible SQL surface can serve them until the engine gains
	//      the TBS slice (sdn follow-on: TBS record schema/vtab + per-source
	//      shadow + unified view, mirroring the OMM slice incl. hot-window
	//      and boot-rebuild hardening), and host-01's cell_cache_sql is
	//      deployed to the shape that slice defines.
	//
	// This assertion pins fact 1 exactly: it HOLDS until the engine TBS slice
	// lands, at which point this block must be replaced by the positive read
	// (rows/Frames/FrameCount == 3, every frame a size-prefixed $TBS) it
	// blocks today — a test-forced checkout of the slice + cache_sql pair.
	if _, err := consumerStore.QueryRawStream("SELECT _data FROM TBS ORDER BY rowid DESC LIMIT ?", 5); err == nil {
		t.Fatalf("engine serves $TBS already: update this test and the cache lane's cell_cache_sql to the slice-defined read")
	} else if !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("engine $TBS read failed with an unexpected error (update this pin): %v", err)
	}
}
