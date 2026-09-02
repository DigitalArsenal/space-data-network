package storefront

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	sdsdpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	sdspnm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	sdsstf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/google/uuid"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const ListingMerkleProfile = "SDN-MERKLE-SHA256-v1"

// ListingDraft is the SDS-exact JSON contract for the listing publication
// endpoint. Fields synthesized from the node identity deliberately are absent.
type ListingDraft struct {
	ListingKind      string                `json:"LISTING_KIND"`
	Title            string                `json:"TITLE"`
	Description      string                `json:"DESCRIPTION"`
	DataTypes        []string              `json:"DATA_TYPES"`
	PrimaryCategory  string                `json:"PRIMARY_CATEGORY"`
	Categories       []string              `json:"CATEGORIES"`
	AccessType       string                `json:"ACCESS_TYPE"`
	DeliveryMethods  []string              `json:"DELIVERY_METHODS"`
	Pricing          []ListingPricingDraft `json:"PRICING"`
	AcceptedPayments []string              `json:"ACCEPTED_PAYMENTS"`
	License          string                `json:"LICENSE"`
	TermsCID         string                `json:"TERMS_CID"`
	Coverage         ListingCoverageDraft  `json:"COVERAGE"`
	ExpiresAt        uint64                `json:"EXPIRES_AT"`
	SourceConnector  string                `json:"SOURCE_CONNECTOR_ID,omitempty"`
}

type ListingPricingDraft struct {
	Name                 string   `json:"NAME"`
	PriceAmount          uint64   `json:"PRICE_AMOUNT"`
	PriceCurrency        string   `json:"PRICE_CURRENCY"`
	DurationDays         uint32   `json:"DURATION_DAYS"`
	RateLimit            uint32   `json:"RATE_LIMIT"`
	Features             []string `json:"FEATURES"`
	MaxRecordsPerRequest uint32   `json:"MAX_RECORDS_PER_REQUEST"`
	Description          string   `json:"DESCRIPTION"`
}

type ListingCoverageDraft struct {
	Spatial  ListingSpatialCoverageDraft  `json:"SPATIAL"`
	Temporal ListingTemporalCoverageDraft `json:"TEMPORAL"`
}

type ListingSpatialCoverageDraft struct {
	Type          string    `json:"TYPE"`
	Regions       []string  `json:"REGIONS"`
	ObjectIDs     []string  `json:"OBJECT_IDS"`
	MinAltitudeKM float64   `json:"MIN_ALTITUDE_KM"`
	MaxAltitudeKM float64   `json:"MAX_ALTITUDE_KM"`
	GeoBounds     []float64 `json:"GEO_BOUNDS"`
}

type ListingTemporalCoverageDraft struct {
	StartEpoch      string `json:"START_EPOCH"`
	EndEpoch        string `json:"END_EPOCH"`
	UpdateFrequency string `json:"UPDATE_FREQUENCY"`
	HistoricalDepth uint32 `json:"HISTORICAL_DEPTH"`
	LatencySeconds  uint32 `json:"LATENCY_SECONDS"`
}

type ListingPropagationReport struct {
	ListingID        string `json:"listing_id"`
	STFCID           string `json:"stf_cid"`
	PNMCID           string `json:"pnm_cid"`
	DPMCID           string `json:"dpm_cid,omitempty"`
	AnnouncedToPeers int    `json:"announced_to_peers"`
	PropagationError string `json:"propagation_error,omitempty"`
}

type canonicalSTFDocument struct {
	ListingID          string                `json:"LISTING_ID"`
	ProviderPeerID     string                `json:"PROVIDER_PEER_ID"`
	ProviderEPMCID     string                `json:"PROVIDER_EPM_CID"`
	Title              string                `json:"TITLE"`
	Description        string                `json:"DESCRIPTION"`
	DataTypes          []string              `json:"DATA_TYPES"`
	Coverage           ListingCoverageDraft  `json:"COVERAGE"`
	SampleCID          string                `json:"SAMPLE_CID"`
	AccessType         string                `json:"ACCESS_TYPE"`
	EncryptionRequired bool                  `json:"ENCRYPTION_REQUIRED"`
	Pricing            []ListingPricingDraft `json:"PRICING"`
	AcceptedPayments   []string              `json:"ACCEPTED_PAYMENTS"`
	CreatedAt          uint64                `json:"CREATED_AT"`
	UpdatedAt          uint64                `json:"UPDATED_AT"`
	Active             bool                  `json:"ACTIVE"`
	Signature          []byte                `json:"SIGNATURE,omitempty"`
	ListingKind        string                `json:"LISTING_KIND"`
	Tags               []string              `json:"TAGS"`
	SampleRecordCount  uint32                `json:"SAMPLE_RECORD_COUNT"`
	DeliveryMethods    []string              `json:"DELIVERY_METHODS"`
	Version            uint32                `json:"VERSION"`
	ExpiresAt          uint64                `json:"EXPIRES_AT"`
	TermsCID           string                `json:"TERMS_CID"`
	License            string                `json:"LICENSE"`
	SourcePeerID       string                `json:"SOURCE_PEER_ID"`
	PrimaryCategory    string                `json:"PRIMARY_CATEGORY"`
	Categories         []string              `json:"CATEGORIES"`
}

