package license

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdsent "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ENT"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	defaultPlan                      = "free"
	defaultStatus                    = entitlementStatusActive
	entitlementSchema                = "ENT.fbs"
	defaultEntitlementProviderPeerID = "sdn-license-entitlements"
	entitlementLoadPageSize          = 1000
)

// EntitlementStore persists current xpub subscription state as SDS ENT records
// in the node's FlatSQL store. The in-memory map is a rebuildable projection of
// the immutable ENT record stream.
type EntitlementStore struct {
	flatStore      *storage.FlatSQLStore
	providerPeerID string

	mu           sync.RWMutex
	entitlements map[string]*Entitlement
}

// NewEntitlementStore builds the entitlement projection from FlatSQL ENT records.
func NewEntitlementStore(flatStore *storage.FlatSQLStore, providerPeerID string) (*EntitlementStore, error) {
	if flatStore == nil {
		return nil, errors.New("FlatSQL store is required")
	}
	providerPeerID = strings.TrimSpace(providerPeerID)
	if providerPeerID == "" {
		providerPeerID = defaultEntitlementProviderPeerID
	}

	store := &EntitlementStore{
		flatStore:      flatStore,
		providerPeerID: providerPeerID,
		entitlements:   make(map[string]*Entitlement),
	}
	if err := store.loadProjection(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *EntitlementStore) loadProjection() error {
	var afterRowID int64
	for {
		records, err := s.flatStore.QueryRawRecords(storage.RawRecordQuery{
			SchemaName:     entitlementSchema,
			Limit:          entitlementLoadPageSize,
			UseRowIDCursor: true,
			AfterRowID:     afterRowID,
		})
		if err != nil {
			return fmt.Errorf("load entitlement records: %w", err)
		}
		if len(records) == 0 {
			return nil
		}

		s.mu.Lock()
		for _, record := range records {
			if record == nil {
				continue
			}
			if record.RowID > afterRowID {
				afterRowID = record.RowID
			}
			ent, err := decodeEntitlementRecord(record.Data)
			if err != nil {
				s.mu.Unlock()
				return fmt.Errorf("decode entitlement record %s: %w", record.CID, err)
			}
			if ent.UpdatedAt <= 0 && !record.Timestamp.IsZero() {
				ent.UpdatedAt = record.Timestamp.Unix()
			}
			s.mergeEntitlementLocked(ent)
		}
		s.mu.Unlock()

		if len(records) < entitlementLoadPageSize {
			return nil
		}
	}
}

func (s *EntitlementStore) mergeEntitlementLocked(ent *Entitlement) {
	ent = normalizeEntitlement(ent)
	if ent == nil || ent.XPub == "" {
		return
	}
	current := s.entitlements[ent.XPub]
	if current == nil || ent.UpdatedAt >= current.UpdatedAt {
		s.entitlements[ent.XPub] = cloneEntitlement(ent)
	}
}

// Close is kept for callers that own entitlement stores. FlatSQL lifecycle is
// owned by the node-level storage layer.
func (s *EntitlementStore) Close() error {
	return nil
}

// GetEntitlement returns entitlement for xpub.
func (s *EntitlementStore) GetEntitlement(xpub string) (*Entitlement, error) {
	xpub = strings.TrimSpace(xpub)
	if xpub == "" {
		return nil, errors.New("xpub is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	ent := s.entitlements[xpub]
	if ent == nil {
		return nil, nil
	}
	return cloneEntitlement(ent), nil
}

// GetOrCreateEntitlement returns current entitlement or creates an active free plan.
func (s *EntitlementStore) GetOrCreateEntitlement(xpub, peerID string) (*Entitlement, error) {
	ent, err := s.GetEntitlement(xpub)
	if err != nil {
		return nil, err
	}
	if ent != nil {
		return ent, nil
	}

	now := time.Now().Unix()
	newEnt := &Entitlement{
		XPub:           strings.TrimSpace(xpub),
		PeerID:         strings.TrimSpace(peerID),
		Plan:           defaultPlan,
		Status:         defaultStatus,
		ExpiresAt:      0,
		CreatedAt:      now,
		UpdatedAt:      now,
		ProviderPeerID: s.providerPeerID,
	}
	if err := s.UpsertEntitlement(newEnt); err != nil {
		return nil, err
	}
	return cloneEntitlement(newEnt), nil
}

// UpsertEntitlement appends an authoritative ENT record and updates the
// rebuildable current-state projection.
func (s *EntitlementStore) UpsertEntitlement(ent *Entitlement) error {
	ent = normalizeEntitlement(ent)
	if ent == nil {
		return errors.New("entitlement is required")
	}
	if ent.XPub == "" {
		return errors.New("xpub is required")
	}

	s.mu.RLock()
	current := cloneEntitlement(s.entitlements[ent.XPub])
	s.mu.RUnlock()

	now := time.Now().Unix()
	if ent.CreatedAt <= 0 {
		switch {
		case current != nil && current.CreatedAt > 0:
			ent.CreatedAt = current.CreatedAt
		case current != nil && current.UpdatedAt > 0:
			ent.CreatedAt = current.UpdatedAt
		default:
			ent.CreatedAt = now
		}
	}
	ent.UpdatedAt = now
	if ent.EntitlementID == "" {
		ent.EntitlementID = entitlementRecordID(ent.XPub)
	}
	if ent.ProviderPeerID == "" {
		ent.ProviderPeerID = s.providerPeerID
	}

	recordBytes, err := encodeEntitlementRecord(ent)
	if err != nil {
		return err
	}
	if _, err := s.flatStore.Store(entitlementSchema, recordBytes, entitlementRecordPeerID(ent, s.providerPeerID), ent.ProviderSignature); err != nil {
		return fmt.Errorf("store entitlement record: %w", err)
	}

	s.mu.Lock()
	s.mergeEntitlementLocked(ent)
	s.mu.Unlock()
	return nil
}

func normalizeEntitlement(ent *Entitlement) *Entitlement {
	if ent == nil {
		return nil
	}
	ent.XPub = strings.TrimSpace(ent.XPub)
	ent.EntitlementID = strings.TrimSpace(ent.EntitlementID)
	ent.PeerID = strings.TrimSpace(ent.PeerID)
	ent.Plan = strings.TrimSpace(ent.Plan)
	if ent.Plan == "" {
		ent.Plan = defaultPlan
	}
	ent.Status = normalizeEntitlementStatus(ent.Status)
	if ent.Status == "" {
		ent.Status = defaultStatus
	}
	ent.StripeCustomerID = strings.TrimSpace(ent.StripeCustomerID)
	ent.StripeSubscriptionID = strings.TrimSpace(ent.StripeSubscriptionID)
	ent.ProviderPeerID = strings.TrimSpace(ent.ProviderPeerID)
	return ent
}

func cloneEntitlement(ent *Entitlement) *Entitlement {
	if ent == nil {
		return nil
	}
	copy := *ent
	if len(ent.ProviderSignature) > 0 {
		copy.ProviderSignature = append([]byte(nil), ent.ProviderSignature...)
	}
	return &copy
}

func entitlementRecordID(xpub string) string {
	return "entitlement:" + strings.TrimSpace(xpub)
}

func entitlementRecordPeerID(ent *Entitlement, fallback string) string {
	if ent != nil && strings.TrimSpace(ent.ProviderPeerID) != "" {
		return strings.TrimSpace(ent.ProviderPeerID)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	if ent != nil && strings.TrimSpace(ent.PeerID) != "" {
		return strings.TrimSpace(ent.PeerID)
	}
	return defaultEntitlementProviderPeerID
}

func normalizeEntitlementStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "active":
		return entitlementStatusActive
	case "cancelled", "canceled":
		return entitlementStatusCancelled
	case "past_due", "pastdue", "past-due":
		return entitlementStatusPastDue
	case "suspended":
		return entitlementStatusSuspended
	case "expired":
		return entitlementStatusExpired
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func encodeEntitlementRecord(ent *Entitlement) ([]byte, error) {
	ent = normalizeEntitlement(ent)
	if ent == nil {
		return nil, errors.New("entitlement is required")
	}
	if ent.XPub == "" {
		return nil, errors.New("xpub is required")
	}

	builder := flatbuffers.NewBuilder(512)

	entitlementID := stringOffset(builder, ent.EntitlementID)
	xpub := stringOffset(builder, ent.XPub)
	peerID := stringOffset(builder, ent.PeerID)
	plan := stringOffset(builder, ent.Plan)
	statusText := stringOffset(builder, ent.Status)
	stripeCustomerID := stringOffset(builder, ent.StripeCustomerID)
	stripeSubscriptionID := stringOffset(builder, ent.StripeSubscriptionID)
	providerPeerID := stringOffset(builder, ent.ProviderPeerID)
	providerSignature := fbByteVector(builder, ent.ProviderSignature, sdsent.ENTStartPROVIDER_SIGNATUREVector)

	sdsent.ENTStart(builder)
	sdsent.ENTAddENTITLEMENT_ID(builder, entitlementID)
	sdsent.ENTAddXPUB(builder, xpub)
	sdsent.ENTAddPEER_ID(builder, peerID)
	sdsent.ENTAddPLAN(builder, plan)
	addENTStatus(builder, ent.Status)
	sdsent.ENTAddSTATUS_TEXT(builder, statusText)
	sdsent.ENTAddSTRIPE_CUSTOMER_ID(builder, stripeCustomerID)
	sdsent.ENTAddSTRIPE_SUBSCRIPTION_ID(builder, stripeSubscriptionID)
	sdsent.ENTAddEXPIRES_AT(builder, int64ToUint64(ent.ExpiresAt))
	sdsent.ENTAddCREATED_AT(builder, int64ToUint64(ent.CreatedAt))
	sdsent.ENTAddUPDATED_AT(builder, int64ToUint64(ent.UpdatedAt))
	sdsent.ENTAddPROVIDER_PEER_ID(builder, providerPeerID)
	sdsent.ENTAddPROVIDER_SIGNATURE(builder, providerSignature)
	root := sdsent.ENTEnd(builder)
	sdsent.FinishENTBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

func decodeEntitlementRecord(data []byte) (*Entitlement, error) {
	if len(data) == 0 {
		return nil, errors.New("empty entitlement record")
	}
	if !sdsent.ENTBufferHasIdentifier(data) {
		return nil, errors.New("record is not ENT.fbs")
	}
	record := sdsent.GetRootAsENT(data, 0)
	ent := &Entitlement{
		EntitlementID:        string(record.ENTITLEMENT_ID()),
		XPub:                 string(record.XPUB()),
		PeerID:               string(record.PEER_ID()),
		Plan:                 string(record.PLAN()),
		Status:               entitlementStatusFromRecord(record),
		StripeCustomerID:     string(record.STRIPE_CUSTOMER_ID()),
		StripeSubscriptionID: string(record.STRIPE_SUBSCRIPTION_ID()),
		ExpiresAt:            uint64ToInt64(record.EXPIRES_AT()),
		CreatedAt:            uint64ToInt64(record.CREATED_AT()),
		UpdatedAt:            uint64ToInt64(record.UPDATED_AT()),
		ProviderPeerID:       string(record.PROVIDER_PEER_ID()),
		ProviderSignature:    append([]byte(nil), record.PROVIDER_SIGNATUREBytes()...),
	}
	return normalizeEntitlement(ent), nil
}

func entitlementStatusFromRecord(record *sdsent.ENT) string {
	if record == nil {
		return defaultStatus
	}
	statusText := normalizeEntitlementStatus(string(record.STATUS_TEXT()))
	if statusText != "" && statusText != defaultStatus {
		return statusText
	}
	switch record.STATUS().String() {
	case "Cancelled":
		return entitlementStatusCancelled
	case "PastDue":
		return entitlementStatusPastDue
	case "Suspended":
		return entitlementStatusSuspended
	case "Expired":
		return entitlementStatusExpired
	default:
		if statusText != "" {
			return statusText
		}
		return defaultStatus
	}
}

func addENTStatus(builder *flatbuffers.Builder, status string) {
	switch normalizeEntitlementStatus(status) {
	case entitlementStatusCancelled:
		sdsent.ENTAddSTATUS(builder, 1)
	case entitlementStatusPastDue:
		sdsent.ENTAddSTATUS(builder, 2)
	case entitlementStatusSuspended:
		sdsent.ENTAddSTATUS(builder, 3)
	case entitlementStatusExpired:
		sdsent.ENTAddSTATUS(builder, 4)
	default:
		sdsent.ENTAddSTATUS(builder, 0)
	}
}

func stringOffset(builder *flatbuffers.Builder, value string) flatbuffers.UOffsetT {
	if value == "" {
		return 0
	}
	return builder.CreateString(value)
}

func fbByteVector(builder *flatbuffers.Builder, data []byte, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(data) == 0 {
		return 0
	}
	start(builder, len(data))
	for i := len(data) - 1; i >= 0; i-- {
		builder.PrependByte(data[i])
	}
	return builder.EndVector(len(data))
}

func int64ToUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func uint64ToInt64(value uint64) int64 {
	if value > uint64(^uint64(0)>>1) {
		return 0
	}
	return int64(value)
}
