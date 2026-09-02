package storefront

import (
	"fmt"
	"strings"
	"time"

	sdsacl "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ACL"
	sdspur "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PUR"
	sdsrev "github.com/DigitalArsenal/spacedatastandards.org/lib/go/REV"
	sdsstf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/cct"
)

func encodeStorefrontRecord(schemaName string, data interface{}, moduleCategory func(string) string) ([]byte, error) {
	switch schemaName {
	case SchemaSTF:
		listing, ok := listingRecord(data)
		if !ok {
			return nil, fmt.Errorf("%s expects Listing, got %T", schemaName, data)
		}
		return encodeListingRecord(listing, moduleCategory), nil
	case SchemaACL:
		grant, ok := accessGrantRecord(data)
		if !ok {
			return nil, fmt.Errorf("%s expects AccessGrant, got %T", schemaName, data)
		}
		return encodeAccessGrantRecord(grant), nil
	case SchemaPUR:
		req, ok := purchaseRequestRecord(data)
		if !ok {
			return nil, fmt.Errorf("%s expects PurchaseRequest, got %T", schemaName, data)
		}
		return encodePurchaseRequestRecord(req), nil
	case SchemaREV:
		review, ok := reviewRecord(data)
		if !ok {
			return nil, fmt.Errorf("%s expects Review, got %T", schemaName, data)
		}
		return encodeReviewRecord(review), nil
	default:
		return nil, fmt.Errorf("unsupported storefront schema %s", schemaName)
	}
}

func listingRecord(data interface{}) (*Listing, bool) {
	switch v := data.(type) {
	case Listing:
		return &v, true
	case *Listing:
		return v, v != nil
	default:
		return nil, false
	}
}

func accessGrantRecord(data interface{}) (*AccessGrant, bool) {
	switch v := data.(type) {
	case AccessGrant:
		return &v, true
	case *AccessGrant:
		return v, v != nil
	default:
		return nil, false
	}
}

func purchaseRequestRecord(data interface{}) (*PurchaseRequest, bool) {
	switch v := data.(type) {
	case PurchaseRequest:
		return &v, true
	case *PurchaseRequest:
		return v, v != nil
	default:
		return nil, false
	}
}

func reviewRecord(data interface{}) (*Review, bool) {
	switch v := data.(type) {
	case Review:
		return &v, true
	case *Review:
		return v, v != nil
	default:
		return nil, false
	}
}

// listingPrimaryCategory resolves the $CCT capabilityClass a listing shelves
// under.
//
// Before SDS v1.186.0 an $STF carried no capability category at all — only
// DATA_TYPES and TAGS — so a storefront shelf and a library shelf were grouped
// by two unrelated systems. $STF now forbids re-deriving a category from
// DATA_TYPES, TAGS or TITLE, none of which are a controlled vocabulary, which
// leaves exactly one honest source: the module behind a module listing.
//
// A data-stream listing therefore resolves to UNSPECIFIED, and so does a module
// listing whose module the catalog never categorized. That is the correct
// answer, not a gap to paper over: PRIMARY_CATEGORY says WHAT THE UNIT DOES,
// and nothing in a data-stream listing states it. LISTING_KIND (delivery kind)
// and ACCESS_TYPE (commercial model) already carry what those listings do know,
// and neither is a substitute.
func listingPrimaryCategory(listing *Listing, moduleCategory func(string) string) string {
	if listing.ListingKind != ListingKindWASMModule {
		if category := strings.TrimSpace(listing.PrimaryCategory); category != "" {
			if _, ok := sdsstf.EnumValuescapabilityClass[category]; ok {
				return category
			}
		}
		return cct.Unspecified
	}
	if moduleCategory == nil {
		return cct.Unspecified
	}
	moduleID := strings.TrimSpace(listing.ProtectedDelivery.ModuleID)
	if moduleID == "" {
		return cct.Unspecified
	}
	if class := strings.TrimSpace(moduleCategory(moduleID)); class != "" {
		return class
	}
	return cct.Unspecified
}