func (d ListingDraft) validate() error {
	if _, ok := sdsstf.EnumValueslistingCategory[strings.TrimSpace(d.ListingKind)]; !ok {
		return fmt.Errorf("LISTING_KIND must be one of DataStream, WasmModule, Service")
	}
	if strings.TrimSpace(d.Title) == "" {
		return errors.New("TITLE is required")
	}
	if _, ok := sdsstf.EnumValuesaccessCategory[strings.TrimSpace(d.AccessType)]; !ok {
		return errors.New("ACCESS_TYPE must be one of OneTime, Subscription, Streaming, Query")
	}
	if len(d.Pricing) == 0 {
		return errors.New("PRICING must contain at least one PricingTier")
	}
	primary := strings.TrimSpace(d.PrimaryCategory)
	if primary == "" {
		primary = "UNSPECIFIED"
	}
	if _, ok := sdsstf.EnumValuescapabilityClass[primary]; !ok {
		return fmt.Errorf("PRIMARY_CATEGORY %q is not a ratified $CCT capabilityClass", primary)
	}
	seen := make(map[string]bool, len(d.Categories))
	containsPrimary := len(d.Categories) == 0
	for _, raw := range d.Categories {
		category := strings.TrimSpace(raw)
		if _, ok := sdsstf.EnumValuescapabilityClass[category]; !ok {
			return fmt.Errorf("CATEGORIES member %q is not a ratified $CCT capabilityClass", category)
		}
		if seen[category] {
			return fmt.Errorf("CATEGORIES repeats %q", category)
		}
		seen[category] = true
		containsPrimary = containsPrimary || category == primary
	}
	if !containsPrimary {
		return errors.New("CATEGORIES must include PRIMARY_CATEGORY when nonempty")
	}
	for _, method := range d.AcceptedPayments {
		if _, ok := sdsstf.EnumValuespaymentMethod[strings.TrimSpace(method)]; !ok {
			return fmt.Errorf("ACCEPTED_PAYMENTS member %q is not an SDS paymentMethod", method)
		}
	}
	if d.ListingKind == "DataStream" && len(d.DataTypes) == 0 {
		return errors.New("DATA_TYPES is required for DataStream listings")
	}
	if strings.TrimSpace(d.SourceConnector) != "" && strings.TrimSpace(d.ListingKind) != "DataStream" {
		return errors.New("SOURCE_CONNECTOR_ID is only valid for DataStream listings")
	}
	if len(d.Coverage.Spatial.GeoBounds) != 0 && len(d.Coverage.Spatial.GeoBounds) != 4 {
		return errors.New("COVERAGE.SPATIAL.GEO_BOUNDS must be empty or contain four values")
	}
	return nil
}

func listingFromDraft(d ListingDraft, peerID, epmCID string, now time.Time) *Listing {
	primary := strings.TrimSpace(d.PrimaryCategory)
	if primary == "" {
		primary = "UNSPECIFIED"
	}
	listing := &Listing{
		ListingID:         uuid.NewString(),
		ListingKind:       listingKindFromSDS(d.ListingKind),
		ProviderPeerID:    peerID,
		ProviderEPMCID:    epmCID,
		Title:             strings.TrimSpace(d.Title),
		Description:       d.Description,
		DataTypes:         cleanStrings(d.DataTypes),
		PrimaryCategory:   primary,
		Categories:        cleanStrings(d.Categories),
		AccessType:        accessTypeFromSDS(d.AccessType),
		DeliveryMethods:   cleanStrings(d.DeliveryMethods),
		AcceptedPayments:  paymentMethodsFromSDS(d.AcceptedPayments),
		License:           d.License,
		TermsCID:          d.TermsCID,
		SourceConnectorID: strings.TrimSpace(d.SourceConnector),
		Coverage: DataCoverage{
			Spatial: SpatialCoverage{
				Type: d.Coverage.Spatial.Type, Regions: cleanStrings(d.Coverage.Spatial.Regions),
				ObjectIDs: cleanStrings(d.Coverage.Spatial.ObjectIDs), MinAltitudeKm: d.Coverage.Spatial.MinAltitudeKM,
				MaxAltitudeKm: d.Coverage.Spatial.MaxAltitudeKM, GeoBounds: append([]float64(nil), d.Coverage.Spatial.GeoBounds...),
			},
			Temporal: TemporalCoverage{
				StartEpoch: d.Coverage.Temporal.StartEpoch, EndEpoch: d.Coverage.Temporal.EndEpoch,
				UpdateFrequency: d.Coverage.Temporal.UpdateFrequency, HistoricalDepthDays: d.Coverage.Temporal.HistoricalDepth,
				LatencySeconds: d.Coverage.Temporal.LatencySeconds,
			},
		},
		CreatedAt: now, UpdatedAt: now, Version: 1, Active: true,
	}
	if d.ExpiresAt > 0 {
		listing.ExpiresAt = time.Unix(int64(d.ExpiresAt), 0).UTC()
	}
	for _, tier := range d.Pricing {
		listing.Pricing = append(listing.Pricing, PricingTier{
			Name: tier.Name, PriceAmount: tier.PriceAmount, PriceCurrency: tier.PriceCurrency,
			DurationDays: tier.DurationDays, RateLimit: tier.RateLimit,
			MaxRecordsPerRequest: tier.MaxRecordsPerRequest,
			Features:             cleanStrings(tier.Features), Description: tier.Description,
		})
	}
	return listing
}

func listingKindFromSDS(kind string) ListingKind {
	switch strings.TrimSpace(kind) {
	case "WasmModule":
		return ListingKindWASMModule
	case "Service":
		return ListingKindService
	default:
		return ListingKindDataStream
	}
}

func listingKindSDS(kind ListingKind) string {
	switch kind {
	case ListingKindWASMModule:
		return "WasmModule"
	case ListingKindService:
		return "Service"
	default:
		return "DataStream"
	}
}

