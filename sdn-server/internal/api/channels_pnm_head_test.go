package api

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// The $PNM head route is the browser-reachable catch-up token: a consumer
// compares its CID against its own sync point and, when they match, fetches
// zero bytes. It answered 404 for EVERY published channel on prod
// (sdn.spaceaware.io, measured 2026-08-08) because getPNM only ever consulted
// the in-memory registry, which holds bytes solely for publications the current
// PROCESS witnessed. A consumer node that replicated somebody else's
// publication, or any node after a restart, has none.
//
// These tests pin the durable path: publication row -> $PNM record by CID ->
// bytes on the wire, with the envelope bound to the publication the node itself
// recorded.

func pnmHeadTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// seedPublishedChannel writes one $PNM record plus the dataset-publication row
// that names it — exactly the durable state prod carries (host-01: 28
// publication rows, PNM records under sds_p_<peer>__PNM, pin ledger EMPTY).
func seedPublishedChannel(t *testing.T, store *storage.FlatSQLStore, providerID, sourceName, manifestCID string, publishedAt time.Time) ([]byte, string) {
	t.Helper()
	_, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	pnmBytes, err := storage.BuildDatasetPublicationPNM(&storage.DatasetPublicationManifest{
		CID:    manifestCID,
		FileID: "$RFB",
		Path:   "rfb-manifest.dpm",
	}, storage.DatasetPublicationPNMOptions{
		SigningKey:  signingKey,
		PublishedAt: publishedAt,
	})
	if err != nil {
		t.Fatalf("BuildDatasetPublicationPNM failed: %v", err)
	}
	pnmCID, err := store.Store("PNM.fbs", pnmBytes, "16Uiu2HAmPNMHeadFixtureProvider", nil)
	if err != nil {
		t.Fatalf("store PNM failed: %v", err)
	}
	if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
		SchemaName:   "RFB.fbs",
		ProviderID:   providerID,
		SourceName:   sourceName,
		BatchID:      "batch-001",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50000,
		RecordCount:  5289,
		ByteCount:    1048576,
		ShardCID:     "bafkshard-" + manifestCID,
		IndexCID:     "bafkindex-" + manifestCID,
		ManifestCID:  manifestCID,
		PNMCID:       pnmCID,
		FeedHead:     "feed-head-" + manifestCID,
		PublishedAt:  publishedAt,
	}); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}
	return pnmBytes, pnmCID
}

// A brand-new handler over a store that already holds a publication — i.e. a
// restarted node — must serve the head. Before the fix this was a 404, which is
// the defect the live gallery hit.
func TestChannelPNMHeadServedFromDurablePublicationAfterRestart(t *testing.T) {
	t.Parallel()

	store := pnmHeadTestStore(t)
	publishedAt := time.Unix(1_785_896_379, 0).UTC()
	want, wantCID := seedPublishedChannel(t, store, "space-data-network-02", "satnogs-db", "bafkmanifest-rfb-head", publishedAt)

	mux := http.NewServeMux()
	// A handler constructed fresh over the store: its in-memory registry is
	// empty, exactly like a node that just booted.
	NewChannelHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels/satnogs-db-RFB/pnm", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pnm = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); string(got) != string(want) {
		t.Fatalf("served %d PNM bytes, want the %d stored bytes", len(got), len(want))
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/vnd.sdn.pnm" {
		t.Fatalf("Content-Type = %q, want application/vnd.sdn.pnm", contentType)
	}
	// The head token is content-addressed, so an ETag is honest here and lets a
	// consumer skip the body entirely.
	if etag := rec.Header().Get("ETag"); etag != `"bafkmanifest-rfb-head"` {
		t.Fatalf("ETag = %q, want the manifest CID the $PNM names", etag)
	}
	// The bytes must be the record the publication row names, not some other
	// $PNM that happens to be in the store.
	if wantCID == "" {
		t.Fatal("fixture stored no PNM CID")
	}

	// The response is a decodable, structurally verified $PNM envelope — the
	// browser mirror (sdn-js verifySignedPnm) has to be able to read it.
	evidence, err := channels.VerifySignedPNMEnvelope(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("served PNM does not verify structurally: %v", err)
	}
	if evidence.CID != "bafkmanifest-rfb-head" {
		t.Fatalf("PNM CID = %q, want bafkmanifest-rfb-head", evidence.CID)
	}
}