func encodeListingRecord(listing *Listing, moduleCategory func(string) string) []byte {
	builder := flatbuffers.NewBuilder(2048)

	listingID := stringOffset(builder, listing.ListingID)
	providerPeerID := stringOffset(builder, listing.ProviderPeerID)
	providerEPMCID := stringOffset(builder, listing.ProviderEPMCID)
	title := stringOffset(builder, listing.Title)
	description := stringOffset(builder, listing.Description)
	dataTypes := fbStringVector(builder, listing.DataTypes, sdsstf.STFStartDATA_TYPESVector)
	tags := fbStringVector(builder, listing.Tags, sdsstf.STFStartTAGSVector)
	coverage := buildSTFCoverage(builder, listing.Coverage)
	sampleCID := stringOffset(builder, listing.SampleCID)
	deliveryMethods := fbStringVector(builder, listing.DeliveryMethods, sdsstf.STFStartDELIVERY_METHODSVector)
	protectedDelivery := buildSTFProtectedDelivery(builder, listing.ProtectedDelivery)
	pricing := buildSTFPricingVector(builder, listing.Pricing)
	acceptedPayments := fbPaymentMethodVector(builder, listing.AcceptedPayments, sdsstf.STFStartACCEPTED_PAYMENTSVector)
	reputation := buildSTFReputation(builder, listing.Reputation)
	termsCID := stringOffset(builder, listing.TermsCID)
	license := stringOffset(builder, listing.License)
	signature := fbByteVector(builder, listing.Signature, sdsstf.STFStartSIGNATUREVector)
	sourcePeerID := stringOffset(builder, listing.SourcePeerID)
	primaryCategory := listingPrimaryCategory(listing, moduleCategory)
	categories := fbCapabilityClassVector(builder, listing.Categories, sdsstf.STFStartCATEGORIESVector)

	sdsstf.STFStart(builder)
	sdsstf.STFAddLISTING_ID(builder, listingID)
	sdsstf.STFAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdsstf.STFAddPROVIDER_EPM_CID(builder, providerEPMCID)
	sdsstf.STFAddTITLE(builder, title)
	sdsstf.STFAddDESCRIPTION(builder, description)
	sdsstf.STFAddDATA_TYPES(builder, dataTypes)
	sdsstf.STFAddCOVERAGE(builder, coverage)
	sdsstf.STFAddSAMPLE_CID(builder, sampleCID)
	addSTFAccessType(builder, listing.AccessType)
	sdsstf.STFAddENCRYPTION_REQUIRED(builder, listing.EncryptionRequired)
	sdsstf.STFAddPRICING(builder, pricing)
	sdsstf.STFAddACCEPTED_PAYMENTS(builder, acceptedPayments)
	sdsstf.STFAddCREATED_AT(builder, unixSeconds(listing.CreatedAt))
	sdsstf.STFAddUPDATED_AT(builder, unixSeconds(listing.UpdatedAt))
	sdsstf.STFAddACTIVE(builder, listing.Active)
	sdsstf.STFAddSIGNATURE(builder, signature)
	addSTFListingKind(builder, listing.ListingKind)
	sdsstf.STFAddTAGS(builder, tags)
	sdsstf.STFAddSAMPLE_RECORD_COUNT(builder, listing.SampleRecordCount)
	sdsstf.STFAddDELIVERY_METHODS(builder, deliveryMethods)
	sdsstf.STFAddPROTECTED_DELIVERY(builder, protectedDelivery)
	sdsstf.STFAddREPUTATION(builder, reputation)
	sdsstf.STFAddVERSION(builder, listing.Version)
	sdsstf.STFAddEXPIRES_AT(builder, unixSeconds(listing.ExpiresAt))
	sdsstf.STFAddTERMS_CID(builder, termsCID)
	sdsstf.STFAddLICENSE(builder, license)
	sdsstf.STFAddSOURCE_PEER_ID(builder, sourcePeerID)
	// The shelf. Written explicitly even when it is UNSPECIFIED — $CCT holds
	// UNSPECIFIED at ordinal 0 so the default would be correct, but a reader of
	// these bytes cannot distinguish a defaulted field from a stated one, and
	// this lane is exactly where that ambiguity used to do damage.
	//
	// Module listings normally leave CATEGORIES empty because their single shelf
	// comes from the module-catalog join. The SDS-exact publication lane can
	// populate this vector for data and service offerings that genuinely span
	// several ratified capability classes.
	sdsstf.STFAddPRIMARY_CATEGORY(builder, sdsstf.EnumValuescapabilityClass[primaryCategory])
	if categories != 0 {
		sdsstf.STFAddCATEGORIES(builder, categories)
	}
	root := sdsstf.STFEnd(builder)
	sdsstf.FinishSTFBuffer(builder, root)
	return builder.FinishedBytes()
}