func accessTypeFromSDS(access string) AccessType {
	switch strings.TrimSpace(access) {
	case "Subscription":
		return AccessTypeSubscription
	case "Streaming":
		return AccessTypeStreaming
	case "Query":
		return AccessTypeQuery
	default:
		return AccessTypeOneTime
	}
}

func accessTypeSDS(access AccessType) string {
	switch access {
	case AccessTypeSubscription:
		return "Subscription"
	case AccessTypeStreaming:
		return "Streaming"
	case AccessTypeQuery:
		return "Query"
	default:
		return "OneTime"
	}
}

func paymentMethodsFromSDS(methods []string) []PaymentMethod {
	result := make([]PaymentMethod, 0, len(methods))
	for _, method := range methods {
		value := sdsstf.EnumValuespaymentMethod[strings.TrimSpace(method)]
		result = append(result, PaymentMethod(int(value)))
	}
	return result
}

func paymentMethodsSDS(methods []PaymentMethod) []string {
	result := make([]string, 0, len(methods))
	for _, method := range methods {
		result = append(result, paymentMethodName(method))
	}
	return result
}

func paymentMethodName(method PaymentMethod) string {
	names := []string{"Crypto_ETH", "Crypto_SOL", "Crypto_BTC", "SDN_Credits", "Fiat_Stripe", "Free", "UsageBased", "Enterprise"}
	if int(method) < 0 || int(method) >= len(names) {
		return "Crypto_ETH"
	}
	return names[int(method)]
}

func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func canonicalDocumentForListing(listing *Listing) canonicalSTFDocument {
	pricing := make([]ListingPricingDraft, 0, len(listing.Pricing))
	for _, tier := range listing.Pricing {
		pricing = append(pricing, ListingPricingDraft{
			Name: tier.Name, PriceAmount: tier.PriceAmount, PriceCurrency: tier.PriceCurrency,
			DurationDays: tier.DurationDays, RateLimit: tier.RateLimit,
			Features: jsonStrings(tier.Features), MaxRecordsPerRequest: tier.MaxRecordsPerRequest,
			Description: tier.Description,
		})
	}
	return canonicalSTFDocument{
		ListingID: listing.ListingID, ProviderPeerID: listing.ProviderPeerID, ProviderEPMCID: listing.ProviderEPMCID,
		Title: listing.Title, Description: listing.Description, DataTypes: jsonStrings(listing.DataTypes),
		Coverage: ListingCoverageDraft{
			Spatial: ListingSpatialCoverageDraft{
				Type: listing.Coverage.Spatial.Type, Regions: jsonStrings(listing.Coverage.Spatial.Regions),
				ObjectIDs:     jsonStrings(listing.Coverage.Spatial.ObjectIDs),
				MinAltitudeKM: listing.Coverage.Spatial.MinAltitudeKm, MaxAltitudeKM: listing.Coverage.Spatial.MaxAltitudeKm,
				GeoBounds: jsonFloat64s(listing.Coverage.Spatial.GeoBounds),
			},
			Temporal: ListingTemporalCoverageDraft{
				StartEpoch: listing.Coverage.Temporal.StartEpoch, EndEpoch: listing.Coverage.Temporal.EndEpoch,
				UpdateFrequency: listing.Coverage.Temporal.UpdateFrequency,
				HistoricalDepth: listing.Coverage.Temporal.HistoricalDepthDays, LatencySeconds: listing.Coverage.Temporal.LatencySeconds,
			},
		},
		SampleCID: listing.SampleCID, AccessType: accessTypeSDS(listing.AccessType),
		EncryptionRequired: listing.EncryptionRequired, Pricing: pricing,
		AcceptedPayments: paymentMethodsSDS(listing.AcceptedPayments), CreatedAt: unixSeconds(listing.CreatedAt),
		UpdatedAt: unixSeconds(listing.UpdatedAt), Active: listing.Active, ListingKind: listingKindSDS(listing.ListingKind),
		Tags: jsonStrings(listing.Tags), SampleRecordCount: listing.SampleRecordCount,
		DeliveryMethods: jsonStrings(listing.DeliveryMethods), Version: listing.Version,
		ExpiresAt: unixSeconds(listing.ExpiresAt), TermsCID: listing.TermsCID, License: listing.License,
		SourcePeerID: listing.SourcePeerID, PrimaryCategory: listing.PrimaryCategory,
		Categories: jsonStrings(listing.Categories),
	}
}

func jsonStrings(values []string) []string {
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func jsonFloat64s(values []float64) []float64 {
	result := make([]float64, len(values))
	copy(result, values)
	return result
}

func buildSignedListingArtifacts(listing *Listing, key ed25519.PrivateKey, moduleCategory func(string) string) ([]byte, []byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, nil, errors.New("ed25519 node signing key is required")
	}
	listing.Signature = nil
	unsignedSTF := encodeListingRecord(listing, moduleCategory)
	listing.Signature = ed25519.Sign(key, unsignedSTF)
	signedSTF := encodeListingRecord(listing, moduleCategory)

	document := canonicalDocumentForListing(listing)
	unsignedJSON, err := json.Marshal(document)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal canonical STF JSON: %w", err)
	}
	jsonSignature := ed25519.Sign(key, unsignedJSON)
	document.Signature = jsonSignature
	signedJSON, err := json.Marshal(document)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal signed canonical STF JSON: %w", err)
	}
	return signedSTF, signedJSON, jsonSignature, nil
}

