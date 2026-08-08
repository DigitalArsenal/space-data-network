package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// A/B/A on ONE process at ONE pin: what the channel collection costs with the
// pin-ledger sweep reachable versus guarded out.
//
// GET /api/v1/channels returned NOTHING within 60 s on sdn.spaceaware.io
// (measured 2026-08-08). The cost is structural, not incidental:
// restoreVerifiedDatasetPublicationMetadataList calls LocalReplicaStats once
// per supported schema, and LocalReplicaStats ends in CountRawRecords +
// RawRecordHead over the schema's records. Prod carries ~10.8k OMM and 5,289
// RFB records across ~40 schemas.
//
// The guard is exact, not a heuristic: every row that sweep can emit requires a
// VERIFIED pnm-role pin-ledger entry, so when the ledger holds none the sweep is
// provably empty. Run with -v to see the numbers.
func TestChannelCollectionCostWithAndWithoutTheLedgerSweep(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	// Shape-matched to host-01: an EMPTY pin ledger, publications that carry
	// pnm_cid, and enough raw records for the per-schema count to be visible.
	tags := storage.SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		ContentKeyID: "public",
	}
	const records = 2000
	for i := 0; i < records; i++ {
		record := sds.NewOMMBuilder().
			WithNoradCatID(uint32(40000 + i)).
			WithObjectName(fmt.Sprintf("COSTFIXTURE-%d", i)).
			WithEpoch("2026-05-12T00:00:00Z").
			Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:costfixture", nil, tags); err != nil {
			t.Fatalf("store OMM %d failed: %v", i, err)
		}
	}
	if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50000,
		RecordCount:  records,
		ByteCount:    1 << 20,
		ShardCID:     "bafkshard-cost",
		IndexCID:     "bafkindex-cost",
		ManifestCID:  "bafkmanifest-cost",
		PNMCID:       "bafkpnm-cost",
		FeedHead:     "feed-head-cost",
		PublishedAt:  time.Unix(1_785_896_379, 0).UTC(),
	}); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}

	handler := NewChannelHandler(store)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	collection := func() time.Duration {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/channels", nil)
		rec := httptest.NewRecorder()
		start := time.Now()
		mux.ServeHTTP(rec, req)
		elapsed := time.Since(start)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/channels = %d, want 200", rec.Code)
		}
		return elapsed
	}
	// The sweep, measured directly — this is the work the guard removes.
	sweep := func() time.Duration {
		start := time.Now()
		for _, schemaName := range sds.SupportedSchemas {
			if _, err := store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
				SchemaName:   schemaName,
				QueryProfile: storage.DatasetPublicationQueryProfile,
			}); err != nil {
				t.Fatalf("LocalReplicaStats(%s) failed: %v", schemaName, err)
			}
		}
		return time.Since(start)
	}

	// A / B / A on one process at one pin.
	guardedA := collection()
	sweepCost := sweep()
	guardedB := collection()

	t.Logf("A/B/A over %d supported schemas, %d records, EMPTY pin ledger (host-01 shape):",
		len(sds.SupportedSchemas), records)
	t.Logf("  A  guarded collection : %s", guardedA)
	t.Logf("  B  ledger sweep alone : %s", sweepCost)
	t.Logf("  A' guarded collection : %s", guardedB)

	if guardedB > sweepCost {
		t.Fatalf("guarded collection (%s) costs more than the sweep it replaces (%s)", guardedB, sweepCost)
	}
	// The guarded answer must still be a USEFUL one: the published channel is
	// discoverable, which is the whole point of keeping the route alive.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !containsString(rec.Body.String(), `"celestrak-gp-OMM"`) {
		t.Fatalf("guarded collection lost the published channel: %s", rec.Body.String())
	}
}