func encodeAccessGrantRecord(grant *AccessGrant) []byte {
	builder := flatbuffers.NewBuilder(1536)

	grantID := stringOffset(builder, grant.GrantID)
	listingID := stringOffset(builder, grant.ListingID)
	buyerPeerID := stringOffset(builder, grant.BuyerPeerID)
	buyerKey := fbByteVector(builder, grant.BuyerEncryptionPubkey, sdsacl.ACLStartBUYER_ENCRYPTION_PUBKEYVector)
	tierName := stringOffset(builder, grant.TierName)
	paymentTxHash := stringOffset(builder, grant.PaymentTxHash)
	providerSignature := fbByteVector(builder, grant.ProviderSignature, sdsacl.ACLStartPROVIDER_SIGNATUREVector)
	keyAlgorithm := stringOffset(builder, grant.KeyAlgorithm)
	paymentCurrency := stringOffset(builder, grant.PaymentCurrency)
	paymentChain := stringOffset(builder, grant.PaymentChain)
	deliveryTopic := stringOffset(builder, grant.DeliveryTopic)
	notes := stringOffset(builder, grant.Notes)
	providerPeerID := stringOffset(builder, grant.ProviderPeerID)
	grantResponse := stringOffset(builder, grant.GrantResponseBase64)
	fieldStreamPolicy := buildACLGrantFieldStreamPolicy(builder, grant.FieldStreamPolicy)

	sdsacl.ACLStart(builder)
	sdsacl.ACLAddGRANT_ID(builder, grantID)
	sdsacl.ACLAddLISTING_ID(builder, listingID)
	sdsacl.ACLAddBUYER_PEER_ID(builder, buyerPeerID)
	sdsacl.ACLAddBUYER_ENCRYPTION_PUBKEY(builder, buyerKey)
	addACLAccessType(builder, grant.AccessType)
	sdsacl.ACLAddTIER_NAME(builder, tierName)
	sdsacl.ACLAddGRANTED_AT(builder, unixSeconds(grant.GrantedAt))
	sdsacl.ACLAddEXPIRES_AT(builder, unixSeconds(grant.ExpiresAt))
	sdsacl.ACLAddPAYMENT_TX_HASH(builder, paymentTxHash)
	addACLPaymentMethod(builder, grant.PaymentMethod)
	sdsacl.ACLAddPROVIDER_SIGNATURE(builder, providerSignature)
	sdsacl.ACLAddKEY_ALGORITHM(builder, keyAlgorithm)
	sdsacl.ACLAddRATE_LIMIT(builder, grant.RateLimit)
	sdsacl.ACLAddMAX_RECORDS_PER_REQUEST(builder, grant.MaxRecordsPerRequest)
	addACLGrantStatus(builder, grant.Status)
	sdsacl.ACLAddPAYMENT_AMOUNT(builder, grant.PaymentAmount)
	sdsacl.ACLAddPAYMENT_CURRENCY(builder, paymentCurrency)
	sdsacl.ACLAddPAYMENT_CHAIN(builder, paymentChain)
	sdsacl.ACLAddNEXT_RENEWAL(builder, unixSeconds(grant.NextRenewal))
	sdsacl.ACLAddAUTO_RENEW(builder, grant.AutoRenew)
	sdsacl.ACLAddRENEWAL_COUNT(builder, grant.RenewalCount)
	sdsacl.ACLAddTOTAL_REQUESTS(builder, grant.TotalRequests)
	sdsacl.ACLAddTOTAL_RECORDS(builder, grant.TotalRecords)
	sdsacl.ACLAddLAST_ACCESS(builder, unixSeconds(grant.LastAccess))
	sdsacl.ACLAddDELIVERY_TOPIC(builder, deliveryTopic)
	sdsacl.ACLAddCREATED_AT(builder, unixSeconds(grant.CreatedAt))
	sdsacl.ACLAddUPDATED_AT(builder, unixSeconds(grant.UpdatedAt))
	sdsacl.ACLAddNOTES(builder, notes)
	sdsacl.ACLAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdsacl.ACLAddGRANT_RESPONSE_BASE64(builder, grantResponse)
	sdsacl.ACLAddFIELD_STREAM_POLICY(builder, fieldStreamPolicy)
	root := sdsacl.ACLEnd(builder)
	sdsacl.FinishACLBuffer(builder, root)
	return builder.FinishedBytes()
}

func encodePurchaseRequestRecord(req *PurchaseRequest) []byte {
	builder := flatbuffers.NewBuilder(1536)

	requestID := stringOffset(builder, req.RequestID)
	listingID := stringOffset(builder, req.ListingID)
	tierName := stringOffset(builder, req.TierName)
	buyerPeerID := stringOffset(builder, req.BuyerPeerID)
	buyerKey := fbByteVector(builder, req.BuyerEncryptionPubkey, sdspur.PURStartBUYER_ENCRYPTION_PUBKEYVector)
	paymentCurrency := stringOffset(builder, req.PaymentCurrency)
	paymentTxHash := stringOffset(builder, req.PaymentTxHash)
	paymentChain := stringOffset(builder, req.PaymentChain)
	paymentReference := stringOffset(builder, purchasePaymentReference(req))
	buyerSignature := fbByteVector(builder, req.BuyerSignature, sdspur.PURStartBUYER_SIGNATUREVector)
	keyAlgorithm := stringOffset(builder, req.KeyAlgorithm)
	buyerEmail := stringOffset(builder, req.BuyerEmail)
	senderAddress := stringOffset(builder, req.SenderAddress)
	paymentIntentID := stringOffset(builder, req.PaymentIntentID)
	creditsTransactionID := stringOffset(builder, req.CreditsTransactionID)
	statusMessage := stringOffset(builder, req.StatusMessage)
	grantID := stringOffset(builder, req.GrantID)
	providerPeerID := stringOffset(builder, req.ProviderPeerID)
	preferredDeliveryMethod := stringOffset(builder, req.PreferredDeliveryMethod)
	webhookURL := stringOffset(builder, req.WebhookURL)
	providerSignature := fbByteVector(builder, req.ProviderSignature, sdspur.PURStartPROVIDER_SIGNATUREVector)

	sdspur.PURStart(builder)
	sdspur.PURAddREQUEST_ID(builder, requestID)
	sdspur.PURAddLISTING_ID(builder, listingID)
	sdspur.PURAddTIER_NAME(builder, tierName)
	sdspur.PURAddBUYER_PEER_ID(builder, buyerPeerID)
	sdspur.PURAddBUYER_ENCRYPTION_PUBKEY(builder, buyerKey)
	addPURPaymentMethod(builder, req.PaymentMethod)
	sdspur.PURAddPAYMENT_AMOUNT(builder, req.PaymentAmount)
	sdspur.PURAddPAYMENT_CURRENCY(builder, paymentCurrency)
	sdspur.PURAddPAYMENT_TX_HASH(builder, paymentTxHash)
	sdspur.PURAddPAYMENT_CHAIN(builder, paymentChain)
	sdspur.PURAddPAYMENT_REFERENCE(builder, paymentReference)
	sdspur.PURAddBUYER_SIGNATURE(builder, buyerSignature)
	sdspur.PURAddTIMESTAMP(builder, unixSeconds(req.CreatedAt))
	sdspur.PURAddKEY_ALGORITHM(builder, keyAlgorithm)
	sdspur.PURAddBUYER_EMAIL(builder, buyerEmail)
	sdspur.PURAddSENDER_ADDRESS(builder, senderAddress)
	sdspur.PURAddCONFIRMATION_BLOCK(builder, req.ConfirmationBlock)
	sdspur.PURAddPAYMENT_INTENT_ID(builder, paymentIntentID)
	sdspur.PURAddCREDITS_TRANSACTION_ID(builder, creditsTransactionID)
	addPURPurchaseStatus(builder, req.Status)
	sdspur.PURAddSTATUS_MESSAGE(builder, statusMessage)
	sdspur.PURAddCREATED_AT(builder, unixSeconds(req.CreatedAt))
	sdspur.PURAddUPDATED_AT(builder, unixSeconds(req.UpdatedAt))
	sdspur.PURAddPAYMENT_DEADLINE(builder, unixSeconds(req.PaymentDeadline))
	sdspur.PURAddPAYMENT_CONFIRMED_AT(builder, unixSeconds(req.PaymentConfirmedAt))
	sdspur.PURAddGRANT_ISSUED_AT(builder, unixSeconds(req.GrantIssuedAt))
	sdspur.PURAddGRANT_ID(builder, grantID)
	sdspur.PURAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdspur.PURAddPROVIDER_ACKNOWLEDGED_AT(builder, unixSeconds(req.ProviderAcknowledgedAt))
	sdspur.PURAddPREFERRED_DELIVERY_METHOD(builder, preferredDeliveryMethod)
	sdspur.PURAddWEBHOOK_URL(builder, webhookURL)
	sdspur.PURAddPROVIDER_SIGNATURE(builder, providerSignature)
	root := sdspur.PUREnd(builder)
	sdspur.FinishPURBuffer(builder, root)
	return builder.FinishedBytes()
}