// VerifySTFBytes verifies the embedded FlatBuffer-form signature by rebuilding
// the same unsigned $STF from the bytes themselves.
func VerifySTFBytes(stfBytes []byte, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("ed25519 public key is required")
	}
	listing, err := decodeListingRecord(stfBytes)
	if err != nil {
		return err
	}
	signature := append([]byte(nil), listing.Signature...)
	listing.Signature = nil
	unsigned := encodeListingRecord(listing, func(string) string { return listing.PrimaryCategory })
	if !ed25519.Verify(publicKey, unsigned, signature) {
		return errors.New("invalid STF FlatBuffer signature")
	}
	return nil
}

// VerifyCanonicalSTFJSON verifies a self-contained canonical JSON rendering by
// removing its embedded SIGNATURE and reproducing the IDL-order JSON bytes.
func VerifyCanonicalSTFJSON(documentBytes []byte, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("ed25519 public key is required")
	}
	var document canonicalSTFDocument
	if err := json.Unmarshal(documentBytes, &document); err != nil {
		return fmt.Errorf("decode canonical STF JSON: %w", err)
	}
	canonicalSigned, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("rebuild signed canonical STF JSON: %w", err)
	}
	if !bytes.Equal(documentBytes, canonicalSigned) {
		return errors.New("STF JSON is not the canonical signed representation")
	}
	signature := append([]byte(nil), document.Signature...)
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("canonical STF JSON signature length = %d", len(signature))
	}
	document.Signature = nil
	unsigned, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("rebuild canonical STF JSON: %w", err)
	}
	if !ed25519.Verify(publicKey, unsigned, signature) {
		return errors.New("invalid STF canonical JSON signature")
	}
	return nil
}

func (s *Service) SetProviderEPMCID(cid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerEPMCID = strings.TrimSpace(cid)
}

func (s *Service) resolveProviderEPMCID() (string, error) {
	s.mu.RLock()
	cid := s.providerEPMCID
	s.mu.RUnlock()
	if cid != "" {
		return cid, nil
	}
	epmBytes, err := s.store.flatStore.LoadLocalEPM(s.peerID)
	if err != nil {
		return "", fmt.Errorf("resolve node EPM: %w", err)
	}
	if len(epmBytes) == 0 {
		return "", errors.New("resolve node EPM: empty EPM record")
	}
	return storage.ComputeCID(epmBytes), nil
}

