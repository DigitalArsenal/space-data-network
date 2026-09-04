package storefront

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	sdsdpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	sdsepm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	sdspnm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	libp2p "github.com/libp2p/go-libp2p"
	ps "github.com/libp2p/go-libp2p-pubsub"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func publicationTestService(t *testing.T) (*Service, ed25519.PublicKey) {
	t.Helper()
	store := newTestStoreHelper(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, "12D3KooWPublicationProvider", privateKey, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetProviderEPMCID("bafy-provider-epm")
	service.SetDatasetPublisher(&fakeListingDatasetPublisher{t: t, dir: t.TempDir()})
	return service, publicKey
}

// fakeListingDatasetPublisher exports the shard exactly as the daemon's
// publisher does (a real shard file on disk with its SHA-256 and length) and
// stands in for Kubo with a deterministic CID; the DPM must carry what it
// returns, never a hash of bytes that were never stored.
type fakeListingDatasetPublisher struct {
	t      *testing.T
	dir    string
	assets []*ListingDatasetAsset
}

func (f *fakeListingDatasetPublisher) PublishListingDataset(_ context.Context, listingID, updateID string, filter storage.IndexedRecordQuery, records []storage.DatasetExportRecord) (*ListingDatasetAsset, error) {
	export, err := storage.ExportDatasetRecords(filepath.Join(f.dir, listingID, filter.SchemaName, updateID), filter, records)
	if err != nil {
		return nil, err
	}
	asset := &ListingDatasetAsset{
		Schema: filter.SchemaName, CID: "bafyfake" + export.ShardSHA256[:24], SHA256: export.ShardSHA256,
		ByteLength: uint64(export.ShardBytes), IndexCID: "bafyfakeindex" + export.IndexSHA256[:16], ShardPath: export.ShardPath,
	}
	f.assets = append(f.assets, asset)
	return asset, nil
}

func validPublicationDraft(kind string) ListingDraft {
	return ListingDraft{
		ListingKind: kind, Title: "Canonical " + kind + " listing", Description: "signed listing fixture",
		DataTypes: []string{"OMM"}, PrimaryCategory: "DATA_SOURCES_AND_INGEST",
		Categories: []string{"DATA_SOURCES_AND_INGEST"}, AccessType: "Streaming",
		DeliveryMethods:  []string{"PubSub"},
		Pricing:          []ListingPricingDraft{{Name: "Open", PriceCurrency: "SDN", Features: []string{"verified"}}},
		AcceptedPayments: []string{"Free"}, License: "fixture-license", TermsCID: "bafy-terms",
		Coverage: ListingCoverageDraft{
			Spatial:  ListingSpatialCoverageDraft{Type: "global"},
			Temporal: ListingTemporalCoverageDraft{UpdateFrequency: "hourly"},
		},
		ExpiresAt: uint64(time.Now().Add(24 * time.Hour).Unix()),
	}
}

func TestPublishListingRoundTripDualFormatAndTamper(t *testing.T) {
	service, publicKey := publicationTestService(t)
	for _, kind := range []string{"DataStream", "WasmModule", "Service"} {
		t.Run(kind, func(t *testing.T) {
			draft := validPublicationDraft(kind)
			if kind == "DataStream" {
				draft.SourceConnector = "connector-fixture-01"
			}
			if kind == "WasmModule" {
				draft.PrimaryCategory = "UNSPECIFIED"
				draft.Categories = nil
			}
			report, err := service.PublishListingDraft(context.Background(), draft)
			if err != nil {
				t.Fatalf("PublishListingDraft: %v", err)
			}
			if report.PropagationError == "" || report.AnnouncedToPeers != 0 {
				t.Fatalf("disabled pubsub report = %+v", report)
			}
			publication, err := service.store.GetListingPublication(report.ListingID)
			if err != nil || publication == nil {
				t.Fatalf("GetListingPublication: %v, %+v", err, publication)
			}
			if err := VerifySTFBytes(publication.STFBytes, publicKey); err != nil {
				t.Fatalf("FlatBuffer verification: %v", err)
			}
			if err := VerifyCanonicalSTFJSON(publication.CanonicalJSON, publicKey); err != nil {
				t.Fatalf("canonical JSON verification: %v", err)
			}

			flatTamper := append([]byte(nil), publication.STFBytes...)
			at := bytes.Index(flatTamper, []byte(draft.Title))
			if at < 0 {
				t.Fatal("title not found in STF bytes")
			}
			flatTamper[at] ^= 1
			if err := VerifySTFBytes(flatTamper, publicKey); err == nil {
				t.Fatal("one-byte STF tamper verified")
			}
			jsonTamper := append([]byte(nil), publication.CanonicalJSON...)
			at = bytes.Index(jsonTamper, []byte(draft.Title))
			if at < 0 {
				t.Fatal("title not found in canonical JSON")
			}
			jsonTamper[at] ^= 1
			if err := VerifyCanonicalSTFJSON(jsonTamper, publicKey); err == nil {
				t.Fatal("one-byte canonical JSON tamper verified")
			}

			listing, err := service.store.GetListing(report.ListingID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "Service" && listing.ListingKind != ListingKindService {
				t.Fatalf("service listing kind = %q", listing.ListingKind)
			}
			if kind == "DataStream" && (listing.SourceConnectorID != draft.SourceConnector || !listing.Active) {
				t.Fatalf("connector reference changed listing lifecycle: connector = %q, active = %v", listing.SourceConnectorID, listing.Active)
			}
		})
	}
}

// TestPublishListingRecommendedRetention: a DataStream draft that recommends
// ArchiveAll publishes under both signatures, reads back the word from JSON,
// from the index and from the $STF bytes; a listing under the default rule
// writes no RECOMMENDED_RETENTION key to its canonical JSON (so every
// signature minted before the field existed keeps verifying); an unknown word
// and a non-default rule on a non-data listing are refused.
func TestPublishListingRecommendedRetention(t *testing.T) {
	service, publicKey := publicationTestService(t)

	draft := validPublicationDraft("DataStream")
	draft.RecommendedRetention = "ArchiveAll"
	report, err := service.PublishListingDraft(context.Background(), draft)
	if err != nil {
		t.Fatalf("PublishListingDraft(ArchiveAll): %v", err)
	}
	if report.Listing == nil || report.Listing.RecommendedRetention != "ArchiveAll" {
		t.Fatalf("report listing = %+v", report.Listing)
	}
	publication, err := service.store.GetListingPublication(report.ListingID)
	if err != nil || publication == nil {
		t.Fatalf("GetListingPublication: %v, %+v", err, publication)
	}
	if err := VerifySTFBytes(publication.STFBytes, publicKey); err != nil {
		t.Fatalf("FlatBuffer verification: %v", err)
	}
	if err := VerifyCanonicalSTFJSON(publication.CanonicalJSON, publicKey); err != nil {
		t.Fatalf("canonical JSON verification: %v", err)
	}
	if !bytes.Contains(publication.CanonicalJSON, []byte(`"RECOMMENDED_RETENTION":"ArchiveAll"`)) {
		t.Fatalf("canonical JSON lacks the rule: %s", publication.CanonicalJSON)
	}
	decoded, err := decodeListingRecord(publication.STFBytes)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RecommendedRetention != "ArchiveAll" {
		t.Fatalf("STF bytes RECOMMENDED_RETENTION = %q", decoded.RecommendedRetention)
	}
	stored, err := service.store.GetListing(report.ListingID)
	if err != nil || stored == nil {
		t.Fatalf("GetListing: %v, %+v", err, stored)
	}
	if stored.RecommendedRetention != "ArchiveAll" {
		t.Fatalf("stored RECOMMENDED_RETENTION = %q", stored.RecommendedRetention)
	}
	asJSON, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(asJSON, []byte(`"RECOMMENDED_RETENTION":"ArchiveAll"`)) {
		t.Fatalf("listing JSON lacks the IDL key: %s", asJSON)
	}
	own, err := service.OwnListings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundOwn := false
	for _, item := range own.Listings {
		if item.ListingID == report.ListingID {
			foundOwn = true
			if item.RecommendedRetention != "ArchiveAll" {
				t.Fatalf("own listing RECOMMENDED_RETENTION = %q", item.RecommendedRetention)
			}
		}
	}
	if !foundOwn {
		t.Fatal("own listings omit the published listing")
	}

	// The default rule: no key in the canonical JSON, ReplaceCurrent on read.
	plain, err := service.PublishListingDraft(context.Background(), validPublicationDraft("DataStream"))
	if err != nil {
		t.Fatal(err)
	}
	plainPublication, err := service.store.GetListingPublication(plain.ListingID)
	if err != nil || plainPublication == nil {
		t.Fatalf("GetListingPublication: %v, %+v", err, plainPublication)
	}
	if bytes.Contains(plainPublication.CanonicalJSON, []byte("RECOMMENDED_RETENTION")) {
		t.Fatalf("default rule wrote a RECOMMENDED_RETENTION key: %s", plainPublication.CanonicalJSON)
	}
	if err := VerifyCanonicalSTFJSON(plainPublication.CanonicalJSON, publicKey); err != nil {
		t.Fatalf("default-rule canonical JSON verification: %v", err)
	}
	if err := VerifySTFBytes(plainPublication.STFBytes, publicKey); err != nil {
		t.Fatalf("default-rule FlatBuffer verification: %v", err)
	}
	plainStored, err := service.store.GetListing(plain.ListingID)
	if err != nil || plainStored == nil {
		t.Fatalf("GetListing: %v, %+v", err, plainStored)
	}
	if plainStored.RecommendedRetention != "ReplaceCurrent" {
		t.Fatalf("default rule reads %q, want ReplaceCurrent", plainStored.RecommendedRetention)
	}

	// Refusals.
	bad := validPublicationDraft("DataStream")
	bad.RecommendedRetention = "KeepForever"
	if _, err := service.PublishListingDraft(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "RECOMMENDED_RETENTION must be ReplaceCurrent or ArchiveAll") {
		t.Fatalf("unknown word published: %v", err)
	}
	for _, kind := range []string{"WasmModule", "Service"} {
		wrong := validPublicationDraft(kind)
		wrong.PrimaryCategory = "UNSPECIFIED"
		wrong.Categories = nil
		wrong.RecommendedRetention = "ArchiveAll"
		if _, err := service.PublishListingDraft(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "only valid for DataStream listings") {
			t.Fatalf("%s with ArchiveAll published: %v", kind, err)
		}
		wrong.RecommendedRetention = "ReplaceCurrent"
		if _, err := service.PublishListingDraft(context.Background(), wrong); err != nil {
			t.Fatalf("%s with the default rule refused: %v", kind, err)
		}
	}
}

func TestListingPNMResolvesSTFAndVerifies(t *testing.T) {
	service, publicKey := publicationTestService(t)
	report, err := service.PublishListingDraft(context.Background(), validPublicationDraft("Service"))
	if err != nil {
		t.Fatal(err)
	}
	publication, err := service.store.GetListingPublication(report.ListingID)
	if err != nil {
		t.Fatal(err)
	}
	if !sdspnm.SizePrefixedPNMBufferHasIdentifier(publication.PNMBytes) {
		t.Fatal("PNM is missing $PNM identifier")
	}
	epmPublicKey, err := ed25519SigningKeyFromEPM(publishedListingTestEPM(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyListingPNM(publication.PNMBytes, epmPublicKey); err != nil {
		t.Fatal(err)
	}
	pnm := sdspnm.GetSizePrefixedRootAsPNM(publication.PNMBytes, 0)
	if got := string(pnm.CID()); got != publication.STFCID {
		t.Fatalf("PNM CID = %q, STF CID = %q", got, publication.STFCID)
	}
	resolved, err := service.store.flatStore.Get(SchemaSTF, string(pnm.CID()))
	if err != nil {
		t.Fatalf("resolve PNM CID: %v", err)
	}
	if !bytes.Equal(resolved, publication.STFBytes) {
		t.Fatal("PNM CID did not resolve to exact STF bytes")
	}
	if string(pnm.FILE_ID()) == "" || !strings.Contains(string(pnm.FILE_ID()), ":listing:"+report.ListingID+":") {
		t.Fatalf("unexpected FILE_ID %q", pnm.FILE_ID())
	}
}

func TestListingSigningKeyResolvesFromPublishedEPM(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ed25519SigningKeyFromEPM(publishedListingTestEPM(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resolved, publicKey) {
		t.Fatalf("resolved EPM key = %x, want %x", resolved, publicKey)
	}
}

func publishedListingTestEPM(publicKey ed25519.PublicKey) []byte {
	builder := flatbuffers.NewBuilder(256)
	publicKeyHex := builder.CreateString(hex.EncodeToString(publicKey))
	addressType := builder.CreateString("ed25519")
	sdsepm.CryptoKeyStart(builder)
	sdsepm.CryptoKeyAddPUBLIC_KEY(builder, publicKeyHex)
	sdsepm.CryptoKeyAddKEY_TYPE(builder, sdsepm.KeyTypeSigning)
	sdsepm.CryptoKeyAddADDRESS_TYPE(builder, addressType)
	key := sdsepm.CryptoKeyEnd(builder)
	sdsepm.EPMStartKEYSVector(builder, 1)
	builder.PrependUOffsetT(key)
	keys := builder.EndVector(1)
	sdsepm.EPMStart(builder)
	sdsepm.EPMAddKEYS(builder, keys)
	root := sdsepm.EPMEnd(builder)
	sdsepm.FinishSizePrefixedEPMBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func TestOneTimeDatasetDPMMerkleRootAndFileID(t *testing.T) {
	service, publicKey := publicationTestService(t)
	for i := 0; i < 25; i++ {
		record := sds.NewOMMBuilder().
			WithNoradCatID(uint32(43000 + i)).
			WithObjectID("FIXTURE-" + hex.EncodeToString([]byte{byte(i)})).
			WithEpoch(time.Unix(1700000000+int64(i), 0).UTC().Format(time.RFC3339)).
			Build()
		if _, err := service.store.flatStore.Store("OMM.fbs", record, "fixture-provider", nil); err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
	}
	draft := validPublicationDraft("DataStream")
	draft.AccessType = "OneTime"
	report, err := service.PublishListingDraft(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if report.DPMCID == "" {
		t.Fatal("OneTime listing did not return dpm_cid")
	}
	publication, err := service.store.GetListingPublication(report.ListingID)
	if err != nil {
		t.Fatal(err)
	}
	if !sdsdpm.DPMBufferHasIdentifier(publication.DPMBytes) {
		t.Fatal("DPM is missing $DPM identifier")
	}
	if err := VerifyListingDPM(publication.DPMBytes, publicKey); err != nil {
		t.Fatalf("DPM signature: %v", err)
	}
	dpm := sdsdpm.GetRootAsDPM(publication.DPMBytes, 0)
	if dpm.INDEXESLength() != 2 {
		t.Fatalf("DPM indexes = %d", dpm.INDEXESLength())
	}
	// PUB-03: the DATA_SHARD asset is the shard the publisher pinned — its CID
	// is the one Kubo serves, its SHA-256 and length are those of the shard
	// file on disk — never an in-memory hash nothing stores.
	publisher := service.datasetPublisher.(*fakeListingDatasetPublisher)
	if len(publisher.assets) != 1 {
		t.Fatalf("shards pinned = %d, want 1 (one schema)", len(publisher.assets))
	}
	pinned := publisher.assets[0]
	var shardAsset *sdsdpm.DPMAsset
	for i := 0; i < dpm.ASSETSLength(); i++ {
		var asset sdsdpm.DPMAsset
		if dpm.ASSETS(&asset, i) && asset.ASSET_KIND().String() == "DATA_SHARD" {
			shardAsset = &asset
		}
	}
	if shardAsset == nil {
		t.Fatal("DPM has no DATA_SHARD asset")
	}
	if got := string(shardAsset.CID()); got != pinned.CID {
		t.Fatalf("DATA_SHARD CID = %s, want the pinned shard %s", got, pinned.CID)
	}
	shardBytes, err := os.ReadFile(pinned.ShardPath)
	if err != nil {
		t.Fatalf("pinned shard not on disk: %v", err)
	}
	shardSum := sha256.Sum256(shardBytes)
	if got := string(shardAsset.BYTE_SHA256()); got != hex.EncodeToString(shardSum[:]) {
		t.Fatalf("DATA_SHARD SHA-256 = %s, want the shard file's %s", got, hex.EncodeToString(shardSum[:]))
	}
	if got := shardAsset.BYTE_LENGTH(); got != uint64(len(shardBytes)) {
		t.Fatalf("DATA_SHARD BYTE_LENGTH = %d, want %d", got, len(shardBytes))
	}
	if string(shardAsset.MULTIFORMAT_ADDRESS()) != "/ipfs/"+pinned.CID {
		t.Fatalf("DATA_SHARD address = %s", shardAsset.MULTIFORMAT_ADDRESS())
	}
	indexes := make(map[string]string)
	for i := 0; i < dpm.INDEXESLength(); i++ {
		var index sdsdpm.DPMCompletenessIndex
		dpm.INDEXES(&index, i)
		if string(index.MERKLE_PROFILE()) != ListingMerkleProfile {
			t.Fatalf("MERKLE_PROFILE = %q", index.MERKLE_PROFILE())
		}
		indexes[string(index.INDEX_NAME())] = string(index.INDEX_ROOT())
	}
	stored, err := service.store.flatStore.QueryIndexedRecords(storage.IndexedRecordQuery{
		SchemaName: "OMM.fbs", AllowLargeResultSet: true, OrderByCID: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 25 {
		t.Fatalf("stored OMM records = %d, want 25", len(stored))
	}
	sort.Slice(stored, func(i, j int) bool { return stored[i].CID < stored[j].CID })
	records := make([][]byte, 0, len(stored))
	for _, record := range stored {
		records = append(records, record.Data)
	}
	wantRoot := ComputeListingMerkleRoot(records)
	if got := indexes["record_cid"]; got != wantRoot {
		t.Fatalf("Merkle root = %s, recomputed = %s", got, wantRoot)
	}
	pnm := sdspnm.GetSizePrefixedRootAsPNM(publication.PNMBytes, 0)
	if got, want := string(dpm.FILE_ID()), string(pnm.FILE_ID()); got != want {
		t.Fatalf("DPM FILE_ID = %q, PNM FILE_ID = %q", got, want)
	}
	if !strings.Contains(string(pnm.MULTIFORMAT_ADDRESS()), report.DPMCID) {
		t.Fatal("PNM announcement does not reference DPM CID")
	}
	if got, want := indexes["file_id"], ComputeListingFileIDMerkleRoot(string(pnm.FILE_ID()), records); got != want {
		t.Fatalf("file_id Merkle root = %s, recomputed = %s", got, want)
	}
}

func TestPublishFailureHonestyKeepsLocalListing(t *testing.T) {
	service, _ := publicationTestService(t)
	report, err := service.PublishListingDraft(context.Background(), validPublicationDraft("Service"))
	if err != nil {
		t.Fatal(err)
	}
	if report.AnnouncedToPeers != 0 || strings.TrimSpace(report.PropagationError) == "" {
		t.Fatalf("dishonest propagation report: %+v", report)
	}
	failureReport, _ := json.Marshal(report)
	t.Logf("disabled_pubsub_response=%s", failureReport)
	listing, err := service.store.GetListing(report.ListingID)
	if err != nil || listing == nil {
		t.Fatalf("listing was not stored locally: %v, %+v", err, listing)
	}
}

func TestPublishListingHTTPResponseAndGETSearch(t *testing.T) {
	service, _ := publicationTestService(t)
	handler := NewAPIHandler(service, nil, nil, nil, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, nil)
	draft := validPublicationDraft("Service")
	draft.Title = "HTTP Service Search Fixture"
	body, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/storefront/listings/publish", bytes.NewReader(body))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("publish status = %d, body = %s", response.Code, response.Body.String())
	}
	var report ListingPropagationReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.ListingID == "" || report.STFCID == "" || report.PNMCID == "" || report.PropagationError == "" {
		t.Fatalf("incomplete response: %+v", report)
	}
	search := httptest.NewRequest(http.MethodGet, "/api/storefront/listings/search?q=HTTP+Service+Search+Fixture", nil)
	searchResponse := httptest.NewRecorder()
	mux.ServeHTTP(searchResponse, search)
	if searchResponse.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", searchResponse.Code, searchResponse.Body.String())
	}
	var result SearchResult
	if err := json.Unmarshal(searchResponse.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Listings) != 1 || result.Listings[0].ListingKind != ListingKindService {
		t.Fatalf("search result = %+v", result.Listings)
	}

	browse := httptest.NewRequest(http.MethodGet, "/api/storefront/listings", nil)
	browseResponse := httptest.NewRecorder()
	mux.ServeHTTP(browseResponse, browse)
	if browseResponse.Code != http.StatusOK {
		t.Fatalf("browse status = %d, body = %s", browseResponse.Code, browseResponse.Body.String())
	}
	var browsed SearchResult
	if err := json.Unmarshal(browseResponse.Body.Bytes(), &browsed); err != nil {
		t.Fatal(err)
	}
	if len(browsed.Listings) != 1 || browsed.Listings[0].ListingKind != ListingKindService {
		t.Fatalf("browse result = %+v", browsed.Listings)
	}

	detail := httptest.NewRequest(http.MethodGet, "/api/storefront/listings/"+report.ListingID, nil)
	detailResponse := httptest.NewRecorder()
	mux.ServeHTTP(detailResponse, detail)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", detailResponse.Code, detailResponse.Body.String())
	}
	var detailed Listing
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detailed); err != nil {
		t.Fatal(err)
	}
	if detailed.ListingKind != ListingKindService || detailed.PrimaryCategory != draft.PrimaryCategory {
		t.Fatalf("detail listing = %+v", detailed)
	}
}

func TestTwoNodeServiceListingPropagation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	publisherHost, publisherKey := newListingTestHost(t)
	subscriberHost, subscriberKey := newListingTestHost(t)
	defer publisherHost.Close()
	defer subscriberHost.Close()
	if err := subscriberHost.Connect(ctx, peer.AddrInfo{ID: publisherHost.ID(), Addrs: publisherHost.Addrs()}); err != nil {
		t.Fatalf("connect subscriber: %v", err)
	}
	publisherPubSub, err := ps.NewGossipSub(ctx, publisherHost)
	if err != nil {
		t.Fatal(err)
	}
	subscriberPubSub, err := ps.NewGossipSub(ctx, subscriberHost)
	if err != nil {
		t.Fatal(err)
	}
	publisherService := newListingPeerService(t, publisherHost, publisherKey, publisherPubSub)
	subscriberService := newListingPeerService(t, subscriberHost, subscriberKey, subscriberPubSub)
	defer publisherService.Close()
	defer subscriberService.Close()

	deadline := time.Now().Add(20 * time.Second)
	for (len(publisherService.listingTopic.ListPeers()) == 0 || len(subscriberService.listingTopic.ListPeers()) == 0) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(publisherService.listingTopic.ListPeers()) == 0 {
		t.Fatal("listing pubsub mesh did not form")
	}
	if _, err := listingPublicKeyForPeer(publisherHost.ID()); err != nil {
		t.Fatalf("publisher peer key: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	draft := validPublicationDraft("Service")
	draft.Title = "Two Node Service Fixture"
	report, err := publisherService.PublishListingDraft(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if report.PropagationError != "" || report.AnnouncedToPeers < 1 {
		t.Fatalf("propagation report = %+v", report)
	}
	publishedReport, _ := json.Marshal(report)
	t.Logf("publisher_response=%s", publishedReport)
	subscriberMux := http.NewServeMux()
	NewAPIHandler(subscriberService, nil, nil, nil, nil).RegisterRoutes(subscriberMux, nil)
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "/api/storefront/listings/search?q="+url.QueryEscape(draft.Title), nil)
		response := httptest.NewRecorder()
		subscriberMux.ServeHTTP(response, request)
		var result SearchResult
		err := json.Unmarshal(response.Body.Bytes(), &result)
		if response.Code == http.StatusOK && err == nil && len(result.Listings) == 1 {
			listing := result.Listings[0]
			if listing.SourcePeerID != publisherHost.ID().String() {
				t.Fatalf("source_peer_id = %q, want %q", listing.SourcePeerID, publisherHost.ID())
			}
			if listing.ListingKind != ListingKindService {
				t.Fatalf("listing_kind = %q, want Service", listing.ListingKind)
			}
			t.Logf("subscriber_search_response=%s", strings.TrimSpace(response.Body.String()))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("subscriber did not index Service listing within 30 seconds")
}

func newListingTestHost(t *testing.T) (host.Host, ed25519.PrivateKey) {
	t.Helper()
	privateKey, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := libp2p.New(libp2p.Identity(privateKey), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := privateKey.Raw()
	if err != nil {
		h.Close()
		t.Fatal(err)
	}
	return h, ed25519.PrivateKey(raw)
}

func newListingPeerService(t *testing.T, h host.Host, key ed25519.PrivateKey, pubsub *ps.PubSub) *Service {
	t.Helper()
	store := newTestStoreHelper(t)
	service, err := NewService(store, h.ID().String(), key, nil, pubsub)
	if err != nil {
		t.Fatal(err)
	}
	service.SetProviderEPMCID("bafy-" + h.ID().ShortString())
	return service
}

// Without a publisher (no Kubo) a stored-records listing is refused rather
// than published with a CID no node can fetch.
func TestOneTimeDatasetListingNeedsAPublisher(t *testing.T) {
	service, _ := publicationTestService(t)
	service.SetDatasetPublisher(nil)
	record := sds.NewOMMBuilder().WithNoradCatID(43999).WithObjectID("FIXTURE-NOPIN").WithEpoch(time.Unix(1700000000, 0).UTC().Format(time.RFC3339)).Build()
	if _, err := service.store.flatStore.Store("OMM.fbs", record, "fixture-provider", nil); err != nil {
		t.Fatal(err)
	}
	draft := validPublicationDraft("DataStream")
	draft.AccessType = "OneTime"
	_, err := service.PublishListingDraft(context.Background(), draft)
	if err == nil || !strings.Contains(err.Error(), "Kubo") {
		t.Fatalf("publish without a shard publisher: err = %v, want a refusal naming Kubo", err)
	}
}
