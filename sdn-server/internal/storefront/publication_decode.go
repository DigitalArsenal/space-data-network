package storefront

import (
	"errors"
	"fmt"
	"time"

	sdsstf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STF"
)

func decodeListingRecord(data []byte) (listing *Listing, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			listing = nil
			err = fmt.Errorf("malformed STF FlatBuffer: %v", recovered)
		}
	}()
	if !sdsstf.STFBufferHasIdentifier(data) {
		return nil, errors.New("STF buffer missing $STF file identifier")
	}
	record := sdsstf.GetRootAsSTF(data, 0)
	listing = &Listing{
		ListingID:          string(record.LISTING_ID()),
		ProviderPeerID:     string(record.PROVIDER_PEER_ID()),
		ProviderEPMCID:     string(record.PROVIDER_EPM_CID()),
		Title:              string(record.TITLE()),
		Description:        string(record.DESCRIPTION()),
		SampleCID:          string(record.SAMPLE_CID()),
		AccessType:         AccessType(int(record.ACCESS_TYPE())),
		EncryptionRequired: record.ENCRYPTION_REQUIRED(),
		CreatedAt:          unixTime(record.CREATED_AT()),
		UpdatedAt:          unixTime(record.UPDATED_AT()),
		Active:             record.ACTIVE(),
		Signature:          append([]byte(nil), record.SIGNATUREBytes()...),
		ListingKind:        listingKindFromSDS(record.LISTING_KIND().String()),
		SampleRecordCount:  record.SAMPLE_RECORD_COUNT(),
		Version:            record.VERSION(),
		ExpiresAt:          unixTime(record.EXPIRES_AT()),
		TermsCID:           string(record.TERMS_CID()),
		License:            string(record.LICENSE()),
		SourcePeerID:       string(record.SOURCE_PEER_ID()),
		PrimaryCategory:    record.PRIMARY_CATEGORY().String(),
	}
	for i := 0; i < record.DATA_TYPESLength(); i++ {
		listing.DataTypes = append(listing.DataTypes, string(record.DATA_TYPES(i)))
	}
	for i := 0; i < record.TAGSLength(); i++ {
		listing.Tags = append(listing.Tags, string(record.TAGS(i)))
	}
	for i := 0; i < record.DELIVERY_METHODSLength(); i++ {
		listing.DeliveryMethods = append(listing.DeliveryMethods, string(record.DELIVERY_METHODS(i)))
	}
	for i := 0; i < record.ACCEPTED_PAYMENTSLength(); i++ {
		listing.AcceptedPayments = append(listing.AcceptedPayments, PaymentMethod(int(record.ACCEPTED_PAYMENTS(i))))
	}
	for i := 0; i < record.CATEGORIESLength(); i++ {
		listing.Categories = append(listing.Categories, record.CATEGORIES(i).String())
	}
	if coverage := record.COVERAGE(nil); coverage != nil {
		if spatial := coverage.SPATIAL(nil); spatial != nil {
			listing.Coverage.Spatial = SpatialCoverage{
				Type: string(spatial.TYPE()), MinAltitudeKm: spatial.MIN_ALTITUDE_KM(),
				MaxAltitudeKm: spatial.MAX_ALTITUDE_KM(),
			}
			for i := 0; i < spatial.REGIONSLength(); i++ {
				listing.Coverage.Spatial.Regions = append(listing.Coverage.Spatial.Regions, string(spatial.REGIONS(i)))
			}
			for i := 0; i < spatial.OBJECT_IDSLength(); i++ {
				listing.Coverage.Spatial.ObjectIDs = append(listing.Coverage.Spatial.ObjectIDs, string(spatial.OBJECT_IDS(i)))
			}
			for i := 0; i < spatial.GEO_BOUNDSLength(); i++ {
				listing.Coverage.Spatial.GeoBounds = append(listing.Coverage.Spatial.GeoBounds, spatial.GEO_BOUNDS(i))
			}
		}
		if temporal := coverage.TEMPORAL(nil); temporal != nil {
			listing.Coverage.Temporal = TemporalCoverage{
				StartEpoch: string(temporal.START_EPOCH()), EndEpoch: string(temporal.END_EPOCH()),
				UpdateFrequency:     string(temporal.UPDATE_FREQUENCY()),
				HistoricalDepthDays: temporal.HISTORICAL_DEPTH(), LatencySeconds: temporal.LATENCY_SECONDS(),
			}
		}
	}
	for i := 0; i < record.PRICINGLength(); i++ {
		var tier sdsstf.PricingTier
		if !record.PRICING(&tier, i) {
			continue
		}
		decoded := PricingTier{
			Name: string(tier.NAME()), PriceAmount: tier.PRICE_AMOUNT(), PriceCurrency: string(tier.PRICE_CURRENCY()),
			DurationDays: tier.DURATION_DAYS(), RateLimit: tier.RATE_LIMIT(),
			MaxRecordsPerRequest: tier.MAX_RECORDS_PER_REQUEST(), Description: string(tier.DESCRIPTION()),
		}
		for j := 0; j < tier.FEATURESLength(); j++ {
			decoded.Features = append(decoded.Features, string(tier.FEATURES(j)))
		}
		listing.Pricing = append(listing.Pricing, decoded)
	}
	if protected := record.PROTECTED_DELIVERY(nil); protected != nil {
		listing.ProtectedDelivery = ProtectedDelivery{
			EncryptedCID: string(protected.ENCRYPTED_CID()), ManifestCID: string(protected.MANIFEST_CID()),
			ContentHash: string(protected.CONTENT_HASH()), ContentKeyID: string(protected.CONTENT_KEY_ID()),
			LicenseModuleID: string(protected.LICENSE_MODULE_ID()), ModuleID: string(protected.MODULE_ID()),
			ModuleVersion: string(protected.MODULE_VERSION()), GrantScope: string(protected.GRANT_SCOPE()),
			DeliveryProtocol: string(protected.DELIVERY_PROTOCOL()),
		}
		for i := 0; i < protected.REQUIRED_SCOPESLength(); i++ {
			listing.ProtectedDelivery.RequiredScopes = append(listing.ProtectedDelivery.RequiredScopes, string(protected.REQUIRED_SCOPES(i)))
		}
		if policy := protected.FIELD_STREAM_POLICY(nil); policy != nil {
			decoded := &GrantFieldStreamPolicy{
				PolicyID: string(policy.POLICY_ID()), PolicyVersion: policy.POLICY_VERSION(),
				StreamID: string(policy.STREAM_ID()), SchemaCode: string(policy.SCHEMA_CODE()),
				KeyEpoch: string(policy.KEY_EPOCH()), GrantScope: string(policy.GRANT_SCOPE()),
			}
			for i := 0; i < policy.ALLOWED_FIELD_PATHSLength(); i++ {
				decoded.AllowedFieldPaths = append(decoded.AllowedFieldPaths, string(policy.ALLOWED_FIELD_PATHS(i)))
			}
			for i := 0; i < policy.REDACTED_FIELD_PATHSLength(); i++ {
				decoded.RedactedFieldPaths = append(decoded.RedactedFieldPaths, string(policy.REDACTED_FIELD_PATHS(i)))
			}
			for i := 0; i < policy.ALLOWED_OPERATIONSLength(); i++ {
				decoded.AllowedOperations = append(decoded.AllowedOperations, string(policy.ALLOWED_OPERATIONS(i)))
			}
			listing.ProtectedDelivery.FieldStreamPolicy = decoded
		}
	}
	if reputation := record.REPUTATION(nil); reputation != nil {
		listing.Reputation = ProviderReputation{
			TotalSales: reputation.TOTAL_SALES(), AverageRatingX10: reputation.AVERAGE_RATING_X10(),
			TotalRatings: reputation.TOTAL_RATINGS(), UptimePercentageX100: reputation.UPTIME_PERCENTAGE_X100(),
			AvgDeliveryLatencyMs: reputation.AVG_DELIVERY_LATENCY_MS(), DisputeCount: reputation.DISPUTE_COUNT(),
			ProviderSince: reputation.PROVIDER_SINCE(),
		}
	}
	return listing, nil
}

func unixTime(seconds uint64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(int64(seconds), 0).UTC()
}