// PublishListingDraft performs the durable part first and returns propagation
// failure explicitly without rolling back the locally useful listing.
func (s *Service) PublishListingDraft(ctx context.Context, draft ListingDraft) (*ListingPropagationReport, error) {
	if err := draft.validate(); err != nil {
		return nil, err
	}
	epmCID, err := s.resolveProviderEPMCID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	listing := listingFromDraft(draft, s.peerID, epmCID, now)

	s.store.mu.RLock()
	moduleCategory := s.store.moduleCategory
	s.store.mu.RUnlock()
	if listing.ListingKind == ListingKindWASMModule {
		listing.PrimaryCategory = listingPrimaryCategory(listing, moduleCategory)
		if listing.PrimaryCategory == "UNSPECIFIED" {
			listing.Categories = nil
		} else if !stringSliceContains(listing.Categories, listing.PrimaryCategory) {
			listing.Categories = append(listing.Categories, listing.PrimaryCategory)
		}
	}
	stfBytes, canonicalJSON, canonicalJSONSignature, err := buildSignedListingArtifacts(listing, s.signingKey, moduleCategory)
	if err != nil {
		return nil, err
	}
	stfCID, err := s.store.StorePublishedListing(listing, stfBytes, canonicalJSON, canonicalJSONSignature)
	if err != nil {
		return nil, fmt.Errorf("store signed STF: %w", err)
	}

	fileID := listingPublicationFileID(s.peerID, listing.ListingID, listing.UpdatedAt)
	var dpmBytes []byte
	var dpmCID string
	if listing.ListingKind == ListingKindDataStream && listing.AccessType == AccessTypeOneTime {
		dpmBytes, err = s.buildListingDPM(listing, fileID)
		if err != nil {
			return nil, fmt.Errorf("build OneTime dataset DPM: %w", err)
		}
		dpmRecord := sdsdpm.GetRootAsDPM(dpmBytes, 0)
		dpmCID, err = s.store.flatStore.StoreRoutedByProducer(SchemaDPM, dpmBytes, s.peerID, dpmRecord.PROVIDER_SIGNATUREBytes())
		if err != nil {
			return nil, fmt.Errorf("store signed DPM: %w", err)
		}
	}

	pnmBytes, err := buildListingPNM(stfCID, dpmCID, listing.ListingID, fileID, now, s.signingKey)
	if err != nil {
		return nil, err
	}
	pnmCID, err := s.store.flatStore.StoreRoutedByProducer(SchemaPNM, pnmBytes, s.peerID, nil)
	if err != nil {
		return nil, fmt.Errorf("store signed PNM: %w", err)
	}
	if err := s.store.StorePublicationArtifacts(listing.ListingID, pnmCID, pnmBytes, dpmCID, dpmBytes); err != nil {
		return nil, err
	}

	report := &ListingPropagationReport{ListingID: listing.ListingID, STFCID: stfCID, PNMCID: pnmCID, DPMCID: dpmCID}
	if s.listingTopic == nil {
		report.PropagationError = "storefront listing pubsub is disabled"
		return report, nil
	}
	peerCount := len(s.listingTopic.ListPeers())
	if err := s.listingTopic.Publish(ctx, pnmBytes); err != nil {
		report.PropagationError = "publish PNM: " + err.Error()
		return report, nil
	}
	report.AnnouncedToPeers = peerCount
	// The PNM remains the announcement. Sending its referenced immutable bytes
	// on the same topic gives a subscriber an immediate CID-resolving path while
	// normal content routing converges.
	if err := s.listingTopic.Publish(ctx, stfBytes); err != nil {
		report.PropagationError = "publish referenced STF bytes: " + err.Error()
		return report, nil
	}
	if len(dpmBytes) > 0 {
		if err := s.listingTopic.Publish(ctx, dpmBytes); err != nil {
			report.PropagationError = "publish referenced DPM bytes: " + err.Error()
		}
	}
	return report, nil
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func listingPublicationFileID(peerID, listingID string, updatedAt time.Time) string {
	return fmt.Sprintf("%s:listing:%s:%d", providerSlug(peerID), listingID, updatedAt.Unix())
}

func providerSlug(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func buildListingPNM(stfCID, dpmCID, listingID, fileID string, publishedAt time.Time, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("ed25519 node signing key is required for PNM")
	}
	timestamp := publishedAt.UTC().Format(time.RFC3339Nano)
	builder := flatbuffers.NewBuilder(512)
	addressValue := "/ipfs/" + stfCID
	if dpmCID != "" {
		addressValue += "?dpm=" + dpmCID
	}
	address := builder.CreateString(addressValue)
	timestampOffset := builder.CreateString(timestamp)
	cidOffset := builder.CreateString(stfCID)
	fileName := builder.CreateString(listingID + ".stf")
	fileIDOffset := builder.CreateString(fileID)
	signature := builder.CreateString(hex.EncodeToString(ed25519.Sign(key, []byte(stfCID))))
	timestampSignature := builder.CreateString(hex.EncodeToString(ed25519.Sign(key, []byte(timestamp))))
	signatureType := builder.CreateString("Ed25519")
	timestampSignatureType := builder.CreateString("Ed25519")
	sdspnm.PNMStart(builder)
	sdspnm.PNMAddMULTIFORMAT_ADDRESS(builder, address)
	sdspnm.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	sdspnm.PNMAddCID(builder, cidOffset)
	sdspnm.PNMAddFILE_NAME(builder, fileName)
	sdspnm.PNMAddFILE_ID(builder, fileIDOffset)
	sdspnm.PNMAddSIGNATURE(builder, signature)
	sdspnm.PNMAddTIMESTAMP_SIGNATURE(builder, timestampSignature)
	sdspnm.PNMAddSIGNATURE_TYPE(builder, signatureType)
	sdspnm.PNMAddTIMESTAMP_SIGNATURE_TYPE(builder, timestampSignatureType)
	root := sdspnm.PNMEnd(builder)
	sdspnm.FinishSizePrefixedPNMBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func VerifyListingPNM(pnmBytes []byte, publicKey ed25519.PublicKey) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("malformed PNM FlatBuffer: %v", recovered)
		}
	}()
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("ed25519 public key is required")
	}
	if !sdspnm.SizePrefixedPNMBufferHasIdentifier(pnmBytes) {
		return errors.New("PNM buffer missing $PNM file identifier")
	}
	pnm := sdspnm.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	if string(pnm.SIGNATURE_TYPE()) != "Ed25519" || string(pnm.TIMESTAMP_SIGNATURE_TYPE()) != "Ed25519" {
		return errors.New("PNM signature types must both be Ed25519")
	}
	if len(pnm.CID()) == 0 || len(pnm.PUBLISH_TIMESTAMP()) == 0 {
		return errors.New("PNM CID and publish timestamp are required")
	}
	signature, err := hex.DecodeString(string(pnm.SIGNATURE()))
	if err != nil || !ed25519.Verify(publicKey, pnm.CID(), signature) {
		return errors.New("invalid PNM CID signature")
	}
	timestampSignature, err := hex.DecodeString(string(pnm.TIMESTAMP_SIGNATURE()))
	if err != nil || !ed25519.Verify(publicKey, pnm.PUBLISH_TIMESTAMP(), timestampSignature) {
		return errors.New("invalid PNM timestamp signature")
	}
	return nil
}

type listingDatasetRecord struct {
	Schema string
	CID    string
	Data   []byte
}

func (s *Service) buildListingDPM(listing *Listing, fileID string) ([]byte, error) {
	if len(s.signingKey) != ed25519.PrivateKeySize {
		return nil, errors.New("ed25519 node signing key is required for DPM")
	}
	schemaNames := make([]string, 0, len(listing.DataTypes))
	seen := make(map[string]bool)
	for _, dataType := range listing.DataTypes {
		schema := strings.TrimPrefix(strings.TrimSpace(dataType), "$")
		if !strings.HasSuffix(schema, ".fbs") {
			schema += ".fbs"
		}
		if err := sds.ValidateSchemaName(schema); err != nil {
			return nil, fmt.Errorf("DATA_TYPES member %q: %w", dataType, err)
		}
		if !seen[schema] {
			seen[schema] = true
			schemaNames = append(schemaNames, schema)
		}
	}
	if len(schemaNames) == 0 {
		return nil, errors.New("OneTime dataset listing has no stored SDS DATA_TYPES")
	}
	sort.Strings(schemaNames)
	var records []listingDatasetRecord
	for _, schema := range schemaNames {
		for offset := 0; ; {
			stored, err := s.store.flatStore.QueryIndexedRecords(storage.IndexedRecordQuery{
				SchemaName: schema, AllowLargeResultSet: true, OrderByCID: true,
				Limit: 250000, Offset: offset,
			})
			if err != nil {
				return nil, fmt.Errorf("query %s records: %w", schema, err)
			}
			for _, record := range stored {
				records = append(records, listingDatasetRecord{Schema: schema, CID: record.CID, Data: append([]byte(nil), record.Data...)})
			}
			if len(stored) < 250000 {
				break
			}
			offset += len(stored)
		}
	}
	if len(records) == 0 {
		return nil, errors.New("OneTime dataset listing query matched no stored records")
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Schema == records[j].Schema {
			return records[i].CID < records[j].CID
		}
		return records[i].Schema < records[j].Schema
	})

	queryDocument := struct {
		SchemaNames []string `json:"schema_names"`
		Order       string   `json:"order"`
	}{SchemaNames: schemaNames, Order: "schema_name ASC, cid ASC"}
	canonicalQuery, _ := json.Marshal(queryDocument)
	queryHash := sha256.Sum256(canonicalQuery)
	resultBytes := listingDatasetResultBytes(records)
	resultHash := sha256.Sum256(resultBytes)
	dataRoot := ComputeListingMerkleRoot(recordData(records))
	fileIDRoot := ComputeListingFileIDMerkleRoot(fileID, recordData(records))
	resultCID := storage.ComputeCID(resultBytes)
	queryCID := storage.ComputeCID(canonicalQuery)

	build := func(signature []byte) []byte {
		return buildListingDPMBytes(listing, fileID, schemaNames, canonicalQuery,
			hex.EncodeToString(queryHash[:]), hex.EncodeToString(resultHash[:]),
			resultCID, uint64(len(resultBytes)), queryCID, dataRoot, fileIDRoot, signature)
	}
	unsigned := build(nil)
	digest := sha256.Sum256(unsigned)
	return build(ed25519.Sign(s.signingKey, digest[:])), nil
}

