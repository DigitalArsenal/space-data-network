package storage

// sdn-tbs-feed-sync-for-cache-lane (2026-08-21): cellular ingest is
// owner-placed on host-02 while the aggregate cache lane reads the consumer
// node's LOCAL store (host-01) — so the $TBS rows must ride the
// dataset-feed-head-sync data plane, and the missing piece was its trigger:
// publishing.auto_publish declared no TBS.fbs lane anywhere in repo (the
// same defect sdn-rfb-publish-to-consumer-node fixed for $RFB — 5,289
// records never replicated because nothing fired a publication). The lane is
// now shipped in deployment/celestrak/config.yaml and pinned by
// TestShippedCelesTrakConfigCarriesTheTBSAutoPublishLane.
//
// THIS test machines the full durable hop that lane triggers: REAL
// size-prefixed $TBS FlatBuffers with the required SOURCES + CONSENSUS
// (built via the vendored TBS binding, TBS.fbs "a site cannot be
// re-serialized unattributed"), stored on a producer store via
// StoreWithSourceTags (the same path the cell-tower ingest flow's
// storage-ingest cap uses), exported as a dataset window, imported on a
// consumer store through the EXACT materialize function the feed-head
// handler calls (ImportDatasetShardFromFiles), then verified three ways:
//
//  1. indexed record query: 3 rows, each decodes as $TBS with SOURCES==1 and
//     CONSENSUS present — attribution survived the hop.
//  2. replay import: CID-idempotent, 0 new rows — the re-announcement /
//     restart-burst case.
//  3. the ENGINE read surface the cache lane queries
//     (`SELECT _data FROM TBS ORDER BY rowid DESC LIMIT ?`) fails
//     `no such table` — the machine-checked statement of the deploy hold,
//     which flips positive when the engine TBS record slice lands
//     (engine_records.go, engineOMMSchemaName: "the ONLY SDS schema routed
//     into the engine so far").

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/TBS"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// producerPeer/consumerPeer are test stand-ins for host-02 (ingest, the
// auto_publish producer) and host-01 (aggregate cache, the feed-head
// consumer). No wallet identity is implied — the store treats them as opaque
// peer labels, exactly like latestTestPeerID.
const (
	tbsProducerPeer = "16Uiu2HAmHost02CellIngest"
	tbsConsumerPeer = "16Uiu2HAmHost01Cache"
)