func encodeReviewRecord(review *Review) []byte {
	builder := flatbuffers.NewBuilder(1024)

	reviewID := stringOffset(builder, review.ReviewID)
	listingID := stringOffset(builder, review.ListingID)
	reviewerPeerID := stringOffset(builder, review.ReviewerPeerID)
	title := stringOffset(builder, review.Title)
	content := stringOffset(builder, review.Content)
	aclGrantID := stringOffset(builder, review.ACLGrantID)
	reviewerSignature := fbByteVector(builder, review.ReviewerSignature, sdsrev.REVStartREVIEWER_SIGNATUREVector)
	qualityMetrics := buildREVQualityMetrics(builder, review.QualityMetrics)
	providerResponse := stringOffset(builder, review.ProviderResponse)
	moderationNotes := stringOffset(builder, review.ModerationNotes)

	sdsrev.REVStart(builder)
	sdsrev.REVAddREVIEW_ID(builder, reviewID)
	sdsrev.REVAddLISTING_ID(builder, listingID)
	sdsrev.REVAddREVIEWER_PEER_ID(builder, reviewerPeerID)
	sdsrev.REVAddRATING(builder, byte(review.Rating))
	sdsrev.REVAddTITLE(builder, title)
	sdsrev.REVAddCONTENT(builder, content)
	sdsrev.REVAddACL_GRANT_ID(builder, aclGrantID)
	sdsrev.REVAddTIMESTAMP(builder, unixSeconds(review.CreatedAt))
	sdsrev.REVAddREVIEWER_SIGNATURE(builder, reviewerSignature)
	sdsrev.REVAddQUALITY_METRICS(builder, qualityMetrics)
	sdsrev.REVAddVERIFIED_PURCHASE(builder, review.VerifiedPurchase)
	sdsrev.REVAddUPDATED_AT(builder, unixSeconds(review.UpdatedAt))
	addREVReviewStatus(builder, review.Status)
	sdsrev.REVAddHELPFUL_COUNT(builder, review.HelpfulCount)
	sdsrev.REVAddNOT_HELPFUL_COUNT(builder, review.NotHelpfulCount)
	sdsrev.REVAddPROVIDER_RESPONSE(builder, providerResponse)
	sdsrev.REVAddPROVIDER_RESPONSE_AT(builder, unixSeconds(review.ProviderResponseAt))
	sdsrev.REVAddFLAGGED_COUNT(builder, review.FlaggedCount)
	sdsrev.REVAddMODERATION_NOTES(builder, moderationNotes)
	root := sdsrev.REVEnd(builder)
	sdsrev.FinishREVBuffer(builder, root)
	return builder.FinishedBytes()
}

func buildSTFCoverage(builder *flatbuffers.Builder, coverage DataCoverage) flatbuffers.UOffsetT {
	spatial := buildSTFSpatialCoverage(builder, coverage.Spatial)
	temporal := buildSTFTemporalCoverage(builder, coverage.Temporal)

	sdsstf.DataCoverageStart(builder)
	sdsstf.DataCoverageAddSPATIAL(builder, spatial)
	sdsstf.DataCoverageAddTEMPORAL(builder, temporal)
	return sdsstf.DataCoverageEnd(builder)
}