func listingDatasetResultBytes(records []listingDatasetRecord) []byte {
	var result bytes.Buffer
	for _, record := range records {
		_ = binary.Write(&result, binary.BigEndian, uint32(len(record.Schema)))
		result.WriteString(record.Schema)
		_ = binary.Write(&result, binary.BigEndian, uint32(len(record.Data)))
		result.Write(record.Data)
	}
	return result.Bytes()
}

func recordData(records []listingDatasetRecord) [][]byte {
	result := make([][]byte, 0, len(records))
	for _, record := range records {
		result = append(result, record.Data)
	}
	return result
}

// ComputeListingMerkleRoot implements SDN-MERKLE-SHA256-v1: leaves are
// SHA-256(0x00 || canonical-record-bytes), internal nodes are
// SHA-256(0x01 || left || right), and an odd node is promoted unchanged.
func ComputeListingMerkleRoot(records [][]byte) string {
	if len(records) == 0 {
		return hex.EncodeToString(sha256.New().Sum(nil))
	}
	level := make([][sha256.Size]byte, 0, len(records))
	for _, record := range records {
		leaf := make([]byte, 1, len(record)+1)
		leaf[0] = 0
		leaf = append(leaf, record...)
		level = append(level, sha256.Sum256(leaf))
	}
	for len(level) > 1 {
		next := make([][sha256.Size]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				next = append(next, level[i])
				continue
			}
			material := make([]byte, 1, 1+sha256.Size*2)
			material[0] = 1
			material = append(material, level[i][:]...)
			material = append(material, level[i+1][:]...)
			next = append(next, sha256.Sum256(material))
		}
		level = next
	}
	return hex.EncodeToString(level[0][:])
}

func ComputeListingFileIDMerkleRoot(fileID string, records [][]byte) string {
	bound := make([][]byte, 0, len(records))
	for _, record := range records {
		material := make([]byte, 0, len(fileID)+1+len(record))
		material = append(material, fileID...)
		material = append(material, 0)
		material = append(material, record...)
		bound = append(bound, material)
	}
	return ComputeListingMerkleRoot(bound)
}