// buildTBSSite writes ONE size-prefixed $TBS FlatBuffer the way the
// cell-tower ingest flow's parser does: ID from the mobile-network
// addressing, the reconciled position in LATITUDE/LONGITUDE, and the REQUIRED
// attribution pair — a single SOURCES entry (the bulk source) plus the
// CONSENSUS that reduced it (SINGLE_SOURCE: the published position rests on
// one report, never independently corroborated).
func buildTBSSite(t *testing.T, id string, mcc, mnc uint32, cellID, lat, lon float64, siteName string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(1024)

	idOff := b.CreateString(id)
	nativeIDOff := b.CreateString("opencellid:" + id)
	cellIDOff := b.CreateString(fmt.Sprintf("%.0f", cellID))
	providerIDOff := b.CreateString("opencellid")
	sourceURLOff := b.CreateString("https://fixture.test/opencellid/export.csv.gz")
	queryOff := b.CreateString("bbox=lat,lon,radius&format=csv")
	retrievedAtOff := b.CreateString("2026-08-21T00:00:00Z")
	licenseOff := b.CreateString("ODbL-1.0")
	licenseURLOff := b.CreateString("https://opendatacommons.org/licenses/odbl/1-0/")
	attributionOff := b.CreateString("(c) OpenCellID contributors")
	firstObservedOff := b.CreateString("2026-08-01T00:00:00Z")
	lastObservedOff := b.CreateString("2026-08-20T00:00:00Z")
	operatorOff := b.CreateString("FIXTURE-MNO")
	siteNameOff := b.CreateString(siteName)
	countryOff := b.CreateString("US")

	TBS.TBSProvenanceStart(b)
	TBS.TBSProvenanceAddPROVIDER_ID(b, providerIDOff)
	TBS.TBSProvenanceAddAUTHORITY(b, providerIDOff)
	TBS.TBSProvenanceAddSOURCE_URL(b, sourceURLOff)
	TBS.TBSProvenanceAddSOURCE_QUERY(b, queryOff)
	TBS.TBSProvenanceAddRETRIEVED_AT(b, retrievedAtOff)
	TBS.TBSProvenanceAddLICENSE(b, licenseOff)
	TBS.TBSProvenanceAddLICENSE_URL(b, licenseURLOff)
	TBS.TBSProvenanceAddATTRIBUTION(b, attributionOff)
	TBS.TBSProvenanceAddNATIVE_ID(b, nativeIDOff)
	TBS.TBSProvenanceAddREPORTED_LATITUDE(b, lat)
	TBS.TBSProvenanceAddREPORTED_LONGITUDE(b, lon)
	TBS.TBSProvenanceAddCONTRIBUTED(b, true)
	source0 := TBS.TBSProvenanceEnd(b)

	TBS.TBSStartSOURCESVector(b, 1)
	b.PrependUOffsetT(source0)
	sourcesVec := b.EndVector(1)

	winningProviderOff := b.CreateString("opencellid")
	mergedAtOff := b.CreateString("2026-08-21T01:00:00Z")
	TBS.TBSConsensusStart(b)
	TBS.TBSConsensusAddMETHOD(b, TBS.EnumValuestbsMergeMethod["SINGLE_SOURCE"])
	TBS.TBSConsensusAddPROVIDERS_CONSULTED(b, 1)
	TBS.TBSConsensusAddPROVIDERS_AGREEING(b, 1)
	TBS.TBSConsensusAddWINNING_PROVIDER_ID(b, winningProviderOff)
	TBS.TBSConsensusAddPOSITION_SPREAD_M(b, 0)
	TBS.TBSConsensusAddCONFIDENCE(b, 0.9)
	TBS.TBSConsensusAddMERGED_AT(b, mergedAtOff)
	consensusOff := TBS.TBSConsensusEnd(b)

	TBS.TBSStart(b)
	TBS.TBSAddID(b, idOff)
	TBS.TBSAddNATIVE_ID(b, nativeIDOff)
	TBS.TBSAddRADIO(b, TBS.EnumValuestbsRadioClass["LTE"])
	TBS.TBSAddMCC(b, mcc)
	TBS.TBSAddMNC(b, mnc)
	TBS.TBSAddCELL_ID(b, cellIDOff)
	TBS.TBSAddLATITUDE(b, lat)
	TBS.TBSAddLONGITUDE(b, lon)
	TBS.TBSAddSAMPLES(b, 137)
	TBS.TBSAddAVERAGE_SIGNAL_DBM(b, -78.5)
	TBS.TBSAddFIRST_OBSERVED(b, firstObservedOff)
	TBS.TBSAddLAST_OBSERVED(b, lastObservedOff)
	TBS.TBSAddOPERATOR(b, operatorOff)
	TBS.TBSAddFREQUENCY_MHZ(b, 869.0)
	TBS.TBSAddSITE_NAME(b, siteNameOff)
	TBS.TBSAddCOUNTRY_CODE(b, countryOff)
	TBS.TBSAddSOURCES(b, sourcesVec)
	TBS.TBSAddCONSENSUS(b, consensusOff)
	root := TBS.TBSEnd(b)
	TBS.FinishSizePrefixedTBSBuffer(b, root)
	return b.FinishedBytes()
}