func buildSTFSpatialCoverage(builder *flatbuffers.Builder, coverage SpatialCoverage) flatbuffers.UOffsetT {
	coverageType := stringOffset(builder, coverage.Type)
	regions := fbStringVector(builder, coverage.Regions, sdsstf.SpatialCoverageStartREGIONSVector)
	objectIDs := fbStringVector(builder, coverage.ObjectIDs, sdsstf.SpatialCoverageStartOBJECT_IDSVector)
	geoBounds := fbFloat64Vector(builder, coverage.GeoBounds, sdsstf.SpatialCoverageStartGEO_BOUNDSVector)

	sdsstf.SpatialCoverageStart(builder)
	sdsstf.SpatialCoverageAddTYPE(builder, coverageType)
	sdsstf.SpatialCoverageAddREGIONS(builder, regions)
	sdsstf.SpatialCoverageAddOBJECT_IDS(builder, objectIDs)
	sdsstf.SpatialCoverageAddMIN_ALTITUDE_KM(builder, coverage.MinAltitudeKm)
	sdsstf.SpatialCoverageAddMAX_ALTITUDE_KM(builder, coverage.MaxAltitudeKm)
	sdsstf.SpatialCoverageAddGEO_BOUNDS(builder, geoBounds)
	return sdsstf.SpatialCoverageEnd(builder)
}

func buildSTFTemporalCoverage(builder *flatbuffers.Builder, coverage TemporalCoverage) flatbuffers.UOffsetT {
	startEpoch := stringOffset(builder, coverage.StartEpoch)
	endEpoch := stringOffset(builder, coverage.EndEpoch)
	updateFrequency := stringOffset(builder, coverage.UpdateFrequency)

	sdsstf.TemporalCoverageStart(builder)
	sdsstf.TemporalCoverageAddSTART_EPOCH(builder, startEpoch)
	sdsstf.TemporalCoverageAddEND_EPOCH(builder, endEpoch)
	sdsstf.TemporalCoverageAddUPDATE_FREQUENCY(builder, updateFrequency)
	sdsstf.TemporalCoverageAddHISTORICAL_DEPTH(builder, coverage.HistoricalDepthDays)
	sdsstf.TemporalCoverageAddLATENCY_SECONDS(builder, coverage.LatencySeconds)
	return sdsstf.TemporalCoverageEnd(builder)
}

func buildSTFPricingVector(builder *flatbuffers.Builder, tiers []PricingTier) flatbuffers.UOffsetT {
	if len(tiers) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, len(tiers))
	for i, tier := range tiers {
		offsets[i] = buildSTFPricingTier(builder, tier)
	}
	sdsstf.STFStartPRICINGVector(builder, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	return builder.EndVector(len(offsets))
}

func buildSTFPricingTier(builder *flatbuffers.Builder, tier PricingTier) flatbuffers.UOffsetT {
	name := stringOffset(builder, tier.Name)
	currency := stringOffset(builder, tier.PriceCurrency)
	features := fbStringVector(builder, tier.Features, sdsstf.PricingTierStartFEATURESVector)
	description := stringOffset(builder, tier.Description)

	sdsstf.PricingTierStart(builder)
	sdsstf.PricingTierAddNAME(builder, name)
	sdsstf.PricingTierAddPRICE_AMOUNT(builder, tier.PriceAmount)
	sdsstf.PricingTierAddPRICE_CURRENCY(builder, currency)
	sdsstf.PricingTierAddDURATION_DAYS(builder, tier.DurationDays)
	sdsstf.PricingTierAddRATE_LIMIT(builder, tier.RateLimit)
	sdsstf.PricingTierAddFEATURES(builder, features)
	sdsstf.PricingTierAddMAX_RECORDS_PER_REQUEST(builder, tier.MaxRecordsPerRequest)
	sdsstf.PricingTierAddDESCRIPTION(builder, description)
	return sdsstf.PricingTierEnd(builder)
}

func buildSTFProtectedDelivery(builder *flatbuffers.Builder, delivery ProtectedDelivery) flatbuffers.UOffsetT {
	if isEmptyProtectedDelivery(delivery) {
		return 0
	}
	encryptedCID := stringOffset(builder, delivery.EncryptedCID)
	manifestCID := stringOffset(builder, delivery.ManifestCID)
	contentHash := stringOffset(builder, delivery.ContentHash)
	contentKeyID := stringOffset(builder, delivery.ContentKeyID)
	licenseModuleID := stringOffset(builder, delivery.LicenseModuleID)
	moduleID := stringOffset(builder, delivery.ModuleID)
	moduleVersion := stringOffset(builder, delivery.ModuleVersion)
	requiredScopes := fbStringVector(builder, delivery.RequiredScopes, sdsstf.ProtectedDeliveryBindingStartREQUIRED_SCOPESVector)
	grantScope := stringOffset(builder, delivery.GrantScope)
	deliveryProtocol := stringOffset(builder, delivery.DeliveryProtocol)
	fieldStreamPolicy := buildSTFGrantFieldStreamPolicy(builder, delivery.FieldStreamPolicy)

	sdsstf.ProtectedDeliveryBindingStart(builder)
	sdsstf.ProtectedDeliveryBindingAddENCRYPTED_CID(builder, encryptedCID)
	sdsstf.ProtectedDeliveryBindingAddMANIFEST_CID(builder, manifestCID)
	sdsstf.ProtectedDeliveryBindingAddCONTENT_HASH(builder, contentHash)
	sdsstf.ProtectedDeliveryBindingAddCONTENT_KEY_ID(builder, contentKeyID)
	sdsstf.ProtectedDeliveryBindingAddLICENSE_MODULE_ID(builder, licenseModuleID)
	sdsstf.ProtectedDeliveryBindingAddMODULE_ID(builder, moduleID)
	sdsstf.ProtectedDeliveryBindingAddMODULE_VERSION(builder, moduleVersion)
	sdsstf.ProtectedDeliveryBindingAddREQUIRED_SCOPES(builder, requiredScopes)
	sdsstf.ProtectedDeliveryBindingAddGRANT_SCOPE(builder, grantScope)
	sdsstf.ProtectedDeliveryBindingAddDELIVERY_PROTOCOL(builder, deliveryProtocol)
	sdsstf.ProtectedDeliveryBindingAddFIELD_STREAM_POLICY(builder, fieldStreamPolicy)
	return sdsstf.ProtectedDeliveryBindingEnd(builder)
}