// The channel id a consumer uses addresses the DATA source. On the node that
// published, the source id is the lane's source_name; on a node that replicated
// it, datasetPublicationSourceID prefers the relaying node's provider_id. Both
// must resolve, or the $RFB gallery has to guess (it guessed satnogs-db-RFB,
// satnogs-RFB and celestrak-RFB in turn, all 404).
func TestChannelPNMHeadResolvesBothSourceAndProviderChannelIDs(t *testing.T) {
	t.Parallel()

	store := pnmHeadTestStore(t)
	publishedAt := time.Unix(1_785_896_379, 0).UTC()
	want, _ := seedPublishedChannel(t, store, "space-data-network-02", "satnogs-db", "bafkmanifest-rfb-alias", publishedAt)

	mux := http.NewServeMux()
	NewChannelHandler(store).RegisterRoutes(mux)

	for _, channelID := range []string{"satnogs-db-RFB", "space-data-network-02-RFB"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/channels/"+channelID+"/pnm", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s/pnm = %d, want 200 (body %q)", channelID, rec.Code, rec.Body.String())
		}
		if string(rec.Body.Bytes()) != string(want) {
			t.Fatalf("%s served different bytes than the stored $PNM", channelID)
		}
	}
}

// The head is the NEWEST publication. A consumer comparing CIDs must see the
// head move when something is published, or catch-up never fires.
func TestChannelPNMHeadServesNewestPublication(t *testing.T) {
	t.Parallel()

	store := pnmHeadTestStore(t)
	older := time.Unix(1_785_800_000, 0).UTC()
	newer := time.Unix(1_785_896_379, 0).UTC()
	seedPublishedChannel(t, store, "space-data-network-02", "satnogs-db", "bafkmanifest-rfb-old", older)
	// Two windows of the same lane differ by their window bounds; seed the
	// newer one under a distinct batch so both rows survive the upsert key.
	newestBytes, _ := seedPublishedChannel(t, store, "space-data-network-02", "satnogs-db-newer", "bafkmanifest-rfb-new", newer)

	handler := NewChannelHandler(store)
	parsed, err := channels.ParseChannelID("satnogs-db-newer-RFB")
	if err != nil {
		t.Fatalf("ParseChannelID failed: %v", err)
	}
	metadata, ok := handler.restoreVerifiedPNMFromDatasetPublication(parsed)
	if !ok {
		t.Fatal("restoreVerifiedPNMFromDatasetPublication found no head for the newer publication")
	}
	if metadata.PNMCID != "bafkmanifest-rfb-new" {
		t.Fatalf("head PNM CID = %q, want the newest publication's", metadata.PNMCID)
	}
	if string(metadata.PNMBytes) != string(newestBytes) {
		t.Fatal("head bytes are not the newest publication's $PNM")
	}
}