func TestTBSFeedSyncMaterializesOnConsumerNode(t *testing.T) {
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	producer, err := NewFlatSQLStore(filepath.Join(tmpDir, "producer"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore producer failed: %v", err)
	}
	defer producer.Close()
	consumer, err := NewFlatSQLStore(filepath.Join(tmpDir, "consumer"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore consumer failed: %v", err)
	}
	defer consumer.Close()

	// The lane's source tags (deployment/celestrak/config.yaml auto_publish
	// lane: source_name cell-tower-bulk) match the cell-tower ingest flow's
	// attribution defaults: provider opencellid, source cell-tower-bulk.
	tags := SourceTags{
		ProviderID:   "opencellid",
		SourceName:   "cell-tower-bulk",
		SourceURL:    "https://fixture.test/opencellid/export.csv.gz",
		BatchID:      "cell-tower-bulk-2026-08-21",
		ContentKeyID: "public",
	}

	sites := []struct {
		id       string
		mcc, mnc uint32
		cellID   float64
		lat, lon float64
		name     string
	}{
		{"310-410-1234-5678", 310, 410, 5678, 40.7128, -74.0060, "WTC-CELL"},
		{"310-260-2345-6789", 310, 260, 6789, 34.0522, -118.2437, "LAX-TOWER"},
		{"311-480-3456-7890", 311, 480, 7890, 51.5074, -0.1278, "LHR-ROOFTOP"},
	}
	for i, s := range sites {
		if _, err := producer.StoreWithSourceTags("TBS.fbs",
			buildTBSSite(t, s.id, s.mcc, s.mnc, s.cellID, s.lat, s.lon, s.name),
			tbsProducerPeer, nil, tags); err != nil {
			t.Fatalf("store TBS site %d (%s): %v", i, s.id, err)
		}
	}

	// Producer exports the batch as a shard window; the consumer materializes
	// it through the exact function the feed-head handler calls.
	export, err := producer.ExportDatasetWindow(filepath.Join(tmpDir, "export-tbs"), IndexedRecordQuery{
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

	imported, index, err := consumer.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, tbsConsumerPeer)
	if err != nil {
		t.Fatalf("ImportDatasetShardFromFiles failed: %v", err)
	}
	if imported != 3 || index.RecordCount != 3 {
		t.Fatalf("imported=%d recordCount=%d, want 3/3", imported, index.RecordCount)
	}

	// The durable hop: every imported row decodes as $TBS with the required
	// attribution pair intact (SOURCES==1, CONSENSUS present).
	records, err := consumer.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "TBS.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query imported TBS records failed: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("imported TBS records = %d, want 3", len(records))
	}
	for i, rec := range records {
		if !TBS.SizePrefixedTBSBufferHasIdentifier(rec.Data) {
			t.Fatalf("record %d is not a size-prefixed $TBS buffer", i)
		}
		site := TBS.GetSizePrefixedRootAsTBS(rec.Data, 0)
		if len(site.ID()) == 0 {
			t.Fatalf("record %d lost its TBS.ID (required by TBS.fbs)", i)
		}
		if site.SOURCESLength() != 1 {
			t.Fatalf("record %d SOURCESLength = %d, want 1 (attribution must survive the hop)", i, site.SOURCESLength())
		}
		var prov TBS.TBSProvenance
		if !site.SOURCES(&prov, 0) {
			t.Fatalf("record %d SOURCES[0] unreadable", i)
		}
		if string(prov.PROVIDER_ID()) != "opencellid" {
			t.Fatalf("record %d SOURCES[0].PROVIDER_ID = %q, want opencellid", i, prov.PROVIDER_ID())
		}
		if site.CONSENSUS(nil) == nil {
			t.Fatalf("record %d lost CONSENSUS (required by TBS.fbs)", i)
		}
		if site.LATITUDE() == 0 || site.LONGITUDE() == 0 {
			t.Fatalf("record %d lost its reconciled position (%f, %f)", i, site.LATITUDE(), site.LONGITUDE())
		}
	}

	// Re-announcement (restart burst): the same shard re-imported is
	// CID-idempotent — 0 new rows, matching the RFB replay path.
	imported, _, err = consumer.ImportDatasetShardFromFiles(export.ShardPath, export.IndexPath, tbsConsumerPeer)
	if err != nil {
		t.Fatalf("replay ImportDatasetShardFromFiles failed: %v", err)
	}
	if imported != 0 {
		t.Fatalf("replay imported=%d, want 0 for an already-present immutable shard", imported)
	}

	// THE HOLD, machine-pinned: the cache lane reads via
	// storage.flatsql_query_stream -> store.QueryRawStream, the ENGINE's
	// per-schema record surface. The engine routes OMM.fbs ONLY today
	// (engine_records.go, "the ONLY SDS schema routed into the engine so
	// far"), so the cache lane's modern read shape fails `no such table: TBS`.
	// Until the engine TBS record slice lands (registered follow-on), the
	// cache flow must stay HELD: an instant-but-empty or `no such table`
	// answer is not shippable. This assertion is what makes the hold
	// machine-checked rather than asserted — it flips positive the moment the
	// slice lands.
	stream, err := consumer.QueryRawStream("SELECT _data FROM TBS ORDER BY rowid DESC LIMIT ?", 3)
	if err == nil {
		t.Fatalf("engine TBS record slice exists already: raw stream of %d bytes — the deploy hold must be re-assessed", len(stream.Bytes))
	}
	if !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("engine query against the missing TBS surface returned %v, want a 'no such table' error", err)
	}
}