func isEmptyProtectedDelivery(delivery ProtectedDelivery) bool {
	return delivery.EncryptedCID == "" &&
		delivery.ManifestCID == "" &&
		delivery.ContentHash == "" &&
		delivery.ContentKeyID == "" &&
		delivery.LicenseModuleID == "" &&
		delivery.ModuleID == "" &&
		delivery.ModuleVersion == "" &&
		len(delivery.RequiredScopes) == 0 &&
		delivery.GrantScope == "" &&
		delivery.DeliveryProtocol == "" &&
		delivery.FieldStreamPolicy == nil
}

func buildSTFReputation(builder *flatbuffers.Builder, reputation ProviderReputation) flatbuffers.UOffsetT {
	if reputation == (ProviderReputation{}) {
		return 0
	}
	sdsstf.ProviderReputationStart(builder)
	sdsstf.ProviderReputationAddTOTAL_SALES(builder, reputation.TotalSales)
	sdsstf.ProviderReputationAddAVERAGE_RATING_X10(builder, reputation.AverageRatingX10)
	sdsstf.ProviderReputationAddTOTAL_RATINGS(builder, reputation.TotalRatings)
	sdsstf.ProviderReputationAddUPTIME_PERCENTAGE_X100(builder, reputation.UptimePercentageX100)
	sdsstf.ProviderReputationAddAVG_DELIVERY_LATENCY_MS(builder, reputation.AvgDeliveryLatencyMs)
	sdsstf.ProviderReputationAddDISPUTE_COUNT(builder, reputation.DisputeCount)
	sdsstf.ProviderReputationAddPROVIDER_SINCE(builder, reputation.ProviderSince)
	return sdsstf.ProviderReputationEnd(builder)
}

func buildSTFGrantFieldStreamPolicy(builder *flatbuffers.Builder, policy *GrantFieldStreamPolicy) flatbuffers.UOffsetT {
	if policy == nil {
		return 0
	}
	policyID := stringOffset(builder, policy.PolicyID)
	streamID := stringOffset(builder, policy.StreamID)
	schemaCode := stringOffset(builder, policy.SchemaCode)
	allowedFields := fbStringVector(builder, policy.AllowedFieldPaths, sdsstf.GrantFieldStreamPolicyStartALLOWED_FIELD_PATHSVector)
	redactedFields := fbStringVector(builder, policy.RedactedFieldPaths, sdsstf.GrantFieldStreamPolicyStartREDACTED_FIELD_PATHSVector)
	keyEpoch := stringOffset(builder, policy.KeyEpoch)
	grantScope := stringOffset(builder, policy.GrantScope)
	allowedOperations := fbStringVector(builder, policy.AllowedOperations, sdsstf.GrantFieldStreamPolicyStartALLOWED_OPERATIONSVector)

	sdsstf.GrantFieldStreamPolicyStart(builder)
	sdsstf.GrantFieldStreamPolicyAddPOLICY_ID(builder, policyID)
	sdsstf.GrantFieldStreamPolicyAddPOLICY_VERSION(builder, policy.PolicyVersion)
	sdsstf.GrantFieldStreamPolicyAddSTREAM_ID(builder, streamID)
	sdsstf.GrantFieldStreamPolicyAddSCHEMA_CODE(builder, schemaCode)
	sdsstf.GrantFieldStreamPolicyAddALLOWED_FIELD_PATHS(builder, allowedFields)
	sdsstf.GrantFieldStreamPolicyAddREDACTED_FIELD_PATHS(builder, redactedFields)
	sdsstf.GrantFieldStreamPolicyAddKEY_EPOCH(builder, keyEpoch)
	sdsstf.GrantFieldStreamPolicyAddGRANT_SCOPE(builder, grantScope)
	sdsstf.GrantFieldStreamPolicyAddALLOWED_OPERATIONS(builder, allowedOperations)
	return sdsstf.GrantFieldStreamPolicyEnd(builder)
}