// VerifyListingDPM verifies the provider signature using the same canonical
// unsigned FlatBuffer reconstruction used by the listing publisher.
func VerifyListingDPM(dpmBytes []byte, publicKey ed25519.PublicKey) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("malformed DPM FlatBuffer: %v", recovered)
		}
	}()
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("ed25519 public key is required")
	}
	if !sdsdpm.DPMBufferHasIdentifier(dpmBytes) {
		return errors.New("DPM buffer missing $DPM file identifier")
	}
	dpm := sdsdpm.GetRootAsDPM(dpmBytes, 0)
	if string(dpm.SIGNATURE_TYPE()) != "Ed25519" {
		return fmt.Errorf("DPM SIGNATURE_TYPE = %q", dpm.SIGNATURE_TYPE())
	}
	query := dpm.QUERY(nil)
	if query == nil {
		return errors.New("DPM missing QUERY")
	}
	queryDigest := sha256.Sum256(query.CANONICAL_QUERY())
	if string(query.QUERY_SHA256()) != hex.EncodeToString(queryDigest[:]) {
		return errors.New("DPM canonical query hash does not match CANONICAL_QUERY")
	}
	schemaNames := make([]string, 0, query.SCHEMA_NAMESLength())
	for i := 0; i < query.SCHEMA_NAMESLength(); i++ {
		schemaNames = append(schemaNames, string(query.SCHEMA_NAMES(i)))
	}
	var resultCID, queryCID, resultSHA, dataRoot, recordRoot, fileIDRoot string
	var resultLength uint64
	for i := 0; i < dpm.ASSETSLength(); i++ {
		var asset sdsdpm.DPMAsset
		if !dpm.ASSETS(&asset, i) {
			continue
		}
		switch asset.ASSET_KIND().String() {
		case "DATA_SHARD":
			resultCID = string(asset.CID())
			resultLength = asset.BYTE_LENGTH()
			resultSHA = string(asset.BYTE_SHA256())
			dataRoot = string(asset.DATA_ROOT())
		case "QUERY_INDEX":
			queryCID = string(asset.CID())
		}
	}
	for i := 0; i < dpm.INDEXESLength(); i++ {
		var index sdsdpm.DPMCompletenessIndex
		if !dpm.INDEXES(&index, i) {
			continue
		}
		if string(index.MERKLE_PROFILE()) != ListingMerkleProfile {
			return fmt.Errorf("DPM index %q uses unsupported Merkle profile %q", index.INDEX_NAME(), index.MERKLE_PROFILE())
		}
		switch string(index.INDEX_NAME()) {
		case "record_cid":
			recordRoot = string(index.INDEX_ROOT())
		case "file_id":
			fileIDRoot = string(index.INDEX_ROOT())
		}
	}
	if resultCID == "" || queryCID == "" || dataRoot == "" || recordRoot == "" || fileIDRoot == "" {
		return errors.New("DPM missing listing dataset assets")
	}
	if recordRoot != dataRoot {
		return errors.New("DPM record_cid index root does not match DATA_ROOT")
	}
	if resultSHA != string(query.RESULT_SHA256()) {
		return errors.New("DPM DATA_SHARD hash does not match QUERY.RESULT_SHA256")
	}
	publishedAt, err := time.Parse(time.RFC3339Nano, string(dpm.PUBLISH_TIMESTAMP()))
	if err != nil {
		return fmt.Errorf("parse DPM PUBLISH_TIMESTAMP: %w", err)
	}
	listing := &Listing{
		ListingID: string(dpm.DATASET_ID()), ProviderPeerID: string(dpm.PROVIDER_PEER_ID()),
		ProviderEPMCID: string(dpm.PROVIDER_EPM_CID()), UpdatedAt: publishedAt,
	}
	unsigned := buildListingDPMBytes(listing, string(dpm.FILE_ID()), schemaNames,
		append([]byte(nil), query.CANONICAL_QUERY()...), string(query.QUERY_SHA256()),
		string(query.RESULT_SHA256()), resultCID, resultLength, queryCID, dataRoot, fileIDRoot, nil)
	digest := sha256.Sum256(unsigned)
	if !ed25519.Verify(publicKey, digest[:], dpm.PROVIDER_SIGNATUREBytes()) {
		return errors.New("invalid DPM provider signature")
	}
	return nil
}