// FAIL CLOSED: a $PNM whose CID does not name the manifest in the node's own
// publication row is not this channel's head. Without this binding the route
// would serve whatever record sat at the CID the row happens to carry.
func TestChannelPNMHeadRefusesEnvelopeThatDoesNotBindToThePublication(t *testing.T) {
	t.Parallel()

	store := pnmHeadTestStore(t)
	publishedAt := time.Unix(1_785_896_379, 0).UTC()
	_, pnmCID := seedPublishedChannel(t, store, "space-data-network-02", "satnogs-db", "bafkmanifest-rfb-bound", publishedAt)

	// Re-point the publication row at a DIFFERENT manifest while keeping the
	// same $PNM record: the envelope no longer describes this publication.
	if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
		SchemaName:   "RFB.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "satnogs-db",
		BatchID:      "batch-001",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        50000,
		RecordCount:  5289,
		ByteCount:    1048576,
		ShardCID:     "bafkshard-rebound",
		IndexCID:     "bafkindex-rebound",
		ManifestCID:  "bafkmanifest-SOMETHING-ELSE",
		PNMCID:       pnmCID,
		FeedHead:     "feed-head-rebound",
		PublishedAt:  publishedAt,
	}); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}

	mux := http.NewServeMux()
	NewChannelHandler(store).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels/satnogs-db-RFB/pnm", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /pnm = %d, want 404 when the $PNM does not bind to the publication", rec.Code)
	}
}

// A channel with no publication at all still 404s — the fallback must not
// invent a head.
func TestChannelPNMHeadStill404sWithoutAPublication(t *testing.T) {
	t.Parallel()

	store := pnmHeadTestStore(t)
	mux := http.NewServeMux()
	NewChannelHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels/nothing-published-RFB/pnm", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /pnm = %d, want 404 for an unpublished channel", rec.Code)
	}
}

// The collection is the DISCOVERY surface. It returned nothing within 60 s on
// prod because it swept LocalReplicaStats over every supported schema, and
// LocalReplicaStats counts every raw record in the schema. It must answer, and
// the answer must name the channels a head is actually available for.
func TestChannelCollectionListsPublishedChannelsWithoutARecordScan(t *testing.T) {
	t.Parallel()

	store := pnmHeadTestStore(t)
	publishedAt := time.Unix(1_785_896_379, 0).UTC()
	seedPublishedChannel(t, store, "space-data-network-02", "satnogs-db", "bafkmanifest-rfb-list", publishedAt)

	mux := http.NewServeMux()
	NewChannelHandler(store).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels?standardCode=RFB", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	mux.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channels = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"satnogs-db-RFB"`, `"space-data-network-02-RFB"`, `"headAvailable":true`} {
		if !containsString(body, want) {
			t.Fatalf("collection response is missing %s: %s", want, body)
		}
	}
	// Not a benchmark, just a floor: the ledger sweep this replaces took >60 s
	// on prod. Anything in the same order of magnitude is a regression.
	if elapsed > 5*time.Second {
		t.Fatalf("collection took %s, want well under the ledger-sweep cost", elapsed)
	}
}

// ⛔ THE LIST MUST NOT FETCH HEADS. The head fallback reads a record BY CID,
// and prod reads a record by CID in 12-29 s today
// (sdn-record-by-cid-read-12-to-29-seconds). If the collection resolved
// verified metadata per row it would re-hang the route from a different cause,
// so a listing may never populate the registry with PNM bytes.
func TestChannelCollectionDoesNotFetchPNMBytesPerRow(t *testing.T) {
	t.Parallel()

	store := pnmHeadTestStore(t)
	publishedAt := time.Unix(1_785_896_379, 0).UTC()
	seedPublishedChannel(t, store, "space-data-network-02", "satnogs-db", "bafkmanifest-rfb-nofetch", publishedAt)

	handler := NewChannelHandler(store)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels?standardCode=RFB", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channels = %d, want 200", rec.Code)
	}

	// If the listing had gone through channelDetail -> verifiedChannelMetadata,
	// the head restore would have cached PNM bytes in the registry. It must not
	// have: nothing is in there until somebody actually asks for a head.
	for _, metadata := range handler.metadata.List() {
		if len(metadata.PNMBytes) > 0 {
			t.Fatalf("listing fetched $PNM bytes for %s — the collection paid a record-by-CID read per row",
				metadata.ChannelID)
		}
	}

	// And the head route still works right after, which is the point: the list
	// advertises availability, the head route delivers it.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/channels/satnogs-db-RFB/pnm", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pnm after listing = %d, want 200", rec.Code)
	}
}

func containsString(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