func buildACLGrantFieldStreamPolicy(builder *flatbuffers.Builder, policy *GrantFieldStreamPolicy) flatbuffers.UOffsetT {
	if policy == nil {
		return 0
	}
	policyID := stringOffset(builder, policy.PolicyID)
	streamID := stringOffset(builder, policy.StreamID)
	schemaCode := stringOffset(builder, policy.SchemaCode)
	allowedFields := fbStringVector(builder, policy.AllowedFieldPaths, sdsacl.GrantFieldStreamPolicyStartALLOWED_FIELD_PATHSVector)
	redactedFields := fbStringVector(builder, policy.RedactedFieldPaths, sdsacl.GrantFieldStreamPolicyStartREDACTED_FIELD_PATHSVector)
	keyEpoch := stringOffset(builder, policy.KeyEpoch)
	grantScope := stringOffset(builder, policy.GrantScope)
	allowedOperations := fbStringVector(builder, policy.AllowedOperations, sdsacl.GrantFieldStreamPolicyStartALLOWED_OPERATIONSVector)

	sdsacl.GrantFieldStreamPolicyStart(builder)
	sdsacl.GrantFieldStreamPolicyAddPOLICY_ID(builder, policyID)
	sdsacl.GrantFieldStreamPolicyAddPOLICY_VERSION(builder, policy.PolicyVersion)
	sdsacl.GrantFieldStreamPolicyAddSTREAM_ID(builder, streamID)
	sdsacl.GrantFieldStreamPolicyAddSCHEMA_CODE(builder, schemaCode)
	sdsacl.GrantFieldStreamPolicyAddALLOWED_FIELD_PATHS(builder, allowedFields)
	sdsacl.GrantFieldStreamPolicyAddREDACTED_FIELD_PATHS(builder, redactedFields)
	sdsacl.GrantFieldStreamPolicyAddKEY_EPOCH(builder, keyEpoch)
	sdsacl.GrantFieldStreamPolicyAddGRANT_SCOPE(builder, grantScope)
	sdsacl.GrantFieldStreamPolicyAddALLOWED_OPERATIONS(builder, allowedOperations)
	return sdsacl.GrantFieldStreamPolicyEnd(builder)
}

func buildREVQualityMetrics(builder *flatbuffers.Builder, metrics DataQualityMetrics) flatbuffers.UOffsetT {
	sdsrev.DataQualityMetricsStart(builder)
	sdsrev.DataQualityMetricsAddSCHEMA_COMPLIANCE(builder, byte(metrics.SchemaCompliance))
	sdsrev.DataQualityMetricsAddDATA_FRESHNESS(builder, byte(metrics.DataFreshness))
	sdsrev.DataQualityMetricsAddCOVERAGE_ACCURACY(builder, byte(metrics.CoverageAccuracy))
	sdsrev.DataQualityMetricsAddDELIVERY_RELIABILITY(builder, byte(metrics.DeliveryReliability))
	return sdsrev.DataQualityMetricsEnd(builder)
}

func stringOffset(builder *flatbuffers.Builder, value string) flatbuffers.UOffsetT {
	if value == "" {
		return 0
	}
	return builder.CreateString(value)
}

func fbStringVector(builder *flatbuffers.Builder, values []string, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, len(values))
	for i, value := range values {
		offsets[i] = builder.CreateString(value)
	}
	start(builder, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	return builder.EndVector(len(offsets))
}

func fbByteVector(builder *flatbuffers.Builder, values []byte, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	start(builder, len(values))
	for i := len(values) - 1; i >= 0; i-- {
		builder.PrependByte(values[i])
	}
	return builder.EndVector(len(values))
}

func fbFloat64Vector(builder *flatbuffers.Builder, values []float64, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	start(builder, len(values))
	for i := len(values) - 1; i >= 0; i-- {
		builder.PrependFloat64(values[i])
	}
	return builder.EndVector(len(values))
}

func fbPaymentMethodVector(builder *flatbuffers.Builder, methods []PaymentMethod, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(methods) == 0 {
		return 0
	}
	start(builder, len(methods))
	for i := len(methods) - 1; i >= 0; i-- {
		builder.PrependInt8(paymentMethodValue(methods[i]))
	}
	return builder.EndVector(len(methods))
}

func fbCapabilityClassVector(builder *flatbuffers.Builder, categories []string, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	values := make([]byte, 0, len(categories))
	for _, category := range categories {
		if value, ok := sdsstf.EnumValuescapabilityClass[strings.TrimSpace(category)]; ok {
			values = append(values, byte(value))
		}
	}
	if len(values) == 0 {
		return 0
	}
	start(builder, len(values))
	for i := len(values) - 1; i >= 0; i-- {
		builder.PrependByte(values[i])
	}
	return builder.EndVector(len(values))
}

func paymentMethodValue(method PaymentMethod) int8 {
	switch method {
	case PaymentMethodCryptoSOL:
		return 1
	case PaymentMethodCryptoBTC:
		return 2
	case PaymentMethodSDNCredits:
		return 3
	case PaymentMethodFiatStripe:
		return 4
	case PaymentMethodFree:
		return 5
	case PaymentMethodUsageBased:
		return 6
	case PaymentMethodEnterprise:
		return 7
	default:
		return 0
	}
}

func addSTFAccessType(builder *flatbuffers.Builder, access AccessType) {
	switch access {
	case AccessTypeSubscription:
		sdsstf.STFAddACCESS_TYPE(builder, 1)
	case AccessTypeStreaming:
		sdsstf.STFAddACCESS_TYPE(builder, 2)
	case AccessTypeQuery:
		sdsstf.STFAddACCESS_TYPE(builder, 3)
	default:
		sdsstf.STFAddACCESS_TYPE(builder, 0)
	}
}

