package storefront

import (
	"context"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestWizardPublicationRoundTripsAsVerifiedSignedPNM(t *testing.T) {
	service, publicKey := publicationTestService(t)
	storeWizardOMM(t, service.store.FlatStore())
	draft := validPublicationDraft("DataStream")
	draft.AccessType = "OneTime"
	report, err := service.PublishListing(context.Background(), ListingPublicationRequest{
		Listing: draft,
		Dataset: &DatasetSelection{
			SchemaName: "OMM.fbs", ProviderID: "provider-a", SourceName: "catalog-a", BatchID: "batch-a",
		},
		Publication: PublicationOptions{
			AnnounceTo: []string{"storefront"}, PinRecords: true, PinManifest: true, RetentionDays: 30,
		},
	})
	if err != nil {
		t.Fatalf("PublishListing: %v", err)
	}
	if report.STFCID == "" || report.PNMCID == "" || report.DPMCID == "" {
		t.Fatalf("publication CIDs = %+v", report)
	}
	publication, err := service.store.GetListingPublication(report.ListingID)
	if err != nil || publication == nil {
		t.Fatalf("GetListingPublication: %v, %+v", err, publication)
	}
	if err := VerifyListingPNM(publication.PNMBytes, publicKey); err != nil {
		t.Fatalf("VerifyListingPNM: %v", err)
	}
}

func TestWithdrawOwnListingChangesListingState(t *testing.T) {
	service, _ := publicationTestService(t)
	report, err := service.PublishListingDraft(context.Background(), validPublicationDraft("Service"))
	if err != nil {
		t.Fatalf("PublishListingDraft: %v", err)
	}
	withdrawn, err := service.WithdrawOwnListing(context.Background(), report.ListingID)
	if err != nil {
		t.Fatalf("WithdrawOwnListing: %v", err)
	}
	if withdrawn.State != "unpublished" || withdrawn.Active {
		t.Fatalf("withdrawn listing = %+v", withdrawn)
	}
	current, err := service.OwnListings(context.Background())
	if err != nil {
		t.Fatalf("OwnListings: %v", err)
	}
	if len(current.Listings) != 1 || current.Listings[0].State != "unpublished" {
		t.Fatalf("current listings = %+v", current.Listings)
	}
}

func TestPublishableInventoryListsStoredStandard(t *testing.T) {
	service, _ := publicationTestService(t)
	storeWizardOMM(t, service.store.FlatStore())
	inventory, err := service.PublishableInventory(context.Background())
	if err != nil {
		t.Fatalf("PublishableInventory: %v", err)
	}
	if len(inventory.Datasets) != 1 {
		t.Fatalf("inventory = %+v", inventory)
	}
	item := inventory.Datasets[0]
	if item.Standard != "OMM" || item.SchemaName != "OMM.fbs" || item.SourceName != "catalog-a" || item.BatchID != "batch-a" || item.RecordCount != 1 {
		t.Fatalf("inventory item = %+v", item)
	}
}

func storeWizardOMM(t *testing.T, store *storage.FlatSQLStore) {
	t.Helper()
	record := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS").
		WithEpoch("2026-09-03T00:00:00Z").
		Build()
	_, err := store.StoreWithSourceTags("OMM.fbs", record, "peer-a", nil, storage.SourceTags{
		ProviderID: "provider-a", SourceName: "catalog-a", BatchID: "batch-a",
	})
	if err != nil {
		t.Fatalf("StoreWithSourceTags: %v", err)
	}
}
