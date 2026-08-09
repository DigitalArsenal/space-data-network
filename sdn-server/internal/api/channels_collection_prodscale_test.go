package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// PROD-SCALE cost proof for GET /api/v1/channels.
//
// WHY THIS FILE EXISTS. The previous cost test
// (channels_collection_cost_test.go) measured a 2,000-record store, concluded
// the collection route was cheap, and shipped. On host-01 — 1.38 M records, 43
// sources, 28 publication rows, an EMPTY pin ledger — the same route took
// 37–76 s filtered and exceeded 240 s unfiltered. Under EVIDENCE
// ADMISSIBILITY a conclusion drawn at 2 k records does not transfer to 1.38 M,
// and this defect is exactly what that costs. So: this test builds a store at
// the shape the defect was measured at, and it measures ROUND TRIPS, which is
// what actually scales.
//
// WHAT THE DEFECT ACTUALLY WAS. Not the pin-ledger probe. The route issued ONE
// store read PER SUPPORTED SCHEMA (206 of them) to build its discovery rows,
// against a table holding 28 rows in total. The FlatSQL engine is a
// single-threaded WASM runtime: every store call serializes on one lock, and on
// a loaded host-01 ONE such call was measured at 0.5–23 s. 206 x that is the
// 240 s. Row COUNT was never the cost; ROUND TRIPS were.
//
// Scale is settable so the same test can be run at the exact prod shape:
//
//	SDN_CHANNEL_COST_RECORDS=1380000 go test ./internal/api/ \
//	  -run TestChannelCollectionCostAtProdScale -v -timeout 4h
//
// The default is a store large enough to make a per-schema record count
// visible while still running in CI time. The ASSERTIONS are scale-independent:
// they compare the route against the shape it replaced, in ONE process at ONE
// pin, on the SAME store.
func TestChannelCollectionCostAtProdScale(t *testing.T) {
	records := 25_000
	if raw := os.Getenv("SDN_CHANNEL_COST_RECORDS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			t.Fatalf("SDN_CHANNEL_COST_RECORDS=%q is not a positive integer", raw)
		}
		records = parsed
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	// host-01's shape: publications that carry pnm_cid, an EMPTY pin ledger,
	// and enough raw records that a per-schema CountRawRecords is not free.
	tags := storage.SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		ContentKeyID: "public",
	}
	ingestStart := time.Now()
	const chunk = 5_000
	for offset := 0; offset < records; offset += chunk {
		size := chunk
		if remaining := records - offset; remaining < size {
			size = remaining
		}
		batch := make([][]byte, 0, size)
		for i := 0; i < size; i++ {
			batch = append(batch, sds.NewOMMBuilder().
				WithNoradCatID(uint32(40000+offset+i)).
				WithObjectName(fmt.Sprintf("COSTFIXTURE-%d", offset+i)).
				WithEpoch("2026-05-12T00:00:00Z").
				Build())
		}
		if _, err := store.StoreBatchWithSourceTags("OMM.fbs", batch, "source:costfixture", nil, tags); err != nil {
			t.Fatalf("StoreBatchWithSourceTags at offset %d failed: %v", offset, err)
		}
	}
	ingestElapsed := time.Since(ingestStart)

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

	collection := func(query string) (time.Duration, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/channels"+query, nil)
		rec := httptest.NewRecorder()
		start := time.Now()
		mux.ServeHTTP(rec, req)
		elapsed := time.Since(start)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/channels%s = %d, want 200", query, rec.Code)
		}
		return elapsed, rec.Body.String()
	}

	// The shape this route used to have, run against the SAME store in the SAME
	// process: one publication read per supported schema, plus the boolean
	// answered by materializing pin-ledger rows. Nothing here is a simulation —
	// these are the same exported store calls the old code made.
	perSchemaFanOut := func() (time.Duration, int) {
		start := time.Now()
		reads := 0
		if _, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{
			Role:              verifiedPNMPinLedgerRole,
			VerificationState: verifiedPinLedgerState,
		}); err != nil {
			t.Fatalf("ListPinLedgerEntries failed: %v", err)
		}
		reads++
		for _, schemaName := range sds.SupportedSchemas {
			if _, err := channels.StandardCodeFromSchemaName(schemaName); err != nil {
				continue
			}
			if _, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
				SchemaName:   schemaName,
				QueryProfile: storage.DatasetPublicationQueryProfile,
			}); err != nil {
				t.Fatalf("ListDatasetShardPublications(%s) failed: %v", schemaName, err)
			}
			reads++
		}
		return time.Since(start), reads
	}

	// The shape it has now: one publication read for every schema at once, plus
	// a LIMIT 1 existence probe.
	singleRead := func() (time.Duration, int) {
		start := time.Now()
		if _, err := store.HasPinLedgerEntry(storage.PinLedgerQuery{
			Role:              verifiedPNMPinLedgerRole,
			VerificationState: verifiedPinLedgerState,
		}); err != nil {
			t.Fatalf("HasPinLedgerEntry failed: %v", err)
		}
		if _, err := store.ListDatasetShardPublicationsForProfile(storage.DatasetPublicationQueryProfile, ""); err != nil {
			t.Fatalf("ListDatasetShardPublicationsForProfile failed: %v", err)
		}
		return time.Since(start), 2
	}

	// A/B/A on one process at one pin, on one store.
	routeA, bodyA := collection("")
	oldShape, oldReads := perSchemaFanOut()
	newShape, newReads := singleRead()
	routeB, _ := collection("")
	filtered, _ := collection("?standard=OMM")

	t.Logf("PROD-SCALE channel collection cost — %d records, %d supported schemas, EMPTY pin ledger (host-01 shape)",
		records, len(sds.SupportedSchemas))
	t.Logf("  fixture ingest              : %s", ingestElapsed)
	t.Logf("  A  GET /api/v1/channels     : %s", routeA)
	t.Logf("  B  old per-schema fan-out   : %s over %d store reads", oldShape, oldReads)
	t.Logf("  B' new single-read shape    : %s over %d store reads", newShape, newReads)
	t.Logf("  A' GET /api/v1/channels     : %s", routeB)
	t.Logf("  .. GET ?standard=OMM        : %s", filtered)

	// 1. The route must cost the SINGLE-READ shape, not the fan-out shape. The
	//    old route WAS the fan-out, so this is the regression bar: if a future
	//    change reintroduces a per-schema loop, the route's cost lands on the
	//    wrong side of this line.
	if routeB > oldShape {
		t.Fatalf("collection (%s) costs at least the per-schema fan-out it replaced (%s over %d reads) — the round-trip fan-out is back",
			routeB, oldShape, oldReads)
	}
	// 2. And the reduction must be structural, not incidental: 206 reads down
	//    to 2. Anything above a 10x cushion means the loop is still in there
	//    somewhere.
	if oldReads < 10*newReads {
		t.Fatalf("old shape issued %d store reads and the new shape %d — expected the fan-out to be at least 10x", oldReads, newReads)
	}
	if newShape*10 > oldShape {
		t.Fatalf("new shape (%s, %d reads) is not materially cheaper than the fan-out (%s, %d reads)",
			newShape, newReads, oldShape, oldReads)
	}

	// 3. The answer must be UNCHANGED. A fast route that lost the channel is
	//    not a fix — the discovery row plus headAvailable is the whole point.
	var payload struct {
		Count   int `json:"count"`
		Results []struct {
			ChannelID     string `json:"channelId"`
			HeadAvailable bool   `json:"headAvailable"`
			RecordCount   int    `json:"recordCount"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(bodyA), &payload); err != nil {
		t.Fatalf("collection body is not JSON: %v", err)
	}
	found := false
	for _, row := range payload.Results {
		if row.ChannelID != "celestrak-gp-OMM" {
			continue
		}
		found = true
		if !row.HeadAvailable {
			t.Fatalf("celestrak-gp-OMM lost headAvailable: %s", bodyA)
		}
		if row.RecordCount != records {
			t.Fatalf("celestrak-gp-OMM recordCount = %d, want %d", row.RecordCount, records)
		}
	}
	if !found {
		t.Fatalf("collection lost the published channel celestrak-gp-OMM: %s", bodyA)
	}
}

// The pin-ledger probe answers one bit and must never materialize a row to do
// it. Pinned as a PROPERTY, not a timing: HasPinLedgerEntry has to agree with
// ListPinLedgerEntries in both directions, empty and populated, so a future
// "optimisation" cannot quietly make the probe lie.
func TestHasPinLedgerEntryMatchesTheListItReplaces(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	query := storage.PinLedgerQuery{Role: verifiedPNMPinLedgerRole, VerificationState: verifiedPinLedgerState}

	present, err := store.HasPinLedgerEntry(query)
	if err != nil {
		t.Fatalf("HasPinLedgerEntry on empty ledger failed: %v", err)
	}
	if present {
		t.Fatal("HasPinLedgerEntry reported evidence in an empty ledger")
	}
	schemas, err := store.DistinctPinLedgerSchemas(query)
	if err != nil {
		t.Fatalf("DistinctPinLedgerSchemas on empty ledger failed: %v", err)
	}
	if len(schemas) != 0 {
		t.Fatalf("DistinctPinLedgerSchemas on empty ledger = %v, want none", schemas)
	}

	// A row that does NOT match must not flip the bit: the probe is filtering in
	// the engine, not scanning and hoping.
	if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID:               "bafk-unverified",
		SchemaName:        "OMM.fbs",
		Role:              verifiedPNMPinLedgerRole,
		VerificationState: "pending",
		UpdatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertPinLedgerEntry(pending) failed: %v", err)
	}
	present, err = store.HasPinLedgerEntry(query)
	if err != nil {
		t.Fatalf("HasPinLedgerEntry failed: %v", err)
	}
	if present {
		t.Fatal("a pending pnm row was read as VERIFIED evidence")
	}

	if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID:               "bafk-verified",
		SchemaName:        "RFB.fbs",
		Role:              verifiedPNMPinLedgerRole,
		VerificationState: verifiedPinLedgerState,
		UpdatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertPinLedgerEntry(verified) failed: %v", err)
	}
	present, err = store.HasPinLedgerEntry(query)
	if err != nil {
		t.Fatalf("HasPinLedgerEntry failed: %v", err)
	}
	entries, err := store.ListPinLedgerEntries(query)
	if err != nil {
		t.Fatalf("ListPinLedgerEntries failed: %v", err)
	}
	if present != (len(entries) > 0) {
		t.Fatalf("HasPinLedgerEntry = %v but ListPinLedgerEntries returned %d rows", present, len(entries))
	}
	schemas, err = store.DistinctPinLedgerSchemas(query)
	if err != nil {
		t.Fatalf("DistinctPinLedgerSchemas failed: %v", err)
	}
	if len(schemas) != 1 || schemas[0] != "RFB.fbs" {
		t.Fatalf("DistinctPinLedgerSchemas = %v, want [RFB.fbs] — the sweep must only visit schemas the ledger has evidence for", schemas)
	}
}