func addACLAccessType(builder *flatbuffers.Builder, access AccessType) {
	switch access {
	case AccessTypeSubscription:
		sdsacl.ACLAddACCESS_TYPE(builder, 1)
	case AccessTypeStreaming:
		sdsacl.ACLAddACCESS_TYPE(builder, 2)
	case AccessTypeQuery:
		sdsacl.ACLAddACCESS_TYPE(builder, 3)
	default:
		sdsacl.ACLAddACCESS_TYPE(builder, 0)
	}
}

func addSTFListingKind(builder *flatbuffers.Builder, kind ListingKind) {
	switch kind {
	case ListingKindWASMModule:
		sdsstf.STFAddLISTING_KIND(builder, 1)
	case ListingKindService:
		sdsstf.STFAddLISTING_KIND(builder, 2)
	default:
		sdsstf.STFAddLISTING_KIND(builder, 0)
	}
}

func addACLPaymentMethod(builder *flatbuffers.Builder, method PaymentMethod) {
	switch method {
	case PaymentMethodCryptoSOL:
		sdsacl.ACLAddPAYMENT_METHOD(builder, 1)
	case PaymentMethodCryptoBTC:
		sdsacl.ACLAddPAYMENT_METHOD(builder, 2)
	case PaymentMethodSDNCredits:
		sdsacl.ACLAddPAYMENT_METHOD(builder, 3)
	case PaymentMethodFiatStripe:
		sdsacl.ACLAddPAYMENT_METHOD(builder, 4)
	case PaymentMethodFree:
		sdsacl.ACLAddPAYMENT_METHOD(builder, 5)
	case PaymentMethodUsageBased:
		sdsacl.ACLAddPAYMENT_METHOD(builder, 6)
	case PaymentMethodEnterprise:
		sdsacl.ACLAddPAYMENT_METHOD(builder, 7)
	default:
		sdsacl.ACLAddPAYMENT_METHOD(builder, 0)
	}
}

func addPURPaymentMethod(builder *flatbuffers.Builder, method PaymentMethod) {
	switch method {
	case PaymentMethodCryptoSOL:
		sdspur.PURAddPAYMENT_METHOD(builder, 1)
	case PaymentMethodCryptoBTC:
		sdspur.PURAddPAYMENT_METHOD(builder, 2)
	case PaymentMethodSDNCredits:
		sdspur.PURAddPAYMENT_METHOD(builder, 3)
	case PaymentMethodFiatStripe:
		sdspur.PURAddPAYMENT_METHOD(builder, 4)
	case PaymentMethodFree:
		sdspur.PURAddPAYMENT_METHOD(builder, 5)
	case PaymentMethodUsageBased:
		sdspur.PURAddPAYMENT_METHOD(builder, 6)
	case PaymentMethodEnterprise:
		sdspur.PURAddPAYMENT_METHOD(builder, 7)
	default:
		sdspur.PURAddPAYMENT_METHOD(builder, 0)
	}
}

func addACLGrantStatus(builder *flatbuffers.Builder, status GrantStatus) {
	switch status {
	case GrantStatusRevoked:
		sdsacl.ACLAddSTATUS(builder, 1)
	case GrantStatusExpired:
		sdsacl.ACLAddSTATUS(builder, 2)
	case GrantStatusSuspended:
		sdsacl.ACLAddSTATUS(builder, 3)
	case GrantStatusPending:
		sdsacl.ACLAddSTATUS(builder, 4)
	default:
		sdsacl.ACLAddSTATUS(builder, 0)
	}
}

func addPURPurchaseStatus(builder *flatbuffers.Builder, status PurchaseStatus) {
	switch status {
	case PurchaseStatusPaymentDetected:
		sdspur.PURAddSTATUS(builder, 1)
	case PurchaseStatusPaymentConfirmed:
		sdspur.PURAddSTATUS(builder, 2)
	case PurchaseStatusCompleted:
		sdspur.PURAddSTATUS(builder, 3)
	case PurchaseStatusFailed:
		sdspur.PURAddSTATUS(builder, 4)
	case PurchaseStatusCancelled:
		sdspur.PURAddSTATUS(builder, 5)
	case PurchaseStatusRefundRequested:
		sdspur.PURAddSTATUS(builder, 6)
	case PurchaseStatusRefunded:
		sdspur.PURAddSTATUS(builder, 7)
	case PurchaseStatusExpired:
		sdspur.PURAddSTATUS(builder, 8)
	default:
		sdspur.PURAddSTATUS(builder, 0)
	}
}

func addREVReviewStatus(builder *flatbuffers.Builder, status ReviewStatus) {
	switch status {
	case ReviewStatusPending:
		sdsrev.REVAddSTATUS(builder, 1)
	case ReviewStatusFlagged:
		sdsrev.REVAddSTATUS(builder, 2)
	case ReviewStatusHidden:
		sdsrev.REVAddSTATUS(builder, 3)
	case ReviewStatusRemoved:
		sdsrev.REVAddSTATUS(builder, 4)
	default:
		sdsrev.REVAddSTATUS(builder, 0)
	}
}

func purchasePaymentReference(req *PurchaseRequest) string {
	if req.PaymentIntentID != "" {
		return req.PaymentIntentID
	}
	if req.CreditsTransactionID != "" {
		return req.CreditsTransactionID
	}
	return req.PaymentTxHash
}

func unixSeconds(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	seconds := t.Unix()
	if seconds < 0 {
		return 0
	}
	return uint64(seconds)
}