func buildListingDPMBytes(listing *Listing, fileID string, schemaNames []string, canonicalQuery []byte, queryHash, resultHash, resultCID string, resultLength uint64, queryCID, dataRoot, fileIDRoot string, signature []byte) []byte {
	builder := flatbuffers.NewBuilder(2048)
	version := builder.CreateString("1.0.0")
	datasetID := builder.CreateString(listing.ListingID)
	updateID := builder.CreateString(fmt.Sprintf("%d", listing.UpdatedAt.Unix()))
	fileIDOffset := builder.CreateString(fileID)
	providerPeerID := builder.CreateString(listing.ProviderPeerID)
	providerEPMCID := builder.CreateString(listing.ProviderEPMCID)
	publishedAt := builder.CreateString(listing.UpdatedAt.UTC().Format(time.RFC3339Nano))

	assetCID := builder.CreateString(resultCID)
	assetAddress := builder.CreateString("/ipfs/" + resultCID)
	assetName := builder.CreateString(listing.ListingID + ".dataset")
	assetFileID := builder.CreateString(fileID)
	assetProtocol := builder.CreateString("FlatSQL canonical record set v1")
	assetSHA := builder.CreateString(resultHash)
	assetRoot := builder.CreateString(dataRoot)
	assetSchema := builder.CreateString(strings.Join(schemaNames, ","))
	sdsdpm.DPMAssetStart(builder)
	sdsdpm.DPMAssetAddASSET_KIND(builder, 0)
	sdsdpm.DPMAssetAddTRANSPORT_KIND(builder, 0)
	sdsdpm.DPMAssetAddCID(builder, assetCID)
	sdsdpm.DPMAssetAddMULTIFORMAT_ADDRESS(builder, assetAddress)
	sdsdpm.DPMAssetAddFILE_NAME(builder, assetName)
	sdsdpm.DPMAssetAddFILE_ID(builder, assetFileID)
	sdsdpm.DPMAssetAddTRANSPORT_PROTOCOL(builder, assetProtocol)
	sdsdpm.DPMAssetAddBYTE_LENGTH(builder, resultLength)
	sdsdpm.DPMAssetAddBYTE_SHA256(builder, assetSHA)
	sdsdpm.DPMAssetAddDATA_ROOT(builder, assetRoot)
	sdsdpm.DPMAssetAddSCHEMA_NAME(builder, assetSchema)
	asset := sdsdpm.DPMAssetEnd(builder)
	queryAssetCID := builder.CreateString(queryCID)
	queryAssetAddress := builder.CreateString("/ipfs/" + queryCID)
	queryAssetName := builder.CreateString(listing.ListingID + ".query.json")
	queryAssetFileID := builder.CreateString(fileID)
	queryAssetProtocol := builder.CreateString("canonical-json")
	queryAssetSHA := builder.CreateString(queryHash)
	queryAssetSchema := builder.CreateString("DPM.index.json")
	sdsdpm.DPMAssetStart(builder)
	sdsdpm.DPMAssetAddASSET_KIND(builder, 1)
	sdsdpm.DPMAssetAddTRANSPORT_KIND(builder, 0)
	sdsdpm.DPMAssetAddCID(builder, queryAssetCID)
	sdsdpm.DPMAssetAddMULTIFORMAT_ADDRESS(builder, queryAssetAddress)
	sdsdpm.DPMAssetAddFILE_NAME(builder, queryAssetName)
	sdsdpm.DPMAssetAddFILE_ID(builder, queryAssetFileID)
	sdsdpm.DPMAssetAddTRANSPORT_PROTOCOL(builder, queryAssetProtocol)
	sdsdpm.DPMAssetAddBYTE_LENGTH(builder, uint64(len(canonicalQuery)))
	sdsdpm.DPMAssetAddBYTE_SHA256(builder, queryAssetSHA)
	sdsdpm.DPMAssetAddSCHEMA_NAME(builder, queryAssetSchema)
	queryAsset := sdsdpm.DPMAssetEnd(builder)
	sdsdpm.DPMStartASSETSVector(builder, 2)
	builder.PrependUOffsetT(queryAsset)
	builder.PrependUOffsetT(asset)
	assets := builder.EndVector(2)

	canonicalQueryOffset := builder.CreateString(string(canonicalQuery))
	queryHashOffset := builder.CreateString(queryHash)
	resultHashOffset := builder.CreateString(resultHash)
	queryEngine := builder.CreateString("FlatSQL")
	queryEngineVersion := builder.CreateString("sdn-listing-v1")
	canonicalOrder := builder.CreateString("schema_name ASC, cid ASC")
	queryProtocol := builder.CreateString("SDN-LISTING-DATASET-v1")
	schemaVector := listingStringVector(builder, schemaNames, sdsdpm.DPMQueryBindingStartSCHEMA_NAMESVector)
	sdsdpm.DPMQueryBindingStart(builder)
	sdsdpm.DPMQueryBindingAddCANONICAL_QUERY(builder, canonicalQueryOffset)
	sdsdpm.DPMQueryBindingAddQUERY_SHA256(builder, queryHashOffset)
	sdsdpm.DPMQueryBindingAddRESULT_SHA256(builder, resultHashOffset)
	sdsdpm.DPMQueryBindingAddQUERY_ENGINE(builder, queryEngine)
	sdsdpm.DPMQueryBindingAddQUERY_ENGINE_VERSION(builder, queryEngineVersion)
	sdsdpm.DPMQueryBindingAddCANONICAL_ORDER(builder, canonicalOrder)
	sdsdpm.DPMQueryBindingAddQUERY_PROTOCOL(builder, queryProtocol)
	sdsdpm.DPMQueryBindingAddSCHEMA_NAMES(builder, schemaVector)
	query := sdsdpm.DPMQueryBindingEnd(builder)

	recordIndex := buildListingCompletenessIndex(builder, "record_cid", "schema_name ASC, cid ASC", dataRoot)
	fileIDIndex := buildListingCompletenessIndex(builder, "file_id", "file_id ASC, schema_name ASC, cid ASC", fileIDRoot)
	sdsdpm.DPMStartINDEXESVector(builder, 2)
	builder.PrependUOffsetT(fileIDIndex)
	builder.PrependUOffsetT(recordIndex)
	indexes := builder.EndVector(2)

	var signatureOffset flatbuffers.UOffsetT
	var signatureType flatbuffers.UOffsetT
	if len(signature) > 0 {
		signatureOffset = builder.CreateByteVector(signature)
		signatureType = builder.CreateString("Ed25519")
	}
	sdsdpm.DPMStart(builder)
	sdsdpm.DPMAddVERSION(builder, version)
	sdsdpm.DPMAddDATASET_ID(builder, datasetID)
	sdsdpm.DPMAddUPDATE_ID(builder, updateID)
	sdsdpm.DPMAddFILE_ID(builder, fileIDOffset)
	sdsdpm.DPMAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdsdpm.DPMAddPROVIDER_EPM_CID(builder, providerEPMCID)
	sdsdpm.DPMAddPUBLISH_TIMESTAMP(builder, publishedAt)
	sdsdpm.DPMAddASSETS(builder, assets)
	sdsdpm.DPMAddQUERY(builder, query)
	sdsdpm.DPMAddINDEXES(builder, indexes)
	if signatureOffset != 0 {
		sdsdpm.DPMAddPROVIDER_SIGNATURE(builder, signatureOffset)
		sdsdpm.DPMAddSIGNATURE_TYPE(builder, signatureType)
	}
	root := sdsdpm.DPMEnd(builder)
	sdsdpm.FinishDPMBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func buildListingCompletenessIndex(builder *flatbuffers.Builder, name, order, root string) flatbuffers.UOffsetT {
	indexName := builder.CreateString(name)
	indexOrder := builder.CreateString(order)
	indexRoot := builder.CreateString(root)
	merkleProfile := builder.CreateString(ListingMerkleProfile)
	sdsdpm.DPMCompletenessIndexStart(builder)
	sdsdpm.DPMCompletenessIndexAddINDEX_NAME(builder, indexName)
	sdsdpm.DPMCompletenessIndexAddCANONICAL_ORDER(builder, indexOrder)
	sdsdpm.DPMCompletenessIndexAddINDEX_ROOT(builder, indexRoot)
	sdsdpm.DPMCompletenessIndexAddMERKLE_PROFILE(builder, merkleProfile)
	sdsdpm.DPMCompletenessIndexAddSUPPORTS_RANGE_COMPLETENESS(builder, true)
	return sdsdpm.DPMCompletenessIndexEnd(builder)
}

func listingStringVector(builder *flatbuffers.Builder, values []string, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	offsets := make([]flatbuffers.UOffsetT, 0, len(values))
	for _, value := range values {
		offsets = append(offsets, builder.CreateString(value))
	}
	start(builder, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	return builder.EndVector(len(offsets))
}
